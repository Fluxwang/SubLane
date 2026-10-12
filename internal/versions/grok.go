package versions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

const grokVersionsURL = "https://registry.npmjs.org/-/package/@xai-official/grok/dist-tags"

type GrokReleases struct{ client *http.Client }

func NewGrokReleases(transport http.RoundTripper) *GrokReleases {
	return &GrokReleases{client: newReleaseClient(transport)}
}

func (r *GrokReleases) Close() { r.client.CloseIdleConnections() }

func (r *GrokReleases) Latest(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, grokVersionsURL, nil)
	if err != nil {
		return "", ErrSync
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "SubLane")
	response, err := r.client.Do(request)
	if err != nil {
		return "", ErrSync
	}
	if err := releaseResponseError(response); err != nil {
		return "", err
	}
	defer response.Body.Close()
	limited := &io.LimitedReader{R: response.Body, N: (64 << 10) + 1}
	decoder := json.NewDecoder(limited)
	var tags struct {
		Latest string `json:"latest"`
	}
	if decoder.Decode(&tags) != nil || expectEOF(decoder) != nil || limited.N <= 0 {
		return "", ErrSync
	}
	// Only npm's stable channel is eligible; a numerically newer alpha tag is not a stable release.
	version, err := normalize(tags.Latest)
	if err != nil || version == "" {
		return "", ErrSync
	}
	return version, nil
}
