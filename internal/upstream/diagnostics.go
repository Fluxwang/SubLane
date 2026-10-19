package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"regexp"
	"syscall"

	"github.com/murongg/SubLane/internal/logging"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

var diagnosticRequestID = regexp.MustCompile(`^(req_[a-zA-Z0-9_-]{1,124}|[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12})$`)

func logSDKFailure(ctx context.Context, stage string, err error, transport *engineTransport) {
	transport.mu.Lock()
	status, requestID, kind := transport.lastStatus, transport.lastRequestID, transport.failureKind
	transport.mu.Unlock()
	if kind == "" {
		kind = diagnosticErrorKind(err)
	}
	level := slog.LevelError
	if kind == "canceled" {
		level = slog.LevelDebug
	} else if status >= 400 && status < 500 {
		level = slog.LevelWarn
	}
	logger := logging.From(ctx)
	if !logger.Enabled(ctx, level) {
		return
	}
	attrs := []slog.Attr{slog.String("stage", stage), slog.String("upstream_error_kind", kind)}
	if status != 0 {
		attrs = append(attrs, slog.Int("upstream_status", status))
	}
	if requestID != "" {
		attrs = append(attrs, slog.String("upstream_request_id", requestID))
	}
	var rejected exec.StatusError
	if errors.As(err, &rejected) {
		// The SDK can map a failed HTTP 200 stream to a different error status.
		attrs = append(attrs, slog.Int("upstream_error_status", rejected.StatusCode()))
	}
	// Error messages can echo credentials, URLs and arbitrary prompt content. A
	// closed vocabulary is safer than attempting to redact every possible secret.
	for depth := 0; err != nil && depth < 8; depth++ {
		if fields := diagnosticErrorFields(err.Error()); len(fields) > 0 {
			attrs = append(attrs, fields...)
			break
		}
		err = errors.Unwrap(err)
	}
	logger.LogAttrs(ctx, level, "Upstream request failed", attrs...)
}

func diagnosticErrorKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection_reset"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected_eof"
	case errors.Is(err, ErrInterrupted):
		return "stream_interrupted"
	case errors.Is(err, ErrResponse):
		return "invalid_response"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "dns_error"
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return "timeout"
	}
	var rejected exec.StatusError
	if errors.As(err, &rejected) {
		return "http_error"
	}
	return "sdk_error"
}

func diagnosticErrorFields(raw string) []slog.Attr {
	if len(raw) > 16<<10 {
		return nil
	}
	type details struct {
		Code string `json:"code"`
		Type string `json:"type"`
	}
	var body struct {
		Error    *details `json:"error"`
		Response struct {
			Error *details `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(raw), &body) != nil {
		return nil
	}
	value := body.Error
	if value == nil {
		value = body.Response.Error
	}
	if value == nil {
		return nil
	}
	var attrs []slog.Attr
	if value.Code != "" {
		attrs = append(attrs, slog.String("upstream_error_code", diagnosticErrorLabel(value.Code)))
	}
	if value.Type != "" {
		attrs = append(attrs, slog.String("upstream_error_type", diagnosticErrorLabel(value.Type)))
	}
	return attrs
}

func diagnosticErrorLabel(value string) string {
	switch value {
	case "server_error", "internal_error", "internal_server_error", "api_error",
		"invalid_request_error", "bad_request_error", "invalid_request", "invalid_prompt",
		"context_length_exceeded", "context_window_exceeded",
		"rate_limit_error", "rate_limit_exceeded", "usage_limit_reached", "insufficient_quota",
		"model_not_found", "model_not_available", "model_not_supported",
		"model_at_capacity", "model_is_at_capacity", "server_is_overloaded",
		"overloaded_error", "service_unavailable_error", "authentication_error",
		"invalid_api_key", "auth_unavailable", "unauthorized", "permission_error",
		"permission_denied", "forbidden", "not_found_error", "not_found",
		"timeout_error", "request_timeout", "thinking_signature_invalid",
		"previous_response_not_found", "content_policy_violation", "cyber_policy", "bio_policy":
		return value
	default:
		return "unknown"
	}
}
