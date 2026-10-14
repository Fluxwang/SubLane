package gateway

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/upstream"
)

func TestAvailabilityUsesLocalAdmissionWithoutRoutingOrInference(t *testing.T) {
	ctx := context.Background()
	s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("availability contacted upstream")
		return nil, nil
	}))
	id := ids["codex"]
	catalog, err := s.accounts.Catalog(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.accounts.SaveCatalog(ctx, id, catalog.Revision, []string{"synthetic-model"}, nil, s.now().Unix(), upstream.CatalogSource("codex")); err != nil {
		t.Fatal(err)
	}
	value, err := s.Availability(ctx, 1, 1, "synthetic-model")
	if err != nil || value.State != "available" || value.Available != 1 {
		t.Fatalf("available: %+v %v", value, err)
	}
	saveExhaustedQuota(t, s, id)
	value, err = s.Availability(ctx, 1, 1, "synthetic-model")
	if err != nil || value.State != "unavailable" || len(value.Accounts) != 1 || value.Accounts[0].Reason != "quota_exhausted" {
		t.Fatalf("quota: %+v %v", value, err)
	}
	if _, err := s.db.Exec("UPDATE account_usage SET updated_at=updated_at-121"); err != nil {
		t.Fatal(err)
	}
	value, err = s.Availability(ctx, 1, 1, "synthetic-model")
	if err != nil || value.State != "available" {
		t.Fatalf("stale quota: %+v %v", value, err)
	}
	if _, err := s.db.Exec("UPDATE accounts SET models_snapshot=NULL WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	value, err = s.Availability(ctx, 1, 1, "synthetic-model")
	if err != nil || value.State != "unknown" {
		t.Fatalf("unknown catalog: %+v %v", value, err)
	}
	var bindings int
	if err := s.db.QueryRow("SELECT count(*) FROM account_affinity").Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 0 {
		t.Fatal("inspection created a conversation binding")
	}
	pools, err := s.WorkspaceCapacity(ctx)
	if err != nil || len(pools) != 1 || !pools[0].Unknown {
		t.Fatal("invalidated catalog remains unknown", pools, err)
	}
}

func TestAvailabilityRespectsPolicyAndWorkspaceAccess(t *testing.T) {
	ctx := context.Background()
	s, _ := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) { return syntheticStream(), nil }))
	pool, err := groups.New(s.db).Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = groups.New(s.db).Save(ctx, 1, groups.Input{Name: pool.Name, Enabled: true, AccountIDs: pool.AccountIDs, ModelPolicy: &groups.ModelPolicy{Restricted: true, Models: []string{"synthetic-allowed"}}})
	if err != nil {
		t.Fatal(err)
	}
	value, err := s.Availability(ctx, 1, 1, "synthetic-denied")
	if err != nil || value.State != "unavailable" || value.Reasons[0].Code != "model_not_allowed" {
		t.Fatal(value, err)
	}
	if _, err := s.Availability(ctx, 999, 1, "synthetic-allowed"); !errors.Is(err, groups.ErrUnavailable) {
		t.Fatal("unauthorized diagnostics", err)
	}
	if _, err := s.Availability(ctx, 1, 1, ""); !errors.Is(err, upstream.ErrInput) {
		t.Fatal("empty model", err)
	}
	if _, err := s.Availability(ctx, 1, 999, "synthetic-allowed"); !errors.Is(err, groups.ErrUnavailable) {
		t.Fatal("foreign pool", err)
	}
}

func TestAvailabilityReportsModelLimitsAndCurrentBusyState(t *testing.T) {
	ctx := context.Background()
	s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) { return syntheticStream(), nil }))
	id := ids["codex"]
	catalog, _ := s.accounts.Catalog(ctx, id)
	if err := s.accounts.SaveCatalog(ctx, id, catalog.Revision, []string{"synthetic-model", "synthetic-other"}, nil, s.now().Unix(), upstream.CatalogSource("codex")); err != nil {
		t.Fatal(err)
	}
	if err := s.loadRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	key := modelKey{id, "synthetic-model"}
	s.modelHealth[key] = &modelRuntime{key: key, lifecycle: catalog.Revision, runtimeRevision: s.health[id].revision, failures: 1, cooldownUntil: s.now().Add(time.Minute).Unix(), persistFailed: true}
	value, err := s.Availability(ctx, 1, 1, "synthetic-model")
	if err != nil || value.State != "unavailable" || value.Accounts[0].Reason != "model_cooling" || value.RetryAt <= s.now().Unix() {
		t.Fatal(value, err)
	}
	value, err = s.Availability(ctx, 1, 1, "synthetic-other")
	if err != nil || value.State != "available" {
		t.Fatal("unrelated model blocked", value, err)
	}
	s.health[id].InFlight = s.health[id].MaxConcurrency
	value, err = s.Availability(ctx, 1, 1, "synthetic-other")
	if err != nil || value.Accounts[0].Reason != "account_busy" {
		t.Fatal(value, err)
	}
}
