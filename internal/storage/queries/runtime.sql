-- name: ListAccountRuntime :many
SELECT a.id,a.max_concurrency,COALESCE(r.cooldown_until,0) AS cooldown_until,COALESCE(r.reason,'') AS reason,COALESCE(r.failures,0) AS failures,COALESCE(r.last_failure_at,0) AS last_failure_at,COALESCE(r.revision,0) AS revision,
 a.models_revision AS lifecycle,
 (SELECT COUNT(*) FROM account_model_runtime m WHERE m.account_id=a.id AND m.lifecycle=a.models_revision AND m.runtime_revision=COALESCE(r.revision,0) AND m.cooldown_until>0) AS limited_models
FROM accounts a LEFT JOIN account_runtime r ON r.account_id=a.id WHERE a.tenant_id=sqlc.arg(tenant_id);

-- name: SaveAccountRuntime :exec
INSERT INTO account_runtime(account_id,cooldown_until,reason,failures,last_failure_at,revision)
SELECT id,sqlc.arg(cooldown_until),sqlc.arg(reason),sqlc.arg(failures),sqlc.arg(last_failure_at),sqlc.arg(revision) FROM accounts
WHERE id=sqlc.arg(account_id) AND tenant_id=sqlc.arg(tenant_id)
ON CONFLICT(account_id) DO UPDATE SET cooldown_until=excluded.cooldown_until,reason=excluded.reason,failures=excluded.failures,last_failure_at=excluded.last_failure_at,revision=excluded.revision;

-- name: SetAccountConcurrency :execrows
UPDATE accounts SET max_concurrency=sqlc.arg(max_concurrency),updated_at=sqlc.arg(now)
WHERE id=sqlc.arg(id) AND tenant_id=sqlc.arg(tenant_id);
