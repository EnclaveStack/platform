-- enclave:rebuild
-- 0003_domain_kind: allow kind 'domain' (L3-005 §5, ADR-015). SQLite can't alter a CHECK constraint, so
-- the table is rebuilt. The runner applies "rebuild" migrations with foreign keys off (so dropping the
-- old table doesn't cascade into events and refs) and runs PRAGMA foreign_key_check before committing.
-- The definition below is 0001's, verbatim, plus 0002's serving_revision; only the kind list changes.

CREATE TABLE resources_new (
  id                  TEXT PRIMARY KEY,
  kind                TEXT NOT NULL CHECK (kind IN ('app', 'database', 'cache', 'bucket', 'secret', 'domain')),
  name                TEXT NOT NULL,
  spec_json           TEXT NOT NULL,
  secret_ciphertext   BLOB,
  generation          INTEGER NOT NULL,
  observed_generation INTEGER NOT NULL DEFAULT 0,
  phase               TEXT NOT NULL CHECK (phase IN ('pending', 'provisioning', 'ready', 'updating', 'deleting', 'failed')),
  message             TEXT NOT NULL DEFAULT '',
  steps_json          TEXT NOT NULL DEFAULT '[]',
  outputs_json        TEXT NOT NULL DEFAULT '{}',
  owner_id            TEXT,
  attempts            INTEGER NOT NULL DEFAULT 0,
  requested_action    TEXT NOT NULL DEFAULT '' CHECK (requested_action IN ('', 'restart', 'redeploy')),
  next_attempt_at     TEXT,
  last_transition_at  TEXT NOT NULL,
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL,
  serving_revision    INTEGER NOT NULL DEFAULT 0,
  UNIQUE (kind, name)
);

INSERT INTO resources_new (id, kind, name, spec_json, secret_ciphertext, generation, observed_generation, phase, message, steps_json,
  outputs_json, owner_id, attempts, requested_action, next_attempt_at, last_transition_at, created_at, updated_at, serving_revision)
SELECT id, kind, name, spec_json, secret_ciphertext, generation, observed_generation, phase, message, steps_json,
  outputs_json, owner_id, attempts, requested_action, next_attempt_at, last_transition_at, created_at, updated_at, serving_revision
FROM resources;

DROP TABLE resources;
ALTER TABLE resources_new RENAME TO resources;

CREATE INDEX resources_due ON resources(phase, next_attempt_at);
CREATE INDEX resources_kind_created ON resources(kind, created_at, id);
CREATE INDEX resources_owner ON resources(owner_id);
