package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/allocations"
	"github.com/murongg/SubLane/internal/apikey"
	"github.com/murongg/SubLane/internal/content"
	"github.com/murongg/SubLane/internal/gateway"
	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/upstream"
)

type keyPrincipalKey struct{}

func (h *keyHTTP) registerGateway(router chi.Router) {
	router.Group(func(openai chi.Router) {
		openai.Use(h.requireKey)
		// Inline chi groups also wrap fallback handlers, preserving authentication on unknown paths and methods.
		routeErrors(openai)
		openai.Get("/models", h.models)
		openai.Get("/responses", h.websocket)
		openai.Post("/responses", func(w http.ResponseWriter, r *http.Request) { h.proxy(w, r, gateway.Responses) })
		openai.Post("/responses/compact", func(w http.ResponseWriter, r *http.Request) { h.proxy(w, r, gateway.Compact) })
		openai.Post("/chat/completions", func(w http.ResponseWriter, r *http.Request) { h.proxy(w, r, gateway.Chat) })
	})
	router.Route("/messages", func(messages chi.Router) {
		messages.Use(nativeIdentity, h.requireMessagesKey)
		routeErrorsWith(messages, writeMessagesError)
		messages.Post("/", func(w http.ResponseWriter, r *http.Request) { h.proxy(w, r, gateway.Messages) })
	})
}

func (h *keyHTTP) requireKey(next http.Handler) http.Handler {
	return h.authenticateKey(next, gateway.Responses)
}

func (h *keyHTTP) authenticateKey(next http.Handler, kind gateway.Kind) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fail, writeError := gatewayWriters(kind)
		secret, valid := gatewaySecret(r, kind)
		if !valid {
			fail(w, apikey.ErrInvalidKey)
			return
		}
		if (kind == gateway.Messages || kind.IsGemini()) && h.service == nil {
			writeError(w, 503, "unavailable")
			return
		}
		if !h.available(w) {
			return
		}
		principal, err := h.service.Authenticate(r.Context(), strings.TrimSpace(secret))
		if err != nil {
			fail(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyPrincipalKey{}, principal)))
	})
}

