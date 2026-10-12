package server

import (
	"context"
	"time"

	"github.com/murongg/SubLane/internal/gateway"
)

type streamEvent struct {
	data  []byte
	reply chan error
}

// Event reads happen separately so idle upstreams cannot prevent downstream
// keepalive. Only this loop invokes writers; heartbeats never reset upstream idle.
func consumeEvents(ctx context.Context, result *gateway.Exchange, idle, keepalive time.Duration, yield func([]byte) error, heartbeat func() error) error {
	operation, cancel := context.WithCancel(ctx)
	events := make(chan streamEvent)
	done := make(chan error, 1)
	stopped := make(chan struct{})
	// The caller still validates/translates terminal output before releasing its lease.
	finished := false
	drainCanceled := func() {
		cancel()
		<-stopped
		finished = true
	}
	go func() {
		defer close(stopped)
		done <- result.Events(func(data []byte) error {
			event := streamEvent{data: data, reply: make(chan error, 1)}
			select {
			case events <- event:
			case <-operation.Done():
				return operation.Err()
			}
			select {
			case err := <-event.reply:
				return err
			case <-operation.Done():
				return operation.Err()
			}
		})
	}()
	defer func() {
		cancel()
		if !finished {
			result.Body.Close()
		}
		<-stopped
	}()
	var idleC, pingC <-chan time.Time
	var idleTimer *time.Timer
	if idle > 0 {
		idleTimer = time.NewTimer(idle)
		idleC = idleTimer.C
		defer idleTimer.Stop()
	}
	if keepalive > 0 && heartbeat != nil {
		ticker := time.NewTicker(keepalive)
		pingC = ticker.C
		defer ticker.Stop()
	}
	for {
		select {
		case event := <-events:
			if idleTimer != nil {
				idleTimer.Reset(idle)
			}
			err := yield(event.data)
			event.reply <- err
			if err != nil {
				<-stopped
				finished = true
				return err
			}
		case err := <-done:
			finished = true
			return err
		case <-pingC:
			if err := heartbeat(); err != nil {
				// A failed heartbeat can race cancellation; preserve the final usage drain.
				if ctx.Err() == context.Canceled {
					drainCanceled()
					return err
				}
				result.Fail(context.Canceled)
				return err
			}
		case <-idleC:
			result.Fail(context.DeadlineExceeded)
			return context.DeadlineExceeded
		case <-ctx.Done():
			if ctx.Err() == context.Canceled {
				// Codex owns a bounded drain after disconnect to capture final usage.
				// Stop downstream delivery without prematurely closing its upstream body.
				drainCanceled()
				return ctx.Err()
			}
			result.Fail(ctx.Err())
			return ctx.Err()
		}
	}
}
