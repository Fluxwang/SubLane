package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/murongg/SubLane/internal/audit"
	"github.com/murongg/SubLane/internal/vault"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/murongg/SubLane/internal/apikey"
	"github.com/murongg/SubLane/internal/auth"
	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/storage"
	"github.com/murongg/SubLane/internal/tenants"
)

func TestPersonalKeyOwnershipAndGatewayBoundary(t *testing.T) {
	db, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "key-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	identity, err := auth.New(db)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Options{Auth: identity, Keys: newTestKeyService(t, db), Ping: db.PingContext, StartedAt: time.Now()})
	origin := "http://example.test"
	setup := request(h, "POST", "/api/auth/setup", origin, map[string]string{"username": "owner-test", "password": "owner pass 42", "workspace_name": "Synthetic workspace"}, nil)
	owner := setup.Result().Cookies()[0]
	memberInput := map[string]string{"username": "member-test", "password": "member pass 42"}
	if got := request(h, "POST", "/api/members", origin, memberInput, owner).Code; got != 201 {
		t.Fatalf("member create: %d", got)
	}
	login := request(h, "POST", "/api/auth/login", origin, memberInput, nil)
	member := login.Result().Cookies()[0]
	configureTestPool(t, db)
	if result := request(h, "POST", "/api/keys", origin, map[string]string{"name": "Missing pool"}, member); result.Code != 400 {
		t.Fatalf("key created without choosing a pool: %d", result.Code)
	}
	created := request(h, "POST", "/api/keys", origin, map[string]any{"name": "Synthetic laptop", "group_id": 1}, member)
	if created.Code != 201 {
		t.Fatalf("member key create: %d", created.Code)
	}
	var key apikey.CreatedKey
	if err := json.Unmarshal(created.Body.Bytes(), &key); err != nil || key.Secret == "" {
		t.Fatal("missing creation secret")
	}
	list := request(h, "GET", "/api/keys", "", nil, member)
	if list.Code != 200 || strings.Contains(list.Body.String(), key.Secret) {
		t.Fatal("key listing exposed secret or denied owner")
	}
	if got := request(h, "POST", fmt.Sprintf("/api/keys/%d/revoke", key.Key.ID), origin, map[string]string{}, owner).Code; got != 404 {
		t.Fatalf("cross-user revocation: %d", got)
	}
	if got := request(h, "POST", "/api/keys", origin, map[string]any{"name": "Forged owner", "user_id": 1}, member).Code; got != 400 {
		t.Fatal("owner injection accepted")
	}
	gateway := func(path, token string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "http://example.test"+path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if got := gateway("/v1/models", "", member).Code; got != 401 {
		t.Fatal("browser session used as gateway key")
	}
	if got := gateway("/api/system", key.Secret, nil).Code; got != 401 {
		t.Fatal("API key granted administrator access")
	}
	if got := gateway("/api/keys", key.Secret, nil).Code; got != 401 {
		t.Fatal("API key accessed key management")
	}
	result := gateway("/v1/models", key.Secret, nil)
	if result.Code != 501 || !strings.Contains(result.Body.String(), "gateway_not_configured") {
		t.Fatalf("gateway readiness: %d", result.Code)
	}
	if got := request(h, "POST", fmt.Sprintf("/api/keys/%d/revoke", key.Key.ID), origin, map[string]string{}, member).Code; got != 200 {
		t.Fatal("revoke failed")
	}
	if got := gateway("/v1/models", key.Secret, nil).Code; got != 401 {
		t.Fatal("revoked gateway key accepted")
	}
}

func newTestKeyService(t *testing.T, connection *sql.DB) *apikey.Service {
	t.Helper()
	cipher, err := vault.Open(filepath.Join(t.TempDir(), "credentials.key"), true)
	if err != nil {
		t.Fatal(err)
	}
	return apikey.New(connection, cipher)
}
func TestPersonalKeyDisclosureBoundaryAndNoStore(t *testing.T) {
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "disclosure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	identity, err := auth.New(connection)
	if err != nil {
		t.Fatal(err)
	}
	keys := newTestKeyService(t, connection)
	h := New(Options{Auth: identity, Keys: keys, Audit: audit.New(connection)})
	origin := "http://example.test"
	owner := request(h, "POST", "/api/auth/setup", origin, map[string]string{"username": "synthetic-admin", "password": "synthetic-pass", "workspace_name": "Synthetic workspace"}, nil).Result().Cookies()[0]
	member, err := identity.CreateMember(ctx, "synthetic-member", "synthetic-pass")
	if err != nil {
		t.Fatal(err)
	}
	configureTestPool(t, connection)
	cookie := request(h, "POST", "/api/auth/login", origin, map[string]string{"username": "synthetic-member", "password": "synthetic-pass"}, nil).Result().Cookies()[0]
	created, err := keys.CreateInGroup(ctx, member.ID, 1, "Synthetic client")
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/keys/%d/secret", created.Key.ID)
	for _, check := range []struct {
		method, origin string
		cookie         *http.Cookie
		status         int
	}{{"POST", origin, nil, 401}, {"POST", origin, owner, 404}, {"POST", "https://other.example.test", cookie, 403}, {"GET", "", cookie, 405}} {
		got := request(h, check.method, path, check.origin, map[string]any{}, check.cookie)
		if got.Code != check.status || strings.Contains(got.Body.String(), created.Secret) {
			t.Fatal("disclosure boundary", got.Code, check.status)
		}
	}
	r := httptest.NewRequest("POST", "http://example.test"+path, strings.NewReader("{}"))
	r.Header.Set("Origin", origin)
	r.Header.Set("Authorization", "Bearer "+created.Secret)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("bearer key used for secret disclosure")
	}
	got := request(h, "POST", path, origin, map[string]any{}, cookie)
	if got.Code != 200 || got.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("disclosure failed or cacheable", got.Code)
	}
	var result struct {
		Secret string `json:"secret"`
	}
	if json.Unmarshal(got.Body.Bytes(), &result) != nil || result.Secret != created.Secret {
		t.Fatal("wrong key returned")
	}
	if strings.Contains(request(h, "GET", "/api/keys", "", nil, cookie).Body.String(), created.Secret) {
		t.Fatal("list leaked secret")
	}
	events, err := audit.New(connection).List(ctx, audit.Filter{})
	if err != nil || events.Events[0].Action != "key.reveal" || events.Events[0].Outcome != "success" {
		t.Fatal("missing disclosure audit", err)
	}
	if _, err := keys.Revoke(ctx, member.ID, created.Key.ID); err != nil {
		t.Fatal(err)
	}
	if got := request(h, "POST", path, origin, map[string]any{}, cookie); got.Code != 409 || strings.Contains(got.Body.String(), created.Secret) {
		t.Fatal("revoked key disclosed")
	}
}

