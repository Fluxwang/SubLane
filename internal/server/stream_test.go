package server

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/murongg/SubLane/internal/gateway"
)

func TestStreamKeepaliveBeforeFirstEventAndIdleTimeout(t *testing.T) {
	policy := gateway.DefaultPolicy()
	policy.KeepaliveInterval = 10 * time.Millisecond
	policy.StreamIdleTimeout = 80 * time.Millisecond
	f := newConfiguredForwardFixture(t, "codex", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, false, policy)
	req, _ := http.NewRequest("POST", f.server.URL+"/v1/responses", strings.NewReader(`{"model":"synthetic-model","input":"synthetic","stream":true}`))
	req.Header.Set("Authorization", "Bearer "+f.secret)
	req.Header.Set("Content-Type", "application/json")
	f.server.Client().Timeout = time.Second
	started := time.Now()
	response, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, ":") || time.Since(started) > time.Second {
		t.Fatalf("missing prompt keepalive: %q %v", line, err)
	}
	body, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(body), "gateway_timeout") {
		t.Fatalf("heartbeats hid upstream idle timeout: %s %v", body, err)
	}
}

func TestStreamCancellationStillDrainsFinalUsage(t *testing.T) {
	finish := make(chan struct{})
	f := newForwardFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"synthetic\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"output\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n")
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", f.server.URL+"/v1/responses", strings.NewReader(`{"model":"synthetic-model","input":"synthetic","stream":true}`))
	req.Header.Set("Authorization", "Bearer "+f.secret)
	req.Header.Set("Content-Type", "application/json")
	response, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(response.Body).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()
	response.Body.Close()
	time.Sleep(50 * time.Millisecond)
	close(finish)
	deadline := time.Now().Add(time.Second)
	for {
		page, err := f.forwarding.Requests(context.Background(), gateway.RequestFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Requests) > 0 {
			if page.Requests[0].InputTokens == nil || *page.Requests[0].InputTokens != 7 {
				t.Fatalf("cancellation lost final usage: %+v", page.Requests[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("canceled request did not settle")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHeartbeatDisconnectPreservesFinalUsage(t *testing.T) {
	finish := make(chan struct{})
	f := newForwardFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"synthetic\"}\n\n")
		w.(http.Flusher).Flush()
		<-finish
		time.Sleep(20 * time.Millisecond)
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"output\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n")
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	x, err := f.forwarding.Open(ctx, 1, 1, []byte(`{"model":"synthetic-model","input":"synthetic"}`), nil, gateway.Responses)
	if err != nil {
		t.Fatal(err)
	}
	consumeEvents(ctx, x, 0, time.Millisecond, func([]byte) error { return nil }, func() error { cancel(); close(finish); return io.ErrClosedPipe })
	x.Body.Close()
	page, err := f.forwarding.Requests(context.Background(), gateway.RequestFilter{})
	if err != nil || len(page.Requests) != 1 || page.Requests[0].InputTokens == nil || *page.Requests[0].InputTokens != 7 {
		t.Fatalf("heartbeat disconnect lost final usage: %+v %v", page, err)
	}
}
