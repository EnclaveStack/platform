# L3-007 · Reliability and Observability

---
| Field | Value |
|---|---|
| Document ID | L3-007-reliability-observability-v1 |
| Layer | L3 — Architecture |
| Status | Draft |
| Version | v13 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-22 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs |
| License | Apache-2.0 |
---

## Purpose

The exact behaviour behind ADR-011 (observability) and ADR-012 (resilience): how errors are classified, retried and surfaced; how the system recovers; what it logs and measures. Requirements: `L2-004` CP-07 – CP-10, CP-17, CP-18.

## 1 · Error handling layers

| Layer | Rule |
|---|---|
| Driver | Wraps every failure with context (`pull image nginx:1.27: …`). Marks unrecoverable ones `engine.Permanent` (image not found / unauthorized, invalid reference, port already allocated on a fixed port, invalid config). Everything else is transient. |
| Reconciler | Transient → `attempts+1`, `next_attempt_at = now + backoff(attempts)`, phase unchanged, event `retry_scheduled`, log `warn`. Permanent or attempts = 8 → `phase=failed`, `message` = the driver's message, event `failed`, log `error`. Panics in a worker are recovered and treated as transient. |
| Service | Domain and validation errors become typed `api.Error` values (code, status, details). Unexpected errors become `INTERNAL`. |
| HTTP | One `writeError` path renders the L0-003 envelope with the request ID; 5xx are logged with the cause, 4xx at `info`. Recovery middleware converts panics to `500 INTERNAL`. |
| Clients | SDK/console/provider retry network errors and `429/502/503/504` (3 attempts, backoff 250 ms × 2ⁿ ± 20 %, honour `Retry-After` up to 30 s). Creates are safe to retry because of the idempotency key. |

## 2 · Backoff

`delay(n) = min(2 s × 2ⁿ⁻¹, 5 min) × U(0.8, 1.2)` for attempt `n ≥ 1`; at most 8 attempts (~8.5 min total before `failed`).

## 3 · Timeouts

| Operation | Timeout |
|---|---|
| Image pull | 10 min |
| Container create / start / remove | 2 min each |
| Wait for healthy | 2 min (then transient failure) |
| Bucket operations | 30 s |
| HTTP read header / read / write / idle | 5 s / 30 s / 60 s / 120 s |
| One reconcile operation | 30 min (image pull + health budget ≤ 15 min + switch) |
| Graceful shutdown | 15 s for requests, then workers cancelled |

## 4 · Recovery

- **Restart / crash**: resources in `pending`, `provisioning`, `updating`, `deleting` are picked up by the scheduler on start (their `next_attempt_at` is in the past). Drivers are idempotent: "ensure container X at generation G" inspects first and only creates what is missing.
- **Drift** (every 60 s): each `ready` resource's runtime is inspected; if missing or stopped, the resource moves to `provisioning` with event `drift_detected` and is re-ensured.
- **Engine down**: `/readyz` stays 200 (the database is fine); `engine.available=false` in `/machine`; reconcile attempts fail transiently and resume when the engine returns; `enclave_engine_up` = 0.

## 5 · Logging

slog JSON (text on a TTY). Every HTTP request logs once on completion: `request_id, method, route, status, duration_ms, principal`. Reconcile logs `resource_id, kind, generation, step, attempt, duration_ms, error`. Redaction drops attributes named like `password, secret, token, value, authorization, cookie` and the logger never receives spec maps of secrets. `--log-level debug|info|warn|error` (default `info`).

## 6 · Metrics and probes

| Name | Type | Labels |
|---|---|---|
| `enclave_http_requests_total` | counter | `method`, `route`, `status` |
| `enclave_http_request_duration_seconds` | histogram | `method`, `route` |
| `enclave_reconcile_total` | counter | `kind`, `result` (`ready`, `retry`, `failed`, `deleted`) |
| `enclave_reconcile_duration_seconds` | histogram | `kind` |
| `enclave_resources` | gauge | `kind`, `phase` |
| `enclave_engine_up` | gauge | — |

`/healthz` → 200 while the process serves. `/readyz` → 200 when SQLite answers `SELECT 1`, else 503.

## 7 · Memory

- argon2id needs 64 MiB per hash (L3-006 §3). The timing-equaliser hash used for unknown emails is computed on first use, not at start-up — computing it eagerly made every CLI command, even `enclave version`, peak at 89 MB (now 22 MB).
- After each password operation (setup, sign-in) the 64 MiB working set is returned to the OS in the background (`auth.ReleaseMemory`), because the control plane shares the machine with user workloads.
- Measure footprint as the OS charges it: on Linux, RSS; on macOS, the *physical footprint* (`vmmap --summary <pid>`) — macOS RSS keeps counting pages Go has already released.

