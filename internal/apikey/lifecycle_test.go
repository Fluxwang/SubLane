package apikey

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/murongg/SubLane/internal/audit"
	"github.com/murongg/SubLane/internal/auth"
	"github.com/murongg/SubLane/internal/storage"
)

func TestKeyLifecycleAndExpiryBoundary(t *testing.T) {
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "lifecycle.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	identity, err := auth.New(connection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Setup(ctx, "synthetic-admin", "synthetic-pass", "Synthetic workspace"); err != nil {
		t.Fatal(err)
	}
	ctx = audit.WithActor(ctx, audit.Actor{TenantID: 1, ID: 1, Username: "synthetic-admin", Role: "admin", Source: "user"})
	keys := newTestKeys(t, connection)
	now := time.Now().Unix()
	keys.now = func() time.Time { return time.Unix(now, 0) }
	expiry := now + 60
	created, err := keys.CreateWithExpiry(ctx, 1, 1, "Test key", &expiry)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Key.Enabled || created.Key.ExpiresAt == nil {
		t.Fatal("lifecycle metadata missing")
	}
	if _, err := keys.Authenticate(ctx, created.Secret); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Update(ctx, 2, created.Key.ID, UpdateInput{Name: "Other", Enabled: false}); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-owner edit", err)
	}
	changed, err := keys.Update(ctx, 1, created.Key.ID, UpdateInput{Name: "Renamed", Enabled: false, ExpiresAt: &expiry})
	if err != nil || changed.Name != "Renamed" {
		t.Fatal(err)
	}
	if _, err := keys.Authenticate(ctx, created.Secret); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("paused key authenticated")
	}
	if _, err := keys.Update(ctx, 1, created.Key.ID, UpdateInput{Name: "Renamed", Enabled: true, ExpiresAt: &expiry}); err != nil {
		t.Fatal(err)
	}
	now = expiry
	if _, err := keys.Authenticate(ctx, created.Secret); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("key valid at expiry boundary")
	}
	if _, err := keys.Update(ctx, 1, created.Key.ID, UpdateInput{Name: "Expired", Enabled: true, ExpiresAt: &expiry}); err != nil {
		t.Fatal("expired key cannot be renamed", err)
	}
	if _, err := keys.Update(ctx, 1, created.Key.ID, UpdateInput{Name: "Extended", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Authenticate(ctx, created.Secret); err != nil {
		t.Fatal("secret changed by metadata edit", err)
	}
	if _, err := keys.CreateWithExpiry(ctx, 1, 1, "Past", &expiry); !errors.Is(err, ErrInput) {
		t.Fatal("past expiry accepted")
	}
	if _, err := keys.Revoke(ctx, 1, created.Key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Update(ctx, 1, created.Key.ID, UpdateInput{Name: "Revived", Enabled: true}); !errors.Is(err, ErrRevoked) {
		t.Fatal("revoked key edited", err)
	}
	events, err := audit.New(connection).List(ctx, audit.Filter{Resource: "key"})
	if err != nil || len(events.Events) != 6 {
		t.Fatalf("audit events: %d %v", len(events.Events), err)
	}
	if _, err := connection.Exec("CREATE TRIGGER fail_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.CreateInGroup(ctx, 1, 1, "Rollback"); err == nil {
		t.Fatal("audit failure ignored")
	}
	page, err := keys.List(ctx, 1, Filter{IncludeRevoked: true})
	if err != nil || len(page.Keys) != 1 {
		t.Fatal("key survived audit failure", err)
	}
}
