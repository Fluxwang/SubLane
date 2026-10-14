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
	"github.com/murongg/SubLane/internal/upstream"
)

func TestRefreshFailuresDoNotCoolModelTraffic(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "valid fallback", false: "expired retry"}[valid], func(t *testing.T) {
			var refreshes atomic.Int32
			var executions atomic.Int32
			s, ids := codexGateway(t, transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "auth.openai.com" {
					refreshes.Add(1)
					return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
				}
				executions.Add(1)
				return syntheticStream(), nil
			}))
			ctx := context.Background()
			save := func(token string, validity time.Duration) {
				t.Helper()
				_, err := s.accounts.Authorize(ctx, "Synthetic subscription", accounts.Credential{AccountID: "synthetic-subject", AccessToken: token, RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(validity).Unix()}, ids["codex"])
				if err != nil {
					t.Fatal(err)
				}
				catalog, err := s.accounts.Catalog(ctx, ids["codex"])
				if err != nil {
					t.Fatal(err)
				}
				if err := s.accounts.SaveCatalog(ctx, ids["codex"], catalog.Revision, []string{"synthetic-model"}, nil, time.Now().Unix(), upstream.CatalogSource("codex")); err != nil {
					t.Fatal(err)
				}
			}
			validity := -time.Second
			if valid {
				validity = 90 * time.Second
			}
			save("synthetic-old", validity)
			raw := []byte(`{"model":"synthetic-model","input":"synthetic"}`)
			headers := http.Header{"Session_id": {"synthetic-session"}}
			for range 4 {
				x, err := s.Open(ctx, 1, 1, raw, headers, Responses)
				if !valid {
					if !errors.Is(err, accounts.ErrRefresh) {
						t.Fatalf("refresh backoff became a model cooldown: %v", err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("valid token could not serve traffic: %v", err)
				}
				if err := x.Events(func([]byte) error { return nil }); err != nil {
					t.Fatal(err)
				}
				x.Body.Close()
			}
			if refreshes.Load() != 1 {
				t.Fatalf("requests ignored refresh backoff: %d", refreshes.Load())
			}
			if !valid && executions.Load() != 0 {
				t.Fatal("expired credentials reached inference")
			}
			states, err := s.Runtime(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, state := range states {
				if state.ID == ids["codex"] && (state.Failures != 0 || state.CooldownUntil != 0) {
					t.Fatalf("refresh failure penalized model runtime: %+v", state)
				}
			}
			save("synthetic-repaired", time.Hour)
			x, err := s.Open(ctx, 1, 1, raw, headers, Responses)
			if err != nil {
				t.Fatalf("repaired credentials remained blocked: %v", err)
			}
			x.Body.Close()
		})
	}
}
