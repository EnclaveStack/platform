# L2-004 · Control Plane and Resource Lifecycle PRD

---
| Field | Value |
|---|---|
| Document ID | L2-004-control-plane-resource-lifecycle-prd-v1 |
| Layer | L2 — Product Requirements |
| Status | Draft |
| Version | v3 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-21 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

What the EnclaveStack control plane must do for every service kind, independent of which surface (console `L2-001`, CLI `L2-002`, OpenTofu `L2-003`) asks. Traces to `L1-001` §2 and §6. Engineering: `L3-001`, `L3-002`, `L3-003`, `L3-005`, `L3-007`.

## 1 · Service kinds in v0.2

| Kind | What the user gets | Replaces | Required input | Optional input | What the user reads back |
|---|---|---|---|---|---|
| `database` | A PostgreSQL server with one database and one owner user | RDS | name | version (`16` default, `17`) | host, port, internal host/port, database, username; password via its managed secret |
| `cache` | A Redis server, persisted to disk | ElastiCache | name | max memory MiB (64–4096, default 256) | host, port, internal host/port |
| `bucket` | A bucket on the machine's S3-compatible object store | S3 | name | versioning (off) | endpoint, internal endpoint, bucket, credentials secret name |
| `app` | A long-running container from any image | ECS / App Service | name, image | port, environment, secret environment, command, publish on host, memory MiB | host, port (when published), internal host/port |
| `secret` | An encrypted value apps can consume as an environment variable | Secrets Manager | name, value | — | version, last updated — **never the value**, except through an explicit, audited reveal |

## 2 · Requirements

| ID | Requirement | Priority |
|---|---|---|
| CP-01 | Every kind supports create, read, list, update and delete through one HTTP API; no surface has a private path | Must |
| CP-02 | Requests are accepted immediately (`202`) and fulfilled asynchronously; the resource exposes a phase (`pending → provisioning → ready`, `updating`, `deleting`, `failed`) and a human message | Must |
| CP-03 | Names are unique per kind, DNS-safe (`a-z`, `0-9`, `-`, 3–40 chars, starts with a letter) and immutable | Must |
| CP-04 | Fields that cannot change in place (database engine and major version, bucket name) are rejected on update with a clear error, never silently ignored | Must |
| CP-05 | A create retried with the same idempotency key returns the original result instead of creating a duplicate | Must |
| CP-06 | Concurrent updates are detected: an update carrying a stale version is rejected | Must |
| CP-07 | Transient failures (image pull, engine busy, health not yet passing) are retried automatically with backoff; permanent failures (bad image, invalid config) stop immediately with an actionable message | Must |
| CP-08 | A resource that failed can be retried on demand (`reconcile` action) or by changing its spec | Must |
| CP-09 | After a crash, restart or power loss, every in-flight operation resumes to completion without operator action | Must |
| CP-10 | If a running service disappears or stops outside EnclaveStack (drift), the control plane notices within 60 s and restores it | Should |
| CP-11 | Apps can restart (same version) and redeploy (pull the image again, recreate); both are visible as events | Must |
| CP-12 | Deleting a secret an app still uses, or a database's managed password, is refused with the list of dependants | Must |
| CP-13 | Each resource keeps an event history (created, updated, provisioning steps, failures, actions, drift) | Must |
| CP-14 | Container logs for apps, databases and caches are readable (last N lines) | Must |
| CP-15 | Secret values are encrypted at rest and never appear in API responses, logs, events or errors; reveal is a separate, audited call requiring write access | Must |
| CP-16 | The machine reports its name, version, engine availability, CPU, total memory and memory reserved by resources | Must |
| CP-17 | If the container engine is unavailable, the API still answers reads and reports the engine as down; writes are accepted and queued until it returns | Should |
| CP-18 | Health (`/healthz`, `/readyz`) and Prometheus metrics (`/metrics`) are exposed for monitoring | Must |
| CP-19 | Deleting a database, cache or bucket deletes its data; the console, CLI and provider say so before confirming | Must |

## 3 · Out of scope for v0.2

Domains/HTTPS, scheduled backups, multiple users and roles, multiple machines, resource quotas, image builds from source (apps run existing images).

## 4 · v0.3

The kinds `domain`, `job` and `backup`, zero-downtime deploys and builds from source are specified in `L2-005`. Every rule above applies to them unchanged.

## 5 · v0.4

The kinds `network`, `firewall`, `loadbalancer`, `zone` and `record`, placement in networks, private DNS, replicas and DNS-01 certificates are specified in `L2-006`. Every rule above applies to them unchanged.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler | Service kinds, lifecycle, idempotency, concurrency, retry, recovery, drift, dependency and secret rules (CP-01 – CP-19) |
| v2 | 2026-10-20 | Soupler | §4: v0.3 kinds and capabilities are specified in L2-005 |
| v3 | 2026-10-21 | Soupler | §5: v0.4 kinds and capabilities are specified in L2-006 |
