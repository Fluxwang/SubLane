-- Widen limits without rebuilding resource tables or changing saved policies.
ALTER TABLE accounts RENAME COLUMN max_concurrency TO previous_max_concurrency;
ALTER TABLE accounts ADD COLUMN max_concurrency INTEGER NOT NULL DEFAULT 30 CHECK(max_concurrency BETWEEN 1 AND 1000);
UPDATE accounts SET max_concurrency=previous_max_concurrency;
ALTER TABLE accounts DROP COLUMN previous_max_concurrency;

ALTER TABLE memberships RENAME COLUMN max_concurrency TO previous_max_concurrency;
ALTER TABLE memberships ADD COLUMN max_concurrency INTEGER NOT NULL DEFAULT 10 CHECK(max_concurrency BETWEEN 0 AND 1000);
UPDATE memberships SET max_concurrency=previous_max_concurrency;
ALTER TABLE memberships DROP COLUMN previous_max_concurrency;

ALTER TABLE account_model_runtime ADD COLUMN reason TEXT NOT NULL DEFAULT 'rate_limited';
ALTER TABLE account_model_runtime ADD COLUMN last_failure_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE account_model_runtime ADD COLUMN failure_window_at INTEGER NOT NULL DEFAULT 0;

-- Legacy transient penalties had no model attribution. Preserve actual rate limits.
UPDATE account_runtime SET cooldown_until=0,failures=0,reason='',last_failure_at=0 WHERE reason IN ('timeout','upstream_error');
