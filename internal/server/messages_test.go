package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/murongg/SubLane/internal/gateway"
)

const syntheticMessage = `{"id":"msg_synthetic","type":"message","role":"assistant","model":"synthetic-model","content":[{"type":"thinking","thinking":"Synthetic reasoning","signature":"synthetic-signature"},{"type":"tool_use","id":"tool_synthetic","name":"read_file","input":{"path":"example.txt"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":5,"cache_creation_input_tokens":3,"cache_read_input_tokens":12,"output_tokens":7}}`

func TestMessagesPreserveClaudeCodeControlTurns(t *testing.T) {
	turns := []map[string]any{
		{"role": "user", "content": []map[string]string{{"type": "text", "text": "Synthetic prompt"}}},
		{"role": "system", "content": []map[string]string{}, "output_config": map[string]string{"effort": "medium"}},
		{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "tool_previous", "name": "read_file", "input": map[string]string{"path": "example.txt"}}}},
		{"role": "user", "content": []map[string]string{{"type": "tool_result", "tool_use_id": "tool_previous", "content": "Synthetic result"}}},
		{"role": "system", "content": []map[string]string{{"type": "text", "text": "Synthetic updated instruction"}}, "cache_control": map[string]string{"type": "ephemeral"}},
	}
	want, _ := json.Marshal(turns)
	var expected any
	if err := json.Unmarshal(want, &expected); err != nil {
		t.Fatal(err)
	}
	f := newProviderFixture(t, "claude", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages any  `json:"messages"`
			Stream   bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if !reflect.DeepEqual(request.Messages, expected) {
			t.Error("Claude Code control turns or tool history changed")
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: "+syntheticMessageEvents()+"\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, syntheticMessage)
		}
	})
	for _, stream := range []bool{false, true} {
		body, err := json.Marshal(map[string]any{
			"model": "synthetic-model", "max_tokens": 1024, "stream": stream, "messages": turns,
			"system":        []map[string]any{{"type": "text", "text": "Synthetic system", "cache_control": map[string]string{"type": "ephemeral"}}},
			"tools":         []map[string]any{{"name": "read_file", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]string{"type": "string"}}}}},
			"thinking":      map[string]string{"type": "adaptive", "display": "omitted"},
			"output_config": map[string]string{"effort": "medium"},
			"metadata":      map[string]string{"user_id": "synthetic-client-session"},
		})
		if err != nil {
			t.Fatal(err)
		}
		response, raw := messageRequest(t, f, "POST", "/v1/messages?beta=true", string(body), http.Header{"X-Api-Key": {f.secret}, "Anthropic-Beta": {"mid-conversation-system-2026-04-07"}})
		if response.StatusCode != 200 || !strings.Contains(raw, "tool_synthetic") || !strings.Contains(raw, "synthetic-signature") {
			t.Fatalf("Claude Code request failed (stream=%t): %d %s", stream, response.StatusCode, raw)
		}
		if stream && (!strings.Contains(raw, "event: message_stop\n") || strings.Contains(raw, "[DONE]")) {
			t.Fatalf("invalid Claude Code stream: %s", raw)
		}
	}
}

