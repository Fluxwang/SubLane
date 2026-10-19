package apikey

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/murongg/SubLane/internal/auth"
	"github.com/murongg/SubLane/internal/groups"
	"github.com/murongg/SubLane/internal/storage"
	"github.com/murongg/SubLane/internal/tenants"
	"github.com/murongg/SubLane/internal/vault"
)

func TestPersonalKeysAreBoundToWorkspace(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	connection, err := storage.Open(ctx, filepath.Join(dir, "synthetic.db"))
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
	member, err := identity.CreateMember(ctx, "synthetic-member", "synthetic-password")
	if err != nil {
		t.Fatal(err)
	}
	secondWorkspace, err := tenants.New(connection).Create(ctx, member.ID, "Second workspace")
	if err != nil {
		t.Fatal(err)
	}
	firstPool, err := groups.New(connection).Save(ctx, 0, groups.Input{Name: "Pool", Enabled: true, AccountIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	secondPool, err := groups.NewForTenant(connection, secondWorkspace.ID).Save(ctx, 0, groups.Input{Name: "Pool", Enabled: true, AccountIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := groups.New(connection).SetMemberGroups(ctx, member.ID, []int64{firstPool.ID}); err != nil {
		t.Fatal(err)
	}
	cipher, err := vault.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	first := New(connection, cipher)
	second := NewForTenant(connection, cipher, secondWorkspace.ID)
	a, err := first.CreateInGroup(ctx, member.ID, firstPool.ID, "First key")
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.CreateInGroup(ctx, member.ID, secondPool.ID, "Second key")
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		service *Service
		own     CreatedKey
		other   CreatedKey
	}{{first, a, b}, {second, b, a}} {
		for _, includeRevoked := range []bool{false, true} {
			page, err := check.service.List(ctx, member.ID, Filter{IncludeRevoked: includeRevoked})
			if err != nil || len(page.Keys) != 1 || page.Total != 1 || page.Keys[0].ID != check.own.Key.ID {
				t.Fatalf("key metadata leaked across workspaces: %+v, %v", page, err)
			}
		}
		if _, err := check.service.Authenticate(ctx, check.other.Secret); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("foreign workspace key authenticated: %v", err)
		}
		principal, err := check.service.Authenticate(ctx, check.own.Secret)
		if err != nil || principal.TenantID != check.service.tenantID {
			t.Fatalf("key lost its immutable workspace binding: %+v, %v", principal, err)
		}
		if _, err := check.service.Reveal(ctx, member.ID, check.other.Key.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign workspace key secret disclosed: %v", err)
		}
	}
}

func TestUsableKeyReadinessRequiresAnActiveKeyAndReadyPoolAccount(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	connection, err := storage.Open(ctx, filepath.Join(dir, "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	identity, err := auth.New(connection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Setup(ctx, "synthetic-owner", "synthetic-password", "Synthetic workspace"); err != nil {
		t.Fatal(err)
	}
	pool, err := groups.New(connection).Save(ctx, 0, groups.Input{Name: "Synthetic pool", Enabled: true, AccountIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := vault.Open(filepath.Join(dir, "key"), true)
	if err != nil {
		t.Fatal(err)
	}
	service := New(connection, cipher)
	created, err := service.CreateInGroup(ctx, 1, pool.ID, "Synthetic key")
	if err != nil {
		t.Fatal(err)
	}
	check := func(want bool) {
		t.Helper()
		ready, err := service.HasUsableKey(ctx, 1)
		if err != nil || ready != want {
			t.Fatalf("usable key readiness: got %v, want %v, err %v", ready, want, err)
		}
	}
	check(false)
	if _, err := connection.ExecContext(ctx, `INSERT INTO accounts(id,name,account_id,status,credential,created_at,updated_at) VALUES('synthetic-account','Synthetic account','synthetic-upstream','ready',x'01',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(ctx, "INSERT INTO group_accounts(group_id,account_id) VALUES(?,'synthetic-account')", pool.ID); err != nil {
		t.Fatal(err)
	}
	check(true)
	if _, err := connection.ExecContext(ctx, "UPDATE accounts SET provider='xai' WHERE id='synthetic-account'"); err != nil {
		t.Fatal(err)
	}
	check(true)
	if _, err := connection.ExecContext(ctx, "UPDATE accounts SET provider='claude' WHERE id='synthetic-account'"); err != nil {
		t.Fatal(err)
	}
	check(true)
	if _, err := connection.ExecContext(ctx, "UPDATE accounts SET provider='antigravity' WHERE id='synthetic-account'"); err != nil {
		t.Fatal(err)
	}
	check(true)
	if _, err := connection.ExecContext(ctx, "UPDATE accounts SET provider='codex' WHERE id='synthetic-account'"); err != nil {
		t.Fatal(err)
	}
	check(true)
	if _, err := service.Update(ctx, 1, created.Key.ID, UpdateInput{Name: "Synthetic key", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	check(false)
}

func TestKeyOwnershipHashingAndRevocation(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	identity, err := auth.New(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Setup(ctx, "owner-test", "owner pass 42", "Synthetic workspace"); err != nil {
		t.Fatal(err)
	}
	member, err := identity.CreateMember(ctx, "member-test", "member pass 42")
	if err != nil {
		t.Fatal(err)
	}
	keys := newTestKeys(t, db)
	created, err := keys.CreateInGroup(ctx, member.ID, 1, "Laptop test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Secret, "sl_") || len(created.Secret) != 46 {
		t.Fatal("invalid key format")
	}
	var digest []byte
	if err := db.QueryRow("SELECT token_hash FROM api_keys WHERE id = ?", created.Key.ID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256([]byte(created.Secret))
	if string(digest) != string(expected[:]) {
		t.Fatal("plaintext key is not hashed")
	}
	page, err := keys.List(ctx, member.ID, Filter{})
	if err != nil || len(page.Keys) != 1 {
		t.Fatal("missing own key", err)
	}
	payload, _ := json.Marshal(page)
	if strings.Contains(string(payload), created.Secret) || strings.Contains(string(payload), "token_hash") {
		t.Fatal("secret leaked in metadata")
	}
	other, err := keys.List(ctx, 1, Filter{})
	if err != nil || len(other.Keys) != 0 {
		t.Fatal("another user's keys were exposed")
	}
	if _, err := keys.Revoke(ctx, 1, created.Key.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-user revocation accepted")
	}
	principal, err := keys.Authenticate(ctx, created.Secret)
	if err != nil || principal.UserID != member.ID {
		t.Fatal("key authentication failed", err)
	}
	if _, err := identity.SetMemberEnabled(ctx, member.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Authenticate(ctx, created.Secret); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("disabled member key worked")
	}
	if _, err := identity.SetMemberEnabled(ctx, member.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Authenticate(ctx, created.Secret); err != nil {
		t.Fatal("active key not restored after re-enable", err)
	}
	if _, err := keys.Revoke(ctx, member.ID, created.Key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Authenticate(ctx, created.Secret); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("revoked key worked")
	}
	page, err = keys.List(ctx, member.ID, Filter{IncludeRevoked: true})
	if err != nil || page.Keys[0].RevokedAt == nil || page.Keys[0].LastUsedAt == nil {
		t.Fatal("missing key lifecycle metadata", err)
	}
}

func TestKeyLimitAndInvalidInput(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	identity, err := auth.New(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := identity.Setup(ctx, "owner-test", "owner pass 42", "Synthetic workspace"); err != nil {
		t.Fatal(err)
	}
	keys := newTestKeys(t, db)
	for _, name := range []string{"", "   ", strings.Repeat("x", 65)} {
		if _, err := keys.CreateInGroup(ctx, 1, 1, name); !errors.Is(err, ErrInput) {
			t.Fatal("invalid key name accepted")
		}
	}
	var first int64
	for i := 0; i < 20; i++ {
		key, err := keys.CreateInGroup(ctx, 1, 1, "Synthetic key")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = key.Key.ID
		}
	}
	if _, err := keys.CreateInGroup(ctx, 1, 1, "Too many"); !errors.Is(err, ErrLimit) {
		t.Fatal("active key limit not enforced")
	}
	if _, err := keys.Revoke(ctx, 1, first); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.CreateInGroup(ctx, 1, 1, "Replacement"); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "sl_short", strings.Repeat("x", 46)} {
		if _, err := keys.Authenticate(ctx, token); !errors.Is(err, ErrInvalidKey) {
			t.Fatal("invalid token accepted")
		}
	}
}

func TestKeysEnforceGroupGrantsOnCreationAndEveryAuthentication(t *testing.T) {
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "groups.db"))
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
	member, err := identity.CreateMember(ctx, "synthetic-member", "synthetic-pass")
	if err != nil {
		t.Fatal(err)
	}
	configureTestPool(t, connection)
	pools := groups.New(connection)
	pool, err := pools.Save(ctx, 0, groups.Input{Name: "Private pool", Enabled: true, AccountIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	keys := newTestKeys(t, connection)
	if _, err := keys.CreateInGroup(ctx, member.ID, pool.ID, "Denied"); !errors.Is(err, groups.ErrUnavailable) {
		t.Fatal("unauthorized group accepted", err)
	}
	if err := pools.SetMemberGroups(ctx, member.ID, []int64{pool.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.CreateInGroup(ctx, member.ID, 1, "Default denied"); !errors.Is(err, groups.ErrUnavailable) {
		t.Fatal("omitted group bypassed revoked default grant", err)
	}
	created, err := keys.CreateInGroup(ctx, member.ID, pool.ID, "Synthetic key")
	if err != nil {
		t.Fatal(err)
	}
	principal, err := keys.Authenticate(ctx, created.Secret)
	if err != nil || principal.GroupID != pool.ID {
		t.Fatal("key group not preserved", err)
	}
	if err := pools.SetMemberGroups(ctx, member.ID, []int64{}); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Authenticate(ctx, created.Secret); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("revoked group access remained usable", err)
	}
	page, err := keys.List(ctx, member.ID, Filter{})
	if err != nil || page.Keys[0].GroupAccess != "blocked" {
		t.Fatal("blocked group not reflected in metadata", err)
	}
	if err := pools.SetMemberGroups(ctx, member.ID, []int64{pool.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := pools.Save(ctx, pool.ID, groups.Input{Name: pool.Name, Enabled: false, AccountIDs: []string{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.Authenticate(ctx, created.Secret); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("disabled group key worked", err)
	}
}

func TestManagedPoolRequiresSchemeBinding(t *testing.T) {
	ctx := context.Background()
	conn, err := storage.Open(ctx, filepath.Join(t.TempDir(), "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	identity, err := auth.New(conn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = identity.Setup(ctx, "synthetic-admin", "synthetic-pass", "Synthetic workspace"); err != nil {
		t.Fatal(err)
	}
	member, err := identity.CreateMember(ctx, "synthetic-user", "synthetic-pass")
	if err != nil {
		t.Fatal(err)
	}
	keys := newTestKeys(t, conn)
	legacy, err := keys.CreateInGroup(ctx, member.ID, 1, "Legacy")
	if err != nil {
		t.Fatal(err)
	}
	// Minimal synthetic scheme fixture deliberately exercises authentication independent of management validation.
	for _, stmt := range []string{"INSERT OR IGNORE INTO group_members(group_id,user_id) VALUES(1,2)", "INSERT INTO allocation_schemes(id,name,group_id,enabled,created_at) VALUES(1,'Synthetic',1,1,1)", `INSERT INTO allocation_revisions(scheme_id,effective_at,config) VALUES(1,1,'{"mode":"tokens","period":"day","members":[{"user_id":2,"limit":100}],"rates":[] }')`} {
		if _, err = conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = keys.Authenticate(ctx, legacy.Secret); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("legacy key bypassed managed pool", err)
	}
	bound, err := keys.CreateInScheme(ctx, member.ID, 1, "Bound", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = keys.Authenticate(ctx, bound.Secret); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec("DELETE FROM group_members WHERE user_id=2"); err != nil {
		t.Fatal(err)
	}
	if _, err = keys.Authenticate(ctx, bound.Secret); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("removed member retained access", err)
	}
}
