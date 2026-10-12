-- name: GetClientVersionState :one
SELECT value FROM settings WHERE key = sqlc.arg(key);

-- name: SaveClientVersionState :exec
INSERT INTO settings(key,value) VALUES(sqlc.arg(key),sqlc.arg(value))
ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP;
