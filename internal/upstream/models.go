package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
	core "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// CatalogSource invalidates cached discovery when its request contract changes.
// Bump the adapter revision if a provider's discovery protocol or normalization changes.
func CatalogSource(provider string) string {
	return catalogSource(provider, DefaultCodexVersion)
}

func (c *Client) CatalogSource(provider string) string {
	return catalogSource(provider, c.codexVersion())
}

func catalogSource(provider, version string) string {
	source := provider + ":v1"
	if provider == "codex" {
		source += ":" + version
	}
	return source
}

// Thinking suffixes are SDK request options; capability discovery reports the base model.
// Group permission checks still use the complete client-requested ID.
func CatalogModelID(model string) string {
	if index := strings.LastIndex(model, "("); index > 0 && strings.HasSuffix(model, ")") {
		return model[:index]
	}
	return model
}

func (c *Client) providerModels(ctx context.Context, credential accounts.Credential) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	endpoint, method := "https://api.anthropic.com/v1/models?limit=1000", "GET"
	if credential.Kind() == "antigravity" {
		endpoint, method = "https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels", "POST"
	}
	if credential.Kind() == "xai" {
		endpoint = "https://api.x.ai/v1/models"
	}
	if credential.Kind() == "openai" {
		endpoint = credential.BaseURL + "/models"
	}
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(`{}`)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, ErrInput
	}
	req.Header.Set("Content-Type", "application/json")
	if credential.Kind() == "claude" {
		req.Header.Set("Anthropic-Version", "2023-06-01")
		req.Header.Set("User-Agent", claudeUserAgent(c.currentClaudeVersion()))
	}
	executor, err := c.executor(credential.Kind())
	if err != nil {
		return nil, err
	}
	response, err := executor.HttpRequest(c.sdkContext(ctx, credential), sdkAuth(credential), req)
	if err != nil {
		return nil, sdkError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, &UpstreamError{Status: response.StatusCode, RetryAfter: response.Header.Get("Retry-After")}
	}
	raw, err := readBounded(response.Body, 2<<20)
	if err != nil {
		return nil, err
	}
	var value struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models map[string]json.RawMessage `json:"models"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return nil, ErrResponse
	}
	result := []Model{}
	if credential.Kind() != "antigravity" {
		if value.Data == nil {
			return nil, ErrResponse
		}
		for _, m := range value.Data {
			if m.ID != "" && len(m.ID) <= 128 {
				result = append(result, Model{ID: m.ID, Object: "model", OwnedBy: credential.Kind()})
			}
		}
	} else {
		if value.Models == nil {
			return nil, ErrResponse
		}
		for id := range value.Models {
			if id != "" && len(id) <= 128 {
				result = append(result, Model{ID: id, Object: "model", OwnedBy: "antigravity"})
			}
		}
	}
	if len(result) > 512 {
		return nil, ErrResponse
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
func (c *Client) prepareProject(ctx context.Context, credential accounts.Credential) (accounts.Credential, error) {
	executor, err := c.executor(credential.Kind())
	if err != nil {
		return accounts.Credential{}, err
	}
	preparer, ok := executor.(core.RequestAuthPreparer)
	auth := sdkAuth(credential)
	if !ok || !preparer.ShouldPrepareRequestAuth(auth) {
		return credential, nil
	}
	updated, err := preparer.PrepareRequestAuth(c.sdkContext(ctx, credential), auth)
	if err != nil {
		return accounts.Credential{}, sdkError(err)
	}
	if updated == nil {
		return credential, nil
	}
	fields := map[string]json.RawMessage{}
	data, _ := json.Marshal(credential)
	json.Unmarshal(data, &fields)
	delete(fields, "metadata")
	for key, value := range credential.Metadata {
		fields[key] = value
	}
	for _, key := range []string{"project_id"} {
		if value, ok := updated.Metadata[key]; ok {
			fields[key], _ = json.Marshal(value)
		}
	}
	fields["type"], _ = json.Marshal(credential.Kind())
	data, _ = json.Marshal(fields)
	return accounts.ParseFor(credential.Kind(), data)
}