## 8 · v0.3 behaviour (planned)

| Topic | Rule |
|---|---|
| Rolling deploy (ADR-016) | A probe failure is not a reconcile error: the attempt counts towards the health budget, not the 8-attempt backoff. Exhausting the budget is permanent for that generation, and the serving revision is untouched. Engine errors while starting the revision stay transient as today |
| Edge | Edge drift (container missing or stopped) is repaired within 60 s. A port conflict on 80/443 is permanent for the edge and reported on every domain until it is fixed and `reconcile` is run. Certificate renewal belongs to Caddy, which renews once a third of a certificate's lifetime is left. Each drift pass re-reads ready domains' certificates and records issuer and expiry. A domain goes back to `provisioning` only when its certificate is gone, expired, or has less than a tenth of its lifetime left. The rule is relative because internal certificates live 12 hours and ACME ones 90 days; a fixed 7-day window looped every internal domain (found in the v0.3 live check) |
| Scheduler (ADR-017) | Exactly-once start through the claim transaction; catch-up at most once; `running` runs are reconciled at start. The scheduler stops claiming new runs while the engine is down; occurrences missed meanwhile follow the catch-up rule |
| Backups (ADR-018) | A failed upload aborts its multipart upload, so no partial objects are left. The SHA-256 is verified by reading the object back on restore before anything is changed |
| Timeouts | Health budget ≤ 15 min; job timeout 1 s – 24 h (default 1 h); backup default 2 h; clone 5 min; build 30 min; `caddy reload` 30 s |
| Logs | Deploy, probe, run and build log lines carry `resource_id`, `revision` or `run_id` |
| Metrics | `enclave_deploys_total{result}`, `enclave_health_probe_seconds`, `enclave_runs_total{kind,status}`, `enclave_run_duration_seconds{kind}`, `enclave_backup_bytes`, `enclave_certificate_expiry_seconds{hostname}`, `enclave_edge_up` |

## 9 · v0.4 behaviour (built 2026-10-21)

| Topic | Rule |
|---|---|
| Networks (ADR-020) | Creating a network that overlaps one outside EnclaveStack is permanent with the engine's message. System services missing from a network are reconnected by the drift pass. A database or cache move disconnects and reconnects one container; an app move is a rolling deploy |
| Firewalls (ADR-021) | Applying is idempotent and atomic per target. A helper failure is transient (retried with backoff) unless nftables rejects the ruleset, which is permanent and names the target. After a control-plane restart every policy is applied again. Drift re-applies within 60 s for new or restarted containers |
| Replicas (ADR-022) | A deploy succeeds only when every new replica passed its health check. A replica that disappears in steady state triggers a new revision, not a partial repair |
| Load balancers | A configuration HAProxy rejects is permanent and the running one keeps serving. Replica discovery refreshes every second. `healthyTargets` is refreshed on each drift pass; zero healthy targets is reported in the message but is not a failure of the load balancer itself |
| DNS records (ADR-023) | Provider calls time out after 30 s. `429` honours `Retry-After`; `5xx` and network errors are transient; `401`/`403` are permanent. Records are re-read every 10 minutes |
| DNS-01 (ADR-024) | An order is bounded by 10 minutes, propagation wait included. Renewal starts with a third of the lifetime left; a domain fails only with less than a tenth left. Challenge records are removed even when the order fails |
| Timeouts | Network create 30 s; firewall apply 30 s per container; HAProxy check and reload 30 s; provider call 30 s; ACME order 10 min |
| Metrics | `enclave_firewall_apply_total{result}`, `enclave_firewall_apply_seconds`, `enclave_lb_healthy_targets{name}`, `enclave_dns_calls_total{provider,result}`, `enclave_acme_orders_total{challenge,result}` |

## 10 · v0.5 behaviour (built 2026-10-21)

