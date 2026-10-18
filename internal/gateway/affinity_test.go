package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/murongg/SubLane/internal/upstream"
)

func TestRequestAffinityRequiresUsableHistory(t *testing.T) {
	for _, test := range []struct {
		name string
		kind Kind
		raw  string
		want affinityMode
	}{
		{"missing assistant content", Chat, `{"messages":[{"role":"assistant"},{"role":"user","content":"Continue"}]}`, pinnedAffinity},
		{"empty assistant content", Responses, `{"input":[{"role":"assistant","content":null},{"role":"user","content":"Continue"}]}`, pinnedAffinity},
		{"complete chat tool history", Chat, `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_synthetic","type":"function","function":{"name":"synthetic","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_synthetic","content":"Synthetic result"},{"role":"user","content":"Continue"}]}`, portableAffinity},
		{"complete response tool history", Responses, `{"input":[{"type":"function_call","call_id":"call_synthetic","name":"synthetic","arguments":"{}"},{"type":"function_call_output","call_id":"call_synthetic","output":"Synthetic result"},{"role":"user","content":"Continue"}]}`, portableAffinity},
		{"tool result delta", Responses, `{"input":[{"type":"function_call_output","call_id":"call_synthetic","output":"Synthetic result"}]}`, pinnedAffinity},
		{"plain compaction", Compact, `{"input":[{"role":"assistant","content":"Synthetic reply"},{"role":"user","content":"Continue"}]}`, pinnedAffinity},
		{"opaque compaction", Compact, `{"input":[{"type":"compaction","encrypted_content":"synthetic-ciphertext"}]}`, continuationAffinity},
	} {
		t.Run(test.name, func(t *testing.T) {
			var input map[string]json.RawMessage
			if err := json.Unmarshal([]byte(test.raw), &input); err != nil {
				t.Fatal(err)
			}
			if got := requestAffinity(input, test.kind); got != test.want {
				t.Fatalf("request affinity: got %v, want %v", got, test.want)
			}
		})
	}
}

func affinityGateway(t *testing.T) (*Service, map[string]string) {
	t.Helper()
	s, ids := providerGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
		return syntheticStream(), nil
	}))
	for provider, id := range ids {
		if err := s.accounts.SaveCatalog(context.Background(), id, 0, []string{"synthetic-" + provider, "synthetic-shared"}, s.now().Unix(), upstream.CatalogSource(provider)); err != nil {
			t.Fatal(err)
		}
	}
	return s, ids
}

func TestAffinityModelSwitchUsesFullHistoryAndPersists(t *testing.T) {
	for _, kind := range []Kind{Chat, Responses} {
		t.Run(map[Kind]string{Chat: "chat", Responses: "responses"}[kind], func(t *testing.T) {
			s, ids := affinityGateway(t)
			ctx := context.Background()
			const session = "synthetic-model-switch"
			initial := `{"model":"synthetic-claude","prompt_cache_key":"synthetic-model-switch","messages":[{"role":"user","content":"Synthetic question"}]}`
			if kind == Responses {
				initial = `{"model":"synthetic-claude","prompt_cache_key":"synthetic-model-switch","input":"Synthetic question"}`
			}
			first, err := s.Open(ctx, 1, 1, []byte(initial), nil, kind)
			if err != nil {
				t.Fatal("initial request", err)
			}
			first.Body.Close()
			raw := `{"model":"synthetic-codex","prompt_cache_key":"synthetic-model-switch","messages":[{"role":"user","content":"Synthetic question"},{"role":"assistant","content":"Synthetic reply"},{"role":"user","content":"Continue"}]}`
			if kind == Responses {
				raw = `{"model":"synthetic-codex","prompt_cache_key":"synthetic-model-switch","input":[{"role":"user","content":"Synthetic question"},{"role":"assistant","content":"Synthetic reply"},{"role":"user","content":"Continue"}]}`
			}
			response, err := s.Open(ctx, 1, 1, []byte(raw), nil, kind)
			if err != nil {
				t.Fatal("full-history model switch rejected", err)
			}
			response.Body.Close()
			reopened := New(ctx, s.db, s.accounts, s.provider)
			defer reopened.Close()
			if id, _, err := reopened.selectAccount(ctx, 1, 1, session, "", "synthetic-codex", kind); err != nil || id != ids["codex"] {
				t.Fatal("switched binding was not persisted", id, err)
			}
			var count int
			if err := s.db.QueryRow("SELECT count(*) FROM account_affinity").Scan(&count); err != nil || count != 1 {
				t.Fatal("model switch created extra bindings", count, err)
			}
		})
	}
}

