package versions

import "net/http"

const claudeVersionsURL = "https://registry.npmjs.org/-/package/@anthropic-ai/claude-code/dist-tags"

type ClaudeReleases struct{ *npmReleases }

func NewClaudeReleases(transport http.RoundTripper) *ClaudeReleases {
	return &ClaudeReleases{newNPMReleases(transport, claudeVersionsURL, "stable")}
}
