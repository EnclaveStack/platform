-- enclave:rebuild
-- 0016_v0_10: v0.10 (L2-016, L3-005 §12, ADR-055 – ADR-060). The resources definition is 0015's with the 'function'
-- kind added. Runs gain the function triggers, their event and a queue message's receipt. Machines may be a
-- standby (at most one). Notifications may carry an event. New tables hold the cluster's epoch, the database CA
-- and each event alert's cursor.

CREATE TABLE resources_new (
  id                   TEXT PRIMARY KEY,
  project              TEXT NOT NULL DEFAULT 'default' REFERENCES projects(name),
  kind                 TEXT NOT NULL CHECK (kind IN ('app', 'database', 'cache', 'bucket', 'secret', 'domain', 'job', 'backup', 'network', 'firewall', 'loadbalancer', 'zone', 'record', 'vm', 'volume', 'snapshot', 'queue', 'topic', 'search', 'transcoder', 'cdn', 'email', 'dashboard', 'alert', 'channel', 'logexport', 'secretstore', 'stack', 'tunnel', 'function')),
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
  requested_action     TEXT NOT NULL DEFAULT '' CHECK (requested_action IN ('', 'restart', 'redeploy', 'purge', 'refresh', 'move')),
  next_attempt_at      TEXT,
  last_transition_at   TEXT NOT NULL,
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  serving_revision     INTEGER NOT NULL DEFAULT 0,
  next_run_at          TEXT,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  machine_id           TEXT REFERENCES machines(id),
  stack_id             TEXT,
  UNIQUE (project, kind, name)
);

INSERT INTO resources_new SELECT id, project, kind, name, spec_json, secret_ciphertext, generation, observed_generation, phase, message,
  steps_json, outputs_json, owner_id, attempts, requested_action, next_attempt_at, last_transition_at, created_at, updated_at,
  serving_revision, next_run_at, consecutive_failures, machine_id, stack_id
FROM resources;

DROP TABLE resources;
ALTER TABLE resources_new RENAME TO resources;

CREATE INDEX resources_due ON resources(phase, next_attempt_at);
CREATE INDEX resources_kind_created ON resources(kind, created_at, id);
CREATE INDEX resources_project_kind ON resources(project, kind, created_at, id);
CREATE INDEX resources_owner ON resources(owner_id);
CREATE INDEX resources_next_run ON resources(next_run_at) WHERE next_run_at IS NOT NULL;
CREATE INDEX resources_machine ON resources(machine_id);
CREATE INDEX resources_stack ON resources(stack_id) WHERE stack_id IS NOT NULL;

CREATE TABLE runs_new (
  id            TEXT PRIMARY KEY,
  resource_id   TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  trigger       TEXT NOT NULL CHECK (trigger IN ('schedule', 'manual', 'catch_up', 'pre_restore', 'watch', 'queue', 'topic', 'bucket', 'invoke')),
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
  outputs_json  TEXT NOT NULL DEFAULT '[]',
  event_json    TEXT,
  receipt       TEXT
);

INSERT INTO runs_new (id, resource_id, trigger, status, scheduled_for, started_at, finished_at, exit_code, message, skipped_count, output_tail,
  artifact_json, subject_key, outputs_json)
SELECT id, resource_id, trigger, status, scheduled_for, started_at, finished_at, exit_code, message, skipped_count, output_tail,
  artifact_json, subject_key, outputs_json
FROM runs;

DROP TABLE runs;
ALTER TABLE runs_new RENAME TO runs;

CREATE INDEX runs_resource_started ON runs(resource_id, started_at DESC, id DESC);
CREATE INDEX runs_status ON runs(status);

CREATE TABLE machines_new (
  id                  TEXT PRIMARY KEY,
  name                TEXT NOT NULL UNIQUE,
  state               TEXT NOT NULL CHECK (state IN ('waiting', 'online', 'offline')),
  state_since         TEXT NOT NULL,
  labels_json         TEXT NOT NULL DEFAULT '{}',
  schedulable         INTEGER NOT NULL DEFAULT 1,
  address             TEXT,
  agent_port          INTEGER NOT NULL DEFAULT 0,
  tunnel_port         INTEGER NOT NULL DEFAULT 0,
  tunnel_public_key   TEXT NOT NULL DEFAULT '',
  cert_serial         TEXT NOT NULL DEFAULT '',
  cert_not_after      TEXT,
  info_json           TEXT NOT NULL DEFAULT '{}',
  last_seen_at        TEXT,
  join_code_hash      TEXT UNIQUE,
  join_expires_at     TEXT,
  created_at          TEXT NOT NULL,
  is_main             INTEGER NOT NULL DEFAULT 0,
  role                TEXT NOT NULL DEFAULT 'worker' CHECK (role IN ('worker', 'edge', 'standby')),
  join_key_ciphertext BLOB
);

