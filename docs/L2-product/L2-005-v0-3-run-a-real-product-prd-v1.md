# L2-005 · v0.3 "Run a Real Product" PRD

---
| Field | Value |
|---|---|
| Document ID | L2-005-v0-3-run-a-real-product-prd-v1 |
| Layer | L2 — Product |
| Status | Draft |
| Version | v2 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-21 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs |
| License | Apache-2.0 |
---

## Purpose

This PRD sets what v0.3 adds so that a team can run a production web product on EnclaveStack. That covers four needs:
- reachable on its own domain with HTTPS;
- deployed without downtime;
- backed up on a schedule;
- able to run periodic work.

It traces to the roadmap in `L1-002` §4 (v0.3) and builds on the lifecycle rules in `L2-004`, which apply to every new kind unchanged. The surface requirements live in their own PRDs:
- console: `L2-001` CON-19 – CON-25;
- CLI: `L2-002` CLI-13 – CLI-17;
- OpenTofu: `L2-003` TF-11 – TF-15.

Engineering: `L3-002` v4, `L3-003` ADR-015 – ADR-019, `L3-004` DAR-008 – DAR-011, `L3-005` v2.

## 1 · The user story

> A developer has `api` (an app), `orders` (a database) and `sessions` (a cache) running on EnclaveStack v0.2. They want `api.example.com` to serve the app over HTTPS. Pushing a new image must not drop requests. The database must be backed up every night, both to the machine and to off-site S3. A cleanup command must run every hour. They want to do all of this from whichever surface they prefer, and have OpenTofu describe it all.

Galiaraa's own needs map onto this one-to-one: the gateway on a domain, rolling deploys of services, RDS-style snapshots and scheduled workers.

## 2 · New kinds and capabilities

| Kind / capability | What the user gets | Replaces (AWS / GCP) | Required input | Optional input | What the user reads back |
|---|---|---|---|---|---|
| `domain` (new kind) | A hostname routed to an app's port, with a TLS certificate obtained and renewed automatically | Route 53 alias + ACM + ALB listener / Cloud DNS + managed certificate | name, hostname, app | port (default: the app's port), TLS mode `auto` / `internal` / `off` (default: `internal` for `*.localhost`, else `auto`), HTTP → HTTPS redirect (default on) | URL, certificate state (`pending`, `issued`, `failed`, `none`), certificate expiry, public IP the hostname must resolve to |
| Zero-downtime deploys (app capability) | Image or setting changes start a new revision, wait until it is healthy, then switch traffic and stop the old one; a revision that never becomes healthy is discarded and the old one keeps serving | ECS rolling deployment / Cloud Run revisions | — | health check: `http` (path), `tcp`, `command` or `none`; interval, timeout, retries, start period; strategy `rolling` (default) or `recreate` | serving revision, last revision outcome |
| `job` (new kind) | A command run on a cron schedule, with run history and logs | EventBridge Scheduler + ECS task / Cloud Scheduler + Cloud Run job | name, schedule, and either an app (inherits its image and environment) or an image | command, time zone, timeout, overlap policy, memory, extra environment, secret environment | next run, last run outcome; run history with status, duration, exit code and log tail |
| `backup` (new kind) | Scheduled logical backups of a database or cache, kept with a retention policy, restorable on demand | RDS automated backups / Cloud SQL backups | name, source (a database or cache), destination (a EnclaveStack bucket, or external S3) | schedule (default daily 03:00), retention count (default 7) and days, path prefix | next run, last run outcome; run history with size and object key |
| Build from source (app capability) | An app built from a Git repository and a Dockerfile instead of a prebuilt image; a redeploy builds the latest commit of the ref | CodeBuild + ECR / Cloud Build + Artifact Registry | Git URL, ref | Dockerfile path, build context path, credentials secret for private repositories, build arguments | built commit, image ID, build log |

## 3 · Requirements

### Domains

