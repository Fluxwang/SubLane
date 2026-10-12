package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/apikey"
)

func TestCatalogEndpointsRespectRoleOwnershipAndKeyLifecycle(t *testing.T) {
	f := newForwardFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"models":[{"slug":"synthetic-model"}]}`)
	})
	h := f.server.Config.Handler
	login := func(name, password string) *http.Cookie {
		response := request(h, "POST", "/api/auth/login", "http://example.test", map[string]string{"username": name, "password": password}, nil)
		if response.Code != 200 {
			t.Fatal(response.Code)
		}
		return response.Result().Cookies()[0]
	}
	member := login("member-test", "member pass 42")
	admin := login("owner-test", "owner pass 42")
	path := fmt.Sprintf("/api/keys/%d/models", f.keyID)
	response := request(h, "GET", path, "", nil, member)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"id":"synthetic-model"`) || strings.Contains(response.Body.String(), "codex/") {
		t.Fatal("personal catalog", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "synthetic-upstream") || strings.Contains(response.Body.String(), f.secret) {
		t.Fatal("private account data leaked")
	}
	if result := request(h, "GET", path, "", nil, admin); result.Code != 404 {
		t.Fatal("admin read another owner's catalog", result.Code)
	}
	if result := request(h, "GET", "/api/groups/1/models", "", nil, member); result.Code != 403 {
		t.Fatal("member read administrator catalog", result.Code)
	}
	if result := request(h, "GET", "/api/groups/1/models", "", nil, admin); result.Code != 200 {
		t.Fatal("administrator catalog", result.Code)
	}
	rows, err := f.accounts.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	accountPath := "/api/accounts/" + rows[0].ID + "/models"
	if result := request(h, "GET", accountPath, "", nil, member); result.Code != 403 {
		t.Fatal("member read account catalog", result.Code)
	}
	if result := request(h, "GET", accountPath, "", nil, admin); result.Code != 200 {
		t.Fatal("account catalog", result.Code)
	}
	if result := request(h, "POST", accountPath+"/refresh", "https://foreign.example.test", map[string]any{}, admin); result.Code != 403 {
		t.Fatal("refresh lost origin protection", result.Code)
	}
	if _, err := f.keys.Update(context.Background(), f.userID, f.keyID, apikey.UpdateInput{Name: "Synthetic client", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if result := request(h, "GET", path, "", nil, member); result.Code != 409 {
		t.Fatal("paused key retained catalog access", result.Code)
	}
}

func TestImportStartsModelDiscoveryWithoutFailingCredentialStorage(t *testing.T) {
	f := newForwardFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"models":[{"slug":"synthetic-discovered"}]}`)
	})
	h := f.server.Config.Handler
	login := request(h, "POST", "/api/auth/login", "http://example.test", map[string]string{"username": "owner-test", "password": "owner pass 42"}, nil)
	if login.Code != 200 {
		t.Fatal("login", login.Code, login.Body.String())
	}
	admin := login.Result().Cookies()[0]
	credential := fmt.Sprintf(`{"type":"codex","access_token":"synthetic-new-access","refresh_token":"synthetic-refresh","account_id":"synthetic-new","expired":"%s"}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	result := request(h, "POST", "/api/accounts/import", "http://example.test", map[string]string{"provider": "codex", "name": "Synthetic auto-discovery", "auth_json": credential}, admin)
	if result.Code != 201 {
		t.Fatal(result.Code, result.Body.String())
	}
	var created accounts.Account
	if err := json.Unmarshal(result.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		catalog, err := f.accounts.Catalog(context.Background(), created.ID)
		if err == nil && catalog.UpdatedAt > 0 && len(catalog.Models) == 1 && catalog.Models[0] == "synthetic-discovered" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("import did not discover models")
}

func TestCachedModelsDoNotConsumeUpstreamRequestSlots(t *testing.T) {
	f := newForwardFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("fresh catalog contacted upstream") })
	for range f.forwarding.Policy().Capacity {
		release, err := f.forwarding.Acquire()
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	r := httptest.NewRequest(http.MethodGet, "http://example.test/v1/models", nil)
	r.Header.Set("Authorization", "Bearer "+f.secret)
	w := httptest.NewRecorder()
	f.server.Config.Handler.ServeHTTP(w, r)
	var catalog struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil || catalog.Object != "list" || len(catalog.Data) != 1 || catalog.Data[0].ID != "synthetic-model" {
		t.Fatal("cached native model listing", w.Code, w.Body.String())
	}
}
