package gateway

import (
	"encoding/json"
	"errors"

	"github.com/murongg/SubLane/internal/upstream"
)

var ErrContextLimit = errors.New("conversation_context_limit")

// Conversation retains bounded, connection-local state for translating incremental WebSocket turns to stateless HTTP.
type Conversation struct {
	MaxRequestBody int64
	request        map[string]json.RawMessage
	output         []json.RawMessage
	responseID     string
	overflow       bool
}

func (c *Conversation) bodyLimit() int64 {
	if c.MaxRequestBody > 0 {
		return c.MaxRequestBody
	}
	return upstream.DefaultMaxRequestBody
}

func (c *Conversation) Normalize(raw []byte) ([]byte, bool, error) {
	if int64(len(raw)) > c.bodyLimit() {
		return nil, false, ErrContextLimit
	}
	var input map[string]json.RawMessage
	if json.Unmarshal(raw, &input) != nil || input == nil {
		return nil, false, upstream.ErrInput
	}
	var kind, previous string
	if json.Unmarshal(input["type"], &kind) != nil || kind != "response.create" && kind != "response.append" {
		return nil, false, upstream.ErrInput
	}
	if kind == "response.append" && c.request == nil {
		return nil, false, upstream.ErrContinuation
	}
	if value := input["previous_response_id"]; len(value) > 0 && string(value) != "null" {
		if json.Unmarshal(value, &previous) != nil {
			return nil, false, upstream.ErrInput
		}
	}
	items, err := conversationInput(input["input"])
	if err != nil {
		return nil, false, err
	}
	full := fullTranscript(items)
	if previous != "" && previous != c.responseID && !full {
		return nil, false, upstream.ErrContinuation
	}
	appendHistory := c.request != nil && !full
	if c.overflow && appendHistory {
		return nil, false, ErrContextLimit
	}
	if appendHistory {
		prior, err := conversationInput(c.request["input"])
		if err != nil {
			return nil, false, err
		}
		merged := make([]json.RawMessage, 0, len(prior)+len(c.output)+len(items))
		merged = append(merged, prior...)
		merged = append(merged, c.output...)
		merged = append(merged, items...)
		items = merged
	}
	for _, key := range []string{"model", "instructions"} {
		if _, ok := input[key]; !ok && c.request != nil {
			if value, ok := c.request[key]; ok {
				input[key] = value
			}
		}
	}
	var model string
	if json.Unmarshal(input["model"], &model) != nil || model == "" {
		return nil, false, upstream.ErrInput
	}
	if c.request != nil && (appendHistory || previous != "" || kind == "response.append") {
		var priorModel string
		_ = json.Unmarshal(c.request["model"], &priorModel)
		// Normalization removes continuation IDs and fills history. Do not let that
		// turn an incremental model change into permission to rebind an account.
		if upstream.CatalogModelID(model) != upstream.CatalogModelID(priorModel) {
			return nil, false, ErrAffinityUnavailable
		}
	}
	prewarm := false
	if flag, ok := input["generate"]; ok {
		var generate bool
		if json.Unmarshal(flag, &generate) != nil {
			return nil, false, upstream.ErrInput
		}
		prewarm = !generate
	}
	delete(input, "type")
	delete(input, "previous_response_id")
	delete(input, "generate")
	input["stream"] = json.RawMessage("true")
	input["input"], _ = json.Marshal(items)
	normalized, err := json.Marshal(input)
	if err != nil {
		return nil, false, upstream.ErrInput
	}
	if int64(len(normalized)) > c.bodyLimit() {
		return nil, false, ErrContextLimit
	}
	return normalized, prewarm, nil
}

func (c *Conversation) Accept(request, event []byte) error {
	var response struct {
		Response struct {
			ID     string            `json:"id"`
			Output []json.RawMessage `json:"output"`
		} `json:"response"`
	}
	if json.Unmarshal(event, &response) != nil || response.Response.ID == "" {
		return upstream.ErrResponse
	}
	var input map[string]json.RawMessage
	if json.Unmarshal(request, &input) != nil {
		return upstream.ErrInput
	}
	size := int64(len(request))
	for _, item := range response.Response.Output {
		size += int64(len(item))
	}
	c.responseID = response.Response.ID
	c.overflow = size > c.bodyLimit()
	if c.overflow {
		delete(input, "input")
		c.output = nil
	} else {
		c.output = response.Response.Output
	}
	c.request = input
	return nil
}

func conversationInput(raw []byte) ([]json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []json.RawMessage{}, nil
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil && list != nil {
		return list, nil
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return nil, upstream.ErrInput
	}
	message, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": text}}})
	return []json.RawMessage{message}, nil
}

func fullTranscript(items []json.RawMessage) bool {
	for _, raw := range items {
		var item struct {
			Type string `json:"type"`
			Role string `json:"role"`
		}
		if json.Unmarshal(raw, &item) == nil && (item.Type == "compaction" || item.Type == "reasoning" || item.Type == "function_call" || item.Role == "assistant") {
			return true
		}
	}
	return false
}