| Topic | Rule |
|---|---|
| VM availability (ADR-025) | QEMU, firmware or acceleration missing is not a failure: the VM stays `pending` with the fix in its message and retries with backoff; `GET /machine` reports the same |
| VM processes | QEMU survives control-plane restarts. On start, running VMs are adopted by pidfile and QMP, or started. A QEMU that exits without the guest shutting down is a crash: drift starts it again within 60 s. A guest shutdown sets `power: stopped` with an event |
| Stop | ACPI power-down, then `quit` after 60 s; a QEMU that is still alive 10 s later is killed |
| VM link (ADR-026) | QEMU reconnects to its link every second, so a recreated or restarted link container restores traffic without touching the VM. Drift recreates a missing link container on the same host ports, with the same device address; measured live: traffic back 10 s after the link was removed |
| Images | Downloads go to a temporary file and are renamed only after the checksum matches; an interrupted download starts over. Network errors are transient; `404` and checksum mismatches are permanent |
| Snapshots and copies (ADR-028) | Copies write `.partial` files and rename on success; a reconcile resuming after a crash deletes the partial file and copies again. A failed `drive-backup` job is reported with QEMU's error |
| Timeouts | QMP command 10 s; VM start (process up and QMP ready) 60 s; image download 30 min; disk copy and snapshot 2 h; archive copy (filesystem) 2 h |
| Metrics | `enclave_vms{power}`, `enclave_vm_starts_total{reason}` (`create`, `start`, `crash`, `adopt_failed`), `enclave_image_downloads_total{result}`, `enclave_snapshots_total{type,result}`, `enclave_snapshot_seconds{type}` |

## 11 · v0.6 behaviour (planned)

| Topic | Rule |
|---|---|
| Broker (ADR-029) | A missing or stopped broker is drift: the reconciler starts it again on its volume and every queue and topic re-checks its stream. While it is down, data endpoints answer `503` and queue and topic reconciles wait as for an unavailable engine |
| Deliveries | At least once; a webhook failure is retried with backoff up to 10 attempts; a subscription's goroutine stops when its topic is no longer ready and restarts when it is. Dead-letter advisories are kept in a stream, so moves happen after a control-plane restart |
| Engines with slow first starts | OpenSearch and ClickHouse get 3 minutes to become healthy (others 2) |
| Transcoders | A watch listing that fails is retried on the next tick; a run interrupted by a restart is marked failed with "interrupted" and its key isn't retried automatically (TRN-04 converts it again) |
| CDN | The cache survives container recreation (its volume); origin errors serve stale copies up to a day |
| Timeouts | Broker request 10 s; receive wait ≤ 20 s; webhook delivery 10 s; SMTP send 30 s; dump and restore as ADR-018 |
| Metrics | `enclave_messages_sent_total{kind}`, `enclave_deliveries_total{target_type,result}` (`delivered`, `retry`, `gave_up`), `enclave_dead_letters_total`, `enclave_purges_total{kind}` (queues and CDNs). Transcodes use the shared run metrics: `enclave_runs_total{kind="transcoder",status}` and `enclave_run_duration_seconds{kind="transcoder"}` |

## 12 · v0.7 behaviour (planned)

| Topic | Rule |
|---|---|
| Sampler (ADR-037) | Every 30 s; a sample that can't reach the engine is skipped (a gap, not zeros). Counters reset by a container restart are detected (a value lower than the last) and the interval stores no rate. A pass over 50 resources must finish within 5 s; one that is still running when the next is due is skipped and counted |
| Storage | Points written in one transaction per sample; the 15-minute sweep rolls up and deletes old points in batches of 10 000 rows so writers aren't blocked |
| Alerts (ADR-038) | Evaluated after each sample; state is saved before notifications are queued, so a crash between the two re-checks rather than double-sends. After a restart, the first evaluation compares against the saved state |
| Notifications (ADR-039) | Outbox polled every 2 s; retries after 15, 30, 60, 120, 240 s; then failed with an event on the channel |
| Log export (ADR-040) | One pass per export every `flushSeconds`; a failing destination backs off up to 5 min and catches up afterwards; checkpoints are saved after each accepted batch |
| Secret stores (ADR-041) | Secrets with a source are due at `next_run_at`; a failed fetch is retried with the reconciler's backoff and the last value kept |
| Timeouts | Engine stats 10 s per pass; edge metrics exec 5 s; notification request 10 s; SMTP 30 s; log batch request 30 s; secret store request 15 s |
| Metrics (of EnclaveStack itself) | `enclave_samples_total{result}` (`ok`, `skipped`), `enclave_sample_duration_seconds`, `enclave_alert_transitions_total{to}`, `enclave_notifications_total{type,result}`, `enclave_log_lines_exported_total{destination}`, `enclave_log_export_errors_total{destination}`, `enclave_secret_syncs_total{provider,result}`, plus `enclave_resource_value{project,kind,name,metric}` (MET-07) |