| ID | Requirement | Priority |
|---|---|---|
| DOM-01 | A domain routes HTTP and HTTPS for its hostname to one app port through a single edge proxy that EnclaveStack manages on ports 80 and 443 | Must |
| DOM-02 | Hostnames are unique across all domains, lowercase, valid DNS names (≤ 253 chars, labels ≤ 63), and may not be an IP address. Wildcards are out of scope for v0.3 | Must |
| DOM-03 | TLS `auto` obtains and renews a publicly trusted certificate (ACME HTTP-01) and serves it with no user action. Renewal starts 30 days before expiry | Must |
| DOM-04 | TLS `internal` issues a certificate from a EnclaveStack certificate authority created on first use. Its root certificate can be downloaded so the operator can trust it. `*.localhost` hostnames default to `internal` and need no DNS | Must |
| DOM-05 | While a certificate cannot be obtained (DNS does not point here, port 80 is unreachable), the domain stays `provisioning`. The message names the cause and what to do ("Point api.example.com's A record to 203.0.113.7"), and retries follow the normal backoff. After the attempts run out it is `failed`, and the `reconcile` action retries | Must |
| DOM-06 | Deleting an app that a domain points at is refused with the domain listed (same rule as CP-12). Deleting a domain removes its route at once; its certificate is kept for 30 days in case it is re-created | Must |
| DOM-07 | If ports 80 or 443 are already taken on the machine, every domain reports it instead of failing silently, and `GET /machine` shows the edge as unavailable with the reason | Must |
| DOM-08 | Changing a domain's app or port switches traffic without dropping in-flight requests; changing its hostname is a replacement (immutable, CP-04) | Must |
| DOM-09 | Requests through the edge reach the app with the original `Host`, the client IP in `X-Forwarded-For`, and the scheme in `X-Forwarded-Proto` | Must |

### Zero-downtime deploys

| ID | Requirement | Priority |
|---|---|---|
| DEP-01 | An app update or redeploy starts a new revision next to the serving one, waits for its health check, then moves internal traffic (the app's internal host) and edge traffic (its domains) to it, then stops the old revision after a 10 s drain | Must |
| DEP-02 | Health checks: `http` (GET path, success = 2xx/3xx), `tcp` (connect), `command` (run inside the container, success = exit 0), or `none`. Defaults: `http /` when the app has a port and a domain, `tcp` when it has only a port, otherwise "running for 5 s without restarting" | Must |
| DEP-03 | A revision that fails its health check within its budget (start period + retries × interval, ≤ 15 min) is stopped and removed. The old revision keeps serving. The app is `failed` with the probe's last result and log tail in the message, and `outputs.servingRevision` still names the old revision | Must |
| DEP-04 | Apps published on a host port cannot run two revisions at once. They use `recreate` (stop, then start) automatically and say so in the deploy events. Domains are the recommended way to expose an app | Must |
| DEP-05 | Strategy `recreate` can be chosen explicitly, e.g. for apps that must never run twice (single-writer workers) | Should |
| DEP-06 | Each deploy records events: revision started, health check passing or failing (attempt n of m), traffic switched, old revision stopped. Rolling deploys of an app with a domain drop no requests under continuous load | Must |

### Scheduled jobs

| ID | Requirement | Priority |
|---|---|---|
| JOB-01 | A schedule is a 5-field cron expression or one of `@hourly`, `@daily`, `@weekly`, `@monthly`, evaluated in the job's IANA time zone (default: the machine's) | Must |
| JOB-02 | A job either references an app (it runs that app's current image with its environment and secret environment, plus the job's own overrides) or names an image directly | Must |
| JOB-03 | Overlap policy `forbid` (default: a due run is skipped and recorded as skipped while one is running) or `allow` | Must |
| JOB-04 | Each run records trigger (schedule or manual), status (`running`, `succeeded`, `failed`, `timed_out`, `skipped`), start, end, exit code and the last 64 KiB of output. Runs are kept for 30 days or the newest 100 per job | Must |
| JOB-05 | A run exceeding its timeout (1 s – 24 h, default 1 h) is stopped and marked `timed_out` | Must |
| JOB-06 | If the machine was off when a run was due, at most one missed run starts on recovery, and only if it was due within the last schedule interval; the rest are recorded as skipped | Must |
| JOB-07 | The `run` action starts a run now, subject to the overlap policy | Must |
| JOB-08 | Deleting an app referenced by a job is refused (CP-12) | Must |

### Backups

