# L3-003 · Architecture Decision Records

---
| Field | Value |
|---|---|
| Document ID | L3-003-architecture-decision-records-v1 |
| Layer | L3 — Architecture |
| Status | Draft |
| Version | v18 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-24 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

The decisions that shape how EnclaveStack works, numbered after ADR-001 and ADR-002 in `L3-001` §5. Each records context, decision and consequences; where options were weighed, the analysis is in `L3-004` (DAR-NNN). Never renumber; supersede instead.

## ADR-003 — API-first: one control-plane API, three equal clients
- **Status**: Accepted (DAR-002)
- **Context**: `L1-001` promises the console, the CLI and an OpenTofu provider as equal surfaces. Any capability built into one surface's private path would break parity.
- **Decision**: The versioned HTTP API (`/api/v1`, `L3-002`) is the only way to change state. The console uses it through `EnclaveApi`; the CLI and provider through the Go SDK `pkg/client`. Wire types and error codes live once in `pkg/api`.
- **Consequences**: A feature is "done" only when it is reachable from all three surfaces (enforced by the parity e2e suite, `L5-002` §4). The API must offer full create / read / update / delete for every kind, token auth for non-browser clients, idempotent creates and optimistic concurrency.

## ADR-004 — Declarative resources reconciled asynchronously
- **Status**: Accepted (DAR-003)
- **Context**: Starting containers, pulling images and waiting for health take seconds to minutes and can fail transiently. Holding HTTP requests open for that is fragile; OpenTofu needs a stable read-back of desired versus actual state.
- **Decision**: Every resource is stored as **spec** (desired, versioned by `generation`) and **status** (`phase`, `message`, `observedGeneration`, `steps`, `outputs`). Writes return `202` immediately. A reconciler converges actual to desired with a scheduler (queue + 2 s database poll), a bounded worker pool (4), one in-flight operation per resource, exponential backoff with jitter, and periodic drift detection. Drivers must be idempotent.
- **Consequences**: Clients wait by polling (`--wait`, provider timeouts, console polling). Crash recovery is the normal path, not a special case. Phase transitions are a small state machine tested in `internal/domain`.

## ADR-005 — Embedded SQLite as the control-plane store
- **Status**: Accepted (DAR-004)
- **Context**: The control plane runs on one machine next to user workloads; installing and operating a separate database server for EnclaveStack's own state would defeat the one-command install.
- **Decision**: SQLite in WAL mode via the pure-Go driver `modernc.org/sqlite` (no cgo, so cross-compilation stays trivial), one file in the data directory, `busy_timeout` 5 s, foreign keys on, migrations embedded and applied in order at start inside a transaction. All writes go through `internal/store` with explicit transactions.
- **Consequences**: Single-writer semantics are fine at this scale (`L5-003` measures it). Backups of EnclaveStack's own state are a file copy using SQLite's online backup. Multi-machine clustering would need a different store — out of scope until v1.x.

## ADR-006 — Engine drivers behind an interface; Docker first
- **Status**: Accepted (DAR-005)
- **Context**: Provisioning must be testable without Docker, and other runtimes (Podman, containerd) may come later.
- **Decision**: `engine.Driver` (`Ensure`, `Delete`, `Status`, `Restart`, `Redeploy`, `Logs`, `Ping`, `Info`). The **docker** driver uses the official Docker Engine SDK; every object it creates carries labels `enclave.managed=true`, `enclave.resource-id`, `enclave.generation`, lives on the `enclave` bridge network, and uses volumes named `es-<resource-id>-data`. Buckets live on one shared MinIO container (`es-system-objectstore`) driven with `minio-go`. The **fake** driver keeps state in memory with configurable step latency and failure injection; it backs API integration tests, provider acceptance tests, console e2e tests, load tests and demos (`enclave server --driver fake`).
- **Consequences**: Driver contract tests run the same suite against both drivers (docker only when Docker is available). Real-engine behaviour is proven by the Docker e2e suite.

## ADR-007 — Authentication: sessions for browsers, personal API tokens for code
- **Status**: Accepted (DAR-006)
- **Context**: Browsers need cookie sessions; the CLI, CI jobs and OpenTofu need bearer credentials that can be scoped and revoked. A freshly installed control plane must not be claimable by whoever reaches it first.
- **Decision**: Passwords hashed with **argon2id** (64 MiB, 3 iterations, 2 lanes, 16-byte salt). Browser sessions: random 32-byte ID, only its SHA-256 stored, cookie `es_session` `HttpOnly; SameSite=Strict; Path=/` (+ `Secure` when served over TLS), 7-day sliding expiry; state-changing requests with a cookie must carry `X-Enclave-CSRF: 1` (a custom header cannot be sent cross-site without a CORS preflight, which the server never grants). API tokens: `es_pat_` + 32 random bytes (base32), SHA-256 stored, scopes `read` / `write`, optional expiry, last-used tracking, revocable. First-run setup requires a one-time **setup code** printed to the server's terminal (and log) at first start. Login throttling: 5 failures per email+IP per 15 min → `429` with `Retry-After`.
- **Consequences**: Credentials are never stored in recoverable form. `enclave login` exchanges a password for a token via a short-lived session. Full threat model: `L3-006`.

## ADR-008 — One binary for server and CLI; Cobra commands
- **Status**: Accepted
- **Context**: Users install one thing. The CLI and the server share versioning and the SDK.
- **Decision**: `cmd/enclave` builds a single binary; `enclave server` runs the control plane, every other command is a client. Commands use `spf13/cobra`; configuration precedence flag > environment > `~/.config/enclave/config.json` (0600). Exit codes per `L2-002` CLI-06.
- **Consequences**: Server-only dependencies (SQLite, Docker SDK) are linked into the CLI binary too (~30 MB) — accepted for a single-install product.

## ADR-009 — The OpenTofu provider lives in this monorepo; published through a release mirror
- **Status**: Accepted (DAR-007)
- **Context**: The provider must change in lockstep with the API and the SDK. The OpenTofu and Terraform registries require a public repository named `terraform-provider-<name>` with signed GitHub releases.
- **Decision**: Develop the provider here (`internal/provider`, `cmd/terraform-provider-enclave`) on `terraform-plugin-framework` (protocol v6). At public launch, the release pipeline pushes tagged sources to a mirror repository `EnclaveStack/terraform-provider-enclave` and publishes signed builds there with GoReleaser.
- **Consequences**: API, SDK and provider change in one PR and are tested together (acceptance tests boot an in-process control plane with the fake driver). The mirror repository is release plumbing only — no development happens there.

## ADR-010 — Error envelope, idempotency and optimistic concurrency
- **Status**: Accepted
- **Context**: `L0-003` mandates one error shape with stable `UPPER_SNAKE` codes defined once. Retried creates must not duplicate resources (CP-05); concurrent edits must not silently overwrite each other (CP-06).
- **Decision**: Errors: `{"error":{"code","message","requestId","details":[{"field","code","message"}]}}`, codes defined in `pkg/api/errors.go` and generated into the console. `POST` accepts `Idempotency-Key` (≤ 255 chars): the first response is stored for 24 h keyed by (token or user, key) with a hash of the request; a replay returns the stored response, a different body with the same key returns `422 IDEMPOTENCY_KEY_REUSED`. Resources carry `generation`; responses send `ETag: "<generation>"`; `PATCH`/`DELETE` honour `If-Match` and return `412 PRECONDITION_FAILED` on mismatch.
- **Consequences**: The SDK sets an idempotency key on every create and retries safely. The console and provider send `If-Match` on updates.

## ADR-011 — Observability: structured logs, metrics, health probes
- **Status**: Accepted
- **Context**: A control plane that silently retries must make its behaviour visible; secrets must never leak into logs.
- **Decision**: `log/slog` JSON to stdout (text when attached to a terminal), fields `time level msg request_id method path status duration_ms resource_id kind attempt error`. Each request gets `X-Request-Id` (accepted from the client if well-formed, else generated) echoed in responses and error bodies. Levels: `info` lifecycle, `warn` retryable failures and throttling, `error` permanent failures and 5xx. A redaction rule drops any attribute whose key matches `password|secret|token|value|authorization|cookie`. Prometheus metrics at `/metrics`: `enclave_http_requests_total{method,route,status}`, `enclave_http_request_duration_seconds`, `enclave_reconcile_total{kind,result}`, `enclave_reconcile_duration_seconds{kind}`, `enclave_resources{kind,phase}`, `enclave_engine_up`. `/healthz` = process alive; `/readyz` = database reachable (engine status reported, not required).
- **Consequences**: Log scans in the definition of done look for `"level":"ERROR"`; a test asserts secrets never appear in captured logs.

## ADR-012 — Resilience: retries, timeouts, panics, shutdown
- **Status**: Accepted
- **Context**: CP-07, CP-09, CP-17.
- **Decision**: Errors are classified by the driver as **transient** (default) or **permanent** (`engine.Permanent(err)`). Reconcile retries transient errors with exponential backoff (base 2 s, ×2, cap 5 min, ±20 % jitter, 8 attempts). Every driver call runs under a context timeout (pull 10 min, other steps 2 min). HTTP server: `ReadHeaderTimeout` 5 s, `ReadTimeout` 30 s, `WriteTimeout` 60 s, `IdleTimeout` 120 s, request bodies ≤ 1 MiB. A recovery middleware turns handler panics into `500 INTERNAL` with the request ID and logs the stack; the reconciler recovers worker panics into a transient failure. SIGINT/SIGTERM: stop accepting requests, let in-flight requests finish (15 s), cancel workers, wait for them, close the store. Clients (SDK, console) retry network errors and `429/502/503/504` up to 3 times with backoff, honouring `Retry-After`.
- **Consequences**: Behaviour is specified in `L3-007` and proven by the recovery and fault-injection tests in `L5-002`.

## ADR-013 — Object store image: Chainguard's MinIO, pinned by digest
- **Status**: Accepted
- **Context**: Buckets run on one shared S3-compatible store (ADR-006). During implementation, `minio/minio` turned out to be gone from Docker Hub and quay.io (MinIO stopped publishing community images in 2025); the tag originally pinned could not be pulled. Alternatives available: `cgr.dev/chainguard/minio` (MinIO built from source by Chainguard; only a `latest` tag is free), `bitnamilegacy/minio` (frozen, unpatched), `rustfs/rustfs` (young MinIO-compatible project), SeaweedFS and Garage (S3 gateways with partial API coverage and more setup).
- **Decision**: Use `cgr.dev/chainguard/minio`, **pinned by digest** in `internal/engine/docker` (`DefaultObjectStoreImage`) so the mutable tag can never change what runs; `Options.ObjectStoreImage` overrides it. It keeps 100 % MinIO API compatibility for `minio-go` and for users' S3 clients.
- **Consequences**: Image updates are a deliberate digest bump, tested by the Docker driver suite (`TestBucketStoresObjectsAndDeleteEmptiesIt`) and the engine e2e. If Chainguard stops publishing, RustFS is the next candidate behind the same driver code.

## ADR-014 — One design system package: `@enclavestack/ui`
- **Status**: Accepted (founder request 2026-10-20: "all typography and UI cosmetic attributes in a shared package instead of inline")
- **Context**: Tokens lived in `console/src/theme/tokens.css`, but components were ad-hoc CSS classes in the console's `app.css`, 13 inline `style` attributes set sizes and spacing, and the website kept its own copy of the tokens.
- **Decision**: `packages/ui` (`@enclavestack/ui`) is the design system. `tokens.css` is the only place a colour, type size, weight, radius, spacing step, shadow, wash, avatar tone or motion value is written (light and dark). `components.css` and `layout.css` style everything from those tokens. Typed React components cover:
  - **Text and layout**: Text, Heading, Stack, Inline, Grid, VisuallyHidden.
  - **Controls**: Button and `buttonClass` (for router links), IconButton, Chip, Badge, Avatar, Status/Dot, ProgressBar, Skeleton, Alert.
  - **Surfaces**: Panel, Card, Fact, EmptyState, Rows, Disclosure.
  - **Forms**: Field, Input, Select, Textarea, Checkbox, Choice (a radio option as a card, added for the restore drawer), Form, FormRow, Fieldset, KeyValueRow.
  - **Navigation**: SegmentedTabs (arrow-key navigation), TabPanel, StageTrack.
  - **Overlays**: Drawer, ConfirmDialog, ToastProvider/useToast.
  - **Code**: CodeBlock, Code, Terminal.
  - **Application layout**: Shell, Sidebar, NavItemContent/`navItemClass`, Workspace, List and list cards, the detail head/hero/body, Page, Table, AuthLayout, Splash.
  - **Icons**: one curated icon set, from lucide.

  Components accept no `className` or `style` prop. A value that is genuinely data is applied without inline styles: a percentage uses the native `<progress>` element, and a number of steps uses a class variant. The console holds behaviour and copy only. `console/src/design-system.test.ts` fails on any stylesheet, `style=` attribute, colour literal or hand-written class name in `console/src`. `packages/ui/src/tokens.test.ts` checks three things: dark mode overrides the same tokens whether chosen by the OS or forced, every `var(--es-*)` used is defined, and component CSS and TSX contain no colour literals.
- **Consequences**: A visual change is one edit in `packages/ui`. The website (separate repository) still copies the tokens: it can consume `@enclavestack/ui` once the package is published to npm at public launch (`L4-002` §3); until then `marketing/app/globals.css` names `L2.5-001` as its source.

## ADR-015 — Edge proxy: a Caddy container configured by the reconciler
- **Status**: Accepted (DAR-008) — **implemented** in v0.3 phase 2 (2026-10-21), `L2-005` DOM-01 – DOM-09
- **Context**: Domains need one process on ports 80/443 that terminates TLS, obtains and renews certificates, and forwards to apps on the private `enclave` network. On macOS the host cannot reach container IPs.
- **Decision**:
  - **The container.** The system container `es-system-edge` runs Caddy 2.10.2, pinned by digest (as in ADR-013).
    - **Lifecycle.** It is created with the first domain and stopped when the last domain is deleted. Its certificates and CA live in the volume `es-system-edge-data`, which is never deleted with domains.
    - **Exposure.** It binds ports 80 and 443 on `--edge-address`. The default is `127.0.0.1`, so nothing is exposed until the operator opts in with `0.0.0.0` (L3-006 §5). `--edge-http-port` and `--edge-https-port` move it off 80/443, for example for tests.
    - **Rebuilds.** A changed image, address or port recreates the container.
  - **Configuration.** The reconciler renders the **whole** Caddy JSON configuration from all live domains in the store: sorted, deterministic, built as JSON values and never by string templating. Hostnames are validated (`L3-002` §6.5), so a hostname cannot inject configuration.
    - **Apply.** Rendering and applying run under one lock, so an older configuration can never overwrite a newer one.
    - **Delivery.** The file is copied in through the engine's copy API. A fresh container reads it at start; a running one gets `caddy reload` by engine exec, which is atomic and graceful.
    - **Admin endpoint.** It listens only on `localhost` inside the container, so nothing outside it can reconfigure the proxy.
    - **Servers.** An HTTPS server holds the TLS routes, with automatic redirects disabled. An HTTP server holds the redirect routes (308 to `https://{host}{uri}`), the plain-HTTP routes (`tls: off` or `redirectHttp: false`), and a final 404 for unknown hostnames.
  - **Certificates.**
    - `auto` uses ACME HTTP-01, with the account email from the machine setting `acmeEmail`, falling back to the admin's email.
    - `internal` uses Caddy's local CA, named "EnclaveStack Local CA", with trust-store installation disabled. `GET /api/v1/machine/ca` returns its root certificate.
    - The reconciler reads certificate state by a TLS handshake to `127.0.0.1:<https port>` with the hostname as SNI. The leaf must cover the hostname; the reconciler records its issuer and `NotAfter`. No Caddy-internal API is needed.
    - The test-only options `ACMEDirectory` and `ACMERootPEM` point `auto` at Pebble.
  - **Upstreams.** A route targets the app's **stable alias** (`es-app-<name>:<port>`, ADR-016), not a revision container. Caddy retries a failed dial for up to 10 s (every 250 ms), so a request that meets a revision as it stops is retried against the new one.
    - App deploys never touch the edge.
    - The edge is re-rendered only when a domain changes, or when an app that domains route to changes (its port).
    - Measured on real Docker: 16,192 HTTPS requests across a rolling deploy, 0 failed (DEP-06).
  - **Loopback guard.** A domain with `tls: auto` while the edge listens on loopback fails at once. The message says to restart with `--edge-address 0.0.0.0` and forward 80/443, because no public CA could ever reach it.
  - **Address shown in messages.** The public address is not auto-detected; nothing leaves the machine (`L1-001`). The operator sets `publicAddress`, and messages fall back to "this machine's public IP".
- **Consequences**:
  - **Engine contract:** the `Driver` gains `ConfigureEdge`, `Certificate`, `EdgeStatus` and `RootCA`, and the Docker driver gains internal copy and exec helpers (argument arrays only, no shell). The fake driver implements all of them, with injectable pending certificates and port conflicts. Services may call only the read methods (`internal/archtest`).
  - **Port conflicts:** a conflict on 80/443 is a permanent, actionable error. It is shown on every domain and in `GET /machine` (`edge.message`).
  - **Drift and renewal:** edge drift (container removed or stopped) is repaired like any other resource (CP-10). Each drift pass also re-reads ready domains' certificates, keeping issuer and expiry current. A certificate that is gone, expired, or has less than a tenth of its lifetime left (renewal clearly failed) sends the domain back to provisioning. The window is relative: internal certificates live 12 hours.
  - **Schema:** `resources.kind` gains `domain` through migration `0003_domain_kind`, a table rebuild, because SQLite cannot alter CHECK constraints. Rebuild migrations run on one connection with foreign keys off and commit only after `PRAGMA foreign_key_check` passes.
  - **Upgrades:** Caddy upgrades are deliberate digest bumps, tested by the edge suites (`L5-002` §6).

