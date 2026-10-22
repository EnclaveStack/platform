-- 0001_init: the control plane's initial schema (L3-005 §1). Never edit after release; add 0002_….

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE users (
  id            TEXT PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT NOT NULL,
  created_at    TEXT NOT NULL
);

CREATE TABLE sessions (
  id_hash      TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at   TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  last_seen_at TEXT NOT NULL
);
CREATE INDEX sessions_expires_at ON sessions(expires_at);

CREATE TABLE api_tokens (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  prefix       TEXT NOT NULL,
  secret_hash  TEXT NOT NULL UNIQUE,
  scope        TEXT NOT NULL CHECK (scope IN ('read', 'write')),
  created_at   TEXT NOT NULL,
  last_used_at TEXT,
  expires_at   TEXT,
  revoked_at   TEXT
);

CREATE TABLE resources (
  id                  TEXT PRIMARY KEY,
  kind                TEXT NOT NULL CHECK (kind IN ('app', 'database', 'cache', 'bucket', 'secret')),
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
  UNIQUE (kind, name)
);
CREATE INDEX resources_due ON resources(phase, next_attempt_at);
CREATE INDEX resources_kind_created ON resources(kind, created_at, id);
CREATE INDEX resources_owner ON resources(owner_id);

CREATE TABLE resource_refs (
  from_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  to_id   TEXT NOT NULL REFERENCES resources(id) ON DELETE RESTRICT,
  field   TEXT NOT NULL,
  PRIMARY KEY (from_id, to_id, field)
);
CREATE INDEX resource_refs_to ON resource_refs(to_id);

CREATE TABLE resource_events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  type        TEXT NOT NULL,
  message     TEXT NOT NULL,
  created_at  TEXT NOT NULL
);
CREATE INDEX resource_events_resource ON resource_events(resource_id, id);

CREATE TABLE idempotency_keys (
  principal    TEXT NOT NULL,
  key          TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  status       INTEGER NOT NULL,
  body         BLOB NOT NULL,
  created_at   TEXT NOT NULL,
  PRIMARY KEY (principal, key)
);
CREATE INDEX idempotency_keys_created ON idempotency_keys(created_at);
