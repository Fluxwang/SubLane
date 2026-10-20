package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"

	"github.com/murongg/SubLane/internal/logging"
)

type diagnosticStatusError struct {
	status int
	body   string
}

func (e diagnosticStatusError) Error() string   { return e.body }
func (e diagnosticStatusError) StatusCode() int { return e.status }

func TestSDKFailureDiagnosticsRedactUntrustedFields(t *testing.T) {
	for _, tc := range []struct {
		name, body, code, kind string
	}{
		{"known labels", `{"error":{"type":"api_error","code":"server_error","message":"sk-synthetic-secret synthetic-private-prompt","param":"synthetic-private-param"}}`, "server_error", "api_error"},
		{"unknown labels", `{"error":{"type":"synthetic-private-type","code":"synthetic-private-code","message":"synthetic-private-message"}}`, "unknown", "unknown"},
		{"response envelope", `{"response":{"error":{"type":"server_error","code":"server_error","message":"synthetic-private-message"}}}`, "server_error", "server_error"},
		{"non-JSON", `<html>synthetic-private-message https://example.test/?token=synthetic-token</html>`, "", ""},
		{"oversized", strings.Repeat("synthetic-private-message", 1024), "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buffer bytes.Buffer
			ctx := logging.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buffer, nil)).With("request_id", "req_synthetic"))
			transport := &engineTransport{lastStatus: 200}
			logSDKFailure(ctx, "stream", diagnosticStatusError{502, tc.body}, transport)
			var record map[string]any
			if json.Unmarshal(buffer.Bytes(), &record) != nil || record["upstream_status"] != float64(200) || record["upstream_error_status"] != float64(502) || record["request_id"] != "req_synthetic" {
				t.Fatal("diagnostic omitted safe metadata", buffer.String())
			}
			if tc.code != "" && record["upstream_error_code"] != tc.code || tc.kind != "" && record["upstream_error_type"] != tc.kind {
				t.Fatal("safe error labels lost", record)
			}
			for _, secret := range []string{"synthetic-private", "sk-synthetic", "example.test", "synthetic-token"} {
				if strings.Contains(buffer.String(), secret) {
					t.Fatalf("diagnostic exposed %s", secret)
				}
			}
		})
	}
}

func TestSDKFailureDiagnosticsClassifyErrorsWithoutTheirText(t *testing.T) {
	for _, tc := range []struct {
		err  error
		kind string
	}{
		{context.Canceled, "canceled"},
		{context.DeadlineExceeded, "timeout"},
		{&net.DNSError{Name: "synthetic-private-host", Err: "synthetic-private-dns"}, "dns_error"},
		{&url.Error{Op: "Post", URL: "https://user:pass@example.test/?token=synthetic-token", Err: syscall.ECONNRESET}, "connection_reset"},
		{syscall.ECONNREFUSED, "connection_refused"},
		{io.ErrUnexpectedEOF, "unexpected_eof"},
		{ErrInterrupted, "stream_interrupted"},
		{ErrResponse, "invalid_response"},
		{errors.New("synthetic-private-error"), "sdk_error"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			var buffer bytes.Buffer
			ctx := logging.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
			logSDKFailure(ctx, "execute", tc.err, &engineTransport{})
			var record map[string]any
			if json.Unmarshal(buffer.Bytes(), &record) != nil || record["upstream_error_kind"] != tc.kind {
				t.Fatal("incorrect failure classification", buffer.String())
			}
			if record["upstream_status"] != nil || strings.Contains(buffer.String(), "synthetic-private") || strings.Contains(buffer.String(), "example.test") {
				t.Fatal("failed transport leaked content or invented an HTTP status", buffer.String())
			}
		})
	}
}

func TestDiagnosticRequestIDsAreBounded(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"req_synthetic-upstream", true},
		{"00000000-0000-4000-8000-000000000001", true},
		{"sk-synthetic-secret", false},
		{"https://user:pass@example.test/?token=synthetic-token", false},
		{"req_synthetic\nprivate", false},
		{"req_" + strings.Repeat("x", 200), false},
	} {
		t.Run(tc.id[:min(len(tc.id), 50)], func(t *testing.T) {
			transport := &engineTransport{base: usageTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {tc.id}}, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			request, err := http.NewRequest(http.MethodPost, "https://example.test", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			var buffer bytes.Buffer
			ctx := logging.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buffer, nil)))
			logSDKFailure(ctx, "stream", ErrInterrupted, transport)
			var record map[string]any
			if json.Unmarshal(buffer.Bytes(), &record) != nil {
				t.Fatal("invalid diagnostic JSON")
			}
			if tc.want && record["upstream_request_id"] != tc.id || !tc.want && record["upstream_request_id"] != nil {
				t.Fatal("unsafe or missing upstream request ID", record)
			}
		})
	}
}
