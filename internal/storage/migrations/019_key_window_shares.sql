-- Key-share allocations count usage per gateway key from the moment the key joins its scheme.
-- Member-mode entries keep key_id 0 and existing bindings keep joined_at 0.
ALTER TABLE allocation_entries ADD COLUMN key_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE allocation_keys ADD COLUMN joined_at INTEGER NOT NULL DEFAULT 0;
CREATE INDEX allocation_entries_key ON allocation_entries(scheme_id,key_id,started_at) WHERE key_id<>0;
