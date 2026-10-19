package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/murongg/SubLane/internal/gateway"
)

func TestHourlyActivityHTTPUsesTheSessionIdentity(t *testing.T) {
	f := newForwardFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"output\":[]}}\n\n")
	})
	ctx := context.Background()
	ownerKey, err := f.keys.CreateInGroup(ctx, 1, 1, "Synthetic owner client")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{f.secret, ownerKey.Secret, ownerKey.Secret} {
		request, _ := http.NewRequest("POST", f.server.URL+"/v1/responses", strings.NewReader(`{"model":"synthetic-model","input":"synthetic"}`))
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal("synthetic request failed", response.StatusCode)
		}
	}
	for _, test := range []struct {
		username, password, path string
		want                     int64
	}{
		{"member-test", "member pass 42", "/api/me/usage?days=1&user_id=1&scope=all", 1},
		{"owner-test", "owner pass 42", "/api/usage?days=1", 3},
	} {
		login, err := f.identity.Login(ctx, test.username, test.password)
		if err != nil {
			t.Fatal(err)
		}
		response := request(f.server.Config.Handler, "GET", test.path, "", nil, &http.Cookie{Name: sessionCookie, Value: login.Token})
		var page gateway.Statistics
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil {
			t.Fatal("usage unavailable", response.Code)
		}
		var count int64
		for _, cell := range page.Activity.Cells {
			count += cell.Requests
		}
		if count != test.want {
			t.Fatal("hourly ownership escaped session", test.username, count)
		}
	}
}

func TestKeyUsageRankingHTTPFollowsTheSessionScope(t *testing.T) {
	f := newForwardFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"output\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n")
	})
	ctx := context.Background()
	ownerKey, err := f.keys.CreateInGroup(ctx, 1, 1, "Synthetic owner client")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{f.secret, ownerKey.Secret, ownerKey.Secret} {
		request, _ := http.NewRequest("POST", f.server.URL+"/v1/responses", strings.NewReader(`{"model":"synthetic-model","input":"synthetic"}`))
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	for _, test := range []struct {
		username, password, path string
		want                     []gateway.KeyStatistic
	}{
		{"member-test", "member pass 42", "/api/me/usage?days=1&user_id=1&scope=all", []gateway.KeyStatistic{{Requests: 1}}},
		{"owner-test", "owner pass 42", "/api/usage?days=1", []gateway.KeyStatistic{{Name: "Synthetic owner client", Owner: "owner-test", Requests: 2}, {Owner: "member-test", Requests: 1}}},
	} {
		login, err := f.identity.Login(ctx, test.username, test.password)
		if err != nil {
			t.Fatal(err)
		}
		response := request(f.server.Config.Handler, "GET", test.path, "", nil, &http.Cookie{Name: sessionCookie, Value: login.Token})
		var page gateway.Statistics
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil {
			t.Fatal("usage unavailable", response.Code)
		}
		if len(page.Keys) != len(test.want) {
			t.Fatal("key ranking escaped the session scope", test.username, page.Keys)
		}
		for i, want := range test.want {
			got := page.Keys[i]
			if got.Owner != want.Owner || got.Requests != want.Requests || (want.Name != "" && got.Name != want.Name) {
				t.Fatal("key ranking mislabeled", test.username, got)
			}
		}
	}
	login, err := f.identity.Login(ctx, "member-test", "member pass 42")
	if err != nil {
		t.Fatal(err)
	}
	if response := request(f.server.Config.Handler, "GET", "/api/usage?days=1", "", nil, &http.Cookie{Name: sessionCookie, Value: login.Token}); response.Code != http.StatusForbidden {
		t.Fatal("members reached workspace key usage", response.Code)
	}
}

func TestKeyListHTTPShowsLifetimeUsageOnlyForTheOwnersKeys(t *testing.T) {
	f := newForwardFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"output\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n")
	})
	ctx := context.Background()
	ownerKey, err := f.keys.CreateInGroup(ctx, 1, 1, "Synthetic owner client")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{f.secret, ownerKey.Secret, ownerKey.Secret} {
		request, _ := http.NewRequest("POST", f.server.URL+"/v1/responses", strings.NewReader(`{"model":"synthetic-model","input":"synthetic"}`))
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
	}
	if _, err := f.keys.Revoke(ctx, 1, ownerKey.Key.ID); err != nil {
		t.Fatal(err)
	}
	type page struct {
		KeyTrackingSince int64 `json:"key_tracking_since"`
		Keys             []struct {
			ID    int64             `json:"id"`
			Usage *gateway.KeyUsage `json:"usage"`
		} `json:"keys"`
	}
	for _, test := range []struct {
		username, password, path string
		requests                 int64
	}{
		{"member-test", "member pass 42", "/api/keys", 1},
		{"owner-test", "owner pass 42", "/api/keys?revoked=include", 2},
	} {
		login, err := f.identity.Login(ctx, test.username, test.password)
		if err != nil {
			t.Fatal(err)
		}
		response := request(f.server.Config.Handler, "GET", test.path, "", nil, &http.Cookie{Name: sessionCookie, Value: login.Token})
		var value page
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &value) != nil {
			t.Fatal("key list unavailable", response.Code)
		}
		if value.KeyTrackingSince <= 0 || len(value.Keys) != 1 || value.Keys[0].Usage == nil {
			t.Fatal("missing key lifetime usage", test.username, response.Body.String())
		}
		if usage := value.Keys[0].Usage; usage.Requests != test.requests || usage.InputTokens != 7*test.requests {
			t.Fatal("key lifetime usage escaped its owner", test.username, usage)
		}
	}
}