## ADR-016 — App revisions, health probes and rolling deploys
- **Status**: Accepted (DAR-009) — **implemented** in v0.3 phase 1 (2026-10-21), `L2-005` DEP-01 – DEP-06
- **Context**: v0.2 recreates the app container on every change, so each deploy drops traffic, and a bad image takes the app down.
- **Decision**:
  - **Revisions.** Each deploy (a new generation, a redeploy, or a `reconcile` of a failed one) is a revision.
    - **Numbering.** Numbers come from a counter that only grows. The reconciler passes `outputs.latestRevision + 1` to the driver, and a discarded revision's number is spent, so a number never names two different deploys.
    - **Containers.** Each revision's container is `es-app-<name>-r<N>`, labelled `enclave.revision=N`. Every revision carries the network alias `es-app-<name>` (the app's `internalHost`). While two revisions overlap, internal DNS answers with both, which is acceptable because both are healthy.
    - **The serving revision** is stored in `resources.serving_revision` and reported as `outputs.servingRevision`.
  - **Rolling steps** (`strategy: rolling`, the default unless the app publishes a host port; steps *pull → start → health → switch*):
    1. Start the new revision.
    2. Probe it until healthy (budget ≤ 15 min).
    3. Switch traffic: edge routes from phase 2 (ADR-015).
    4. Drain for 10 s.
    5. Stop the old revision (SIGTERM, 10 s grace) and remove it.
    6. The reconciler then records `serving_revision`.
  - **Recreate steps** (`strategy: recreate`, or a published host port, whose port two containers cannot share): stop the old revision, keep its host port for the new one, start it, then probe it. The deploy records a `deploy_recreate` event explaining why.
  - **Probes.**
    - `http` and `tcp` checks run inside the system container `es-system-probe`: busybox 1.37 pinned by digest, idle, on the `enclave` network. They use engine exec with fixed argument arrays: `wget -q -T <timeout> -O /dev/null http://<revision>:<port><path>`, or `nc -z -w <timeout> <revision> <port>`.
    - `command` checks exec the user's argument array inside the new revision.
    - Implicit default: `tcp` on the port every 1 s, up to 60 times, or, with no port, running for 5 s without restarting.
    - A revision that stops or restarts while being probed fails at once.
    - Each attempt is shown as the resource's message ("Health check attempt 3 of 12 · last: …").
  - **Failure.**
    - A revision that exhausts its budget is removed. The error, which is permanent for that spec, carries the probe result and the revision's last log lines.
    - The app goes `failed` with `lastDeploy: failed`, while `servingRevision` still names the old revision, which kept running.
    - With `recreate` there is no old revision, so the message says the app is not running.
    - A `reconcile` action or a new spec tries again with a new number.
  - **Crash recovery.** On start, the reconciler lists the app's containers by label.
    - It keeps the serving revision: the recorded one, or else the newest running one.
    - Any other revision, the leftover of an interrupted deploy, is removed and the deploy starts again from the top.
    - A serving revision the store had not yet recorded is probed before it is accepted.
    - A v0.2 container (no revision label) is adopted as revision 0 and replaced by the next deploy.
- **Consequences**:
  - **Store and outputs:** migration `0002_app_revisions` adds `resources.serving_revision`. Outputs gain `servingRevision`, `latestRevision` and `lastDeploy`.
  - **Engine reporting:** the optional `engine.Narrator` lets drivers report progress messages and the events `revision_started`, `health_check_passed`, `health_check_failed`, `traffic_switched`, `revision_stopped` and `deploy_recreate`.
  - **Actions:** restart (CP-11) restarts the serving revision in place; redeploy creates a new revision.
  - **Timeout:** the reconcile operation timeout grows to 30 min (pull + health budget).
  - **Limit for internal clients:** a client that resolved the old revision just before it stopped can be refused. In the real-Docker test, 3 of 829 internal requests failed during a rolling deploy. Domain traffic switches explicitly and is the zero-drop path (DEP-06, phase 2).

## ADR-017 — Scheduler and run history
- **Status**: Accepted — **implemented** for jobs in v0.3 phase 3 and for backups (BAK-05) in phase 4 (2026-10-21), `L2-005` JOB-01 – JOB-08
- **Context**: Jobs and backups need cron schedules that survive restarts, never overlap unless asked, and keep a history.
- **Decision**:
  - **Schedule evaluation.** Schedules are parsed with the `robfig/cron/v3` standard parser (MIT; parser only, our own loop), limited to 5 fields and `@hourly`, `@daily`, `@weekly`, `@monthly` (`@every` is rejected). `pkg/api` embeds `time/tzdata`, so time zones resolve the same on every host.
  - **Where it runs.** The scheduler is a goroutine of the reconciler (`internal/reconciler/scheduler.go`), so it shares secret resolution, metrics and shutdown with it. It ticks every second.
  - **Claiming.** Each tick lists ready resources whose `next_run_at` is due. Each one is claimed in one `BEGIN IMMEDIATE` transaction that re-reads the row, inserts the `runs` row(s) and advances `next_run_at`, so an occurrence starts exactly once.
  - **Overlap.** Under `forbid`, a due occurrence while a run is `running` is recorded as `skipped`, and the manual run action answers `409 RUN_IN_PROGRESS`.
  - **Catch-up.** When several occurrences are due at once (the machine or engine was down), the earlier ones become one `skipped` row with `skippedCount`, and the latest starts as `catch_up`. Counting stops at 100,000 occurrences.
  - **Adoption (execution and recovery in one path).** Every run still marked `running` that this process isn't executing is adopted. That covers a manual run just inserted by the API, a run claimed before a crash, and a run whose process restarted.
    - Adoption calls `StartJob`, which is idempotent: it does nothing when the run's container already exists, so a container that outlived a restart is followed rather than restarted.
    - It then calls `WaitJob` with the remaining part of the timeout.
    - A claimed run whose container never started is therefore started (at-least-once). This deliberately replaces the earlier "missing container → failed" rule, because claimed-but-not-started is the common crash case.
    - A container that disappears *while being waited on* fails the run.
  - **Execution.**
    - Jobs run as one-off containers `es-job-<name>-<run id>`, labelled with the resource and run IDs.
    - The image, environment and secrets are resolved like an app's. An app-based job inherits the app's image, command, environment and secret environment, and the job's own entries win.
    - The container has no restart policy and runs with the same isolation as apps.
    - At the timeout it gets SIGKILL and the run is `timed_out`.
    - The exit code and the last 64 KiB of combined output are stored on the run, then the container is removed (only after the result is read).
    - Concurrency is capped at 8 runs; while the engine is down nothing is claimed or adopted.
  - **Results.** Failures and timeouts add a `run_failed` / `run_timed_out` event. `outputs.lastRunStatus`, `lastRunAt` and `nextRunAt` are merged into the resource without touching its spec or phase. `consecutive_failures` counts failed runs in a row.
- **Consequences**:
  - Store: migration `0004_jobs` adds the `runs` table, `resources.next_run_at` and `resources.consecutive_failures`.
  - Engine contract: the `Driver` gains `StartJob` and `WaitJob`. Services may not call them (`internal/archtest`).
  - Tests: they manipulate `next_run_at` directly instead of a fake clock, because the scheduler reads `Now` and the real store.
  - Retention: swept hourly, keeping every run younger than 30 days and the newest 100 per resource.
  - Timestamps: building this exposed text-compared timestamps of varying width (see L3-005 §1). They are now fixed width (`0005`).

## ADR-018 — Backups: logical dumps streamed to S3-compatible storage
- **Status**: Accepted (DAR-011) — **implemented** in v0.3 phase 4 (2026-10-21), `L2-005` BAK-01 – BAK-09. The bullets below are the decision as built; "As implemented" lists where the build differs from the first draft, and why
- **Decision**:
  - **PostgreSQL.** Engine exec runs `pg_dump --format=custom --no-owner --no-acl <db>` in the database container, with the password passed through the exec environment, never as an argument. Its stdout streams straight into a multipart upload (`minio-go`, 16 MiB parts) while a SHA-256 is computed.
  - **Redis.** `BGSAVE`, wait until `LASTSAVE` changes, then stream `/data/dump.rdb` through exec `cat`.
  - **Destinations.** A EnclaveStack bucket uses the object store's system credentials. External S3 takes its access and secret keys from the referenced secret, as JSON `{"accessKey","secretKey"}`.
  - **Objects.** Keys follow `L2-005` BAK-02. Object metadata records the source kind, engine version, run ID and SHA-256.
  - **Retention.** After each success, retention deletes the oldest objects beyond `keep` and `keepDays`. It never deletes the newest successful object, and it lists by prefix, so objects written by other tools are left alone.
  - **Restore, `new` mode.** Create the target resource with `restoreFrom` (internal, not client-settable). Its provisioning streams the object into `pg_restore --no-owner --role=<owner> -d <db>` (PostgreSQL), or places `dump.rdb` in the volume before first start (Redis).
  - **Restore, `replace` mode.** First run a `pre_restore` backup and abort if it fails. Then drop and recreate the database's objects (`pg_restore --clean --if-exists`), or stop Redis, swap `dump.rdb` and start it again.
  - **Failure handling.** Failures increment `consecutive_failures`; three in a row set the backup to `failed`.
- **Consequences**:
  - Logical dumps cannot recover to a point in time; WAL archiving is the future option (DAR-011).
  - A backup of a large database takes as long as `pg_dump` does; the default timeout is 2 h.
  - Restores are verified end to end by an integration test with real PostgreSQL and Redis (`L5-002`).
- **As implemented** (differences from the first draft, each found while building or testing):
  - **The SHA-256 lives in the run, not in object metadata.** It is only known once the upload has finished, and multipart uploads can't add metadata afterwards. The run's `artifact` records key, size, SHA-256 and destination. Object metadata carries the source and run ID.
  - **Every restore verifies first.** The object is copied to a private temporary file under the data directory while its SHA-256 is checked. Only a matching copy reaches `pg_restore` or Redis, so a damaged or altered object changes nothing.
  - **Redis restore goes through the AOF.** With `appendonly yes`, Redis 7 ignores `dump.rdb` at start (verified both with and without an existing AOF), so swapping the file restores nothing. The procedure:
    1. A helper container writes `dump.rdb` and removes `appendonlydir`.
    2. A temporary `redis-server --appendonly no` loads the dump.
    3. `CONFIG SET appendonly yes` rewrites it into a fresh AOF; the restore waits for `aof_rewrite_in_progress:0`, then runs `SHUTDOWN NOSAVE`.
    4. The real cache starts again. The data survives later restarts (tested on real Redis).
  - **New-mode restores use an internal output marker** (`pendingRestore`), not a `restoreFrom` spec field. The new resource takes the source's spec. After the restore it shows `restoredFrom`.
  - **Keys carry milliseconds** (`20261021T030000.000Z`), because two runs in the same second must sort in order for retention.
  - **Retention counts only this backup's own runs.** It considers objects whose run ID belongs to this backup and still records a stored object. Two backups of one source into the same bucket and prefix would otherwise delete each other's objects; a test reproduced that.
  - **Retention never deletes a run a restore is waiting for.** Otherwise a safety backup before a replace evicted the very run being restored when `keep` was small; a test reproduced that too.
  - **Retention updates run history.** A run whose object retention removed loses its `artifact` and says "Removed by retention." Restoring it answers `409 RUN_NOT_RESTORABLE` at once instead of creating a resource that then fails.
  - **A replace that fails permanently** (damaged or missing object, failed safety backup) has changed nothing. Its marker is dropped and the message says Retry brings the source back as it was. A failed new-mode restore says to delete the new resource.
  - **Three failures in a row move a `ready` backup to `failed`.** The phase machine gained `ready → failed`, only for this (L3-002 §5). Before it was allowed, the transition was silently refused and the backup never failed.

## ADR-019 — Builds from Git through the engine's build API
- **Status**: Accepted (DAR-010); spike gate **passed** and **implemented** in v0.3 phase 5 (2026-10-21) — `L2-005` BLD-01 – BLD-05
- **Decision**:
  - **Clone.** The reconciler clones the ref with `go-git` (Apache-2.0), shallow, into a temporary directory under the data dir.
  - **Credentials.**
    - HTTPS uses a token from the credentials secret.
    - SSH uses a key from it, plus a required `knownHosts` entry; there is no trust-on-first-use.
  - **Build.** The `context` sub-directory is tarred (honouring `.dockerignore`) and sent to the Engine image build API, tagged `enclave.local/<app>:<generation>-<short sha>`. *Superseded in part, see "As implemented": the context goes through the build session, and tags are `r<revision>-<12-char sha>`.* Build arguments are passed as build args; secrets are not supported as build args. The build log streams into a per-app build log (`GET …/logs?build=true`).
  - **Skipping.** A build is skipped when the commit, the Dockerfile path, the context path and the build arguments match the serving revision.
  - **Clean-up.** Images older than the previous revision are removed after a successful deploy.
- **Spike gate (before implementation)**: show through the Go SDK, on Docker Desktop (macOS) and Docker Engine (Linux), that a BuildKit build with a session works. Otherwise the classic builder is accepted with `RUN --mount` documented as unsupported. Record the outcome here.
- **Spike outcome (2026-10-21): passed; BuildKit is the builder.**
  - **Setup**: Go SDK `docker/docker` v28.5.2 with `moby/buildkit` v0.25.2 `session`. The session runs over `client.DialHijack("/session")`, and the context goes in the request body as a tar.
  - **Docker Desktop 29.7.2 (macOS, arm64)** and **Linux Docker Engine 29.9.1** (`docker:29-dind`) behave identically:
    - a plain build succeeds;
    - `RUN --mount=type=cache` succeeds;
    - a failing step reports `process "/bin/sh -c …" did not complete successfully: exit code: 3`.
  - **The classic builder** builds the same plain Dockerfile but refuses `--mount` ("requires BuildKit"), so it is not used.
  - **Logs**: BuildKit sends progress as `moby.buildkit.trace` aux messages, which decode (`controlapi.StatusResponse`) into step names (`[2/2] RUN …`) and the step's stdout/stderr. That is the build log.
  - **Cost**: about 24 MB of binary for the session and trace packages.
- **Decided at implementation (2026-10-21)**:
  - **Build history.** A `builds` table (L3-005, migration `0007_builds`) keeps each build's commit, inputs hash, image, result, message and a log tail (256 KiB, the end kept), for the newest 10 builds per app. `GET …/logs?build=true` returns the latest build's log.
  - **Skipping.** Before cloning, the ref is resolved to a commit (`ls-remote`; a 40-hex ref is used as is). A build is skipped when the inputs hash matches the serving revision's and that image still exists. The hash covers the URL, commit, Dockerfile, context and sorted build arguments.
  - **Image names.** Built images are tagged `enclave.local/<app>:r<revision>-<12-char sha>` and are never pulled. A missing built image is a permanent error that says to redeploy.
  - **Failures.** Clone and network failures are transient and retried with backoff. A Dockerfile that fails is permanent for that commit; the message names the failing step and its last log lines. Either way, the serving revision keeps running (DEP-03).
  - **Jobs** that run with a source app use the app's current built image (output `builtImage`). Until the app has one, the run fails with that reason.
  - **Fake engine.** It uses an in-memory Git source and builder, so the CLI, provider and console tests need no network. `internal/build` is tested against a real Git HTTP server, and the Docker driver against real BuildKit.
  - **URLs.** `http://` Git URLs are accepted for `localhost` and `127.0.0.1` only (a local Gitea, tests), as for S3 endpoints.
- **As implemented** (differences from the decision above, found while building or in the live check):
  - **The context goes through the build session, not as a tar body.** The first build of a real repository (`docker/welcome-to-docker`, 888 KB of context) failed on Docker Desktop with `failed to read dockerfile: archive/tar: invalid tar header`.
    - The same bytes buffered (which gives a known Content-Length) built, and the same stream built on Linux Docker Engine 29.9.1. So Docker Desktop's API proxy truncates a chunked `/build` body; the tests had passed only because their contexts fit in one chunk.
    - Spooling to a file did not help, because the HTTP client streams files chunked too, and buffering every context in memory was not acceptable.
    - The fix is what the Docker CLI does for BuildKit. The context directory is served over the session's file sync (`RemoteContext: "client-session"`, `filesync` with `fsutil`, and the `.dockerignore` patterns as excludes), and the request has no body.
    - This works on both engines (spiked), and the driver test now builds a 2 MiB context with an exclude.
    - `internal/build` therefore checks and describes the context (`PrepareContext`: inside the checkout, through symlinks too, and its excludes) instead of tarring it.
  - **Retry after a failed build builds again.** Without this rule, Retry fell back to the serving image and reported Ready with nothing new deployed; a test found it.
  - **Clean-up keeps images still in use.** An image a container still uses (a job run, say) is kept and removed by a later clean-up instead of failing it. Deleting an app removes all of its built images.
  - **Progress.** Step lines (`#3 [2/5] RUN …`) become the status message while a build runs, so `--wait`, the console and `apply` show where the build is.
- **Consequences**: no extra daemon or CLI. The clone directory is removed after each build. Disk use is bounded by keeping two images per app.

## ADR-020 — Networks: one bridge network each, subnets as address ranges, private DNS aliases
- **Status**: Accepted (DAR-012) — **implemented** in v0.4 phase 1 (2026-10-21), `L2-006` NET-01 – NET-08
- **Context**: v0.3 puts every container on the one `enclave` network, so every resource can reach every other. Teams want a private data tier and AWS-style layouts.
- **Decision**:
  - **Engine object.** A `network` resource is the Docker bridge network `es-net-<name>`, labelled like every managed object. Its IPAM subnet is the network's CIDR, its gateway the CIDR's first address, and its automatic range (`IPRange`) the first subnet. `internet: false` creates it with `Internal: true` (no NAT, no published ports).
  - **Subnets** are named ranges inside the network, not separate engine networks, so subnets of one network reach each other directly. A resource placed in the first subnet gets Docker's automatic address. A resource placed in another subnet gets the lowest free address of that subnet, chosen by the driver from the network's current endpoints; when the engine reports the address taken (a race with another worker), the driver tries the next one.
  - **Placement.** `network` and `subnet` on app, database, cache and image-based job specs. A resource with no network stays on `enclave`. Each container joins exactly one EnclaveStack network. An app revision starts in its spec's network (a move is a rolling deploy, NET-06). A database or cache that moves is disconnected from the old network and connected to the new one; its volume and published port are kept.
  - **System services.** The edge proxy, the probe helper and the object store are connected to every EnclaveStack network, so domains, health checks and buckets keep working (NET-04). Connecting them is part of creating a network and is checked by every drift pass.
  - **Private DNS.** Every container of a resource carries the network alias `<name>.<kind>.internal` (`orders.database.internal`), next to `es-<kind>-<name>`. Docker's embedded DNS answers both inside the network. Containers created before v0.4 gain the alias when they are next recreated; `privateDns` is reported only once the alias is present.
  - **Validation.** CIDRs are canonical IPv4 prefixes in the private ranges, /16 – /28. Subnets lie inside the CIDR, are /16 – /29, don't overlap and are 1 – 16 in number. A new network may not overlap another EnclaveStack network or the `enclave` network's subnet; an overlap with a network outside EnclaveStack is reported by the engine and becomes a permanent failure that names it.
