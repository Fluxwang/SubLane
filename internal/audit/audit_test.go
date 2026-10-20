package audit

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/murongg/SubLane/internal/storage"
	"github.com/murongg/SubLane/internal/storage/db"
)

func seedAuditTenant(t *testing.T, connection *sql.DB, tenantID int64) {
	t.Helper()
	role := "member"
	if tenantID == 1 {
		role = "admin"
	}
	if _, err := connection.Exec(`INSERT INTO users(id,username,role,password_hash,enabled,created_at)
		VALUES(?,?,?,'synthetic-hash',1,1)`, tenantID, fmt.Sprintf("synthetic-user-%d", tenantID), role); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(`INSERT INTO tenants(id,name,owner_user_id,created_at) VALUES(?,?,?,1)`, tenantID, "Synthetic workspace", tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(`INSERT INTO memberships(tenant_id,user_id,role,created_at) VALUES(?,?,'owner',1)`, tenantID, tenantID); err != nil {
		t.Fatal(err)
	}
}

func TestManagementAuditIsScopedToWorkspace(t *testing.T) {
	connection, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	seedAuditTenant(t, connection, 1)
	seedAuditTenant(t, connection, 2)
	for _, tenantID := range []int64{1, 2} {
		ctx := WithActor(context.Background(), Actor{TenantID: tenantID, ID: tenantID, Username: "synthetic-actor", Role: "admin", Source: "user"})
		if err := Record(ctx, db.New(connection), "group.create", "group", ID(tenantID)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tenantID := range []int64{1, 2} {
		page, err := NewForTenant(connection, tenantID).List(context.Background(), Filter{})
		if err != nil || len(page.Events) != 1 || page.Total != 1 || page.Events[0].ResourceID != ID(tenantID) {
			t.Fatalf("management audit leaked across workspaces: %+v, %v", page, err)
		}
	}
}

func TestAllocationDeletionAuditRecordsItsAction(t *testing.T) {
	ctx := WithActor(context.Background(), Actor{TenantID: 1, ID: 1, Username: "synthetic-admin", Role: "admin", Source: "local"})
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	seedAuditTenant(t, connection, 1)
	if err := Record(ctx, db.New(connection), "allocation.delete", "allocation", "7"); err != nil {
		t.Fatal(err)
	}
	page, err := New(connection).List(ctx, Filter{Resource: "allocation"})
	if err != nil || len(page.Events) != 1 || page.Events[0].Action != "allocation.delete" {
		t.Fatal(page, err)
	}
}

func TestProxyAuditCanBeFiltered(t *testing.T) {
	ctx := WithActor(context.Background(), Actor{TenantID: 1, ID: 1, Username: "synthetic-admin", Role: "admin", Source: "user"})
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	seedAuditTenant(t, connection, 1)
	id := strings.Repeat("a", 32)
	if err := Record(ctx, db.New(connection), "proxy.create", "proxy", id); err != nil {
		t.Fatal(err)
	}
	page, err := New(connection).List(ctx, Filter{Resource: "proxy"})
	if err != nil || len(page.Events) != 1 || page.Events[0].ResourceID != id {
		t.Fatalf("proxy filter: %+v %v", page, err)
	}
}

func TestInvitationAuditCanBeFiltered(t *testing.T) {
	ctx := WithActor(context.Background(), Actor{TenantID: 1, ID: 1, Username: "synthetic-admin", Role: "admin", Source: "user"})
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	seedAuditTenant(t, connection, 1)
	if err := Record(ctx, db.New(connection), "invitation.create", "invitation", "3"); err != nil {
		t.Fatal(err)
	}
	page, err := New(connection).List(ctx, Filter{Resource: "invitation"})
	if err != nil || len(page.Events) != 1 || page.Events[0].Action != "invitation.create" {
		t.Fatalf("invitation filter: %+v %v", page, err)
	}
}

func TestAuditTransactionAndBoundedListing(t *testing.T) {
	ctx := WithActor(context.Background(), Actor{TenantID: 1, ID: 1, Username: "synthetic-admin", Role: "admin", Source: "user"})
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	seedAuditTenant(t, connection, 1)
	q := db.New(connection)
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Record(ctx, q.WithTx(tx), "key.create", "key", "12"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	service := New(connection)
	page, err := service.List(ctx, Filter{})
	if err != nil || len(page.Events) != 0 {
		t.Fatal("audit survived rollback", err)
	}
	for range 52 {
		if err := Record(ctx, q, "key.update", "key", "12"); err != nil {
			t.Fatal(err)
		}
	}
	page, err = service.List(ctx, Filter{Resource: "key", Outcome: "success"})
	if err != nil || len(page.Events) != 50 || page.NextCursor == 0 {
		t.Fatal("pagination", err)
	}
	if page.Events[0].ActorName != "synthetic-admin" || page.Events[0].Action != "key.update" {
		t.Fatal("missing actor snapshot")
	}
	next, err := service.List(ctx, Filter{BeforeID: page.NextCursor})
	if err != nil || len(next.Events) != 2 {
		t.Fatal("cursor lost rows", err)
	}
	if err := service.Failure(ctx, "group.update", "group", "3", 403); err != nil {
		t.Fatal(err)
	}
	page, err = service.List(ctx, Filter{Outcome: "failure"})
	if err != nil || len(page.Events) != 1 || page.Events[0].HTTPStatus == nil || *page.Events[0].HTTPStatus != 403 {
		t.Fatal("failed attempt missing", err)
	}
	if err := Record(ctx, q, "arbitrary-secret", "key", "12"); err == nil {
		t.Fatal("unknown action accepted")
	}
	if err := Record(ctx, q, "key.update", "key", "sl_secret"); err == nil {
		t.Fatal("unsafe identifier accepted")
	}
	if _, err := connection.Exec("UPDATE audit_events SET created_at=1"); err != nil {
		t.Fatal(err)
	}
	page, err = service.List(ctx, Filter{})
	if err != nil || len(page.Events) != 0 {
		t.Fatal("expired audit retained", err)
	}
}

func TestAuditCountLimitPreservesMonotonicCursor(t *testing.T) {
	ctx := WithActor(context.Background(), Actor{TenantID: 1, ID: 1, Username: "synthetic-admin", Role: "admin", Source: "user"})
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	seedAuditTenant(t, connection, 1)
	_, err = connection.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<10001)
 INSERT INTO audit_events(actor_id,actor_name,actor_role,source,action,resource,resource_id,outcome,created_at)
 SELECT 1,'synthetic-admin','admin','user','key.create','key',CAST(x AS TEXT),'success',unixepoch() FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	if err := Record(ctx, db.New(connection), "key.update", "key", "2"); err != nil {
		t.Fatal(err)
	}
	var count, smallest, largest int64
	if err := connection.QueryRow("SELECT count(*),min(id),max(id) FROM audit_events").Scan(&count, &smallest, &largest); err != nil {
		t.Fatal(err)
	}
	if count != 10000 || smallest != 3 || largest != 10002 {
		t.Fatal("audit capacity or cursor changed", count, smallest, largest)
	}
}
