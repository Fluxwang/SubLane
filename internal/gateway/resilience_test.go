package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/upstream"
)

func TestTransientFailuresAffectOnlyRequestedModel(t *testing.T) {
	ctx := context.Background()
	s, ids := codexGateway(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "synthetic-other") {
			return syntheticStream(), nil
		}
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}))
	catalog, err := s.accounts.Catalog(ctx, ids["codex"])
	if err != nil {
		t.Fatal(err)
	}
	if err := s.accounts.SaveCatalog(ctx, ids["codex"], catalog.Revision, []string{"synthetic-model", "synthetic-other"}, time.Now().Unix(), upstream.CatalogSource("codex")); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		x, err := s.Open(ctx, 1, 1, []byte(`{"model":"synthetic-model","input":"synthetic"}`), nil, Responses)
		if err != nil {
			t.Fatal(err)
		}
		x.Body.Close()
	}
	if _, err := s.Open(ctx, 1, 1, []byte(`{"model":"synthetic-model","input":"synthetic"}`), nil, Responses); !errors.Is(err, ErrModelCooling) {
		t.Fatalf("expected model cooldown, got %v", err)
	}
	x, err := s.Open(ctx, 1, 1, []byte(`{"model":"synthetic-other","input":"synthetic"}`), nil, Responses)
	if err != nil {
		t.Fatalf("unrelated model blocked: %v", err)
	}
	if err := x.Events(func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	x.Body.Close()
}

func TestRetryRechecksKeyRevocation(t *testing.T) {
	var s *Service
	var key int64
	var calls atomic.Int32
	s, _ = codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		if _, err := s.db.ExecContext(context.Background(), "UPDATE api_keys SET revoked_at=1 WHERE id=?", key); err != nil {
			t.Error(err)
		}
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}))
	result, err := s.db.ExecContext(context.Background(), "INSERT INTO api_keys(user_id,group_id,name,prefix,token_hash,created_at) VALUES(1,1,'synthetic','sl_fake',randomblob(32),1)")
	if err != nil {
		t.Fatal(err)
	}
	key, err = result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Open(WithRequestIdentity(context.Background(), key, "http"), 1, 1, []byte(`{"model":"synthetic-model","input":"synthetic"}`), nil, Responses)
	if !errors.Is(err, groups.ErrUnavailable) || calls.Load() != 1 {
		t.Fatalf("revoked key retried: calls=%d err=%v", calls.Load(), err)
	}
}

func TestRetryHonorsCancellationAndAccountDisablement(t *testing.T) {
	for _, canceled := range []bool{true, false} {
		t.Run(map[bool]string{true: "canceled", false: "disabled"}[canceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			var s *Service
			var ids map[string]string
			s, ids = codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				if canceled {
					cancel()
				} else if _, err := s.accounts.SetEnabled(context.Background(), ids["codex"], false); err != nil {
					t.Error(err)
				}
				return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			}))
			s.policy.RetryDelay = time.Millisecond
			_, err := s.Open(ctx, 1, 1, []byte(`{"model":"synthetic-model","input":"synthetic"}`), nil, Responses)
			want := accounts.ErrDisabled
			if canceled {
				want = context.Canceled
			}
			if !errors.Is(err, want) || calls.Load() != 1 {
				t.Fatalf("retry bypassed %v: calls=%d err=%v", want, calls.Load(), err)
			}
		})
	}
}

func TestRetryDoesNotReplayPartialOutputOrLongRetryAfter(t *testing.T) {
	for _, partial := range []bool{true, false} {
		t.Run(map[bool]string{true: "partial output", false: "retry deadline"}[partial], func(t *testing.T) {
			var calls atomic.Int32
			s, _ := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				if partial {
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"synthetic\"}\n\n"))}, nil
				}
				return &http.Response{StatusCode: 503, Header: http.Header{"Retry-After": {"30"}}, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			}))
			x, err := s.Open(context.Background(), 1, 1, []byte(`{"model":"synthetic-model","input":"synthetic"}`), nil, Responses)
			if err != nil {
				t.Fatal(err)
			}
			if partial {
				x.Events(func([]byte) error { return nil })
			}
			x.Body.Close()
			if calls.Load() != 1 {
				t.Fatalf("unsafe replay: %d attempts", calls.Load())
			}
		})
	}
}

func TestTemporaryHTTPFailureRetriesWithoutDuplicateHistory(t *testing.T) {
	var calls atomic.Int32
	s, _ := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		return syntheticStream(), nil
	}))
	x, err := s.Open(context.Background(), 1, 1, []byte(`{"model":"synthetic-model","input":"synthetic"}`), nil, Responses)
	if err != nil {
		t.Fatal(err)
	}
	if x.StatusCode != 200 {
		x.Body.Close()
		t.Fatalf("temporary HTTP failure was not retried: %d", x.StatusCode)
	}
	if err := x.Events(func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	x.Body.Close()
	page, err := s.Requests(context.Background(), RequestFilter{})
	if err != nil || len(page.Requests) != 1 || page.Requests[0].Outcome != "success" || calls.Load() != 2 {
		t.Fatalf("duplicate/missing history or attempt: %+v calls=%d err=%v", page, calls.Load(), err)
	}
}

func TestTimeoutResponsesDoNotCoolWholeAccount(t *testing.T) {
	s, _ := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 408, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}))
	for range 4 {
		x, err := s.Open(context.Background(), 1, 1, []byte(`{"model":"synthetic-model","input":"synthetic"}`), nil, Responses)
		if err != nil {
			t.Fatalf("request timeout blocked account: %v", err)
		}
		x.Body.Close()
	}
}

func TestAccountAndMemberConcurrencyCanExceedLegacyCaps(t *testing.T) {
	s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) { return syntheticStream(), nil }))
	ctx := context.Background()
	if err := s.SetConcurrency(ctx, ids["codex"], 64); err != nil {
		t.Fatalf("account limit 64 rejected: %v", err)
	}
	if err := s.SetMemberLimits(ctx, 1, 0, 64); err != nil {
		t.Fatalf("member limit 64 rejected: %v", err)
	}
	states, err := s.Runtime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		if state.ID == ids["codex"] && state.MaxConcurrency != 64 {
			t.Fatal("saved limit lost")
		}
	}
}

func TestTransientFailureWindowDoesNotAccumulateOldFailures(t *testing.T) {
	s, _ := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	}))
	s.policy.Retries = 0
	s.policy.TransientWindow = time.Second
	clock := atomic.Int64{}
	clock.Store(time.Now().UnixMilli())
	s.now = func() time.Time { return time.UnixMilli(clock.Load()) }
	for range 4 {
		x, err := s.Open(context.Background(), 1, 1, []byte(`{"model":"synthetic-model","input":"synthetic"}`), nil, Responses)
		if err != nil {
			t.Fatalf("failures outside the counting window accumulated: %v", err)
		}
		x.Body.Close()
		clock.Add(700)
	}
}