INSERT INTO machines_new (id, name, state, state_since, labels_json, schedulable, address, agent_port, tunnel_port, tunnel_public_key, cert_serial,
  cert_not_after, info_json, last_seen_at, join_code_hash, join_expires_at, created_at, is_main, role, join_key_ciphertext)
SELECT id, name, state, state_since, labels_json, schedulable, address, agent_port, tunnel_port, tunnel_public_key, cert_serial,
  cert_not_after, info_json, last_seen_at, join_code_hash, join_expires_at, created_at, is_main, role, join_key_ciphertext
FROM machines;

DROP TABLE machines;
ALTER TABLE machines_new RENAME TO machines;

CREATE UNIQUE INDEX machines_one_main ON machines(is_main) WHERE is_main = 1;
CREATE UNIQUE INDEX machines_one_standby ON machines(role) WHERE role = 'standby';

CREATE TABLE notifications_new (
  id              TEXT PRIMARY KEY,
  channel_id      TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  incident_id     TEXT,
  event           TEXT NOT NULL CHECK (event IN ('firing', 'resolved', 'repeat', 'test', 'event')),
  alert           TEXT NOT NULL DEFAULT '',
  subject         TEXT NOT NULL DEFAULT '',
  body_json       TEXT NOT NULL,
  status          TEXT NOT NULL CHECK (status IN ('pending', 'sent', 'failed')),
  attempts        INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT NOT NULL,
  last_error      TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  sent_at         TEXT
);

INSERT INTO notifications_new SELECT id, channel_id, incident_id, event, alert, subject, body_json, status, attempts, next_attempt_at, last_error,
  created_at, sent_at
FROM notifications;

DROP TABLE notifications;
ALTER TABLE notifications_new RENAME TO notifications;

CREATE INDEX notifications_due ON notifications(status, next_attempt_at);
CREATE INDEX notifications_channel ON notifications(channel_id, created_at DESC);
CREATE INDEX notifications_incident ON notifications(incident_id);

-- The control plane's epoch, raised by each promotion of a standby (ADR-057).
CREATE TABLE cluster (
  id    INTEGER PRIMARY KEY CHECK (id = 1),
  epoch INTEGER NOT NULL DEFAULT 0
);
INSERT INTO cluster (id, epoch) VALUES (1, 0);

-- The CA that signs databases' TLS certificates, its key sealed with the master key (ADR-058).
CREATE TABLE database_ca (
  id             INTEGER PRIMARY KEY CHECK (id = 1),
  cert_pem       TEXT NOT NULL,
  key_ciphertext BLOB NOT NULL,
  created_at     TEXT NOT NULL
);

-- The last event each event alert handled (ADR-060).
CREATE TABLE alert_cursors (
  alert_id TEXT PRIMARY KEY REFERENCES resources(id) ON DELETE CASCADE,
  event_id INTEGER NOT NULL
);

-- What the main machine last sent its standby (ADR-057).
CREATE TABLE standby_copies (
  machine_id    TEXT PRIMARY KEY REFERENCES machines(id) ON DELETE CASCADE,
  last_copy_at  TEXT,
  bytes         INTEGER NOT NULL DEFAULT 0,
  last_error    TEXT NOT NULL DEFAULT '',
  attempted_at  TEXT NOT NULL
);

-- Each TLS database's certificate, its key sealed with the master key; a new one recreates the container
-- (ADR-058).
CREATE TABLE database_certs (
  resource_id    TEXT PRIMARY KEY REFERENCES resources(id) ON DELETE CASCADE,
  cert_pem       TEXT NOT NULL,
  key_ciphertext BLOB NOT NULL,
  names          TEXT NOT NULL,
  serial         TEXT NOT NULL,
  not_after      TEXT NOT NULL
);
