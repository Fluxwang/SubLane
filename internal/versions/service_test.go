package versions

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/murongg/SubLane/internal/audit"
	"github.com/murongg/SubLane/internal/auth"
	"github.com/murongg/SubLane/internal/storage"
)

func TestVersionPrecedencePersistenceAndMonotonicSync(t *testing.T) {
	for _, provider := range []Provider{Codex, Grok, Claude} {
		t.Run(string(provider), func(t *testing.T) {
			ctx := context.Background()
			connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "versions.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			identity, err := auth.New(connection)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := identity.Setup(ctx, "synthetic-admin", "synthetic-password", "Synthetic workspace"); err != nil {
				t.Fatal(err)
			}
			clock := atomic.Int64{}
			clock.Store(1900000000)
			latest := "0.200.0"
			calls := 0
			fetch := func(context.Context) (string, error) { calls++; return latest, nil }
			s, err := NewForProvider(ctx, connection, provider, "0.100.0", fetch)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			s.now = func() time.Time { return time.Unix(clock.Load(), 0) }
			if s.Current() != "0.100.0" || !s.View().AutoSync {
				t.Fatal("invalid defaults", s.View())
			}
			if _, err := s.Sync(ctx, true); err != nil {
				t.Fatal(err)
			}
			if s.Current() != "0.200.0" || s.View().Source != "synced" {
				t.Fatal(s.View())
			}
			actor := audit.WithActor(ctx, audit.Actor{TenantID: 1, ID: 1, Username: "synthetic-admin", Role: "admin", Source: "user"})
			if _, err := s.Save(actor, Config{ManualVersion: "0.150.0", AutoSync: true}); err != nil {
				t.Fatal(err)
			}
			clock.Add(61)
			latest = "0.300.0"
			if _, err := s.Sync(ctx, true); err != nil {
				t.Fatal(err)
			}
			if s.Current() != "0.150.0" || s.View().LatestVersion != "0.300.0" {
				t.Fatal("sync replaced manual pin", s.View())
			}
			if _, err := s.Save(actor, Config{AutoSync: false}); err != nil {
				t.Fatal(err)
			}
			if s.Current() != "0.300.0" {
				t.Fatal("disabling sync lost known version")
			}
			clock.Add(61)
			latest = "0.250.0"
			if _, err := s.Sync(ctx, true); err != nil {
				t.Fatal(err)
			}
			if s.Current() != "0.300.0" {
				t.Fatal("automatic version downgraded")
			}
			reopened, err := NewForProvider(ctx, connection, provider, "0.100.0", fetch)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if reopened.Current() != "0.300.0" || reopened.View().AutoSync {
				t.Fatal("settings did not survive restart", reopened.View())
			}
			var events int
			if err := connection.QueryRow("SELECT count(*) FROM audit_events WHERE action='settings.update'").Scan(&events); err != nil || events != 2 {
				t.Fatal("settings audit missing", events, err)
			}
			if calls != 3 {
				t.Fatal("unexpected release requests", calls)
			}

		})
	}
}

