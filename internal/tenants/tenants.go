package tenants

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/murongg/SubLane/internal/audit"
	"github.com/murongg/SubLane/internal/pagination"
	storageDB "github.com/murongg/SubLane/internal/storage/db"
)

var (
	ErrForbidden      = errors.New("tenant_forbidden")
	ErrInput          = errors.New("tenant_invalid_input")
	ErrNotFound       = errors.New("tenant_member_not_found")
	ErrAlreadyMember  = errors.New("tenant_member_exists")
	ErrWorkspaceLimit = errors.New("workspace_limit_reached")
)

const MaxOwnedWorkspaces = 10

type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

type Tenant struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type Member struct {
	TenantID int64 `json:"tenant_id"`
	UserID   int64 `json:"user_id"`
	Role     Role  `json:"role"`
}

type MemberSummary struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Role      Role   `json:"role"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"created_at"`
}

type MemberPage struct {
	pagination.Metadata
	Members    []MemberSummary `json:"members"`
	NextCursor int64           `json:"next_cursor"`
}

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

func (s *Service) ListMembers(ctx context.Context, tenantID, beforeID int64) (MemberPage, error) {
	return s.ListMembersPage(ctx, tenantID, pagination.Input{Cursor: beforeID})
}

func (s *Service) ListMembersPage(ctx context.Context, tenantID int64, bounds pagination.Input) (MemberPage, error) {
	page := MemberPage{Members: []MemberSummary{}}
	if !bounds.Valid() {
		return page, ErrInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	q := storageDB.New(tx)
	total, err := q.CountTenantMembers(ctx, tenantID)
	if err != nil {
		return page, err
	}
	page.Metadata = bounds.Resolve(total)
	rows, err := q.ListTenantMembers(ctx, storageDB.ListTenantMembersParams{TenantID: tenantID, BeforeID: bounds.Cursor, PageLimit: page.PageSize + 1, PageOffset: page.Offset()})
	if err != nil {
		return page, err
	}
	for _, row := range rows {
		page.Members = append(page.Members, MemberSummary{ID: row.ID, Username: row.Username,
			Role: Role(row.Role), Enabled: row.Enabled == 1 && row.UserEnabled, CreatedAt: row.CreatedAt})
	}
	if int64(len(page.Members)) > page.PageSize {
		page.Members = page.Members[:page.PageSize]
		page.NextCursor = page.Members[len(page.Members)-1].ID
	}
	return page, tx.Commit()
}

func (s *Service) SetMemberEnabled(ctx context.Context, actorID, tenantID, userID int64, enabled bool) (MemberSummary, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MemberSummary{}, err
	}
	defer tx.Rollback()
	q := storageDB.New(tx)
	allowed, err := q.CanManageTenant(ctx, storageDB.CanManageTenantParams{TenantID: tenantID, ActorID: actorID})
	if err != nil {
		return MemberSummary{}, err
	}
	if !allowed {
		return MemberSummary{}, ErrForbidden
	}
	flag := int64(0)
	if enabled {
		flag = 1
	}
	updated, err := q.SetTenantMemberEnabled(ctx, storageDB.SetTenantMemberEnabledParams{TenantID: tenantID, UserID: userID, Enabled: flag})
	if err != nil {
		return MemberSummary{}, err
	}
	if updated != 1 {
		return MemberSummary{}, ErrNotFound
	}
	row, err := q.GetTenantMember(ctx, storageDB.GetTenantMemberParams{TenantID: tenantID, UserID: userID})
	if err != nil {
		return MemberSummary{}, err
	}
	if err := audit.Record(ctx, q, "member.update", "member", audit.ID(userID)); err != nil {
		return MemberSummary{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemberSummary{}, err
	}
	return MemberSummary{ID: row.ID, Username: row.Username, Role: Role(row.Role),
		Enabled: row.Enabled == 1 && row.UserEnabled, CreatedAt: row.CreatedAt}, nil
}

func (s *Service) AllIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM tenants ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func CreateInitial(ctx context.Context, tx *sql.Tx, ownerUserID, now int64, name string) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO tenants(id,name,owner_user_id,created_at)
		VALUES(1,?,?,?)`, name, ownerUserID, now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO memberships(tenant_id,user_id,role,created_at)
		VALUES(1,?,'owner',?)`, ownerUserID, now)
	return err
}

