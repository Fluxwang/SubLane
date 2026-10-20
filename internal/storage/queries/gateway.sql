-- name: CountAccountAffinity :one
SELECT count(*) FROM account_affinity;

-- name: PruneAccountAffinity :exec
DELETE FROM account_affinity WHERE expires_at <= sqlc.arg(now);

-- name: CreateAccountAffinity :exec
INSERT INTO account_affinity(user_id, group_id, session_hash, provider, account_id, expires_at, model) VALUES (sqlc.arg(user_id), sqlc.arg(group_id), sqlc.arg(session_hash), sqlc.arg(provider), sqlc.arg(account_id), sqlc.arg(expires_at), sqlc.arg(model));

-- name: TouchAccountAffinity :exec
UPDATE account_affinity SET expires_at = sqlc.arg(expires_at), model = sqlc.arg(model)
WHERE user_id = sqlc.arg(user_id) AND group_id = sqlc.arg(group_id) AND session_hash = sqlc.arg(session_hash) AND provider = sqlc.arg(provider)
AND (expires_at < sqlc.arg(threshold) OR model != sqlc.arg(model));

-- name: ReplaceAccountAffinity :exec
UPDATE account_affinity SET account_id = sqlc.arg(account_id), model = sqlc.arg(model), expires_at = sqlc.arg(expires_at)
WHERE user_id = sqlc.arg(user_id) AND group_id = sqlc.arg(group_id) AND session_hash = sqlc.arg(session_hash) AND provider = sqlc.arg(provider);

-- name: ListSessionAffinities :many
SELECT provider, account_id, expires_at, model FROM account_affinity
WHERE user_id = sqlc.arg(user_id) AND group_id = sqlc.arg(group_id)
AND session_hash = sqlc.arg(session_hash) AND expires_at > sqlc.arg(now)
ORDER BY provider;
