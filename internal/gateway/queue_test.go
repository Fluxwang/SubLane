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
	"github.com/murongg/SubLane/internal/allocations"
	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/upstream"
)

type queuedResult struct {
	exchange *Exchange
	err      error
}

func queuedOpen(s *Service, ctx context.Context, headers http.Header) <-chan queuedResult {
	done := make(chan queuedResult, 1)
	go func() {
		exchange, err := s.Open(ctx, 1, 1, []byte(`{"model":"synthetic-model","input":"synthetic"}`), headers, Responses)
		done <- queuedResult{exchange, err}
	}()
	return done
}

func receiveQueued(t *testing.T, done <-chan queuedResult) queuedResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("queued request did not finish promptly")
		return queuedResult{}
	}
}

func TestAccountQueueWaitsForStreamClosureAndPreservesAffinity(t *testing.T) {
	for _, sticky := range []bool{false, true} {
		t.Run(map[bool]string{false: "sessionless", true: "sticky"}[sticky], func(t *testing.T) {
			var calls atomic.Int64
			s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return syntheticStream(), nil
			}))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := s.SetConcurrency(ctx, ids["codex"], 1); err != nil {
				t.Fatal(err)
			}
			var headers http.Header
			if sticky {
				headers = http.Header{"Session_id": {"synthetic-queue-session"}}
			}
			first := receiveQueued(t, queuedOpen(s, ctx, headers))
			if first.err != nil {
				t.Fatal(first.err)
			}
			defer first.exchange.Body.Close()
			if sticky {
				backup, err := s.accounts.Authorize(ctx, "Synthetic backup", accounts.Credential{AccountID: "synthetic-backup", AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()}, "")
				if err != nil {
					t.Fatal(err)
				}
				if err := s.accounts.SaveCatalog(ctx, backup.ID, 0, []string{"synthetic-model"}, time.Now().Unix(), upstream.CatalogSource("codex")); err != nil {
					t.Fatal(err)
				}
				configureTestPool(t, s.db)
			}
			done := queuedOpen(s, ctx, headers)
			select {
			case result := <-done:
				if result.exchange != nil {
					result.exchange.Body.Close()
				}
				t.Fatal("saturated request must wait for the existing stream", result.err)
			case <-time.After(100 * time.Millisecond):
			}
			if calls.Load() != 1 {
				t.Fatal("waiting request dispatched early", calls.Load())
			}
			if err := first.exchange.Events(func([]byte) error { return nil }); err != nil {
				t.Fatal(err)
			}
			first.exchange.Body.Close()
			second := receiveQueued(t, done)
			if second.err != nil {
				t.Fatal("request failed after capacity became available", second.err)
			}
			defer second.exchange.Body.Close()
			if calls.Load() != 2 {
				t.Fatal("queued request not dispatched exactly once", calls.Load())
			}
			if err := second.exchange.Events(func([]byte) error { return nil }); err != nil {
				t.Fatal(err)
			}
			second.exchange.Body.Close()
			rows, err := s.Requests(ctx, RequestFilter{})
			if err != nil || len(rows.Requests) != 2 {
				t.Fatal("missing queue request history", rows, err)
			}
			for _, row := range rows.Requests {
				if row.AccountID != ids["codex"] || row.Outcome != "success" {
					t.Fatal("queue changed affinity or outcome", row)
				}
			}
		})
	}
}