| ID | Requirement | Priority |
|---|---|---|
| BAK-01 | A backup's source is a database (PostgreSQL logical dump of its database with `pg_dump`, custom format) or a cache (Redis RDB snapshot) | Must |
| BAK-02 | Destination is a EnclaveStack bucket, or external S3-compatible storage (endpoint, region, bucket, credentials secret). Objects are written under `<prefix>/<source-name>/<UTC timestamp>-<run id>.<ext>` | Must |
| BAK-03 | Dumps stream from the source to the destination without being written in full to the control plane's disk | Must |
| BAK-04 | Retention: after each successful run, objects beyond the newest `count` and older than `days` are deleted. A run never deletes the only successful backup | Must |
| BAK-05 | Runs follow JOB-04 – JOB-07: history, timeout (default 2 h), at most one catch-up run, `run` action, overlap always forbidden | Must |
| BAK-06 | Restore into a new resource: `restore` with `{runId, mode: "new", name}` creates a new database or cache from the backup and leaves the source untouched. This is the default and the recommended path | Must |
| BAK-07 | Restore in place: `restore` with `{runId, mode: "replace"}` overwrites the source after the user types its name. A new backup of the current state is taken first, and the restore aborts if that fails | Must |
| BAK-08 | Deleting a backup leaves its stored objects in place and says so. Deleting a source database or bucket that a backup uses is refused (CP-12) | Must |
| BAK-09 | A run that fails (source down, destination unreachable, credentials rejected) records why; three failures in a row put the backup into `failed` | Must |

### Builds

| ID | Requirement | Priority |
|---|---|---|
| BLD-01 | An app has exactly one of `image` or `source`. A source app is built from Git (`https://` or `ssh://`; private repositories through a credentials secret holding a token or SSH key) with the repository's Dockerfile | Should |
| BLD-02 | A build is skipped when the commit and build inputs are unchanged since the serving revision | Should |
| BLD-03 | Build logs stream to the app's events and logs. A build failure is permanent for that commit (the message shows the failing step), and the serving revision keeps running (DEP-03) | Should |
| BLD-04 | `redeploy` resolves the ref to its latest commit and builds it. Push webhooks are out of scope for v0.3 | Should |
| BLD-05 | Built images are kept for the serving and previous revision only. Older ones are removed after a successful deploy | Should |

## 4 · Applies to every new kind

`L2-004` CP-01 – CP-19 apply unchanged to `domain`, `job` and `backup`:
- one API, asynchronous phases, idempotent creates, `If-Match`, retry and backoff;
- crash recovery, events, and deletion rules;
- parity across the console, CLI and OpenTofu.

## 5 · Order of delivery

1. **Zero-downtime deploys and health checks.** The domain switch depends on revisions.
2. **Domains.**
3. **Jobs.** Jobs bring the scheduler and the run history that backups reuse.
4. **Backups.**
5. **Builds.** Builds are *Should*: v0.3 can ship without them if the build spike in DAR-010 fails.

**Delivery status (2026-10-21):** all five phases are built and tested on every surface; v0.3 is not released yet. The DAR-010 build spike passed, so builds are in.

| Phase | Requirements | Built | Where it's recorded |
|---|---|---|---|
| 1 · Zero-downtime deploys | DEP-01 – DEP-06 | Yes | ADR-016, `L3-002` §6, `L5-001` |
| 2 · Domains + HTTPS | DOM-01 – DOM-09 | Yes | ADR-015 |
| 3 · Jobs | JOB-01 – JOB-08 | Yes | ADR-017 |
| 4 · Backups + restore | BAK-01 – BAK-09 | Yes | ADR-018 |
| 5 · Builds from Git | BLD-01 – BLD-05 | Yes | ADR-019 |

## 6 · Out of scope for v0.3

- Wildcard certificates and DNS-01 challenges (v0.4, with DNS providers).
- Load balancing across several replicas (v0.4).
- Push webhooks, preview environments and a separate image registry (v0.8, with more than one machine).
- App volumes and volume backups (apps have no persistent volumes yet).
- Backup encryption beyond what the destination provides.
- Bucket replication.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler Labs | v0.3 scope: domain, job and backup kinds; zero-downtime deploys; builds from source (DOM, DEP, JOB, BAK, BLD requirements) |
| v2 | 2026-10-21 | Soupler Labs | Delivery status: all five phases built (unreleased) |