func AddNewMember(ctx context.Context, tx *sql.Tx, tenantID, userID, now int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO memberships(tenant_id,user_id,role,created_at)
		VALUES(?,?,'member',?)`, tenantID, userID, now)
	return err
}

func (s *Service) List(ctx context.Context, userID int64) ([]Tenant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id,t.name,t.status FROM tenants t
		JOIN memberships m ON m.tenant_id=t.id JOIN users u ON u.id=m.user_id
		WHERE m.user_id=? AND m.enabled=1 AND u.enabled=1 ORDER BY t.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Tenant{}
	for rows.Next() {
		var tenant Tenant
		if err := rows.Scan(&tenant.ID, &tenant.Name, &tenant.Status); err != nil {
			return nil, err
		}
		result = append(result, tenant)
	}
	return result, rows.Err()
}

func (s *Service) Create(ctx context.Context, ownerUserID int64, name string) (Tenant, error) {
	var err error
	name, err = NormalizeName(name)
	if err != nil {
		return Tenant{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Tenant{}, err
	}
	defer tx.Rollback()
	var enabled bool
	if err := tx.QueryRowContext(ctx, "SELECT enabled FROM users WHERE id=?", ownerUserID).Scan(&enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Tenant{}, ErrForbidden
		}
		return Tenant{}, err
	}
	if !enabled {
		return Tenant{}, ErrForbidden
	}
	var owned int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM tenants WHERE owner_user_id=?", ownerUserID).Scan(&owned); err != nil {
		return Tenant{}, err
	}
	if owned >= MaxOwnedWorkspaces {
		return Tenant{}, ErrWorkspaceLimit
	}
	now := time.Now().Unix()
	result, err := tx.ExecContext(ctx, "INSERT INTO tenants(name,owner_user_id,created_at) VALUES(?,?,?)", name, ownerUserID, now)
	if err != nil {
		return Tenant{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Tenant{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO memberships(tenant_id,user_id,role,created_at) VALUES(?,?,'owner',?)", id, ownerUserID, now); err != nil {
		return Tenant{}, err
	}
	if err := tx.Commit(); err != nil {
		return Tenant{}, err
	}
	return Tenant{ID: id, Name: name, Status: "active"}, nil
}

func NormalizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) == 0 || utf8.RuneCountInString(name) > 64 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", ErrInput
	}
	return name, nil
}

func (s *Service) AddMember(ctx context.Context, actorID, tenantID, userID int64, role Role) error {
	_, err := s.addMember(ctx, actorID, tenantID, userID, "", role)
	return err
}

func (s *Service) AddMemberByUsername(ctx context.Context, actorID, tenantID int64, username string, role Role) (MemberSummary, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return MemberSummary{}, ErrInput
	}
	return s.addMember(ctx, actorID, tenantID, 0, username, role)
}

func (s *Service) addMember(ctx context.Context, actorID, tenantID, userID int64, username string, role Role) (MemberSummary, error) {
	if role != RoleAdmin && role != RoleMember {
		return MemberSummary{}, ErrInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MemberSummary{}, err
	}
	defer tx.Rollback()
	q := storageDB.New(tx)
	var actorRole Role
	var ownerID int64
	err = tx.QueryRowContext(ctx, `SELECT m.role,t.owner_user_id FROM memberships m
		JOIN tenants t ON t.id=m.tenant_id JOIN users u ON u.id=m.user_id
		WHERE m.tenant_id=? AND m.user_id=? AND t.status='active' AND m.enabled=1 AND u.enabled=1`, tenantID, actorID).Scan(&actorRole, &ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return MemberSummary{}, ErrForbidden
	}
	if err != nil {
		return MemberSummary{}, err
	}
	if actorRole != RoleOwner && actorRole != RoleAdmin {
		return MemberSummary{}, ErrForbidden
	}
	if username != "" {
		if err := tx.QueryRowContext(ctx, "SELECT id FROM users WHERE username=? AND enabled=1", username).Scan(&userID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return MemberSummary{}, ErrInput
			}
			return MemberSummary{}, err
		}
	}
	// Owner transfer is a separate operation; ordinary member updates must never remove the only owner.
	if userID == ownerID {
		return MemberSummary{}, ErrForbidden
	}
	if username != "" {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM memberships WHERE tenant_id=? AND user_id=?)", tenantID, userID).Scan(&exists); err != nil {
			return MemberSummary{}, err
		}
		if exists {
			return MemberSummary{}, ErrAlreadyMember
		}
	}
	now := time.Now().Unix()
	result, err := tx.ExecContext(ctx, `INSERT INTO memberships(tenant_id,user_id,role,created_at)
		SELECT ?,id,?,? FROM users WHERE id=? AND enabled=1
		ON CONFLICT(tenant_id,user_id) DO UPDATE SET role=excluded.role,enabled=1`, tenantID, role, now, userID)
	if err != nil {
		return MemberSummary{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return MemberSummary{}, err
	}
	if affected == 0 {
		return MemberSummary{}, ErrInput
	}
	row, err := q.GetTenantMember(ctx, storageDB.GetTenantMemberParams{TenantID: tenantID, UserID: userID})
	if err != nil {
		return MemberSummary{}, err
	}
	if err := audit.Record(ctx, q, "member.update", "member", audit.ID(userID)); err != nil {
		return MemberSummary{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemberSummary{}, err
	}
	return MemberSummary{ID: row.ID, Username: row.Username, Role: Role(row.Role), Enabled: row.Enabled == 1 && row.UserEnabled, CreatedAt: row.CreatedAt}, nil
}

func (s *Service) SetMemberRole(ctx context.Context, actorID, tenantID, userID int64, role Role) (MemberSummary, error) {
	if role != RoleAdmin && role != RoleMember {
		return MemberSummary{}, ErrInput
	}
	if actorID == userID {
		return MemberSummary{}, ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MemberSummary{}, err
	}
	defer tx.Rollback()
	q := storageDB.New(tx)
	allowed, err := q.CanManageTenant(ctx, storageDB.CanManageTenantParams{TenantID: tenantID, ActorID: actorID})
	if err != nil {
		return MemberSummary{}, err
	}
	if !allowed {
		return MemberSummary{}, ErrForbidden
	}
	current, err := q.GetTenantMember(ctx, storageDB.GetTenantMemberParams{TenantID: tenantID, UserID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return MemberSummary{}, ErrNotFound
	}
	if err != nil {
		return MemberSummary{}, err
	}
	if current.Role == string(RoleOwner) {
		return MemberSummary{}, ErrForbidden
	}
	if role == RoleAdmin && (current.Enabled != 1 || !current.UserEnabled) {
		return MemberSummary{}, ErrInput
	}
	if err := q.UpdateTenantMemberRole(ctx, storageDB.UpdateTenantMemberRoleParams{TenantID: tenantID, UserID: userID, Role: string(role)}); err != nil {
		return MemberSummary{}, err
	}
	if err := audit.Record(ctx, q, "member.update", "member", audit.ID(userID)); err != nil {
		return MemberSummary{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemberSummary{}, err
	}
	return MemberSummary{ID: current.ID, Username: current.Username, Role: role, Enabled: current.Enabled == 1 && current.UserEnabled, CreatedAt: current.CreatedAt}, nil
}

func (s *Service) Membership(ctx context.Context, tenantID, userID int64) (Member, bool, error) {
	var member Member
	err := s.db.QueryRowContext(ctx, `SELECT m.tenant_id,m.user_id,m.role FROM memberships m
		JOIN tenants t ON t.id=m.tenant_id JOIN users u ON u.id=m.user_id
		WHERE m.tenant_id=? AND m.user_id=? AND t.status='active' AND m.enabled=1 AND u.enabled=1`, tenantID, userID).
		Scan(&member.TenantID, &member.UserID, &member.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return Member{}, false, nil
	}
	if err != nil {
		return Member{}, false, err
	}
	return member, true, nil
}

func (s *Service) Suspend(ctx context.Context, tenantID int64) error {
	result, err := s.db.ExecContext(ctx, "UPDATE tenants SET status='suspended' WHERE id=?", tenantID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrInput
	}
	return nil
}