func TestAccountQueueRechecksKeyValidity(t *testing.T) {
	for _, change := range []string{"disabled", "revoked", "expired", "member_disabled"} {
		t.Run(change, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) { return syntheticStream(), nil }))
			if err := s.SetConcurrency(ctx, ids["codex"], 1); err != nil {
				t.Fatal(err)
			}
			result, err := s.db.ExecContext(ctx, "INSERT INTO api_keys(user_id,group_id,name,prefix,token_hash,created_at) VALUES(1,1,'synthetic','sl_fake',randomblob(32),1)")
			if err != nil {
				t.Fatal(err)
			}
			key, err := result.LastInsertId()
			if err != nil {
				t.Fatal(err)
			}
			first := receiveQueued(t, queuedOpen(s, ctx, nil))
			if first.err != nil {
				t.Fatal(first.err)
			}
			defer first.exchange.Body.Close()
			done := queuedOpen(s, WithRequestIdentity(ctx, key, "http"), nil)
			waitForQueue(t, s, 1)
			statement := map[string]string{
				"disabled":        "UPDATE api_keys SET enabled=0",
				"revoked":         "UPDATE api_keys SET revoked_at=1",
				"expired":         "UPDATE api_keys SET expires_at=1",
				"member_disabled": "UPDATE memberships SET enabled=0",
			}[change]
			if _, err := s.db.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
			if result := receiveQueued(t, done); !errors.Is(result.err, groups.ErrUnavailable) {
				t.Fatal("waiting request bypassed revoked access", result.err)
			}
			if err := first.exchange.Events(func([]byte) error { return nil }); err != nil {
				t.Fatal("policy change interrupted an active stream", err)
			}
		})
	}
}

func TestAccountQueueChargesReleasedRequestBeforeAdmittingNext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return syntheticStream(), nil }))
	if err := s.SetConcurrency(ctx, ids["codex"], 1); err != nil {
		t.Fatal(err)
	}
	scheme, err := allocations.New(s.db).SaveScheme(ctx, 0, allocations.SchemeInput{Name: "Synthetic queue allowance", GroupID: 1, Enabled: true, Config: allocations.Config{Mode: "tokens", Period: "day", Members: []allocations.Share{{UserID: 1, Limit: 5}}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.db.ExecContext(ctx, "INSERT INTO api_keys(user_id,group_id,name,prefix,token_hash,created_at) VALUES(1,1,'synthetic','sl_fake',randomblob(32),1)")
	if err != nil {
		t.Fatal(err)
	}
	key, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO allocation_keys(key_id,scheme_id) VALUES(?,?)", key, scheme.ID); err != nil {
		t.Fatal(err)
	}
	keyCtx := WithRequestIdentity(ctx, key, "http")
	first := receiveQueued(t, queuedOpen(s, keyCtx, nil))
	if first.err != nil {
		t.Fatal(first.err)
	}
	defer first.exchange.Body.Close()
	done := queuedOpen(s, WithRequestIdentity(ctx, key, "http"), nil)
	waitForQueue(t, s, 1)
	var active int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM allocation_entries WHERE state='active'").Scan(&active); err != nil || active != 1 {
		t.Fatal("waiting reserved an allocation before dispatch", active, err)
	}
	if err := first.exchange.Events(func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	first.exchange.Body.Close()
	if result := receiveQueued(t, done); !errors.Is(result.err, allocations.ErrQuota) {
		t.Fatal("queue admitted before completed usage was charged", result.err)
	}
	if calls.Load() != 1 {
		t.Fatal("exhausted queue request dispatched", calls.Load())
	}
}

func waitForQueue(t *testing.T, s *Service, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		waiting := s.waiting[1]
		s.mu.Unlock()
		if waiting == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("queue did not reach expected size", count)
}

func TestAccountQueueBoundsWaitingAndReleasesCanceledRequests(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int64
	s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return syntheticStream(), nil
	}))
	s.policy.QueueLimit = 8
	if err := s.SetConcurrency(ctx, ids["codex"], 1); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Unix(1800000000, 0) }
	if err := s.SetMemberLimits(ctx, 1, 10, 10); err != nil {
		t.Fatal(err)
	}
	first := receiveQueued(t, queuedOpen(s, ctx, nil))
	if first.err != nil {
		t.Fatal(first.err)
	}
	defer first.exchange.Body.Close()
	queuedCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	waiters := make([]<-chan queuedResult, 8)
	for i := range waiters {
		waiters[i] = queuedOpen(s, queuedCtx, nil)
	}
	waitForQueue(t, s, 8)
	overflow := receiveQueued(t, queuedOpen(s, ctx, nil))
	if !errors.Is(overflow.err, ErrAccountQueueFull) {
		t.Fatal("queue overflow was not rejected", overflow.err)
	}
	// Multiple policy rechecks must not charge RPM again while a request waits.
	time.Sleep(300 * time.Millisecond)
	limits, err := s.MemberLimits(ctx, 1)
	if err != nil || limits.RequestsThisMinute != 10 || limits.InFlight != 9 {
		t.Fatal("waiting leaked member leases or RPM", limits, err)
	}
	cancel()
	for _, done := range waiters {
		if result := receiveQueued(t, done); !errors.Is(result.err, context.Canceled) {
			t.Fatal("canceled waiter continued", result.err)
		}
	}
	waitForQueue(t, s, 0)
	limits, err = s.MemberLimits(ctx, 1)
	if err != nil || limits.InFlight != 1 || calls.Load() != 1 {
		t.Fatal("cancellation leaked capacity or sent requests", limits, calls.Load(), err)
	}
}

