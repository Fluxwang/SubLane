ALTER TABLE account_affinity ADD COLUMN model TEXT NOT NULL DEFAULT '' CHECK(length(model) <= 128);