func syntheticMessageEvents() string {
	return strings.Join([]string{
		`{"type":"message_start","message":{"id":"msg_synthetic","type":"message","role":"assistant","model":"synthetic-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":5,"cache_creation_input_tokens":3,"cache_read_input_tokens":12,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Synthetic reasoning"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"synthetic-signature"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool_synthetic","name":"read_file","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"example.txt\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	}, "\n\ndata: ")
}

func messageRequest(t *testing.T, f forwardFixture, method, path, body string, headers http.Header) (*http.Response, string) {
	t.Helper()
	r, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header = headers.Clone()
	if r.Header == nil {
		r.Header = make(http.Header)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Anthropic-Version", "2023-06-01")
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, string(raw)
}

func TestMessagesNativeJSONAndSSE(t *testing.T) {
	f := newProviderFixture(t, "claude", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("wrong upstream path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-upstream-access" || r.Header.Get("X-Api-Key") != "" || r.Header.Get("Cookie") != "" {
			t.Error("client credentials reached upstream")
		}
		var request map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("invalid upstream body")
		}
		for _, field := range []string{"messages", "system", "tools", "thinking", "max_tokens"} {
			if len(request[field]) == 0 {
				t.Errorf("native field lost: %s", field)
			}
		}
		if !strings.Contains(string(request["system"]), "Synthetic system") || !strings.Contains(string(request["messages"]), "tool_result") {
			t.Error("native content lost")
		}
		if string(request["stream"]) == "true" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: "+syntheticMessageEvents()+"\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, syntheticMessage)
		}
	})
	for _, stream := range []bool{false, true} {
		body := `{"model":"synthetic-model","max_tokens":1024,"system":[{"type":"text","text":"Synthetic system","cache_control":{"type":"ephemeral"}}],"thinking":{"type":"enabled","budget_tokens":128},"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"tool_previous","name":"read_file","input":{"path":"example.txt"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_previous","content":"Synthetic result"}]}]`
		if stream {
			body += `,"stream":true`
		}
		body += `}`
		response, raw := messageRequest(t, f, "POST", "/v1/messages", body, http.Header{"X-Api-Key": {f.secret}, "Cookie": {"synthetic-cookie=private"}})
		if response.StatusCode != 200 {
			t.Fatalf("messages failed: %d %s", response.StatusCode, raw)
		}
		if response.Header.Get("Request-Id") == "" || response.Header.Get("Request-Id") != response.Header.Get("X-Request-Id") {
			t.Fatal("missing request correlation")
		}
		for _, value := range []string{"synthetic-signature", "tool_synthetic", "tool_use"} {
			if !strings.Contains(raw, value) {
				t.Fatalf("native response lost %s: %s", value, raw)
			}
		}
		if stream {
			if !strings.Contains(raw, "event: message_start\n") || !strings.Contains(raw, "event: message_stop\n") || strings.Contains(raw, "[DONE]") {
				t.Fatalf("invalid native SSE: %s", raw)
			}
		} else if !json.Valid([]byte(raw)) || !strings.Contains(raw, `"type":"message"`) {
			t.Fatalf("invalid message: %s", raw)
		}
	}
	deadline := time.Now().Add(time.Second)
	for {
		page, err := f.forwarding.Requests(context.Background(), gateway.RequestFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Requests) == 2 {
			for _, record := range page.Requests {
				if record.Operation != "messages" || record.Outcome != "success" || record.InputTokens == nil || *record.InputTokens != 20 || record.OutputTokens == nil || *record.OutputTokens != 7 || record.CachedTokens == nil || *record.CachedTokens != 12 {
					t.Fatalf("wrong message observation: %+v", record)
				}
			}
			if page.Requests[0].FirstTokenMs == nil {
				t.Fatal("missing streamed first-output timing")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("missing message records: %+v", page)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMessagesAuthenticationAndProtectedRouting(t *testing.T) {
	f := newProviderFixture(t, "claude", func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected upstream request") })
	for _, tc := range []struct {
		method, path, bearer, key string
		status                    int
		kind                      string
	}{
		{"POST", "/v1/messages", "", "", 401, "authentication_error"},
		{"POST", "/v1/messages", "", "invalid", 401, "authentication_error"},
		{"POST", "/v1/messages", "Bearer invalid", f.secret, 401, "authentication_error"},
		{"GET", "/v1/messages", "", "", 401, "authentication_error"},
		{"GET", "/v1/messages", "", f.secret, 405, "invalid_request_error"},
		{"GET", "/v1/messages/missing", "", "", 401, "authentication_error"},
		{"GET", "/v1/messages/missing", "Bearer " + f.secret, "", 404, "not_found_error"},
	} {
		response, raw := messageRequest(t, f, tc.method, tc.path, `{}`, http.Header{"Authorization": {tc.bearer}, "X-Api-Key": {tc.key}})
		var value struct {
			Type, RequestID string
			Error           struct{ Type string }
		}
		if json.Unmarshal([]byte(raw), &value) != nil || response.StatusCode != tc.status || value.Type != "error" || value.Error.Type != tc.kind {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, response.StatusCode, raw)
		}
		if response.Header.Get("Request-Id") == "" {
			t.Fatal("missing native error request ID")
		}
		if tc.status == 405 && response.Header.Get("Allow") != "POST" {
			t.Fatal("wrong allowed methods")
		}
	}
}

func TestNativeSSEFramesPrettyPrintedJSON(t *testing.T) {
	var output bytes.Buffer
	if err := writeSSE(&output, []byte("{\n\"type\":\"message_stop\"\n}"), true); err != nil {
		t.Fatal(err)
	}
	var data []string
	for _, line := range strings.Split(output.String(), "\n") {
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(line, "data:"))
		}
	}
	if !json.Valid([]byte(strings.Join(data, "\n"))) {
		t.Fatalf("invalid multiline SSE: %s", output.String())
	}
}

func TestNativeRejectsDuplicateAuthorizationEvenWhenFirstIsEmpty(t *testing.T) {
	f := newProviderFixture(t, "claude", func(http.ResponseWriter, *http.Request) { t.Error("unexpected upstream call") })
	response, raw := messageRequest(t, f, "POST", "/v1/messages", `{}`, http.Header{"Authorization": {"", "Bearer invalid"}, "X-Api-Key": {f.secret}})
	if response.StatusCode != 401 {
		t.Fatalf("ambiguous authorization accepted: %d %s", response.StatusCode, raw)
	}
}
