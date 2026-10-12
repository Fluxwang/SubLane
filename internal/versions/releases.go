package versions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const releasesURL = "https://api.github.com/repos/openai/codex/releases"

type Releases struct{ client *http.Client }

func NewReleases(transport http.RoundTripper) *Releases {
	return &Releases{client: newReleaseClient(transport)}
}

func newReleaseClient(transport http.RoundTripper) *http.Client {
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = 10 * time.Second
		t.MaxIdleConns = 2
		t.MaxIdleConnsPerHost = 2
		t.IdleConnTimeout = 30 * time.Second
		transport = t
	}
	return &http.Client{Transport: transport, Timeout: checkTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (r *Releases) Close() { r.client.CloseIdleConnections() }

type release struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func (value release) version() string {
	if value.Draft || value.Prerelease || !strings.HasPrefix(value.Tag, "rust-v") {
		return ""
	}
	version, err := normalize(strings.TrimPrefix(value.Tag, "rust-v"))
	if err != nil {
		return ""
	}
	return version
}

type rateLimit struct{ retry time.Duration }

func (*rateLimit) Error() string { return ErrSync.Error() }
func (r *Releases) request(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, ErrSync
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "SubLane")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	response, err := r.client.Do(req)
	if err != nil {
		return nil, ErrSync
	}
	if err := releaseResponseError(response); err != nil {
		return nil, err
	}
	return response, nil
}

func releaseResponseError(response *http.Response) error {
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusForbidden {
			delay := 15 * time.Minute
			if seconds, err := strconv.ParseInt(response.Header.Get("Retry-After"), 10, 64); err == nil && seconds > 0 {
				delay = time.Duration(min(seconds, int64(syncInterval.Seconds()))) * time.Second
			}
			if response.Header.Get("X-RateLimit-Remaining") == "0" {
				if reset, err := strconv.ParseInt(response.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
					delay = max(delay, min(syncInterval, time.Until(time.Unix(reset, 0))))
				}
			}
			return &rateLimit{retry: delay}
		}
		return ErrSync
	}
	return nil
}
func (r *Releases) Latest(ctx context.Context) (string, error) {
	response, err := r.request(ctx, releasesURL+"/latest")
	if err != nil {
		return "", err
	}
	limited := &io.LimitedReader{R: response.Body, N: (2 << 20) + 1}
	decoder := json.NewDecoder(limited)
	var value release
	err = decoder.Decode(&value)
	if err == nil {
		err = expectEOF(decoder)
	}
	response.Body.Close()
	if err != nil || limited.N <= 0 {
		return "", ErrSync
	}
	if version := value.version(); version != "" {
		return version, nil
	}
	// The latest release can belong to another component. Scan one bounded page, decoding one item at a time.
	response, err = r.request(ctx, releasesURL+"?per_page=30")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	limited = &io.LimitedReader{R: response.Body, N: (16 << 20) + 1}
	decoder = json.NewDecoder(limited)
	token, err := decoder.Token()
	if err != nil || token != json.Delim('[') {
		return "", ErrSync
	}
	best := ""
	count := 0
	for decoder.More() {
		if count >= 30 {
			return "", ErrSync
		}
		count++
		var item release
		if decoder.Decode(&item) != nil {
			return "", ErrSync
		}
		version := item.version()
		if version != "" && (best == "" || compare(version, best) > 0) {
			best = version
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim(']') || expectEOF(decoder) != nil || limited.N <= 0 || best == "" {
		return "", ErrSync
	}
	return best, nil
}
func expectEOF(decoder *json.Decoder) error {
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return ErrSync
	}
	return nil
}