func TestAffinityModelSwitchRequiresPortableHistory(t *testing.T) {
	for _, test := range []struct {
		name string
		kind Kind
		raw  string
	}{
		{"chat delta", Chat, `{"model":"synthetic-codex","messages":[{"role":"user","content":"Continue"}]}`},
		{"response delta", Responses, `{"model":"synthetic-codex","input":"Continue"}`},
		{"previous response", Responses, `{"model":"synthetic-codex","previous_response_id":"resp_synthetic","input":[{"role":"assistant","content":"Synthetic reply"},{"role":"user","content":"Continue"}]}`},
		{"stored conversation", Responses, `{"model":"synthetic-codex","conversation":"conv_synthetic","input":[{"role":"assistant","content":"Synthetic reply"}]}`},
		{"item reference", Responses, `{"model":"synthetic-codex","input":[{"role":"assistant","content":"Synthetic reply"},{"type":"item_reference","id":"msg_synthetic"}]}`},
		{"encrypted reasoning", Responses, `{"model":"synthetic-codex","input":[{"role":"assistant","content":"Synthetic reply"},{"type":"reasoning","encrypted_content":"synthetic-ciphertext"}]}`},
		{"compaction", Responses, `{"model":"synthetic-codex","input":[{"role":"assistant","content":"Synthetic reply"},{"type":"compaction","encrypted_content":"synthetic-ciphertext"}]}`},
		{"signed thinking", Messages, `{"model":"synthetic-codex","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"Synthetic reasoning","signature":"synthetic-signature"}]},{"role":"user","content":"Continue"}]}`},
		{"redacted thinking", Messages, `{"model":"synthetic-codex","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"synthetic-ciphertext"}]},{"role":"user","content":"Continue"}]}`},
		{"cached Gemini content", Gemini, `{"model":"synthetic-codex","cachedContent":"cachedContents/synthetic","contents":[{"role":"model","parts":[{"text":"Synthetic reply"}]},{"role":"user","parts":[{"text":"Continue"}]}]}`},
		{"Gemini signature", Gemini, `{"model":"synthetic-codex","contents":[{"role":"model","parts":[{"text":"Synthetic reply","thoughtSignature":"synthetic-signature"}]},{"role":"user","parts":[{"text":"Continue"}]}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, ids := affinityGateway(t)
			ctx := context.Background()
			if _, _, err := s.selectAccount(ctx, 1, 1, "synthetic-continuation", "", "synthetic-claude", test.kind); err != nil {
				t.Fatal(err)
			}
			if response, err := s.Open(ctx, 1, 1, []byte(test.raw), http.Header{"Session_id": {"synthetic-continuation"}}, test.kind); !errors.Is(err, ErrAffinityUnavailable) {
				if response != nil {
					response.Body.Close()
				}
				t.Fatal("continuation changed its upstream identity", err)
			}
			if id, _, err := s.selectAccount(ctx, 1, 1, "synthetic-continuation", "", "synthetic-claude", test.kind); err != nil || id != ids["claude"] {
				t.Fatal("rejected switch changed the binding", id, err)
			}
		})
	}
}

func TestAffinityModelSwitchKeepsSupportedModelsOnOriginalAccount(t *testing.T) {
	s, ids := affinityGateway(t)
	ctx := context.Background()
	const session = "synthetic-supported-switch"
	if _, _, err := s.selectAccount(ctx, 1, 1, session, "", "synthetic-claude", Chat); err != nil {
		t.Fatal(err)
	}
	response, err := s.Open(ctx, 1, 1, []byte(`{"model":"synthetic-shared","messages":[{"role":"assistant","content":"Synthetic reply"},{"role":"user","content":"Continue"}]}`), http.Header{"Session_id": {session}}, Chat)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if id, _, err := s.selectAccount(ctx, 1, 1, session, "", "synthetic-shared", Chat); err != nil || id != ids["claude"] {
		t.Fatal("supported model moved to a different provider", id, err)
	}
}

func TestAffinityFullHistoryDoesNotFailOverUnchangedModel(t *testing.T) {
	s, ids := affinityGateway(t)
	ctx := context.Background()
	const session = "synthetic-no-failover"
	if _, _, err := s.selectAccount(ctx, 1, 1, session, "", "synthetic-shared", Messages); err != nil {
		t.Fatal(err)
	}
	if err := s.accounts.SaveCatalog(ctx, ids["claude"], 0, []string{"synthetic-claude"}, s.now().Unix(), upstream.CatalogSource("claude")); err != nil {
		t.Fatal(err)
	}
	response, err := s.Open(ctx, 1, 1, []byte(`{"model":"synthetic-shared","messages":[{"role":"assistant","content":"Synthetic reply"},{"role":"user","content":"Continue"}]}`), http.Header{"Session_id": {session}}, Chat)
	if response != nil {
		response.Body.Close()
	}
	if !errors.Is(err, ErrAffinityUnavailable) {
		t.Fatal("catalog change moved a full-history request", err)
	}
}

func TestAffinityContinuationCannotCrossModelBinding(t *testing.T) {
	s, ids := affinityGateway(t)
	ctx := context.Background()
	if err := s.accounts.SaveCatalog(ctx, ids["codex"], 0, []string{"synthetic-codex", "synthetic-claude"}, s.now().Unix(), upstream.CatalogSource("codex")); err != nil {
		t.Fatal(err)
	}
	const session = "synthetic-old-continuation"
	if id, _, err := s.selectAccount(ctx, 1, 1, session, "", "synthetic-claude", Messages); err != nil || id != ids["claude"] {
		t.Fatal("initial binding", id, err)
	}
	headers := http.Header{"Session_id": {session}}
	response, err := s.Open(ctx, 1, 1, []byte(`{"model":"synthetic-codex","input":[{"role":"assistant","content":"Synthetic reply"},{"role":"user","content":"Continue"}]}`), headers, Responses)
	if err != nil {
		t.Fatal("full-history model switch", err)
	}
	response.Body.Close()
	response, err = s.Open(ctx, 1, 1, []byte(`{"model":"synthetic-claude","input":[{"role":"assistant","content":"Synthetic reply"},{"type":"item_reference","id":"msg_synthetic_original_account"}]}`), headers, Responses)
	if response != nil {
		response.Body.Close()
	}
	if !errors.Is(err, ErrAffinityUnavailable) {
		t.Fatal("old provider state reached the new account", err)
	}
}

func TestAffinityModelSwitchPreservesFailedAndLegacyBindings(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "unavailable target", true: "legacy binding"}[legacy], func(t *testing.T) {
			s, ids := affinityGateway(t)
			ctx := context.Background()
			const session = "synthetic-failed-switch"
			if _, _, err := s.selectAccount(ctx, 1, 1, session, "", "synthetic-claude", Chat); err != nil {
				t.Fatal(err)
			}
			model := "synthetic-unavailable"
			if legacy {
				model = "synthetic-codex"
				if _, err := s.db.Exec("UPDATE account_affinity SET model = ''"); err != nil {
					t.Fatal(err)
				}
			}
			raw := `{"model":"` + model + `","messages":[{"role":"assistant","content":"Synthetic reply"},{"role":"user","content":"Continue"}]}`
			if response, err := s.Open(ctx, 1, 1, []byte(raw), http.Header{"Session_id": {session}}, Chat); err == nil {
				response.Body.Close()
				t.Fatal("unavailable or unknown model binding moved")
			}
			if id, _, err := s.selectAccount(ctx, 1, 1, session, "", "synthetic-claude", Chat); err != nil || id != ids["claude"] {
				t.Fatal("failed switch lost the original account", id, err)
			}
			if legacy {
				response, err := s.Open(ctx, 1, 1, []byte(raw), http.Header{"Session_id": {session}}, Chat)
				if err != nil {
					t.Fatal("supported request did not establish legacy model metadata", err)
				}
				response.Body.Close()
			}
		})
	}
}

func TestAffinityContinuationKeepsItsAccount(t *testing.T) {
	for _, test := range []struct {
		name, model, raw string
		kind             Kind
		want             error
	}{
		{"item reference", "synthetic-codex", `{"model":"synthetic-codex","input":[{"type":"item_reference","id":"msg_synthetic"}]}`, Responses, nil},
		{"encrypted reasoning", "synthetic-codex", `{"model":"synthetic-codex","input":[{"type":"reasoning","encrypted_content":"synthetic-ciphertext"},{"role":"user","content":"Continue"}]}`, Responses, nil},
		{"stateless HTTP continuation", "synthetic-codex", `{"model":"synthetic-codex","previous_response_id":"resp_synthetic","input":"Continue"}`, Responses, upstream.ErrContinuation},
		{"signed thinking", "synthetic-claude", `{"model":"synthetic-claude","max_tokens":10,"stream":true,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"Synthetic reasoning","signature":"synthetic-signature"}]},{"role":"user","content":"Continue"}]}`, Messages, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _ := affinityGateway(t)
			ctx := context.Background()
			const session = "synthetic-retained-continuation"
			bound, _, err := s.selectAccount(ctx, 1, 1, session, "", test.model, test.kind)
			if err != nil {
				t.Fatal(err)
			}
			response, err := s.Open(ctx, 1, 1, []byte(test.raw), http.Header{"Session_id": {session}}, test.kind)
			if response != nil {
				response.Body.Close()
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("continuation: got %v, want %v", err, test.want)
			}
			if id, _, err := s.selectAccount(ctx, 1, 1, session, "", test.model, test.kind); err != nil || id != bound {
				t.Fatal("continuation changed its binding", id, err)
			}
		})
	}
}

func TestAffinityModelSwitchChecksDestinationQuota(t *testing.T) {
	for _, exhausted := range []string{"claude", "codex"} {
		t.Run(exhausted, func(t *testing.T) {
			s, ids := affinityGateway(t)
			ctx := context.Background()
			const session = "synthetic-quota-switch"
			if _, _, err := s.selectAccount(ctx, 1, 1, session, "", "synthetic-claude", Chat); err != nil {
				t.Fatal(err)
			}
			saveExhaustedQuota(t, s, ids[exhausted])
			response, err := s.Open(ctx, 1, 1, []byte(`{"model":"synthetic-codex","messages":[{"role":"assistant","content":"Synthetic reply"},{"role":"user","content":"Continue"}]}`), http.Header{"Session_id": {session}}, Chat)
			if response != nil {
				response.Body.Close()
			}
			if exhausted == "claude" && err != nil || exhausted == "codex" && !errors.Is(err, ErrQuotaExhausted) {
				t.Fatal("model switch used the wrong provider quota", err)
			}
		})
	}
}
