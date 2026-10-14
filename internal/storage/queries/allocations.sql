-- name: ListAllocationSchemes :many
SELECT s.*,g.name AS group_name FROM allocation_schemes s
JOIN account_groups g ON g.id=s.group_id WHERE g.tenant_id=sqlc.arg(tenant_id) ORDER BY s.id;

-- name: ListAllocationPoolPolicies :many
SELECT g.id AS group_id,g.restricted_models
FROM allocation_schemes s JOIN account_groups g ON g.id=s.group_id
WHERE g.tenant_id=sqlc.arg(tenant_id) ORDER BY g.id;

-- name: ListAllocationPoolModels :many
SELECT gm.group_id,gm.model FROM group_models gm
JOIN allocation_schemes s ON s.group_id=gm.group_id
JOIN account_groups g ON g.id=s.group_id
WHERE g.tenant_id=sqlc.arg(tenant_id) ORDER BY gm.group_id,gm.model;

-- name: ListAllocationPoolCatalogs :many
SELECT ga.group_id,a.id,a.provider,a.models_snapshot,a.models_revision
FROM group_accounts ga JOIN allocation_schemes s ON s.group_id=ga.group_id
JOIN account_groups g ON g.id=s.group_id JOIN accounts a ON a.id=ga.account_id
WHERE g.tenant_id=sqlc.arg(tenant_id) AND a.enabled=1 AND a.status!='reauth_required'
ORDER BY ga.group_id,a.id;

-- name: GetAllocationScheme :one
SELECT s.*,g.name AS group_name FROM allocation_schemes s
JOIN account_groups g ON g.id=s.group_id WHERE s.id=?;
-- name: GetTenantAllocationScheme :one
SELECT s.*,g.name AS group_name FROM allocation_schemes s
JOIN account_groups g ON g.id=s.group_id WHERE s.id=sqlc.arg(id) AND g.tenant_id=sqlc.arg(tenant_id);
-- name: GetPoolAllocation :one
SELECT id FROM allocation_schemes WHERE group_id=?;
-- name: CreateAllocationScheme :execlastid
INSERT INTO allocation_schemes(name,group_id,enabled,created_at) VALUES(?,?,?,?);
-- name: UpdateAllocationScheme :exec
UPDATE allocation_schemes SET name=?,enabled=? WHERE id=?;
-- name: CurrentAllocationRevision :one
SELECT * FROM allocation_revisions WHERE scheme_id=? AND effective_at<=? ORDER BY effective_at DESC LIMIT 1;
-- name: NextAllocationRevision :one
SELECT * FROM allocation_revisions WHERE scheme_id=? AND effective_at>? ORDER BY effective_at LIMIT 1;
-- name: GetAllocationRevision :one
SELECT * FROM allocation_revisions WHERE id=?;
-- name: SaveAllocationRevision :execlastid
INSERT INTO allocation_revisions(scheme_id,effective_at,config) VALUES(?,?,?);
-- name: DeleteNextAllocationRevisions :exec
DELETE FROM allocation_revisions WHERE scheme_id=? AND effective_at>?;
-- name: AllocationPoolAccounts :many
SELECT a.id,a.provider,a.models_revision,(SELECT count(*) FROM group_accounts other WHERE other.account_id=a.id AND other.group_id<>ga.group_id) AS shared
FROM group_accounts ga JOIN accounts a ON a.id=ga.account_id WHERE ga.group_id=? ORDER BY a.id;
-- name: CanUseAllocation :one
SELECT EXISTS(SELECT 1 FROM allocation_schemes s
JOIN effective_group_access access ON access.group_id=s.group_id
WHERE s.id=? AND access.user_id=? AND s.enabled=1);
-- name: BindKeyAllocation :exec
INSERT INTO allocation_keys(key_id,scheme_id) VALUES(?,?);
-- name: GetKeyAllocation :one
SELECT scheme_id FROM allocation_keys WHERE key_id=?;
-- name: BeginAllocationEntry :exec
INSERT INTO allocation_entries(request_id,scheme_id,revision_id,user_id,account_id,model,mode,window_start,reset_at,started_at,state)
VALUES(?,?,?,?,?,?,?,?,?,?,'active');
-- name: GetAllocationEntry :one
SELECT * FROM allocation_entries WHERE request_id=?;
-- name: FinishAllocationEntry :exec
UPDATE allocation_entries SET finished_at=?,state=?,input_tokens=?,output_tokens=?,cached_tokens=?,cost=?,manual=? WHERE request_id=?;
-- name: RecoverAllocationEntries :exec
UPDATE allocation_entries SET state='pending' WHERE state='active' AND EXISTS(
SELECT 1 FROM allocation_schemes s JOIN account_groups g ON g.id=s.group_id
WHERE s.id=allocation_entries.scheme_id AND g.tenant_id=sqlc.arg(tenant_id));
-- name: AllocationMemberUsage :one
-- A time-zone change reassigns existing requests by their start instant, regardless of their stored original window.
SELECT CAST(COALESCE(sum(cost),0) AS INTEGER) AS used,CAST(COALESCE(sum(input_tokens+output_tokens),0) AS INTEGER) AS tokens
FROM allocation_entries WHERE scheme_id=sqlc.arg(scheme_id) AND user_id=sqlc.arg(user_id)
AND started_at>=sqlc.arg(started_from) AND started_at<sqlc.arg(started_to) AND mode=sqlc.arg(mode) AND state='settled';
-- name: AllocationMemberPending :one
SELECT count(*) FROM allocation_entries WHERE scheme_id=? AND user_id=? AND state='pending';
-- name: AllocationMemberExposure :one
SELECT CAST(COALESCE(SUM(CASE WHEN state='active' THEN 1 ELSE 0 END),0) AS INTEGER) AS active,
CAST(COALESCE(SUM(CASE WHEN state='pending' THEN 1 ELSE 0 END),0) AS INTEGER) AS pending
FROM allocation_entries WHERE scheme_id=sqlc.arg(scheme_id) AND user_id=sqlc.arg(user_id)
AND started_at>=sqlc.arg(started_from) AND started_at<sqlc.arg(started_to)
AND mode=sqlc.arg(mode) AND state IN ('active','pending');
-- name: ListAllocationPending :many
SELECT * FROM allocation_entries WHERE scheme_id=sqlc.arg(scheme_id) AND (sqlc.arg(user_id)=0 OR user_id=sqlc.arg(user_id)) AND state='pending' ORDER BY started_at DESC,request_id DESC LIMIT 256;
-- name: PruneAllocations :execrows
DELETE FROM allocation_entries WHERE rowid IN (
 SELECT old.rowid FROM allocation_entries old WHERE old.state='settled' AND old.reset_at<sqlc.arg(before) LIMIT 500
);
-- name: AccountHasAllocation :one
SELECT EXISTS(SELECT 1 FROM group_accounts ga JOIN allocation_schemes s ON s.group_id=ga.group_id WHERE ga.account_id=?);
-- name: CountActiveAllocationEntries :one
SELECT count(*) FROM allocation_entries WHERE scheme_id=? AND state='active';
-- name: UnbindSchemeKeys :exec
DELETE FROM allocation_keys WHERE scheme_id=?;
-- name: DeleteAllocationEntries :exec
DELETE FROM allocation_entries WHERE scheme_id=?;
-- name: DeleteAllocationRevisions :exec
DELETE FROM allocation_revisions WHERE scheme_id=?;
-- name: DeleteAllocationScheme :exec
DELETE FROM allocation_schemes WHERE id=?;
