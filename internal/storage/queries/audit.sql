-- name: CreateAuditEvent :exec
INSERT INTO audit_events(tenant_id,actor_id,actor_name,actor_role,source,action,resource,resource_id,outcome,http_status,created_at)
VALUES(sqlc.arg(tenant_id),sqlc.arg(actor_id),sqlc.arg(actor_name),sqlc.arg(actor_role),sqlc.arg(source),sqlc.arg(action),sqlc.arg(resource),sqlc.arg(resource_id),sqlc.arg(outcome),sqlc.narg(http_status),sqlc.arg(created_at));

-- name: PruneAuditEvents :exec
DELETE FROM audit_events AS current_event WHERE current_event.tenant_id=sqlc.arg(scope_tenant_id)
AND (current_event.created_at<sqlc.arg(oldest) OR current_event.id IN (
SELECT old_event.id FROM audit_events AS old_event WHERE old_event.tenant_id=sqlc.arg(scope_tenant_id)
ORDER BY old_event.id DESC LIMIT -1 OFFSET 10000));

-- name: ListAuditEvents :many
SELECT * FROM audit_events
WHERE tenant_id=sqlc.arg(tenant_id) AND (id<sqlc.arg(before_id) OR sqlc.arg(before_id)=0)
AND (resource=sqlc.arg(resource) OR sqlc.arg(resource)='')
AND (outcome=sqlc.arg(outcome) OR sqlc.arg(outcome)='')
ORDER BY id DESC LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountAuditEvents :one
SELECT COUNT(*) FROM audit_events
WHERE tenant_id=sqlc.arg(tenant_id)
AND (resource=sqlc.arg(resource) OR sqlc.arg(resource)='')
AND (outcome=sqlc.arg(outcome) OR sqlc.arg(outcome)='');
