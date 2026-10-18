package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenInitializesSchemaAndPreservesData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "synthetic # database.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	var journal string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("journal=%s err=%v", journal, err)
	}
	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign_keys=%d err=%v", foreignKeys, err)
	}
	if _, err := db.Exec("INSERT INTO settings (key, value) VALUES (?, ?)", "synthetic-setting", "synthetic-value"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var value string
	if err := db.QueryRow("SELECT value FROM settings WHERE key = ?", "synthetic-setting").Scan(&value); err != nil || value != "synthetic-value" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	var migrations int
	wantMigrations, versionErr := CurrentSchemaVersion()
	if err := db.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&migrations); err != nil || versionErr != nil || migrations != wantMigrations {
		t.Fatalf("migrations=%d want=%d err=%v versionErr=%v", migrations, wantMigrations, err, versionErr)
	}
}

func TestOpenAddsInvitationMigrationToExistingSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "synthetic-previous.db")
	previous, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := migrations.ReadFile("migrations/001_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := previous.ExecContext(ctx, `CREATE TABLE schema_migrations (name TEXT PRIMARY KEY NOT NULL, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if _, err := previous.ExecContext(ctx, string(initial)); err != nil {
		t.Fatal(err)
	}
	if _, err := previous.ExecContext(ctx, `INSERT INTO schema_migrations(name) VALUES('001_schema.sql'); INSERT INTO settings(key,value) VALUES('synthetic.migration','retained')`); err != nil {
		t.Fatal(err)
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var retained string
	if err := upgraded.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='synthetic.migration'`).Scan(&retained); err != nil || retained != "retained" {
		t.Fatalf("previous data: %q %v", retained, err)
	}
	var table string
	if err := upgraded.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='invitations'`).Scan(&table); err != nil || table != "invitations" {
		t.Fatalf("invitation table: %q %v", table, err)
	}
	var applied int
	if err := upgraded.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE name='002_invitations.sql'`).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("migration record: %d %v", applied, err)
	}
}

func TestOpenAddsAffinityModelWithoutMovingLegacyBindings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "synthetic-affinity-upgrade.db")
	previous, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := previous.ExecContext(ctx, `CREATE TABLE schema_migrations (name TEXT PRIMARY KEY NOT NULL, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "017_affinity_model.sql" {
			break
		}
		if err := apply(ctx, previous, entry.Name()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := previous.ExecContext(ctx, `INSERT INTO users(id,username,password_hash,role,created_at) VALUES(1,'synthetic-owner','synthetic-hash','admin',1);
		INSERT INTO tenants(id,name,owner_user_id,created_at) VALUES(1,'Synthetic workspace',1,1);
		INSERT INTO account_groups(id,name,enabled,created_at,updated_at) VALUES(1,'Synthetic pool',1,1,1);
		INSERT INTO account_affinity(user_id,group_id,session_hash,provider,account_id,expires_at) VALUES(1,1,zeroblob(32),'auto','synthetic-original-account',1900000000)`); err != nil {
		t.Fatal(err)
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		upgraded, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		var account, model string
		var expiry int64
		err = upgraded.QueryRowContext(ctx, "SELECT account_id,model,expires_at FROM account_affinity").Scan(&account, &model, &expiry)
		upgraded.Close()
		if err != nil || account != "synthetic-original-account" || model != "" || expiry != 1900000000 {
			t.Fatalf("legacy binding changed: account=%q model=%q expiry=%d err=%v", account, model, expiry, err)
		}
	}
}

func TestOpenUpdatesAccountConcurrencyDefaultAndPreservesSettings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "synthetic-account-limits.db")
	previous, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = previous.Close() })
	initial, err := migrations.ReadFile("migrations/001_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE schema_migrations (name TEXT PRIMARY KEY NOT NULL, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		string(initial),
		`INSERT INTO schema_migrations(name) VALUES('001_schema.sql')`,
		`INSERT INTO users(id,username,password_hash,role,created_at) VALUES(1,'synthetic-owner','synthetic-hash','admin',1)`,
		`INSERT INTO tenants(id,name,owner_user_id,created_at) VALUES(1,'Synthetic workspace',1,1)`,
		`INSERT INTO accounts(id,provider,name,account_id,email,plan,status,credential,expires_at,created_at,updated_at,max_concurrency) VALUES
		 ('synthetic-low','codex','Synthetic low','synthetic-low','','','ready',X'00',0,1,1,1),
		 ('synthetic-default','codex','Synthetic default','synthetic-default','','','ready',X'00',0,1,1,2),
		 ('synthetic-high','codex','Synthetic high','synthetic-high','','','ready',X'00',0,1,1,8)`,
		`INSERT INTO account_groups(id,name,enabled,created_at,updated_at) VALUES(1,'Synthetic pool',1,1,1)`,
		`INSERT INTO group_accounts(group_id,account_id) VALUES(1,'synthetic-default')`,
	} {
		if _, err := previous.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		upgraded, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = upgraded.Close() })
		for id, limit := range map[string]int{"synthetic-low": 1, "synthetic-default": 2, "synthetic-high": 8} {
			var got int
			if err := upgraded.QueryRowContext(ctx, "SELECT max_concurrency FROM accounts WHERE id=?", id).Scan(&got); err != nil || got != limit {
				t.Fatal("migration changed a saved account setting", id, got, err)
			}
		}
		var references int
		if err := upgraded.QueryRowContext(ctx, "SELECT count(*) FROM group_accounts WHERE account_id='synthetic-default'").Scan(&references); err != nil || references != 1 {
			t.Fatal("migration lost account pool references", references, err)
		}
		if attempt == 0 {
			if _, err := upgraded.ExecContext(ctx, `INSERT INTO accounts(id,provider,name,account_id,email,plan,status,credential,expires_at,created_at,updated_at) VALUES('synthetic-new','codex','Synthetic new','synthetic-new','','','ready',X'00',0,2,2)`); err != nil {
				t.Fatal(err)
			}
		}
		var got int
		if err := upgraded.QueryRowContext(ctx, "SELECT max_concurrency FROM accounts WHERE id='synthetic-new'").Scan(&got); err != nil || got != 30 {
			t.Fatal("new account default did not become 30", got, err)
		}
		for _, invalid := range []int{0, 1001} {
			if _, err := upgraded.ExecContext(ctx, "UPDATE accounts SET max_concurrency=? WHERE id='synthetic-new'", invalid); err == nil {
				t.Fatal("database accepted an invalid account limit", invalid)
			}
		}
		if err := upgraded.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenUpdatesMemberConcurrencyDefaultAndPreservesLimits(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "synthetic-members.db")
	previous, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = previous.Close() })
	initial, err := migrations.ReadFile("migrations/001_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE schema_migrations (name TEXT PRIMARY KEY NOT NULL, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		string(initial),
		`INSERT INTO schema_migrations(name) VALUES('001_schema.sql')`,
		`INSERT INTO users(id,username,password_hash,role,created_at) VALUES
		 (1,'synthetic-owner','synthetic-hash','admin',1),
		 (2,'synthetic-member','synthetic-hash','member',1),
		 (3,'synthetic-admin','synthetic-hash','member',1)`,
		`INSERT INTO tenants(id,name,owner_user_id,created_at) VALUES(1,'Synthetic workspace',1,1)`,
		`INSERT INTO memberships(tenant_id,user_id,role,enabled,requests_per_minute,max_concurrency,created_at) VALUES
		 (1,1,'owner',1,0,0,1), (1,2,'member',0,60,2,1), (1,3,'admin',1,100,8,1)`,
		`INSERT INTO member_rate(tenant_id,user_id,window_start,requests) VALUES(1,2,1900000020,5)`,
	} {
		if _, err := previous.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		upgraded, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = upgraded.Close() })
		for _, want := range []struct {
			userID, enabled, rpm, concurrency int64
			role                              string
		}{{1, 1, 0, 0, "owner"}, {2, 0, 60, 2, "member"}, {3, 1, 100, 8, "admin"}} {
			var enabled, rpm, concurrency int64
			var role string
			err := upgraded.QueryRowContext(ctx, "SELECT role,enabled,requests_per_minute,max_concurrency FROM memberships WHERE tenant_id=1 AND user_id=?", want.userID).Scan(&role, &enabled, &rpm, &concurrency)
			if err != nil || role != want.role || enabled != want.enabled || rpm != want.rpm || concurrency != want.concurrency {
				t.Fatalf("member %d changed: role=%q enabled=%d rpm=%d concurrency=%d err=%v", want.userID, role, enabled, rpm, concurrency, err)
			}
		}
		var start, requests int64
		if err := upgraded.QueryRowContext(ctx, "SELECT window_start,requests FROM member_rate WHERE tenant_id=1 AND user_id=2").Scan(&start, &requests); err != nil || start != 1900000020 || requests != 5 {
			t.Fatalf("member rate window was lost: start=%d requests=%d err=%v", start, requests, err)
		}
		if attempt == 0 {
			for _, statement := range []string{
				`INSERT INTO users(id,username,password_hash,role,created_at) VALUES
				 (4,'synthetic-new-member','synthetic-hash','member',2),
				 (5,'synthetic-new-admin','synthetic-hash','member',2),
				 (6,'synthetic-new-owner','synthetic-hash','member',2)`,
				`INSERT INTO tenants(id,name,owner_user_id,created_at) VALUES(2,'New synthetic workspace',6,2)`,
				`INSERT INTO memberships(tenant_id,user_id,role,created_at) VALUES
				 (2,4,'member',2), (2,5,'admin',2), (2,6,'owner',2)`,
			} {
				if _, err := upgraded.ExecContext(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, userID := range []int64{4, 5, 6} {
			var rpm, concurrency int64
			if err := upgraded.QueryRowContext(ctx, "SELECT requests_per_minute,max_concurrency FROM memberships WHERE tenant_id=2 AND user_id=?", userID).Scan(&rpm, &concurrency); err != nil || rpm != 0 || concurrency != 10 {
				t.Fatalf("new member %d defaults: rpm=%d concurrency=%d err=%v", userID, rpm, concurrency, err)
			}
		}
		if _, err := upgraded.ExecContext(ctx, "UPDATE memberships SET max_concurrency=1001 WHERE tenant_id=2 AND user_id=4"); err == nil {
			t.Fatal("database accepted concurrency above 1000")
		}
		if err := upgraded.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenAddsReasoningEffortAndPreservesRequestHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "synthetic-history.db")
	previous, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := previous.ExecContext(ctx, `CREATE TABLE schema_migrations (name TEXT PRIMARY KEY NOT NULL, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_schema.sql", "002_invitations.sql", "003_proxies.sql", "004_allocation_started_index.sql", "005_operational_indexes.sql"} {
		data, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := previous.ExecContext(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
		if _, err := previous.ExecContext(ctx, "INSERT INTO schema_migrations(name) VALUES(?)", name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := previous.ExecContext(ctx, `INSERT INTO request_records(user_id,key_id,group_id,model,transport,operation,started_at,duration_ms,outcome,request_id) VALUES(1,1,1,'synthetic-model','http','responses',1900000000,42,'success','req_synthetic_legacy')`); err != nil {
		t.Fatal(err)
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		upgraded, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		var effort, requestID string
		err = upgraded.QueryRowContext(ctx, "SELECT reasoning_effort,request_id FROM request_records").Scan(&effort, &requestID)
		upgraded.Close()
		if err != nil || effort != "" || requestID != "req_synthetic_legacy" {
			t.Fatalf("retained history: effort=%q id=%q err=%v", effort, requestID, err)
		}
	}
}

const formerProxySchema = `CREATE TABLE proxies (
 id TEXT PRIMARY KEY NOT NULL,
 tenant_id INTEGER NOT NULL REFERENCES tenants(id),
 name TEXT NOT NULL,
 address BLOB NOT NULL CHECK(length(address) <= 8192),
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 UNIQUE(tenant_id, name)
);
ALTER TABLE accounts ADD COLUMN proxy_id TEXT REFERENCES proxies(id) ON DELETE RESTRICT;
CREATE INDEX accounts_proxy_id_idx ON accounts(proxy_id) WHERE proxy_id IS NOT NULL;`

const formerProxyChecks = `ALTER TABLE proxies ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE proxies ADD COLUMN checked_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE proxies ADD COLUMN reachable INTEGER NOT NULL DEFAULT 0 CHECK(reachable IN (0,1));
ALTER TABLE proxies ADD COLUMN exit_ip TEXT NOT NULL DEFAULT '' CHECK(length(exit_ip) <= 45);
ALTER TABLE proxies ADD COLUMN country TEXT NOT NULL DEFAULT '' CHECK(length(country) <= 2);
ALTER TABLE proxies ADD COLUMN region TEXT NOT NULL DEFAULT '' CHECK(length(region) <= 128);
ALTER TABLE proxies ADD COLUMN city TEXT NOT NULL DEFAULT '' CHECK(length(city) <= 128);
ALTER TABLE proxies ADD COLUMN latency_ms INTEGER NOT NULL DEFAULT 0 CHECK(latency_ms BETWEEN 0 AND 30000);
ALTER TABLE proxies ADD COLUMN check_error TEXT NOT NULL DEFAULT '' CHECK(length(check_error) <= 32);`

func formerProxyDatabase(t *testing.T, path string, withChecks bool) {
	t.Helper()
	ctx := context.Background()
	previous, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := previous.ExecContext(ctx, `CREATE TABLE schema_migrations (name TEXT PRIMARY KEY NOT NULL, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"001_schema.sql", "002_invitations.sql"} {
		data, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := previous.ExecContext(ctx, string(data)); err != nil {
			t.Fatal(name, err)
		}
		if _, err := previous.ExecContext(ctx, "INSERT INTO schema_migrations(name) VALUES(?)", name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := previous.ExecContext(ctx, formerProxySchema); err != nil {
		t.Fatal(err)
	}
	if _, err := previous.ExecContext(ctx, "INSERT INTO schema_migrations(name) VALUES('003_proxies.sql')"); err != nil {
		t.Fatal(err)
	}
	if withChecks {
		if _, err := previous.ExecContext(ctx, formerProxyChecks); err != nil {
			t.Fatal(err)
		}
		if _, err := previous.ExecContext(ctx, "INSERT INTO schema_migrations(name) VALUES('004_proxy_checks.sql')"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := previous.ExecContext(ctx, `INSERT INTO users(id,username,role,password_hash,enabled,created_at) VALUES(1,'synthetic-admin','admin','synthetic-hash',1,1); INSERT INTO tenants(id,name,owner_user_id,created_at) VALUES(1,'Synthetic workspace',1,1); INSERT INTO proxies(id,tenant_id,name,address,created_at,updated_at) VALUES('synthetic-proxy',1,'Synthetic exit',x'01',1,1)`); err != nil {
		t.Fatal(err)
	}
	if withChecks {
		if _, err := previous.ExecContext(ctx, `UPDATE proxies SET checked_at=123,reachable=1,exit_ip='203.0.113.8',country='US' WHERE id='synthetic-proxy'`); err != nil {
			t.Fatal(err)
		}
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAddsProxyCheckColumnsToExistingProxySchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "synthetic-previous-proxy.db")
	formerProxyDatabase(t, path, false)
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var name string
	var revision, checkedAt, reachable int64
	if err := upgraded.QueryRowContext(ctx, `SELECT name,revision,checked_at,reachable FROM proxies WHERE id='synthetic-proxy'`).Scan(&name, &revision, &checkedAt, &reachable); err != nil || name != "Synthetic exit" || revision != 0 || checkedAt != 0 || reachable != 0 {
		t.Fatalf("proxy migration: %q %d %d %d %v", name, revision, checkedAt, reachable, err)
	}
	var count int
	wantMigrations, err := CurrentSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if err := upgraded.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != wantMigrations {
		t.Fatalf("merged migration count: %d %v", count, err)
	}
}

func TestOpenNormalizesFormerProxyCheckMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "synthetic-four-step.db")
	formerProxyDatabase(t, path, true)
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var checkedAt int64
	var exitIP string
	if err := upgraded.QueryRowContext(ctx, `SELECT checked_at,exit_ip FROM proxies WHERE id='synthetic-proxy'`).Scan(&checkedAt, &exitIP); err != nil || checkedAt != 123 || exitIP != "203.0.113.8" {
		t.Fatalf("former proxy data: %d %q %v", checkedAt, exitIP, err)
	}
	var count int
	wantMigrations, err := CurrentSchemaVersion()
	if err != nil {
		t.Fatal(err)
	}
	if err := upgraded.QueryRowContext(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != wantMigrations {
		t.Fatalf("former migration record retained: %d %v", count, err)
	}
}

func TestValidateSnapshotAcceptsFormerProxyCheckHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "synthetic-four-step-backup.db")
	formerProxyDatabase(t, path, true)
	connection, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	version, err := ValidateSnapshot(ctx, connection)
	if err != nil || version != 4 {
		t.Fatalf("former backup history: %d %v", version, err)
	}
	current, err := CurrentSchemaVersion()
	if err != nil || current <= version {
		t.Fatalf("merged current version: %d %v", current, err)
	}
}

func TestOpenExplainsUnsupportedPrereleaseDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "synthetic-legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY, applied_at TEXT);
		INSERT INTO schema_migrations(name,applied_at) VALUES('001_settings.sql','synthetic-time');
		CREATE TABLE account_affinity(id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Open(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "older pre-release schema") {
		t.Fatalf("legacy database did not explain startup failure: %v", err)
	}
	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var count int
	if err := check.QueryRow(`SELECT count(*) FROM schema_migrations WHERE name='001_schema.sql'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy database was modified during rejection: count=%d err=%v", count, err)
	}
}
