package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
)

func syntheticClaudeCredential() accounts.Credential {
	return accounts.Credential{Provider: "claude", AccountID: "00000000-0000-4000-8000-000000000001", AccessToken: "sk-ant-oat01-synthetic-test", ExpiresAt: time.Now().Add(time.Hour).Unix(), Metadata: map[string]json.RawMessage{"account_uuid": json.RawMessage(`"00000000-0000-4000-8000-000000000001"`)}}
}

func TestClaudeVersionSnapshotChangesWithoutMutatingInFlightRequest(t *testing.T) {
	var version atomic.Value
	version.Store("2.200.0")
	var resolutions atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	client := NewWithOptions(Options{ClaudeVersion: func() string { resolutions.Add(1); return version.Load().(string) }, Transport: usageTransport(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		wireVersion := claudeWireVersion(raw)
		if wireVersion == "2.200.0" {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		if r.Header.Get("User-Agent") != "claude-cli/"+wireVersion+" (external, cli)" {
			t.Error("request identity changed across a version update")
		}
		return syntheticClaudeStream(), nil
	})})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	request := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"Synthetic prompt."}],"reasoning_effort":"medium"}`)
	done := make(chan error, 1)
	go func() {
		response, err := client.Chat(ctx, syntheticClaudeCredential(), request, nil)
		if err == nil {
			response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first request did not reach its upstream")
	}
	version.Store("2.201.0")
	response, err := client.Chat(ctx, syntheticClaudeCredential(), request, nil)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	response.Body.Close()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if resolutions.Load() != 2 || len(client.runtime.manager.List()) != 0 {
		t.Fatal("version snapshots or credential isolation violated", resolutions.Load())
	}
}

func claudeWireVersion(body []byte) string {
	match := regexp.MustCompile(`cc_version=([0-9]+\.[0-9]+\.[0-9]+)`).FindSubmatch(body)
	if len(match) == 2 {
		return string(match[1])
	}
	return ""
}

func syntheticClaudeStream() *http.Response {
	body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Synthetic response.\"}}\n\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestClaudeOpus55UsesSupportedVersionAndAdaptiveThinking(t *testing.T) {
	client := NewWithTransport(usageTransport(func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Thinking struct {
				Type   string `json:"type"`
				Budget *int64 `json:"budget_tokens"`
			} `json:"thinking"`
			Output struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		version := claudeWireVersion(raw)
		if version == "2.1.258" || version == "" || r.Header.Get("User-Agent") != "claude-cli/"+version+" (external, cli)" || body.Thinking.Type != "adaptive" || body.Thinking.Budget != nil || body.Output.Effort != "medium" {
			return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"type":"invalid_request_error","message":"synthetic incompatible Claude client or thinking"}}`))}, nil
		}
		return syntheticClaudeStream(), nil
	}))
	defer client.Close()
	response, err := client.Chat(context.Background(), syntheticClaudeCredential(), []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"Synthetic prompt."}],"reasoning_effort":"medium"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("Claude rejected its version/thinking contract: HTTP %d", response.StatusCode)
	}
}

func TestAdaptiveClaudeTranslationPreservesEffortAndOutputLimit(t *testing.T) {
	for _, test := range []struct {
		name, model, effort, thinking string
		limit                         int64
	}{
		{"Opus medium", "claude-opus-5-5", "medium", "adaptive", 64},
		{"Opus minimum", "claude-opus-5-5", "none", "adaptive", 96},
		{"Opus suffix", "claude-opus-5-5(high)", "medium", "adaptive", 80},
		{"Sonnet medium", "claude-sonnet-5-5", "medium", "adaptive", 64},
		{"Sonnet minimum", "claude-sonnet-5-5", "none", "between_tools", 96},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := NewWithTransport(usageTransport(func(r *http.Request) (*http.Response, error) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				var native struct {
					Max      int64 `json:"max_tokens"`
					Thinking struct {
						Type   string `json:"type"`
						Budget *int64 `json:"budget_tokens"`
					} `json:"thinking"`
					Output struct {
						Effort string `json:"effort"`
					} `json:"output_config"`
				}
				if err := json.Unmarshal(raw, &native); err != nil {
					t.Fatal(err)
				}
				effort := test.effort
				if effort == "none" {
					effort = "low"
				}
				if strings.Contains(test.model, "(high)") {
					effort = "high"
				}
				if native.Max != test.limit || native.Thinking.Type != test.thinking || native.Thinking.Budget != nil || native.Output.Effort != effort {
					t.Errorf("lost client intent: output limit=%d thinking=%s effort=%s", native.Max, native.Thinking.Type, native.Output.Effort)
				}
				return syntheticClaudeStream(), nil
			}))
			defer client.Close()
			body, _ := json.Marshal(map[string]any{"model": test.model, "messages": []map[string]string{{"role": "user", "content": "Synthetic prompt."}}, "max_tokens": test.limit, "reasoning_effort": test.effort})
			response, err := client.Chat(context.Background(), syntheticClaudeCredential(), body, nil)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
		})
	}
}

