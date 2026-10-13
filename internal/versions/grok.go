package versions

import "net/http"

const grokVersionsURL = "https://registry.npmjs.org/-/package/@xai-official/grok/dist-tags"

type GrokReleases struct{ *npmReleases }

func NewGrokReleases(transport http.RoundTripper) *GrokReleases {
	return &GrokReleases{newNPMReleases(transport, grokVersionsURL, "latest")}
}
