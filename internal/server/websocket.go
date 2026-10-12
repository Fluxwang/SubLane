package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/murongg/SubLane/internal/apikey"
	"github.com/murongg/SubLane/internal/gateway"
	"github.com/murongg/SubLane/internal/upstream"
)

func (h *keyHTTP) websocket(w http.ResponseWriter, r *http.Request) {
	if !websocket.IsWebSocketUpgrade(r) {
		w.Header().Set("Allow", "POST")
		writeGatewayError(w, 405, "method_not_allowed")
		return
	}
	if h.gateway == nil {
		gatewayUnavailable(w, r)
		return
	}
	select {
	case h.sockets <- struct{}{}:
		defer func() { <-h.sockets }()
	default:
		gatewayError(w, gateway.ErrBusy)
		return
	}
	upgrader := websocket.Upgrader{
		HandshakeTimeout: 5 * time.Second,
		CheckOrigin: func(request *http.Request) bool {
			origin := request.Header.Get("Origin")
			if origin == "" {
				return true
			}
			expected := h.publicURL
			if expected == "" {
				scheme := "http"
				if request.TLS != nil {
					scheme = "https"
				}
				expected = scheme + "://" + request.Host
			}
			return origin == expected
		},
		Error: func(w http.ResponseWriter, r *http.Request, status int, reason error) {
			writeGatewayError(w, status, "websocket_upgrade_failed")
		},
	}
	// Gorilla writes the handshake after hijacking, bypassing ordinary response headers.
	conn, err := upgrader.Upgrade(w, r, http.Header{"Cache-Control": {"no-store"}, "X-Request-ID": {gateway.RequestID(r.Context())}})
	if err != nil {
		return
	}
	if state, ok := r.Context().Value(requestLogKey{}).(*requestLogState); ok {
		state.upgraded = true
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	conn.SetReadLimit(h.gateway.MaxRequestBody())
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(5 * time.Minute)) })
	messages := make(chan []byte, 1)
	go func() {
		defer cancel()
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if kind != websocket.TextMessage && kind != websocket.BinaryMessage {
				continue
			}
			select {
			case messages <- data:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	write := func(data []byte) error {
		if err := conn.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil {
			return err
		}
		return conn.WriteMessage(websocket.TextMessage, data)
	}
	turnID := ""
	writeError := func(err error) error {
		status, code := gatewayFailure(err)
		raw, _ := json.Marshal(map[string]any{"type": "error", "status": status, "error": gatewayErrorBody(code)})
		return write(correlateEvent(raw, turnID))
	}
	_, secret, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	initial, _ := r.Context().Value(keyPrincipalKey{}).(apikey.Principal)
	headers := r.Header.Clone()
	if headers.Get("Session_id") == "" {
		headers.Set("Session_id", "ws-"+randomID())
	}
	conversation := gateway.Conversation{MaxRequestBody: h.gateway.MaxRequestBody()}
	for {
		select {
		case <-ctx.Done():
			return
		case message := <-messages:
			turnID = ""
			// A successful handshake does not authorize later turns after member suspension or key revocation.
			principal, err := h.service.Authenticate(ctx, strings.TrimSpace(secret))
			if err != nil || principal.UserID != initial.UserID || principal.KeyID != initial.KeyID || principal.GroupID != initial.GroupID {
				_ = writeError(apikey.ErrInvalidKey)
				return
			}
			normalized, prewarm, err := conversation.Normalize(message)
			if err != nil {
				if writeError(err) != nil {
					return
				}
				continue
			}
			if prewarm {
				ctxWithID := gateway.WithRequestIdentity(ctx, principal.KeyID, "websocket")
				turnID = gateway.RequestID(ctxWithID)
				var input struct {
					Model string `json:"model"`
				}
				if err := json.Unmarshal(normalized, &input); err != nil {
					if writeError(upstream.ErrInput) != nil {
						return
					}
					continue
				}
				if err := h.gateway.AuthorizeModel(ctx, principal.UserID, principal.GroupID, input.Model); err != nil {
					if writeError(err) != nil {
						return
					}
					continue
				}
				// Local prewarm also retains text for later turns, so it must obey the current rules.
				if err := h.gateway.CheckPrewarm(ctxWithID, principal.UserID, principal.GroupID, normalized); err != nil {
					if writeError(err) != nil {
						return
					}
					continue
				}

				created, completed := prewarmEvents(normalized)
				if write(created) != nil || write(completed) != nil {
					return
				}
				if err := conversation.Accept(normalized, completed); err != nil {
					if writeError(err) != nil {
						return
					}
				}
				continue
			}
			release, err := h.gateway.Acquire()
			if err != nil {
				if writeError(err) != nil {
					return
				}
				continue
			}
			turnCtx, turnCancel := context.WithTimeout(ctx, h.gateway.Policy().RequestTimeout)
			turnCtx = gateway.WithRequestIdentity(turnCtx, principal.KeyID, "websocket")
			turnID = gateway.RequestID(turnCtx)
			err = h.websocketTurn(turnCtx, principal.UserID, principal.GroupID, normalized, headers, &conversation, write)
			turnCancel()
			release()
			if err != nil && ctx.Err() == nil {
				if writeError(err) != nil {
					return
				}
			}
		}
	}
}

func (h *keyHTTP) websocketTurn(ctx context.Context, userID, groupID int64, request []byte, headers http.Header, conversation *gateway.Conversation, write func([]byte) error) error {
	result, err := h.gateway.Open(ctx, userID, groupID, request, headers, gateway.Responses)
	if err != nil {
		return err
	}
	defer result.Body.Close()
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return &upstream.UpstreamError{Status: result.StatusCode, RetryAfter: result.Header.Get("Retry-After")}
	}
	return consumeEvents(ctx, result, h.gateway.Policy().StreamIdleTimeout, 0, func(event []byte) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var metadata struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(event, &metadata) != nil {
			return upstream.ErrResponse
		}
		if metadata.Type == "response.completed" || metadata.Type == "response.incomplete" {
			if err := conversation.Accept(request, event); err != nil {
				return err
			}
		}
		for _, chunk := range result.Translate(ctx, event) {
			chunk = bytes.TrimSpace(chunk)
			if bytes.HasPrefix(chunk, []byte("data:")) {
				chunk = bytes.TrimSpace(chunk[5:])
			}
			if len(chunk) > 0 {
				if metadata.Type == "response.created" || metadata.Type == "response.completed" || metadata.Type == "response.incomplete" {
					chunk = correlateEvent(chunk, gateway.RequestID(ctx))
				}
				if err := write(chunk); err != nil {
					return err
				}
			}
		}
		return nil
	}, nil)
}

func prewarmEvents(request []byte) ([]byte, []byte) {
	var input struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(request, &input)
	id := "resp_prewarm_" + randomID()
	response := map[string]any{"id": id, "object": "response", "created_at": time.Now().Unix(), "model": input.Model, "status": "in_progress", "output": []any{}, "error": nil}
	created, _ := json.Marshal(map[string]any{"type": "response.created", "sequence_number": 0, "response": response})
	response["status"] = "completed"
	response["usage"] = map[string]int{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	completed, _ := json.Marshal(map[string]any{"type": "response.completed", "sequence_number": 1, "response": response})
	return created, completed
}

func randomID() string {
	value := make([]byte, 16)
	_, _ = rand.Read(value)
	return hex.EncodeToString(value)
}

// Add gateway correlation beside the protocol payload; never replace the upstream response ID.
func correlateEvent(raw []byte, id string) []byte {
	if id == "" {
		return raw
	}
	var event map[string]json.RawMessage
	if json.Unmarshal(raw, &event) != nil || event == nil {
		return raw
	}
	event["request_id"], _ = json.Marshal(id)
	value, err := json.Marshal(event)
	if err != nil {
		return raw
	}
	return value
}
