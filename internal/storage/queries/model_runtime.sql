-- name: GetAccountModelRuntime :one
SELECT a.models_revision AS lifecycle, COALESCE(ar.revision,0) AS runtime_revision,
       COALESCE(m.cooldown_until,0) AS cooldown_until, COALESCE(m.failures,0) AS failures, COALESCE(m.version,0) AS version,
       COALESCE(m.reason,'') AS reason, COALESCE(m.last_failure_at,0) AS last_failure_at,
       COALESCE(m.failure_window_at,0) AS failure_window_at
FROM accounts a LEFT JOIN account_runtime ar ON ar.account_id=a.id
LEFT JOIN account_model_runtime m ON m.account_id=a.id AND m.model=sqlc.arg(model)
 AND m.lifecycle=a.models_revision AND m.runtime_revision=COALESCE(ar.revision,0)
WHERE a.id=sqlc.arg(target_id) AND a.tenant_id=sqlc.arg(workspace_id);

-- name: ListAvailabilityModelLimits :many
SELECT m.model,m.cooldown_until,m.failures FROM account_model_runtime m
JOIN accounts a ON a.id=m.account_id LEFT JOIN account_runtime ar ON ar.account_id=a.id
WHERE a.id=sqlc.arg(target_id) AND a.tenant_id=sqlc.arg(workspace_id)
 AND m.lifecycle=a.models_revision AND m.runtime_revision=COALESCE(ar.revision,0);

-- name: SaveAccountModelRuntime :one
INSERT INTO account_model_runtime(account_id,model,cooldown_until,failures,lifecycle,runtime_revision,version,reason,last_failure_at,failure_window_at)
SELECT a.id,sqlc.arg(model),sqlc.arg(cooldown_until),sqlc.arg(failures),a.models_revision,sqlc.arg(runtime_revision),1,sqlc.arg(reason),sqlc.arg(last_failure_at),sqlc.arg(failure_window_at)
FROM accounts a
WHERE a.id=sqlc.arg(target_id) AND a.tenant_id=sqlc.arg(workspace_id)
 AND a.models_revision=sqlc.arg(lifecycle)
 AND COALESCE((SELECT revision FROM account_runtime WHERE account_runtime.account_id=a.id),0)=sqlc.arg(runtime_revision)
ON CONFLICT(account_id,model) DO UPDATE SET cooldown_until=excluded.cooldown_until, failures=excluded.failures,
 lifecycle=excluded.lifecycle,runtime_revision=excluded.runtime_revision,version=account_model_runtime.version+1,
 reason=excluded.reason,last_failure_at=excluded.last_failure_at,failure_window_at=excluded.failure_window_at
RETURNING version;

-- name: DeleteAccountModelLimit :exec
DELETE FROM account_model_runtime WHERE account_model_runtime.account_id=sqlc.arg(target_id) AND model=sqlc.arg(model)
 AND version=sqlc.arg(version) AND lifecycle=sqlc.arg(lifecycle) AND runtime_revision=sqlc.arg(runtime_revision)
 AND EXISTS(SELECT 1 FROM accounts a LEFT JOIN account_runtime ar ON ar.account_id=a.id
  WHERE a.id=account_model_runtime.account_id AND a.tenant_id=sqlc.arg(workspace_id)
   AND a.models_revision=account_model_runtime.lifecycle AND COALESCE(ar.revision,0)=account_model_runtime.runtime_revision);

-- name: DeleteAccountModelRuntime :exec
DELETE FROM account_model_runtime WHERE account_model_runtime.account_id=sqlc.arg(target_id)
 AND EXISTS(SELECT 1 FROM accounts a WHERE a.id=account_model_runtime.account_id AND a.tenant_id=sqlc.arg(workspace_id));

-- name: DeleteObsoleteModelRuntime :exec
DELETE FROM account_model_runtime
WHERE EXISTS(SELECT 1 FROM accounts a LEFT JOIN account_runtime ar ON ar.account_id=a.id
 WHERE a.id=account_model_runtime.account_id AND a.tenant_id=sqlc.arg(workspace_id)
  AND (a.models_revision!=account_model_runtime.lifecycle
   OR COALESCE(ar.revision,0)!=account_model_runtime.runtime_revision
   OR (account_model_runtime.cooldown_until<=sqlc.arg(now) AND NOT EXISTS(SELECT 1 FROM json_each(CASE WHEN json_valid(a.models_snapshot) THEN a.models_snapshot ELSE '{}' END,'$.models') j
    WHERE j.value=account_model_runtime.model))));
