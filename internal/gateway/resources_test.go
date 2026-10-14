package gateway

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/upstream"
)

func TestResourceRoutingPrioritiesFallbackAndAffinity(t *testing.T) {
	s, ids := providerGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("unexpected provider IO")
		return nil, errors.New("synthetic")
	}))
	ctx := context.Background()
	channel, err := s.accounts.ImportProvider(ctx, "openai", "Synthetic channel", []byte(`{"api_key":"synthetic-key","base_url":"https://relay.example.test/v1"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.accounts.SaveCatalog(ctx, channel.ID, 0, []string{"synthetic-model", "api-only"}, nil, s.now().Unix(), upstream.CatalogSource("openai")); err != nil {
		t.Fatal(err)
	}
	if err := s.accounts.RecordUse(ctx, channel.ID, "synthetic-key", true); err != nil {
		t.Fatal(err)
	}
	set := func(preference string, fallback bool, subscriptions bool) {
		t.Helper()
		resources := []groups.Resource{{Kind: "channel", ID: channel.ID}}
		if subscriptions {
			resources = append(resources, groups.Resource{Kind: "subscription", ID: ids["codex"]})
		}
		if _, err := groups.New(s.db).Save(ctx, 1, groups.Input{Name: "Synthetic resources", Enabled: true, Resources: resources, Routing: &groups.Routing{Preference: preference, AllowAPIFallback: fallback}}); err != nil {
			t.Fatal(err)
		}
	}
	set("api_first", false, true)
	for _, kind := range []Kind{Responses, Messages, Gemini} {
		id, _, err := s.selectAccount(ctx, 1, 1, "", "", "synthetic-model", kind)
		if err != nil || id != channel.ID {
			t.Fatal("API preference ignored", kind, id, err)
		}
	}
	set("subscription_first", false, true)
	if id, _, err := s.selectAccount(ctx, 1, 1, "", "", "synthetic-model", Responses); err != nil || id != ids["codex"] {
		t.Fatal("subscription priority", id, err)
	}
	if _, err := s.accounts.SetEnabled(ctx, ids["codex"], false); err != nil {
		t.Fatal(err)
	}
	if id, _, err := s.selectAccount(ctx, 1, 1, "", "", "synthetic-model", Responses); err == nil {
		t.Fatal("API fallback happened without opt-in", id)
	}
	if state, err := groups.New(s.db).Connection(ctx, 1); err != nil || state == "ready" {
		t.Fatal("blocked API fallback counted as ready", state, err)
	}
	catalog, err := s.GroupCatalog(ctx, 1, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range catalog.Models {
		if model.ID == "api-only" {
			t.Fatal("blocked API model advertised")
		}
	}
	set("subscription_first", true, true)
	bound, _, err := s.selectAccount(ctx, 1, 1, "synthetic-channel-conversation", "", "synthetic-model", Responses)
	if err != nil || bound != channel.ID {
		t.Fatal("opt-in API fallback", bound, err)
	}
	if _, err := s.accounts.SetEnabled(ctx, ids["codex"], true); err != nil {
		t.Fatal(err)
	}
	if id, _, err := s.selectAccount(ctx, 1, 1, "synthetic-channel-conversation", "", "synthetic-model", Responses); err != nil || id != bound {
		t.Fatal("existing channel conversation moved", id, err)
	}
	set("subscription_first", false, true)
	if id, _, err := s.selectAccount(ctx, 1, 1, "synthetic-channel-conversation", "", "synthetic-model", Responses); err == nil || id != bound {
		t.Fatal("bound conversation bypassed updated routing policy", id, err)
	}
	set("subscription_first", false, false)
	if id, _, err := s.selectAccount(ctx, 1, 1, "", "", "api-only", Responses); err != nil || id != channel.ID {
		t.Fatal("API-only resource group blocked", id, err)
	}
	if _, err := groups.New(s.db).Save(ctx, 1, groups.Input{Name: "Synthetic resources", Enabled: true, Resources: []groups.Resource{{Kind: "channel", ID: channel.ID}}, ModelPolicy: &groups.ModelPolicy{Restricted: true, Models: []string{"api-only"}}}); err != nil {
		t.Fatal(err)
	}
	if id, _, err := s.selectAccount(ctx, 1, 1, "", "", "api-only", Responses); err != nil || id != channel.ID {
		t.Fatal("native API model allowlist lost", id, err)
	}
}
