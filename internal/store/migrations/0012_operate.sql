-- enclave:rebuild
-- 0012_operate: v0.7 (L3-005 §9, ADR-035 – ADR-041). Projects become namespaces: resources gain a project and are
-- unique per (project, kind, name); every existing row lands in 'default'. The resources definition is 0011's with
-- the project column, five kinds and the 'refresh' action added. Users gain roles; tokens trade their scope for a
-- role and a project list. New tables hold members, invites, the audit list, metrics, alerts, notifications and
-- log export progress.

CREATE TABLE projects (
  name        TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);
INSERT INTO projects (name, description, created_at) VALUES ('default', 'Everything made before projects existed, and anything created without one.', strftime('%Y-%m-%dT%H:%M:%S.000000000Z', 'now'));

CREATE TABLE resources_new (
  id                   TEXT PRIMARY KEY,
  project              TEXT NOT NULL DEFAULT 'default' REFERENCES projects(name),
  kind                 TEXT NOT NULL CHECK (kind IN ('app', 'database', 'cache', 'bucket', 'secret', 'domain', 'job', 'backup', 'network', 'firewall', 'loadbalancer', 'zone', 'record', 'vm', 'volume', 'snapshot', 'queue', 'topic', 'search', 'transcoder', 'cdn', 'email', 'dashboard', 'alert', 'channel', 'logexport', 'secretstore')),
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
  requested_action     TEXT NOT NULL DEFAULT '' CHECK (requested_action IN ('', 'restart', 'redeploy', 'purge', 'refresh')),
  next_attempt_at      TEXT,
  last_transition_at   TEXT NOT NULL,
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  serving_revision     INTEGER NOT NULL DEFAULT 0,
  next_run_at          TEXT,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  UNIQUE (project, kind, name)
);

INSERT INTO resources_new SELECT id, 'default', kind, name, spec_json, secret_ciphertext, generation, observed_generation, phase, message,
  steps_json, outputs_json, owner_id, attempts, requested_action, next_attempt_at, last_transition_at, created_at, updated_at,
  serving_revision, next_run_at, consecutive_failures
FROM resources;

DROP TABLE resources;
ALTER TABLE resources_new RENAME TO resources;

CREATE INDEX resources_due ON resources(phase, next_attempt_at);
CREATE INDEX resources_kind_created ON resources(kind, created_at, id);
CREATE INDEX resources_project_kind ON resources(project, kind, created_at, id);
CREATE INDEX resources_owner ON resources(owner_id);
CREATE INDEX resources_next_run ON resources(next_run_at) WHERE next_run_at IS NOT NULL;

-- People and roles (ADR-036). The person who set the machine up becomes its admin.
ALTER TABLE users ADD COLUMN machine_role TEXT CHECK (machine_role IN ('viewer', 'editor', 'admin'));
ALTER TABLE users ADD COLUMN last_sign_in_at TEXT;
UPDATE users SET machine_role = 'admin';

CREATE TABLE project_members (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  project TEXT NOT NULL REFERENCES projects(name) ON DELETE CASCADE,
  role    TEXT NOT NULL CHECK (role IN ('viewer', 'editor', 'admin')),
  PRIMARY KEY (user_id, project)
);
CREATE INDEX project_members_project ON project_members(project);

CREATE TABLE invites (
  user_id    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  code_hash  TEXT NOT NULL UNIQUE,
  kind       TEXT NOT NULL CHECK (kind IN ('invite', 'reset')),
  expires_at TEXT NOT NULL
);

