package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"strings"
	"testing"
)

func TestSSECompletionIncludesFinishedOutputItems(t *testing.T) {
	input := ": heartbeat\n\nevent: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\n" +
		"data: \"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_test\",\"content\":[]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"output\":[]}}\n\n"
	var final []byte
	count := 0
	err := readEvents(strings.NewReader(input), func(event []byte) error { count++; final = append([]byte(nil), event...); return nil })
	if err != nil || count != 2 {
		t.Fatal("SSE framing failed", err, count)
	}
	var completed struct {
		Response struct {
			Output []struct {
				ID string `json:"id"`
			} `json:"output"`
		} `json:"response"`
	}
	if json.Unmarshal(final, &completed) != nil || len(completed.Response.Output) != 1 || completed.Response.Output[0].ID != "msg_test" {
		t.Fatal("completed output missing")
	}
}

func TestSSERequiresTerminalEventAndPropagatesCancellation(t *testing.T) {
	input := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"synthetic\"}\n\n"
	if err := readEvents(strings.NewReader(input), func([]byte) error { return nil }); !errors.Is(err, ErrInterrupted) {
		t.Fatal("truncated stream reported success", err)
	}
	canceled := errors.New("synthetic cancellation")
	if err := readEvents(strings.NewReader(input), func([]byte) error { return canceled }); !errors.Is(err, canceled) {
		t.Fatal("consumer cancellation lost", err)
	}
	if err := readEvents(strings.NewReader("data: "+strings.Repeat("x", MaxBody+1)+"\n\n"), func([]byte) error { return nil }); !errors.Is(err, ErrResponse) {
		t.Fatal("unbounded SSE event accepted", err)
	}
}

func TestFailedSSEDeliversUsageBeforeInterrupting(t *testing.T) {
	raw := []byte(`{"type":"response.failed","response":{"usage":{"input_tokens":3,"output_tokens":1}}}`)
	var seen []byte
	err := readEvents(strings.NewReader("data: "+string(raw)+"\n\n"), func(event []byte) error {
		seen = append([]byte(nil), event...)
		return nil
	})
	if !errors.Is(err, ErrInterrupted) || string(seen) != string(raw) {
		t.Fatal("failed terminal usage was not observed", err, string(seen))
	}
}

func TestIncompleteResponsePreservesPartialResult(t *testing.T) {
	client := New()
	defer client.Close()
	stream := &Stream{registry: client.registry, format: translator.FormatOpenAIResponse, model: "synthetic-model"}
	raw := stream.Complete(context.Background(), []byte(`{"type":"response.incomplete","response":{"id":"resp_partial","status":"incomplete","output":[],"incomplete_details":{"reason":"max_output_tokens"}}}`))
	var response struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &response) != nil || response.ID != "resp_partial" || response.Status != "incomplete" {
		t.Fatal("incomplete response was discarded")
	}
}
