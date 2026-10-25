-- enclave:rebuild
-- 0015_public_access: public access (L2-014, L3-005 §11, ADR-051 – ADR-053). The resources definition is 0013's
-- with the 'tunnel' kind added. Machines gain a role (every existing machine is a worker) and the sealed key of
-- an edge machine's join. New tables hold each tunnel's edge listener ports and the gate's sessions.

CREATE TABLE resources_new (
  id                   TEXT PRIMARY KEY,
  project              TEXT NOT NULL DEFAULT 'default' REFERENCES projects(name),
  kind                 TEXT NOT NULL CHECK (kind IN ('app', 'database', 'cache', 'bucket', 'secret', 'domain', 'job', 'backup', 'network', 'firewall', 'loadbalancer', 'zone', 'record', 'vm', 'volume', 'snapshot', 'queue', 'topic', 'search', 'transcoder', 'cdn', 'email', 'dashboard', 'alert', 'channel', 'logexport', 'secretstore', 'stack', 'tunnel')),
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

ALTER TABLE machines ADD COLUMN role TEXT NOT NULL DEFAULT 'worker' CHECK (role IN ('worker', 'edge'));
ALTER TABLE machines ADD COLUMN join_key_ciphertext BLOB;

CREATE TABLE tunnel_ports (
  tunnel_id  TEXT PRIMARY KEY REFERENCES resources(id) ON DELETE CASCADE,
  http_port  INTEGER NOT NULL UNIQUE,
  https_port INTEGER UNIQUE
);

CREATE TABLE gate_sessions (
  token_hash TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  hostname   TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  checked_at TEXT NOT NULL
);
CREATE INDEX gate_sessions_user ON gate_sessions(user_id);
CREATE INDEX gate_sessions_expires ON gate_sessions(expires_at);
