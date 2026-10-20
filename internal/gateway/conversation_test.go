package gateway

import (
	"encoding/json"
	"errors"
	"github.com/murongg/SubLane/internal/upstream"
	"strings"
	"testing"
)

func TestConversationAcceptsLargeRequests(t *testing.T) {
	conversation := Conversation{}
	raw, _ := json.Marshal(map[string]any{"type": "response.create", "model": "synthetic-model", "instructions": strings.Repeat("x", (8<<20)+1), "input": []any{}})
	initial, _, err := conversation.Normalize(raw)
	if err != nil {
		t.Fatal("large initial request rejected", err)
	}
	if err := conversation.Accept(initial, []byte(`{"type":"response.completed","response":{"id":"resp_test","output":[]}}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conversation.Normalize([]byte(`{"type":"response.create","previous_response_id":"resp_test","input":[]}`)); err != nil {
		t.Fatal("large retained context rejected", err)
	}
}

func TestConversationConfiguredRequestBodyLimit(t *testing.T) {
	const limit = 512
	for _, size := range []int{limit - 1, limit, limit + 1} {
		conversation := Conversation{MaxRequestBody: limit}
		raw := `{"type":"response.create","model":"synthetic-model","input":[]}`
		raw += strings.Repeat(" ", size-len(raw))
		_, _, err := conversation.Normalize([]byte(raw))
		if size > limit && !errors.Is(err, ErrContextLimit) || size <= limit && err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
	}
	conversation := Conversation{MaxRequestBody: limit}
	initial, _, err := conversation.Normalize([]byte(`{"type":"response.create","model":"synthetic-model","input":"` + strings.Repeat("x", 150) + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := conversation.Accept(initial, []byte(`{"response":{"id":"resp_test","output":[]}}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conversation.Normalize([]byte(`{"type":"response.create","previous_response_id":"resp_test","input":"` + strings.Repeat("x", 250) + `"}`)); !errors.Is(err, ErrContextLimit) {
		t.Fatal("merged context exceeded request limit", err)
	}
	if err := conversation.Accept(initial, []byte(`{"response":{"id":"resp_test","output":[{"type":"function_call_output","output":"`+strings.Repeat("x", limit)+`"}]}}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conversation.Normalize([]byte(`{"type":"response.create","previous_response_id":"resp_test","input":[]}`)); !errors.Is(err, ErrContextLimit) {
		t.Fatal("retained output exceeded request limit", err)
	}
	if _, _, err := conversation.Normalize([]byte(`{"type":"response.create","input":[{"type":"compaction","encrypted_content":"synthetic"}]}`)); err != nil {
		t.Fatal("compaction did not recover overflow", err)
	}
}

func TestConversationReconstructsIncrementalInputAndCompaction(t *testing.T) {
	conversation := Conversation{}
	initial, prewarm, err := conversation.Normalize([]byte(`{"type":"response.create","model":"synthetic-model","input":[{"role":"user","content":"first"}]}`))
	if err != nil || prewarm {
		t.Fatal(err)
	}
	if err := conversation.Accept(initial, []byte(`{"type":"response.completed","response":{"id":"resp_one","output":[{"type":"function_call","call_id":"call_test","name":"synthetic","arguments":"{}"}]}}`)); err != nil {
		t.Fatal(err)
	}
	next, _, err := conversation.Normalize([]byte(`{"type":"response.create","previous_response_id":"resp_one","input":[{"type":"function_call_output","call_id":"call_test","output":"synthetic output"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Model    string            `json:"model"`
		Input    []json.RawMessage `json:"input"`
		Previous string            `json:"previous_response_id"`
	}
	if json.Unmarshal(next, &request) != nil || len(request.Input) != 3 || request.Previous != "" || request.Model != "synthetic-model" {
		t.Fatal("incremental context lost")
	}
	if _, _, err := conversation.Normalize([]byte(`{"type":"response.create","previous_response_id":"resp_other","input":[]}`)); !errors.Is(err, upstream.ErrContinuation) {
		t.Fatal("unknown continuation accepted", err)
	}
	replaced, _, err := conversation.Normalize([]byte(`{"type":"response.create","model":"synthetic-model","input":[{"type":"compaction","encrypted_content":"synthetic-compaction"},{"role":"user","content":"after compaction"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(replaced, &request) != nil || len(request.Input) != 2 {
		t.Fatal("stale transcript merged into compact replay")
	}
}

func TestConversationModelSwitchRequiresFullIndependentInput(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want error
	}{
		{"full history", `{"type":"response.create","model":"synthetic-other","input":[{"role":"assistant","content":"Synthetic reply"},{"role":"user","content":"Continue"}]}`, nil},
		{"implicit delta", `{"type":"response.create","model":"synthetic-other","input":"Continue"}`, ErrAffinityUnavailable},
		{"previous response", `{"type":"response.create","model":"synthetic-other","previous_response_id":"resp_synthetic","input":"Continue"}`, ErrAffinityUnavailable},
		{"previous with history", `{"type":"response.create","model":"synthetic-other","previous_response_id":"resp_synthetic","input":[{"role":"assistant","content":"Synthetic reply"}]}`, ErrAffinityUnavailable},
		{"append", `{"type":"response.append","model":"synthetic-other","input":"Continue"}`, ErrAffinityUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			conversation := Conversation{}
			initial, _, err := conversation.Normalize([]byte(`{"type":"response.create","model":"synthetic-model","input":"Synthetic question"}`))
			if err != nil {
				t.Fatal(err)
			}
			if err := conversation.Accept(initial, []byte(`{"response":{"id":"resp_synthetic","output":[{"role":"assistant","content":"Synthetic reply"}]}}`)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := conversation.Normalize([]byte(test.raw)); !errors.Is(err, test.want) {
				t.Fatalf("model switch: got %v, want %v", err, test.want)
			}
		})
	}
}
