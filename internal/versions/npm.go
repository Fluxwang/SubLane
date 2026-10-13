package versions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

type npmReleases struct {
	client   *http.Client
	url, tag string
}

func newNPMReleases(transport http.RoundTripper, url, tag string) *npmReleases {
	return &npmReleases{client: newReleaseClient(transport), url: url, tag: tag}
}

func (r *npmReleases) Close() { r.client.CloseIdleConnections() }

func (r *npmReleases) Latest(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
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
	var tags map[string]string
	if decoder.Decode(&tags) != nil || expectEOF(decoder) != nil || limited.N <= 0 {
		return "", ErrSync
	}
	// Each publisher defines its own stable channel; never substitute latest for a missing stable tag.
	version, err := normalize(tags[r.tag])
	if err != nil || version == "" {
		return "", ErrSync
	}
	return version, nil
}