- **Consequences**:
  - **Engine contract:** the `Driver` gains `EnsureNetwork`, `DeleteNetwork` and `NetworkHealth`. `EnsureInput` gains `Placement` (network name, subnet range, whether the subnet is the automatic one).
  - **Outputs:** `privateIp` and `privateDns` on placed kinds; `gateway`, `dockerNetwork` and `attached` on networks.
  - **Dependencies:** placed resources reference their network (`resource_refs`), so CP-12 refuses deleting a network in use.
  - **Schema:** migration `0008_network_kinds` rebuilds `resources` to allow the v0.4 kinds (as `0003`).
- **As implemented**:
  - **A database or cache that moves is recreated**, not reconnected. A spec change already recreates its container with the same volume and host port, so a move is the same path; the interruption is a container restart and the data is kept.
  - **Fixed addresses** are the lowest free ones of the subnet, read from the engine network's endpoints, and held for two minutes in the driver once handed out, so two workers creating containers at once don't pick the same one.
  - **The probe helper is made sure of before a firewall's rules are rendered**, because it is otherwise created lazily by the first health check and its address would be missing from the allow list (found by the real-Docker firewall test).
  - **No `attached` output**: it would go stale between reconciles; the console counts placed resources itself.
  - Spiked and tested on Docker Desktop 29.7.2: isolation between networks (name and address), fixed and automatic subnet addresses, `.internal` names, an internal network with no route out, and an overlap with a foreign network reported as a permanent error.

