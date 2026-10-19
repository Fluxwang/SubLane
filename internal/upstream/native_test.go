package upstream

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const messageStart = `data: {"type":"message_start","message":{"id":"synthetic","type":"message","role":"assistant"}}` + "\n\n"

func TestMessagesValidationAcceptsSystemTurns(t *testing.T) {
	for _, content := range []string{
		`"Synthetic instruction"`,
		`[{"type":"text","text":"Synthetic instruction","cache_control":{"type":"ephemeral"}}]`,
		`[]`,
	} {
		raw := []byte(`{"model":"synthetic-model","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"Synthetic prompt"},{"role":"system","content":` + content + `,"output_config":{"effort":"medium"}}]}`)
		if err := ValidateMessages(raw); err != nil {
			t.Errorf("system content %s rejected: %v", content, err)
		}
	}
}

func TestNativeLargeRequestValidation(t *testing.T) {
	text := strings.Repeat("x", (8<<20)+1)
	if err := ValidateMessages([]byte(`{"model":"synthetic","max_tokens":1,"messages":[{"role":"user","content":"` + text + `"}]}`)); err != nil {
		t.Fatal("large Messages request rejected", err)
	}
	if _, err := GeminiRequest([]byte(`{"contents":[{"parts":[{"text":"`+text+`"}]}]}`), "synthetic"); err != nil {
		t.Fatal("large Gemini request rejected", err)
	}
}

func TestNativeConfiguredRequestBodyLimit(t *testing.T) {
	messages := []byte(`{"model":"synthetic","max_tokens":1,"messages":[{"role":"user","content":"synthetic"}]}`)
	if err := ValidateMessages(messages, int64(len(messages))); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMessages(messages, int64(len(messages)-1)); !errors.Is(err, ErrInput) {
		t.Fatal("oversized Messages request accepted", err)
	}
	gemini := []byte(`{"contents":[{"parts":[{"text":"synthetic"}]}]}`)
	if err := ValidateGemini(gemini, int64(len(gemini))); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGemini(gemini, int64(len(gemini)-1)); !errors.Is(err, ErrInput) {
		t.Fatal("oversized Gemini request accepted", err)
	}
	if _, err := GeminiRequest(gemini, "synthetic", int64(len(gemini))); !errors.Is(err, ErrInput) {
		t.Fatal("normalized Gemini request exceeded limit", err)
	}
}

func TestNativeStreamCompletionAndFailureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		read        func(io.Reader, func([]byte) error) error
		want        error
	}{
		{"Claude truncated", messageStart, readMessageEvents, ErrInterrupted},
		{"Claude missing stop reason", messageStart + "data: {\"type\":\"message_stop\"}\n\n", readMessageEvents, ErrResponse},
		{"Claude duplicate start", messageStart + messageStart, readMessageEvents, ErrResponse},
		{"Gemini truncated", `data: {"candidates":[{"index":0,"content":{"parts":[{"text":"synthetic"}]}}]}` + "\n\n", readGeminiEvents, ErrInterrupted},
		{"Gemini missing candidate", `data: {"usageMetadata":{"promptTokenCount":3}}` + "\n\n", readGeminiEvents, ErrInterrupted},
		{"Gemini unfinished second candidate", `data: {"candidates":[{"index":0,"finishReason":"STOP"},{"index":1,"content":{"parts":[{"text":"synthetic"}]}}]}` + "\n\n", readGeminiEvents, ErrInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.read(strings.NewReader(tc.input), func([]byte) error { return nil }); !errors.Is(err, tc.want) {
				t.Fatalf("wanted %v, got %v", tc.want, err)
			}
		})
	}
	for _, read := range []func(io.Reader, func([]byte) error) error{readMessageEvents, readGeminiEvents} {
		if err := read(strings.NewReader("data: "+strings.Repeat("x", MaxBody+1)+"\n\n"), func([]byte) error { return nil }); !errors.Is(err, ErrResponse) {
			t.Fatal("unbounded event accepted", err)
		}
	}
	canceled := errors.New("synthetic consumer cancellation")
	if err := readMessageEvents(strings.NewReader(messageStart), func([]byte) error { return canceled }); !errors.Is(err, canceled) {
		t.Fatal("cancellation lost", err)
	}
}

