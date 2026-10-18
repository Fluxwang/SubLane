package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/murongg/SubLane/internal/storage/db"
	"github.com/murongg/SubLane/internal/upstream"
)

func leaseSyntheticModel(t *testing.T, s *Service) *observation {
	t.Helper()
	e, err := s.begin(context.Background(), 1, 1, Gemini)
	if err != nil {
		t.Fatal(err)
	}
	e.record.Model = "antigravity/synthetic-model"
	e.record.Provider = "antigravity"
	id, _, err := s.leaseAccount(context.Background(), e, "", "antigravity", "synthetic-model", Gemini, pinnedAffinity)
	if err != nil {
		e.fail(err)
		t.Fatal(err)
	}
	e.record.AccountID = id
	return e
}

func TestModelCooldownRejectsLateSuccessAndLateFailureAfterResume(t *testing.T) {
	s, id := modelLimitFixture(t, syntheticModelLimit)
	clock := s.now()
	s.now = func() time.Time { return clock }
	older, newer := leaseSyntheticModel(t, s), leaseSyntheticModel(t, s)
	newer.fail(&upstream.UpstreamError{Status: 429, RetryAfter: "120", LimitedModel: "synthetic-model"})
	clock = clock.Add(121 * time.Second)
	older.finish("success", "", "", "")
	row, err := s.queries.GetAccountModelRuntime(context.Background(), db.GetAccountModelRuntimeParams{TargetID: id, WorkspaceID: 1, Model: "synthetic-model"})
	if err != nil || row.Failures != 1 {
		t.Fatal("late success erased newer model failure", row, err)
	}
	recovery := leaseSyntheticModel(t, s)
	recovery.finish("success", "", "", "")
	late := leaseSyntheticModel(t, s)
	if err := s.Resume(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	late.fail(&upstream.UpstreamError{Status: 429, RetryAfter: "120", LimitedModel: "synthetic-model"})
	row, err = s.queries.GetAccountModelRuntime(context.Background(), db.GetAccountModelRuntimeParams{TargetID: id, WorkspaceID: 1, Model: "synthetic-model"})
	if err != nil || row.Failures != 0 {
		t.Fatal("late failure restored manually cleared model state", row, err)
	}
}

func TestModelCooldownKeepsFailedWritesClosed(t *testing.T) {
	s, id := modelLimitFixture(t, syntheticModelLimit)
	if _, err := s.db.Exec(`CREATE TRIGGER synthetic_model_write_failure BEFORE INSERT ON account_model_runtime BEGIN SELECT RAISE(ABORT,'synthetic-write-failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := openSyntheticModel(t, s, "synthetic-model", ""); err == nil {
		t.Fatal("limit accepted")
	}
	if _, _, err := s.selectAccount(context.Background(), 1, 1, "", "antigravity", "synthetic-model", Gemini); err == nil || err.Error() != "model_cooling" {
		t.Fatal("failed persistence reopened the model", err)
	}
	if err := openSyntheticModel(t, s, "synthetic-other", ""); err != nil {
		t.Fatal("failed model persistence blocked unrelated model", err)
	}
	states, err := s.Runtime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		if state.ID == id && state.LimitedModels != 1 {
			t.Fatal("unpersisted model limit disappeared from diagnostics", state)
		}
	}
}

func TestObsoleteModelStateCannotExhaustRuntimeCapacity(t *testing.T) {
	s, id := modelLimitFixture(t, syntheticModelLimit)
	for i := range maxModelRuntime {
		key := modelKey{id, "synthetic-obsolete-" + strconv.Itoa(i)}
		s.modelHealth[key] = &modelRuntime{key: key, lifecycle: -1, runtimeRevision: -1, persistFailed: true, failures: 1}
	}
	e := leaseSyntheticModel(t, s)
	e.finish("success", "", "", "")
	if len(s.modelHealth) != 0 {
		t.Fatal("invalid authorization retained model state", len(s.modelHealth))
	}
}

const syntheticModelLimit = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"synthetic-private","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED","domain":"cloudcode-pa.googleapis.com","metadata":{"model":"synthetic-model","quotaResetDelay":"120s"}}]}}`

func modelLimitFixture(t *testing.T, failure string) (*Service, string) {
	t.Helper()
	s, ids := providerGateway(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		var input struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			t.Error("invalid synthetic generation body")
		}
		if input.Model == "synthetic-model" {
			return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": {"120"}}, Body: io.NopCloser(strings.NewReader(failure))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"response":{"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Synthetic response"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}}`))}, nil
	}))
	id := ids["antigravity"]
	catalog, err := s.accounts.Catalog(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.accounts.SaveCatalog(context.Background(), id, catalog.Revision, []string{"synthetic-model", "synthetic-other"}, s.now().Unix(), upstream.CatalogSource("antigravity")); err != nil {
		t.Fatal(err)
	}
	return s, id
}

func openSyntheticModel(t *testing.T, s *Service, model, session string) error {
	t.Helper()
	raw := `{"model":"antigravity/` + model + `","contents":[{"parts":[{"text":"Synthetic input"}]}]}`
	x, err := s.Open(context.Background(), 1, 1, []byte(raw), http.Header{"Session_id": {session}}, Gemini)
	if x != nil {
		defer x.Body.Close()
		if x.StatusCode >= 400 {
			return &upstream.UpstreamError{Status: x.StatusCode, RetryAfter: x.Header.Get("Retry-After")}
		}
		body, readErr := io.ReadAll(x.Body)
		if readErr != nil {
			return readErr
		}
		return x.AcceptGemini(body)
	}
	return err
}

func TestModelRateLimitPreservesOtherModelsAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	s, id := modelLimitFixture(t, syntheticModelLimit)
	if bound, _, err := s.selectAccount(ctx, 1, 1, "synthetic-binding", "antigravity", "synthetic-model", Gemini); err != nil || bound != id {
		t.Fatal("initial binding", bound, err)
	}
	var rejected *upstream.UpstreamError
	if err := openSyntheticModel(t, s, "synthetic-model", "synthetic-binding"); !errors.As(err, &rejected) || rejected.Status != 429 {
		t.Fatal("missing upstream limit", err)
	}
	states, err := s.Runtime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		if state.ID == id && (state.Failures != 0 || state.State != "available") {
			t.Fatal("model failure penalized its whole account", state)
		}
	}
	if err := openSyntheticModel(t, s, "synthetic-other", ""); err != nil {
		t.Fatal("unrelated model was blocked", err)
	}
	restarted := New(ctx, s.db, s.accounts, s.provider)
	defer restarted.Close()
	bound, _, err := restarted.selectAccount(ctx, 1, 1, "synthetic-binding", "antigravity", "synthetic-model", Gemini)
	if err == nil || err.Error() != "model_cooling" || bound != id {
		t.Fatal("model cooldown lost or binding changed", bound, err)
	}
	if _, _, err := restarted.selectAccount(ctx, 1, 1, "", "antigravity", "synthetic-other", Gemini); err != nil {
		t.Fatal("restart blocked unrelated model", err)
	}
	page, err := s.Requests(ctx, RequestFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range page.Requests {
		if record.ErrorCode == "model_rate_limited" {
			found = true
		}
	}
	if !found {
		t.Fatal("model scope missing from safe diagnostics")
	}
	if err := restarted.Resume(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := restarted.selectAccount(ctx, 1, 1, "synthetic-binding", "antigravity", "synthetic-model", Gemini); err != nil {
		t.Fatal("manual cooldown clear did not cover models", err)
	}
}

func TestUnknownRateLimitRemainsAccountWide(t *testing.T) {
	for _, failure := range []string{`{"error":{"code":429}}`, strings.ReplaceAll(syntheticModelLimit, "synthetic-model", "synthetic-unrelated"), strings.ReplaceAll(syntheticModelLimit, "cloudcode-pa.googleapis.com", "unknown.example.test")} {
		s, id := modelLimitFixture(t, failure)
		if err := openSyntheticModel(t, s, "synthetic-model", ""); err == nil {
			t.Fatal("limit accepted")
		}
		if _, _, err := s.selectAccount(context.Background(), 1, 1, "", "antigravity", "synthetic-other", Gemini); !errors.Is(err, ErrAccountCooling) {
			t.Fatal("uncertain scope weakened account cooldown", id, err)
		}
	}
}

func TestModelCooldownIsInvalidatedByAccountLifecycle(t *testing.T) {
	s, id := modelLimitFixture(t, syntheticModelLimit)
	if err := openSyntheticModel(t, s, "synthetic-model", ""); err == nil {
		t.Fatal("limit accepted")
	}
	for _, enabled := range []bool{false, true} {
		if _, err := s.accounts.SetEnabled(context.Background(), id, enabled); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.selectAccount(context.Background(), 1, 1, "", "antigravity", "synthetic-model", Gemini); err != nil {
		t.Fatal("old model cooldown survived lifecycle change", err)
	}
}

func TestModelCooldownSerializesRecovery(t *testing.T) {
	s, id := modelLimitFixture(t, syntheticModelLimit)
	clock := s.now().Add(time.Minute)
	s.now = func() time.Time { return clock }
	if err := openSyntheticModel(t, s, "synthetic-model", ""); err == nil {
		t.Fatal("limit accepted")
	}
	clock = clock.Add(121 * time.Second)
	entry, err := s.begin(context.Background(), 1, 1, Gemini)
	if err != nil {
		t.Fatal(err)
	}
	entry.record.Model = "antigravity/synthetic-model"
	entry.record.Provider = "antigravity"
	bound, _, err := s.leaseAccount(context.Background(), entry, "", "antigravity", "synthetic-model", Gemini, pinnedAffinity)
	if err != nil || bound != id {
		t.Fatal("recovery request rejected", bound, err)
	}
	entry.record.AccountID = bound
	defer entry.finish("success", "", "", "")
	if _, _, err := s.selectAccount(context.Background(), 1, 1, "", "antigravity", "synthetic-model", Gemini); !errors.Is(err, ErrAccountBusy) {
		t.Fatal("concurrent recovery admitted", err)
	}
	if _, _, err := s.selectAccount(context.Background(), 1, 1, "", "antigravity", "synthetic-other", Gemini); err != nil {
		t.Fatal("recovery blocked other models", err)
	}
}