func (h *keyHTTP) models(w http.ResponseWriter, r *http.Request) {
	if h.gateway == nil {
		gatewayUnavailable(w, r)
		return
	}
	// Shared discovery owns upstream admission; a cached list consumes no generation slot.
	principal, _ := r.Context().Value(keyPrincipalKey{}).(apikey.Principal)
	models, err := h.gateway.Models(r.Context(), principal.UserID, principal.GroupID)
	if err != nil {
		gatewayError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": models})
}

func (h *keyHTTP) proxy(w http.ResponseWriter, r *http.Request, kind gateway.Kind) {
	fail, writeError := gatewayWriters(kind)
	if h.gateway == nil {
		writeError(w, 501, "gateway_not_configured")
		return
	}
	release, err := h.gateway.Acquire()
	if err != nil {
		fail(w, err)
		return
	}
	defer release()
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, 415, "json_required")
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(30 * time.Second))
	r.Body = http.MaxBytesReader(w, r.Body, h.gateway.MaxRequestBody())
	raw, err := io.ReadAll(r.Body)
	_ = controller.SetReadDeadline(time.Time{})
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			writeError(w, 413, "request_too_large")
		} else {
			fail(w, upstream.ErrInput)
		}
		return
	}
	var flags struct {
		Stream bool `json:"stream"`
	}
	if kind.IsGemini() {
		if alt := r.URL.Query().Get("alt"); kind == gateway.GeminiStream && alt != "" && alt != "sse" {
			fail(w, upstream.ErrInput)
			return
		}
		raw, err = upstream.GeminiRequest(raw, chi.URLParam(r, "model"), h.gateway.MaxRequestBody())
		if err != nil {
			fail(w, err)
			return
		}
	}
	if json.Unmarshal(raw, &flags) != nil || kind == gateway.Compact && flags.Stream {
		fail(w, upstream.ErrInput)
		return
	}
	if kind.IsGemini() {
		flags.Stream = kind == gateway.GeminiStream
	}
	policy := h.gateway.Policy()
	ctx, cancel := context.WithTimeout(r.Context(), policy.RequestTimeout)
	defer cancel()
	principal, _ := r.Context().Value(keyPrincipalKey{}).(apikey.Principal)
	ctx = gateway.WithRequestIdentity(ctx, principal.KeyID, "http")
	w.Header().Set("X-Request-ID", gateway.RequestID(ctx))
	result, err := h.gateway.Open(ctx, principal.UserID, principal.GroupID, raw, r.Header, kind)
	if err != nil {
		fail(w, err)
		return
	}
	defer result.Body.Close()
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		fail(w, &upstream.UpstreamError{Status: result.StatusCode, RetryAfter: result.Header.Get("Retry-After")})
		return
	}
	if kind == gateway.Compact || (kind == gateway.Messages || kind.IsGemini()) && !flags.Stream {
		data, err := io.ReadAll(io.LimitReader(result.Body, upstream.MaxBody+1))
		if err != nil || len(data) > upstream.MaxBody || !json.Valid(data) {
			result.Fail(upstream.ErrResponse)
			fail(w, upstream.ErrResponse)
			return
		}
		if kind == gateway.Messages {
			if err := result.AcceptMessage(data); err != nil {
				fail(w, err)
				return
			}
		} else if kind.IsGemini() {
			if err := result.AcceptGemini(data); err != nil {
				fail(w, err)
				return
			}
		} else {
			result.AcceptCompact(data)
		}
		writeRawJSON(w, data)
		return
	}
	started := false
	startStream := func() {
		if started {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(200)
		started = true
	}
	var heartbeat func() error
	if flags.Stream {
		heartbeat = func() error {
			startStream()
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return err
			}
			return controller.Flush()
		}
	}
	var completed []byte
	err = consumeEvents(ctx, result, policy.StreamIdleTimeout, policy.KeepaliveInterval, func(event []byte) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !flags.Stream {
			completed = bytes.Clone(event)
			return nil
		}
		startStream()
		for _, chunk := range result.Translate(ctx, event) {
			chunk = bytes.TrimSpace(chunk)
			if len(chunk) == 0 {
				continue
			}
			if bytes.HasPrefix(chunk, []byte("data:")) {
				chunk = bytes.TrimSpace(chunk[5:])
			}
			if err := writeSSE(w, chunk, kind == gateway.Responses || kind == gateway.Messages); err != nil {
				return err
			}
		}
		return controller.Flush()
	}, heartbeat)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		if !started {
			fail(w, err)
			return
		}
		status, code := gatewayFailure(err)
		payload, _ := json.Marshal(map[string]any{"type": "error", "status": status, "error": gatewayErrorBody(code)})
		if kind == gateway.Messages {
			payload, _ = json.Marshal(messagesErrorBody(w, status, code))
		} else if kind.IsGemini() {
			payload, _ = json.Marshal(geminiErrorBody(status, code))
		}
		_ = writeSSE(w, payload, kind == gateway.Responses || kind == gateway.Messages)
		_ = controller.Flush()
		return
	}
	if flags.Stream {
		if kind == gateway.Chat {
			_ = writeSSE(w, []byte("[DONE]"), false)
			_ = controller.Flush()
		}
		return
	}
	output := result.Complete(ctx, completed)
	if !json.Valid(output) {
		fail(w, upstream.ErrResponse)
		return
	}
	writeRawJSON(w, output)
}

