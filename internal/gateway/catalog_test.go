package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/auth"
	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/upstream"
)

func TestCatalogUnionsAccountsAndRoutesOnlySupportedModels(t *testing.T) {
	ctx := context.Background()
	var selected string
	s, ids := discoveryGateway(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			models := `{"models":[{"slug":"synthetic-shared"},{"slug":"synthetic-basic"}]}`
			if r.Header.Get("Chatgpt-Account-Id") == "synthetic-premium" {
				models = `{"models":[{"slug":"synthetic-shared"},{"slug":"synthetic-premium-only"}]}`
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(models))}, nil
		}
		selected = r.Header.Get("Chatgpt-Account-Id")
		return syntheticStream(), nil
	}))
	extra, err := s.accounts.Authorize(ctx, "Synthetic premium", accounts.Credential{AccountID: "synthetic-premium", AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()}, "")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := groups.New(s.db).Save(ctx, 0, groups.Input{Name: "Mixed", Enabled: true, AccountIDs: []string{ids["codex"], extra.ID}})
	if err != nil {
		t.Fatal(err)
	}
	models, err := s.Models(ctx, 1, pool.ID)
	if err != nil || len(models) != 3 {
		t.Fatalf("expected three native models, got %+v: %v", models, err)
	}
	for range 3 {
		response, err := s.Open(ctx, 1, pool.ID, []byte(`{"model":"codex/synthetic-premium-only","input":"synthetic"}`), nil, Responses)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if selected != "synthetic-premium" {
			t.Fatal("model sent to an unsupported account")
		}
	}
	sticky := http.Header{"Session_id": {"synthetic-catalog-session"}}
	response, err := s.Open(ctx, 1, pool.ID, []byte(`{"model":"synthetic-premium-only","input":"synthetic"}`), sticky, Responses)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if _, err := s.Open(ctx, 1, pool.ID, []byte(`{"model":"synthetic-basic","input":"synthetic"}`), sticky, Responses); !errors.Is(err, ErrAffinityUnavailable) {
		t.Fatal("sticky model change moved the conversation", err)
	}
	if _, err := groups.New(s.db).Save(ctx, pool.ID, groups.Input{Name: pool.Name, Enabled: true, AccountIDs: []string{ids["codex"], extra.ID}, ModelPolicy: &groups.ModelPolicy{Restricted: true, Models: []string{"synthetic-shared"}}}); err != nil {
		t.Fatal(err)
	}
	models, err = s.Models(ctx, 1, pool.ID)
	if err != nil || len(models) != 1 || models[0].ID != "synthetic-shared" {
		t.Fatal("automatic catalog overwrote group policy", models, err)
	}
	if _, err := groups.New(s.db).Save(ctx, pool.ID, groups.Input{Name: pool.Name, Enabled: true, AccountIDs: []string{ids["codex"], extra.ID}, ModelPolicy: &groups.ModelPolicy{Restricted: true, Models: []string{"synthetic-shared(high)"}}}); err != nil {
		t.Fatal(err)
	}
	models, err = s.Models(ctx, 1, pool.ID)
	if err != nil || len(models) != 1 || models[0].ID != "synthetic-shared(high)" {
		t.Fatal("explicit thinking variant disappeared", models, err)
	}
	response, err = s.Open(ctx, 1, pool.ID, []byte(`{"model":"synthetic-shared(high)","input":"synthetic"}`), nil, Responses)
	if err != nil {
		t.Fatal("thinking variant could not route", err)
	}
	response.Body.Close()
}

func TestCatalogCacheSurvivesRestartAndRetainsFailedRefresh(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	var failing atomic.Bool
	s, ids := discoveryGateway(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if failing.Load() {
			return nil, errors.New("synthetic failure")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"synthetic-model"}]}`))}, nil
	}))
	first, err := s.AccountCatalog(ctx, ids["codex"], true)
	if err != nil || !first.Known || len(first.Models) != 1 {
		t.Fatal(first, err)
	}
	reopened := New(ctx, s.db, s.accounts, s.provider)
	defer reopened.Close()
	cached, err := reopened.AccountCatalog(ctx, ids["codex"], false)
	if err != nil || calls.Load() != 1 || cached.UpdatedAt != first.UpdatedAt {
		t.Fatal("cache was not persistent", cached, err)
	}
	reopened.catalog.now = func() time.Time { return time.Unix(first.UpdatedAt, 0).Add(20 * time.Minute) }
	failing.Store(true)
	stale, err := reopened.AccountCatalog(ctx, ids["codex"], true)
	if err != nil || !stale.Stale || !stale.RefreshFailed || !stale.Known || stale.UpdatedAt != first.UpdatedAt || len(stale.Models) != 1 {
		t.Fatal("failed refresh lost snapshot", stale, err)
	}
	before := calls.Load()
	_, _ = reopened.AccountCatalog(ctx, ids["codex"], true)
	if calls.Load() != before {
		t.Fatal("failure backoff bypassed")
	}
	if _, err := s.accounts.SetEnabled(ctx, ids["codex"], false); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.AccountCatalog(ctx, ids["codex"], false); !errors.Is(err, accounts.ErrDisabled) {
		t.Fatal("disabled account served catalog", err)
	}
}

func TestCatalogEmptyObservationIsNotUnknown(t *testing.T) {
	ctx := context.Background()
	s, ids := discoveryGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[]}`))}, nil
	}))
	if _, err := groups.New(s.db).Save(ctx, 1, groups.Input{Name: "Default", Enabled: true, AccountIDs: []string{ids["codex"]}}); err != nil {
		t.Fatal(err)
	}
	catalog, err := s.AccountCatalog(ctx, ids["codex"], true)
	if err != nil || !catalog.Known || len(catalog.Models) != 0 {
		t.Fatal("known empty observation lost", catalog, err)
	}
	models, err := s.Models(ctx, 1, 1)
	if err != nil || len(models) != 0 {
		t.Fatal(models, err)
	}
	if _, err := s.Open(ctx, 1, 1, []byte(`{"model":"synthetic-unknown","input":"synthetic"}`), nil, Responses); !errors.Is(err, ErrModelUnavailable) {
		t.Fatal("unknown model was forwarded", err)
	}
	encoded, _ := json.Marshal(catalog)
	if strings.Contains(string(encoded), "synthetic-access") {
		t.Fatal("catalog leaked credentials")
	}
}

