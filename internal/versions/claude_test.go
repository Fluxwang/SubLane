package versions

import (
	"context"
	"net/http"
	"testing"
)

func TestClaudeOfficialVersionUsesStableTag(t *testing.T) {
	source := NewClaudeReleases(transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://registry.npmjs.org/-/package/@anthropic-ai/claude-code/dist-tags" || r.Header.Get("Authorization") != "" {
			t.Fatal("unsafe Claude release request")
		}
		return releaseResponse(200, `{"stable":"2.200.0","latest":"2.900.0","next":"2.900.0"}`), nil
	}))
	defer source.Close()
	version, err := source.Latest(context.Background())
	if err != nil || version != "2.200.0" {
		t.Fatal("followed the wrong release channel", version, err)
	}
}

func TestClaudeVersionDoesNotFallbackToLatestOrPrereleases(t *testing.T) {
	for _, body := range []string{`{"latest":"2.900.0"}`, `{"stable":"2.200.0-beta.1","latest":"2.900.0"}`, `{"stable":"invalid"}`} {
		source := NewClaudeReleases(transportFunc(func(*http.Request) (*http.Response, error) { return releaseResponse(200, body), nil }))
		if _, err := source.Latest(context.Background()); err == nil {
			t.Fatal("accepted unsafe stable channel")
		}
		source.Close()
	}
}
