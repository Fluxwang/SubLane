package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	queries "github.com/murongg/SubLane/internal/storage/db"
)

func TestKeyWindowShareMigrationKeepsMemberAllocations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "synthetic.db")
	previous, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = previous.Exec(`CREATE TABLE schema_migrations(name TEXT PRIMARY KEY NOT NULL,applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	entries, _ := migrations.ReadDir("migrations")
	for _, entry := range entries {
		if entry.Name() >= "019_key_window_shares.sql" {
			break
		}
		if err := apply(ctx, previous, entry.Name()); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO users(id,username,password_hash,role,created_at) VALUES(1,'synthetic-owner','synthetic-hash','admin',1)`,
		`INSERT INTO tenants(id,name,owner_user_id,created_at) VALUES(1,'Synthetic workspace',1,1)`,
		`INSERT INTO account_groups(id,name,enabled,created_at,updated_at) VALUES(1,'Synthetic pool',1,1,1)`,
		`INSERT INTO api_keys(id,user_id,group_id,name,prefix,token_hash,created_at) VALUES(7,1,1,'synthetic','sl_fake',randomblob(32),1)`,
		`INSERT INTO allocation_schemes(id,name,group_id,enabled,created_at) VALUES(1,'Synthetic allowance',1,1,1)`,
		`INSERT INTO allocation_revisions(id,scheme_id,effective_at,config) VALUES(1,1,1,'{"mode":"tokens","period":"day","members":[{"user_id":1,"limit":5}],"rates":[]}')`,
		`INSERT INTO allocation_keys(key_id,scheme_id) VALUES(7,1)`,
		`INSERT INTO allocation_entries(request_id,scheme_id,revision_id,user_id,account_id,model,mode,window_start,reset_at,started_at,state,cost)
			VALUES('synthetic-request',1,1,1,'synthetic-account','synthetic','tokens',1,86401,10,'settled',5)`,
	} {
		if _, err := previous.Exec(statement); err != nil {
			t.Fatal(statement, err)
		}
	}
	previous.Close()
	connection, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	q := queries.New(connection)
	binding, err := q.GetKeyAllocationBinding(ctx, 7)
	if err != nil || binding.SchemeID != 1 || binding.JoinedAt != 0 {
		t.Fatal("member-mode key binding changed", binding, err)
	}
	usage, err := q.AllocationMemberUsage(ctx, queries.AllocationMemberUsageParams{SchemeID: 1, UserID: 1, StartedFrom: 0, StartedTo: 86400, Mode: "tokens"})
	if err != nil || usage.Used != 5 {
		t.Fatal("member-mode usage lost", usage, err)
	}
	keyed, err := q.AllocationKeyUsage(ctx, queries.AllocationKeyUsageParams{SchemeID: 1, KeyID: 7, StartedFrom: 0, StartedTo: 86400})
	if err != nil || keyed.Used != 0 {
		t.Fatal("member-mode usage was attributed to a key", keyed, err)
	}
}