func TestNativeStreamsSanitizeErrorsAndConsumeTrailingUsage(t *testing.T) {
	for _, tc := range []struct {
		read   func(io.Reader, func([]byte) error) error
		input  string
		status int
	}{
		{readMessageEvents, `data: {"type":"error","error":{"type":"rate_limit_error","message":"synthetic-private-body"}}` + "\n\n", 429},
		{readGeminiEvents, `data: {"error":{"code":503,"message":"synthetic-private-body"}}` + "\n\n", 503},
	} {
		err := tc.read(strings.NewReader(tc.input), func([]byte) error { t.Fatal("upstream error body leaked"); return nil })
		var rejected *UpstreamError
		if !errors.As(err, &rejected) || rejected.Status != tc.status || strings.Contains(err.Error(), "private") {
			t.Fatal("wrong sanitized error", err)
		}
	}
	input := `data: {"candidates":[{"index":0,"finishReason":"STOP"}]}` + "\n\n" + `data: {"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":5,"thoughtsTokenCount":2,"cachedContentTokenCount":12}}` + "\n\n"
	stream := &Stream{}
	var in, out, cached *int64
	err := readGeminiEvents(strings.NewReader(input), func(raw []byte) error { in, out, cached = stream.GeminiUsage(raw); return nil })
	if err != nil || in == nil || *in != 20 || out == nil || *out != 7 || cached == nil || *cached != 12 {
		t.Fatal("trailing usage lost", err, in, out, cached)
	}
}

func TestNativeRequestValidationRejectsMalformedContent(t *testing.T) {
	for _, raw := range []string{
		`{"model":"synthetic","max_tokens":1,"messages":[{"role":"user","content":[null]}]}`,
		`{"model":"synthetic","max_tokens":1,"messages":[{"role":"user","content":[{}]}]}`,
		`{"model":"synthetic","max_tokens":0,"messages":[{"role":"user","content":"synthetic"}]}`,
		`{"model":"synthetic","max_tokens":1,"messages":[]}`,
		`{"model":"synthetic","max_tokens":1,"messages":[{"role":"developer","content":"synthetic"}]}`,
		`{"model":"synthetic","max_tokens":1,"messages":[{"role":"user","content":[]}]}`,
		`{"model":"synthetic","max_tokens":1,"messages":[{"role":"assistant","content":[]}]}`,
		`{"model":"synthetic","max_tokens":1,"messages":[{"role":"system","content":null}]}`,
		`{"model":"synthetic","max_tokens":1,"messages":[{"role":"system"}]}`,
		`{"model":"synthetic","max_tokens":1,"messages":[{"role":"system","content":[null]}]}`,
		`{"model":"synthetic","max_tokens":1,"messages":[{"role":"system","content":{}}]}`,
	} {
		if ValidateMessages([]byte(raw)) == nil {
			t.Fatal("invalid message accepted", raw)
		}
	}
	for _, model := range []string{"", "../synthetic", "synthetic:delete", "synthetic%2fother"} {
		if _, err := GeminiRequest([]byte(`{"contents":[{"parts":[{"text":"synthetic"}]}]}`), model); err == nil {
			t.Fatal("invalid path model accepted", model)
		}
	}
}

func TestNativeNonStreamingUpstreamBodyIsBoundedBeforeSDKBuffering(t *testing.T) {
	transport := &engineTransport{maxResponseBytes: 32, base: usageTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 64)))}, nil
	})}
	request, _ := http.NewRequest("GET", "https://example.test/synthetic", nil)
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if !errors.Is(err, ErrResponse) || len(raw) > 32 {
		t.Fatal("unbounded non-streaming body", len(raw), err)
	}
}
