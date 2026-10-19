package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/murongg/SubLane/internal/accounts"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func GeminiRequest(raw []byte, model string, maxRequestBody ...int64) ([]byte, error) {
	var body map[string]json.RawMessage
	if int64(len(raw)) > requestBodyLimit(maxRequestBody...) || json.Unmarshal(raw, &body) != nil || body == nil || model == "" || len(model) > 128 || strings.ContainsAny(model, "/:?#%") {
		return nil, ErrInput
	}
	// The route is authoritative; body fields must never bypass model policy or select the streaming operation.
	body["model"], _ = json.Marshal(model)
	delete(body, "stream")
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, ErrInput
	}
	return encoded, ValidateGemini(encoded, maxRequestBody...)
}

func ValidateGemini(raw []byte, maxRequestBody ...int64) error {
	var request struct {
		Contents []struct {
			Role  string
			Parts []map[string]json.RawMessage
		}
	}
	if int64(len(raw)) > requestBodyLimit(maxRequestBody...) || json.Unmarshal(raw, &request) != nil || len(request.Contents) == 0 {
		return ErrInput
	}
	for _, content := range request.Contents {
		if len(content.Parts) == 0 || (content.Role != "" && content.Role != "user" && content.Role != "model") {
			return ErrInput
		}
		for _, part := range content.Parts {
			if len(part) == 0 {
				return ErrInput
			}
		}
	}
	return nil
}

func (c *Client) Gemini(ctx context.Context, credential accounts.Credential, raw []byte, headers http.Header, stream bool) (*Stream, error) {
	if err := ValidateGemini(raw, c.MaxRequestBody()); err != nil {
		return nil, err
	}
	response, err := c.runSDK(ctx, credential, raw, headers, exec.Options{Stream: stream, SourceFormat: translator.FormatGemini, ResponseFormat: translator.FormatGemini})
	if err != nil {
		return nil, err
	}
	return &Stream{Response: response, format: translator.FormatGemini}, nil
}

type geminiResponse struct {
	Candidates []struct {
		Index        int    `json:"index"`
		FinishReason string `json:"finishReason"`
		Content      struct {
			Parts []struct {
				Text                     string
				FunctionCall, InlineData json.RawMessage
			}
		}
	} `json:"candidates"`
	PromptFeedback struct{ BlockReason string } `json:"promptFeedback"`
	Usage          geminiUsage                  `json:"usageMetadata"`
	Error          *struct{ Code int }          `json:"error"`
}

type geminiUsage struct {
	Input    *int64 `json:"promptTokenCount"`
	Output   *int64 `json:"candidatesTokenCount"`
	Thoughts *int64 `json:"thoughtsTokenCount"`
	Cached   *int64 `json:"cachedContentTokenCount"`
}

func (s *Stream) GeminiUsage(raw []byte) (input, output, cached *int64) {
	var event geminiResponse
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	for _, field := range []struct {
		target **int64
		value  *int64
	}{{&s.geminiUsage.Input, event.Usage.Input}, {&s.geminiUsage.Output, event.Usage.Output}, {&s.geminiUsage.Thoughts, event.Usage.Thoughts}, {&s.geminiUsage.Cached, event.Usage.Cached}} {
		if field.value != nil && *field.value >= 0 && *field.value <= 1_000_000_000 {
			*field.target = field.value
		}
	}
	if s.geminiUsage.Output != nil {
		total := *s.geminiUsage.Output
		if s.geminiUsage.Thoughts != nil {
			total += *s.geminiUsage.Thoughts
		}
		if total <= 1_000_000_000 {
			output = &total
		}
	}
	// Gemini promptTokenCount already includes cached input; thoughts are additional output tokens.
	return s.geminiUsage.Input, output, s.geminiUsage.Cached
}

func GeminiEventInfo(raw []byte) (output, incomplete bool) {
	var event geminiResponse
	if json.Unmarshal(raw, &event) != nil {
		return
	}
	for _, candidate := range event.Candidates {
		incomplete = incomplete || candidate.FinishReason == "MAX_TOKENS"
		for _, part := range candidate.Content.Parts {
			output = output || part.Text != "" || (len(part.FunctionCall) > 0 && string(part.FunctionCall) != "null") || (len(part.InlineData) > 0 && string(part.InlineData) != "null")
		}
	}
	return
}

func ValidateGeminiResponse(raw []byte) error {
	var response geminiResponse
	if len(raw) > MaxBody || json.Unmarshal(raw, &response) != nil || response.Error != nil {
		return ErrResponse
	}
	if len(response.Candidates) == 0 && response.PromptFeedback.BlockReason == "" {
		return ErrResponse
	}
	for _, candidate := range response.Candidates {
		if candidate.FinishReason == "" {
			return ErrResponse
		}
	}
	return nil
}

func readGeminiEvents(reader io.Reader, yield func([]byte) error) error {
	return readGeminiStream(reader, yield, false)
}

func readGeminiStream(reader io.Reader, yield func([]byte) error, envelope bool) error {
	finished := make(map[int]bool)
	blocked := false
	err := readSSE(reader, func(raw []byte) (bool, error) {
		var event geminiResponse
		if json.Unmarshal(raw, &event) != nil {
			return false, ErrResponse
		}
		if envelope && event.Error == nil {
			var wrapped struct {
				Response json.RawMessage `json:"response"`
			}
			if json.Unmarshal(raw, &wrapped) != nil {
				return false, ErrResponse
			}
			if len(wrapped.Response) > 0 && json.Unmarshal(wrapped.Response, &event) != nil {
				return false, ErrResponse
			}
		}
		if event.Error != nil {
			status := event.Error.Code
			if status < 400 || status > 599 {
				status = 502
			}
			return false, &UpstreamError{Status: status}
		}
		blocked = blocked || event.PromptFeedback.BlockReason != ""
		for _, candidate := range event.Candidates {
			if candidate.Index < 0 || candidate.Index > 4095 {
				return false, ErrResponse
			}
			finished[candidate.Index] = finished[candidate.Index] || candidate.FinishReason != ""
		}
		return false, yield(raw)
	})
	if !errors.Is(err, errSSEEOF) {
		return err
	}
	if !blocked && len(finished) == 0 {
		return ErrInterrupted
	}
	for _, done := range finished {
		if !done {
			return ErrInterrupted
		}
	}
	// Usage-only chunks may follow finishReason, so consume to a clean EOF before recording success.
	return nil
}
