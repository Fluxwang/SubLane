-- name: ListTenantMembers :many
SELECT u.id,u.username,m.role,m.enabled,u.enabled AS user_enabled,m.created_at
FROM memberships m JOIN users u ON u.id=m.user_id
WHERE m.tenant_id=sqlc.arg(tenant_id) AND m.role IN ('owner','member','admin')
AND (u.id<sqlc.arg(before_id) OR sqlc.arg(before_id)=0)
ORDER BY u.id DESC LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountTenantMembers :one
SELECT COUNT(*) FROM memberships m JOIN users u ON u.id=m.user_id
WHERE m.tenant_id=sqlc.arg(tenant_id) AND m.role IN ('owner','member','admin');

-- name: CanManageTenant :one
SELECT EXISTS(SELECT 1 FROM memberships m JOIN tenants t ON t.id=m.tenant_id JOIN users u ON u.id=m.user_id
WHERE m.tenant_id=sqlc.arg(tenant_id) AND m.user_id=sqlc.arg(actor_id)
AND m.role IN ('owner','admin') AND m.enabled=1 AND u.enabled=1 AND t.status='active');

-- name: SetTenantMemberEnabled :execrows
UPDATE memberships SET enabled=sqlc.arg(enabled)
WHERE tenant_id=sqlc.arg(tenant_id) AND user_id=sqlc.arg(user_id) AND role='member';

-- name: UpdateTenantMemberRole :exec
UPDATE memberships SET role=sqlc.arg(role)
WHERE tenant_id=sqlc.arg(tenant_id) AND user_id=sqlc.arg(user_id) AND role IN ('member','admin');

-- name: GetTenantMember :one
SELECT u.id,u.username,m.role,m.enabled,u.enabled AS user_enabled,m.created_at
FROM memberships m JOIN users u ON u.id=m.user_id
WHERE m.tenant_id=sqlc.arg(tenant_id) AND m.user_id=sqlc.arg(user_id);