func configureTestPool(t *testing.T, conn *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		"INSERT OR IGNORE INTO account_groups(id,name,enabled,created_at,updated_at) VALUES(1,'Default',1,1,1)",
		"INSERT OR IGNORE INTO group_accounts SELECT 1,id FROM accounts",
		"INSERT OR IGNORE INTO group_members SELECT 1,id FROM users",
	} {
		if _, err := conn.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPersonalKeyListFiltersBeforePaginationAndScopesRevokedCount(t *testing.T) {
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "filtered-keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	identity, err := auth.New(connection)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := identity.Setup(ctx, "synthetic-owner", "synthetic-password", "First workspace")
	if err != nil {
		t.Fatal(err)
	}
	member, err := identity.CreateMember(ctx, "synthetic-member", "synthetic-password")
	if err != nil {
		t.Fatal(err)
	}
	configureTestPool(t, connection)
	tenancy := tenants.New(connection)
	second, err := tenancy.Create(ctx, member.ID, "Second workspace")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := groups.NewForTenant(connection, second.ID).Save(ctx, 0, groups.Input{Name: "Second group", Enabled: true, AccountIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	// Historical fixtures exceed today's creation limit to exercise cursor pagination.
	for id := int64(1); id <= 123; id++ {
		userID, groupID := member.ID, int64(1)
		var revoked any
		if id%2 == 0 || id > 120 {
			revoked = int64(1900000000)
		}
		if id == 121 {
			userID = owner.User.ID
		}
		if id > 121 {
			groupID = pool.ID
		}
		digest := sha256.Sum256([]byte(fmt.Sprintf("synthetic-hash-%d", id)))
		if _, err := connection.ExecContext(ctx, `INSERT INTO api_keys(id,user_id,group_id,name,prefix,token_hash,created_at,revoked_at,enabled)
			VALUES(?,?,?,?,?,?,1900000000,?,0)`, id, userID, groupID, fmt.Sprintf("Synthetic key %d", id), "sl_fixture", digest[:], revoked); err != nil {
			t.Fatal(err)
		}
	}
	cipher, err := vault.Open(filepath.Join(t.TempDir(), "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	h := NewMulti(connection, identity, tenancy, "", func(id int64) http.Handler {
		return New(Options{Auth: identity, Tenants: tenancy, TenantID: id, Keys: apikey.NewForTenant(connection, cipher, id)})
	})
	session, err := identity.Login(ctx, member.Username, "synthetic-password")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: session.Token}
	list := func(path string, workspace int64) (keys []apikey.Key, cursor, count int64) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "http://example.test"+path, nil)
		r.AddCookie(cookie)
		r.Header.Set(workspaceHeader, fmt.Sprint(workspace))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var page struct {
			Keys         []apikey.Key `json:"keys"`
			NextCursor   int64        `json:"next_cursor"`
			RevokedCount int64        `json:"revoked_count"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
			t.Fatalf("list failed: %d %s", w.Code, w.Body.String())
		}
		return page.Keys, page.NextCursor, page.RevokedCount
	}
	keys, cursor, count := list("/api/keys", 1)
	if len(keys) != 50 || cursor != 21 || count != 60 {
		t.Fatalf("default page: keys=%d cursor=%d revoked=%d", len(keys), cursor, count)
	}
	for _, key := range keys {
		if key.RevokedAt != nil || key.Enabled {
			t.Fatal("revoked keys must be hidden and paused keys must remain visible")
		}
	}
	keys, cursor, count = list("/api/keys?cursor=21", 1)
	if len(keys) != 10 || keys[0].ID != 19 || keys[9].ID != 1 || cursor != 0 || count != 60 {
		t.Fatalf("filtered last page: keys=%d cursor=%d revoked=%d", len(keys), cursor, count)
	}
	keys, cursor, count = list("/api/keys?revoked=include", 1)
	if len(keys) != 50 || keys[0].ID != 120 || keys[0].RevokedAt == nil || cursor != 71 || count != 60 {
		t.Fatalf("included page: keys=%d cursor=%d revoked=%d", len(keys), cursor, count)
	}
	keys, cursor, count = list("/api/keys", second.ID)
	if len(keys) != 0 || cursor != 0 || count != 2 {
		t.Fatalf("second workspace: keys=%d cursor=%d revoked=%d", len(keys), cursor, count)
	}
	keys, _, count = list("/api/keys?revoked=include", second.ID)
	if len(keys) != 2 || count != 2 {
		t.Fatal("second workspace revoked keys missing")
	}
}
