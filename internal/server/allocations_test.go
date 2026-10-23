package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/murongg/SubLane/internal/accounts"
	"github.com/murongg/SubLane/internal/allocations"
	"github.com/murongg/SubLane/internal/apikey"
	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/upstream"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestUnknownWindowOverrideHasSpecificAPIError(t *testing.T) {
	w := httptest.NewRecorder()
	allocationError(w, allocations.ErrUnknownWindowOverride)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "allocation_unknown_window_override") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestOwnerAllowancePoolChoicesAndKeyCreation(t *testing.T) {
	f := newForwardFixture(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected upstream call") })
	ctx := context.Background()
	owner, err := f.identity.Login(ctx, "owner-test", "owner pass 42")
	if err != nil {
		t.Fatal(err)
	}
	scheme, err := f.forwarding.Allocations().SaveScheme(ctx, 0, allocations.SchemeInput{
		Name: "Synthetic owner allowance", GroupID: 1, Enabled: true,
		Config: allocations.Config{Mode: "tokens", Period: "month", Members: []allocations.Share{{UserID: owner.User.ID, Limit: 100}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: owner.Token}
	h := f.server.Config.Handler
	response := request(h, http.MethodGet, "/api/keys/groups", "", nil, cookie)
	if response.Code != http.StatusOK {
		t.Fatalf("owner pool choices: %d %s", response.Code, response.Body.String())
	}
	var choices struct {
		Groups []groups.Choice `json:"groups"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &choices); err != nil {
		t.Fatal(err)
	}
	if len(choices.Groups) != 1 || choices.Groups[0].ID != scheme.GroupID || choices.Groups[0].SchemeID != scheme.ID || choices.Groups[0].AccountCount != 1 {
		t.Fatalf("owner's allocation pool missing: %+v", choices.Groups)
	}
	created := request(h, http.MethodPost, "/api/keys", "http://example.test", map[string]any{
		"name": "Synthetic owner client", "group_id": choices.Groups[0].ID, "scheme_id": choices.Groups[0].SchemeID,
	}, cookie)
	if created.Code != http.StatusCreated {
		t.Fatalf("owner scheme key creation: %d %s", created.Code, created.Body.String())
	}
}

func TestWindowedAmountPersonalBalanceResponse(t *testing.T) {
	f := newForwardFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	ctx := context.Background()
	accounts, err := f.accounts.List(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatal(accounts, err)
	}
	if _, err := f.groups.Save(ctx, 1, groups.Input{Name: "Default", Enabled: true, AccountIDs: []string{}}); err != nil {
		t.Fatal(err)
	}
	pool, err := f.groups.Save(ctx, 0, groups.Input{Name: "Synthetic window pool", Enabled: true, AccountIDs: []string{accounts[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.groups.SetMemberGroups(ctx, f.userID, []int64{pool.ID}); err != nil {
		t.Fatal(err)
	}
	_, err = f.forwarding.Allocations().SaveScheme(ctx, 0, allocations.SchemeInput{Name: "Synthetic windows", GroupID: pool.ID, Enabled: true, Config: allocations.Config{
		Mode: "windows", Period: "durations", Windows: []allocations.WindowCondition{{DurationSeconds: 5 * 3600, Limit: 100}, {DurationSeconds: 7 * 86400, Limit: 200}},
		Members: []allocations.Share{{UserID: f.userID}},
		Rates:   []allocations.Rate{{Model: "synthetic-model", Input: 1_000_000, Output: 1_000_000}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	member, err := f.identity.Login(ctx, "member-test", "member pass 42")
	if err != nil {
		t.Fatal(err)
	}
	response := request(f.server.Config.Handler, "GET", "/api/me/allocations", "", nil, &http.Cookie{Name: sessionCookie, Value: member.Token})
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var payload struct {
		Schemes []struct {
			Config struct {
				Mode    string `json:"mode"`
				Period  string `json:"period"`
				Windows []struct {
					DurationSeconds int64 `json:"duration_seconds"`
					Limit           int64 `json:"limit"`
				} `json:"windows"`
				Members []struct {
					Limit int64 `json:"limit"`
				} `json:"members"`
			} `json:"config"`
			Balances []struct {
				Kind          string `json:"window_kind"`
				WindowSeconds int64  `json:"window_seconds"`
				Mode          string `json:"mode"`
				Limit         int64  `json:"limit"`
			} `json:"balances"`
		} `json:"schemes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || len(payload.Schemes) != 1 {
		t.Fatal(err, response.Body.String())
	}
	got := payload.Schemes[0]
	if got.Config.Mode != "windows" || got.Config.Period != "durations" || len(got.Config.Windows) != 2 || got.Config.Windows[0].DurationSeconds != 5*3600 || got.Config.Windows[0].Limit != 100 || got.Config.Windows[1].DurationSeconds != 7*86400 || got.Config.Windows[1].Limit != 200 || len(got.Config.Members) != 1 || got.Config.Members[0].Limit != 0 || len(got.Balances) != 2 || got.Balances[0].Kind != "duration" || got.Balances[0].WindowSeconds != 5*3600 || got.Balances[0].Mode != "amount" || got.Balances[0].Limit != 100 || got.Balances[1].Kind != "duration" || got.Balances[1].WindowSeconds != 7*86400 || got.Balances[1].Mode != "amount" || got.Balances[1].Limit != 200 {
		t.Fatal("personal response disagreed with windowed allowance", got)
	}
}

func TestAllocationManagementRolesAndPersonalIsolation(t *testing.T) {
	f := newForwardFixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	owner, err := f.identity.Login(context.Background(), "owner-test", "owner pass 42")
	if err != nil {
		t.Fatal(err)
	}
	member, err := f.identity.Login(context.Background(), "member-test", "member pass 42")
	if err != nil {
		t.Fatal(err)
	}
	adminCookie := &http.Cookie{Name: sessionCookie, Value: owner.Token}
	memberCookie := &http.Cookie{Name: sessionCookie, Value: member.Token}
	h := f.server.Config.Handler
	body := map[string]any{"name": "Synthetic allocation"}
	if r := request(h, "POST", "/api/allocations", "http://example.test", body, memberCookie); r.Code != 403 {
		t.Fatal("member management", r.Code)
	}
	if r := request(h, "POST", "/api/allocations", "http://foreign.example.test", body, adminCookie); r.Code != 403 {
		t.Fatal("origin", r.Code)
	}
	if r := request(h, "GET", "/api/me/allocations?user_id=1", "", nil, memberCookie); r.Code != 200 || r.Body.String() != "{\"schemes\":[]}\n" {
		t.Fatal("own scope", r.Code, r.Body.String())
	}
}

func TestSchemeKeysIsolateTeamsAcrossHTTPAndWebSocket(t *testing.T) {
	f := newForwardFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n")
	})
	ctx := context.Background()
	original, err := f.accounts.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.accounts.Authorize(ctx, "Synthetic second", accounts.Credential{AccessToken: "synthetic-2", RefreshToken: "synthetic-refresh-2", AccountID: "synthetic-subscription-2", ExpiresAt: time.Now().Add(time.Hour).Unix()}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.accounts.SaveCatalog(ctx, second.ID, 0, []string{"synthetic-model"}, nil, time.Now().Unix(), upstream.CatalogSource("codex")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.groups.Save(ctx, 1, groups.Input{Name: "Default", Enabled: true, AccountIDs: []string{}}); err != nil {
		t.Fatal(err)
	}
	manager := f.forwarding.Allocations()
	secrets := []string{}
	schemeIDs := []int64{}
	grantedPools := []int64{}
	for i, account := range []string{original[0].ID, second.ID} {
		name := fmt.Sprintf("Synthetic %d", i)
		pool, err := f.groups.Save(ctx, 0, groups.Input{Name: name, Enabled: true, AccountIDs: []string{account}})
		if err != nil {
			t.Fatal(err)
		}
		grantedPools = append(grantedPools, pool.ID)
		if err := f.groups.SetMemberGroups(ctx, f.userID, grantedPools); err != nil {
			t.Fatal(err)
		}
		config := allocations.Config{Mode: "tokens", Period: "day", Members: []allocations.Share{{UserID: f.userID, Limit: 5}}}
		if i == 1 {
			config.Mode = "amount"
			config.Members[0].Limit = 18
			config.Rates = []allocations.Rate{{Model: "synthetic-model", Input: 2000000, Cached: 1000000, Output: 6000000}}
		}
		scheme, err := manager.SaveScheme(ctx, 0, allocations.SchemeInput{Name: name, GroupID: pool.ID, Enabled: true, Config: config})
		if err != nil {
			t.Fatal(err)
		}
		key, err := f.keys.CreateInScheme(ctx, f.userID, scheme.ID, name, nil)
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, key.Secret)
		schemeIDs = append(schemeIDs, scheme.ID)
	}
	call := func(secret string, want int) {
		t.Helper()
		req, _ := http.NewRequest("POST", f.server.URL+"/v1/responses", strings.NewReader(`{"model":"synthetic-model","input":[]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+secret)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != want {
			t.Fatal(response.StatusCode, string(body))
		}
	}
	call(secrets[0], 200)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.server.URL, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer " + secrets[0]}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"synthetic-model","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	_, message, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(message), "allocation_exhausted") {
		t.Fatal("WS bypassed quota", string(message), err)
	}
	call(secrets[1], 200)
	duplicate, err := f.keys.CreateInScheme(ctx, f.userID, schemeIDs[1], "Synthetic duplicate", nil)
	if err != nil {
		t.Fatal(err)
	}
	call(duplicate.Secret, 403)
	page, err := manager.Own(ctx, f.userID)
	if err != nil || len(page) != 2 {
		t.Fatal(page, err)
	}
	if page[0].Balances[0].Used != 5 || page[1].Balances[0].Used != 18 {
		t.Fatal("ledgers mixed", page)
	}
}

func TestTokenShareGatewayChargesWithoutQuotaSynchronization(t *testing.T) {
	f := newForwardFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n")
	})
	ctx := context.Background()
	accounts, err := f.accounts.List(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatal(accounts, err)
	}
	if _, err := f.groups.Save(ctx, 1, groups.Input{Name: "Default", Enabled: true, AccountIDs: []string{}}); err != nil {
		t.Fatal(err)
	}
	pool, err := f.groups.Save(ctx, 0, groups.Input{Name: "Synthetic pool", Enabled: true, AccountIDs: []string{accounts[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.groups.SetMemberGroups(ctx, f.userID, []int64{pool.ID}); err != nil {
		t.Fatal(err)
	}
	manager := f.forwarding.Allocations()
	scheme, err := manager.SaveScheme(ctx, 0, allocations.SchemeInput{Name: "Synthetic token shares", GroupID: pool.ID, Enabled: true,
		Config: allocations.Config{Mode: "ratio", RatioUnit: "tokens", Period: "month", Total: 10, Members: []allocations.Share{{UserID: f.userID, Limit: 5000}}}})
	if err != nil {
		t.Fatal(err)
	}
	key, err := f.keys.CreateInScheme(ctx, f.userID, scheme.ID, "Synthetic key", nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(want int) {
		t.Helper()
		req, _ := http.NewRequest("POST", f.server.URL+"/v1/responses", strings.NewReader(`{"model":"synthetic-model","input":[]}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key.Secret)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != want {
			body, _ := io.ReadAll(response.Body)
			t.Fatal(response.StatusCode, string(body))
		}
	}
	call(200)
	call(403)
	detail, err := manager.Detail(ctx, scheme.ID, f.userID)
	if err != nil || len(detail.Balances) != 1 || detail.Balances[0].Mode != "tokens" || detail.Balances[0].Limit != 5 || detail.Balances[0].Used != 5 {
		t.Fatal(detail, err)
	}
}

type keyShareFixture struct {
	forwardFixture
	pool    int64
	limited []string
	keyIDs  []int64
	open    string
}

// Each synthetic response costs 5 micro-USD at the fixture rate.
func newKeyShareFixture(t *testing.T) keyShareFixture {
	t.Helper()
	f := newForwardFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n")
	})
	ctx := context.Background()
	list, err := f.accounts.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	if _, err := f.groups.Save(ctx, 1, groups.Input{Name: "Default", Enabled: true, AccountIDs: []string{}}); err != nil {
		t.Fatal(err)
	}
	pool, err := f.groups.Save(ctx, 0, groups.Input{Name: "Synthetic shared pool", Enabled: true, AccountIDs: []string{list[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.groups.SetMemberGroups(ctx, f.userID, []int64{pool.ID}); err != nil {
		t.Fatal(err)
	}
	out := keyShareFixture{forwardFixture: f, pool: pool.ID}
	for _, name := range []string{"Synthetic holder A", "Synthetic holder B", "Synthetic owner"} {
		key, err := f.keys.CreateInGroup(ctx, f.userID, pool.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		if name == "Synthetic owner" {
			out.open = key.Secret
			continue
		}
		out.limited = append(out.limited, key.Secret)
		out.keyIDs = append(out.keyIDs, key.Key.ID)
	}
	return out
}

func (f keyShareFixture) save(t *testing.T, id int64, enabled bool, shares ...int64) allocations.Scheme {
	t.Helper()
	keys := []allocations.KeyShare{}
	for i, share := range shares {
		keys = append(keys, allocations.KeyShare{KeyID: f.keyIDs[i], Shares: []allocations.KeyWindowShare{{DurationSeconds: 5 * 3600, Share: share}}})
	}
	scheme, err := f.forwarding.Allocations().SaveScheme(context.Background(), id, allocations.SchemeInput{Name: "Synthetic key shares", GroupID: f.pool, Enabled: enabled, Config: allocations.Config{
		Mode: "keys", Period: "durations", Windows: []allocations.WindowCondition{{DurationSeconds: 5 * 3600, Limit: 25}},
		Keys:  keys,
		Rates: []allocations.Rate{{Model: "synthetic-model", Input: 1_000_000, Output: 1_000_000}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return scheme
}

func (f keyShareFixture) call(t *testing.T, secret string, want int) http.Header {
	t.Helper()
	req, _ := http.NewRequest("POST", f.server.URL+"/v1/responses", strings.NewReader(`{"model":"synthetic-model","input":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != want {
		t.Fatal(response.StatusCode, string(body))
	}
	if want == 429 && !strings.Contains(string(body), "allocation_key_window_exhausted") {
		t.Fatal("unexpected refusal", string(body))
	}
	return response.Header
}

func TestKeyShareLimitsSelectedKeysOnly(t *testing.T) {
	f := newKeyShareFixture(t)
	// 40% of a 25 micro-USD budget admits two 5 micro-USD requests.
	f.save(t, 0, true, 4000, 4000)
	f.call(t, f.limited[0], 200)
	f.call(t, f.limited[0], 200)
	header := f.call(t, f.limited[0], 429)
	if retry, err := strconv.Atoi(header.Get("Retry-After")); err != nil || retry < 5*3600-60 || retry > 5*3600 || header.Get("X-SubLane-Allocation-Window") != "18000" {
		t.Fatal("missing recovery time", header)
	}
	f.call(t, f.limited[1], 200)
	for range 3 {
		f.call(t, f.open, 200)
	}
}

func TestKeyShareChangesApplyImmediatelyWithoutClearingUsage(t *testing.T) {
	f := newKeyShareFixture(t)
	scheme := f.save(t, 0, true, 4000)
	f.call(t, f.limited[0], 200)
	f.call(t, f.limited[0], 200)
	f.call(t, f.limited[0], 429)
	// 60% of 25 is 15: the 10 already used still counts, leaving room for exactly one request.
	f.save(t, scheme.ID, true, 6000)
	f.call(t, f.limited[0], 200)
	f.call(t, f.limited[0], 429)
	f.save(t, scheme.ID, true, 2000)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.server.URL, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer " + f.limited[0]}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"synthetic-model","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	_, message, err := conn.ReadMessage()
	if err != nil || !strings.Contains(string(message), "allocation_key_window_exhausted") {
		t.Fatal("WebSocket bypassed key share", string(message), err)
	}
}

func TestKeyShareSelectionStaysInsideItsPool(t *testing.T) {
	f := newKeyShareFixture(t)
	ctx := context.Background()
	manager := f.forwarding.Allocations()
	scheme := f.save(t, 0, true, 4000)
	input := func(key int64) allocations.SchemeInput {
		return allocations.SchemeInput{Name: "Synthetic key shares", GroupID: f.pool, Enabled: true, Config: allocations.Config{
			Mode: "keys", Period: "durations", Windows: []allocations.WindowCondition{{DurationSeconds: 5 * 3600, Limit: 25}},
			Keys:  []allocations.KeyShare{{KeyID: key, Shares: []allocations.KeyWindowShare{{DurationSeconds: 5 * 3600, Share: 4000}}}},
			Rates: []allocations.Rate{{Model: "synthetic-model", Input: 1_000_000, Output: 1_000_000}},
		}}
	}
	otherPool, err := f.groups.Save(ctx, 0, groups.Input{Name: "Synthetic other pool", Enabled: true, AccountIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.groups.SetMemberGroups(ctx, f.userID, []int64{f.pool, otherPool.ID}); err != nil {
		t.Fatal(err)
	}
	other, err := f.keys.CreateInGroup(ctx, f.userID, otherPool.ID, "Synthetic other pool")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SaveScheme(ctx, scheme.ID, input(other.Key.ID)); !errors.Is(err, allocations.ErrInput) {
		t.Fatal("key from another pool accepted", err)
	}
	if _, err := f.keys.Revoke(ctx, f.userID, f.keyIDs[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SaveScheme(ctx, scheme.ID, input(f.keyIDs[1])); !errors.Is(err, allocations.ErrInput) {
		t.Fatal("revoked key accepted", err)
	}
	member := input(0)
	member.Config = allocations.Config{Mode: "tokens", Period: "day", Members: []allocations.Share{{UserID: f.userID, Limit: 5}}}
	if _, err := manager.SaveScheme(ctx, scheme.ID, member); !errors.Is(err, allocations.ErrInput) {
		t.Fatal("key-share scheme switched to member mode", err)
	}
	if _, err := f.keys.CreateInScheme(ctx, f.userID, scheme.ID, "Synthetic self-selected", nil); err == nil {
		t.Fatal("member created a key inside a key-share scheme")
	}
	login, err := f.identity.Login(ctx, "member-test", "member pass 42")
	if err != nil {
		t.Fatal(err)
	}
	response := request(f.server.Config.Handler, http.MethodGet, "/api/keys/groups", "", nil, &http.Cookie{Name: sessionCookie, Value: login.Token})
	var choices struct {
		Groups []groups.Choice `json:"groups"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &choices); err != nil {
		t.Fatal(err, response.Body.String())
	}
	found := false
	for _, choice := range choices.Groups {
		if choice.ID == f.pool {
			found = choice.SchemeID == 0
		}
	}
	if !found {
		t.Fatal("key-share pool not offered for ordinary keys", choices.Groups)
	}
}

func TestKeyShareDisablingOrRemovingLiftsLimits(t *testing.T) {
	f := newKeyShareFixture(t)
	ctx := context.Background()
	scheme := f.save(t, 0, true, 4000, 4000)
	f.call(t, f.limited[0], 200)
	f.call(t, f.limited[0], 200)
	f.call(t, f.limited[0], 429)
	if err := f.forwarding.Allocations().SetEnabled(ctx, scheme.ID, false); err != nil {
		t.Fatal(err)
	}
	f.call(t, f.limited[0], 200)
	f.call(t, f.limited[1], 200)
	f.call(t, f.limited[1], 200)
	if err := f.forwarding.Allocations().SetEnabled(ctx, scheme.ID, true); err != nil {
		t.Fatal(err)
	}
	// Usage made while disabled still counts once the scheme is enabled again.
	f.call(t, f.limited[1], 429)
	f.save(t, scheme.ID, true, 4000)
	f.call(t, f.limited[1], 200)
	if _, err := f.keys.Update(ctx, f.userID, f.keyIDs[0], apikey.UpdateInput{Name: "Synthetic holder A", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err := f.forwarding.Allocations().SetEnabled(ctx, scheme.ID, false); err != nil {
		t.Fatal(err)
	}
	f.call(t, f.limited[0], 401)
}

func TestKeyShareReportIsAdministratorOnly(t *testing.T) {
	f := newKeyShareFixture(t)
	ctx := context.Background()
	scheme := f.save(t, 0, true, 4000, 6000)
	f.call(t, f.limited[0], 200)
	f.call(t, f.limited[0], 200)
	owner, err := f.identity.Login(ctx, "owner-test", "owner pass 42")
	if err != nil {
		t.Fatal(err)
	}
	member, err := f.identity.Login(ctx, "member-test", "member pass 42")
	if err != nil {
		t.Fatal(err)
	}
	h := f.server.Config.Handler
	path := fmt.Sprintf("/api/allocations/%d", scheme.ID)
	if r := request(h, "GET", path, "", nil, &http.Cookie{Name: sessionCookie, Value: member.Token}); r.Code != 403 {
		t.Fatal("member read key-share report", r.Code)
	}
	if r := request(h, "GET", "/api/me/allocations", "", nil, &http.Cookie{Name: sessionCookie, Value: member.Token}); r.Code != 200 || r.Body.String() != "{\"schemes\":[]}\n" {
		t.Fatal("key-share report leaked into personal allowances", r.Code, r.Body.String())
	}
	admin := &http.Cookie{Name: sessionCookie, Value: owner.Token}
	response := request(h, "GET", path, "", nil, admin)
	if response.Code != 200 || strings.Contains(response.Body.String(), "sl_") && strings.Contains(response.Body.String(), f.limited[0]) {
		t.Fatal(response.Code, response.Body.String())
	}
	var detail struct {
		Balances []struct {
			KeyID         int64  `json:"key_id"`
			KeyName       string `json:"key_name"`
			WindowSeconds int64  `json:"window_seconds"`
			Share         int64  `json:"share"`
			Limit         int64  `json:"limit"`
			Used          int64  `json:"used"`
			Admission     string `json:"admission"`
			ResetAt       int64  `json:"reset_at"`
		} `json:"balances"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil || len(detail.Balances) != 2 {
		t.Fatal(err, response.Body.String())
	}
	first, second := detail.Balances[0], detail.Balances[1]
	if first.KeyID != f.keyIDs[0] || first.KeyName != "Synthetic holder A" || first.WindowSeconds != 5*3600 || first.Share != 4000 || first.Limit != 10 || first.Used != 10 || first.Admission != "exhausted" || first.ResetAt <= time.Now().Unix() {
		t.Fatal("exhausted key report", first)
	}
	if second.KeyID != f.keyIDs[1] || second.Limit != 15 || second.Used != 0 || second.Admission != "active" {
		t.Fatal("idle key report", second)
	}
	candidates := request(h, "GET", fmt.Sprintf("/api/allocations/keys?group_id=%d", f.pool), "", nil, admin)
	var choices struct {
		Keys []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			Prefix string `json:"prefix"`
			Owner  string `json:"owner"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(candidates.Body.Bytes(), &choices); err != nil || candidates.Code != 200 || len(choices.Keys) != 3 || choices.Keys[0].ID != f.keyIDs[0] || choices.Keys[0].Owner != "member-test" {
		t.Fatal("key candidates", candidates.Code, candidates.Body.String())
	}
	if r := request(h, "GET", fmt.Sprintf("/api/allocations/keys?group_id=%d", f.pool), "", nil, &http.Cookie{Name: sessionCookie, Value: member.Token}); r.Code != 403 {
		t.Fatal("member listed key candidates", r.Code)
	}
}
