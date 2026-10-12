package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/murongg/SubLane/internal/accounts"
)

// Billing and inference use the same instance-wide Grok client version policy.
func (c *Client) grokUsage(ctx context.Context, credential accounts.Credential) (Usage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://cli-chat-proxy.grok.com/v1/billing?format=credits", nil)
	if err != nil {
		return Usage{}, ErrInput
	}
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-XAI-Token-Auth", "xai-grok-cli")
	request.Header.Set("x-grok-client-mode", "cli")
	applyGrokVersion(request.Header, c.currentGrokVersion())
	request.Header.Set("x-userid", credential.AccountID)
	raw, err := c.subscriptionUsageResponse(request)
	if err != nil {
		return Usage{}, err
	}
	var payload struct {
		Config json.RawMessage `json:"config"`
	}
	if json.Unmarshal(raw, &payload) != nil || len(payload.Config) == 0 {
		return Usage{}, ErrResponse
	}
	type cents struct {
		Val int64 `json:"val"`
	}
	var config *struct {
		Percent *float64 `json:"creditUsagePercent"`
		Period  *struct {
			Type  string          `json:"type"`
			Start json.RawMessage `json:"start"`
			End   json.RawMessage `json:"end"`
		} `json:"currentPeriod"`
		Limit *cents          `json:"monthlyLimit"`
		Used  *cents          `json:"used"`
		Start json.RawMessage `json:"billingPeriodStart"`
		End   json.RawMessage `json:"billingPeriodEnd"`
	}
	if json.Unmarshal(payload.Config, &config) != nil {
		return Usage{}, ErrResponse
	}
	result := Usage{Limits: []UsageLimit{}, UpdatedAt: time.Now().Unix()}
	if config == nil {
		return result, nil
	}
	window := UsageWindow{Kind: "primary", UsedPercent: config.Percent}
	startRaw, endRaw := config.Start, config.End
	if config.Period != nil {
		startRaw, endRaw = config.Period.Start, config.Period.End
		if config.Period.Type == "USAGE_PERIOD_TYPE_WEEKLY" {
			seconds := int64(7 * 86400)
			window.WindowSeconds = &seconds
		}
	} else if window.UsedPercent == nil {
		// Only legacy included credits define this ratio. On-demand, prepaid and product
		// breakdowns are different counters; none can fill a missing subscription percent.
		if config.Limit != nil && config.Limit.Val < 0 || config.Used != nil && config.Used.Val < 0 {
			return Usage{}, ErrResponse
		}
		if config.Limit != nil && config.Limit.Val > 0 && config.Used != nil {
			used := float64(config.Used.Val) / float64(config.Limit.Val) * 100
			window.UsedPercent = &used
		}
	}
	if window.UsedPercent != nil && *window.UsedPercent < 0 {
		return Usage{}, ErrResponse
	}
	start, err := subscriptionResetAt(startRaw)
	if err != nil {
		return Usage{}, err
	}
	window.ResetAt, err = subscriptionResetAt(endRaw)
	if err != nil {
		return Usage{}, err
	}
	if start != nil && window.ResetAt != nil {
		seconds := *window.ResetAt - *start
		if seconds <= 0 || seconds > 366*86400 {
			return Usage{}, ErrResponse
		}
		window.WindowSeconds = &seconds
	}
	result.Limits = append(result.Limits, UsageLimit{Windows: []UsageWindow{window}})
	return result, nil
}
