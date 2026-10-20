package tenants

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/murongg/SubLane/internal/storage"
)

func TestCreateCapsOwnedWorkspacesAtTen(t *testing.T) {
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "synthetic.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "INSERT INTO users(id,username,role,password_hash,enabled,created_at) VALUES(2,'synthetic-owner','member','synthetic-hash',1,1)"); err != nil {
		t.Fatal(err)
	}
	service := New(connection)
	for i := 0; i < 10; i++ {
		if _, err := service.Create(ctx, 2, "Workspace "+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := service.List(ctx, 2)
	if err != nil || len(listed) != 10 {
		t.Fatalf("owned workspaces: count=%d err=%v", len(listed), err)
	}
	if _, err := service.Create(ctx, 2, "Eleventh workspace"); !errors.Is(err, ErrWorkspaceLimit) {
		t.Fatalf("eleventh workspace should be rejected: %v", err)
	}
}

func TestMembershipIsScopedToTenant(t *testing.T) {
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	for _, id := range []int64{2, 3} {
		_, err = connection.ExecContext(ctx,
			"INSERT INTO users(id,username,role,password_hash,enabled,created_at) VALUES(?,?,'member','synthetic-hash',1,1)",
			id, "member-"+strconv.FormatInt(id, 10))
		if err != nil {
			t.Fatal(err)
		}
	}
	service := New(connection)
	if _, err := service.Create(ctx, 2, "Bad\nname"); err != ErrInput {
		t.Fatalf("control character accepted in workspace name: %v", err)
	}
	if _, err := service.Create(ctx, 3, "这是一个长度合理的中文工作空间名称示例示例示例"); err != nil {
		t.Fatalf("valid non-ASCII workspace name rejected: %v", err)
	}
	alpha, err := service.Create(ctx, 2, "Alpha")
	if err != nil {
		t.Fatal(err)
	}
	beta, err := service.Create(ctx, 3, "Beta")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AddMember(ctx, 2, alpha.ID, 3, RoleMember); err != nil {
		t.Fatal(err)
	}
	if err := service.AddMember(ctx, 2, alpha.ID, 2, RoleMember); err != ErrForbidden {
		t.Fatalf("owner membership was changed through AddMember: %v", err)
	}
	if err := service.AddMember(ctx, 2, beta.ID, 2, RoleMember); err != ErrForbidden {
		t.Fatalf("non-member actor added a member across tenants: %v", err)
	}
	listed, err := service.List(ctx, 2)
	if err != nil || len(listed) != 1 || listed[0].ID != alpha.ID {
		t.Fatalf("workspace list leaked another tenant: %+v, %v", listed, err)
	}

	checks := []struct {
		tenant int64
		user   int64
		role   Role
		ok     bool
	}{
		{alpha.ID, 2, RoleOwner, true},
		{alpha.ID, 3, RoleMember, true},
		{beta.ID, 3, RoleOwner, true},
		{beta.ID, 2, "", false},
	}
	for _, check := range checks {
		member, ok, err := service.Membership(ctx, check.tenant, check.user)
		if err != nil {
			t.Fatal(err)
		}
		if ok != check.ok || member.Role != check.role {
			t.Fatalf("membership tenant=%d user=%d: got (%+v, %v), want (%q, %v)", check.tenant, check.user, member, ok, check.role, check.ok)
		}
	}
	if err := service.Suspend(ctx, alpha.ID); err != nil {
		t.Fatal(err)
	}
	_, ok, err := service.Membership(ctx, alpha.ID, 3)
	if err != nil || ok {
		t.Fatalf("suspended tenant should deny membership: ok=%v err=%v", ok, err)
	}
	_, ok, err = service.Membership(ctx, beta.ID, 3)
	if err != nil || !ok {
		t.Fatalf("suspending another tenant affected beta: ok=%v err=%v", ok, err)
	}
}

func TestMemberListIncludesOwnerWithoutAllowingOwnerChanges(t *testing.T) {
	ctx := context.Background()
	connection, err := storage.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	for _, id := range []int64{2, 3} {
		if _, err := connection.ExecContext(ctx,
			"INSERT INTO users(id,username,role,password_hash,enabled,created_at) VALUES(?,?,'member','synthetic-hash',1,1)",
			id, "synthetic-"+strconv.FormatInt(id, 10)); err != nil {
			t.Fatal(err)
		}
	}
	service := New(connection)
	workspace, err := service.Create(ctx, 2, "Synthetic workspace")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AddMember(ctx, 2, workspace.ID, 3, RoleMember); err != nil {
		t.Fatal(err)
	}
	page, err := service.ListMembers(ctx, workspace.ID, 0)
	if err != nil || len(page.Members) != 2 || page.Total != 2 || page.Members[0].ID != 3 || page.Members[1].ID != 2 || page.Members[1].Role != RoleOwner {
		t.Fatalf("workspace roster omitted owner: %+v, %v", page, err)
	}
	if _, err := service.SetMemberEnabled(ctx, 2, workspace.ID, 2, false); err != ErrNotFound {
		t.Fatalf("owner status changed from roster: %v", err)
	}
	if _, err := service.SetMemberRole(ctx, 2, workspace.ID, 3, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetMemberRole(ctx, 3, workspace.ID, 2, RoleMember); err != ErrForbidden {
		t.Fatalf("owner role changed from roster: %v", err)
	}
}
