package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/murongg/SubLane/internal/auth"
	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/server"
	"github.com/murongg/SubLane/internal/storage"
	"github.com/murongg/SubLane/internal/tenants"
	"github.com/murongg/SubLane/internal/upstream"
	"github.com/murongg/SubLane/internal/vault"
)

type subscriptionTransport func(*http.Request) (*http.Response, error)

func (f subscriptionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRuntimeRoutesManagementToOwnedWorkspace(t *testing.T) {
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	identity, err := auth.New(connection)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := identity.Setup(ctx, "platform-owner", "synthetic-password", "Synthetic workspace")
	if err != nil {
		t.Fatal(err)
	}
	member, err := identity.CreateMember(ctx, "workspace-owner", "synthetic-password")
	if err != nil {
		t.Fatal(err)
	}
	outsider, err := identity.CreateMember(ctx, "first-only-member", "synthetic-password")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := tenants.New(connection).Create(ctx, member.ID, "Second workspace")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := groups.New(connection).Save(ctx, 0, groups.Input{Name: "First pool", Enabled: true, AccountIDs: []string{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := groups.NewForTenant(connection, workspace.ID).Save(ctx, 0, groups.Input{Name: "Second pool", Enabled: true, AccountIDs: []string{}}); err != nil {
		t.Fatal(err)
	}
	cipher, err := vault.Open(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	provider := upstream.NewWithTransport(subscriptionTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"data":[{"id":"synthetic-model"}]}`
		if strings.Contains(r.URL.Path, "fetchAvailableModels") {
			body = `{"models":{"synthetic-model":{}}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	defer provider.Close()
	registry := &tenantRegistry{ctx: ctx, db: connection, vault: cipher, auth: identity,
		tenants: tenants.New(connection), provider: provider, started: time.Now()}
	defer registry.Close()
	h := server.NewMulti(connection, identity, tenants.New(connection), "", registry.Handler)
	owner, err := identity.Login(ctx, member.Username, "synthetic-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"claude", "antigravity"} {
		input, _ := json.Marshal(map[string]string{"provider": kind, "name": "Synthetic " + kind, "auth_json": `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","email":"member@example.test","expired":"2030-01-01T00:00:00Z","project_id":"synthetic-project"}`})
		request := func(path, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			req.Header.Set("X-SubLane-Workspace", strconv.FormatInt(workspace.ID, 10))
			req.Header.Set("Origin", "http://example.com")
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(&http.Cookie{Name: "sublane_session", Value: owner.Token})
			out := httptest.NewRecorder()
			h.ServeHTTP(out, req)
			return out
		}
		imported := request("/api/accounts/import", string(input))
		if imported.Code != http.StatusCreated {
			t.Fatalf("%s subscription import: %d %s", kind, imported.Code, imported.Body.String())
		}
		var account struct{ ID string }
		if err := json.Unmarshal(imported.Body.Bytes(), &account); err != nil {
			t.Fatal(err)
		}
		verified := request("/api/accounts/"+account.ID+"/check", `{}`)
		if verified.Code != http.StatusOK || !strings.Contains(verified.Body.String(), "synthetic-model") {
			t.Fatalf("%s subscription verification: %d %s", kind, verified.Code, verified.Body.String())
		}
		started := request("/api/accounts/oauth", `{"provider":"`+kind+`","name":"Synthetic authorization"}`)
		if started.Code != http.StatusCreated {
			t.Fatalf("%s subscription authorization: %d %s", kind, started.Code, started.Body.String())
		}
	}
	for _, check := range []struct {
		workspace int64
		token     string
		status    int
		contains  string
	}{{workspace.ID, owner.Token, 200, "Second pool"}, {1, platform.Token, 200, "First pool"}, {workspace.ID, platform.Token, 401, ""}} {
		req := httptest.NewRequest(http.MethodGet, "/api/groups", nil)
		req.Header.Set("X-SubLane-Workspace", strconv.FormatInt(check.workspace, 10))
		req.AddCookie(&http.Cookie{Name: "sublane_session", Value: check.token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != check.status || (check.contains != "" && !strings.Contains(w.Body.String(), check.contains)) {
			t.Fatalf("workspace %d management: %d %s", check.workspace, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/settings/backup", "/api/settings/codex", "/api/settings/grok", "/api/settings/claude"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-SubLane-Workspace", strconv.FormatInt(workspace.ID, 10))
		req.AddCookie(&http.Cookie{Name: "sublane_session", Value: owner.Token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "restore-ready") {
			t.Fatalf("workspace administrator accessed platform settings: %s %d %s", path, w.Code, w.Body.String())
		}
	}
	membersRequest := httptest.NewRequest(http.MethodGet, "/api/members", nil)
	membersRequest.Header.Set("X-SubLane-Workspace", strconv.FormatInt(workspace.ID, 10))
	membersRequest.AddCookie(&http.Cookie{Name: "sublane_session", Value: owner.Token})
	membersResponse := httptest.NewRecorder()
	h.ServeHTTP(membersResponse, membersRequest)
	if membersResponse.Code != 200 || strings.Contains(membersResponse.Body.String(), outsider.Username) {
		t.Fatalf("workspace administrator saw another workspace's members: %d %s", membersResponse.Code, membersResponse.Body.String())
	}
	statusRequest := httptest.NewRequest(http.MethodPatch,
		"/api/members/"+strconv.FormatInt(outsider.ID, 10), strings.NewReader(`{"enabled":false}`))
	statusRequest.Header.Set("X-SubLane-Workspace", strconv.FormatInt(workspace.ID, 10))
	statusRequest.Header.Set("Origin", "http://example.com")
	statusRequest.Header.Set("Content-Type", "application/json")
	statusRequest.AddCookie(&http.Cookie{Name: "sublane_session", Value: owner.Token})
	statusResponse := httptest.NewRecorder()
	h.ServeHTTP(statusResponse, statusRequest)
	var stillEnabled bool
	if err := connection.QueryRow("SELECT enabled FROM users WHERE id=?", outsider.ID).Scan(&stillEnabled); err != nil {
		t.Fatal(err)
	}
	if statusResponse.Code != 404 || !stillEnabled {
		t.Fatalf("workspace administrator changed a foreign member globally: %d enabled=%v", statusResponse.Code, stillEnabled)
	}
	createRequest := httptest.NewRequest(http.MethodPost, "/api/members",
		strings.NewReader(`{"username":"second-only-member","password":"synthetic-password"}`))
	createRequest.Header.Set("X-SubLane-Workspace", strconv.FormatInt(workspace.ID, 10))
	createRequest.Header.Set("Origin", "http://example.com")
	createRequest.Header.Set("Content-Type", "application/json")
	createRequest.AddCookie(&http.Cookie{Name: "sublane_session", Value: owner.Token})
	createResponse := httptest.NewRecorder()
	h.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != 201 {
		t.Fatalf("workspace member creation: %d %s", createResponse.Code, createResponse.Body.String())
	}
	var firstMembership, secondMembership bool
	if err := connection.QueryRow(`SELECT EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id
		WHERE m.tenant_id=1 AND u.username='second-only-member'),
		EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id=m.user_id
		WHERE m.tenant_id=? AND u.username='second-only-member')`, workspace.ID).Scan(&firstMembership, &secondMembership); err != nil {
		t.Fatal(err)
	}
	if firstMembership || !secondMembership {
		t.Fatalf("new member joined the wrong workspace: first=%v second=%v", firstMembership, secondMembership)
	}
	resetRequest := httptest.NewRequest(http.MethodPost,
		"/api/members/"+strconv.FormatInt(outsider.ID, 10)+"/password",
		strings.NewReader(`{"new_password":"replacement-password"}`))
	resetRequest.Header.Set("X-SubLane-Workspace", strconv.FormatInt(workspace.ID, 10))
	resetRequest.Header.Set("Origin", "http://example.com")
	resetRequest.Header.Set("Content-Type", "application/json")
	resetRequest.AddCookie(&http.Cookie{Name: "sublane_session", Value: owner.Token})
	resetResponse := httptest.NewRecorder()
	h.ServeHTTP(resetResponse, resetRequest)
	if resetResponse.Code != 403 {
		t.Fatalf("workspace administrator reset a global password: %d %s", resetResponse.Code, resetResponse.Body.String())
	}
	if _, err := identity.Login(ctx, outsider.Username, "synthetic-password"); err != nil {
		t.Fatalf("foreign member password was changed: %v", err)
	}
}

func TestIdleRuntimeClosesOnlyAfterLastRequest(t *testing.T) {
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	registry := &tenantRegistry{ctx: ctx, db: connection, now: func() time.Time { return now }}
	defer registry.Close()
	handler := registry.Handler(1)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	entered := make(chan struct{})
	release := make(chan struct{})
	registry.runtimes[1].handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	}()
	<-entered
	now = now.Add(runtimeIdleTimeout + time.Second)
	registry.reapIdle()
	if len(registry.runtimes) != 1 {
		t.Fatal("active runtime was removed")
	}
	close(release)
	<-done
	now = now.Add(runtimeIdleTimeout + time.Second)
	registry.reapIdle()
	if len(registry.runtimes) != 0 {
		t.Fatal("idle runtime was retained")
	}
}