func TestCatalogRefreshCoalescesAndRejectsReauthorizationRace(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	s, ids := discoveryGateway(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"synthetic-old"}]}`))}, nil
	}))
	first, err := s.AccountCatalog(ctx, ids["codex"], false)
	if err != nil || first.Known || !first.Refreshing {
		t.Fatal(first, err)
	}
	<-started
	for range 5 {
		value, err := s.AccountCatalog(ctx, ids["codex"], false)
		if err != nil || !value.Refreshing {
			t.Fatal(value, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("concurrent reads duplicated discovery")
	}
	_, err = s.accounts.Authorize(ctx, "Synthetic reauthorized", accounts.Credential{AccountID: "synthetic-subject", AccessToken: "synthetic-new", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()}, ids["codex"])
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	s.catalog.mu.Lock()
	flight := s.catalog.entries[ids["codex"]].flight
	s.catalog.mu.Unlock()
	if flight != nil {
		<-flight
	}
	saved, err := s.accounts.Catalog(ctx, ids["codex"])
	if err != nil || saved.UpdatedAt != 0 {
		t.Fatal("old authorization published a snapshot", saved, err)
	}
}

func TestCatalogShutdownCancelsRefreshAndExpiredSnapshotsCannotRoute(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	s, ids := discoveryGateway(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	catalog, _ := s.accounts.Catalog(ctx, ids["codex"])
	if err := s.accounts.SaveCatalog(ctx, ids["codex"], catalog.Revision, []string{"synthetic-model"}, nil, time.Now().Add(-25*time.Hour).Unix(), upstream.CatalogSource("codex")); err != nil {
		t.Fatal(err)
	}
	if _, err := groups.New(s.db).Save(ctx, 1, groups.Input{Name: "Default", Enabled: true, AccountIDs: []string{ids["codex"]}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Models(ctx, 1, 1); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatal("expired catalog advertised", err)
	}
	<-started
	if _, err := s.Open(ctx, 1, 1, []byte(`{"model":"synthetic-model","input":"synthetic"}`), nil, Responses); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatal("expired catalog routed", err)
	}
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not cancel catalog refresh")
	}
}

func TestCatalogCannotPublishAcrossGroupAccessRevocation(t *testing.T) {
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	s, ids := discoveryGateway(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"synthetic-private-model"}]}`))}, nil
	}))
	identity, err := auth.New(s.db)
	if err != nil {
		t.Fatal(err)
	}
	member, err := identity.CreateMember(ctx, "synthetic-catalog-member", "synthetic-pass")
	if err != nil {
		t.Fatal(err)
	}
	manager := groups.New(s.db)
	pool, err := manager.Save(ctx, 0, groups.Input{Name: "Synthetic private pool", Enabled: true, AccountIDs: []string{ids["codex"]}})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetMemberGroups(ctx, member.ID, []int64{pool.ID}); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := s.Models(ctx, member.ID, pool.ID); finished <- err }()
	<-started
	if err := manager.SetMemberGroups(ctx, member.ID, []int64{}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-finished; !errors.Is(err, groups.ErrUnavailable) {
		t.Fatal("catalog crossed revoked group access", err)
	}
}