func TestAccountQueueTimeoutIsRejectedWithoutAccountPenalty(t *testing.T) {
	ctx := context.Background()
	s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) { return syntheticStream(), nil }))
	s.accountWaitTimeout = 20 * time.Millisecond
	if err := s.SetConcurrency(ctx, ids["codex"], 1); err != nil {
		t.Fatal(err)
	}
	headers := http.Header{"Session_id": {"synthetic-timeout-session"}}
	first := receiveQueued(t, queuedOpen(s, ctx, headers))
	if first.err != nil {
		t.Fatal(first.err)
	}
	defer first.exchange.Body.Close()
	result := receiveQueued(t, queuedOpen(s, ctx, headers))
	if !errors.Is(result.err, ErrAccountWaitTimeout) {
		t.Fatal("full account did not time out", result.err)
	}
	rows, err := s.Requests(ctx, RequestFilter{})
	if err != nil || len(rows.Requests) != 1 || rows.Requests[0].ErrorCode != "account_wait_timeout" || rows.Requests[0].Outcome != "rejected" {
		t.Fatal("queue timeout lost its diagnostic", rows, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.health[ids["codex"]]
	if s.waiting[1] != 0 || s.memberActive[1] != 1 || state.InFlight != 1 || state.Failures != 0 || state.CooldownUntil != 0 {
		t.Fatal("local timeout penalized the account or leaked capacity", state)
	}
}

func TestAccountQueueRechecksPolicyBeforeDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return syntheticStream(), nil }))
	if err := s.SetConcurrency(ctx, ids["codex"], 1); err != nil {
		t.Fatal(err)
	}
	first := receiveQueued(t, queuedOpen(s, ctx, nil))
	if first.err != nil {
		t.Fatal(first.err)
	}
	defer first.exchange.Body.Close()
	done := queuedOpen(s, ctx, nil)
	waitForQueue(t, s, 1)
	if _, err := groups.New(s.db).Save(ctx, 1, groups.Input{Name: "Synthetic restricted", Enabled: true, AccountIDs: []string{ids["codex"]}, ModelPolicy: &groups.ModelPolicy{Restricted: true, Models: []string{"synthetic-other"}}}); err != nil {
		t.Fatal(err)
	}
	if result := receiveQueued(t, done); !errors.Is(result.err, ErrModelNotAllowed) {
		t.Fatal("waiting request bypassed new policy", result.err)
	}
	if calls.Load() != 1 {
		t.Fatal("policy rejection reached upstream", calls.Load())
	}
}

func TestAccountQueueShutdownCancelsWaiting(t *testing.T) {
	ctx := context.Background()
	s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) { return syntheticStream(), nil }))
	if err := s.SetConcurrency(ctx, ids["codex"], 1); err != nil {
		t.Fatal(err)
	}
	first := receiveQueued(t, queuedOpen(s, ctx, nil))
	if first.err != nil {
		t.Fatal(first.err)
	}
	defer first.exchange.Body.Close()
	done := queuedOpen(s, ctx, nil)
	waitForQueue(t, s, 1)
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	if result := receiveQueued(t, done); !errors.Is(result.err, context.Canceled) {
		t.Fatal("shutdown did not cancel waiter", result.err)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("waiting delayed shutdown")
	}
	waitForQueue(t, s, 0)
}

