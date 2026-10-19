-- name: RecordStatistics :exec
INSERT INTO usage_daily(day,user_id,group_id,provider,model,requests,completed,incomplete,errors,canceled,rejected,duration_ms,input_tokens,output_tokens,cached_tokens,input_reported,output_reported,cached_reported)
VALUES(sqlc.arg(day),sqlc.arg(user_id),sqlc.arg(group_id),sqlc.arg(provider),sqlc.arg(model),sqlc.arg(requests),sqlc.arg(completed),sqlc.arg(incomplete),sqlc.arg(errors),sqlc.arg(canceled),sqlc.arg(rejected),sqlc.arg(duration_ms),sqlc.arg(input_tokens),sqlc.arg(output_tokens),sqlc.arg(cached_tokens),sqlc.arg(input_reported),sqlc.arg(output_reported),sqlc.arg(cached_reported))
ON CONFLICT(day,user_id,group_id,provider,model) DO UPDATE SET
requests=usage_daily.requests+excluded.requests,
completed=usage_daily.completed+excluded.completed,
incomplete=usage_daily.incomplete+excluded.incomplete,
errors=usage_daily.errors+excluded.errors,
canceled=usage_daily.canceled+excluded.canceled,
rejected=usage_daily.rejected+excluded.rejected,
duration_ms=usage_daily.duration_ms+excluded.duration_ms,
input_tokens=usage_daily.input_tokens+excluded.input_tokens,
output_tokens=usage_daily.output_tokens+excluded.output_tokens,
cached_tokens=usage_daily.cached_tokens+excluded.cached_tokens,
input_reported=usage_daily.input_reported+excluded.input_reported,
output_reported=usage_daily.output_reported+excluded.output_reported,
cached_reported=usage_daily.cached_reported+excluded.cached_reported;

-- name: GetStatisticsCoverage :one
SELECT value FROM settings WHERE key='usage.daily.started_at';

-- name: CanTrackStatisticsModel :one
SELECT CAST(
 EXISTS(SELECT 1 FROM usage_daily s JOIN account_groups g ON g.id=s.group_id
 WHERE g.tenant_id=(SELECT target.tenant_id FROM account_groups target WHERE target.id=sqlc.arg(group_id))
 AND s.day=sqlc.arg(day) AND s.user_id=sqlc.arg(user_id) AND s.model=sqlc.arg(model))
 OR (SELECT COUNT(DISTINCT s.model) FROM usage_daily s JOIN account_groups g ON g.id=s.group_id
 WHERE g.tenant_id=(SELECT target.tenant_id FROM account_groups target WHERE target.id=sqlc.arg(group_id))
 AND s.day=sqlc.arg(day) AND s.user_id=sqlc.arg(user_id) AND s.model!='[other models]')<64
AS INTEGER);

-- name: PruneStatistics :execrows
DELETE FROM usage_daily WHERE rowid IN (SELECT old.rowid FROM usage_daily old WHERE old.day<sqlc.arg(before_day) LIMIT 500);

-- name: GetStatisticsTotals :one
SELECT CAST(COALESCE(SUM(s.requests),0) AS INTEGER) AS requests,
CAST(COALESCE(SUM(s.completed),0) AS INTEGER) AS completed,
CAST(COALESCE(SUM(s.incomplete),0) AS INTEGER) AS incomplete,
CAST(COALESCE(SUM(s.errors),0) AS INTEGER) AS errors,
CAST(COALESCE(SUM(s.canceled),0) AS INTEGER) AS canceled,
CAST(COALESCE(SUM(s.rejected),0) AS INTEGER) AS rejected,
CAST(COALESCE(SUM(s.duration_ms),0) AS INTEGER) AS duration_ms,
CAST(COALESCE(SUM(s.input_tokens),0) AS INTEGER) AS input_tokens,
CAST(COALESCE(SUM(s.output_tokens),0) AS INTEGER) AS output_tokens,
CAST(COALESCE(SUM(s.cached_tokens),0) AS INTEGER) AS cached_tokens,
CAST(COALESCE(SUM(s.input_reported),0) AS INTEGER) AS input_reported,
CAST(COALESCE(SUM(s.output_reported),0) AS INTEGER) AS output_reported,
CAST(COALESCE(SUM(s.cached_reported),0) AS INTEGER) AS cached_reported
FROM usage_daily s JOIN account_groups g ON g.id=s.group_id
WHERE g.tenant_id=sqlc.arg(tenant_id) AND s.day>=sqlc.arg(from_day) AND s.day<sqlc.arg(to_day) AND (s.user_id=sqlc.arg(user_id) OR sqlc.arg(user_id)=0);

