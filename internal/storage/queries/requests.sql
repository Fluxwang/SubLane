-- name: RecordRequest :exec
INSERT INTO request_records(user_id,key_id,group_id,account_id,provider,model,transport,operation,started_at,duration_ms,outcome,error_code,upstream_status,input_tokens,output_tokens,cached_tokens,request_id,first_token_ms,reasoning_effort,content_mode,content_revision,content_rule_ids,content_check_failed)
VALUES(sqlc.arg(user_id),sqlc.arg(key_id),sqlc.arg(group_id),sqlc.arg(account_id),sqlc.arg(provider),sqlc.arg(model),sqlc.arg(transport),sqlc.arg(operation),sqlc.arg(started_at),sqlc.arg(duration_ms),sqlc.arg(outcome),sqlc.arg(error_code),sqlc.narg(upstream_status),sqlc.narg(input_tokens),sqlc.narg(output_tokens),sqlc.narg(cached_tokens),sqlc.arg(request_id),sqlc.narg(first_token_ms),sqlc.arg(reasoning_effort),sqlc.arg(content_mode),sqlc.arg(content_revision),COALESCE(NULLIF(sqlc.arg(content_rule_ids),''),'[]'),sqlc.arg(content_check_failed));

-- name: RequestRecorded :one
SELECT EXISTS(SELECT 1 FROM request_records WHERE request_id=?);

-- name: PruneRequests :execrows
DELETE FROM request_records WHERE id IN (
 SELECT r.id FROM request_records r
 WHERE r.started_at<sqlc.arg(before_time)
 OR r.id<=(SELECT newest.id FROM request_records newest ORDER BY newest.id DESC LIMIT 1 OFFSET 5000)
 LIMIT 500
);

-- name: ListRequests :many
SELECT r.*,COALESCE(u.username,'') AS username,COALESCE(k.name,'') AS key_name,COALESCE(g.name,'') AS group_name,COALESCE(a.name,'') AS account_name
FROM request_records r LEFT JOIN users u ON u.id=r.user_id LEFT JOIN api_keys k ON k.id=r.key_id LEFT JOIN account_groups g ON g.id=r.group_id LEFT JOIN accounts a ON a.id=r.account_id
WHERE g.tenant_id=sqlc.arg(tenant_id)
 AND (r.user_id=sqlc.arg(user_id) OR sqlc.arg(user_id)=0) AND (r.id<sqlc.arg(cursor) OR sqlc.arg(cursor)=0) AND (r.account_id=sqlc.arg(account_id) OR sqlc.arg(account_id)='') AND (r.outcome=sqlc.arg(outcome) OR sqlc.arg(outcome)='') AND r.started_at>=sqlc.arg(since)
 AND (r.started_at<sqlc.arg(until_time) OR sqlc.arg(until_time)=0)
 AND (r.user_id=sqlc.arg(member_id) OR sqlc.arg(member_id)=0)
 AND (r.key_id=sqlc.arg(key_id) OR sqlc.arg(key_id)=0)
 AND (r.model=sqlc.arg(model) OR sqlc.arg(model)='')
 AND (r.request_id=sqlc.arg(request_id) OR sqlc.arg(request_id)='')
ORDER BY r.id DESC LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountRequests :one
-- Match ListRequests filters before pagination so totals never escape the caller's scope.
SELECT COUNT(*) FROM request_records r JOIN account_groups g ON g.id=r.group_id
WHERE g.tenant_id=sqlc.arg(tenant_id)
 AND (r.user_id=sqlc.arg(user_id) OR sqlc.arg(user_id)=0)
 AND (r.account_id=sqlc.arg(account_id) OR sqlc.arg(account_id)='')
 AND (r.outcome=sqlc.arg(outcome) OR sqlc.arg(outcome)='')
 AND r.started_at>=sqlc.arg(since)
 AND (r.started_at<sqlc.arg(until_time) OR sqlc.arg(until_time)=0)
 AND (r.user_id=sqlc.arg(member_id) OR sqlc.arg(member_id)=0)
 AND (r.key_id=sqlc.arg(key_id) OR sqlc.arg(key_id)=0)
 AND (r.model=sqlc.arg(model) OR sqlc.arg(model)='')
 AND (r.request_id=sqlc.arg(request_id) OR sqlc.arg(request_id)='');

-- name: ListRequestCallers :many
SELECT r.user_id, r.key_id, COALESCE(u.username,'') AS username, COALESCE(k.name,'') AS key_name
FROM request_records r
LEFT JOIN users u ON u.id=r.user_id
LEFT JOIN api_keys k ON k.id=r.key_id
JOIN account_groups g ON g.id=r.group_id
WHERE g.tenant_id=sqlc.arg(tenant_id)
AND (r.user_id=sqlc.arg(user_id) OR sqlc.arg(user_id)=0) AND r.started_at>=sqlc.arg(since)
GROUP BY r.user_id,r.key_id ORDER BY username,key_name,r.key_id LIMIT 5000;
