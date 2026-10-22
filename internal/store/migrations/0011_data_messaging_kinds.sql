-- enclave:rebuild
-- 0011_data_messaging_kinds: the v0.6 kinds and the purge action (L3-005 §8, ADR-029 – ADR-034). The resources
-- definition is 0010's, verbatim, with six kinds and 'purge' added. Runs gain the 'watch' trigger and a transcoder's
-- input key and output keys; other tables keep referencing resources by name, so they survive.

CREATE TABLE resources_new (
  id                   TEXT PRIMARY KEY,
  kind                 TEXT NOT NULL CHECK (kind IN ('app', 'database', 'cache', 'bucket', 'secret', 'domain', 'job', 'backup', 'network', 'firewall', 'loadbalancer', 'zone', 'record', 'vm', 'volume', 'snapshot', 'queue', 'topic', 'search', 'transcoder', 'cdn', 'email')),
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
  requested_action     TEXT NOT NULL DEFAULT '' CHECK (requested_action IN ('', 'restart', 'redeploy', 'purge')),
  next_attempt_at      TEXT,
  last_transition_at   TEXT NOT NULL,
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  serving_revision     INTEGER NOT NULL DEFAULT 0,
  next_run_at          TEXT,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  UNIQUE (kind, name)
);

INSERT INTO resources_new SELECT id, kind, name, spec_json, secret_ciphertext, generation, observed_generation, phase, message, steps_json,
  outputs_json, owner_id, attempts, requested_action, next_attempt_at, last_transition_at, created_at, updated_at, serving_revision,
  next_run_at, consecutive_failures
FROM resources;

DROP TABLE resources;
ALTER TABLE resources_new RENAME TO resources;

CREATE INDEX resources_due ON resources(phase, next_attempt_at);
CREATE INDEX resources_kind_created ON resources(kind, created_at, id);
CREATE INDEX resources_owner ON resources(owner_id);
CREATE INDEX resources_next_run ON resources(next_run_at) WHERE next_run_at IS NOT NULL;

CREATE TABLE runs_new (
  id            TEXT PRIMARY KEY,
  resource_id   TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  trigger       TEXT NOT NULL CHECK (trigger IN ('schedule', 'manual', 'catch_up', 'pre_restore', 'watch')),
  status        TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'timed_out', 'skipped')),
  scheduled_for TEXT,
  started_at    TEXT NOT NULL,
  finished_at   TEXT,
  exit_code     INTEGER,
  message       TEXT NOT NULL DEFAULT '',
  skipped_count INTEGER NOT NULL DEFAULT 0,
  output_tail   BLOB,
  artifact_json TEXT,
  subject_key   TEXT,
  outputs_json  TEXT NOT NULL DEFAULT '[]'
);

INSERT INTO runs_new (id, resource_id, trigger, status, scheduled_for, started_at, finished_at, exit_code, message, skipped_count, output_tail, artifact_json)
SELECT id, resource_id, trigger, status, scheduled_for, started_at, finished_at, exit_code, message, skipped_count, output_tail, artifact_json
FROM runs;

DROP TABLE runs;
ALTER TABLE runs_new RENAME TO runs;

CREATE INDEX runs_resource_started ON runs(resource_id, started_at DESC, id DESC);
CREATE INDEX runs_status ON runs(status);

-- Objects a transcoder has seen in its input folder (ADR-034); run_id is null for those it found when it became ready.
CREATE TABLE transcoder_objects (
  resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  key         TEXT NOT NULL,
  etag        TEXT NOT NULL,
  run_id      TEXT,
  seen_at     TEXT NOT NULL,
  PRIMARY KEY (resource_id, key)
);

-- Delivery counters per topic subscription (ADR-029); target is "queue/<name>" or the URL.
CREATE TABLE subscription_stats (
  topic_id   TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  target     TEXT NOT NULL,
  delivered  INTEGER NOT NULL DEFAULT 0,
  failed     INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  PRIMARY KEY (topic_id, target)
);