## ADR-021 — Firewalls: nftables in each target's network namespace
- **Status**: Accepted (DAR-013) — **implemented** in v0.4 phase 2 (2026-10-21), `L2-006` FW-01 – FW-09
- **Context**: Security groups need port-level, stateful rules between containers. On Docker Desktop the host never sees that traffic, so host packet filters can't enforce it.
- **Decision**:
  - **Policy.** For each target the reconciler computes one policy from every firewall that names it: the union of their ingress rules; egress restricted if any of them says `deny`, with the union of their egress rules. Sources and destinations are resolved to IPv4 prefixes: a CIDR as given, `any` as `0.0.0.0/0`, a network as its CIDR, a resource as the current addresses of all its containers.
  - **Implicit allows** (FW-05, FW-06): loopback; replies (`ct state established,related`); from the edge proxy and the probe helper; from load balancers that target the resource, on their target ports; and to the container port of a published app (host-port traffic arrives from the network gateway).
  - **Rendering.** One nftables table `inet enclave` with an `input` chain (policy `drop`) and an `output` chain (policy `accept`, or `drop` when egress is restricted; loopback stays allowed, so Docker's embedded DNS at 127.0.0.11 keeps working). The table is replaced atomically in one `nft -f` transaction (`add table`, `delete table`, `add table { … }`). Values come only from validated prefixes, protocols and port numbers, never from free text. A target with no firewall gets the table deleted.
  - **Applying.** A one-off container `es-netfilter-<random>` runs with `--network container:<target>`, capability `NET_ADMIN` only, no mounts, and the ruleset in an environment variable that a fixed script pipes into `nft -f -`. It is removed after it exits; a non-zero exit is an error naming the target.
  - **Helper image.** Built on the machine with the existing BuildKit path (ADR-019): `FROM alpine:3.20@<digest>` plus `apk add nftables`, tagged `enclave.local/netfilter:<hash of the Dockerfile>`. It is built once, the first time a firewall applies, and needs the network once, like an image pull.
  - **When rules are applied.**
    - A firewall's reconcile applies the policy of each of its targets.
    - An app's new revision gets its policy right after its containers start and before its health check, so it never takes traffic unprotected. The window between start and rules is reported in the deploy events (measured in `L5-002` §7).
    - Firewalls that name a resource as a source are re-applied when its addresses change: from a hook the driver calls right after a new revision's containers start, and again after the old revision is stopped (removing its addresses).
    - Every drift pass compares each target's containers (ID and start time) with what was last applied, and re-applies when a container is new or restarted (FW-09).
- **Consequences**:
  - **Engine contract:** the `Driver` gains `ApplyFirewall(resource, policy)` (all running containers of the resource) and `Endpoints(resource)` (their addresses). `EnsureInput` gains `Firewall` (the target's policy) and `Started` (the source hook). Services may not call `ApplyFirewall` (`internal/archtest`).
  - **Limit:** a source's old address stays allowed until the old revision stops and the re-apply after it runs, a few seconds at most. An address freed and handed to another container in that window would be allowed briefly.
  - **Fake engine:** it records each target's last policy, so integration tests assert resolved rules without Docker. Enforcement itself is tested on real Docker (`L5-002` §7).
- **As implemented**:
  - **Measured window**: loading one container's ruleset (create, start and remove the helper) takes about 0.1 s on Docker Desktop 29.7.2 (92 ms measured); that bounds the time a new container runs before its rules load, and it does so before its health check and before it takes traffic.
  - **Clearing is explicit.** A target is cleared only when a firewall stops naming it (update or delete), never by drift; the driver remembers what it loaded per container (ID and start time) in memory, so after a control-plane restart every policy is loaded again once.
  - **Domains re-sync their app's firewall** after configuring the edge, because the edge may have just started and its address must be in the app's allow list.
  - **The helper drops every capability except `NET_ADMIN`**, and a ruleset nftables rejects is a permanent error naming the target.

## ADR-022 — Replicas and load balancers
- **Status**: Accepted (DAR-014) — **implemented** in v0.4 phase 3 (2026-10-21), `L2-006` LB-01 – LB-08
- **Decision**:
  - **Replicas.** A revision is 1 – 10 containers: `es-app-<name>-r<N>` for the first and `es-app-<name>-r<N>-<i>` for the others, labelled `enclave.replica=<i>`. All carry the app's aliases, so Docker's DNS answers with every replica. A rolling deploy starts every new replica, probes each one, switches, drains, then stops all old replicas (ADR-016 with N containers). A steady-state check that finds a replica missing deploys a new revision. `publish` allows one replica only.
  - **Edge.** Domain routes use Caddy's dynamic `a` upstreams on the app's stable alias (refresh 1 s), so requests are spread across replicas and deploys still never touch the edge. Dial retries stay as in ADR-015.
  - **Load balancer.** One container `es-loadbalancer-<name>` per load balancer, running HAProxy 3.2 pinned by digest in master-worker mode, on its targets' network (and on `enclave` too when published from a network without internet). Its configuration is rendered whole from the spec: a `resolvers` section on 127.0.0.11, one backend with one `server-template` per target app (10 slots, the replica maximum) carrying weight and check settings, the balance algorithm, and a stats listener used only by the control plane. Text is rendered only from validated names and numbers.
  - **Applying.** The configuration is copied in, checked with `haproxy -c`, and loaded by `SIGUSR2` to the master: a graceful reload that keeps established connections (LB-06). A configuration HAProxy rejects is a permanent error with its message, and the running configuration stays.
  - **Health.** HAProxy's own checks (`tcp`, or `http` with a path) decide which replicas get traffic. The reconciler reads the stats CSV through the probe helper (engine exec, fixed arguments) and reports `healthyTargets` and `targets`; each drift pass refreshes them.
  - **Publishing.** `publish` binds the port on the publish address with a host port kept across recreation, as for apps.
- **Consequences**: the app driver's revision logic generalises to N containers. `MemoryReservedMiB` counts replicas. Load balancers reference their apps (CP-12) and are firewall sources for them (ADR-021).
- **As implemented**:
  - **Statistics are read inside the load balancer's own container** (`wget` against `127.0.0.1:61999`), not through the probe helper; the listener never leaves the container's loopback. The API refuses 61999 as a load balancer port.
  - **HAProxy runs unprivileged**; the container's namespaced sysctl `net.ipv4.ip_unprivileged_port_start=0` lets it bind ports below 1024.
  - **A target's port change reconfigures its load balancers** through a normal reconcile, found by comparing the recorded backends.
  - **Zero drops across deploys** needed two settings, found with `tests/load/lb.sh`. First, `timeout connect 1s`: a stopped container's address doesn't answer on the bridge, so the 5 s default outlasted clients. Second, `retries 3` with `option redispatch 1` and, for HTTP, `retry-on conn-failure`: the retry goes to another replica, and only connection failures are retried, so nothing an app received is sent twice. Before these: 96 of 11,954 requests failed across three deploys. After: 0 of about 36,000 across nine rolling deploys of a three-replica app at 200 req/s (three runs, Docker Desktop 29.7.2).
  - **The healthy count follows deploys**: when a target app's deploy finishes, its load balancers wait (up to 10 s) until HAProxy sees the new replica set, then record it.
  - Tested on real Docker: three replicas balanced, a stopped replica skipped and reported, an in-place graceful reload (same container and host port), a rejected configuration that leaves the running one serving, and the edge spreading domain requests over three replicas.

## ADR-023 — DNS provider plug-ins
- **Status**: Accepted (DAR-015) — **implemented** in v0.4 phase 4 (2026-10-21), `L2-006` DNS-01 – DNS-08; tested against local fakes of both APIs, not yet against the live services
- **Decision**:
  - **Interface.** `internal/dns` defines `Provider`: `Zone(ctx)` (ID and name servers; checks credentials and access), `Records(ctx, fqdn, type)`, `Upsert(ctx, fqdn, type, values, ttl)` and `Delete(ctx, fqdn, type, values)`. A registry maps provider names to constructors that take the decrypted credentials. `internal/dns` imports nothing of the control plane, like `internal/build`.
  - **Cloudflare.** REST API v4 with a scoped API token (`Zone:Read`, `DNS:Edit`). One Cloudflare record per value; EnclaveStack writes the comment `managed by enclave <record id>` and treats such records as its own.
  - **Route 53.** The REST API, signed with SigV4 by `minio-go`'s signer (already a dependency), region `us-east-1`. A record set holds all values; changes use `UPSERT` and `DELETE` in one change batch.
  - **Ownership and conflicts** (DNS-04). Before the first write, the record is read: none, or the same values, means EnclaveStack owns it from now on (`outputs.owned`); different values make the record fail permanently, naming what's there. Once owned, EnclaveStack keeps it as specified, and deletes it with the resource.
  - **Where it runs.** Records are reconciled by the reconciler directly, like backup storage: there is no engine involvement. Each call has a 30 s timeout; `429`, `5xx` and network errors are transient, and `401`/`403` are permanent with the missing permission named.
  - **Drift.** Ready records are re-read every 10 minutes (not every drift pass, to stay within provider rate limits), and put back if they differ.
  - **Machine records.** A record with `machine: true` takes the machine's `publicAddress` (A for IPv4, AAAA for IPv6). Changing the setting re-reconciles these records; without a public address they fail with that instruction.
- **Consequences**: provider credentials are secrets and never logged. Tests use local fakes of both APIs (`L5-002` §7); nothing reaches the real services in CI.
- **As implemented**:
  - **SigV4 is our own** (`internal/dns/route53/sigv4.go`), not `minio-go`'s: its signer is fixed to the `s3` and `sts` services. It is checked against two of AWS's published signatures (the test suite's `get-vanilla` and the IAM `ListUsers` example), and the Route 53 fake verifies every request's signature.
  - **Provider registry** in `internal/dns/providers`; the fake engine uses an in-memory provider (`dns.Memory`) instead.
  - **Drift**: a record is compared at most every 10 minutes, and the first comparison after a reconcile or a restart waits one interval.

## ADR-024 — Certificates through DNS (DNS-01) and wildcard domains
- **Status**: Accepted (DAR-016) — **implemented** in v0.4 phase 5 (2026-10-21), `L2-006` CRT-01 – CRT-05; tested against Pebble, not yet against the live Let's Encrypt
- **Decision**:
  - **Issuer.** A domain with `zone` and `tls: auto` gets its certificate from the control plane: the ACME client of lego (core packages only), with the DNS-01 challenge solved through the zone's `dns.Provider`. The challenge record `_acme-challenge.<host>` is created before validation and removed afterwards, whatever the outcome (CRT-04).
  - **Account.** One ACME account per machine and directory, its key encrypted in the store; the email as for HTTP-01 (ADR-015).
  - **Storage.** A `certificates` table keeps hostname, domain, certificate chain (PEM), private key (encrypted by `secretbox`, bound to the hostname), issuer and validity.
  - **Serving.** The edge configuration lists these as loaded certificates (`tls.certificates.load_pem`) and skips automatic certificates for their names. The configuration file is therefore written with mode 0600, and the edge volume already holds certificate keys today.
  - **Wildcards.** `*.example.com` (one leading `*` label) is accepted only with a zone. Edge routes are ordered so exact hostnames match before wildcards.
  - **Renewal.** At a third of the lifetime left (30 days for 90-day certificates), each drift pass starts a renewal; the old certificate keeps serving until the new one is loaded. Failures retry with backoff; with less than a tenth of the lifetime left the domain goes to `failed` with the reason (CRT-03).
  - **Loopback edge.** DNS-01 needs no inbound connection, so the loopback guard of ADR-015 doesn't apply to these domains.
  - **Tests.** Pebble validates DNS-01 against its challenge test server; a test DNS provider writes TXT records there, and lego's propagation check is pointed at it (test-only options, like `ACMEDirectory`).
- **Consequences**: `golang.org/x/crypto` and `github.com/go-acme/lego/v4` (core) are added. The `Driver`'s `EdgeConfig` gains `Certificates`. Domains reference their zone (CP-12).
- **As implemented**:
  - **lego v4.35.2**, core packages only; its logger is silenced and errors come back as values. lego takes no context, so an order runs in a goroutine bounded by `OrderTimeout`.
  - **Orders run one at a time** in the reconciler: a wildcard and its base name share one challenge record.
  - **The TXT value is computed here** (`base64url(SHA-256(key authorisation))`) rather than with lego's helper, which resolves CNAMEs over the network first.
  - **A wildcard is verified with a name below it** (`es-check.<base>`) in the TLS handshake.
  - **The fake engine uses a local issuer** (`acme.Local`) that writes and removes the challenge record and signs from an in-process CA, so the whole path runs in integration tests.
  - Tested against Pebble 2.8 with challtestsrv: account registration and reuse, a wildcard and an exact order, Caddy serving both as loaded certificates with the exact route winning, and the configuration file at mode 0600.

## ADR-025 — VMs: QEMU processes on the host, driven over QMP
- **Status**: Accepted (DAR-017) — **implemented** in v0.5 phase 2 (2026-10-21), `L2-007` VM-01 – VM-12, IMG-01 – IMG-05
- **Context**: v0.5 adds whole machines. Docker Desktop doesn't expose `/dev/kvm` on macOS (checked on an Apple M4, Docker Desktop 29.7.2), so a VM can't run inside a container there. The owner chose one engine for both platforms (2026-10-21).
- **Decision**:
  - **Hypervisor.** A new engine port, `engine.Hypervisor`, next to the container `Driver` (ADR-006), with one implementation, `internal/engine/qemu`. It runs `qemu-system-aarch64` or `qemu-system-x86_64` on the host for the host's own architecture: `-accel hvf` on macOS and `-accel kvm` on Linux, `-cpu host`; no emulation (IMG-03). On arm64 the machine is `virt` with the UEFI firmware shipped next to QEMU (`<prefix>/share/qemu/edk2-aarch64-code.fd`, or the distribution's AAVMF path on Linux); on amd64 it is `q35` with QEMU's default BIOS.
  - **Requirements check.** At start and on every `GET /machine`, the hypervisor finds QEMU on `PATH` (or `--qemu`), reads its version (8.0 or later: `-netdev stream` with reconnect), the firmware, and acceleration (`kern.hv_support` on macOS, read-write `/dev/kvm` on Linux). A missing piece is reported with the command that fixes it (VM-11). While it is missing, VMs stay `pending` and retry with backoff instead of failing.
  - **Process model.** Each VM is one QEMU process started with `-daemonize`, a pidfile, `-no-shutdown`, `-display none`, the serial console written to a file, and a QMP socket. Daemonized QEMU is in its own session, so stopping or upgrading the control plane leaves VMs running (VM-08). A VM's files live in `<data dir>/vm/<id>/`: `root.qcow2`, `seed.iso`, `console.log`, `qemu.pid` and `qmp.sock`. The directory is mode 0700.
  - **Socket paths.** macOS limits UNIX socket paths to 104 bytes (the spike hit this). When `<data dir>/vm/<id>/qmp.sock` is longer than 100 bytes, sockets go to `/tmp/enclave-<uid>/`, which must be owned by the server's user with mode 0700, or the hypervisor refuses to use it. QMP is never on TCP: any local user could drive it.
  - **QMP.** QEMU accepts one QMP client at a time (the spike left a client attached and every later one hung). The hypervisor holds one connection per running VM in a `qmp.Monitor` that serialises commands, reconnects, and delivers events. Every command has a timeout.
  - **Power.** `power: running` starts QEMU if it isn't running. `stop` sends `system_powerdown` (ACPI), waits up to 60 s for status `shutdown`, then sends `quit` (VM-06). `restart` is a stop then a start, so changed CPUs, memory and disks apply.
  - **Who stopped it.** With `-no-shutdown`, a guest that powers itself off leaves QEMU running in status `shutdown`. The reconciler records `power: stopped` with an event "The VM shut itself down", then quits QEMU (VM-07). A QEMU process that is gone without that status crashed, and drift starts it again (CP-10).
  - **Adoption.** On start, for each VM whose power is `running`: if the pidfile names a live process whose command line is QEMU with this VM's directory, and QMP answers, the VM is adopted. Otherwise it is started (VM-08, CP-09).
  - **First boot.** The hypervisor writes a NoCloud seed, an ISO 9660 image with Rock Ridge names and the volume label `cidata`, using `github.com/diskfs/go-diskfs` (MIT, pure Go). It holds `meta-data` (instance ID = VM ID, host name = VM name), `network-config` (the link address, ADR-026), and `user-data`. When the user gives a script, `user-data` is a MIME multi-part document. EnclaveStack's own `#cloud-config` creates the user `enclave` (sudo without password, the SSH keys, no password login) and gives the default user the same keys. The user's script is the second part, unchanged. The instance ID never changes, so cloud-init runs the first-boot steps once (VM-03, VM-04).
  - **Images.** The catalog is a table in `internal/engine/qemu` pinning, per name and architecture, a versioned URL (never `latest`), the checksum, the default user and the format. Downloads go to `<data dir>/images/<sha256>.<format>` through a temporary file, are checked, and are shared. A root disk is a qcow2 overlay whose backing file is the cached image, so a VM starts in seconds without copying gigabytes. An image stays while any root disk is based on it; unused images are removed after 7 days (IMG-05).
  - **Disks and resizing.** Root and data disks are attached as `virtio-blk-pci` with `serial=<name cut to 20>` (VOL-01). Growing uses QMP `block_resize` while the VM runs, or `qemu-img resize` while it is stopped (VM-02, VOL-03).
  - **Boot console.** `console.log` is capped at 1 MiB: on each start the old one is renamed to `console.log.1`. `Logs` returns its last lines (VM-09).
- **Consequences**:
  - **Engine contract:** `engine.Hypervisor` provides `Check`, `EnsureVM`, `StopVM`, `DeleteVM`, `VMStatus`, `ConsoleLog`, `EnsureDisk`, `ResizeDisk`, `DeleteDisk`, `SnapshotDisk` and `CachedImages`. `internal/engine/fake` gains an in-memory hypervisor, so integration tests run without QEMU. Services may not call its state-changing methods (`internal/archtest`).
  - **Accounting:** running VMs add their CPUs and memory to `GET /machine`'s reserved figures. A VM is refused when its memory exceeds the machine's memory minus 2 GiB (VM-01).
  - **Service managers:** a systemd unit must use `KillMode=process` (and launchd `AbandonProcessGroup`), or stopping the server kills its VMs. `L4-001` documents both.
  - Spiked on an Apple M4 (macOS 26.4, QEMU 11.1.2 from Homebrew): Debian 12 arm64 booted with HVF, read a seed written by go-diskfs (host name and first-boot command applied) and reached a login prompt in 25 – 27 s.
- **As implemented**:
  - **A monitor connection per operation**, not one held per VM: each QMP use dials the socket under the VM's lock and closes it, so a stuck client can never block the VM's monitor, and the control plane holds no state to lose on restart.
  - **The image cache is keyed by the image's address** (`<data dir>/images/<hash of URL>.<format>` with a JSON sidecar recording the SHA-256, format, size and last use). Each VM directory records which cached image its root disk is based on, which is how `CachedImages` counts users and the 7-day clean-up finds unused ones.
  - **Debian is checked with SHA-512**, the only sum Debian publishes; both checksums are computed during every download.
  - **The guest runs a `virtio-rng-pci` device**, so first boot never waits for entropy.
  - **The console log is rotated on each start**, not capped while the VM runs; reads return at most the last 1 MiB, with terminal escape codes removed.
  - **`enclave` is created with sudo by a cloud-config part placed after the user's own**, with `merge_how` appending lists, so a user's `users:` doesn't remove it.
  - **Without its link, a guest waits for its network at first boot** (about two minutes on Debian before it gives up): the reconciler always ensures the link before starting QEMU.
  - Tested on real QEMU (`make engine-qemu`): first boot with the seed (host name, the `enclave` user, passwordless sudo), adoption by a second hypervisor instance (same process), live root-disk growth, a live snapshot in 0.84 s, a guest power-off told apart from a crash, stop, delete.

## ADR-026 — VMs in networks: a link container per VM
- **Status**: Accepted (DAR-018) — **implemented** in v0.5 phase 3 (2026-10-21), `L2-007` VMN-01 – VMN-05; VMN-06 not built
- **Context**: QEMU runs on the host, but networks are engine bridges that the host can't join on Docker Desktop. A VM must still get a private IP, a `.internal` name, firewalls and `internet: false`, exactly as containers do (ADR-020, ADR-021).
- **Decision**:
  - **Link container.** Every VM has a container `es-vm-<name>` in its network: the VM's network identity. It carries the VM's aliases (`<name>.vm.internal`, `es-vm-<name>`), its private IP and its published ports, so private DNS, placement and subnets (ADR-020) apply to it unchanged.
  - **Link.** QEMU's NIC uses `-netdev stream` to a TCP port the link container publishes on `127.0.0.1` (a host port fixed per VM and kept in its outputs), with reconnect every second. Inside, a helper program, `es-vmlink`, moves length-prefixed Ethernet frames between that connection and a tap device `vm0`. The VM is configured statically (cloud-init `network-config`) as `198.18.0.2/30` with gateway and DNS `198.18.0.1`. Every link uses the same pair, because each lives in its own namespace. `198.18.0.0/15` is the benchmarking range, outside the ranges networks may use (NET-01), so it never collides.
  - **Why not link-local.** The spike used `169.254.77.0/30` first. Linux doesn't forward packets with link-local sources, so the VM reached nothing.
  - **Addressing.** In the link container, `nftables` sends every new connection that arrives on the network interface to the VM (`dnat to 198.18.0.2`). The exceptions are the frame port and DNS. Traffic leaving the VM is masqueraded to the container's address. Other resources therefore see the VM at its private IP, and the VM sees their real addresses. That is what firewalls match on.
  - **DNS.** `es-vmlink` answers on `198.18.0.1:53` (UDP and TCP) by forwarding to Docker's embedded resolver at `127.0.0.11`, so the VM resolves `.internal` names (VMN-02).
  - **Checksums.** Docker Desktop delivers packets whose TCP and UDP checksums were never filled in, marked as already verified. A container accepts them; a VM drops them (the spike counted `TcpInCsumErrors` and saw no internet). `es-vmlink` therefore recomputes the checksum of each unfragmented IPv4 TCP or UDP frame it hands to QEMU.
  - **Image.** `enclave.local/vmlink:<hash>` is built on the machine through the build path of ADR-019 from an embedded Dockerfile. A pinned `golang` stage compiles the embedded `es-vmlink` source (standard library only), and a pinned `alpine` stage adds `nftables` and `iproute2`. It runs with only `NET_ADMIN`, `/dev/net/tun`, `net.ipv4.ip_forward=1` and no mounts.
  - **Own protection.** The frame port is accepted only from the network gateway: published-port traffic arrives from there, and other containers can't open it. Without that rule, any container on the network could inject frames.
  - **SSH and publish.** Port 22 is published on a loopback port fixed per VM (`sshAddress`, VM-05). `publish` ports are published on the publish address, as for apps (VM-10). Both reach the VM through the same DNAT.
  - **Internet off.** Docker publishes nothing from an internal network. So a VM in a network with `internet: false` has a link container that also joins `es-vmlink`, a plain bridge made for this purpose, with the higher gateway priority. Its published frame and SSH ports work through that network. `nftables` drops anything forwarded between `vm0` and `es-vmlink`, so the VM still has no route out (VMN-04).
  - **Firewalls.** A VM target's policy is rendered into a `forward` chain matching `oifname "vm0"` (ingress) and `iifname "vm0"` (egress), with the same rules, resolution and implicit allows as ADR-021. The extra implicit allow is the published ports from the gateway (FW-06). A VM as a source resolves to its link container's address (VMN-03). In the spike, a rule allowing only one container's address on port 9000 let that container through, blocked another, and blocked loopback SSH until the published-port allow was added.
  - **Moving.** Changing `network` or `subnet` recreates the link container there on the same host ports. QEMU reconnects within a second, and the VM keeps its link address, so it isn't restarted (VMN-05).
  - **Domains and load balancers** reach a VM at `es-vm-<name>:<port>`, as they reach an app's stable alias (VMN-06).
- **Consequences**:
  - **Driver:** the container `Driver` gains `EnsureVMLink` and `DeleteVMLink`. `ApplyFirewall` and `Endpoints` accept `vm` resources (the link container).
  - **Limits:** throughput is that of one TCP stream, about 160 MB/s container-to-VM in the spike, which is plenty for a home machine. The VM's IPv6 is off.
  - Spiked on Docker Desktop 29.7.2 with the measured results above:
    - a container reached the VM by `worker.vm.internal`, and the VM reached the container by its alias;
    - the VM saw the container's real source address;
    - the VM reached the internet once checksums were recomputed;
    - restarting the link container restored traffic within 1 s, without touching the VM.
- **As implemented**:
  - **The link device has a fixed MAC address** (`02:68:63:00:00:01`). Found on the live walk-through: a recreated link container got a new random address and the VM kept the old one in its neighbour cache for about three minutes, so traffic stopped that long. With a fixed address, a link removed outside EnclaveStack was back in 10 s.
  - **The VM's addresses are `198.18.0.2/30` and `198.18.0.1`** in every link, as decided; `es-vmlink` renders its own table (`ip hcvm`) from the interfaces it finds and the gateways the driver passes, so the rules never contain free text.
  - **nftables reserves some chain names** (`fwd`, and the spike's `in`): the link's chains are `pre`, `post`, `frame_guard` and `vm_forward`.
  - **The link container is replaced when its configuration changes** (placement, published ports, the helper image), keeping its host ports; QEMU reconnects within a second.
  - **The helper image's tag is a hash of its build context**, which is this package's own source, embedded, rewritten as `package main`; a unit test compiles that context for Linux.
  - Tested on real Docker (`vmlink_test.go`: rules, the frame port's guard, kept host ports, an internal network through `es-vmlink`, the firewall's forward chain) and end to end on real Docker and QEMU (`tests/e2e` and the live walk-through in `L5-001` §2c).

## ADR-027 — Volumes: qcow2 files for disks, engine volumes for filesystems
- **Status**: Accepted (DAR-019) — **implemented** in v0.5 phases 1 and 4 (2026-10-21), `L2-007` VOL-01 – VOL-09
- **Decision**:
  - **Filesystem volumes** (phase 1) are engine volumes `es-vol-<id>`, labelled like every managed object. An app's or job's `volumes` become bind-free mounts of those volumes (`type: volume`), read-only when asked. Every replica and both revisions during a rolling deploy mount the same volume (VOL-04). Changing mounts is a spec change, so it deploys a new revision (VOL-05). `usedBytes` comes from the engine's disk-usage report, refreshed by each drift pass and at most every 5 minutes.
  - **Disk volumes** (phase 4) are qcow2 files `<data dir>/disks/<id>.qcow2` created with `qemu-img create`, thin, so a 50 GiB disk takes what is written. They belong to the hypervisor (ADR-025). A VM's `disks` add `-drive` and `virtio-blk-pci` pairs at start, so a change of `disks` restarts the VM (VOL-02).
  - **Exclusive attachment.** A disk is attached to at most one VM. The service refuses a second one (`VOLUME_ATTACHED`), and two QEMU processes never open one file. QEMU's own image locking backs this up.
  - **References.** A VM references its disks and an app or job its filesystem volumes (`resource_refs`). That gives CP-12 (VOL-07) and the `attachedTo` output.
- **Consequences**:
  - **Driver:** `EnsureVolume`, `DeleteVolume` and `VolumeUsage` on the container `Driver`. `EnsureInput` gains `Mounts`.
  - **Not counted:** databases and caches keep their own `es-<id>-data` volumes, which aren't `volume` resources (VOL-09).
- **As implemented**:
  - **Disks are created only when QEMU's tools are there**: without them a disk volume waits (`ErrNoVMs`) like a VM, with the fix in its message.
  - **A volume started from a snapshot releases the snapshot once it is filled** (`filledFrom` output, references cleared), so the snapshot can be deleted (SNP-06); later updates no longer check it.
  - **Growth is live**: `block_resize` through the attached VM's monitor, or `qemu-img resize` when no running VM has the disk.
  - Tested on real Docker (`volume_test.go`: two replicas reading one volume, read-only enforced, archive snapshot and restore) and on real QEMU (`TestRealDiskVolumes`; the live walk-through attached, formatted and grew a disk while its VM ran).

## ADR-028 — Snapshots: full copies kept on the machine
- **Status**: Accepted (DAR-020) — **implemented** in v0.5 phases 1 and 4 (2026-10-21), `L2-007` SNP-01 – SNP-06, VOL-08, IMG-04
- **Decision**:
  - **Store.** Snapshots are files in `<data dir>/snapshots/`, mode 0600, named by snapshot ID. A snapshot is never changed after it is `ready` (SNP-03). A partial file from an interrupted copy has a `.partial` suffix and is deleted and started again by the reconcile that resumes it (CP-09).
  - **Disk snapshots** (of a disk volume or a VM's root disk):
    - **Source in use.** When the VM runs, QMP `drive-backup` with `sync: full` writes a new qcow2. It is a point-in-time copy with the backing chain flattened (SNP-01, SNP-02). In the spike it copied 1.13 GiB in 0.8 s without pausing the guest (SNP-05).
    - **Source idle.** Otherwise `qemu-img convert -O qcow2` copies the file.
  - **Filesystem snapshots**: a one-off container mounts the volume read-only, and the control plane streams it out with the engine's archive API (`GET /containers/{id}/archive`) into `<id>.tar`, never through a host bind mount. The message says the copy was taken while in use (SNP-01).
  - **Starting from a snapshot**:
    - **New disk.** A volume or VM root disk from a snapshot is a full copy (`qemu-img convert`), grown to the asked size. It never uses a backing file, so the snapshot can be deleted at any time (SNP-06).
    - **New filesystem.** A filesystem volume from a snapshot is filled through the archive API (`PUT …/archive`).
  - **VM from a snapshot** (IMG-04): the new VM's seed has its own instance ID, so cloud-init treats it as a new machine. It sets new keys and a new host name, and regenerates SSH host keys.
- **Consequences**:
  - **Engine contract:** `Hypervisor.SnapshotDisk` and `CopyDisk`; `Driver.SnapshotVolume` and `RestoreVolume`.
  - **Space:** snapshots take space on the machine's disk. `GET /machine` reports the bytes held by snapshots. Copying them to buckets is out of scope for v0.5 (`L2-007` §6).
- **As implemented**:
  - **A disk snapshot reports `sizeGiB`** (the copy's virtual size), which volumes and VMs started from it must at least match.
  - **A VM started from a snapshot holds a reference to it only until its root disk exists**; the reconciler then drops it (`store.DropRef`).
  - **Filesystem copies use a container that is created and never started**, with the volume mounted and no network; it is removed after the copy.
  - Measured live: a disk snapshot of a VM writing to it took 0.6 s; a clone started from a VM snapshot booted with its own host name and new SSH host keys and the source's files.

## ADR-029 — Queues and topics: one NATS JetStream broker, a user per queue and topic
- **Status**: Accepted (DAR-021) — **implemented** in v0.6 (2026-10-21), `L2-008` QUE-01 – QUE-09, TOP-01 – TOP-08
- **Context**: v0.6 adds SQS- and SNS-like messaging. The owner chose NATS JetStream (2026-10-21). Apps must reach their queue with ordinary clients, and one queue's credentials must never reach another's messages.
- **Decision**:
  - **One broker per machine.** A system container `es-system-broker` (`nats` image pinned by digest) runs while at least one queue or topic exists, like the edge (ADR-015). It joins every EnclaveStack network as a system container (ADR-020), keeps its JetStream store in the engine volume `es-system-broker-data`, publishes client port 4222 on a loopback port, and never publishes its monitoring port. Memory is capped at 512 MiB.
  - **Configuration.** The reconciler renders one account `HC` with JetStream, a `system` user (the control plane, password in the managed secret `broker-credentials`) and one user per queue (`q-<name>`) and topic (`t-<name>`) whose password is that resource's managed secret `<name>-<kind>-password`. Passwords appear only as bcrypt hashes (cost 11, cached in memory per password so a rendering doesn't rehash). The file is copied into the container before it starts and on every change, followed by `SIGHUP`: NATS reloads users without dropping connections. Rendering and applying happen under one lock (as the edge).
  - **Queues.** A queue is a stream `ESQ_<name>` on subject `es.q.<name>` with work-queue retention, `MaxAge` = retention, `MaxMsgSize` = `maxMessageBytes`, a 2-minute duplicate window (`deduplicationId` is the `Nats-Msg-Id`), and one durable pull consumer `workers` with explicit acks, `AckWait` = visibility timeout and `MaxDeliver` = `maxReceives` (−1 for none). Hyphens in names become underscores in stream names.
  - **Queue permissions.** `q-<name>` may publish to its subject, to `$JS.API.CONSUMER.MSG.NEXT.ESQ_<name>.workers`, `$JS.ACK.ESQ_<name>.workers.>`, and its stream's and consumer's `INFO` subjects, and subscribe to `_INBOX.>`. It can't create, delete or read anything else.
  - **Dead letters.** A system stream `ES_DEAD_LETTERS` captures `$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.>`, so the advisory survives the control plane being down. The reconciler's delivery loop reads it: for a queue with a dead-letter queue, it copies the message (body and headers, plus `Hc-Dead-Letter-From`) into that queue, then deletes it from the source by sequence; without one, it records an event and leaves the message to its retention (QUE-03).
  - **Topics.** A topic is a stream `EST_<name>` on `es.t.<name>` with limits retention, `MaxAge` = retention. Each subscription is a durable pull consumer `s_<first 12 hex of SHA-256 of its target>` with `DeliverNew` (TOP-04), created and removed by the reconciler as `subscriptions` change. `t-<name>` may publish and subscribe to its subject (live listeners, TOP-06) and read its stream's info.
  - **Delivery loop.** Part of the reconciler, next to the scheduler. One goroutine per subscription of a ready topic fetches up to 32 messages (waiting up to 1 s) and delivers each: to a queue by a JetStream publish with `Nats-Msg-Id` = `<topic>:<sequence>:<subscription>`, so a redelivery is never a duplicate; to a webhook by an HTTP POST (10 s timeout). Success acks; failure naks with a delay of `min(2^(n-1) s, 5 min)`; the 10th failure terminates the message and records an event (TOP-02). Counters are kept in `subscription_stats` (L3-005 §8).
  - **Webhook requests.** Body: the message. Headers: `Content-Type: text/plain; charset=utf-8`, `X-Enclave-Topic`, `X-Enclave-Message-Id` (`<topic>:<sequence>`), `X-Enclave-Attempt`, `X-Enclave-Attr-<name>` per attribute, and with a secret `X-Enclave-Signature: sha256=<hex HMAC-SHA256(secret, body)>`. Redirects aren't followed.
  - **Port.** A new package `internal/messaging` holds the `Broker` interface: control (`EnsureQueue`, `DeleteQueue`, `EnsureTopic`, `DeleteTopic`, `Purge`), data (`Send`, `Receive`, `Delete`, `Peek`, `Publish`, `QueueStats`, `TopicStats`) and delivery (`Fetch`, `DeadLetters`, `MoveToDeadLetter`). `internal/messaging/natsbroker` implements it with `nats.go`; `internal/messaging/memory` implements the same semantics in memory for the fake engine. A `Pool` holds one connection as `system`, reconnecting when the broker's address or password changes.
- **Consequences**:
  - **Driver:** `ConfigureBroker(ctx, BrokerConfig)` starts, reconfigures or (with no users) removes the broker; `BrokerStatus` never fails and reports the address. Services may read `BrokerStatus` but never call `ConfigureBroker`.
  - **Services** call the broker's data methods (send, receive, delete, peek, publish, stats); only the reconciler calls the control and delivery methods (`internal/archtest`).
  - **Delivery is at least once.** A webhook may see a message twice when the control plane stops between its 2xx and the ack.
  - **Messages survive** restarts of the broker and of EnclaveStack. Deleting the last queue and topic removes the broker container and its volume.
- **As implemented**:
  - **Stream names keep hyphens** (`ESQ_my-queue`): NATS accepts them, and a name maps back to its queue without a lookup.
  - **Dead letters move at the next receive.** NATS raises the max-deliveries advisory when a worker asks for messages after the last allowed receive has timed out, not when the timeout passes. SQS behaves the same way. A queue nobody polls keeps the message until its retention ends.
  - **A deleted message stays deleted.** NATS accepts a second ack in silence, so `Delete` reads the message by sequence first and reports an unknown receipt.
  - **The configuration file quotes strings itself.** JSON escaping writes `>` as `\u003e`, which NATS rejects; only `\` and `"` are escaped.
  - **Host ports are fixed when a container is created** (all kinds, not only the broker): Docker Desktop 29.7.2 picks a new port on every restart when the binding leaves it to Docker, which broke every address EnclaveStack had reported. The driver now asks the operating system for a free port and names it in the binding.

## ADR-030 — Database engines: one container plan per engine, a logical dump per engine
- **Status**: Accepted (DAR-022) — **implemented** in v0.6 (2026-10-21), `L2-008` DBE-01 – DBE-06
- **Decision**:
  - **Plans.** The Docker driver's database plan becomes a table per engine (ADR-006):

    | Engine | Image | Port(s) | Data | Health check | Memory (MiB) |
    |---|---|---|---|---|---|
    | postgres | `postgres:<v>-alpine` (as v0.2) | 5432 | `/var/lib/postgresql/data` | `pg_isready` | 512 |
    | mysql | `mysql:8.4` | 3306 | `/var/lib/mysql` | `mysqladmin ping` over TCP | 1024 |
    | mongodb | `mongo:<v>` | 27017 | `/data/db` | `mongosh --eval db.adminCommand('ping')` | 1024 |
    | clickhouse | `clickhouse/clickhouse-server:<v>` | 8123 (HTTP), 9000 (native) | `/var/lib/clickhouse` | `wget http://127.0.0.1:8123/ping` | 2048 |

    Images are pinned by digest per supported version, like the edge's.
  - **Users.** MySQL: `MYSQL_DATABASE`, `MYSQL_USER`, `MYSQL_PASSWORD` and a random root password nobody keeps. MongoDB: the root user is the resource's user (`MONGO_INITDB_ROOT_*`); clients authenticate against `admin`. ClickHouse: `CLICKHOUSE_DB`, `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD` with access management on, and the default user off.
  - **Published ports.** A plan can publish more than one port. ClickHouse publishes both; the second's host port is `nativePort`. Host ports are kept across recreation, as v0.2 does for the first.
  - **Dumps** (ADR-018 unchanged otherwise; passwords go through the exec environment, never argv):
    - MySQL: `mysqldump --single-transaction --routines --triggers --events --no-tablespaces <db>`, format `mysqldump` (`.sql`); restore pipes it into `mysql <db>`, after `DROP DATABASE`/`CREATE DATABASE` when replacing.
    - MongoDB: `mongodump --archive --gzip --db <db>`, format `mongoarchive` (`.archive.gz`); restore `mongorestore --archive --gzip --drop`.
    - ClickHouse: a config file `/etc/clickhouse-server/config.d/enclave.xml` (written before first start) allows backups under `/var/lib/clickhouse/es-backups/`. A dump runs `BACKUP DATABASE <db> TO File('<run>.zip')`, streams the file out and deletes it; format `clickhouse` (`.zip`). Restore copies the file in, then `DROP DATABASE … SYNC` (when replacing) and `RESTORE DATABASE <db> FROM File(…)`.
  - **Retention** recognises the new extensions as EnclaveStack's own (ADR-018's key pattern).
- **Consequences**: `DumpInfo.Ext` gains `sql`, `archive.gz` and `zip`; the fake engine dumps every engine. A restore checks the dump's engine against the target's and refuses a mismatch.
- **As implemented**:
  - **MongoDB defaults to 7.0.** MongoDB 8.0 and later refuse to start on Linux kernels 6.19 – 7.0.13 (SERVER-121912), and Docker Desktop's kernel was 7.0.12. 8.0 stays available. When it refuses, the start fails permanently with a message saying to use 7.0 or update the kernel, instead of retrying.
  - **Health waits differ per engine.** ClickHouse and OpenSearch get longer first starts, and MongoDB's check signs in.

## ADR-031 — Search: one OpenSearch node per search service, security plugin on, HTTP without TLS
- **Status**: Accepted (DAR-023) — **implemented** in v0.6 (2026-10-21), `L2-008` SRC-01 – SRC-05
- **Decision**:
  - **Container.** `opensearchproject/opensearch:<2.19.4 | 3.3.2>` pinned by digest, `discovery.type=single-node`, `bootstrap.memory_lock=false`, `node.store.allow_mmap=false` (so hosts need no `vm.max_map_count` change), `OPENSEARCH_JAVA_OPTS=-Xms<m/2>m -Xmx<m/2>m`, memory limit `memoryMiB`. Data in `es-<id>-data` at `/usr/share/opensearch/data`.
  - **Authentication.** The security plugin stays on with its demo transport certificates (single node, so transport traffic never leaves the container), the `admin` password set by `OPENSEARCH_INITIAL_ADMIN_PASSWORD` from the managed secret, and `plugins.security.ssl.http.enabled=false`: the REST API is plain HTTP with basic authentication, like the other engines' protocols inside a network. The managed password always has an upper-case letter, a lower-case letter, a digit and a symbol, which OpenSearch requires.
  - **Health.** `curl -fsu admin:$PASS http://127.0.0.1:9200/_cluster/health` with the password from the environment, every 5 s, up to 3 minutes for a first start.
  - **Memory change** recreates the container on the same volume (SRC-02).
- **Consequences**: a search service reserves its `memoryMiB`. TLS on the REST API is out of scope; through a CDN or domain it is never exposed (only apps and buckets are origins).

## ADR-032 — Email: Mailpit per email service, relayed through the user's provider
- **Status**: Accepted (DAR-024) — **implemented** in v0.6 (2026-10-21), `L2-008` EML-01 – EML-07
- **Decision**:
  - **Container.** Each email service runs `axllent/mailpit` (pinned by digest) as `es-email-<name>`: SMTP on 1025, its web interface and API on 8025, messages in `es-<id>-data` (`MP_DATABASE`), `MP_MAX_MESSAGES` = `keepMessages`. SMTP requires the user and password (`MP_SMTP_AUTH`), allowed without TLS inside networks (`MP_SMTP_AUTH_ALLOW_INSECURE`). The web interface and API use the same credentials (`MP_UI_AUTH`). Both ports are published on loopback ports, so the machine's user can open `webUrl`.
  - **Relay.** In relay mode, Mailpit's relay settings (`MP_SMTP_RELAY_HOST`, `_PORT`, `_STARTTLS`/`_TLS`, `_AUTH=plain`, `_USERNAME`, `_PASSWORD` from the secret) and `MP_SMTP_RELAY_ALL=true` forward every accepted message and keep a copy. A mode or relay change recreates the container on the same volume (EML-06).
  - **Relay results.** Mailpit records no per-message relay outcome (its documentation, read 2026-10-21), so the API reports `relayed` for every message of a relaying service and `captured` otherwise; a refusal by the provider shows in the service's logs.
  - **Control-plane access.** A new package `internal/mail` (`Client`: `Send`, `List`, `Get`) talks SMTP and Mailpit's API to the service's loopback ports with its credentials; `internal/mail/memory` stands in for the fake engine. Services use it for the data endpoints; the reconciler never needs it.
  - **No direct delivery.** EnclaveStack doesn't run an MTA that delivers to recipients' servers: mail from residential addresses is mostly refused or marked as spam, and the owner chose catch + relay (2026-10-21).
- **Consequences**: an email service reserves 128 MiB. Sender checks apply to the API only (EML-05).
- **As implemented**: Mailpit signs in to the relay with LOGIN when the relay has no TLS (`MP_SMTP_RELAY_AUTH=login`), and with PLAIN otherwise: its SMTP client refuses PLAIN on an unencrypted connection, and the relay failed silently on real Docker until this changed.

## ADR-033 — CDN: an nginx cache container per CDN, behind the edge
- **Status**: Accepted (DAR-025) — **implemented** in v0.6 (2026-10-21), `L2-008` CDN-01 – CDN-07
- **Decision**:
  - **Container.** Each CDN runs `nginx` (pinned by digest) as `es-cdn-<name>` with a cache in `es-<id>-data`: `proxy_cache_path` with `max_size` = `maxSizeMiB`, `inactive=7d`, `use_temp_path=off`; `proxy_cache_valid any <defaultTtl>s` applies only when the origin sends no `Cache-Control`/`Expires`; `proxy_cache_bypass`/`proxy_no_cache` for requests with `Authorization`; nginx itself never caches `no-store`, `private` or `Set-Cookie` responses; `proxy_cache_use_stale error timeout http_502 http_503 http_504 updating` with `max_stale` a day (CDN-06); `proxy_cache_lock on`; `add_header X-Cache $upstream_cache_status always`. Only GET and HEAD are cached; other methods pass through.
  - **Placement.** The container joins the origin app's network and the enclave network (where the edge and object store are). Origins: an app's stable alias and port (ADR-016), or the object store with the path `/<bucket>`.
  - **Edge.** The edge configuration (ADR-015) gains one route per CDN: hostname → `es-cdn-<name>:8080`, with the CDN's TLS mode, redirect and DNS-01 zone exactly as a domain's; certificates come the same way (ADR-024). A hostname is unique across domains and CDNs.
  - **Buckets.** A bucket behind a CDN gets an anonymous read-only object policy (`s3:GetObject` on `<bucket>/*`, no listing), set by the reconciler when the CDN becomes ready and removed when the last CDN on it goes (CDN-02). The objects are public through the CDN anyway; L3-006 §8 records the trade-off.
  - **Purge** runs `find /cache -type f -delete` in the container (CDN-05). `cachedBytes` comes from `du` on each drift pass.
- **Consequences**: the driver gains `PurgeCache` and `CacheUsage`, and the object store driver `SetBucketPublicRead`. A CDN reserves 128 MiB.

## ADR-034 — Transcoders: FFmpeg runs fed and emptied through the engine's archive API
- **Status**: Accepted (DAR-026) — **implemented** in v0.6 (2026-10-21), `L2-008` TRN-01 – TRN-08
- **Decision**:
  - **Watching.** The scheduler (ADR-017) lists each watching transcoder's input folder every 10 s through the bucket API (`backup.Store`, reused for any bucket). The first listing after it becomes ready records every object as seen without a run (TRN-03). Afterwards, each new key (or a key whose ETag changed) gets a run with trigger `watch`. Seen keys live in `transcoder_objects` (L3-005 §8).
  - **Runs.** Each run is a container from `linuxserver/ffmpeg` (pinned by digest) created without a network, with the run's memory limit. The control plane streams the input object into it (`PUT /containers/{id}/archive`, a tar of one file, never held in memory), starts it, waits up to `timeoutSeconds`, then streams `/out` out (`GET …/archive`) and uploads each file to the output bucket. The container runs `sh -c` with one FFmpeg command per preset, built from a fixed table; nothing from the user reaches the command line except the input file name, which is replaced by `input.<ext>`.
  - **Presets:**

    | Preset | FFmpeg | Output |
    |---|---|---|
    | `mp4-480p` / `mp4-720p` / `mp4-1080p` | `-vf scale=-2:'min(<h>,ih)' -c:v libx264 -preset veryfast -crf 23 -c:a aac -b:a 128k -movflags +faststart` | `<preset>.mp4` |
    | `webm-720p` | `-vf scale=-2:'min(720,ih)' -c:v libvpx-vp9 -b:v 0 -crf 33 -deadline realtime -cpu-used 8 -c:a libopus` | `webm-720p.webm` |
    | `hls-720p` | the 720p H.264 settings, `-f hls -hls_time 6 -hls_playlist_type vod` | `hls-720p/index.m3u8` and segments |
    | `mp3` | `-vn -c:a libmp3lame -q:a 2` | `mp3.mp3` |
    | `thumbnail` | `-vf thumbnail,scale=640:-2 -frames:v 1` | `thumbnail.jpg` |

  - **Concurrency.** At most `concurrency` runs of one transcoder at once; further keys wait as runs in status `running` but not started, in key order.
  - **Results.** A run records the input `key`, the output keys and, when FFmpeg fails, the end of its output (JOB-04's 64 KiB).
- **Consequences**: the driver gains `Transcode`, a one-off container like a job run (ADR-017) but fed and emptied by the control plane. A transcoder reserves `memoryMiB` × `concurrency`.
- **As implemented**:
  - **Watched keys over the limit wait for the next listing** instead of queueing as runs. A manual run (`TRN-04`) when `concurrency` runs are going is refused with `RUN_IN_PROGRESS` (409), so the caller knows to try again.
  - **The script creates `/out` first**: FFmpeg doesn't create the folder for its output.
  - **Metrics** are the shared run metrics, `enclave_runs_total{kind="transcoder"}` and `enclave_run_duration_seconds`, not separate transcode metrics.

## ADR-035 — Projects: namespaces on one machine, with `default` keeping v0.6 names
- **Status**: Accepted (DAR-027) — planned for v0.7, `L2-009` PRJ-01 – PRJ-09
- **Context**: v0.7 adds projects. The owner chose full namespaces now (2026-10-22): the same name may exist in two projects. Everything that exists must keep working unchanged.
- **Decision**:
  - **Store.** A `projects` table holds each project (name, description, creation time); `default` is inserted by the migration and can't be deleted. `resources` gains `project TEXT NOT NULL DEFAULT 'default'`, and the uniqueness key becomes `(project, kind, name)`. Every lookup by name takes the project: `GetResourceByName(ctx, project, kind, name)`.
  - **References stay inside a project.** The service resolves every name in a spec in the resource's own project (`resolveRefs`). The reconciler does the same when it reads secrets, networks, apps and buckets by name.
  - **Machine-wide facts stay machine-wide**, checked across all projects: bucket names (the object store has one namespace), domain and CDN hostnames, zone domains, and network ranges.
  - **Engine-side names.** One helper, `domain.Qualified(project, name)`, returns `name` in `default` and `name.project` elsewhere (names can't contain dots, so this never collides). Container names (`es-<kind>-<qualified>`), revision names, Docker network names (`es-net-<qualified>`) and VM process names use it. Private DNS is `<name>.<kind>.internal` in `default` and `<name>.<kind>.<project>.internal` elsewhere. On the broker, a queue or topic is known by one key: `<name>` in `default`, `<project>_<name>` elsewhere (names have no underscores, so the key splits back unambiguously). Users, subjects and streams are built from the key as in v0.6 (`q-<key>`, `es.q.<key>`, `ESQ_<key>`). Data volumes and filesystem volumes are already named by ID.
  - **Object-store credentials per project.** `default` keeps `objectstore-credentials` (the store's root credentials, as in v0.6). The first bucket of any other project creates a system-owned `objectstore-credentials` secret in that project, for a MinIO user `es-p-<project>` whose policy allows only `arn:aws:s3:::<bucket>` and `<bucket>/*` of that project's buckets. The reconciler rewrites the policy whenever a bucket of the project is created or deleted, through MinIO's admin API (`madmin-go`).
  - **API.** `Resource.project`; `CreateResourceRequest.project` (default `default`); `GET /resources?project=`; `GET|POST /projects`, `GET|PATCH|DELETE /projects/{name}`.
- **Consequences**:
  - Migration `0012` rebuilds `resources` with the new column and key; every row lands in `default`. Nothing on the engine is renamed.
  - Moving a resource between projects isn't possible (its engine names would change); it is out of scope.
  - Two projects can't share a bucket, a network or a secret. A team that needs that keeps those resources in one project.

## ADR-036 — Roles: three roles, machine-wide or per project; tokens narrow them
- **Status**: Accepted (DAR-028) — planned for v0.7, `L2-009` IAM-01 – IAM-10, TOK-01 – TOK-05
- **Context**: v0.2 has one admin and tokens with a `read` or `write` scope (ADR-007). v0.7 adds people, roles and project-scoped tokens.
- **Decision**:
  - **Roles are ordered**: `viewer` < `editor` < `admin`. Each request needs one role in one project (or on the machine): reading needs `viewer`; secret values, run output, queue messages and emails need `editor` (they may hold anything an app wrote); changes and actions need `editor`; people and tokens in a project need `admin` there; projects, machine settings, users' machine roles and the audit list need the machine role `admin`.
  - **Effective role** in project P = max(user's machine role, user's role in P). For a token: min(that, the token's role), and nothing in projects the token doesn't list. It is computed on every request from the current rows, so a change applies to the next request (IAM-06).
  - **Store.** `users` gains `machine_role` (nullable), `last_sign_in_at`, and `password_hash` may be empty for a user who hasn't accepted an invite. New tables `project_members (user_id, project, role)`, `invites (user_id, code_hash, kind, expires_at)` and `audit_log`. `api_tokens.scope` becomes `role` (`viewer`/`editor`/`admin`) with `projects_json` (null for all projects); the migration maps `read` → `viewer` and `write` → `editor`.
  - **Invites and resets** reuse the setup code's machinery: a random code shown once (as a link `<console>/invite?code=…`), stored as a SHA-256 hash, valid 7 days, single use. Accepting sets the password (argon2id, ADR-007), deletes the invite and starts a session.
  - **Enforcement.** Routes keep authentication in the middleware. Authorization moves into the service: every method that touches a resource loads it, then asks `Principal.Require(project, role)`. A resource in a project the caller can't see answers `404 NOT_FOUND`; a visible one the caller can't change answers `403 FORBIDDEN` with the role needed. Lists filter by the projects the caller can see.
  - **Audit.** Sign-ins (and failures), people, roles, tokens, projects and secret reveals append an `audit_log` row: time, actor email, action, target, detail, client IP. Rows older than 90 days are swept.
- **Consequences**: `Principal` carries the user's roles and the token's limits. `GET /session` returns the machine role and project roles, so the console hides what the user can't do. The old `scope` field of tokens stays readable (derived from the role) for clients from v0.6.

## ADR-037 — Metrics: sampled by the control plane, kept in SQLite
- **Status**: Accepted (DAR-029) — planned for v0.7, `L2-009` MET-01 – MET-07, DSH-01 – DSH-04
- **Context**: v0.7 adds per-resource metrics. The owner chose to keep them in EnclaveStack, with no Prometheus container (2026-10-22).
- **Decision**:
  - **Package.** A new `internal/monitor` holds the sampler, the alert evaluator (ADR-038), the notifier (ADR-039) and the log shipper (ADR-040). It runs next to the reconciler and uses only read-only engine methods (`internal/archtest`).
  - **Sampling** every 30 s:
    - containers: the driver's new `Stats(ctx, []*domain.Resource)` reads one-shot container stats (`stream=false, one-shot=true`) for every container of each resource. CPU and I/O are cumulative counters, so the sampler keeps the previous sample in memory and stores rates; the first sample after a start stores nothing for them. Restarts come from the engine's restart count and from new container IDs;
    - VMs: the hypervisor's QEMU process CPU time and resident memory;
    - edge: the edge enables Caddy's per-host metrics (`metrics.per_host`); the sampler reads `localhost:2019/metrics` inside the edge container (an exec, as the CDN's `du` does) and maps hosts to domains and CDNs;
    - queues and topics: the broker's queue stats (ADR-029) and the `subscription_stats` counters;
    - volumes: used bytes from the driver and hypervisor; percent full = used / size;
    - machine: CPU and memory summed over everything EnclaveStack runs, against the engine's CPUs and memory; disk free of the data directory (`statfs`).
  - **Storage.** `metric_points (series_id, ts, value)` holds 30-second points for 24 hours; `metric_rollups (series_id, ts, avg, max, count)` holds 15-minute buckets for 30 days. `metric_series (id, resource_id, metric)` names each series, so points are three integers and a real. Each sample is one transaction. A sweep every 15 minutes rolls up finished buckets and deletes old points. Series of deleted resources are deleted with them.
  - **Query.** `GET /resources/{id}/metrics?metric=…&from=…&to=…` picks the step (≥ 30 s, at most 500 points); ranges inside the last 24 hours read points, others read rollups (avg, with max returned too).
  - **Prometheus.** `/metrics` gains `enclave_resource_value{project,kind,name,metric}` with the latest value of each series.
  - **Dashboards** are a resource kind (`dashboard`) with only a spec; the reconciler marks them ready. Widgets name resources but don't reference them (DSH-02), so deleting a resource on a dashboard is allowed.
- **Consequences**: about 13 series per running resource; 50 resources write ~650 rows every 30 s (~1.9 M rows a day before sweeping). The budget in MET-06 is checked in `L5-003`. A gap while EnclaveStack is down shows as missing points, not zeros.

## ADR-038 — Alerts: evaluated every 30 s, with incidents and persisted state
- **Status**: Accepted (DAR-030) — planned for v0.7, `L2-009` ALR-01 – ALR-08
- **Decision**:
  - **Kind.** `alert` has a spec only; the reconciler checks its channels exist and marks it ready. Its channels are references (CP-12); the resources it watches are not (a deleted one makes it "no data").
  - **Evaluator** (in `internal/monitor`), every 30 s after the sampler, for each enabled ready alert and each watched resource (a *subject*):
    - metric: the last `forMinutes` of points all above (or below) the threshold → firing; none → no data; otherwise ok;
    - crash loop: the restarts recorded in the last `windowMinutes` ≥ `restarts`;
    - failed: the subject's phase is `failed`;
    - run failed: a run of the subject finished `failed` or `timed_out` since the last check (it fires and resolves in one step: a single notification).
  - **State.** `alert_states (alert_id, subject_id, state, since, last_notified_at, incident_id)`. A change to firing opens an incident (`incidents`) and queues a notification per channel; a change back resolves it and queues the resolution. The alert's outputs hold the worst state of its subjects. State survives restarts, so a firing alert isn't re-announced (ALR-08).
  - **Test** queues a test notification per channel and waits up to 15 s for the outcomes.
- **Consequences**: alerts fire at most one evaluation (30 s) after their condition is met for its duration (ALR-07).

## ADR-039 — Notification channels: a persisted outbox with retries
- **Status**: Accepted (DAR-031) — planned for v0.7, `L2-009` NCH-01 – NCH-06
- **Decision**:
  - **Outbox.** `notifications (id, channel_id, incident_id, event, status, attempts, next_attempt_at, last_error, created_at, sent_at)`; the notifier sends due rows every 2 s. A failure is retried after 15 s, 30 s, 60 s, 120 s and 240 s (≈ 5 minutes), then marked failed with an event on the channel. The last 100 rows of each channel are kept.
  - **Formats.** Email: through the email service's SMTP with its own credentials (ADR-032, `internal/mail`); a text body and a subject `[EnclaveStack] FIRING: <alert> on <kind>/<name>`. Webhook: `POST` of a JSON body (`event`, `alert`, `project`, `resource`, `state`, `value`, `threshold`, `startedAt`, `resolvedAt`, `url`) with `X-Enclave-Signature: sha256=…` when it has a secret, as topics sign (ADR-029). Slack: `{"text": …}`; Discord: `{"content": …}`; Telegram: `POST https://api.telegram.org/bot<token>/sendMessage` with `chat_id` and `text`. A 2xx reply is delivery. Redirects aren't followed; each request has a 10 s timeout.
  - **Test hosts.** The Telegram base URL is a server option (`--telegram-api`, default `https://api.telegram.org`) so tests can use a local fake.
- **Consequences**: notifications survive restarts of EnclaveStack; a webhook may see one twice when EnclaveStack stops between the reply and recording it.

## ADR-040 — Log export: a shipper that follows the engine's logs with checkpoints
- **Status**: Accepted (DAR-032) — planned for v0.7, `L2-009` LOG-01 – LOG-08
- **Decision**:
  - **Reading.** The driver gains `LogsSince(ctx, r, since)`: every container of the resource (all replicas and revisions), lines with the engine's nanosecond timestamps and the container name. VMs give their boot console. The shipper polls each source of each ready export every `flushSeconds`.
  - **Checkpoints.** `logexport_cursors (export_id, container, ts, skip)`: the timestamp of the last line sent and how many lines with that timestamp were sent. A batch is recorded only after the destination accepted it (at least once, LOG-06). A container whose first available line is newer than its checkpoint lost lines to the engine's log rotation: the shipper records a gap event with the range. *As implemented:* the engine is asked for lines at or after the checkpoint's time, so the checkpoint's own line comes back while it is kept; the first `skip` lines at that time are dropped, and a container whose read-back lacks the checkpoint's line but has newer ones gets an `export_gap` event (and `enclave_log_export_gaps_total`).
  - **Batches** up to 1 MiB or `flushSeconds`. Each line: `{time, project, kind, name, container, stream, message}`.
  - **Destinations**, all over plain HTTP clients (no new SDKs):
    - bucket and s3: a gzip JSON-lines object `<prefix>/<yyyy>/<mm>/<dd>/<hh>/<kind>-<name>-<unix-nanos>.ndjson.gz` through the bucket client the backups use (`internal/backup`);
    - http: `POST` with `Content-Type: application/x-ndjson`;
    - otlp: `POST <endpoint>/v1/logs` with the OTLP/HTTP JSON encoding: one resource per source (`service.name` = name, `enclave.project`, `enclave.kind`), one log record per line;
    - loki: `POST <url>/loki/api/v1/push` with one stream per source and stream (labels `project`, `kind`, `name`, `stream` plus the export's labels).
    The headers secret is a JSON object of header names to values, added to every request (tokens, API keys).
  - **Backoff.** A failed batch is retried after `min(2^n s, 5 min)`; the export stays ready with `lastError` in its outputs and an event the first time.
- **Consequences**: an export reserves no memory of its own; reading logs is the engine's work. Lines are sent once per export, so two exports of the same source send each line twice (once each).

## ADR-041 — External secret stores: plain HTTP clients, values cached encrypted
- **Status**: Accepted (DAR-033) — planned for v0.7, `L2-009` EXT-01 – EXT-08
- **Decision**:
  - **Package.** `internal/secretstores` defines `Store { Fetch(ctx, key, field) (string, error); Test(ctx) error }` with one client per provider, over `net/http` and no vendor SDK:
    - vault: `GET <address>/v1/<mount>/data/<key>` with `X-Vault-Token` (and `X-Vault-Namespace`); the value is `data.data[field]`;
    - onepassword: Connect API — the vault and item by ID or by title (`GET /v1/vaults?filter=name eq "…"`, `GET /v1/vaults/<id>/items?filter=title eq "…"`), then the item; `field` matches a field's label or ID;
    - aws: `secretsmanager.GetSecretValue` (`POST` with `X-Amz-Target`) signed with SigV4. The signer moves from `internal/dns/route53` to a shared `internal/sigv4`, used by both;
    - doppler: `GET https://api.doppler.com/v3/configs/config/secret?name=<key>` with the service token;
    - infisical: universal-auth login (`POST /api/v1/auth/universal-auth/login`), then `GET /api/v3/secrets/raw/<key>?workspaceId=…&environment=…`.
    Without `field`, a value with one field is that field; a value with several is refused with the field names listed. AWS's JSON values take `field` as a key; a plain-text value ignores it. Each store takes an optional `endpoint` for testing and for self-hosted Doppler-compatible hosts.
  - **Sync.** A secret with `source` is reconciled by fetching its value: changed ciphertext bumps `outputs.version` and records an event; the next refresh is `next_run_at` (the scheduler column, ADR-017), every `refreshSeconds` of its store. `refresh` sets it to now. A failure keeps the last value and sets the message; a secret that never had a value is `failed`.
  - **Store reconcile** runs `Test`: wrong credentials or an unreachable store make it `failed` with the provider's message (credentials never appear in messages).
- **Consequences**: values never touch the logs or events. A rotated value reaches apps at their next deploy, like a secret edited by hand.

## ADR-042 — Machines and the agent: the control plane calls each agent over mutual TLS
- **Status**: Accepted (DAR-034) — planned for v0.8, `L2-010` MCH-01 – MCH-12
- **Context**: v0.8 runs resources on more than one computer. The control plane stays on the main machine (`L2-010` §6). Everything it does to the engine today goes through `engine.Driver` and `engine.Hypervisor` (ADR-006, ADR-025), in process.
- **Decision**:
  - **The agent** is `enclave agent run`, the same binary. It runs the Docker driver and the QEMU hypervisor of the computer it's on, with no store, no reconciler and no API of its own, and serves them to the control plane over HTTPS on port 7071. It keeps its identity in `~/.enclave-agent` (mode 0700): its private key, its certificate, the CA certificate, the machine ID and its settings.
  - **One request per engine call.** `POST /agent/v1/<driver|hypervisor>/<Method>` carries the arguments as JSON and returns the result as JSON. Errors keep their class: permanent (`engine.Permanent`), engine unavailable (`engine.ErrUnavailable`), no VMs (`engine.ErrNoVMs`), job missing (`engine.ErrJobMissing`), no CA (`engine.ErrNoCA`), or transient, each with its message. The caller's deadline travels in `X-Enclave-Deadline`, and the agent cancels the call when the control plane hangs up.
  - **Streams.** Calls that read or write data stream the request or response body. The arguments then travel in `X-Enclave-Args` (base64url JSON), and an error found after the stream started arrives in the `X-Enclave-Error` trailer. These calls are `Dump`, `Restore`, `SnapshotVolume`, `RestoreVolume`, the data-volume copies of ADR-047, and the disk copies. A build sends its context directory as a tar body and gets its log lines back.
  - **Callbacks.** `Ensure` and `EnsureVM` report steps, notes and events while they work, and `Ensure` calls the `Started` hook (ADR-021) before new containers take traffic. Their response is a stream of JSON lines: `step`, `note`, `event`, `started` and finally `result` or `error`. On `started`, the control plane runs the hook and answers `POST /agent/v1/calls/<id>/continue`; the agent waits for that answer before going on.
  - **Files the control plane holds** reach the agent before the call that needs them: a snapshot file used as a VM image or to fill a disk volume is uploaded to the agent's cache (`PUT /agent/v1/files/<sha256>`) and the call names the agent's copy. `SnapshotDisk` streams the copy back and the control plane writes it to its snapshot directory.
  - **Version.** `GET /agent/v1/info` returns the agent's version, protocol (1), engine and host facts, VM support and tunnel public key. A different minor version is shown on the machine and nothing new is placed there (MCH-10).
  - **Health.** The control plane calls `info` on each machine every 10 seconds with a 5-second timeout (ADR-046).
- **Consequences**:
  - `internal/agent` holds the server (on the agent) and the client (`agent.Remote`, which implements `engine.Driver` and `engine.Hypervisor`). Both are tested against each other in-process, over real TLS, with the fake driver behind the server.
  - Secret values (`EnsureInput.Env`, passwords, relays) cross the wire inside mutual TLS only. The agent never logs a request body.
  - Throughput of data copies is that of one HTTPS stream through the main machine.

## ADR-043 — Machine identity: an EnclaveStack certificate authority, one-time join codes
- **Status**: Accepted — planned for v0.8, `L2-010` MCH-02 – MCH-04, MCH-09
- **Decision**:
  - **Certificate authority.** The first `machine add` creates an ECDSA P-256 CA, valid 10 years. Its key is kept in the store, encrypted with the machine's master key (`secretbox`, ADR-007). The control plane gets a client certificate from it at every start (CN `enclave-control-plane`, valid 30 days, kept in memory).
  - **Join codes.** `POST /machines` makes a machine in state `waiting` with a code: 20 random bytes, base32, shown once. The store keeps its SHA-256 and an expiry one hour away. A new code for the same machine (`POST /machines/{name}/join-code`) replaces the old one.
  - **Joining.** `enclave agent join <main> --code C` makes an ECDSA P-256 key and sends `POST /api/v1/machines/join` (public, rate-limited) with the code, a certificate request, the address the agent advertises, its agent and tunnel ports, and its facts. A valid, unexpired, unused code returns the machine ID, a certificate for the agent (URI SAN `enclave://machine/<id>`, valid 1 year), the CA certificate and the control plane's URL. The code is spent in the same transaction. The control plane then calls the agent's `info` to bring the machine `online`.
  - **Mutual TLS.** TLS 1.3 only. The agent requires a client certificate chaining to the CA with CN `enclave-control-plane`. The control plane requires the agent's certificate to chain to the CA, carry this machine's URI SAN, and have the serial recorded for it.
  - **Renewal.** When an agent's certificate has less than 60 days left, the control plane asks for a new request (`POST /agent/v1/renew`), signs it and the agent switches to it.
  - **Removal.** Removing a machine deletes its row, so the control plane never trusts that certificate again. `enclave agent leave` removes EnclaveStack's containers, volumes and networks on that computer and deletes `~/.enclave-agent`.
- **Consequences**: a stolen agent identity can't act as the control plane, because only the control plane's client certificate opens an agent. A stolen join code is useless after one use or one hour. Backups of the store include the encrypted CA key, which only the master key opens.

## ADR-044 — Placement: one machine per resource, kept in the store; an engine router
- **Status**: Accepted (DAR-039) — planned for v0.8, `L2-010` PLC-01 – PLC-08
- **Decision**:
  - **Store.** A `machines` table holds each machine. The main machine is the row with `is_main = 1`: its ID (`mch_…`) and name (the computer's name, numbered if taken) are made when the server first starts, never fixed words. `resources` gains `machine_id`, so every existing row is on the main machine.
  - **Spec.** App, database, cache, job, load balancer, VM, volume, search and email specs gain `machine` and `machineLabels`. Placement is the reconciler's: a resource with no machine is placed by `domain.ChooseMachine` before its first ensure, in a transaction that counts every resource's reservation per machine (CP-16 per machine). It considers online, schedulable machines of the same minor version, with the labels and the support the kind needs, and the machine of the volumes it uses (PLC-04). The resource is placed on the one with the most free memory, the main machine on a tie. If none fits, the resource waits with the reason and tries again every 30 seconds.
  - **Pinned kinds.** Buckets, queues, topics, domains, CDNs, transcoders, zones, records and the system containers stay on the main machine. Secrets, networks, firewalls, backups, snapshots and the v0.7 kinds have no runtime of their own and keep the main machine.
  - **Engine router.** `internal/engine/cluster.Router` implements `engine.Driver` and `engine.Hypervisor`. It routes each call by the machine of the resource it names: in process for `main`, `agent.Remote` for the others. Calls naming no resource go to the machine they concern. The edge, broker, object store, certificates, transcoding and project store access go to `main`. Networks go to every online machine. A job run goes to its job's machine, a build to its app's machine, a dump to its database's machine. A call to an offline machine fails at once with `ErrMachineOffline`, which the reconciler treats like `ErrUnavailable`, with the machine in the message. The reconciler, services and monitor keep their single `Driver` and `Hypervisor`, so archtest still forbids services the state-changing calls.
  - **Outputs.** Every placed resource reports `machine` (its name). Published ports bind on that machine's publish address, and `host` is the machine's advertised address.
- **Consequences**:
  - `domain.Resource` gains `Machine` (an ID); the router reads it, so nothing below the reconciler changes signature.
  - With one machine, the router sends everything to the main machine and every output keeps its value (PLC-08).

## ADR-045 — Networks across machines: a mesh container per machine, stand-ins, address pools
- **Status**: Accepted (DAR-035, DAR-036; spiked 2026-10-22) — planned for v0.8, `L2-010` XNET-01 – XNET-08
- **Context**: networks are Docker bridges, one per machine. On Docker Desktop the engine runs in a VM behind NAT, so nothing outside a container can join a bridge or route into it (ADR-026).
- **Decision**:
  - **Every network on every machine.** The router ensures each network on every online machine, with the same range. The default network `enclave` takes the main machine's range on every other machine. A machine that has a foreign network overlapping that range reports it, as NET-01 does.
  - **Mesh container.** With two or more machines, each machine runs `es-system-mesh` from `enclave.local/mesh:<hash>`. The image is built on the machine through the build path (ADR-019) from an embedded Dockerfile: a pinned `golang` stage compiles the `es-mesh` helper (standard library only), and a pinned `alpine` stage adds `wireguard-tools`, `iproute2` and `nftables`.
    - It runs with only `NET_ADMIN`, `net.ipv4.ip_forward=1`, and the volume `es-system-mesh-data` for its private key.
    - It is connected to every EnclaveStack network with a fixed MAC per network (`02:65:73` and three bytes of the network's hash) and an address from its pool.
    - It publishes the tunnel port (51820/udp) on the machine's tunnel address.
    - It holds one WireGuard interface `es0` with a peer for every other online machine. A peer's allowed IPs are that machine's container addresses, one /32 each.
  - **Routing.** For every remote address, the mesh adds a /32 route over `es0` and a proxy neighbour entry on the network's interface, so local containers' ARP for it is answered by the mesh. No address is translated: the receiver sees the real source, and firewalls (ADR-021) match unchanged.
  - **Isolation.** The mesh's nftables table forwards only between `es0` and a network's interface when both source and destination are in that network's range. It clamps TCP MSS to the path, and drops everything else. Ranges never overlap (NET-01), so traffic can't cross networks. Nothing forwarded from `es0` leaves through an interface with internet, so `internet: false` holds (XNET-05).
  - **Stand-ins.** For every container on another machine, in every network, each machine runs a stand-in `es-standin-<hash>`: a container on that network with the remote container's address and aliases. Docker's DNS therefore answers `.internal` names and stable aliases with remote addresses, and the local engine never hands the address out. A stand-in runs `es-mesh standin`, which deletes its own copy of the address, points it at the mesh (route and permanent neighbour entry to the mesh's fixed MAC) and forwards whatever it receives. Docker announces a new endpoint's address even when told not to (spike, DAR-035); a neighbour that cached the stand-in still reaches the right machine. A stand-in has only `NET_ADMIN`, forwarding on and redirects off, and about 1 MiB of memory.
  - **Address pools.** With two or more machines, every container connected to a network takes an explicit address: the lowest free address of its machine's pool within its subnet. The table `address_pool (network, address, machine_id)` assigns each address to at most one machine. The control plane keeps at least eight free addresses (fewer in small subnets, never fewer than a placement needs) in each machine's pool for each subnet it uses. It passes the pools with the mesh configuration. A machine whose pool is empty waits with a transient error until it's topped up. When the second machine joins, the addresses the main machine already uses go into its pool first.
  - **Mesh sync.** A loop in the reconciler builds the whole mesh configuration of each machine every 5 seconds, and at once after a placed resource's containers change. The configuration holds its peers, its pools, its stand-ins, and the default network's range. `Driver.ConfigureMesh` applies it whole and idempotently, as `ConfigureEdge` does. The router wraps `Started` (ADR-021), so a new revision's addresses are announced to every machine before its health check (XNET-07). `Driver.NetworkEndpoints` lists a machine's managed containers per network (name, address, aliases), which the sync reads.
  - **Keys.** `es-mesh` makes the machine's WireGuard key on first start and keeps it in its volume. The public key is reported by `info` and handed to the peers. Private keys never leave their machine.
- **Consequences**:
  - **Engine contract:** the `Driver` gains `ConfigureMesh`, `MeshStatus` (peers and their last handshake, XNET-06) and `NetworkEndpoints`. `Placement` gains `Address` (an address to keep, ADR-047).
  - **One machine:** no mesh, no stand-ins, no pools; addresses are chosen as in v0.7.
  - **Limits:** one stand-in per remote container per machine; throughput is WireGuard's, measured in `L5-003`.

## ADR-046 — Offline machines: health checks, apps restarted elsewhere, clean-up on return
- **Status**: Accepted — planned for v0.8, `L2-010` MCH-06, FAIL-01 – FAIL-06
- **Decision**:
  - **Health.** `internal/machines.Monitor` calls `info` on every machine every 10 seconds (5-second timeout). It records `last_seen_at` and the facts. It turns a machine `offline` after 30 seconds without an answer and `online` at the first answer, with an event on the machine and on what runs there. The monitor's alerts gain the condition `machine` (offline, for 1 – 60 minutes, default 1) on named machines.
  - **Waiting.** A call to an offline machine fails at once with `ErrMachineOffline`. The reconciler waits 30 seconds between tries and the message reads "`<machine>` is offline since `<time>`". Drift checks skip resources on offline machines. The phase doesn't change (FAIL-01).
  - **Restarting elsewhere.** Once a machine has been offline for 60 seconds, the reconciler re-places every app (and image job) on it that has no volume, no `machine` of its own, and labels another machine has. It sets the new machine, bumps nothing in the spec, and deploys a new revision there, with an event naming both machines (FAIL-02). Everything else waits (FAIL-03).
  - **Clean-up.** When a machine comes back, the router first calls `Sweep` on it with the IDs of resources still placed there. The machine removes the containers of every other resource EnclaveStack manages (revisions, replicas, job runs), never volumes. Only then is the machine `online` for placement and mesh sync (FAIL-04).
  - **Main machine down.** Agents keep what runs (containers restart unless stopped, VMs keep running); nothing changes until the control plane is back (FAIL-06).
- **Consequences**: a flapping machine moves its apps at most once per outage, because a re-placed app stays where it went.

## ADR-047 — Moving: copy, then switch
- **Status**: Accepted (DAR-037) — planned for v0.8, `L2-010` MOVE-01 – MOVE-07, MCH-08
- **Decision**:
  - **Trigger.** A resource whose spec `machine` (or labels) no longer matches its `machine_id` is moved by its next ensure. While its machine is offline, a resource with data refuses the change, in the service, with the reason (FAIL-03).
  - **App or job.** The resource gets the new `machine_id` and a normal deploy: the new revision starts on the new machine, takes traffic once healthy, and then the router removes every container of the app on the old machine (MOVE-01).
  - **Database, cache, search, email.**
    - The container on the old machine is stopped.
    - Its data volume streams to the new machine: `Driver.ExportData` writes the volume as a tar stream through a short-lived helper container, and `Driver.ImportData` fills a new volume of the same name.
    - The address moves from the old machine's pool to the new one's, and the container is created there with `Placement.Address` set to it.
    - Once healthy, the old container and volume are removed.
    - If any step before that fails, the container on the old machine is started again and the resource stays there (MOVE-02, MOVE-06).
    - The interruption, from stop to healthy, is recorded in the move's event.
  - **Volume.** What uses it is stopped first: apps and jobs on the old machine are removed, VMs shut down. Then the files are copied (`SnapshotVolume` into `RestoreVolume`, ADR-028) or the disk (`ExportDisk` into `ImportDisk`). The volume and its users get the new `machine_id`, and the users are reconciled there (MOVE-03).
  - **VM.** The VM is shut down (VM-06), its root disk and attached disk volumes are copied, its link container is created on the new machine with the same address, and the VM is started there. The old directory is removed after it boots (MOVE-04).
  - **Load balancer.** Started on the new machine with the same address, then removed from the old one (MOVE-05).
  - **Progress.** A move adds the step `move` with the bytes copied in the message, refreshed every second.
  - **Drain.** Marks the machine not schedulable and sets a new machine on every resource there that has no `machine` of its own (MCH-08). Each move is then an ordinary one.
- **Consequences**: the `Driver` gains `ExportData`, `ImportData` and `Stop`; the `Hypervisor` gains `ExportDisk` and `ImportDisk` (a disk file as a stream). A move needs the free space of the copy on the new machine; the error says so when it's missing.

## ADR-048 — Catalogue and stacks: templates of API specs, members created in one transaction
- **Status**: Accepted (DAR-038) — planned for v0.8, `L2-010` CAT-01 – CAT-09
- **Decision**:
  - **Templates.** A template is YAML or JSON with `name`, `title`, `description`, `version` (semver), `parameters`, `resources` and `outputs` (`L3-002` §11.6). Each resource is a `kind`, a `name` (empty for the stack's own name, otherwise the member is `<stack>-<name>`), a `spec` in the API's form, and optionally `when`, which leaves the member out when it renders empty or `false`. A secret member may say `generate: <length>` instead of a value.
  - **Placeholders.** `${stack}`, `${project}`, `${param:<name>}`, `${name:<member>}` (the member's full name), `${host:<kind>/<member>}` (the name apps reach it by: its container name, which carries the project, ADR-035) and `${ident:<member>}` (a database's database and user name). A string that is exactly one placeholder takes the parameter's type (number, boolean). Anything else is text. There are no loops, expressions, functions, files or network access.
  - **Catalogue.** Built-in templates are embedded in the binary (`internal/catalogue/builtin`), with images pinned by version. Own templates are rows of `catalogue_templates (name, body, version, created_at, created_by)`, added and removed by machine admins. A name can't shadow a built-in.
  - **Stack.** A new kind, `stack`. Its spec is `template`, or `templateBody` given inline, plus `version` (default: the newest), `parameters` and `secretParameters`, which are write-only and stored as the stack's secrets.
    - Creating one renders the template and validates every member with the same code path as a hand-made create (`createIn`, refactored out of `create`), in one transaction. The installer's role, names, references, placement and quotas are checked. Nothing is created unless everything is valid (CAT-08, CAT-09).
    - Members carry `stack_id`. An update re-renders and diffs in one transaction: new members are created, changed ones updated (`updateIn`), and removed ones marked `deleting`.
  - **Phases.** The stack's reconcile aggregates its members: `ready` when all are ready, `failed` when one is (its message names it), otherwise in progress. It checks again whenever a member changes phase.
  - **Protection.** The service refuses `update` and `delete` of a member, naming its stack (CAT-06). Actions are allowed. CP-12 ignores references between members of the same stack.
  - **Delete.** Deleting the stack marks every member `deleting` in one transaction. A member that something outside the stack still uses refuses the whole delete, with the list. The stack is removed when its last member is. Members delete in any order; a network that still has containers retries until they're gone.
- **Consequences**: stacks add no engine calls of their own. A template is never run as code, so a template from a stranger can create no more than its installer could by hand.

## ADR-049 — Per-app bucket access: one object store user per app or job
- **Status**: Accepted — 2026-10-24, `L2-011` BKA-01 – BKA-10
- **Context**: one key per project (ADR-035) can't say "this app may only read this bucket". The object store (MinIO) already supports users with canned policies, and EnclaveStack already makes one such user per project.
- **Decision**:
  - **User.** An app or job with bucket access gets an object store user named `esr-` and the random part of its resource ID (20 characters, so it fits every object store's access-key limits); its policy (canned, named after the user) allows, per grant: `read` → `s3:GetBucketLocation`, `s3:ListBucket` on the bucket and `s3:GetObject` on its objects; `readwrite` → also `s3:PutObject`, `s3:DeleteObject`, `s3:AbortMultipartUpload` and `s3:ListMultipartUploadParts`, and `s3:ListBucketMultipartUploads` on the bucket. Nothing else: no bucket creation, deletion, policy or admin action.
  - **Keys.** Made by the service when access is first given (create or update), in the same transaction, as the managed secret `<name>-bucket-credentials` owned by the resource. The access key is the user name; the secret key is 24 random bytes, base64url-encoded. Later changes keep them.
  - **When it is applied.** The reconciler calls `Driver.SetStoreUserAccess` before it deploys an app's new revision and before it starts a job's run, so a revision or run never starts with access it doesn't have yet. On delete, and when the list becomes empty, it removes the user and policy; the service deletes the secret with the resource (ownership) or when the list empties.
  - **Variables.** The five of `L3-002` §12.3 are added to the container's environment after `env` and `secretEnv`, which may not use those names, so nothing overrides them.
  - **Jobs as apps.** A job with `app` uses the app's user and keys; the reconciler applies the app's grants before the run.
  - **Shared implementation.** `SetProjectStoreAccess` (ADR-035) becomes a project user with `readwrite` on every bucket of the project, through the same code that writes a user and its policy.
- **Verified**: on real MinIO (`internal/engine/docker`, `TestStoreUserReachesItsGrantsAtTheirLevels`) and end to end with the official AWS CLI reading only the five variables (`tests/e2e`, `TestBucketAccessForRealWithTheAWSCLI`): read lists and reads but can't write, delete, create buckets or reach other buckets; read and write writes; removal revokes the keys at once.
- **Consequences**: one more admin call per deploy of an app with access (idempotent, cheap). The object store is on the main machine, so the call goes there whatever machine the app runs on. Removing a user makes its keys fail at once, including in a running container; the app gets new keys only if access is given again.

## ADR-050 — Transcoder renditions, HLS ladders and notifications: FFmpeg per output, a message per run
- **Status**: Accepted — 2026-10-24, `L2-012` TRX-01 – TRX-10
- **Context**: running a video product needs adaptive HLS, chosen sizes and a "done" signal. MediaConvert's API is large; EnclaveStack's transcoder is a watched folder with fixed presets (ADR-034).
- **Decision**:
  - **Script.** `internal/transcode` still builds the whole FFmpeg script from validated values only: names match `^[a-z0-9][a-z0-9-]{0,31}$`, every other value is a bounded integer. Nothing else the user typed reaches the shell.
  - **Rendition.** One FFmpeg run: `scale=w=W:h=H:force_original_aspect_ratio=decrease:force_divisible_by=2` (never above the source: the box is capped at the input's size), H.264 `-crf 23 -maxrate <kbps>k -bufsize <2×kbps>k` (or VP9 `-crf 33 -b:v 0 -maxrate`), keyframes forced every `gopSeconds` with closed GOPs and no scene-cut keyframes, AAC (or Opus) at `audioKbps`, 48 kHz stereo; MP4 with `+faststart`.
  - **Ladder.** One FFmpeg run per rung with the same keyframe expression, so every rung has keyframes at the same timestamps and segments line up (spiked 2026-10-24 on the pinned image: segment 1 of a 360p and a 720p rung both start on a keyframe at the same time). `-f hls -hls_time <segmentSeconds> -hls_playlist_type vod`. Audio is mapped when present (`0:a:0?`). After the rungs, the script writes `master.m3u8` (`#EXT-X-VERSION:3`, one `#EXT-X-STREAM-INF:BANDWIDTH=<(videoKbps+audioKbps)×1000>,RESOLUTION=<from ffprobe on the rung's first segment>` per rung).
  - **Tags.** The reconciler reads the input's user metadata with `Stat`, renders the output folder from it, and writes it onto every output with the `enclave-*` keys.
  - **Message.** After `finish` records the run, the reconciler builds the message once and sends it: a webhook with up to 3 attempts (0 s, 2 s, 8 s) through `internal/webhook` (no redirects, 10 s each, a 2xx is delivery), signed with the named secret; a topic through the broker, as a publish. A failure of either is an event on the transcoder and never changes the run.
- **Consequences**: a ladder costs one decode per rung (simple, and each rung can fail alone with FFmpeg's own message). The transcode container still has no network: it reads its input from a copy and its outputs are copied out.

## ADR-051 — Tunnels: a connector container per tunnel, into a listener of its own on the edge proxy
- **Status**: Accepted (DAR-040, DAR-043) — built 2026-10-24, `L2-014` TUN-01 – TUN-16, PUB-01 – PUB-10
- **Context**: domains are served by the edge proxy (ADR-015) on the machine's ports 80/443, which the internet can't reach behind NAT or CGNAT. A provider's tunnel reverses the direction: a connector at home dials out and the provider's edge sends visitors down that connection.
- **Decision**:
  - **Plug-ins.** `internal/tunnel` defines `Provider` with `Check` (settings and permissions, TUN-09), `Ensure` (make or adopt anything remote; return the connector's settings), `Route` (domain tunnels: set the hostnames served, whole and idempotent), `Status` (connections and the address the provider gave), `Protect` and `Unprotect` (Cloudflare Access, GATE-09) and `Delete`. The built-in plug-ins are `quick`, `tailscale`, `ngrok`, `cloudflare` and `relay`; `custom` has no remote side. A provider's HTTP client is plain `net/http` with the provider's API base injectable for tests, as the DNS plug-ins (ADR-023). DNS records for public domains go through the existing DNS plug-ins, which gain a `proxied` flag that only Cloudflare reads.
  - **Cloudflare.** `POST /accounts/{account}/cfd_tunnel` with `config_src: "cloudflare"` makes a remotely managed tunnel named `enclave-<project>-<name>`. A tunnel with that name is adopted, which keeps creation idempotent across crashes. Its run token is fetched from Cloudflare each time the connector is made and passed in its environment; it is never stored. `PUT …/cfd_tunnel/{id}/configurations` writes the routing rules: one rule per public hostname to the tunnel's listener (below), then the catch-all `http_status:404`. A public domain writes a proxied `CNAME` to `<id>.cfargotunnel.com` in its zone. Delete runs in reverse: Access applications, DNS records, routing rules, connector, then the tunnel itself with its connections cleaned up.
  - **Quick.** No API. The connector runs `cloudflared tunnel --url http://<listener>` and Cloudflare gives it a random `*.trycloudflare.com` hostname. The reconciler reads it from the connector's metrics server (`/quicktunnel`), or else from the line in its log that announces the URL, and records it as the tunnel's `url`. A new hostname after a restart replaces the old one and is an event. Both ways of reading it are checked in the spike.
  - **The connector.** For each tunnel other than a relay, the container `es-tunnel-<12 hex of the tunnel ID>` runs the provider's own agent, pinned by digest (as ADR-013). Secrets reach it in the environment, as they reach every container:
    - `quick` and `cloudflare`: `cloudflare/cloudflared` with `--no-autoupdate` and its metrics server on `:2000` (`TUNNEL_TOKEN` for `cloudflare`).
    - `tailscale`: `tailscale/tailscale` with `TS_AUTHKEY`, `TS_HOSTNAME`, `TS_STATE_DIR` on the volume `es-tunnel-<hash>-state` (so the device keeps its identity and name across restarts), and `TS_SERVE_CONFIG`, a JSON file EnclaveStack writes: one HTTPS handler on 443 proxying to the tunnel's listener, with `AllowFunnel` for `public`. The reconciler reads `tailscale status --json` by engine exec for the device's DNS name, its online state and whether HTTPS and Funnel are available (TUN-09). Delete runs `tailscale logout` before removing the container and its volume.
    - `ngrok`: `ngrok/ngrok` with `NGROK_AUTHTOKEN`, `http <listener> [--url <domain>]`. The reconciler reads the public URL from the agent's local API (`:4040/api/tunnels`).
    - `custom`: the user's image, command and environment, plus `ENCLAVE_UPSTREAM=http://<edge address on es-system-tunnel>:<listener port>`. The reconciler only knows whether it runs. The user's `url` is shown as given.

    The agents' exact flags and endpoints are checked against the pinned versions in the spike. Hardening: all capabilities dropped, `no-new-privileges`, a read-only root filesystem, 128 MiB of memory. It lives on its own network.
  - **The tunnel network.** `es-system-tunnel` is a bridge with internet access whose only other member is the edge proxy. Connectors are never attached to a user's network, so a connector can reach only the edge proxy and the internet.
  - **A listener per tunnel.** The edge proxy gives each tunnel a server of its own on a port from 11000 up (kept in `tunnel_ports`, `L3-005` §11). It is bound to the edge's address on `es-system-tunnel` and is plain HTTP, since TLS ends at Cloudflare. For a domain tunnel, its routes are exactly that tunnel's public domains, then the fixed 404. For an app link, it has one route, whatever the `Host`: the gate, then the app. So the address the provider picks never has to be known before traffic flows. So a tunnel can reach only its own hostnames (TUN-07), and the console, the API and other domains are never on it. The server trusts `Cf-Connecting-IP` only from its connector's address, which sets the visitor's real address for `X-Forwarded-For` (PUB-07). The routes are rendered with the rest of the edge configuration, whole and deterministic (ADR-015).
  - **Public domains leave the machine's ports.** A domain with a tunnel is routed only on its tunnel's listener, not on the edge's ports 80/443, so its gate can't be bypassed from the machine's own network.
  - **Catalogue.** The built-in catalogue (ADR-048) gains a category `public-access` with one template per way in. Each template's members are a credentials secret (a `secret` parameter) and a `tunnel`, with the app, mode and other settings as parameters. Installing it is a stack, as any template is.
  - **State.** For `cloudflare` and `quick`, the reconciler reads `http://<connector>:2000/ready` (`readyConnections`) through the probe helper (ADR-022); for `tailscale`, `ngrok` and `custom`, as above. The tunnel's `connection` is `connected` with at least one connection, or while a custom connector runs, else `reconnecting`. After 30 s without one the connector is restarted with the usual backoff. A public domain's `tunnelState` follows its tunnel's.
  - **Drift.** Every 10 minutes the reconciler reads each Cloudflare tunnel's routing rules and each public domain's DNS record and Access application. Anything different is put back. A tunnel that is gone at Cloudflare is `failed` until **Retry**.
- **Consequences**: one small container per tunnel. A domain's change re-renders the edge, as today, and re-puts its tunnel's routing rules. Cloudflare sees decrypted traffic for Cloudflare tunnels, because it terminates TLS. That is the provider's model, and the relay (ADR-052) is the alternative. Free Universal SSL covers one level below the zone (`dev.galiaraa.com`, not `api.dev.galiaraa.com`), which PUB-05 reports.

## ADR-052 — Edge machines and relays: joining from the main machine, the mesh behind NAT, TCP passthrough
- **Status**: Accepted (DAR-040, DAR-041) — built 2026-10-24, `L2-014` EDG-01 – EDG-06, TUN-03, PUB-03
- **Context**: a rented server has a public address. Machines join by calling the main machine (ADR-043), but a home main machine can't be called. The mesh (ADR-045) already carries encrypted traffic between machines' containers.
- **Decision**:
  - **Role.** `machines.role` is `worker` (every machine today) or `edge`. An edge machine is never schedulable: placement (ADR-044) skips it and `PATCH` refuses `schedulable: true`. It runs the mesh and the relays that name it, nothing else.
  - **Joining from the main machine.** `POST /machines` with `role: "edge"` and `address` makes the machine `waiting` with a join code, as today. The control plane also keeps a join key, `HMAC-SHA256("enclave-edge-join", code)`, sealed with the master key, until the join ends or expires. On the server, `enclave agent join --edge --code C` makes the agent's key and certificate request, then serves one route, `POST /agent/v1/join`, on `:7071` with a temporary self-signed certificate. Every 5 s for the hour, the control plane:
    1. calls that route with a random nonce;
    2. gets back the request, the agent's facts, its own nonce, and a MAC with the join key over both nonces, the request's hash and the hash of the temporary certificate's public key it saw;
    3. checks the MAC, which proves the agent holds the code and that no one in between swapped the certificate;
    4. signs the certificate (URI SAN `enclave://machine/<id>`, as ADR-043) and sends it, with the CA and its own MAC over the agent's nonce and the certificate's hash.

    The agent checks that MAC, which proves this is the right control plane, then switches to mutual TLS with its new identity. The code is spent in the control plane's transaction. After 5 failed proofs the agent stops listening and must be started again. From then on everything is as for any machine (ADR-042).
  - **The mesh behind NAT.** A machine's tunnel address may be empty: it can't be reached. In its mesh configuration, a peer with an address gets an `Endpoint`, and a peer without one doesn't. A machine with an empty address sends `PersistentKeepalive = 25` to every peer that has one. The edge machine therefore learns the home machine's current outside address from its packets (WireGuard roaming) and replies through the NAT. The main machine's tunnel address is empty unless the admin sets one. Machines that both have none still can't talk (EDG-05).
  - **The edge in the mesh.** An edge machine peers only with the main machine; other machines never hear of it. It is on the default network only. Its peer entry and stand-in name only the edge proxy, and the main machine's entry for it names only its relays' addresses. On the main machine, the mesh helper's nftables chain accepts new connections from those addresses only to the edge proxy's relay listener ports (`address . port` pairs) and drops everything else from them. A compromised edge machine therefore can't reach an app, a database or the gate directly; it reaches what visitors reach, through the same gate.
  - **The relay.** For each relay tunnel, the edge machine runs `es-relay-<12 hex>`, HAProxy (the image of ADR-022) in TCP mode. It publishes ports 80 and 443 on `0.0.0.0` of the edge machine and is connected to the default network `enclave` with an address from the edge machine's pool. It sends each connection, with `send-proxy-v2`, to the edge proxy's relay listeners at the edge proxy's address on `enclave`. That address is reached over the mesh: the edge machine gets a stand-in for the edge proxy (ADR-045). HAProxy checks the listeners every 5 s, and its state is the tunnel's.
  - **The edge proxy's relay listeners.** Each relay tunnel gets a pair of servers on ports from 12000 up: HTTP and HTTPS, both wrapped in Caddy's `proxy_protocol` listener with only the relay's address allowed, both bound to the edge's address on `enclave`. Their routes are exactly that tunnel's public domains, then the fixed 404. TLS ends here, at home, with the certificates of ADR-015 and ADR-024. HTTP-01 works because port 80 is relayed, and DNS-01 works with a zone (whether Caddy answers HTTP-01 on the relay server is checked in the spike). The visitor's address comes from the PROXY header (PUB-07).
  - **DNS.** A public domain on a relay with a zone writes an `A` record to the edge machine's public address (its `address`), owned by the domain, at any DNS provider.
- **Consequences**: the rented server holds an agent identity, a mesh key and nothing else. No certificate key, secret or decrypted byte ever reaches it (EDG-04). Its compromise lets an attacker drop or delay traffic, or read encrypted bytes, and nothing more. Every request crosses the WireGuard link: throughput is that of the home upload, measured in `L5-003`. The edge machine needs inbound 80, 443, 7071 and 51820/udp open.

## ADR-053 — The gate: the edge proxy asks the control plane's gate listener
- **Status**: Accepted (DAR-042; the spike passed: Caddy's `reverse_proxy` to `/check` with `handle_response`, proven on real Docker and through a live quick link) — built 2026-10-24, `L2-014` GATE-01 – GATE-09
- **Decision**:
  - **The gate listener.** The control plane serves `internal/gate` on a second listener, `--gate-listen`, whose default is the address the edge container can reach and nothing outside the computer can: on Docker Desktop, `127.0.0.1:7172`, reached from containers as `host.docker.internal:7172`; on Linux, the host's address on the default network's bridge. The spike confirms both. It serves only `GET /check`, `GET` and `POST /.enclave/gate/signin`, `POST /.enclave/gate/signout` and the sign-in page's assets. Everything else is 404. Every request must carry `X-Enclave-Edge-Key`, a random value made at first start, sealed in the store and rendered into the edge configuration, which is written with mode 0600 (as ADR-024). So only the edge proxy can use the listener, and the visitor addresses it passes on can be trusted for rate limits.
  - **Routes for a gated host** (on its tunnel's listener), in order:
    1. remove every `X-Enclave-*` request header;
    2. open paths go to the app;
    3. `/.enclave/gate/*` goes to the gate listener;
    4. otherwise `forward_auth` to `/check` with the host, the path, the cookie and the visitor's address. A `2xx` lets the request through and copies `X-Enclave-User` to it. Anything else is returned as it is: a `302` to the sign-in page, keeping where the visitor was going, for browsers, and a `401` for requests that ask for JSON.
  - **Sessions.** Signing in checks the password through the console's own sign-in code, with its throttle and audit, then checks access. The person needs a role in the domain's project, or a machine-wide role (`L2-009` IAM). It then sets `__Host-enclave_gate`: 32 random bytes, `Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/`, 12 hours. The store keeps only its SHA-256, with the user, the hostname and the expiry (`gate_sessions`). `/check` accepts a session only for its own hostname, and re-checks the person's role at most every 30 s, so removal takes effect within a minute (GATE-03). The console's session cookie and API tokens are never accepted by the gate, and a gate cookie never by the API.
  - **Password.** `password` renders Caddy's `basic_auth` with a bcrypt hash (cost 12) of the secret's password. The reconciler re-renders when the secret changes, through the domain's reference to it, within a minute (GATE-04).
  - **Pages.** The sign-in page, "This environment is private" and "Signed out" are server-rendered HTML with the design system's stylesheet embedded. They use no scripts, carry a strict `Content-Security-Policy` and `X-Frame-Options: DENY`, and check `Origin` on `POST`.
  - **Cloudflare Access.** `Protect` creates a self-hosted Access application for the hostname, with one allow policy for the listed emails and email domains, and keeps its ID in the domain's outputs. `Unprotect` deletes it.
- **Consequences**: the control plane is in the path of every gated request, with one indexed lookup in the store. The edge caches nothing, so revocation is quick. If the control plane is down, gated hosts answer `503` rather than letting anyone in: the gate fails closed.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler | ADR-003 – ADR-012: API-first, declarative reconciliation, SQLite, engine drivers, auth, single binary, provider placement, errors/idempotency/concurrency, observability, resilience |
| v2 | 2026-10-20 | Soupler | ADR-013: object store image (Chainguard MinIO pinned by digest) |
| v3 | 2026-10-20 | Soupler | ADR-014: design system package @enclavestack/ui; no inline styles, stylesheets or hand-written classes in apps (enforced by tests) |
| v4 | 2026-10-20 | Soupler | ADR-015 – ADR-019 for v0.3: Caddy edge proxy, revisions and probes, scheduler and runs, streamed logical backups, builds through the engine API (spike gate) |
| v5 | 2026-10-21 | Soupler | ADR-016 implemented: revisions numbered by a counter that only grows, interrupted deploys discarded and restarted, implicit tcp default, v0.2 adoption, measured internal-client limit |
| v6 | 2026-10-21 | Soupler | ADR-015 implemented: stable-alias upstreams with dial retries (no edge change on deploys), loopback default and --edge-address, server layout, loopback guard, certificate refresh, rebuild migrations |
| v7 | 2026-10-21 | Soupler | ADR-015: certificate refresh relative to lifetime |
| v8 | 2026-10-21 | Soupler | ADR-017 implemented for jobs: scheduler in the reconciler, adoption path (idempotent start, at-least-once), catch-up bound, results and events |
| v9 | 2026-10-21 | Soupler | ADR-018 implemented: as-implemented notes (SHA in the run, verify before restore, Redis via AOF, marker outputs, ms keys, retention per backup and never on a run being restored, forgotten artifacts, recoverable failed replace, ready → failed); ADR-014 gains Choice |
| v10 | 2026-10-21 | Soupler | ADR-019 implemented: context through the build session (Docker Desktop truncates chunked build bodies), Retry rebuilds after a failed build, in-use images kept |
| v11 | 2026-10-21 | Soupler | ADR-017 status: implemented for backups too |
| v12 | 2026-10-21 | Soupler | ADR-020 – ADR-024 for v0.4: networks and private DNS, nftables firewalls, replicas and HAProxy load balancers, DNS provider plug-ins, DNS-01 and wildcard certificates (planned) |
| v13 | 2026-10-21 | Soupler | ADR-020 – ADR-024 implemented, with as-built notes and measurements |
| v14 | 2026-10-21 | Soupler | ADR-022: zero-drop deploys behind a load balancer measured; fast connect timeout and retry on another replica |
| v15 | 2026-10-21 | Soupler | ADR-025 – ADR-028 for v0.5: QEMU hypervisor over QMP, a link container per VM (spiked), volumes, snapshots (planned) |
| v16 | 2026-10-21 | Soupler | ADR-025 – ADR-028 implemented, with as-built notes: QMP per operation, image cache, fixed link MAC (found live), chain names, filled volumes release snapshots |
| v17 | 2026-10-21 | Soupler | ADR-029 – ADR-034 for v0.6: NATS JetStream broker, database engines, OpenSearch, Mailpit email, nginx CDN, FFmpeg transcoders (planned; broker spiked) |
| v18 | 2026-10-22 | Soupler | ADR-029 – ADR-034 implemented, with as-built notes: hyphens kept in stream names, dead letters move at the next receive, host ports fixed at create (found on real Docker), MongoDB 7.0 default (kernel refusal), LOGIN relay auth without TLS, manual transcode refused at concurrency, shared run metrics |
| v19 | 2026-10-22 | Soupler | ADR-035 – ADR-041 for v0.7: projects as namespaces, roles and scoped tokens, built-in metrics, alerts, notification outbox, log shipper, external secret stores (planned) |
| v20 | 2026-10-22 | Soupler | ADR-040 as implemented: inclusive reads, skip counts, rotation gaps |
| v21 | 2026-10-22 | Soupler | ADR-042 – ADR-048 for v0.8: agent over mutual TLS, machine identity and join codes, placement and the engine router, networks across machines (mesh, stand-ins, address pools; spiked), offline machines, moving, catalogue and stacks (planned) |
| v22 | 2026-10-22 | Soupler | Token prefix `es_pat_` (was `hc_pat_` after the rename) |
| v15 | 2026-10-23 | Soupler | ADR-042 – ADR-044: the main machine has a generated ID and a computer-derived name; no `main` row or alias |
| v16 | 2026-10-24 | Soupler | ADR-049: per-app bucket access, one object store user per app or job (L2-011) |
| v17 | 2026-10-24 | Soupler | ADR-050: transcoder renditions, HLS ladders, tags and notifications (L2-012) |
| v18 | 2026-10-24 | Soupler | ADR-051 – ADR-053 for public access: tunnel connectors and per-tunnel edge listeners, edge machines joined from the main machine with relays over the mesh, the gate (planned; L2-014) |