func TestAccountQueueWakeDoesNotOverbook(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return syntheticStream(), nil }))
	if err := s.SetConcurrency(ctx, ids["codex"], 1); err != nil {
		t.Fatal(err)
	}
	first := receiveQueued(t, queuedOpen(s, ctx, nil))
	if first.err != nil {
		t.Fatal(first.err)
	}
	defer first.exchange.Body.Close()
	done := make(chan queuedResult, 8)
	for range 8 {
		go func() { done <- <-queuedOpen(s, ctx, nil) }()
	}
	waitForQueue(t, s, 8)
	if err := first.exchange.Events(func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	first.exchange.Body.Close()
	for i := range 8 {
		result := receiveQueued(t, done)
		if result.err != nil {
			t.Fatal(result.err)
		}
		defer result.exchange.Body.Close()
		if calls.Load() != int64(i+2) {
			t.Fatal("broadcast dispatched more requests than available slots", calls.Load())
		}
		s.mu.Lock()
		inFlight := s.health[ids["codex"]].InFlight
		s.mu.Unlock()
		if inFlight != 1 {
			t.Fatal("account overbooked after broadcast", inFlight)
		}
		if err := result.exchange.Events(func([]byte) error { return nil }); err != nil {
			t.Fatal(err)
		}
		result.exchange.Body.Close()
	}
	waitForQueue(t, s, 0)
}

func TestAccountQueueRechecksChangedConcurrency(t *testing.T) {
	for _, memberLimit := range []bool{false, true} {
		t.Run(map[bool]string{false: "account_increase", true: "member_decrease"}[memberLimit], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) { return syntheticStream(), nil }))
			if err := s.SetConcurrency(ctx, ids["codex"], 1); err != nil {
				t.Fatal(err)
			}
			first := receiveQueued(t, queuedOpen(s, ctx, nil))
			if first.err != nil {
				t.Fatal(first.err)
			}
			defer first.exchange.Body.Close()
			done := queuedOpen(s, ctx, nil)
			waitForQueue(t, s, 1)
			if memberLimit {
				if err := s.SetMemberLimits(ctx, 1, 0, 1); err != nil {
					t.Fatal(err)
				}
				if result := receiveQueued(t, done); !errors.Is(result.err, ErrMemberBusy) {
					t.Fatal("waiting bypassed lowered member concurrency", result.err)
				}
			} else {
				if err := s.SetConcurrency(ctx, ids["codex"], 2); err != nil {
					t.Fatal(err)
				}
				result := receiveQueued(t, done)
				if result.err != nil {
					t.Fatal("increased capacity did not wake request", result.err)
				}
				defer result.exchange.Body.Close()
			}
			if err := first.exchange.Events(func([]byte) error { return nil }); err != nil {
				t.Fatal("changing concurrency interrupted an active stream", err)
			}
		})
	}
}

func TestAccountQueueSuccessClearsFailureThatReleasedCapacity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		return syntheticStream(), nil
	}))
	if err := s.SetConcurrency(ctx, ids["codex"], 1); err != nil {
		t.Fatal(err)
	}
	first := receiveQueued(t, queuedOpen(s, ctx, nil))
	if first.err != nil {
		t.Fatal(first.err)
	}
	defer first.exchange.Body.Close()
	done := queuedOpen(s, ctx, nil)
	waitForQueue(t, s, 1)
	first.exchange.Body.Close()
	second := receiveQueued(t, done)
	if second.err != nil {
		t.Fatal(second.err)
	}
	defer second.exchange.Body.Close()
	if err := second.exchange.Events(func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	second.exchange.Body.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if state := s.health[ids["codex"]]; state.Failures != 0 || state.CooldownUntil != 0 {
		t.Fatal("successful dispatch after waiting retained an older failure", state)
	}
}