## 13 · v0.8 behaviour (planned)

| Topic | Rule |
|---|---|
| Machine health (ADR-046) | `info` every 10 s per machine, 5 s timeout; offline after 30 s without an answer; online at the first answer, after `Sweep`. Facts (CPUs, memory, VM support, versions, tunnel key) refresh with each answer |
| Calls to an offline machine | Fail at once with `ErrMachineOffline`; the reconciler retries every 30 s without counting an attempt, as for an unavailable engine; drift skips the machine |
| Restarting elsewhere | 60 s after a machine went offline; at most once per outage per app; the new revision is a normal deploy with health checks |
| Mesh sync (ADR-045) | Every 5 s and at once after a placed resource's containers change; the whole configuration is applied idempotently. A machine that can't be configured keeps its last configuration; the error is an event on the machine, once per change |
| Address pools | Topped up before each placement and each sync to 8 free per machine, network and subnet in use (fewer in small subnets); an empty pool makes the placement wait (transient) |
| Agent calls | Deadline of the reconciler's operation, passed to the agent; streams carry their own error trailer. A dropped connection is transient and retried. The agent refuses calls when its engine is down with `unavailable`, as the local driver does |
| Agent restart | The agent starts with no state but its identity; the next mesh sync configures it again. Containers keep running across agent restarts |
| Control-plane restart | Machines are marked by their last state; the first health round decides. Moves interrupted mid-copy start again from the copy: the old machine's data is untouched until the switch |
| Moves (ADR-047) | Copy before switch; on failure the source is started again and the resource stays where it was, with `move_failed`. Data is never deleted on the old machine before the new one is healthy |
| Join | A machine `waiting` for more than its code's hour stays listed with "The join code expired"; a new code can be issued |
| Timeouts | Agent `info` 5 s; join 30 s; file upload and data copies bounded by the operation (30 min); mesh configuration 30 s per machine |
| Metrics | `enclave_machines{state}`, `enclave_machine_up{machine}`, `enclave_agent_calls_total{machine,method,result}`, `enclave_agent_call_duration_seconds{machine}`, `enclave_moves_total{kind,result}`, `enclave_move_bytes_total`, `enclave_rescheduled_total`, `enclave_mesh_sync_total{result}`, `enclave_mesh_peers_connected{machine}` |
| Logs | Every call to an agent logs its machine, method, duration and result at debug, and failures at warn; machine state changes at info |

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler Labs | Error layers, backoff, timeouts, recovery, drift, engine outage, logging, metrics, probes |
| v2 | 2026-10-20 | Soupler Labs | Memory policy (argon2id working set, lazy timing hash) and how to measure footprint |
| v3 | 2026-10-20 | Soupler Labs | §8 v0.3 behaviour (planned): rolling deploy failure semantics, edge repair and certificates, scheduler, backups, timeouts, logs, metrics |
| v4 | 2026-10-21 | Soupler Labs | Reconcile operation timeout 30 min (rolling deploys) |
| v5 | 2026-10-21 | Soupler Labs | Certificate refresh rule relative to the certificate lifetime |
| v6 | 2026-10-21 | Soupler Labs | §9 v0.4 behaviour (planned): networks, firewall apply and drift, replicas, load balancers, DNS calls, DNS-01 orders, timeouts, metrics |
| v7 | 2026-10-21 | Soupler Labs | §9 behaviour and metrics built |
| v8 | 2026-10-21 | Soupler Labs | §10 v0.5 behaviour (planned): VM availability, processes and adoption, stop, link reconnect, images, snapshot copies, timeouts, metrics |
| v9 | 2026-10-21 | Soupler Labs | §10 behaviour and metrics built; link repair measured |
| v10 | 2026-10-21 | Soupler Labs | §11 v0.6 behaviour (planned): broker drift, deliveries, slow starts, transcoder and CDN recovery, timeouts, metrics |
| v11 | 2026-10-22 | Soupler Labs | §11 metrics as built: `enclave_purges_total{kind}` for queues and CDNs; transcodes counted by the shared run metrics |
| v12 | 2026-10-22 | Soupler Labs | §12 v0.7 behaviour (planned): sampler, metric storage, alert evaluation, notification retries, log export, secret sync, timeouts, metrics |
| v13 | 2026-10-22 | Soupler Labs | §13 v0.8 behaviour (planned): machine health, offline calls, restarting elsewhere, mesh sync, address pools, agent calls and restarts, moves, join expiry, timeouts, metrics, logs |