func writeRawJSON(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

func writeSSE(w io.Writer, data []byte, named bool) error {
	// Native upstream events may contain multi-line JSON; keep the SSE data field on one line.
	var compact bytes.Buffer
	if json.Compact(&compact, data) == nil {
		data = compact.Bytes()
	}
	if named {
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &event) == nil && event.Type != "" && len(event.Type) < 100 && strings.IndexFunc(event.Type, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0 {
			if _, err := io.WriteString(w, "event: "+event.Type+"\n"); err != nil {
				return err
			}
		}
	}
	_, err := io.WriteString(w, "data: "+string(data)+"\n\n")
	return err
}

func gatewayFailure(err error) (int, string) {
	var rejected *upstream.UpstreamError
	switch {
	case errors.Is(err, content.ErrBlocked):
		return 403, "content_policy_blocked"
	case errors.Is(err, content.ErrUnavailable):
		return 503, "content_check_unavailable"
	case errors.Is(err, apikey.ErrInvalidKey):
		return 401, "invalid_api_key"
	case errors.Is(err, gateway.ErrContextLimit):
		return 400, "conversation_context_limit"
	case errors.Is(err, gateway.ErrModelNotAllowed):
		return 403, "model_not_allowed"
	case errors.Is(err, gateway.ErrModelUnavailable):
		return 404, "model_not_available"
	case errors.Is(err, gateway.ErrCatalogUnavailable):
		return 503, "model_catalog_unavailable"
	case errors.Is(err, upstream.ErrInput):
		return 400, "invalid_model_request"
	case errors.Is(err, upstream.ErrContinuation):
		return 400, "continuation_requires_full_input"
	case errors.Is(err, groups.ErrUnavailable):
		return 403, "group_unavailable"
	case errors.Is(err, gateway.ErrAffinityUnavailable):
		return 409, "conversation_account_unavailable"
	case errors.Is(err, gateway.ErrNoAccount):
		return 503, "no_accounts_available"
	case errors.Is(err, allocations.ErrQuota):
		return 403, err.Error()
	case errors.Is(err, allocations.ErrPending), errors.Is(err, allocations.ErrRisk):
		return 429, err.Error()
	case errors.Is(err, allocations.ErrUnavailable):
		return 403, err.Error()
	case errors.Is(err, allocations.ErrUnpriced):
		return 403, err.Error()
	case errors.Is(err, gateway.ErrAllocationAccounting):
		return 503, "allocation_accounting_unavailable"
	case errors.Is(err, gateway.ErrQuotaExhausted):
		return 403, "quota_exhausted"
	case errors.Is(err, gateway.ErrAccountCooling):
		return 429, "account_cooling"
	case errors.Is(err, gateway.ErrModelCooling):
		return 429, "model_cooling"
	case errors.Is(err, gateway.ErrMemberBusy):
		return 429, "member_busy"
	case errors.Is(err, gateway.ErrMemberRate):
		return 429, "member_rate_limited"
	case errors.Is(err, gateway.ErrAccountQueueFull), errors.Is(err, gateway.ErrAccountWaitTimeout):
		return 429, err.Error()
	case errors.Is(err, gateway.ErrAccountBusy):
		return 429, "account_busy"
	case errors.Is(err, gateway.ErrBusy), errors.Is(err, gateway.ErrAffinityLimit):
		return 429, "gateway_busy"
	case errors.Is(err, accounts.ErrDisabled), errors.Is(err, accounts.ErrProviderDisabled), errors.Is(err, accounts.ErrNotFound):
		return 503, "account_unavailable"
	case errors.Is(err, accounts.ErrReauthorize):
		return 503, "account_reauthorization_required"
	case errors.Is(err, accounts.ErrRefresh):
		return 503, "account_refresh_failed"
	case errors.Is(err, context.DeadlineExceeded):
		return 504, "gateway_timeout"
	case errors.Is(err, upstream.ErrInterrupted):
		return 502, "upstream_stream_interrupted"
	case errors.As(err, &rejected):
		switch rejected.Status {
		case 400:
			return 400, "upstream_invalid_request"
		case 404:
			return 404, "model_not_found"
		case 401:
			return 503, "account_reauthorization_required"
		case 403:
			return 403, "upstream_forbidden"
		case 429:
			if rejected.LimitedModel != "" {
				return 429, "model_rate_limited"
			}
			return 429, "upstream_rate_limited"
		default:
			return 502, "upstream_unavailable"
		}
	default:
		return 502, "upstream_unavailable"
	}
}

func gatewayError(w http.ResponseWriter, err error) {
	writeGatewayFailure(w, err, func(w http.ResponseWriter, status int, code string) {
		writeJSON(w, status, map[string]any{"error": gatewayErrorBody(code)})
	})
}

func writeGatewayFailure(w http.ResponseWriter, err error, writeError func(http.ResponseWriter, int, string)) {
	status, code := gatewayFailure(err)
	if errors.Is(err, gateway.ErrCatalogUnavailable) {
		w.Header().Set("Retry-After", "5")
	}
	if errors.Is(err, accounts.ErrRefresh) {
		seconds := int64(30)
		var refresh *accounts.RefreshError
		if errors.As(err, &refresh) {
			seconds = min(3600, max(1, refresh.RetryAfter))
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	}
	if status == 401 {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	if status == 429 && !errors.Is(err, allocations.ErrPending) && !errors.Is(err, allocations.ErrQuota) && !errors.Is(err, allocations.ErrRisk) {
		value := "1"
		var rejected *upstream.UpstreamError
		if errors.As(err, &rejected) {
			value = rejected.RetryAfter
		}
		var cooling *gateway.CoolingError
		if errors.As(err, &cooling) {
			value = strconv.FormatInt(cooling.RetryAfter, 10)
		}
		var quota *gateway.QuotaError
		if errors.As(err, &quota) {
			value = strconv.FormatInt(quota.RetryAfter, 10)
		}
		var memberRate *gateway.MemberRateError
		if errors.As(err, &memberRate) {
			value = strconv.FormatInt(memberRate.RetryAfter, 10)
		}
		retryAfter(w, value)
	}
	writeError(w, status, code)
}

func gatewayErrorBody(code string) map[string]string {
	return map[string]string{"type": "gateway_error", "code": code, "message": gatewayErrorMessage(code)}
}

func gatewayErrorMessage(code string) string {
	if code == "content_policy_blocked" {
		return "content_policy_blocked: Request blocked by workspace content rules. It was not sent upstream. Remove the sensitive content or contact your workspace administrator."
	}
	return strings.ReplaceAll(code, "_", " ")
}

func writeGatewayError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": gatewayErrorBody(code)})
}

func gatewayUnavailable(w http.ResponseWriter, r *http.Request) {
	writeGatewayError(w, 501, "gateway_not_configured")
}
