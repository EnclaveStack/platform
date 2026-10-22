-- Builds of source apps (ADR-019, L3-005): commit, inputs, result and a log tail. The newest 10 per app are kept.
CREATE TABLE builds (
  id          TEXT PRIMARY KEY,
  resource_id TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  status      TEXT NOT NULL CHECK (status IN ('running','succeeded','failed','skipped')),
  commit_sha  TEXT NOT NULL DEFAULT '',
  inputs_hash TEXT NOT NULL DEFAULT '',
  image       TEXT NOT NULL DEFAULT '',
  message     TEXT NOT NULL DEFAULT '',
  started_at  TEXT NOT NULL,
  finished_at TEXT,
  log         BLOB
);
CREATE INDEX builds_resource_started ON builds (resource_id, started_at DESC);
