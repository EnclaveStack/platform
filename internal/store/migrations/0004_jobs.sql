-- enclave:rebuild
-- 0004_jobs: kind 'job', the scheduler's columns and the run history (L3-005 §5, ADR-017). The resources
-- definition is 0003's, verbatim, with 'job' added to the kind list and two new columns.

CREATE TABLE resources_new (
  id                   TEXT PRIMARY KEY,
  kind                 TEXT NOT NULL CHECK (kind IN ('app', 'database', 'cache', 'bucket', 'secret', 'domain', 'job')),
  name                 TEXT NOT NULL,
  spec_json            TEXT NOT NULL,
  secret_ciphertext    BLOB,
  generation           INTEGER NOT NULL,
  observed_generation  INTEGER NOT NULL DEFAULT 0,
  phase                TEXT NOT NULL CHECK (phase IN ('pending', 'provisioning', 'ready', 'updating', 'deleting', 'failed')),
  message              TEXT NOT NULL DEFAULT '',
  steps_json           TEXT NOT NULL DEFAULT '[]',
  outputs_json         TEXT NOT NULL DEFAULT '{}',
  owner_id             TEXT,
  attempts             INTEGER NOT NULL DEFAULT 0,
  requested_action     TEXT NOT NULL DEFAULT '' CHECK (requested_action IN ('', 'restart', 'redeploy')),
  next_attempt_at      TEXT,
  last_transition_at   TEXT NOT NULL,
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  serving_revision     INTEGER NOT NULL DEFAULT 0,
  next_run_at          TEXT,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
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
CREATE INDEX resources_next_run ON resources(next_run_at) WHERE next_run_at IS NOT NULL;

CREATE TABLE runs (
  id            TEXT PRIMARY KEY,
  resource_id   TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  trigger       TEXT NOT NULL CHECK (trigger IN ('schedule', 'manual', 'catch_up', 'pre_restore')),
  status        TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'timed_out', 'skipped')),
  scheduled_for TEXT,
  started_at    TEXT NOT NULL,
  finished_at   TEXT,
  exit_code     INTEGER,
  message       TEXT NOT NULL DEFAULT '',
  skipped_count INTEGER NOT NULL DEFAULT 0,
  output_tail   BLOB,
  artifact_json TEXT
);
CREATE INDEX runs_resource_started ON runs(resource_id, started_at DESC, id DESC);
CREATE INDEX runs_status ON runs(status);
