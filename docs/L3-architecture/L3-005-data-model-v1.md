# L3-005 · Data Model

---
| Field | Value |
|---|---|
| Document ID | L3-005-data-model-v1 |
| Layer | L3 — Architecture |
| Status | Draft |
| Version | v14 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-22 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs |
| License | Apache-2.0 |
---

## Purpose

The control plane's own persistent state: one SQLite database, `<data-dir>/enclave.db` (ADR-005). Migrations live in `internal/store/migrations/NNNN_<slug>.sql`, are embedded in the binary and applied in order at start; applied versions are recorded in `schema_migrations`. A migration is never edited after release — add a new one.

## 1 · Tables

| Table | Columns (type) | Keys and constraints | Notes |
|---|---|---|---|
| `schema_migrations` | `version INTEGER`, `applied_at TEXT` | PK `version` | |
| `settings` | `key TEXT`, `value TEXT` | PK `key` | `machine_name`, `setup_code_hash`, `objectstore_*` (ciphertext) |
| `users` | `id TEXT`, `email TEXT`, `password_hash TEXT`, `created_at TEXT` | PK `id`; UNIQUE `email COLLATE NOCASE` | argon2id PHC string |
| `sessions` | `id_hash TEXT`, `user_id TEXT`, `created_at`, `expires_at`, `last_seen_at` | PK `id_hash`; FK `user_id → users ON DELETE CASCADE`; INDEX `expires_at` | Only SHA-256 of the cookie value |
| `api_tokens` | `id TEXT`, `user_id TEXT`, `name TEXT`, `prefix TEXT`, `secret_hash TEXT`, `scope TEXT`, `created_at`, `last_used_at`, `expires_at`, `revoked_at` | PK `id`; UNIQUE `secret_hash`; FK `user_id`; CHECK `scope IN ('read','write')` | |
| `resources` | `id TEXT`, `kind TEXT`, `name TEXT`, `spec_json TEXT`, `secret_ciphertext BLOB`, `generation INTEGER`, `observed_generation INTEGER`, `phase TEXT`, `message TEXT`, `steps_json TEXT`, `outputs_json TEXT`, `owner_id TEXT`, `attempts INTEGER`, `requested_action TEXT` (`''`, `restart`, `redeploy`), `next_attempt_at TEXT`, `last_transition_at TEXT`, `created_at TEXT`, `updated_at TEXT` | PK `id`; UNIQUE `(kind, name)`; CHECK `kind IN (…)`; CHECK `phase IN (…)`; INDEX `(phase, next_attempt_at)`; INDEX `(kind, created_at, id)` | `spec_json` never contains a secret value — that is `secret_ciphertext` (AES-256-GCM, nonce-prefixed, resource ID as additional data). `owner_id` is a resource ID or `system` (not a foreign key, so system ownership can be expressed); the reconciler deletes owned rows with their owner |
| `resource_refs` | `from_id TEXT`, `to_id TEXT`, `field TEXT` | PK `(from_id, to_id, field)`; FKs to `resources` (`from` CASCADE, `to` RESTRICT) | App → secret references; RESTRICT backs CP-12 |
| `resource_events` | `id INTEGER`, `resource_id TEXT`, `type TEXT`, `message TEXT`, `created_at TEXT` | PK `id` AUTOINCREMENT; FK `resource_id → resources ON DELETE CASCADE`; INDEX `(resource_id, id)` | Messages never contain secret values |
| `idempotency_keys` | `principal TEXT`, `key TEXT`, `request_hash TEXT`, `status INTEGER`, `body BLOB`, `created_at TEXT` | PK `(principal, key)`; INDEX `created_at` | Swept after 24 h |

`kind` ∈ `app, database, cache, bucket, secret`; `phase` ∈ `pending, provisioning, ready, updating, deleting, failed`; check constraints mirror `pkg/api` (a test compares them).

Every time column holds RFC 3339 UTC with exactly nine fractional digits (`2026-10-21T03:54:40.100000000Z`). The width is fixed so that text comparison equals time comparison: SQLite orders and compares these as strings (`ORDER BY created_at`, `next_run_at <= now`, cursors). RFC3339Nano drops trailing zeros, so before migration `0005` "…40.1Z" sorted after "…40.100182Z".

## 2 · Identifiers

Resources: `<prefix>_<16 lowercase base32 chars>` from 10 crypto-random bytes (`app_`, `db_`, `cache_`, `bkt_`, `sec_`). Users and tokens: `usr_…`, `tok_…`. Docker objects derive from them: container `es-<kind>-<name>`, volume `es-<id>-data`.

