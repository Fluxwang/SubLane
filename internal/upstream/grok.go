package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
)

// Public Grok Build OAuth parameters match the pinned CLIProxyAPI xAI adapter.
const grokClientID = "b1a00492-073a-47ea-816f-4c329264a828"

// The chat proxy rejects obsolete CLI versions with HTTP 426. Keep this fallback on the official stable channel.
const DefaultGrokVersion = "1.0.50"

func (c *Client) currentGrokVersion() string {
	if c.grokVersion != nil {
		if version := c.grokVersion(); version != "" {
			return version
		}
	}
	return DefaultGrokVersion
}

func applyGrokVersion(headers http.Header, version string) {
	headers.Set("x-grok-client-version", version)
	headers.Set("User-Agent", "xai-grok-workspace/"+version)
}

var (
	ErrDeviceDenied  = errors.New("device_access_denied")
	ErrDeviceExpired = errors.New("device_expired")
)

type DeviceAuthorization struct {
	DeviceCode  string `json:"device_code"`
	UserCode    string `json:"user_code"`
	URL         string `json:"verification_uri"`
	CompleteURL string `json:"verification_uri_complete"`
	ExpiresIn   int64  `json:"expires_in"`
	Interval    int64  `json:"interval"`
}

type DevicePendingError struct{ SlowDown bool }

func (e *DevicePendingError) Error() string { return "device_authorization_pending" }

func (c *Client) BeginDevice(ctx context.Context, provider string) (DeviceAuthorization, error) {
	if provider != "xai" {
		return DeviceAuthorization{}, ErrInput
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	form := url.Values{"client_id": {grokClientID}, "scope": {"openid profile email offline_access grok-cli:access api:access"}}
	response, raw, err := c.grokForm(ctx, "/oauth2/device/code", form)
	if err != nil {
		return DeviceAuthorization{}, err
	}
	if response.StatusCode != http.StatusOK {
		return DeviceAuthorization{}, tokenResponseFailure(response.StatusCode, response.Header, raw)
	}
	var device DeviceAuthorization
	if json.Unmarshal(raw, &device) != nil || device.DeviceCode == "" || len(device.DeviceCode) > 16384 || device.UserCode == "" || len(device.UserCode) > 128 || device.ExpiresIn <= 0 || device.ExpiresIn > 1800 || device.Interval < 0 || device.Interval > 60 {
		return DeviceAuthorization{}, ErrResponse
	}
	validURL := func(raw string) bool {
		u, err := url.Parse(raw)
		return err == nil && u.Scheme == "https" && (u.Host == "accounts.x.ai" || u.Host == "auth.x.ai" || u.Host == "x.ai" || u.Host == "grok.com") && u.User == nil && u.Fragment == ""
	}
	if !validURL(device.URL) || device.CompleteURL != "" && !validURL(device.CompleteURL) {
		return DeviceAuthorization{}, ErrResponse
	}
	device.Interval = max(5, device.Interval)
	return device, nil
}

func (c *Client) ExchangeDevice(ctx context.Context, provider, code string) (accounts.Credential, error) {
	if provider != "xai" || code == "" || len(code) > 16384 {
		return accounts.Credential{}, ErrInput
	}
	return c.grokTokens(ctx, url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {code}}, accounts.Credential{Provider: "xai"})
}

func (c *Client) grokForm(ctx context.Context, path string, form url.Values) (*http.Response, []byte, error) {
	form.Set("client_id", grokClientID)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://auth.x.ai"+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, ErrInput
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, nil, ErrUpstream
	}
	defer response.Body.Close()
	raw, err := readBounded(response.Body, 128<<10)
	return response, raw, err
}

func (c *Client) grokTokens(ctx context.Context, form url.Values, old accounts.Credential) (accounts.Credential, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	response, raw, err := c.grokForm(ctx, "/oauth2/token", form)
	if err != nil {
		return accounts.Credential{}, err
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Error        string `json:"error"`
	}
	decodeErr := json.Unmarshal(raw, &token)
	if response.StatusCode != http.StatusOK {
		if form.Get("grant_type") != "refresh_token" {
			switch token.Error {
			case "authorization_pending":
				return accounts.Credential{}, &DevicePendingError{}
			case "slow_down":
				return accounts.Credential{}, &DevicePendingError{SlowDown: true}
			case "access_denied":
				return accounts.Credential{}, ErrDeviceDenied
			case "expired_token":
				return accounts.Credential{}, ErrDeviceExpired
			}
		}
		return accounts.Credential{}, tokenResponseFailure(response.StatusCode, response.Header, raw)
	}
	if decodeErr != nil || token.AccessToken == "" || token.ExpiresIn <= 0 || token.ExpiresIn > 366*86400 || token.Error != "" {
		return accounts.Credential{}, ErrResponse
	}
	next := old
	next.Provider = "xai"
	next.AccessToken = token.AccessToken
	next.ExpiresAt = time.Now().Unix() + token.ExpiresIn
	if token.RefreshToken != "" {
		next.RefreshToken = token.RefreshToken
	}
	if token.IDToken != "" {
		next.IDToken = token.IDToken
	}
	encoded, _ := json.Marshal(next)
	return accounts.ParseFor("xai", encoded)
}