CREATE TABLE api_tokens_new (
  id            TEXT PRIMARY KEY,
  user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name          TEXT NOT NULL,
  prefix        TEXT NOT NULL,
  secret_hash   TEXT NOT NULL UNIQUE,
  role          TEXT NOT NULL CHECK (role IN ('viewer', 'editor', 'admin')),
  projects_json TEXT,
  created_at    TEXT NOT NULL,
  last_used_at  TEXT,
  expires_at    TEXT,
  revoked_at    TEXT
);
-- Before v0.7 the only person was the admin and a write token could do everything, so write becomes admin.
INSERT INTO api_tokens_new SELECT id, user_id, name, prefix, secret_hash, CASE scope WHEN 'read' THEN 'viewer' ELSE 'admin' END, NULL,
  created_at, last_used_at, expires_at, revoked_at
FROM api_tokens;
DROP TABLE api_tokens;
ALTER TABLE api_tokens_new RENAME TO api_tokens;
CREATE INDEX api_tokens_user ON api_tokens(user_id);

CREATE TABLE audit_log (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  at        TEXT NOT NULL,
  actor     TEXT NOT NULL,
  action    TEXT NOT NULL,
  target    TEXT NOT NULL DEFAULT '',
  detail    TEXT NOT NULL DEFAULT '',
  client_ip TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_at ON audit_log(at);

-- Metrics (ADR-037): a series per resource and metric, 30-second points for a day, 15-minute rollups for 30 days.
CREATE TABLE metric_series (
  id          INTEGER PRIMARY KEY,
  resource_id TEXT NOT NULL,
  metric      TEXT NOT NULL,
  UNIQUE (resource_id, metric)
);

CREATE TABLE metric_points (
  series_id INTEGER NOT NULL,
  ts        INTEGER NOT NULL,
  value     REAL NOT NULL,
  PRIMARY KEY (series_id, ts)
) WITHOUT ROWID;
CREATE INDEX metric_points_ts ON metric_points(ts);

CREATE TABLE metric_rollups (
  series_id INTEGER NOT NULL,
  ts        INTEGER NOT NULL,
  avg       REAL NOT NULL,
  max       REAL NOT NULL,
  count     INTEGER NOT NULL,
  PRIMARY KEY (series_id, ts)
) WITHOUT ROWID;
CREATE INDEX metric_rollups_ts ON metric_rollups(ts);

-- Alerts (ADR-038).
CREATE TABLE alert_states (
  alert_id         TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  subject_id       TEXT NOT NULL,
  state            TEXT NOT NULL CHECK (state IN ('ok', 'firing', 'no_data')),
  since            TEXT NOT NULL,
  last_notified_at TEXT,
  incident_id      TEXT,
  last_checked_at  TEXT NOT NULL,
  PRIMARY KEY (alert_id, subject_id)
);

CREATE TABLE incidents (
  id          TEXT PRIMARY KEY,
  alert_id    TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  subject     TEXT NOT NULL,
  started_at  TEXT NOT NULL,
  resolved_at TEXT,
  value       REAL,
  message     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX incidents_alert ON incidents(alert_id, started_at DESC);

-- The notification outbox (ADR-039).
CREATE TABLE notifications (
  id              TEXT PRIMARY KEY,
  channel_id      TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  incident_id     TEXT,
  event           TEXT NOT NULL CHECK (event IN ('firing', 'resolved', 'repeat', 'test')),
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
CREATE INDEX notifications_due ON notifications(status, next_attempt_at);
CREATE INDEX notifications_channel ON notifications(channel_id, created_at DESC);
CREATE INDEX notifications_incident ON notifications(incident_id);

-- Log export progress (ADR-040).
CREATE TABLE logexport_cursors (
  export_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  container TEXT NOT NULL,
  ts        TEXT NOT NULL,
  skip      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (export_id, container)
);

CREATE TABLE logexport_stats (
  export_id    TEXT PRIMARY KEY REFERENCES resources(id) ON DELETE CASCADE,
  lines_sent   INTEGER NOT NULL DEFAULT 0,
  bytes_sent   INTEGER NOT NULL DEFAULT 0,
  last_sent_at TEXT,
  last_error   TEXT NOT NULL DEFAULT '',
  lag_seconds  REAL NOT NULL DEFAULT 0
);
