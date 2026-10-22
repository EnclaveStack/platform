-- enclave:rebuild
-- 0013_machines: v0.8 (L3-005 §10, ADR-042 – ADR-048). Machines join one EnclaveStack: the main machine is the
-- row 'main', and every existing resource is placed on it. The resources definition is 0012's with machine_id,
-- stack_id, the 'stack' kind and the 'move' action added. New tables hold the machine CA, address pools, machine
-- events and the machine's own catalogue templates.

CREATE TABLE machines (
  id                TEXT PRIMARY KEY,
  name              TEXT NOT NULL UNIQUE,
  state             TEXT NOT NULL CHECK (state IN ('waiting', 'online', 'offline')),
  state_since       TEXT NOT NULL,
  labels_json       TEXT NOT NULL DEFAULT '{}',
  schedulable       INTEGER NOT NULL DEFAULT 1,
  address           TEXT,
  agent_port        INTEGER NOT NULL DEFAULT 0,
  tunnel_port       INTEGER NOT NULL DEFAULT 0,
  tunnel_public_key TEXT NOT NULL DEFAULT '',
  cert_serial       TEXT NOT NULL DEFAULT '',
  cert_not_after    TEXT,
  info_json         TEXT NOT NULL DEFAULT '{}',
  last_seen_at      TEXT,
  join_code_hash    TEXT UNIQUE,
  join_expires_at   TEXT,
  created_at        TEXT NOT NULL
);
INSERT INTO machines (id, name, state, state_since, created_at) VALUES ('main', 'main', 'online',
  strftime('%Y-%m-%dT%H:%M:%S.000000000Z', 'now'), strftime('%Y-%m-%dT%H:%M:%S.000000000Z', 'now'));

CREATE TABLE resources_new (
  id                   TEXT PRIMARY KEY,
  project              TEXT NOT NULL DEFAULT 'default' REFERENCES projects(name),
  kind                 TEXT NOT NULL CHECK (kind IN ('app', 'database', 'cache', 'bucket', 'secret', 'domain', 'job', 'backup', 'network', 'firewall', 'loadbalancer', 'zone', 'record', 'vm', 'volume', 'snapshot', 'queue', 'topic', 'search', 'transcoder', 'cdn', 'email', 'dashboard', 'alert', 'channel', 'logexport', 'secretstore', 'stack')),
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
  serving_revision, next_run_at, consecutive_failures, 'main', NULL
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

CREATE TABLE machine_ca (
  id             INTEGER PRIMARY KEY CHECK (id = 1),
  cert_pem       TEXT NOT NULL,
  key_ciphertext BLOB NOT NULL,
  created_at     TEXT NOT NULL
);

CREATE TABLE address_pool (
  network    TEXT NOT NULL,
  address    TEXT NOT NULL,
  machine_id TEXT NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  PRIMARY KEY (network, address)
);
CREATE INDEX address_pool_machine ON address_pool(machine_id, network);

CREATE TABLE machine_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  machine_id TEXT NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
  type       TEXT NOT NULL,
  message    TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX machine_events_machine ON machine_events(machine_id, id);

CREATE TABLE catalogue_templates (
  name       TEXT NOT NULL,
  version    TEXT NOT NULL,
  title      TEXT NOT NULL,
  body       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  created_by TEXT NOT NULL,
  PRIMARY KEY (name, version)
);