-- name: ListStatistics :many
SELECT CAST(CASE sqlc.arg(dimension) WHEN 'day' THEN s.day WHEN 'member' THEN s.user_id WHEN 'group' THEN s.group_id ELSE s.model END AS TEXT) AS bucket,
CAST(COALESCE(CASE sqlc.arg(dimension) WHEN 'day' THEN '' WHEN 'member' THEN u.username WHEN 'group' THEN g.name ELSE s.model END,'') AS TEXT) AS name,
CAST(COALESCE(SUM(s.requests),0) AS INTEGER) AS requests,
CAST(COALESCE(SUM(s.completed),0) AS INTEGER) AS completed,
CAST(COALESCE(SUM(s.incomplete),0) AS INTEGER) AS incomplete,
CAST(COALESCE(SUM(s.errors),0) AS INTEGER) AS errors,
CAST(COALESCE(SUM(s.canceled),0) AS INTEGER) AS canceled,
CAST(COALESCE(SUM(s.rejected),0) AS INTEGER) AS rejected,
CAST(COALESCE(SUM(s.duration_ms),0) AS INTEGER) AS duration_ms,
CAST(COALESCE(SUM(s.input_tokens),0) AS INTEGER) AS input_tokens,
CAST(COALESCE(SUM(s.output_tokens),0) AS INTEGER) AS output_tokens,
CAST(COALESCE(SUM(s.cached_tokens),0) AS INTEGER) AS cached_tokens,
CAST(COALESCE(SUM(s.input_reported),0) AS INTEGER) AS input_reported,
CAST(COALESCE(SUM(s.output_reported),0) AS INTEGER) AS output_reported,
CAST(COALESCE(SUM(s.cached_reported),0) AS INTEGER) AS cached_reported,
-- Usage rankings order models by reported tokens; other dimensions stay request-ordered.
CAST(COALESCE(CASE sqlc.arg(dimension) WHEN 'model' THEN SUM(s.input_tokens+s.output_tokens) ELSE SUM(s.requests) END,0) AS INTEGER) AS weight
FROM usage_daily s LEFT JOIN users u ON u.id=s.user_id JOIN account_groups g ON g.id=s.group_id
WHERE g.tenant_id=sqlc.arg(tenant_id) AND s.day>=sqlc.arg(from_day) AND s.day<sqlc.arg(to_day) AND (s.user_id=sqlc.arg(user_id) OR sqlc.arg(user_id)=0)
GROUP BY 1,2 ORDER BY weight DESC,requests DESC,bucket ASC LIMIT sqlc.arg(max_rows);

-- name: ListStatisticsCostBasis :many
SELECT s.day, s.model,
CAST(COALESCE(SUM(s.input_tokens),0) AS INTEGER) AS input_tokens,
CAST(COALESCE(SUM(s.output_tokens),0) AS INTEGER) AS output_tokens,
CAST(COALESCE(SUM(s.cached_tokens),0) AS INTEGER) AS cached_tokens,
CAST(COALESCE(SUM(s.input_reported),0) AS INTEGER) AS input_reported,
CAST(COALESCE(SUM(s.cached_reported),0) AS INTEGER) AS cached_reported
FROM usage_daily s JOIN account_groups g ON g.id=s.group_id
WHERE g.tenant_id=sqlc.arg(tenant_id) AND s.day>=sqlc.arg(from_day) AND s.day<sqlc.arg(to_day) AND (s.user_id=sqlc.arg(user_id) OR sqlc.arg(user_id)=0)
GROUP BY s.day, s.model;

-- name: RecordKeyStatistics :exec
INSERT INTO usage_daily_keys(day,key_id,requests,input_tokens,output_tokens,cached_tokens,input_reported,output_reported,cached_reported)
VALUES(sqlc.arg(day),sqlc.arg(key_id),sqlc.arg(requests),sqlc.arg(input_tokens),sqlc.arg(output_tokens),sqlc.arg(cached_tokens),sqlc.arg(input_reported),sqlc.arg(output_reported),sqlc.arg(cached_reported))
ON CONFLICT(day,key_id) DO UPDATE SET
requests=usage_daily_keys.requests+excluded.requests,
input_tokens=usage_daily_keys.input_tokens+excluded.input_tokens,
output_tokens=usage_daily_keys.output_tokens+excluded.output_tokens,
cached_tokens=usage_daily_keys.cached_tokens+excluded.cached_tokens,
input_reported=usage_daily_keys.input_reported+excluded.input_reported,
output_reported=usage_daily_keys.output_reported+excluded.output_reported,
cached_reported=usage_daily_keys.cached_reported+excluded.cached_reported;

-- name: GetKeyStatisticsCoverage :one
SELECT value FROM settings WHERE key='usage.keys.started_at';

-- name: PruneKeyStatistics :execrows
DELETE FROM usage_daily_keys WHERE rowid IN (SELECT old.rowid FROM usage_daily_keys old WHERE old.day<sqlc.arg(before_day) LIMIT 500);

-- name: ListKeyStatistics :many
SELECT k.id, k.name, CAST(k.revoked_at IS NOT NULL AS INTEGER) AS revoked, COALESCE(u.username,'') AS owner,
CAST(COALESCE(SUM(s.requests),0) AS INTEGER) AS requests,
CAST(COALESCE(SUM(s.input_tokens),0) AS INTEGER) AS input_tokens,
CAST(COALESCE(SUM(s.output_tokens),0) AS INTEGER) AS output_tokens,
CAST(COALESCE(SUM(s.cached_tokens),0) AS INTEGER) AS cached_tokens,
CAST(COALESCE(SUM(s.input_reported),0) AS INTEGER) AS input_reported,
CAST(COALESCE(SUM(s.output_reported),0) AS INTEGER) AS output_reported,
CAST(COALESCE(SUM(s.cached_reported),0) AS INTEGER) AS cached_reported
FROM usage_daily_keys s JOIN api_keys k ON k.id=s.key_id JOIN account_groups g ON g.id=k.group_id LEFT JOIN users u ON u.id=k.user_id
WHERE g.tenant_id=sqlc.arg(tenant_id) AND s.day>=sqlc.arg(from_day) AND s.day<sqlc.arg(to_day) AND (k.user_id=sqlc.arg(user_id) OR sqlc.arg(user_id)=0)
GROUP BY k.id ORDER BY SUM(s.input_tokens+s.output_tokens) DESC,requests DESC,k.id ASC LIMIT 20;