func TestVersionFailureRetentionCooldownAndPersistenceFailure(t *testing.T) {
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "versions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var fail atomic.Bool
	var calls atomic.Int32
	s, err := New(ctx, connection, "0.100.0", func(context.Context) (string, error) {
		calls.Add(1)
		if fail.Load() {
			return "", errors.New("synthetic upstream body must not escape")
		}
		return "0.200.0", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	clock := atomic.Int64{}
	clock.Store(1900000000)
	s.now = func() time.Time { return time.Unix(clock.Load(), 0) }
	if _, err := s.Sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync(ctx, true); !errors.Is(err, ErrCooldown) || calls.Load() != 1 {
		t.Fatal("manual cooldown bypassed", err)
	}
	clock.Add(61)
	fail.Store(true)
	if _, err := s.Sync(ctx, true); !errors.Is(err, ErrSync) {
		t.Fatal("unsafe release error", err)
	}
	if s.Current() != "0.200.0" || !s.View().SyncFailed || s.View().CheckedAt != clock.Load() {
		t.Fatal("failure lost stable version", s.View())
	}
	if _, err := connection.Exec("CREATE TRIGGER reject_version_write BEFORE UPDATE ON settings BEGIN SELECT RAISE(ABORT,'synthetic failure'); END;"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(ctx, Config{ManualVersion: "0.400.0", AutoSync: false}); err == nil {
		t.Fatal("failed save reported success")
	}
	if s.Current() != "0.200.0" || !s.View().AutoSync {
		t.Fatal("uncommitted setting was published")
	}
}

func TestVersionValidationAndBuiltinFloor(t *testing.T) {
	for _, value := range []string{"0.155.1", "1.2.3", " 0.155.1 "} {
		if _, err := normalize(value); err != nil {
			t.Fatal(value, err)
		}
	}
	for _, value := range []string{"0.1", "v0.1.0", "0.1.0-alpha.1", "0.1.0\nInjected: yes", "01.2.3", "999999999999999999.1.1"} {
		if _, err := normalize(value); !errors.Is(err, ErrInput) {
			t.Fatal("invalid version accepted", value)
		}
	}
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "versions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	s, err := New(ctx, connection, "0.200.0", func(context.Context) (string, error) { return "0.100.0", nil })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Sync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if s.Current() != "0.200.0" {
		t.Fatal("release response lowered built-in baseline")
	}
}

func TestVersionSyncKeepsConcurrentManualSettingsAndJoinsShutdown(t *testing.T) {
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "versions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	s, err := New(ctx, connection, "0.100.0", func(ctx context.Context) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return "0.200.0", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	done := make(chan error, 1)
	go func() { _, err := s.Sync(context.Background(), true); done <- err }()
	<-started
	if _, err := s.Save(ctx, Config{ManualVersion: "0.150.0", AutoSync: false}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.Current() != "0.150.0" || s.View().AutoSync {
		t.Fatal("check overwrote concurrent administrator settings")
	}
	blocked, err := New(ctx, connection, "0.100.0", func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	blocked.now = func() time.Time { return time.Now().Add(7 * time.Hour) }
	finished := make(chan struct{})
	go func() { blocked.Sync(context.Background(), true); close(finished) }()
	deadline := time.Now().Add(time.Second)
	for !blocked.View().Syncing && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	blocked.Close()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not join release request")
	}
}

func TestAutomaticCheckCadenceSurvivesRestartAndUnchangedRelease(t *testing.T) {
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "versions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var calls atomic.Int32
	fetch := func(context.Context) (string, error) { calls.Add(1); return "0.200.0", nil }
	clock := atomic.Int64{}
	clock.Store(time.Now().Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0) }
	s, err := New(ctx, connection, "0.100.0", fetch)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.now = now
	if _, err := s.Sync(ctx, false); err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(time.Hour.Seconds()))
	reopened, err := New(ctx, connection, "0.100.0", fetch)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.now = now
	if _, err := reopened.Sync(ctx, false); err != nil || calls.Load() != 1 {
		t.Fatal("restart repeated a fresh release check", err)
	}
	clock.Add(int64(syncInterval.Seconds()))
	if _, err := reopened.Sync(ctx, false); err != nil || calls.Load() != 2 {
		t.Fatal("automatic cadence skipped", err)
	}
	if reopened.View().CheckedAt != clock.Load() {
		t.Fatal("unchanged release did not renew the persisted check time")
	}
	if _, err := reopened.Save(ctx, Config{AutoSync: false}); err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(syncInterval.Seconds()))
	if _, err := reopened.Sync(ctx, false); err != nil || calls.Load() != 2 {
		t.Fatal("disabled automatic sync made a request", err)
	}
}

func TestDisablingAutomaticSyncCancelsItsPendingResult(t *testing.T) {
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "versions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	started := make(chan struct{})
	s, err := New(ctx, connection, "0.100.0", func(ctx context.Context) (string, error) { close(started); <-ctx.Done(); return "0.200.0", nil })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	done := make(chan error, 1)
	go func() { _, err := s.Sync(ctx, false); done <- err }()
	<-started
	if _, err := s.Save(ctx, Config{AutoSync: false}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s.Current() != "0.100.0" || s.View().LatestVersion != "" || s.View().SyncFailed {
		t.Fatal("disabled operation still published", s.View())
	}
}

func TestVersionPersistenceFailureDoesNotPublishOrHammerUpstream(t *testing.T) {
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "versions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var calls atomic.Int32
	s, err := New(ctx, connection, "0.100.0", func(context.Context) (string, error) { calls.Add(1); return "0.200.0", nil })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Save(ctx, Config{AutoSync: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec("CREATE TRIGGER reject_sync BEFORE UPDATE ON settings BEGIN SELECT RAISE(ABORT,'synthetic failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync(ctx, true); !errors.Is(err, ErrStorage) {
		t.Fatal("failed persistence accepted", err)
	}
	if s.Current() != "0.100.0" || !s.View().SyncFailed {
		t.Fatal("uncommitted release published")
	}
	if _, err := s.Sync(ctx, true); !errors.Is(err, ErrCooldown) || calls.Load() != 1 {
		t.Fatal("storage failure bypassed backoff", err, calls.Load())
	}
}
