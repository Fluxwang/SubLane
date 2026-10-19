package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
)

func (c *Client) claudeUsage(ctx context.Context, credential accounts.Credential) (Usage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/api/oauth/usage", nil)
	if err != nil {
		return Usage{}, ErrInput
	}
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
	request.Header.Set("User-Agent", claudeUserAgent(c.currentClaudeVersion()))
	request.Header.Set("Accept", "application/json")
	raw, err := c.subscriptionUsageResponse(request)
	if err != nil {
		return Usage{}, err
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(raw, &payload) != nil || payload == nil || len(payload) > 128 {
		return Usage{}, ErrResponse
	}
	result := Usage{Limits: []UsageLimit{}, UpdatedAt: time.Now().Unix()}
	main := UsageLimit{Windows: []UsageWindow{}}
	keys := []string{}
	for key := range payload {
		if key == "five_hour" || key == "seven_day" || strings.HasPrefix(key, "seven_day_") {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return Usage{}, ErrResponse
	}
	sort.Strings(keys)
	for _, key := range keys {
		var details *struct {
			Utilization *float64        `json:"utilization"`
			ResetsAt    json.RawMessage `json:"resets_at"`
		}
		if len(key) > 128 || json.Unmarshal(payload[key], &details) != nil {
			return Usage{}, ErrResponse
		}
		if details == nil {
			continue
		}
		if details.Utilization != nil && *details.Utilization < 0 {
			return Usage{}, ErrResponse
		}
		reset, err := subscriptionResetAt(details.ResetsAt)
		if err != nil {
			return Usage{}, err
		}
		seconds, kind := int64(7*24*3600), "secondary"
		if key == "five_hour" {
			seconds, kind = 5*3600, "primary"
		}
		window := UsageWindow{Kind: kind, UsedPercent: details.Utilization, WindowSeconds: &seconds, ResetAt: reset}
		if key == "five_hour" || key == "seven_day" {
			main.Windows = append(main.Windows, window)
		} else {
			result.Limits = append(result.Limits, UsageLimit{Name: strings.TrimPrefix(key, "seven_day_"), Windows: []UsageWindow{window}})
		}
	}
	if len(main.Windows) > 0 {
		result.Limits = append([]UsageLimit{main}, result.Limits...)
	}
	return result, nil
}

func (c *Client) antigravityUsage(ctx context.Context, credential accounts.Credential) (Usage, error) {
	input := map[string]string{}
	if value, ok := credential.Metadata["project_id"]; ok {
		var project string
		if json.Unmarshal(value, &project) != nil || len(project) > 256 {
			return Usage{}, ErrInput
		}
		if project != "" {
			input["project"] = project
		}
	}
	body, _ := json.Marshal(input)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels", bytes.NewReader(body))
	if err != nil {
		return Usage{}, ErrInput
	}
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", sdkauth.AntigravityUserAgent())
	raw, err := c.subscriptionUsageResponse(request)
	if err != nil {
		return Usage{}, err
	}
	var payload struct {
		Models map[string]struct {
			Internal bool `json:"isInternal"`
			Quota    *struct {
				Remaining *float64        `json:"remainingFraction"`
				ResetTime json.RawMessage `json:"resetTime"`
			} `json:"quotaInfo"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &payload) != nil || payload.Models == nil || len(payload.Models) > 512 {
		return Usage{}, ErrResponse
	}
	ids := make([]string, 0, len(payload.Models))
	for id, model := range payload.Models {
		if !model.Internal {
			if id == "" || len(id) > 128 {
				return Usage{}, ErrResponse
			}
			ids = append(ids, id)
		}
	}
	if len(ids) > 128 {
		return Usage{}, ErrResponse
	}
	sort.Strings(ids)
	result := Usage{Limits: []UsageLimit{}, UpdatedAt: time.Now().Unix()}
	for _, id := range ids {
		window := UsageWindow{Kind: "model"}
		if quota := payload.Models[id].Quota; quota != nil {
			if quota.Remaining != nil {
				if *quota.Remaining < 0 || *quota.Remaining > 1 {
					return Usage{}, ErrResponse
				}
				used := (1 - *quota.Remaining) * 100
				window.UsedPercent = &used
			}
			window.ResetAt, err = subscriptionResetAt(quota.ResetTime)
			if err != nil {
				return Usage{}, err
			}
		}
		// Model quotas describe this model only; they must not become an account-wide rejection.
		result.Limits = append(result.Limits, UsageLimit{Name: id, Windows: []UsageWindow{window}})
	}
	return result, nil
}

// Read metadata only: generation probes consume quota and do not measure remaining allowance.
func (c *Client) subscriptionUsageResponse(request *http.Request) ([]byte, error) {
	response, err := c.http.Do(request)
	if err != nil {
		if request.Context().Err() != nil {
			return nil, request.Context().Err()
		}
		return nil, ErrUpstream
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, &UpstreamError{Status: response.StatusCode, RetryAfter: response.Header.Get("Retry-After")}
	}
	raw, err := readBounded(response.Body, 2<<20)
	if err != nil {
		return nil, ErrResponse
	}
	return raw, nil
}

func subscriptionResetAt(raw json.RawMessage) (*int64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	value := string(raw)
	if raw[0] == '"' {
		if json.Unmarshal(raw, &value) != nil {
			return nil, ErrResponse
		}
		if value == "" {
			return nil, nil
		}
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			reset := parsed.Unix()
			if reset < 0 {
				return nil, ErrResponse
			}
			return &reset, nil
		}
	}
	reset, err := strconv.ParseInt(value, 10, 64)
	if err != nil || reset < 0 {
		return nil, ErrResponse
	}
	if reset >= 1_000_000_000_000 {
		reset /= 1000
	}
	if reset > 253402300799 {
		return nil, ErrResponse
	}
	return &reset, nil
}
