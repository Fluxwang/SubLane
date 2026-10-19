-- Per-key daily usage for key usage rankings. Earlier usage is not backfilled: request history is pruned
-- after seven days, so a backfill could look complete while missing data. Like request history, rows carry
-- no foreign key: an aggregate must never abort the request's accounting transaction.
CREATE TABLE usage_daily_keys (
 day INTEGER NOT NULL,
 key_id INTEGER NOT NULL,
 requests INTEGER NOT NULL DEFAULT 0 CHECK(requests>=0),
 input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(input_tokens>=0),
 output_tokens INTEGER NOT NULL DEFAULT 0 CHECK(output_tokens>=0),
 cached_tokens INTEGER NOT NULL DEFAULT 0 CHECK(cached_tokens>=0),
 input_reported INTEGER NOT NULL DEFAULT 0 CHECK(input_reported>=0),
 output_reported INTEGER NOT NULL DEFAULT 0 CHECK(output_reported>=0),
 cached_reported INTEGER NOT NULL DEFAULT 0 CHECK(cached_reported>=0),
 PRIMARY KEY(day,key_id)
);
CREATE INDEX usage_daily_keys_key ON usage_daily_keys(key_id,day);
INSERT INTO settings(key,value) VALUES('usage.keys.started_at',CAST(unixepoch() AS TEXT));
