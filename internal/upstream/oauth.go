package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
)

// Public installed-app OAuth parameters match the pinned SDK's MIT-licensed provider adapters.
// Web state/PKCE and durable storage remain owned by SubLane rather than the SDK's CLI login flow.
const claudeClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

func RedirectURI(provider string) string {
	switch provider {
	case "claude":
		return "http://localhost:54545/callback"
	case "antigravity":
		return sdkauth.AntigravityDefaultCallbackURI()
	default:
		return redirectURI
	}
}
func (c *Client) AuthorizationURLFor(provider, state, challenge string) (string, error) {
	if provider == "codex" {
		return c.AuthorizationURL(state, challenge), nil
	}
	if !accounts.ValidProvider(provider) || provider == "xai" {
		return "", ErrInput
	}
	values := url.Values{"state": {state}, "redirect_uri": {RedirectURI(provider)}, "response_type": {"code"}}
	endpoint := "https://claude.ai/oauth/authorize"
	if provider == "claude" {
		values.Set("client_id", claudeClientID)
		values.Set("code", "true")
		values.Set("scope", "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload")
		values.Set("code_challenge", challenge)
		values.Set("code_challenge_method", "S256")
	} else {
		return sdkauth.BuildAntigravityAuthURL(state, RedirectURI(provider)), nil
	}
	return endpoint + "?" + values.Encode(), nil
}
func (c *Client) ExchangeFor(ctx context.Context, provider, code, state, verifier string) (accounts.Credential, error) {
	if provider == "codex" {
		return c.Exchange(ctx, code, verifier)
	}
	return c.providerTokens(ctx, provider, code, state, verifier, accounts.Credential{Provider: provider})
}
func (c *Client) providerTokens(ctx context.Context, provider, code, state, verifier string, old accounts.Credential) (accounts.Credential, error) {
	if provider == "xai" {
		if code != "" {
			return accounts.Credential{}, ErrInput
		}
		return c.grokTokens(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {old.RefreshToken}}, old)
	}
	if provider == "antigravity" {
		return c.antigravityTokens(ctx, code, old)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	endpoint := "https://platform.claude.com/v1/oauth/token"
	contentType := "application/json"
	var raw []byte
	if provider == "claude" {
		if code != "" {
			if value, fragment, found := strings.Cut(code, "#"); found {
				if fragment != state {
					return accounts.Credential{}, ErrInput
				}
				code = value
			}
			raw, _ = json.Marshal(struct {
				GrantType   string `json:"grant_type"`
				Code        string `json:"code"`
				RedirectURI string `json:"redirect_uri"`
				ClientID    string `json:"client_id"`
				Verifier    string `json:"code_verifier"`
				State       string `json:"state"`
			}{"authorization_code", code, RedirectURI(provider), claudeClientID, verifier, state})
		} else {
			raw, _ = json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": old.RefreshToken, "client_id": claudeClientID})
		}

	} else {
		return accounts.Credential{}, ErrInput
	}
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(raw))
	if err != nil {
		return accounts.Credential{}, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return accounts.Credential{}, ErrUpstream
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return accounts.Credential{}, tokenFailure(response)
	}
	body, err := readBounded(response.Body, 128<<10)
	if err != nil {
		return accounts.Credential{}, err
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Account      struct {
			UUID  string `json:"uuid"`
			Email string `json:"email_address"`
		} `json:"account"`
		Organization struct {
			UUID string `json:"uuid"`
		} `json:"organization"`
	}
	if json.Unmarshal(body, &token) != nil || token.AccessToken == "" || token.ExpiresIn <= 0 || token.ExpiresIn > 366*86400 {
		return accounts.Credential{}, ErrResponse
	}
	next := old
	next.Metadata = maps.Clone(old.Metadata)
	next.Provider = provider
	next.AccessToken = token.AccessToken
	next.ExpiresAt = time.Now().Unix() + token.ExpiresIn
	if token.RefreshToken != "" {
		next.RefreshToken = token.RefreshToken
	}
	if next.Metadata == nil {
		next.Metadata = make(map[string]json.RawMessage)
	}
	if token.Account.Email != "" {
		next.Email = token.Account.Email
	}
	if token.Account.UUID != "" {
		next.Metadata["account_uuid"], _ = json.Marshal(token.Account.UUID)
	}
	if token.Organization.UUID != "" {
		next.Metadata["organization_uuid"], _ = json.Marshal(token.Organization.UUID)
	}
	if next.Email == "" {
		profileURL := "https://www.googleapis.com/oauth2/v2/userinfo?alt=json"
		if provider == "claude" {
			profileURL = "https://api.anthropic.com/api/oauth/profile"
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", profileURL, nil)
		req.Header.Set("Authorization", "Bearer "+next.AccessToken)
		req.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
		response, err := c.http.Do(req)
		if err != nil {
			return accounts.Credential{}, ErrUpstream
		}
		defer response.Body.Close()
		data, err := readBounded(response.Body, 128<<10)
		if err != nil || response.StatusCode != 200 {
			return accounts.Credential{}, ErrUpstream
		}
		var profile struct {
			Email   string `json:"email"`
			Account struct {
				UUID  string `json:"uuid"`
				Email string `json:"email_address"`
			} `json:"account"`
		}
		if json.Unmarshal(data, &profile) != nil {
			return accounts.Credential{}, ErrResponse
		}
		next.Email = profile.Email
		if provider == "claude" {
			next.Email = profile.Account.Email
			if profile.Account.UUID != "" {
				next.Metadata["account_uuid"], _ = json.Marshal(profile.Account.UUID)
			}
		}
	}
	fields := map[string]json.RawMessage{}
	encoded, _ := json.Marshal(next)
	json.Unmarshal(encoded, &fields)
	delete(fields, "metadata")
	for key, value := range next.Metadata {
		fields[key] = value
	}
	fields["type"], _ = json.Marshal(provider)
	encoded, _ = json.Marshal(fields)
	return accounts.ParseFor(provider, encoded)
}
