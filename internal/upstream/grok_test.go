package upstream

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
)

func TestGrokInferenceMeetsCurrentClientVersionFloor(t *testing.T) {
	client := NewWithTransport(usageTransport(func(r *http.Request) (*http.Response, error) {
		status := http.StatusOK
		body := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-response\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
		version := r.Header.Get("x-grok-client-version")
		parts := strings.Split(version, ".")
		acceptable := false
		if len(parts) == 3 {
			major, _ := strconv.Atoi(parts[0])
			minor, _ := strconv.Atoi(parts[1])
			patch, _ := strconv.Atoi(parts[2])
			acceptable = major > 1 || major == 1 && (minor > 0 || patch >= 13)
		}
		if !acceptable || r.Header.Get("User-Agent") != "xai-grok-workspace/"+version {
			status, body = http.StatusUpgradeRequired, `{"error":"synthetic obsolete client version"}`
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	defer client.Close()
	credential := accounts.Credential{Provider: "xai", AccountID: "synthetic-subject", AccessToken: "synthetic-access", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	response, err := client.Chat(context.Background(), credential, []byte(`{"model":"synthetic-model","messages":[{"role":"user","content":"Synthetic prompt."}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Grok inference rejected the client version: HTTP %d", response.StatusCode)
	}
}

func TestConfiguredGrokVersionReachesInferenceAndBilling(t *testing.T) {
	version := "1.200.0"
	resolutions, calls := 0, 0
	client := NewWithOptions(Options{GrokVersion: func() string { resolutions++; return version }, Transport: usageTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("x-grok-client-version") != version || r.Header.Get("User-Agent") != "xai-grok-workspace/"+version {
			t.Fatal("configured Grok version did not reach outbound headers", r.Header)
		}
		body := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-response\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
		if r.URL.Path == "/v1/billing" {
			body = `{"config":null}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	defer client.Close()
	credential := accounts.Credential{Provider: "xai", AccountID: "synthetic-subject", AccessToken: "synthetic-access", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	for _, current := range []string{"1.200.0", "1.201.0"} {
		version = current
		response, err := client.Chat(context.Background(), credential, []byte(`{"model":"synthetic-model","messages":[{"role":"user","content":"Synthetic prompt."}]}`), nil)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if _, err := client.Usage(context.Background(), credential); err != nil {
			t.Fatal(err)
		}
	}
	if resolutions != 4 || calls != 4 {
		t.Fatal("version was not captured once per operation", resolutions, calls)
	}
}

func TestGrokVersionOverrideIsScopedAndDoesNotMutateSDKRequest(t *testing.T) {
	for _, target := range []string{
		"https://cli-chat-proxy.grok.com/v1/responses",
		"https://api.x.ai/v1/models",
		"https://auth.x.ai/oauth2/token",
		"https://untrusted.example.test/v1/responses",
	} {
		request, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("User-Agent", "synthetic-sdk-agent")
		transport := &engineTransport{grokVersion: "1.200.0", base: usageTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host == "cli-chat-proxy.grok.com" {
				if r.Header.Get("x-grok-client-version") != "1.200.0" {
					t.Fatal("missing scoped version override")
				}
			} else if r.Header.Get("x-grok-client-version") != "" || r.Header.Get("User-Agent") != "synthetic-sdk-agent" {
				t.Fatal("unrelated upstream headers were overwritten")
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: http.NoBody}, nil
		})}
		response, err := transport.RoundTrip(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if request.Header.Get("x-grok-client-version") != "" || request.Header.Get("User-Agent") != "synthetic-sdk-agent" {
			t.Fatal("SDK request object was mutated")
		}
	}
}

func TestGrokDiscoveryAndExecutionUseSubscription(t *testing.T) {
	calls := 0
	client := NewWithTransport(usageTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer synthetic-access" {
			t.Error("missing token")
		}
		body := `{"data":[{"id":"synthetic-model"}]}`
		if r.URL.Path == "/v1/models" {
			if r.URL.Host != "api.x.ai" {
				t.Error("unexpected model host")
			}
		} else {
			if r.URL.Host != "cli-chat-proxy.grok.com" || r.URL.Path != "/v1/responses" || r.Header.Get("X-XAI-Token-Auth") != "xai-grok-cli" {
				t.Errorf("not subscription execution: %s", r.URL)
			}
			body = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic-response\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	defer client.Close()
	c := accounts.Credential{Provider: "xai", AccountID: "synthetic-subject", AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	models, err := client.Models(context.Background(), c)
	if err != nil || len(models) != 1 || models[0].ID != "synthetic-model" {
		t.Fatal("models", models, err)
	}
	response, err := client.Responses(context.Background(), c, []byte(`{"model":"synthetic-model","input":"synthetic prompt"}`), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || calls != 2 {
		t.Fatal("execution failed", calls)
	}
}

func TestGrokRefreshPreservesSubjectAndRotatesTokens(t *testing.T) {
	id := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"synthetic-subject","email":"new@example.test"}`)) + ".synthetic"
	client := NewWithTransport(usageTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://auth.x.ai/oauth2/token" {
			t.Error("unexpected token endpoint")
		}
		r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "synthetic-refresh" {
			t.Error("invalid refresh request")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"synthetic-new","refresh_token":"synthetic-rotated","expires_in":3600,"id_token":"` + id + `"}`))}, nil
	}))
	defer client.Close()
	old := accounts.Credential{Provider: "xai", AccountID: "synthetic-subject", Email: "old@example.test", AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh"}
	c, err := client.Refresh(context.Background(), old)
	if err != nil || c.AccountID != old.AccountID || c.RefreshToken != "synthetic-rotated" || c.Email != "new@example.test" {
		t.Fatal("refresh failed", err)
	}
}

func TestGrokDeviceAuthorizationBoundsAndStates(t *testing.T) {
	code := "authorization_pending"
	client := NewWithTransport(usageTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "auth.x.ai" {
			t.Error("unexpected OAuth host")
		}
		r.ParseForm()
		if r.Form.Get("client_id") != "b1a00492-073a-47ea-816f-4c329264a828" {
			t.Error("missing public client")
		}
		status, body := 200, `{"device_code":"synthetic-device","user_code":"MOCK-CODE","verification_uri":"https://accounts.x.ai/oauth2/device","verification_uri_complete":"https://accounts.x.ai/oauth2/device?user_code=MOCK-CODE","expires_in":1800,"interval":5}`
		if r.URL.Path == "/oauth2/token" {
			if r.Form.Get("device_code") != "synthetic-device" || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Error("invalid device exchange")
			}
			status, body = 400, `{"error":"`+code+`"}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	defer client.Close()
	device, err := client.BeginDevice(context.Background(), "xai")
	if err != nil || device.UserCode != "MOCK-CODE" || device.Interval != 5 {
		t.Fatal("device start failed", err)
	}
	for _, value := range []string{"authorization_pending", "slow_down", "access_denied", "expired_token"} {
		code = value
		_, err := client.ExchangeDevice(context.Background(), "xai", device.DeviceCode)
		var pending *DevicePendingError
		if value == "authorization_pending" || value == "slow_down" {
			if !errors.As(err, &pending) || pending.SlowDown != (value == "slow_down") {
				t.Fatal("poll retry lost", value, err)
			}
		} else if value == "access_denied" && !errors.Is(err, ErrDeviceDenied) || value == "expired_token" && !errors.Is(err, ErrDeviceExpired) {
			t.Fatal("terminal state lost", err)
		}
	}
}

func TestGrokDeviceRejectsUnsafeAndMalformedResponses(t *testing.T) {
	for _, extra := range []string{
		`"verification_uri":"http://auth.x.ai/device"`,
		`"verification_uri":"https://untrusted.example.test/device"`,
		`"verification_uri":"https://synthetic-secret@auth.x.ai/device"`,
		`"verification_uri":"https://auth.x.ai/device","verification_uri_complete":"https://untrusted.example.test"`,
		`"verification_uri":"https://auth.x.ai/device","expires_in":0`,
		`"verification_uri":"https://auth.x.ai/device","interval":3600`,
	} {
		client := NewWithTransport(usageTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"device_code":"synthetic-device","user_code":"MOCK-CODE","expires_in":600,` + extra + `}`))}, nil
		}))
		if _, err := client.BeginDevice(context.Background(), "xai"); !errors.Is(err, ErrResponse) {
			t.Fatal("unsafe device response accepted", err)
		}
		client.Close()
	}
}

func TestGrokRefreshClassifiesCredentialAndTransientFailures(t *testing.T) {
	for _, test := range []struct {
		status int
		body   string
		want   error
	}{
		{400, `{"error":"invalid_grant"}`, accounts.ErrReauthorize},
		{403, `{"error":"forbidden"}`, ErrUpstream},
		{429, `{"error":"rate_limited"}`, accounts.ErrRefresh},
		{429, `synthetic non-JSON rate limit`, accounts.ErrRefresh},
		{503, `{"error":"temporarily_unavailable"}`, accounts.ErrRefresh},
	} {
		client := NewWithTransport(usageTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: test.status, Header: http.Header{"Retry-After": {"45"}}, Body: io.NopCloser(strings.NewReader(test.body))}, nil
		}))
		_, err := client.Refresh(context.Background(), accounts.Credential{Provider: "xai", RefreshToken: "synthetic-refresh"})
		if !errors.Is(err, test.want) {
			t.Fatal("misclassified refresh failure", test.status, err)
		}
		client.Close()
	}
}
