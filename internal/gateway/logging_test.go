package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/murongg/SubLane/internal/logging"
	"github.com/murongg/SubLane/internal/upstream"
)

func gatewayLogRecords(t *testing.T, buffer *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(buffer.Bytes()))
	for {
		var record map[string]any
		if err := decoder.Decode(&record); err == io.EOF {
			return records
		} else if err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
}

func TestRetryLogsSafeAttemptMetadata(t *testing.T) {
	var attempts atomic.Int32
	s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"synthetic-upstream-secret"}`))}, nil
		}
		return syntheticStream(), nil
	}))
	var buffer bytes.Buffer
	ctx := logging.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelWarn})))
	x, err := s.Open(ctx, 1, 1, []byte(`{"model":"synthetic-model","input":"synthetic-request-secret"}`), nil, Responses)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Events(func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	x.Body.Close()
	logs := gatewayLogRecords(t, &buffer)
	if len(logs) != 1 || logs[0]["msg"] != "Gateway upstream retry" || logs[0]["upstream_status"] != float64(503) || logs[0]["attempt"] != float64(1) || logs[0]["account_id"] != ids["codex"] {
		t.Fatalf("retry lost safe attempt diagnostics: %+v", logs)
	}
	page, err := s.Requests(context.Background(), RequestFilter{})
	if err != nil || len(page.Requests) != 1 || logs[0]["request_id"] != page.Requests[0].RequestID {
		t.Fatal("retry correlation was lost", err)
	}
	if strings.Contains(buffer.String(), "synthetic-upstream-secret") || strings.Contains(buffer.String(), "synthetic-request-secret") {
		t.Fatal("retry logged private content")
	}
}

func TestGatewayLogsOutcomesWithSafeCorrelation(t *testing.T) {
	for _, tc := range []struct {
		name, transport, outcome, code, level string
		failure                               error
	}{
		{"HTTP success", "http", "success", "", "DEBUG", nil},
		{"WebSocket success", "websocket", "success", "", "INFO", nil},
		{"failure", "http", "error", "request_failed", "ERROR", errors.New("synthetic-error-secret")},
		{"rate limit", "http", "error", "rate_limited", "WARN", &upstream.UpstreamError{Status: 429, RetryAfter: "synthetic-header-secret"}},
		{"policy rejection", "http", "rejected", "model_not_allowed", "WARN", ErrModelNotAllowed},
		{"disconnect", "http", "canceled", "client_disconnected", "DEBUG", context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("observation logging must not contact upstream")
				return nil, nil
			}))
			var buffer bytes.Buffer
			ctx := logging.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
			ctx = WithRequestIdentity(ctx, 42, tc.transport)
			e, err := s.begin(ctx, 1, 1, Responses)
			if err != nil {
				t.Fatal(err)
			}
			e.record.AccountID, e.record.Provider, e.record.Model = ids["codex"], "codex", "synthetic-model"
			if tc.failure != nil {
				e.fail(tc.failure)
			} else {
				e.finish("success", "", "", "")
			}
			e.finish("error", "request_failed", "", "")
			logs := gatewayLogRecords(t, &buffer)
			if len(logs) != 1 {
				t.Fatalf("expected one completion despite repeated settlement, got %d", len(logs))
			}
			log := logs[0]
			if log["msg"] != "Gateway request completed" || log["request_id"] != RequestID(ctx) || log["outcome"] != tc.outcome || log["level"] != tc.level || log["transport"] != tc.transport || log["error_code"] != tc.code {
				t.Fatal("gateway diagnostics lost correlation or outcome", log)
			}
			if log["tenant_id"] != float64(1) || log["user_id"] != float64(1) || log["key_id"] != float64(42) || log["group_id"] != float64(1) || log["account_id"] != ids["codex"] || log["model"] != "synthetic-model" {
				t.Fatal("gateway diagnostics omitted the safe actor context", log)
			}
			if tc.name == "rate limit" && log["upstream_status"] != float64(429) {
				t.Fatal("upstream status was omitted", log)
			}
			if strings.Contains(buffer.String(), "synthetic-error-secret") || strings.Contains(buffer.String(), "synthetic-header-secret") {
				t.Fatal("gateway diagnostics exposed raw errors or headers")
			}
		})
	}
}

type gatewayLogProbe struct {
	bytes.Buffer
	service *Service
	test    *testing.T
}

func (p *gatewayLogProbe) Write(data []byte) (int, error) {
	if !p.service.mu.TryLock() {
		p.test.Error("log output held the gateway admission lock")
	} else {
		p.service.mu.Unlock()
	}
	return p.Buffer.Write(data)
}

func TestGatewayLogsPersistenceFailureOutsideAdmissionLock(t *testing.T) {
	s, ids := codexGateway(t, transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("observation logging must not contact upstream")
		return nil, nil
	}))
	probe := &gatewayLogProbe{service: s, test: t}
	ctx := logging.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(probe, nil)))
	ctx = WithRequestIdentity(ctx, 42, "http")
	e, err := s.begin(ctx, 1, 1, Responses)
	if err != nil {
		t.Fatal(err)
	}
	e.record.AccountID, e.record.Provider, e.record.Model = ids["codex"], "codex", "synthetic-model"
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	e.finish("success", "", "", "")
	logs := gatewayLogRecords(t, &probe.Buffer)
	if len(logs) != 1 || logs[0]["level"] != "ERROR" || logs[0]["error_code"] != "persist_request_failed" || logs[0]["request_id"] != RequestID(ctx) || logs[0]["account_id"] != ids["codex"] {
		t.Fatal("persistence failure lacks request context", logs)
	}
}