func TestCatalogRefreshesAfterDiscoverySourceChanges(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	s, ids := discoveryGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"synthetic-current"},{"slug":"synthetic-new-release"}]}`))}, nil
	}))
	if _, err := groups.New(s.db).Save(ctx, 1, groups.Input{Name: "Default", Enabled: true, AccountIDs: []string{ids["codex"]}}); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"", "codex:0.1.0"} {
		saved := map[string]any{"models": []string{"synthetic-current"}, "updated_at": time.Now().Unix(), "source": source}
		raw, _ := json.Marshal(saved)
		if _, err := s.db.Exec("UPDATE accounts SET models_snapshot=?,models_revision=models_revision+1 WHERE id=?", raw, ids["codex"]); err != nil {
			t.Fatal(err)
		}
		before := calls.Load()
		models, err := s.Models(ctx, 1, 1)
		if err != nil || len(models) != 2 || calls.Load() != before+1 {
			t.Fatalf("fresh timestamp masked obsolete discovery source: count=%d calls=%d err=%v", len(models), calls.Load(), err)
		}
		catalog, err := s.accounts.Catalog(ctx, ids["codex"])
		if err != nil || len(catalog.Models) != 2 || catalog.Source != upstream.CatalogSource("codex") {
			t.Fatal("new catalog not persisted", catalog, err)
		}
		if _, err := s.Models(ctx, 1, 1); err != nil || calls.Load() != before+1 {
			t.Fatal("current source was fetched again", err)
		}
	}
}

func TestVersionChangeDiscardsAnInFlightCatalog(t *testing.T) {
	ctx := context.Background()
	var version atomic.Value
	version.Store("0.200.0")
	started := make(chan struct{})
	release := make(chan struct{})
	s, ids := providerFixture(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("client_version") == "0.200.0" {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"synthetic-model"}]}`))}, nil
	}), false, func() string { return version.Load().(string) })
	if _, err := s.AccountCatalog(ctx, ids["codex"], false); err != nil {
		t.Fatal(err)
	}
	<-started
	s.catalog.mu.Lock()
	flight := s.catalog.entries[ids["codex"]].flight
	s.catalog.mu.Unlock()
	version.Store("0.300.0")
	close(release)
	<-flight
	old, err := s.accounts.Catalog(ctx, ids["codex"])
	if err != nil || old.UpdatedAt != 0 {
		t.Fatal("outdated request published after a version change", old, err)
	}
	if _, err := s.Check(ctx, ids["codex"]); err != nil {
		t.Fatal("old backoff blocked new version discovery", err)
	}
	current, err := s.accounts.Catalog(ctx, ids["codex"])
	if err != nil || current.Source != "codex:v1:0.300.0" {
		t.Fatal(current, err)
	}
}

func TestCatalogRoutesHiddenModelsWithoutListingThem(t *testing.T) {
	ctx := context.Background()
	catalog := `{"models":[{"slug":"synthetic-visible","visibility":"list"},{"slug":"synthetic-internal","visibility":"hide"}]}`
	var routed atomic.Int32
	s, ids := discoveryGateway(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(catalog))}, nil
		}
		routed.Add(1)
		return syntheticStream(), nil
	}))
	pool, err := groups.New(s.db).Save(ctx, 0, groups.Input{Name: "Hidden", Enabled: true, AccountIDs: []string{ids["codex"]}})
	if err != nil {
		t.Fatal(err)
	}
	models, err := s.Models(ctx, 1, pool.ID)
	if err != nil || len(models) != 1 || models[0].ID != "synthetic-visible" {
		t.Fatal("hidden model was listed publicly", models, err)
	}
	snapshot, err := s.AccountCatalog(ctx, ids["codex"], false)
	if err != nil || len(snapshot.HiddenModels) != 1 || snapshot.HiddenModels[0] != "synthetic-internal" {
		t.Fatal("administrator catalog omitted hidden model", snapshot, err)
	}
	response, err := s.Open(ctx, 1, pool.ID, []byte(`{"model":"synthetic-internal","input":"synthetic"}`), nil, Responses)
	if err != nil {
		t.Fatal("hidden model could not route", err)
	}
	response.Body.Close()
	if routed.Load() != 1 {
		t.Fatal("hidden model request did not reach upstream")
	}
	if _, err := groups.New(s.db).Save(ctx, pool.ID, groups.Input{Name: pool.Name, Enabled: true, AccountIDs: []string{ids["codex"]}, ModelPolicy: &groups.ModelPolicy{Restricted: true, Models: []string{"synthetic-visible"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, 1, pool.ID, []byte(`{"model":"synthetic-internal","input":"synthetic"}`), nil, Responses); err == nil {
		t.Fatal("hidden model bypassed the pool allowlist")
	}
}

func TestCatalogIgnoresHiddenModelsWithoutVisibleModels(t *testing.T) {
	ctx := context.Background()
	s, ids := discoveryGateway(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			// Obsolete clients can receive only hidden models; they do not describe current capabilities.
			body := `{"models":[{"slug":"synthetic-internal","visibility":"hide"}]}`
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		t.Error("unsupported model reached upstream")
		return syntheticStream(), nil
	}))
	pool, err := groups.New(s.db).Save(ctx, 0, groups.Input{Name: "Obsolete", Enabled: true, AccountIDs: []string{ids["codex"]}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, 1, pool.ID, []byte(`{"model":"synthetic-internal","input":"synthetic"}`), nil, Responses); !errors.Is(err, ErrModelUnavailable) {
		t.Fatal("hidden-only catalog granted support", err)
	}
}
