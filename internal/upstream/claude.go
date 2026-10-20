package upstream

import (
	"context"
	"encoding/json"
	"strings"

	core "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const DefaultClaudeVersion = "2.1.287"

func (c *Client) currentClaudeVersion() string {
	if c.claudeVersion != nil {
		if version := c.claudeVersion(); version != "" {
			return version
		}
	}
	return DefaultClaudeVersion
}

func claudeUserAgent(version string) string { return "claude-cli/" + version + " (external, cli)" }

func (c *Client) versionedClaudeExecutor(ctx context.Context, version string) (core.ProviderExecutor, error) {
	base, err := c.executor("claude")
	if err != nil {
		return nil, err
	}
	if version == DefaultClaudeVersion {
		return base, nil
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.closed {
		return nil, ErrUpstream
	}
	if c.runtime.claudeCachedVersion == version {
		return c.runtime.claudeCachedExecutor, nil
	}
	// Built-in executor constructors are exposed only through the public SDK bootstrap.
	// Retain one immutable executor snapshot, close its empty bootstrap immediately,
	// and let in-flight requests keep their own snapshot and request context.
	runtime, err := c.startEngine(ctx, version)
	if err != nil {
		return nil, err
	}
	executor, ok := runtime.manager.Executor("claude")
	runtime.cancel()
	<-runtime.done
	if !ok {
		return nil, ErrUpstream
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.runtime.claudeCachedVersion, c.runtime.claudeCachedExecutor = version, executor
	return executor, nil
}

func adaptiveClaudeModel(model string) bool {
	switch CatalogModelID(model) {
	case "claude-opus-5-5", "claude-sonnet-5-5":
		return true
	default:
		return false
	}
}

// Keep strict client validation here; SDK normalization can otherwise silently
// replace unsupported controls. The executor owns final model shaping and signing.
func (c *Client) prepareClaude(raw []byte, opts exec.Options) ([]byte, exec.Options, error) {
	var source map[string]json.RawMessage
	if json.Unmarshal(raw, &source) != nil {
		return nil, opts, ErrInput
	}
	var model string
	if json.Unmarshal(source["model"], &model) != nil {
		return nil, opts, ErrInput
	}
	if !adaptiveClaudeModel(model) {
		return raw, opts, nil
	}
	effort := ""
	var reasoning struct {
		Effort string `json:"effort"`
	}
	if opts.SourceFormat == translator.FormatClaude {
		if len(source["output_config"]) > 0 && json.Unmarshal(source["output_config"], &reasoning) != nil {
			return nil, opts, ErrInput
		}
	} else if len(source["reasoning"]) > 0 && json.Unmarshal(source["reasoning"], &reasoning) != nil {
		return nil, opts, ErrInput
	}
	effort = strings.ToLower(strings.TrimSpace(reasoning.Effort))
	if start := strings.LastIndex(model, "("); start > 0 && strings.HasSuffix(model, ")") {
		effort = strings.ToLower(strings.TrimSpace(model[start+1 : len(model)-1]))
		switch effort {
		case "none", "auto", "minimal", "low", "medium", "high", "xhigh", "max":
		default:
			return nil, opts, ErrInput
		}
	}
	body := raw
	if opts.SourceFormat != translator.FormatClaude {
		body = c.registry.TranslateRequest(opts.SourceFormat, translator.FormatClaude, model, raw, opts.Stream)
	}
	var native map[string]json.RawMessage
	if json.Unmarshal(body, &native) != nil || native == nil {
		return nil, opts, ErrInput
	}
	native["model"], _ = json.Marshal(CatalogModelID(model))
	var thinking map[string]json.RawMessage
	if len(native["thinking"]) > 0 && (json.Unmarshal(native["thinking"], &thinking) != nil || thinking == nil) {
		return nil, opts, ErrInput
	}
	if thinking == nil {
		thinking = map[string]json.RawMessage{}
	}
	var kind string
	if len(thinking["type"]) > 0 && json.Unmarshal(thinking["type"], &kind) != nil {
		return nil, opts, ErrInput
	}
	switch kind {
	case "":
		if len(thinking["type"]) != 0 {
			return nil, opts, ErrInput
		}
	case "enabled", "adaptive", "auto", "disabled", "between_tools":
	default:
		return nil, opts, ErrInput
	}
	sonnet := CatalogModelID(model) == "claude-sonnet-5-5"
	if opts.SourceFormat == translator.FormatClaude && len(thinking["budget_tokens"]) > 0 {
		return nil, opts, ErrInput
	}
	if opts.SourceFormat == translator.FormatClaude && kind == "disabled" && !sonnet {
		return nil, opts, ErrInput
	}
	if kind == "between_tools" && !sonnet {
		return nil, opts, ErrInput
	}
	between := sonnet && (kind == "between_tools" || kind == "disabled" || effort == "none")
	if between && (effort == "xhigh" || effort == "max") {
		return nil, opts, ErrInput
	}
	switch effort {
	case "":
	case "auto":
		effort = ""
	case "none", "minimal":
		effort = "low"
	case "low", "medium", "high", "xhigh", "max":
	default:
		return nil, opts, ErrInput
	}
	var choice struct {
		Type string `json:"type"`
	}
	if len(native["tool_choice"]) > 0 && json.Unmarshal(native["tool_choice"], &choice) != nil {
		return nil, opts, ErrInput
	}
	if choice.Type == "any" || choice.Type == "tool" {
		return nil, opts, ErrInput
	}
	if between {
		thinking = map[string]json.RawMessage{"type": json.RawMessage(`"between_tools"`)}
	} else {
		thinking["type"] = json.RawMessage(`"adaptive"`)
		delete(thinking, "budget_tokens")
	}
	native["thinking"], _ = json.Marshal(thinking)
	if effort != "" || len(native["output_config"]) > 0 {
		var output map[string]json.RawMessage
		if len(native["output_config"]) > 0 && (json.Unmarshal(native["output_config"], &output) != nil || output == nil) {
			return nil, opts, ErrInput
		}
		if output == nil {
			output = map[string]json.RawMessage{}
		}
		if effort != "" {
			output["effort"], _ = json.Marshal(effort)
		} else {
			delete(output, "effort")
		}
		native["output_config"], _ = json.Marshal(output)
	}
	encoded, err := json.Marshal(native)
	if err != nil || int64(len(encoded)) > c.MaxRequestBody() {
		return nil, opts, ErrInput
	}
	opts.SourceFormat = translator.FormatClaude
	return encoded, opts, nil
}
