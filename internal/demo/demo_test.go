package demo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/murongg/SubLane/internal/gateway"
)

func TestDemoLoginReadOnlyAndCleanup(t *testing.T) {
	instance, err := New(t.Context(), Options{Version: "synthetic-version"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	call := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://demo.example.test"+path, strings.NewReader(body))
		r.Header.Set("Origin", "http://demo.example.test")
		r.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		instance.Handler.ServeHTTP(w, r)
		return w
	}
	state := call("GET", "/api/auth/state", "", nil)
	if state.Code != 200 || !strings.Contains(state.Body.String(), `"username":"demo"`) || !strings.Contains(state.Body.String(), `"user":null`) {
		t.Fatalf("anonymous demo state: %d %s", state.Code, state.Body.String())
	}
	if got := call("GET", "/api/accounts", "", nil); got.Code != 401 {
		t.Fatalf("authentication bypassed: %d", got.Code)
	}
	if got := call("POST", "/api/auth/login", `{"username":"demo","password":"wrong-password"}`, nil); got.Code != 401 {
		t.Fatalf("invalid password accepted: %d", got.Code)
	}
	login := call("POST", "/api/auth/login", `{"username":"demo","password":"sublane-demo"}`, nil)
	if login.Code != 200 || len(login.Result().Cookies()) != 1 {
		t.Fatalf("login: %d %s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	prices := call("GET", "/api/pricing", "", cookie)
	if prices.Code != 200 || !strings.Contains(prices.Body.String(), `"prices":[]`) {
		t.Fatalf("offline price catalog: %d %s", prices.Code, prices.Body.String())
	}
	alertSettings := call("GET", "/api/alerts", "", cookie)
	if alertSettings.Code != 200 || !strings.Contains(alertSettings.Body.String(), `"enabled":false`) || !strings.Contains(alertSettings.Body.String(), `"configured":false`) {
		t.Fatalf("read-only alert settings: %d %s", alertSettings.Code, alertSettings.Body.String())
	}
	if got := call("PUT", "/api/alerts", `{"enabled":true,"url":"https://example.test/webhook"}`, cookie); got.Code != 403 || !strings.Contains(got.Body.String(), "demo_read_only") {
		t.Fatalf("alert mutation: %d %s", got.Code, got.Body.String())
	}
	var history gateway.RequestPage
	if err := json.Unmarshal(call("GET", "/api/requests", "", cookie).Body.Bytes(), &history); err != nil || len(history.Requests) == 0 {
		t.Fatalf("history: %+v %v", history, err)
	}
	for _, record := range history.Requests {
		if record.StartedAt > time.Now().Unix() || record.StartedAt < time.Now().Add(-8*24*time.Hour).Unix() {
			t.Fatalf("request timestamp outside recent history: %d", record.StartedAt)
		}
	}
	var report gateway.Statistics
	if err := json.Unmarshal(call("GET", "/api/usage?days=30", "", cookie).Body.Bytes(), &report); err != nil || report.Totals.Requests == 0 || len(report.Days) != 30 {
		t.Fatalf("usage report: %+v %v", report, err)
	}
	var requests, inputs, outputs int64
	for _, day := range report.Days {
		requests += day.Requests
		inputs += day.InputTokens
		outputs += day.OutputTokens
	}
	if requests != report.Totals.Requests || inputs != report.Totals.InputTokens || outputs != report.Totals.OutputTokens {
		t.Fatal("daily usage does not match totals")
	}
	filtered := call("GET", "/api/requests?outcome=error&until="+strconv.FormatInt(time.Now().Unix()+1, 10), "", cookie)
	if err := json.Unmarshal(filtered.Body.Bytes(), &history); err != nil || len(history.Requests) == 0 {
		t.Fatalf("filtered history: %s %v", filtered.Body.String(), err)
	}
	for _, record := range history.Requests {
		if record.Outcome != "error" {
			t.Fatalf("filter ignored: %+v", record)
		}
	}
	for _, path := range []string{"/api/workspaces", "/api/system", "/api/accounts", "/api/accounts/runtime", "/api/groups", "/api/keys", "/api/members", "/api/requests", "/api/me/requests", "/api/usage?days=30", "/api/me/usage?days=7", "/api/me/limits", "/api/me/allocations", "/api/allocations", "/api/audit", "/api/proxies", "/api/settings/codex", "/api/settings/grok", "/api/settings/claude", "/api/settings/timezone", "/api/settings/backup"} {
		t.Run(path, func(t *testing.T) {
			got := call("GET", path, "", cookie)
			if got.Code != 200 {
				t.Fatalf("read: %d %s", got.Code, got.Body.String())
			}
		})
	}
	var accounts struct {
		Accounts []struct {
			ID string `json:"id"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(call("GET", "/api/accounts", "", cookie).Body.Bytes(), &accounts); err != nil || len(accounts.Accounts) < 2 {
		t.Fatalf("seed accounts: %+v %v", accounts, err)
	}
	for _, account := range accounts.Accounts {
		for _, suffix := range []string{"/usage", "/models"} {
			got := call("GET", "/api/accounts/"+account.ID+suffix, "", cookie)
			if got.Code != 200 {
				t.Fatalf("account read %s: %d %s", suffix, got.Code, got.Body.String())
			}
		}
	}
	for _, path := range []string{"/api/workspaces", "/api/auth/setup", "/api/auth/register", "/api/me/password", "/api/accounts/import", "/api/accounts/oauth", "/api/keys", "/api/keys/1/secret", "/api/settings/backup/export", "/api/settings/backup/verify", "/api/settings/codex/sync", "/api/settings/grok/sync", "/api/settings/claude/sync", "/api/proxies/1/check", "/api/unknown"} {
		got := call("POST", path, `{}`, cookie)
		if got.Code != 403 || !strings.Contains(got.Body.String(), "demo_read_only") {
			t.Fatalf("mutation %s: %d %s", path, got.Code, got.Body.String())
		}
	}
	for _, method := range []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"} {
		for _, path := range []string{"/v1", "/v1/models", "/v1/responses", "/v1/messages", "/v1beta/models/example:generateContent"} {
			if got := call(method, path, `{}`, cookie); got.Code != 403 {
				t.Fatalf("gateway %s %s: %d", method, path, got.Code)
			}
		}
	}
	for _, method := range []string{"PUT", "PATCH", "DELETE"} {
		if got := call(method, "/api/accounts/"+accounts.Accounts[0].ID, `{}`, cookie); got.Code != 403 {
			t.Fatalf("write %s: %d", method, got.Code)
		}
	}
	if got := call("POST", "/api/auth/logout", `{}`, cookie); got.Code != 200 {
		t.Fatalf("logout: %d %s", got.Code, got.Body.String())
	}
	if got := call("GET", "/api/accounts", "", cookie); got.Code != 401 {
		t.Fatalf("session not revoked: %d", got.Code)
	}
	directory := instance.directory
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("temporary data retained: %v", err)
	}
}

func TestDemoInstancesAreIndependent(t *testing.T) {
	first, err := New(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := New(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if first.directory == second.directory {
		t.Fatal("demo instances share storage")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRecorder()
	second.Handler.ServeHTTP(r, httptest.NewRequest("GET", "/api/auth/state", nil))
	if r.Code != 200 {
		t.Fatalf("closing one demo affected another: %d", r.Code)
	}
}

func TestOfflineTransportRejectsNonSnapshotRequests(t *testing.T) {
	for _, target := range []string{"https://example.test/", "https://chatgpt.com/backend-api/codex/responses", "https://auth.openai.com/oauth/token", "https://chatgpt.com:443/backend-api/wham/usage"} {
		for _, method := range []string{"GET", "POST"} {
			r, err := http.NewRequestWithContext(t.Context(), method, target, nil)
			if err != nil {
				t.Fatal(err)
			}
			if response, err := (transport{}).RoundTrip(r); err == nil || response != nil {
				t.Fatalf("allowed %s %s", method, target)
			}
		}
	}
}
