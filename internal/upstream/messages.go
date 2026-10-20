package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/murongg/SubLane/internal/accounts"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func ValidateMessages(raw []byte, maxRequestBody ...int64) error {
	var request struct {
		Model     string `json:"model"`
		MaxTokens int64  `json:"max_tokens"`
		Stream    bool   `json:"stream"`
		Messages  []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if int64(len(raw)) > requestBodyLimit(maxRequestBody...) || json.Unmarshal(raw, &request) != nil || request.Model == "" || request.MaxTokens <= 0 || len(request.Messages) == 0 {
		return ErrInput
	}
	for _, message := range request.Messages {
		if message.Role != "user" && message.Role != "assistant" && message.Role != "system" {
			return ErrInput
		}
		var text string
		var blocks []map[string]json.RawMessage
		if len(message.Content) == 0 || string(message.Content) == "null" {
			return ErrInput
		}
		if json.Unmarshal(message.Content, &text) != nil {
			// Claude Code sends empty system turns to update per-message effort without changing the cached prompt.
			if json.Unmarshal(message.Content, &blocks) != nil || len(blocks) == 0 && message.Role != "system" {
				return ErrInput
			}
		}
		for _, block := range blocks {
			var kind string
			if json.Unmarshal(block["type"], &kind) != nil || kind == "" {
				return ErrInput
			}
		}
	}
	return nil
}

func (c *Client) Messages(ctx context.Context, credential accounts.Credential, raw []byte, headers http.Header) (*Stream, error) {
	if err := ValidateMessages(raw, c.MaxRequestBody()); err != nil {
		return nil, err
	}
	var input struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(raw, &input)
	// Preserve native content (including signatures, cache controls and tools) through the SDK's Claude path.
	response, err := c.runSDK(ctx, credential, raw, headers, exec.Options{Stream: input.Stream, SourceFormat: translator.FormatClaude, ResponseFormat: translator.FormatClaude})
	if err != nil {
		return nil, err
	}
	return &Stream{Response: response, format: translator.FormatClaude}, nil
}

type messageUsage struct {
	Input   *int64 `json:"input_tokens"`
	Output  *int64 `json:"output_tokens"`
	Created *int64 `json:"cache_creation_input_tokens"`
	Cached  *int64 `json:"cache_read_input_tokens"`
}

func (s *Stream) MessageUsage(raw []byte) (input, output, cached *int64) {
	var event struct {
		Usage   messageUsage `json:"usage"`
		Message struct {
			Usage messageUsage `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	for _, usage := range []messageUsage{event.Message.Usage, event.Usage} {
		for _, field := range []struct {
			target **int64
			value  *int64
		}{{&s.messageUsage.Input, usage.Input}, {&s.messageUsage.Output, usage.Output}, {&s.messageUsage.Created, usage.Created}, {&s.messageUsage.Cached, usage.Cached}} {
			if field.value != nil && *field.value >= 0 && *field.value <= 1_000_000_000 {
				*field.target = field.value
			}
		}
	}
	if s.messageUsage.Input != nil {
		// Anthropic input_tokens excludes both cache reads and writes; history reports total input consistently.
		total := *s.messageUsage.Input
		if s.messageUsage.Created != nil {
			total += *s.messageUsage.Created
		}
		if s.messageUsage.Cached != nil {
			total += *s.messageUsage.Cached
		}
		if total <= 1_000_000_000 {
			input = &total
		}
	}
	return input, s.messageUsage.Output, s.messageUsage.Cached
}

func ValidateMessageResponse(raw []byte) error {
	var message struct {
		ID, Type, Role string
		Content        []json.RawMessage `json:"content"`
		StopReason     string            `json:"stop_reason"`
	}
	if len(raw) > MaxBody || json.Unmarshal(raw, &message) != nil || message.Type != "message" || message.Role != "assistant" || message.ID == "" || message.Content == nil || message.StopReason == "" {
		return ErrResponse
	}
	return nil
}

func readMessageEvents(reader io.Reader, yield func([]byte) error) error {
	started, finished := false, false
	err := readSSE(reader, func(raw []byte) (bool, error) {
		var event struct {
			Type    string                          `json:"type"`
			Message struct{ ID, Type, Role string } `json:"message"`
			Delta   struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &event) != nil || event.Type == "" {
			return false, ErrResponse
		}
		switch event.Type {
		case "error":
			status := 502
			switch event.Error.Type {
			case "rate_limit_error":
				status = 429
			case "overloaded_error":
				status = 529
			case "authentication_error":
				status = 401
			case "permission_error":
				status = 403
			case "invalid_request_error":
				status = 400
			}
			return false, &UpstreamError{Status: status}
		case "message_start":
			if started || event.Message.ID == "" || event.Message.Type != "message" || event.Message.Role != "assistant" {
				return false, ErrResponse
			}
			started = true
		case "message_delta":
			if !started {
				return false, ErrResponse
			}
			finished = finished || event.Delta.StopReason != ""
		case "message_stop":
			if !started || !finished {
				return false, ErrResponse
			}
		case "ping":
		default:
			if !started {
				return false, ErrResponse
			}
		}
		return event.Type == "message_stop", yield(raw)
	})
	return requireTerminal(err)
}