func TestClaudeNativeAdaptiveAndResponsesRequests(t *testing.T) {
	for _, native := range []bool{false, true} {
		client := NewWithTransport(usageTransport(func(r *http.Request) (*http.Response, error) {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Max      int `json:"max_tokens"`
				Thinking struct {
					Type, Display string
					Budget        *int `json:"budget_tokens"`
				} `json:"thinking"`
				Output struct {
					Effort string `json:"effort"`
				} `json:"output_config"`
			}
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if body.Max != 128 || body.Thinking.Type != "adaptive" || body.Thinking.Budget != nil || body.Output.Effort != "high" {
				t.Fatal("native/Responses contract lost", body)
			}
			if !native {
				return syntheticClaudeStream(), nil
			}
			if body.Thinking.Display != "omitted" {
				t.Fatal("native display intent changed")
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"msg_synthetic","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"Synthetic response."}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))}, nil
		}))
		var response *Stream
		var err error
		if native {
			response, err = client.Messages(context.Background(), syntheticClaudeCredential(), []byte(`{"model":"claude-opus-5-5","max_tokens":128,"messages":[{"role":"user","content":"Synthetic prompt."}],"thinking":{"type":"adaptive","display":"omitted"},"output_config":{"effort":"high"}}`), nil)
		} else {
			response, err = client.Responses(context.Background(), syntheticClaudeCredential(), []byte(`{"model":"claude-opus-5-5","max_output_tokens":128,"input":"Synthetic prompt.","reasoning":{"effort":"high"}}`), nil, false)
		}
		if err != nil {
			client.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		client.Close()
	}
}

func TestAdaptiveClaudeRejectsUnsupportedNativeControlsBeforeIO(t *testing.T) {
	calls := 0
	client := NewWithTransport(usageTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return syntheticClaudeStream(), nil
	}))
	defer client.Close()
	for _, extra := range []string{
		`"thinking":{"type":"enabled","budget_tokens":4096}`,
		`"thinking":{"type":"disabled"}`,
		`"thinking":{"type":"invalid"}`,
		`"tool_choice":{"type":"any"}`,
		`"thinking":{"type":"adaptive"},"output_config":{"effort":"invalid"}`,
	} {
		body := []byte(`{"model":"claude-opus-5-5","max_tokens":128,"messages":[{"role":"user","content":"Synthetic prompt."}],` + extra + `}`)
		if _, err := client.Messages(context.Background(), syntheticClaudeCredential(), body, nil); !errors.Is(err, ErrInput) {
			t.Fatal("unsupported native control reached execution", err)
		}
	}
	if calls != 0 {
		t.Fatal("rejected requests consumed upstream IO", calls)
	}
}

func TestClaudeDiscoveryUsesConfiguredClientIdentity(t *testing.T) {
	calls := 0
	client := NewWithOptions(Options{ClaudeVersion: func() string { return "2.200.0" }, Transport: usageTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("User-Agent") != "claude-cli/2.200.0 (external, cli)" {
			t.Error("discovery lost the configured client version")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"synthetic-model"}]}`))}, nil
	})})
	defer client.Close()
	models, err := client.Models(context.Background(), syntheticClaudeCredential())
	if err != nil || len(models) != 1 || calls != 1 {
		t.Fatal(models, err, calls)
	}
}
