-- name: SetDemoCoverage :exec
-- Only the disposable demo runtime backdates its synthetic usage coverage.
UPDATE settings SET value = sqlc.arg(started_at)
WHERE key IN ('usage.daily.started_at', 'usage.hourly.started_at', 'usage.keys.started_at');
