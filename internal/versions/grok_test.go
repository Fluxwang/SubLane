package versions

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/murongg/SubLane/internal/storage"
)

func TestGrokOfficialStableVersionSelection(t *testing.T) {
	source := NewGrokReleases(transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://registry.npmjs.org/-/package/@xai-official/grok/dist-tags" || r.Method != http.MethodGet {
			t.Fatal("unexpected Grok version source", r.URL)
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("User-Agent") != "SubLane" {
			t.Fatal("unsafe version request headers")
		}
		return releaseResponse(200, `{"latest":"1.200.0","alpha":"1.900.0"}`), nil
	}))
	defer source.Close()
	version, err := source.Latest(context.Background())
	if err != nil || version != "1.200.0" {
		t.Fatal(version, err)
	}
}

func TestGrokVersionSourceRejectsUnsafeResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"prerelease", 200, `{"latest":"1.200.0-alpha.1"}`},
		{"missing stable", 200, `{"alpha":"1.900.0"}`},
		{"malformed", 200, `{"latest":`},
		{"extra JSON", 200, `{"latest":"1.200.0"}{}`},
		{"oversized", 200, `{"padding":"` + strings.Repeat("x", 64<<10) + `","latest":"1.200.0"}`},
		{"redirect", 302, ""},
		{"rate limited", 429, `{"error":"synthetic private detail"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			source := NewGrokReleases(transportFunc(func(*http.Request) (*http.Response, error) {
				calls++
				response := releaseResponse(test.status, test.body)
				response.Header.Set("Location", "https://untrusted.example.test/versions")
				response.Header.Set("Retry-After", "120")
				return response, nil
			}))
			defer source.Close()
			_, err := source.Latest(context.Background())
			if err == nil || strings.Contains(err.Error(), "synthetic private") || calls != 1 {
				t.Fatal("unsafe version source result", err, calls)
			}
			if test.status == 429 {
				var limited *rateLimit
				if !errors.As(err, &limited) || limited.retry.Seconds() != 120 {
					t.Fatal("version check lost upstream backoff", err)
				}
			}
		})
	}
}

func TestProviderVersionPoliciesKeepLegacyCodexStateAndRemainIndependent(t *testing.T) {
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "versions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, err = connection.Exec(`INSERT INTO settings(key,value) VALUES('codex.version','{"manual_version":"0.150.0","auto_sync":false}')`)
	if err != nil {
		t.Fatal(err)
	}
	codex, err := New(ctx, connection, "0.100.0", func(context.Context) (string, error) { return "0.200.0", nil })
	if err != nil {
		t.Fatal(err)
	}
	defer codex.Close()
	grok, err := NewForProvider(ctx, connection, Grok, "1.100.0", func(context.Context) (string, error) { return "1.200.0", nil })
	if err != nil {
		t.Fatal(err)
	}
	defer grok.Close()
	if codex.Current() != "0.150.0" || codex.View().AutoSync || grok.Current() != "1.100.0" || !grok.View().AutoSync {
		t.Fatal("provider settings collided", codex.View(), grok.View())
	}
	if _, err := grok.Sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := grok.Save(ctx, Config{ManualVersion: "1.150.0", AutoSync: false}); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewForProvider(ctx, connection, Grok, "1.100.0", func(context.Context) (string, error) { return "", ErrSync })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Current() != "1.150.0" || reopened.View().LatestVersion != "1.200.0" || reopened.View().AutoSync || codex.Current() != "0.150.0" {
		t.Fatal("provider policy did not persist independently", reopened.View(), codex.View())
	}
}