## 3 · Transactions

Every service operation that writes more than one row (create resource + event + refs + idempotency record; delete with dependency check) runs in one `BEGIN IMMEDIATE` transaction. The reconciler claims work by updating `next_attempt_at` in a transaction so a resource is never processed twice at once.

## 4 · Retention

Sessions expire after 7 days idle; expired sessions and idempotency records older than 24 h are deleted hourly. Events are kept for the resource's lifetime, capped at the newest 500 per resource.

## 5 · v0.3 changes (planned)

These changes ship as new migrations with the features they serve (`L2-005`, ADR-015 – ADR-019). Each migration leaves existing rows valid.

| Change | Detail |
|---|---|
| `resources.kind` CHECK | `domain` **shipped** (`0003_domain_kind`), `job` **shipped** (`0004_jobs`, a table rebuild: SQLite can't alter a CHECK; rebuild migrations start with `-- enclave:rebuild`, run with foreign keys off on one connection and commit only after `PRAGMA foreign_key_check`). `backup` **shipped** (`0006_backup_kind`, the same rebuild; runs and their cascade survive it). `pkg/api` changes first; the check-constraint test compares the two |
| `resources.serving_revision INTEGER NOT NULL DEFAULT 0` — **shipped** (`0002_app_revisions`) | Apps: the revision number receiving traffic (ADR-016); 0 = none yet, or a v0.2 container not yet replaced |
| `resources.next_run_at TEXT NULL` — **shipped** (`0004_jobs`) | Jobs and backups: the next due time (UTC); INDEX `(next_run_at)` WHERE NOT NULL (ADR-017) |
| `resources.consecutive_failures INTEGER NOT NULL DEFAULT 0` — **shipped** (`0004_jobs`) | Jobs and backups; reset on success; three failed backups → `failed` (BAK-09) |
| New table `runs` — **shipped** (`0004_jobs`; backups use `artifact_json` from phase 4, cleared with " Removed by retention." appended to `message` when retention deletes the object, so the run can no longer be restored) | `id TEXT` PK (`run_…`); `resource_id TEXT` FK → `resources ON DELETE CASCADE`; `trigger TEXT` CHECK `IN ('schedule','manual','catch_up','pre_restore')`; `status TEXT` CHECK `IN ('running','succeeded','failed','timed_out','skipped')`; `scheduled_for TEXT NULL`; `started_at TEXT`; `finished_at TEXT NULL`; `exit_code INTEGER NULL`; `message TEXT`; `skipped_count INTEGER DEFAULT 0`; `output_tail BLOB NULL` (≤ 64 KiB); `artifact_json TEXT NULL` (backups: key, size, sha256, destination). INDEX `(resource_id, started_at DESC)`; INDEX `(status)` |
| `resource_refs.field` values | `domain.app`, `job.app`, `job.secretEnv.<KEY>`, `backup.source`, `backup.bucket`, `backup.credentialsSecret`, `app.source.credentialsSecret` |
| New table `builds` — **shipped** (`0007_builds`) | `id TEXT` PK (`bld_…`); `resource_id TEXT` FK → `resources ON DELETE CASCADE`; `status TEXT` CHECK `IN ('running','succeeded','failed','skipped')`; `commit_sha TEXT`; `inputs_hash TEXT`; `image TEXT`; `message TEXT`; `started_at TEXT`; `finished_at TEXT NULL`; `log BLOB` (≤ 256 KiB, the end kept). INDEX `(resource_id, started_at DESC)`. The newest 10 per app are kept (ADR-019) |
| `settings` keys | `public_address`, `acme_email` — **shipped** |
| Fixed-width timestamps — **shipped** (`0005_fixed_width_timestamps`) | Rewrites every stored time to nine fractional digits (§1) |
| Retention | Runs: newest 100 per resource or 30 days, whichever keeps more, swept hourly. A backup's newest successful run is never swept while the backup exists |

`output_tail` holds job output exactly as the job printed it. If a job prints a secret, the tail contains it, so run details need the same access as reveal: a session or a write token (`L3-006` §5).

## 6 · v0.4 changes (shipped 2026-10-21)

These ship as new migrations with the phases they serve (`L2-006`, ADR-020 – ADR-024). Each leaves existing rows valid.

| Change | Detail |
|---|---|
| `resources.kind` CHECK | `network`, `firewall`, `loadbalancer`, `zone` and `record`, through `0008_network_kinds` (a rebuild migration, as `0006`). `pkg/api` changes first; the check-constraint test compares the two |
| `resource_refs.field` values | `<kind>.network` (app, database, cache, job), `firewall.targets[<i>]`, `firewall.ingress[<i>].from[<j>]`, `firewall.egress[<i>].to[<j>]`, `loadbalancer.targets[<i>].app`, `zone.credentialsSecret`, `record.zone`, `domain.zone` |
| New table `certificates` (`0009_certificates`, phase 5) | `hostname TEXT` PK; `domain_id TEXT` FK → `resources ON DELETE CASCADE`; `chain_pem TEXT`; `key_ciphertext BLOB` (secretbox, the hostname as additional data); `issuer TEXT`; `not_before TEXT`; `not_after TEXT`; `updated_at TEXT`. Only DNS-01 certificates live here; Caddy keeps the HTTP-01 and internal ones (ADR-024) |
| `settings` keys | `acme_account:<SHA-256 of the directory URL>`: the ACME account key (secretbox ciphertext, base64) and its registration URL, JSON |
| Docker objects | Network `es-net-<name>`; load balancer container `es-loadbalancer-<name>`; replica containers `es-app-<name>-r<N>-<i>` (`i` ≥ 2); one-off `es-netfilter-<random>` helpers, removed after use |

Firewall application state (what was last applied to which container) is kept in memory only: after a restart every policy is applied again, which is idempotent (ADR-021).

## 7 · v0.5 changes (shipped 2026-10-21)

These ship as new migrations with the phases they serve (`L2-007`, ADR-025 – ADR-028). Each leaves existing rows valid.

| Change | Detail |
|---|---|
| `resources.kind` CHECK | `vm`, `volume` and `snapshot`, through `0010_compute_kinds` (a rebuild migration, as `0008`). `pkg/api` changes first; the check-constraint test compares the two |
| `resource_refs.field` values (as stored) | `spec.volumes[<i>].volume` (apps, jobs), `spec.network`, `spec.disks[<i>]` and `spec.sshKeySecret` (VMs), `spec.image` (a VM's snapshot, dropped once its root disk exists) and `spec.snapshot` (a volume's, cleared once filled), firewall targets and rule sources naming `vm/<name>`. Domain and load-balancer references to VMs aren't built (`L2-007` VMN-06) |
| Files under the data dir | `vm/<id>/` (0700: `root.qcow2`, `seed.iso`, `console.log` and `console.log.1`, `qemu.pid`, `qmp.sock` (or `/tmp/enclave-<uid>/<id>.qmp` when the path is too long), `config` (what the running VM was started with), `image` and `image.sha256`), `images/<hash of URL>.<qcow2\|raw>` with a `.json` sidecar, `disks/<id>.qcow2`, `snapshots/<id>.<qcow2\|tar>` (0600) |
| Docker objects | Link containers `es-vm-<name>`; network `es-vmlink`; filesystem volumes `es-vol-<id>`; one-off `es-snapshot-<random>` helpers, removed after use; image `enclave.local/vmlink:<hash>` |

A VM's host ports (frame link and SSH) are kept in its outputs, so a recreated link container gets the same ones (ADR-026). The guest-shutdown rule writes `power: stopped` into the VM's spec through the normal update path, bumping its version, so a client holding an old version gets `412` (ADR-010).

## 8 · v0.6 changes (planned)

These ship as migrations with the phases they serve (`L2-008`, ADR-029 – ADR-034). Each leaves existing rows valid.

| Change | Detail |
|---|---|
| `resources.kind` CHECK | `queue`, `topic`, `search`, `transcoder`, `cdn` and `email`, through `0011_data_messaging_kinds` (a rebuild migration, as `0010`) |
| `resources.requested_action` CHECK | gains `purge` (queues and CDNs), in the same rebuild |
| `runs` | gains `subject_key TEXT` (a transcoder run's input key) and `outputs_json TEXT NOT NULL DEFAULT '[]'` (the keys it wrote); `trigger` gains `watch` |
| `transcoder_objects` (new) | `(resource_id, key)` primary key, `etag`, `run_id` (null for objects seen before the transcoder became ready), `seen_at`; deleted with the transcoder (ADR-034) |
| `subscription_stats` (new) | `(topic_id, target)` primary key, `delivered`, `failed`, `last_error`, `updated_at`; rows of removed subscriptions and deleted topics are deleted (ADR-029) |
| Managed secrets | `broker-credentials` (system-owned, the broker's `system` password); `<name>-<kind>-password` for queues, topics, search and email services (owned by the resource, deleted with it). Databases keep `<name>-password` |
| `resource_refs.field` values | `spec.deadLetterQueue`, `spec.subscriptions[<i>].queue`, `spec.subscriptions[<i>].secret`, `spec.relay.credentialsSecret`, `spec.input.bucket`, `spec.output.bucket`, `spec.origin`, `spec.zone`, and `spec.network` for the placed kinds |
| Docker objects | `es-system-broker` and its volume `es-system-broker-data`; `es-search-<name>`, `es-email-<name>` and `es-cdn-<name>` with `es-<id>-data`; one-off `es-transcode-<run id>` containers, removed after each run |

## 9 · v0.7 changes (planned)

These ship as migrations with the phases they serve (`L2-009`, ADR-035 – ADR-041). Each leaves existing rows valid.

| Change | Detail |
|---|---|
| `projects` (new) | `name` primary key, `description`, `created_at`; `default` inserted by `0012_projects_and_roles` (ADR-035) |
| `resources` | Rebuilt by `0012`: gains `project TEXT NOT NULL DEFAULT 'default' REFERENCES projects(name)`; the unique key becomes `(project, kind, name)`; index `(project, kind, created_at, id)`. `kind` CHECK gains `dashboard`, `alert`, `channel`, `logexport`, `secretstore` (in `0013` with phase 2 onwards, one rebuild per phase as before). `requested_action` gains `refresh` |
| `users` | Gains `machine_role TEXT CHECK (machine_role IN ('viewer','editor','admin'))` (the existing user becomes `admin`), `last_sign_in_at TEXT`; `password_hash` may be `''` while an invite is open |
| `project_members` (new) | `(user_id, project)` primary key, `role`; cascades with the user and the project |
| `invites` (new) | `user_id` primary key, `code_hash` unique, `kind` (`invite`/`reset`), `expires_at` |
| `api_tokens` | Rebuilt: `scope` becomes `role` (`read` → `viewer`, `write` → `editor`); gains `projects_json TEXT` (null: all projects) |
| `audit_log` (new) | `id` autoincrement, `at`, `actor`, `action`, `target`, `detail`, `client_ip`; index on `at`; swept after 90 days |
| `metric_series` (new) | `id` integer primary key, `resource_id` (a resource ID, or `machine`), `metric`; unique `(resource_id, metric)` |
| `metric_points` (new) | `(series_id, ts)` primary key `WITHOUT ROWID`, `value REAL`; `ts` unix seconds; kept 24 h |
| `metric_rollups` (new) | `(series_id, ts)` primary key `WITHOUT ROWID`, `avg`, `max`, `count`; 15-minute buckets; kept 30 days |
| `alert_states` (new) | `(alert_id, subject_id)` primary key, `state`, `since`, `last_notified_at`, `incident_id`, `last_checked_at` |
| `incidents` (new) | `id`, `alert_id`, `subject`, `started_at`, `resolved_at`, `value`, `message`; kept 90 days |
| `notifications` (new) | `id`, `channel_id`, `incident_id` (null for tests), `event`, `alert`, `subject`, `body_json`, `status`, `attempts`, `next_attempt_at`, `last_error`, `created_at`, `sent_at`; 100 newest per channel kept once sent or failed |
| `logexport_cursors` (new) | `(export_id, container)` primary key, `ts` (RFC 3339 with nanoseconds, as the engine gives it), `skip` |
| `logexport_stats` (new) | `export_id` primary key, `lines_sent`, `bytes_sent`, `last_sent_at`, `last_error`, `lag_seconds` |
| Managed secrets | `objectstore-credentials` in each project other than `default` that has a bucket (system-owned, ADR-035) |
| `resource_refs.field` values | `spec.channels[<i>]`, `spec.email.service`, `spec.webhook.secret`, `spec.secret`, `spec.destination.bucket.bucket`, `spec.destination.s3.credentialsSecret`, `spec.destination.<type>.headersSecret`, `spec.<provider>.tokenSecret`, `spec.<provider>.credentialsSecret`, `spec.source.store` |
| Docker and MinIO objects | Containers and networks of projects other than `default` carry `.<project>` (ADR-035); MinIO users `es-p-<project>` and policies `es-p-<project>` |

## 10 · v0.8 changes (planned)

These ship as migration `0013_machines` with the phases they serve (`L2-010`, ADR-042 – ADR-048). It leaves existing rows valid: everything is on the main machine.

| Change | Detail |
|---|---|
| `machines` (new) | `id` primary key (`main`, or `mch_…`), `name` unique, `state` (`waiting`/`online`/`offline`), `state_since`, `labels_json`, `schedulable`, `address`, `agent_port`, `tunnel_port`, `tunnel_public_key`, `cert_serial`, `cert_not_after`, `info_json` (os, arch, versions, CPUs, memory, VM support), `last_seen_at`, `join_code_hash` (unique, null once used), `join_expires_at`, `created_at`. `main` is inserted with the name `main`; its title is the machine name in `settings` |
| `machine_ca` (new) | one row: `cert_pem`, `key_ciphertext` (sealed with the master key, ADR-043), `created_at` |
| `resources` | Rebuilt by `0013`: gains `machine_id TEXT NOT NULL DEFAULT 'main' REFERENCES machines(id)` and `stack_id TEXT` (the stack a member belongs to); indexes `(machine_id)` and `(stack_id)`. `kind` CHECK gains `stack`. `requested_action` gains `move` |
| `address_pool` (new) | `(network, address)` primary key, `machine_id` (cascades with the machine); `network` is the engine network's name (`enclave` for the default network) (ADR-045) |
| `machine_events` (new) | `id` autoincrement, `machine_id`, `type`, `message`, `created_at`; 200 newest per machine kept |
| `catalogue_templates` (new) | `(name, version)` primary key, `title`, `body`, `created_at`, `created_by` (ADR-048) |
| Settings | `mesh_default_range`: the default network's range, read from the main machine when the second machine joins |
| Stack secrets | `<stack>-<param>` for each `secret` parameter and every `generate` member, owned by the stack (`owner_id`) |
| `resource_refs.field` values | `spec.machine` is not a reference (machines aren't resources); `spec.template` names a catalogue entry, checked by the service |
| Docker objects | On every machine: `es-system-mesh` and the volume `es-system-mesh-data` (two or more machines); stand-ins `es-standin-<12 hex>` labelled `enclave.standin-of=<container>` and `enclave.standin-machine=<machine id>`; every managed object gains the label `enclave.machine=<machine id>` |
| Agent state | `~/.enclave-agent`: `key.pem`, `cert.pem`, `ca.pem`, `agent.json` (machine ID, server URL, ports), mode 0700/0600; `files/` holds uploaded snapshot copies by digest |

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler Labs | Schema, identifiers, transactions, retention |
| v2 | 2026-10-20 | Soupler Labs | §5 v0.3 changes (planned): new kinds, serving_generation, next_run_at, consecutive_failures, runs table, refs, settings, retention |
| v3 | 2026-10-21 | Soupler Labs | serving_revision shipped in migration 0002 |
| v4 | 2026-10-21 | Soupler Labs | Domain kind and settings shipped; rebuild migrations |
| v5 | 2026-10-21 | Soupler Labs | Jobs schema shipped (0004); fixed-width timestamps (0005) and the rule in §1 |
| v6 | 2026-10-21 | Soupler Labs | Backup kind shipped (0006); artifact_json cleared by retention |
| v7 | 2026-10-21 | Soupler Labs | Builds table shipped (0007) |
| v8 | 2026-10-21 | Soupler Labs | §6 v0.4 changes (planned): new kinds (0008), references, certificates table (0009), ACME account setting, Docker object names |
| v9 | 2026-10-21 | Soupler Labs | §6 shipped: 0008_network_kinds and 0009_certificates |
| v10 | 2026-10-21 | Soupler Labs | §7 v0.5 changes (planned): compute kinds (0010), references, files under the data dir, Docker objects |
| v11 | 2026-10-21 | Soupler Labs | §7 shipped (0010_compute_kinds); references and files as stored |
| v12 | 2026-10-21 | Soupler Labs | §8 v0.6 changes (planned): new kinds and the purge action, transcoder runs and seen objects, subscription counters, managed secrets, references, engine objects |
| v13 | 2026-10-22 | Soupler Labs | §9 v0.7 changes (planned): projects and the project column, users and roles, members, invites, tokens with roles, audit, metric series, points and rollups, alert state, incidents, notifications, log export cursors and stats, refs |
| v14 | 2026-10-22 | Soupler Labs | §10 v0.8 changes (planned): machines, machine CA, machine and stack columns on resources, address pools, machine events, catalogue templates, stack secrets, engine labels, agent state |
