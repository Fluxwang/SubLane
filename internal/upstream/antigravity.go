package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
)

func (c *Client) antigravityTokens(ctx context.Context, code string, old accounts.Credential) (accounts.Credential, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	transport := &oauthTransport{parent: ctx, base: c.http.Transport}
	if code != "" {
		client := &http.Client{Transport: transport, CheckRedirect: c.http.CheckRedirect}
		token, err := sdkauth.ExchangeAntigravityCode(ctx, code, RedirectURI("antigravity"), client)
		if err != nil {
			return accounts.Credential{}, transport.failure()
		}
		email, err := sdkauth.FetchAntigravityUserInfo(ctx, token.AccessToken, client)
		if err != nil {
			return accounts.Credential{}, transport.failure()
		}
		value := accounts.Credential{Provider: "antigravity", AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, Email: email, ExpiresAt: time.Now().Unix() + token.ExpiresIn}
		raw, _ := json.Marshal(value)
		return accounts.ParseFor("antigravity", raw)
	}
	executor, err := c.executor("antigravity")
	if err != nil {
		return accounts.Credential{}, err
	}
	auth := sdkAuth(old)
	// Only the explicit refresh call receives this token. The returned snapshot still
	// belongs to accounts.Prepare, which must persist it before any model execution.
	auth.Metadata["refresh_token"] = old.RefreshToken
	updated, err := executor.Refresh(context.WithValue(ctx, "cliproxy.roundtripper", transport), auth)
	if err != nil {
		return accounts.Credential{}, transport.failure()
	}
	if updated == nil {
		return accounts.Credential{}, ErrResponse
	}
	raw, err := json.Marshal(updated.Metadata)
	if err != nil {
		return accounts.Credential{}, ErrResponse
	}
	return accounts.ParseFor("antigravity", raw)
}

// SDK refresh may detach its context and read full bodies. Enforce SubLane's
// deadline, body bound, redirect policy and sanitized errors at the transport seam.
type oauthTransport struct {
	parent context.Context
	base   http.RoundTripper
	mu     sync.Mutex
	err    error
}

func (t *oauthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request.Clone(t.parent))
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		response.Body.Close()
		return nil, ErrUpstream
	}
	body, err := readBounded(response.Body, 128<<10)
	response.Body.Close()
	if err != nil {
		t.record(ErrResponse)
		return nil, ErrResponse
	}
	if request.URL.Host == "oauth2.googleapis.com" && request.URL.Path == "/token" {
		if response.StatusCode != http.StatusOK {
			t.record(tokenResponseFailure(response.StatusCode, response.Header, body))
		}
		if response.StatusCode == 200 {
			var token struct {
				AccessToken string `json:"access_token"`
				ExpiresIn   int64  `json:"expires_in"`
			}
			if json.Unmarshal(body, &token) != nil || token.AccessToken == "" || token.ExpiresIn <= 0 || token.ExpiresIn > 366*86400 {
				t.record(ErrResponse)
				return nil, ErrResponse
			}
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	return response, nil
}
func (t *oauthTransport) record(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err == nil {
		t.err = err
	}
}
func (t *oauthTransport) failure() error {
	if err := t.parent.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return t.err
	}
	return ErrUpstream
}
