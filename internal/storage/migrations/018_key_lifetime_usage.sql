-- Lifetime per-key usage by model for key lifetime cost. It is never pruned; rows are bounded by keys and the
-- per-key model label cap. Input tokens of requests without a cache split are kept apart so one such request
-- cannot make the key's whole priced history unpriceable.
CREATE TABLE usage_lifetime_keys (
 key_id INTEGER NOT NULL,
 model TEXT NOT NULL CHECK(length(model)<=180),
 requests INTEGER NOT NULL DEFAULT 0 CHECK(requests>=0),
 input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(input_tokens>=0),
 output_tokens INTEGER NOT NULL DEFAULT 0 CHECK(output_tokens>=0),
 cached_tokens INTEGER NOT NULL DEFAULT 0 CHECK(cached_tokens>=0),
 unsplit_input_tokens INTEGER NOT NULL DEFAULT 0 CHECK(unsplit_input_tokens>=0),
 input_reported INTEGER NOT NULL DEFAULT 0 CHECK(input_reported>=0),
 output_reported INTEGER NOT NULL DEFAULT 0 CHECK(output_reported>=0),
 PRIMARY KEY(key_id,model)
);
