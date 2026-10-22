# L3-002 · Control-Plane API Contract

---
| Field | Value |
|---|---|
| Document ID | L3-002-console-api-contract-v1 |
| Layer | L3 — Architecture |
| Status | Draft |
| Version | v21 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-22 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs |
| License | Apache-2.0 |
---

## Purpose

The contract between the EnclaveStack control plane and every client — the console, the CLI, the OpenTofu provider and anyone's scripts (ADR-003). The Go types in `pkg/api` are the code form of this document; the console's `src/api/generated.ts` is generated from them; `api/openapi.yaml` is the machine-readable form for third parties. All four change in the same PR (ADR-002, ADR-010). `internal/httpapi/openapi_test.go` fails when the paths in `api/openapi.yaml` and the server's routes differ, or when its error-code list differs from `pkg/api`; `internal/contracts` fails when the generated console file is stale.

## 1 · Transport

| Topic | Rule |
|---|---|
| Base | `/api/v1`, JSON (UTF-8). A request with a body must send `Content-Type: application/json` (`415 UNSUPPORTED_MEDIA_TYPE`). Bodies ≤ 1 MiB (`413 PAYLOAD_TOO_LARGE`). Unknown JSON fields are rejected (`400 MALFORMED_REQUEST`). |
| Authentication | Browser: cookie `es_session` (set by `/session` and `/setup`). Code: `Authorization: Bearer es_pat_…`. Neither → `401 UNAUTHENTICATED`. |
| Authorisation | Sessions have full access. Tokens have scope `read` (GET only) or `write` (everything except managing tokens). Wrong scope → `403 FORBIDDEN`. Token management and secret reveal need a session or a `write` token. |
| CSRF | A cookie-authenticated `POST`/`PATCH`/`DELETE` must carry `X-Enclave-CSRF: 1` (`403 CSRF_REQUIRED`). No CORS headers are ever sent. |
| Request ID | Every response carries `X-Request-Id` (the client's, if it is 1–64 chars of `[A-Za-z0-9._-]`, else generated); errors repeat it in `requestId`. |
| Idempotency | `POST /resources` and `POST /tokens` accept `Idempotency-Key` (1–255 chars). A replay within 24 h returns the original status and body; the same key with a different body → `422 IDEMPOTENCY_KEY_REUSED`. |
| Concurrency | Resource responses carry `ETag: "<generation>"`. `PATCH` and `DELETE` honour `If-Match`; a mismatch → `412 PRECONDITION_FAILED`. |
| Rate limits | Sign-in: 5 failures per email + client IP per 15 min → `429 TOO_MANY_ATTEMPTS` with `Retry-After`. API: 100 requests/s per client, burst 200 → `429 RATE_LIMITED` with `Retry-After`. |
| Pagination | `limit` (1–200, default 100) and opaque `cursor`; responses `{ "items": [...], "nextCursor": "…" | null }`. |
| Time | RFC 3339 UTC strings. |

### Error envelope (`L0-003`)

```json
{ "error": { "code": "VALIDATION_FAILED", "message": "Check the highlighted fields.", "requestId": "req_01h…",
             "details": [ { "field": "spec.image", "code": "REQUIRED", "message": "Enter an image, for example nginx:1.27." } ] } }
```

| Code | HTTP | Meaning |
|---|---|---|
| `MALFORMED_REQUEST` | 400 | Not valid JSON, unknown field, wrong type |
| `UNAUTHENTICATED` | 401 | No or expired credentials |
| `INVALID_CREDENTIALS` | 401 | Email/password wrong (never says which) |
| `FORBIDDEN` | 403 | Credential lacks the scope |
| `CSRF_REQUIRED` | 403 | Cookie request without `X-Enclave-CSRF` |
| `INVALID_SETUP_CODE` | 403 | First-run setup code wrong |
| `NOT_FOUND` | 404 | No such resource, token or route |
| `METHOD_NOT_ALLOWED` | 405 | Route exists, method does not |
| `NAME_TAKEN` | 409 | A resource of this kind already has the name |
| `RESOURCE_IN_USE` | 409 | Delete refused; `details[]` lists dependants |
| `RESOURCE_BUSY` | 409 | Resource is being deleted |
| `ACTION_NOT_SUPPORTED` | 409 | Action not valid for this kind |
| `SETUP_COMPLETE` | 409 | Setup already done |
| `SETUP_REQUIRED` | 409 | Sign-in attempted before setup |
| `PRECONDITION_FAILED` | 412 | `If-Match` does not match |
| `PAYLOAD_TOO_LARGE` | 413 | Body over 1 MiB |
| `UNSUPPORTED_MEDIA_TYPE` | 415 | Body without JSON content type |
| `VALIDATION_FAILED` | 422 | Field rules failed; see `details` |
| `IMMUTABLE_FIELD` | 422 | Update tries to change a field that cannot change |
| `IDEMPOTENCY_KEY_REUSED` | 422 | Same key, different request |
| `TOO_MANY_ATTEMPTS` | 429 | Sign-in throttled |
| `RATE_LIMITED` | 429 | Request rate exceeded |
| `INTERNAL` | 500 | Our fault; details only in server logs |
| `ENGINE_UNAVAILABLE` | 503 | Only for calls that need the engine synchronously (logs) |

Field-level `details[].code`: `REQUIRED`, `INVALID_FORMAT`, `TOO_SHORT`, `TOO_LONG`, `OUT_OF_RANGE`, `NOT_ALLOWED`, `NOT_FOUND`, `IMMUTABLE`.

## 2 · Endpoints

| Method + path | Request | Success | Notes |
|---|---|---|---|
| `GET /api/v1/setup` | — | `200 {"required": bool}` | Public |
| `POST /api/v1/setup` | `{setupCode, machineName, email, password}` | `201 Session` + cookie | Public; only while required |
| `POST /api/v1/session` | `{email, password}` | `200 Session` + cookie | Public; throttled |
| `GET /api/v1/session` | — | `200 Session` | Works for cookie and token |
| `DELETE /api/v1/session` | — | `204` | Clears cookie |
| `GET /api/v1/tokens` | — | `200 {items: Token[]}` | Session or write token |
| `POST /api/v1/tokens` | `{name, scope: "read"|"write", expiresInDays?: 1–365}` | `201 {token: Token, secret: "es_pat_…"}` | Secret shown once |
| `DELETE /api/v1/tokens/{id}` | — | `204` | Revoke |
| `GET /api/v1/machine` | — | `200 Machine` | |
| `GET /api/v1/resources` | query `kind`, `name`, `limit`, `cursor` | `200 {items: Resource[], nextCursor}` | Ordered by creation |
| `POST /api/v1/resources` | `{kind, name, spec}` | `202 Resource` | `Idempotency-Key` |
| `GET /api/v1/resources/{id}` | — | `200 Resource` + `ETag` | |
| `PATCH /api/v1/resources/{id}` | `{spec}` — fields present replace; maps replace whole | `202 Resource` | `If-Match`; no-op patch → `200` unchanged |
| `DELETE /api/v1/resources/{id}` | — | `202 Resource` (phase `deleting`) | `If-Match`; then `404` when gone |
| `POST /api/v1/resources/{id}/actions/{action}` | — | `202 Resource` | `restart` (app, database, cache), `redeploy` (app), `reconcile` (any) |
| `GET /api/v1/resources/{id}/events` | `limit` | `200 {items: Event[]}` | Newest first |
| `GET /api/v1/resources/{id}/logs` | `tail` (1–1000, default 200) | `200 {items: LogLine[]}` | Oldest first; `503 ENGINE_UNAVAILABLE` when the engine is down |
| `GET /api/v1/resources/{id}/value` | — | `200 {"value": "…"}` | Secrets only; session or write token; recorded as `secret_revealed` |
| `GET /healthz` · `GET /readyz` · `GET /metrics` | — | `200` | Outside `/api`, unauthenticated |
| `GET /*` | — | Console SPA | Unknown non-API paths serve `index.html` |

## 3 · Shapes

```ts
type Kind  = 'app' | 'database' | 'cache' | 'bucket' | 'secret';
type Phase = 'pending' | 'provisioning' | 'ready' | 'updating' | 'deleting' | 'failed';
type StepState = 'pending' | 'active' | 'done' | 'failed';

interface Session  { user: { id: string; email: string }; machineName: string; authMethod: 'session' | 'token'; scope: 'read' | 'write' | 'full' }
interface Token    { id: string; name: string; prefix: string; scope: 'read' | 'write'; createdAt: string; lastUsedAt: string | null; expiresAt: string | null }
interface Machine  { name: string; hostname: string; version: string; os: string; arch: string; cpus: number;
                     memoryTotalBytes: number; memoryReservedBytes: number;
                     engine: { driver: 'docker' | 'fake'; available: boolean; version: string; message: string } }
interface Resource {
  id: string;                 // "<prefix>_<16 base32 chars>"; prefixes app_, db_, cache_, bkt_, sec_
  kind: Kind; name: string; generation: number; ownerId: string | null;   // ownerId: managed by another resource
  spec: AppSpec | DatabaseSpec | CacheSpec | BucketSpec | SecretSpec;
  status: { phase: Phase; message: string; observedGeneration: number; attempts: number;
            steps: { name: string; state: StepState }[]; lastTransitionAt: string };
  outputs: Record<string, string | number | boolean>;
  createdAt: string; updatedAt: string;
}
interface AppSpec      { image: string; port?: number; command?: string[]; env?: Record<string,string>; secretEnv?: Record<string,string>; publish?: boolean; memoryMiB?: number;
                         healthCheck?: HealthCheck; strategy?: 'rolling' | 'recreate' }   // HealthCheck: §6.2 (shipped)
interface DatabaseSpec { engine: 'postgres'; version: '16' | '17' }
interface CacheSpec    { engine: 'redis'; version: '7'; maxMemoryMiB?: number }
interface BucketSpec   { versioning?: boolean }
interface SecretSpec   { value?: string }   // write-only: responses never include it
interface Event        { id: number; type: string; message: string; createdAt: string }
interface LogLine      { time: string; stream: 'stdout' | 'stderr'; message: string }
```

Outputs by kind:

| Kind | Outputs |
|---|---|
| `database` | `host`, `port`, `internalHost`, `internalPort` (5432), `database`, `username`, `passwordSecret` |
| `cache` | `host`, `port`, `internalHost`, `internalPort` (6379) |
| `bucket` | `endpoint`, `internalEndpoint`, `bucket`, `credentialsSecret` |
| `app` | `internalHost`, `internalPort`; `host`, `port` when published; `servingRevision`, `latestRevision`, `lastDeploy` (`succeeded`, `failed`, `in_progress`) |
| `secret` | `version`, `valueUpdatedAt` |

Managed resources: a database creates secret `<name>-password` (`ownerId` = the database); the first bucket creates secret `objectstore-credentials` (system-owned, value `{"accessKey","secretKey"}`). Managed secrets cannot be deleted directly, and are deleted with their owner.

## 4 · Validation rules

Defined once in `pkg/api/validation.go` and exported to the console's generated file.

| Field | Rule |
|---|---|
| `name` (all kinds) | `^[a-z][a-z0-9-]{1,38}[a-z0-9]$` (3–40 chars); unique per kind; immutable |
| `app.spec.image` | Required; ≤ 255 chars; Docker reference `[registry/]repo[:tag][@digest]` |
| `app.spec.port` | 1–65535 |
| `app.spec.command` | ≤ 32 items, each ≤ 1024 chars |
| `app.spec.env` keys | `^[A-Za-z_][A-Za-z0-9_]{0,127}$`; ≤ 100 entries; values ≤ 4096 chars |
| `app.spec.secretEnv` | Keys as env; values are names of existing secrets; a key may not appear in both maps |
| `app.spec.memoryMiB` | 64–16384, default 256 |
| `app.spec.publish` | `true` requires `port` |
| `app.spec.healthCheck`, `app.spec.strategy` | As §6.5 (shipped in v0.3 phase 1) |
| `database.spec.version` | `16` (default) or `17`; immutable; `engine` = `postgres`, immutable |
| `cache.spec.maxMemoryMiB` | 64–4096, default 256; `engine` = `redis`, `version` = `7`, immutable |
| `bucket.spec.versioning` | boolean, default false |
| `secret.spec.value` | Required on create; 1–65536 bytes |
| Token `name` | 1–64 chars; `expiresInDays` 1–365 |
| Setup | `machineName` 1–64 chars; `email` contains `@`, ≤ 254 chars; `password` 12–256 chars |

## 5 · Status transitions

```
create ─▶ pending ─▶ provisioning ─▶ ready ◀────────────┐
                         │  ▲            │ PATCH        │
                 transient error         ▼              │
                 (backoff, ≤8)       updating ──────────┘
                         │               │ error
                         ▼               ▼
                       failed ◀──────────┘     (reconcile action or new spec → provisioning)
               ready ──▶ failed only for a backup whose last 3 runs failed (L2-005 BAK-09)
any phase ── DELETE ──▶ deleting ──▶ (gone: 404)
```

## 6 · v0.3 additions (planned)

This section specifies the v0.3 contract (`L2-005`; ADR-015 – ADR-019). **Shipped so far (2026-10-21):**
- Phase 1: `HealthCheck`, `AppSpec.healthCheck`, `AppSpec.strategy`, and the app outputs `servingRevision`, `latestRevision` and `lastDeploy`.
- Phase 2: the `domain` kind, `DomainSpec` and its outputs; `PATCH /machine` and `GET /machine/ca`; the `Machine` fields `publicAddress`, `acmeEmail` and `edge`; `HOSTNAME_TAKEN`; and the domain validation rules. Values sent to `PATCH /machine` are trimmed before they are validated.
- Phase 3: the `job` kind and `JobSpec`; `Run`/`RunDetail`/`RunList`; the `run` action (returns the `Run`, `409 RUN_IN_PROGRESS`); `GET …/runs` (read access) and `GET …/runs/{runId}` (session or write token, because output may hold secrets); job outputs `nextRunAt`, `lastRunStatus`, `lastRunAt`; the field codes `INVALID_SCHEDULE` and `EXACTLY_ONE`. `GET …/logs` on a job answers `409 ACTION_NOT_SUPPORTED`; output lives on runs.
- Phase 4: the `backup` kind (`bak_`) and `BackupSpec`; `RestoreRequest` and the `restore` action (returns the resource being restored into); `Run.artifact`; backup outputs `lastSuccessAt`, and `restoredFrom` on a restored database or cache; `RUN_NOT_RESTORABLE`; the `ready → failed` transition for a backup whose last three runs failed. `GET …/logs` on a backup answers `409 ACTION_NOT_SUPPORTED`. Actions other than `restore` now reject a request body with `400 MALFORMED_REQUEST`, as §6.3 always said; before this they ignored it.
- Phase 5: `GitSource` and `AppSpec.source` (exactly one of `image` / `source`; `EXACTLY_ONE` on `spec.image` when both are set). Source app outputs `builtCommit`, `builtImage`, `builtInputs` (bookkeeping) and `lastBuild`. `GET …/logs?build=true` returns the latest build's log; `build` takes `true` or `false`, anything else is `422`, and a kind other than an app answers `409 ACTION_NOT_SUPPORTED`. `redeploy` resolves the ref again. A source app references its credentials secret.

All of §6 is now served. When each part ships, its rows move into §1 – §5 in the same PR as:
- `pkg/api`;
- `api/openapi.yaml`;
- the generated console file;
- both console adapters;
- the handlers (ADR-010).

### 6.1 Kinds and identifiers

`Kind` gains `domain` (`dom_`), `job` (`job_`) and `backup` (`bak_`). Runs are identified as `run_<16 base32 chars>`.

### 6.2 Shapes

```ts
type Kind = 'app' | 'database' | 'cache' | 'bucket' | 'secret' | 'domain' | 'job' | 'backup';

interface HealthCheck {
  type: 'http' | 'tcp' | 'command' | 'none';
  path?: string;               // http only; default "/"
  port?: number;               // http/tcp; default the app's port
  command?: string[];          // command only
  intervalSeconds?: number;    // default 5
  timeoutSeconds?: number;     // default 3
  retries?: number;            // default 12
  startPeriodSeconds?: number; // default 0
}
interface GitSource {
  git: string;                 // https://… or ssh://…
  ref: string;                 // branch, tag or commit
  dockerfile?: string;         // default "Dockerfile", relative to context
  context?: string;            // default "."
  credentialsSecret?: string;  // secret: {"token"} for https, {"privateKey","knownHosts"} for ssh
  buildArgs?: Record<string, string>;
}
interface AppSpec {            // v0.2 fields unchanged; exactly one of image | source
  image?: string; source?: GitSource;
  port?: number; command?: string[]; env?: Record<string,string>; secretEnv?: Record<string,string>;
  publish?: boolean; memoryMiB?: number;
  healthCheck?: HealthCheck; strategy?: 'rolling' | 'recreate';
}
interface DomainSpec {
  hostname: string;            // immutable
  app: string;                 // app name
  port?: number;               // default the app's port
  tls?: 'auto' | 'internal' | 'off';  // default internal for *.localhost, else auto
  redirectHttp?: boolean;      // default true (ignored when tls is off)
}
interface JobSpec {            // exactly one of app | image
  schedule: string; timeZone?: string;
  app?: string; image?: string; command?: string[];
  env?: Record<string,string>; secretEnv?: Record<string,string>;
  timeoutSeconds?: number;     // default 3600
  overlap?: 'forbid' | 'allow';// default forbid
  memoryMiB?: number;          // default 256
}
interface BackupSpec {
  source: { database: string } | { cache: string };             // immutable
  destination: { bucket: string }
             | { s3: { endpoint: string; region?: string; bucket: string; credentialsSecret: string } };
  prefix?: string;             // default "enclave-backups"
  schedule?: string;           // default "0 3 * * *"
  timeZone?: string;
  keep?: number;               // default 7
  keepDays?: number;           // optional age limit
  timeoutSeconds?: number;     // default 7200
}
interface Run {
  id: string; trigger: 'schedule' | 'manual' | 'catch_up' | 'pre_restore';
  status: 'running' | 'succeeded' | 'failed' | 'timed_out' | 'skipped';
  scheduledFor: string | null; startedAt: string; finishedAt: string | null;
  exitCode: number | null; message: string;
  artifact?: { key: string; sizeBytes: number; sha256: string; destination: string };   // backups
}
interface RunDetail extends Run { outputTail: string }          // jobs; at most 64 KiB
interface Machine {            // v0.2 fields plus:
  publicAddress: string | null; acmeEmail: string | null;
  edge: { available: boolean; message: string };
}
```

New outputs:

| Kind | Outputs |
|---|---|
| `app` | `servingRevision` (generation number), `lastDeploy` (`succeeded`, `failed`, `in_progress`); source apps also `builtCommit` (the serving revision's commit), `builtImage` (its `enclave.local/…` tag) and `lastBuild` (`succeeded`, `failed`, `skipped`) |
| `domain` | `url`, `certificateState` (`pending`, `issued`, `failed`, `none`), `certificateExpiresAt`, `certificateIssuer`, `publicAddress` |
| `job`, `backup` | `nextRunAt`, `lastRunStatus`, `lastRunAt`; backups also `lastSuccessAt` |

### 6.3 Endpoints

| Method + path | Request | Success | Notes |
|---|---|---|---|
| `POST /api/v1/resources/{id}/actions/run` | — | `202 Run` | Jobs and backups; under `forbid` while running → `409 RUN_IN_PROGRESS` |
| `POST /api/v1/resources/{id}/actions/restore` | `{runId, mode: "new", name}` or `{runId, mode: "replace", confirmName}` (JSON body required: `415` without one) | `202 Resource` (the new resource, or the source) | Backups only; `confirmName` must equal the source's name; the run must be `succeeded` and still hold its backup. A new resource gets the source's spec; the restore runs once it is up. A replace sets the source to `updating`, takes a `pre_restore` safety run first and stops if that fails. The object is checked against the run's SHA-256 before anything changes; an object changed or deleted outside EnclaveStack fails the target with the reason |
| `GET /api/v1/resources/{id}/runs` | `limit`, `cursor` | `200 {items: Run[], nextCursor}` | Newest first |
| `GET /api/v1/resources/{id}/runs/{runId}` | — | `200 RunDetail` | |
| `GET /api/v1/resources/{id}/logs?build=true` | `tail` | `200 {items: LogLine[]}` | Source apps: the latest build's log |
| `PATCH /api/v1/machine` | `{publicAddress?, acmeEmail?}` (`null` clears) | `200 Machine` | Session or write token |
| `GET /api/v1/machine/ca` | — | `200` `application/x-pem-file` | The internal CA's root certificate; `404 NOT_FOUND` before the first `internal` domain |

`POST …/actions/{action}` accepts an optional JSON body. Actions that take none reject a body with `400 MALFORMED_REQUEST`. The action list grows to `run` (job, backup) and `restore` (backup).

### 6.4 Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `HOSTNAME_TAKEN` | 409 | Another domain already has this hostname |
| `RUN_IN_PROGRESS` | 409 | A run is in progress and overlap is forbidden |
| `RUN_NOT_RESTORABLE` | 409 | The run did not succeed, retention removed its backup (the run's message says so and it has no `artifact`), or the source no longer exists for a replace |

Field-level `details[].code` gains `INVALID_SCHEDULE` and `EXACTLY_ONE` (exactly one of two fields must be set).

### 6.5 Validation rules

| Field | Rule |
|---|---|
| `domain.spec.hostname` | Lowercase; ≤ 253 chars; labels `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`, at least two labels; not an IP address; no wildcard; unique across domains; immutable |
| `domain.spec.app` | Name of an existing app; the app must have a port, unless `port` is given |
| `domain.spec.port` | 1–65535 |
| `app.spec.image` / `app.spec.source` | Exactly one |
| `app.spec.healthCheck` | `path` starts with `/`, ≤ 255 chars; `command` ≤ 32 items; `intervalSeconds` 1–300; `timeoutSeconds` 1–60 and < `intervalSeconds`; `retries` 1–100; `startPeriodSeconds` 0–600; `startPeriodSeconds + retries × intervalSeconds` ≤ 900 |
| `app.spec.strategy` | `rolling` or `recreate`; `rolling` with `publish: true` is accepted and run as `recreate` (DEP-04) |
| `app.spec.source.git` | ≤ 512 chars, scheme `https` or `ssh` (`http` only for `localhost` / `127.0.0.1`); `ref` 1–255 chars, no whitespace; `dockerfile` and `context` relative, no `..`; `credentialsSecret` an existing secret; `buildArgs` keys as env keys, at most 50; exactly one of `image` / `source` |
| `job.spec.schedule`, `backup.spec.schedule` | 5-field cron or `@hourly` / `@daily` / `@weekly` / `@monthly`; at most one run per minute |
| `*.spec.timeZone` | IANA name |
| `job.spec.app` / `job.spec.image` | Exactly one |
| `job.spec.timeoutSeconds` | 1–86400 |
| `backup.spec.source` | Exactly one of `database` / `cache`, naming an existing resource; immutable |
| `backup.spec.destination` | Exactly one of `bucket` (an existing bucket) / `s3`; `s3.endpoint` an `https://` URL (or `http://` for `localhost`); `credentialsSecret` an existing secret |
| `backup.spec.prefix` | `^[a-z0-9][a-z0-9/_-]{0,99}$` |
| `backup.spec.keep` | 1–365; `keepDays` 0 (no age limit, the default) or 1–3650. Retention counts and deletes only objects written by this backup's own runs, so two backups of one source into the same bucket and prefix never delete each other's; the newest is never deleted |
| `backup.spec.timeoutSeconds` | 60–86400 |
| `machine.publicAddress` | An IPv4/IPv6 address or a hostname, ≤ 253 chars |
| `machine.acmeEmail` | Contains `@`, ≤ 254 chars |

### 6.6 Dependencies (CP-12)

`resource_refs` gains these references, each refusing deletion of its target with `RESOURCE_IN_USE`:
- domain → app;
- job → app, and its secrets;
- backup → its source, bucket and credentials secret;
- source app → its credentials secret.

## 7 · v0.4 additions (planned)

This section specifies the v0.4 contract (`L2-006`; ADR-020 – ADR-024). **All of it is served (2026-10-21).** As each phase ships, its rows move into §1 – §5 in the same PR as `pkg/api`, `api/openapi.yaml`, the generated console file, both console adapters and the handlers (ADR-010). No endpoint is added: every v0.4 kind uses `/resources`.

### 7.1 Kinds and identifiers

`Kind` gains `network` (`net_`), `firewall` (`fw_`), `loadbalancer` (`lb_`), `zone` (`zone_`) and `record` (`rec_`).

### 7.2 Shapes

```ts
type Kind = 'app' | 'database' | 'cache' | 'bucket' | 'secret' | 'domain' | 'job' | 'backup'
          | 'network' | 'firewall' | 'loadbalancer' | 'zone' | 'record';

interface Subnet      { name: string; cidr: string }
interface NetworkSpec {                      // every field immutable
  cidr: string;                              // e.g. "10.40.0.0/16"
  subnets?: Subnet[];                        // default [{ name: "default", cidr }]
  internet?: boolean;                        // default true
}
interface Placement   { network?: string; subnet?: string }   // on AppSpec, DatabaseSpec, CacheSpec, JobSpec (image jobs)
interface AppSpec     { /* v0.3 fields */ network?: string; subnet?: string; replicas?: number }   // replicas 1–10, default 1

interface FirewallRule {
  from?: string[];                           // ingress: "any" | CIDR | "network/<name>" | "<kind>/<name>"
  to?: string[];                             // egress: same forms
  protocol?: 'tcp' | 'udp' | 'icmp' | 'all'; // default tcp
  ports?: string[];                          // "5432" | "8000-8100"; none = all (not with icmp)
}
interface FirewallSpec {
  targets: string[];                         // "app/api" | "database/orders" | "cache/…" | "loadbalancer/…"
  ingress?: FirewallRule[];
  egressDefault?: 'allow' | 'deny';          // default allow
  egress?: FirewallRule[];
}

interface LoadBalancerTarget { app: string; port?: number; weight?: number }   // port: default the app's; weight 1–256, default 1
interface LoadBalancerCheck  { type: 'tcp' | 'http'; path?: string; intervalSeconds?: number; timeoutSeconds?: number;
                               healthyThreshold?: number; unhealthyThreshold?: number }   // defaults: 2, 1, 2, 3
interface LoadBalancerSpec {
  port: number;
  protocol?: 'http' | 'tcp';                 // default http
  targets: LoadBalancerTarget[];             // 1–8 apps, all in one network
  algorithm?: 'round_robin' | 'least_conn' | 'source';   // default round_robin
  healthCheck?: LoadBalancerCheck;           // default { type: "tcp" }
  publish?: boolean;
}

interface ZoneSpec   { provider: 'cloudflare' | 'route53'; zone: string; credentialsSecret: string }   // provider, zone immutable
interface RecordSpec {                       // zone, name, type immutable
  zone: string;                              // a zone resource's name
  name: string;                              // relative: "api", "@" (apex), "*.dev"
  type: 'A' | 'AAAA' | 'CNAME' | 'TXT';
  values?: string[];                         // exactly one of values | machine
  machine?: boolean;                         // A/AAAA to the machine's publicAddress
  ttl?: number;                              // 60–86400, default 300
}
interface DomainSpec { /* v0.3 fields */ zone?: string }   // a zone resource's name: DNS-01; allows "*.example.com"
```

New outputs:

| Kind | Outputs |
|---|---|
| `network` | `dockerNetwork`, `gateway` |
| `app`, `database`, `cache` | `privateIp`, `privateDns` (`<name>.<kind>.internal`); apps also `runningReplicas` |
| `firewall` | `appliedContainers`, `appliedAt` |
| `loadbalancer` | `internalHost`, `internalPort`, `privateDns`; `host`, `port` when published; `targets`, `healthyTargets` (replica counts) |
| `zone` | `zoneId`, `nameServers` (comma-separated), `provider` |
| `record` | `fqdn`, `values` (comma-separated, as at the provider), `owned` |

### 7.3 Validation rules

| Field | Rule |
|---|---|
| `network.spec.cidr` | Canonical IPv4 prefix (host bits zero) inside 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 or 100.64.0.0/10; /16 – /28; doesn't overlap another network or the default network; immutable |
| `network.spec.subnets` | 1–16; `name` as a resource name, unique; `cidr` canonical, /16 – /29, inside the network, not overlapping another subnet; immutable |
| `*.spec.network` / `*.spec.subnet` | An existing network; a subnet of it (default its first); `subnet` needs `network`. On a job only with `image` (an app job runs in the app's network). `publish: true` is refused in a network without internet |
| `app.spec.replicas` | 1–10; more than 1 is refused with `publish: true` |
| `firewall.spec.targets` | 1–20 distinct `<kind>/<name>` of existing apps, databases, caches or load balancers |
| `firewall.spec.ingress` / `egress` | ≤ 50 rules each; ingress rules have `from` (1–20 entries) and no `to`, egress rules the reverse; `protocol` as listed; `ports` ≤ 20 entries, each 1–65535 or `a-b` with a ≤ b, none with `icmp`; named networks and resources must exist; `egress` needs `egressDefault: deny` |
| `loadbalancer.spec.port` | 1–65535 |
| `loadbalancer.spec.targets` | 1–8 distinct apps that exist, all in the same network; each has a port or `port` is set; `weight` 1–256 |
| `loadbalancer.spec.healthCheck` | `path` (http only) starts with `/`; `intervalSeconds` 1–60; `timeoutSeconds` 1–30 and ≤ interval; thresholds 1–10 |
| `zone.spec.zone` | A hostname as `domain.spec.hostname` (no wildcard); unique per provider; immutable with `provider` |
| `zone.spec.credentialsSecret` | An existing secret |
| `record.spec.name` | `@`, or DNS labels relative to the zone, optionally starting with `*.`; the full name ≤ 253 chars; unique per zone and type; immutable |
| `record.spec.values` | 1–20; `A` IPv4, `AAAA` IPv6, `CNAME` exactly one hostname (and the name may not be `@`), `TXT` ≤ 255 chars each; exactly one of `values` / `machine`; `machine` only for `A` / `AAAA` |
| `record.spec.ttl` | 60–86400 |
| `domain.spec.zone` | An existing zone; the hostname equals the zone or ends with `.<zone>`; needs `tls: auto` |
| `domain.spec.hostname` | v0.3 rules, plus one leading `*.` label when `zone` is set |

Field-level `details[].code` gains `OVERLAPS` (a CIDR overlaps another one).

### 7.4 Dependencies (CP-12)

`resource_refs` gains these references, each refusing deletion of its target with `RESOURCE_IN_USE`:
- app, database, cache, job → its network;
- firewall → each target, and each network or resource its rules name;
- load balancer → each target app;
- zone → its credentials secret;
- record → its zone; domain → its zone.

## 8 · v0.5 additions (served 2026-10-21)

This section specifies the v0.5 contract (`L2-007`; ADR-025 – ADR-028). **All of it is served (2026-10-21)**, in `pkg/api`, `api/openapi.yaml`, the generated console file, both console adapters and the handlers. As each phase ships, its rows move into §1 – §5 in the same PR as `pkg/api`, `api/openapi.yaml`, the generated console file, both console adapters and the handlers (ADR-010). No resource endpoint is added: every v0.5 kind uses `/resources`, and VM power uses `actions`.

### 8.1 Kinds and identifiers

`Kind` gains `vm` (`vm_`), `volume` (`vol_`) and `snapshot` (`snap_`).

### 8.2 Shapes

```ts
type Kind = 'app' | 'database' | 'cache' | 'bucket' | 'secret' | 'domain' | 'job' | 'backup'
          | 'network' | 'firewall' | 'loadbalancer' | 'zone' | 'record'
          | 'vm' | 'volume' | 'snapshot';

interface VolumeMount { volume: string; path: string; readOnly?: boolean }   // a filesystem volume's name
interface AppSpec { /* v0.4 fields */ volumes?: VolumeMount[] }               // also on JobSpec

interface VMSpec {
  image: string;                // "ubuntu-24.04" | "debian-12" | "https://…" | "snapshot/<name>"; immutable
  imageSha256?: string;         // URL images only; immutable
  sshKeys?: string[];           // OpenSSH public keys; exactly one of sshKeys | sshKeySecret; immutable
  sshKeySecret?: string;        // a secret holding one key per line; immutable
  userData?: string;            // first-boot script; immutable
  cpus?: number;                // default 1
  memoryMiB?: number;           // default 1024
  diskGiB?: number;             // root disk, default 10; grows only
  network?: string;             // as AppSpec (ADR-020)
  subnet?: string;
  disks?: string[];             // disk volumes' names, in attach order
  publish?: number[];           // VM ports published on the same host port (not 22)
  power?: 'running' | 'stopped';  // default running
}

interface VolumeSpec {
  type: 'disk' | 'filesystem';  // immutable
  sizeGiB?: number;             // disks only: default 10; grows only
  snapshot?: string;            // a snapshot's name to start from; immutable
}

interface SnapshotSpec { source: string }   // "volume/<name>" | "vm/<name>"; immutable
```

Actions: `start`, `stop` and `restart` on a VM set `power` (`start`, `stop`) or restart it, and return `202 Resource`. `GET /resources/{id}/logs?tail=N` on a VM returns its boot console.

New outputs:

| Kind | Outputs |
|---|---|
| `vm` | `power` (`running`, `stopped`, `starting`, `stopping`), `privateIp`, `privateDns` (`<name>.vm.internal`), `sshAddress` (`127.0.0.1:<port>`), `sshUser` (`enclave`), `imageSha256`, `host` when it publishes ports |
| `volume` | `attachedTo` (comma-separated `kind/name`), `sizeGiB` (disks), `usedBytes` (filesystems) |
| `snapshot` | `type` (`disk` or `filesystem`), `sizeBytes`, `takenAt`, `inUse` (`true` when the source was in use, so a filesystem copy may hold a file part-way); disk snapshots also `sizeGiB` |

As built, a VM also reports `linkPort` (the loopback port QEMU streams to; bookkeeping the console hides), and a volume started from a snapshot reports `filledFrom` once it holds the copy.

`GET /machine` gains `vms: { available: boolean, message: string, qemuVersion?: string, accelerator?: 'hvf' | 'kvm' }`, `reservedCpus`, `images: { name, arch, sizeBytes, usedBy }[]` and `snapshotBytes`.

### 8.3 Error codes

| Code | HTTP | Meaning |
|---|---|---|
| `VOLUME_ATTACHED` | 409 | A disk volume is already attached to another VM (`details[0].field` names the volume) |

Field-level `details[].code` gains `GROW_ONLY`: a size may only grow.

### 8.4 Validation rules

| Field | Rule |
|---|---|
| `vm.spec.image` | A catalog name for the machine's architecture, an `https://` URL (≤ 2048 chars), or `snapshot/<name>` of an existing `disk` snapshot; immutable |
| `vm.spec.imageSha256` | 64 lowercase hex characters; only with a URL image; immutable |
| `vm.spec.sshKeys` | 1 – 10 entries, each one line in OpenSSH format (`ssh-ed25519`, `ssh-rsa` ≥ 2048 bits, `ecdsa-sha2-nistp256/384/521`, `sk-…`), ≤ 16 KiB; exactly one of `sshKeys` / `sshKeySecret`; immutable |
| `vm.spec.sshKeySecret` | An existing secret; its value is checked like `sshKeys` when the VM is first created; immutable |
| `vm.spec.userData` | ≤ 64 KiB UTF-8, starting with `#cloud-config` or `#!`; immutable |
| `vm.spec.cpus` | 1 – 16 and ≤ the machine's CPUs |
| `vm.spec.memoryMiB` | 512 – 65536, a multiple of 128, and ≤ the machine's memory minus 2048 MiB |
| `vm.spec.diskGiB` | 5 – 2048; at least the image's own size; an update may not lower it (`GROW_ONLY`) |
| `vm.spec.network` / `subnet` | As apps (§7.3); `publish` is refused in a network without internet |
| `vm.spec.disks` | ≤ 8 distinct existing `disk` volumes, none attached to another VM (`VOLUME_ATTACHED`); their names' first 20 characters are distinct |
| `vm.spec.publish` | ≤ 10 distinct ports 1 – 65535, not 22 |
| `vm.spec.power` | `running` or `stopped` |
| `volume.spec.type` | `disk` or `filesystem`; immutable |
| `volume.spec.sizeGiB` | Disks: 1 – 2048, default 10, may not lower (`GROW_ONLY`), at least the snapshot's size. Refused on filesystems |
| `volume.spec.snapshot` | An existing `ready` snapshot of the same type (a VM snapshot counts as `disk`); immutable |
| `app.spec.volumes` / `job.spec.volumes` | ≤ 10 entries; each an existing `filesystem` volume; `path` absolute, ≤ 255 chars, normalised (no `..`), not `/`, distinct within the resource |
| `snapshot.spec.source` | `volume/<name>` or `vm/<name>` of an existing resource; immutable |

### 8.5 Dependencies (CP-12)

`resource_refs` gains these references, each refusing deletion of its target with `RESOURCE_IN_USE`:
- vm → its network, each disk volume, its `sshKeySecret`;
- app, job → each filesystem volume they mount;
- firewall, load balancer, domain → a VM they name (`vm/<name>`).

Firewalls accept `vm/<name>` wherever §7.3 allows a resource: as a target and as a rule's source or destination.

A snapshot doesn't reference its source, and nothing references a snapshot after it has been copied, so both can be deleted at any time (`L2-007` SNP-02, SNP-06). A VM being created from a snapshot, or a volume being filled from one, holds a reference until its copy is done.

## 9 · v0.6 additions (served 2026-10-22)

This section specifies the v0.6 contract (`L2-008`; ADR-029 – ADR-034). As each phase ships, this section says so, in the same PR as `pkg/api`, `api/openapi.yaml`, the generated console file, both console adapters and the handlers (ADR-010). Every v0.6 kind uses `/resources`. Queues, topics and email services also have data endpoints (§9.3), because sending and receiving messages is what they are for.

### 9.1 Kinds and identifiers

`Kind` gains `queue` (`q_`), `topic` (`tpc_`), `search` (`srch_`), `transcoder` (`tc_`), `cdn` (`cdn_`) and `email` (`mail_`).

### 9.2 Shapes

```ts
type Kind = /* v0.5 kinds */ | 'queue' | 'topic' | 'search' | 'transcoder' | 'cdn' | 'email';

interface DatabaseSpec {
  engine?: 'postgres' | 'mysql' | 'mongodb' | 'clickhouse';   // default postgres; immutable
  version?: string;   // postgres 16 (default) | 17; mysql 8.4; mongodb 7.0 (default) | 8.0; clickhouse 26.3 (default) | 25.8; immutable
  network?: string; subnet?: string;
}

interface QueueSpec {
  visibilityTimeoutSeconds?: number;  // default 30
  retentionHours?: number;            // default 96
  maxMessageBytes?: number;           // default 262144
  maxReceives?: number;               // default 0: no limit
  deadLetterQueue?: string;           // another queue's name; needs maxReceives ≥ 1
}

interface Subscription { queue?: string; url?: string; secret?: string }  // exactly one of queue | url; secret only with url
interface TopicSpec {
  retentionHours?: number;            // default 24
  subscriptions?: Subscription[];
}

interface SearchSpec {
  version?: '2' | '3';                // default 3; immutable
  memoryMiB?: number;                 // default 2048; half is the Java heap
  network?: string; subnet?: string;
}

type Preset = 'mp4-480p' | 'mp4-720p' | 'mp4-1080p' | 'webm-720p' | 'hls-720p' | 'mp3' | 'thumbnail';
interface BucketFolder { bucket: string; prefix?: string }   // prefix "" or "a/b/" (normalised: no leading /, a trailing /)
interface TranscoderSpec {
  input: BucketFolder;
  output: BucketFolder;               // default prefix "transcoded/"
  presets: Preset[];
  watch?: boolean;                    // default true
  timeoutSeconds?: number;            // default 3600
  concurrency?: number;               // default 1
  memoryMiB?: number;                 // default 1024
}

interface CDNSpec {
  hostname: string;                   // immutable
  origin: string;                     // "app/<name>" | "bucket/<name>"
  originPort?: number;                // apps only; default the app's port
  defaultTtlSeconds?: number;         // default 3600
  maxSizeMiB?: number;                // default 1024
  tls?: 'auto' | 'internal' | 'off';  // as DomainSpec
  redirectHttp?: boolean;             // as DomainSpec
  zone?: string;                      // as DomainSpec (DNS-01)
}

interface EmailRelay {
  host: string;
  port?: number;                      // default 587
  tls?: 'starttls' | 'tls' | 'none';  // default starttls
  credentialsSecret: string;          // a secret holding {"username": "…", "password": "…"}
}
interface EmailSpec {
  mode?: 'capture' | 'relay';         // default capture
  relay?: EmailRelay;                 // required in relay mode, refused in capture mode
  domain?: string;                    // the API refuses other sender domains
  keepMessages?: number;              // default 500
  network?: string; subnet?: string;
}
```

Actions:
- `purge` on a queue deletes every message; on a CDN it empties the cache. It returns `202 Resource`; the reconciler carries it out (`requestedAction: purge`), and an event records it.
- `run` on a transcoder takes a body `{ "key": "<object key in the input bucket>" }` and returns `202 Run`. On jobs and backups `run` still takes no body.

`Run` gains `key?: string` (the transcoder's input object) and `outputs?: string[]` (the keys it wrote). `Run.trigger` gains `watch` (a transcoder saw a new object).

New outputs:

| Kind | Outputs |
|---|---|
| `database` | `engine`; ClickHouse also `nativePort` and `internalNativePort` (`port` / `internalPort` are its HTTP interface) |
| `queue` | `url` (`nats://127.0.0.1:<port>`), `internalUrl` (`nats://es-system-broker:4222`), `subject` (`es.q.<name>`), `stream`, `consumer`, `username`, `passwordSecret`, `messages`, `inFlight` (refreshed by each drift pass) |
| `topic` | `url`, `internalUrl`, `subject` (`es.t.<name>`), `stream`, `username`, `passwordSecret`, `subscriptions` (count) |
| `search` | `internalHost`, `internalPort` (9200), `internalUrl`, `host`, `port`, `url`, `username` (`admin`), `passwordSecret`, `version` (exact, e.g. `3.3.2`) |
| `transcoder` | `watching` (boolean), `lastRunAt`, `lastRunStatus` |
| `cdn` | `url`, `origin` (`host:port` it fetches from), `certificateState`, `certificateIssuer`, `certificateExpiresAt`, `cachedBytes` |
| `email` | `internalHost`, `smtpPort` (1025), `host`, `port` (SMTP on the machine), `webUrl` (the message viewer on the machine, same user and password), `username`, `passwordSecret`, `mode` |

Every placed kind (database, search, email) also reports `privateIp` and `privateDns` (§7.2).

`GET /machine` gains `broker: { available: boolean, message: string }`.

### 9.3 Data endpoints

| Method and path | Kinds | Access | Body → response |
|---|---|---|---|
| `POST /resources/{id}/messages` | queue, topic | write | `SendMessage { body: string; attributes?: Record<string, string>; deduplicationId?: string }` → `201 SentMessage { id: string; duplicate: boolean }` |
| `GET /resources/{id}/messages?limit=N` | queue | session or write | → `200 MessageList { stats: QueueStats; items: PeekedMessage[] }`: up to N (1 – 50, default 20) waiting messages, oldest first, without receiving them |
| `POST /resources/{id}/messages/receive` | queue | write | `ReceiveMessages { max?: number; waitSeconds?: number }` → `200 { items: ReceivedMessage[] }`, empty when nothing arrived within the wait |
| `POST /resources/{id}/messages/delete` | queue | write | `DeleteMessages { receipts: string[] }` → `200 { deleted: number; failed: { receipt: string; message: string }[] }` |
| `GET /resources/{id}/deliveries` | topic | read | → `200 DeliveryStats { published: number; items: SubscriptionStats[] }` |
| `POST /resources/{id}/emails` | email | write | `SendEmail { from: string; to: string[]; cc?: string[]; bcc?: string[]; replyTo?: string; subject: string; text?: string; html?: string }` → `201 { id: string }` |
| `GET /resources/{id}/emails?limit=N` | email | session or write | → `200 EmailList { total: number; items: EmailSummary[] }`, newest first (N 1 – 200, default 50) |
| `GET /resources/{id}/emails/{emailId}` | email | session or write | → `200 Email` |

```ts
interface QueueStats { messages: number; inFlight: number; oldestAgeSeconds: number | null; deadLettered: number }
interface PeekedMessage { id: string; body: string; attributes: Record<string, string>; sentAt: string }
interface ReceivedMessage extends PeekedMessage { receipt: string; receiveCount: number }
interface SubscriptionStats { target: string; delivered: number; pending: number; failed: number; lastError: string }   // target "queue/<name>" | the URL
interface EmailSummary { id: string; from: string; to: string[]; subject: string; sentAt: string; sizeBytes: number; relay: 'captured' | 'relayed' }   // relayed: handed to the relay; a refusal shows in the service's logs
interface Email extends EmailSummary { cc: string[]; bcc: string[]; replyTo: string; text: string; html: string; headers: Record<string, string[]> }
```

- A data endpoint on another kind answers `409 ACTION_NOT_SUPPORTED`; on a resource that isn't `ready`, `409 ACTION_NOT_SUPPORTED` ("Wait until it's ready"); when the broker or the email service can't be reached, `503 ENGINE_UNAVAILABLE`.
- Message bodies and emails may hold anything an app sent, so reading them needs the same access as revealing a secret (L3-006 §8).
- A receipt is opaque. Deleting with a receipt of another queue, or one already used, reports it in `failed`; a receipt whose visibility timeout ended may fail, since the message may have gone to another worker.

### 9.4 Validation rules

| Field | Rule |
|---|---|
| `database.spec.engine` / `version` | One of the engines; a version of that engine (§9.2); immutable |
| `queue.spec.visibilityTimeoutSeconds` | 1 – 43 200 |
| `queue.spec.retentionHours` | 1 – 336 |
| `queue.spec.maxMessageBytes` | 1024 – 1 048 576 |
| `queue.spec.maxReceives` | 0 – 1000 |
| `queue.spec.deadLetterQueue` | An existing queue, not itself; requires `maxReceives` ≥ 1 |
| `topic.spec.retentionHours` | 1 – 336 |
| `topic.spec.subscriptions` | ≤ 20, distinct; `queue` an existing queue; `url` `https://…`, or `http://` to `localhost` / `127.0.0.1`, ≤ 2048 chars; `secret` an existing secret, only with `url` |
| `search.spec.version` | `2` or `3`; immutable |
| `search.spec.memoryMiB` | 1024 – 16 384, a multiple of 256 |
| `transcoder.spec.input` / `output` | `bucket` an existing bucket; `prefix` ≤ 255 chars, no `..`, no leading `/`, printable; on the same bucket, neither prefix starts with the other |
| `transcoder.spec.presets` | 1 – 7 distinct presets |
| `transcoder.spec.timeoutSeconds` / `concurrency` / `memoryMiB` | 60 – 86 400 / 1 – 4 / 256 – 8192 |
| `cdn.spec.hostname` | As a domain's (§6.5), no wildcard; not served by another domain or CDN (`HOSTNAME_TAKEN`); immutable |
| `cdn.spec.origin` / `originPort` | `app/<name>` or `bucket/<name>` of an existing resource; `originPort` 1 – 65535, apps only, required when the app has no port |
| `cdn.spec.defaultTtlSeconds` / `maxSizeMiB` | 0 – 31 536 000 / 64 – 102 400 |
| `cdn.spec.tls` / `redirectHttp` / `zone` | As a domain's |
| `email.spec.mode` / `relay` | `capture` or `relay`; `relay` required exactly in relay mode |
| `email.spec.relay` | `host` a hostname or IP; `port` 1 – 65535; `tls` one of the three; `credentialsSecret` an existing secret |
| `email.spec.domain` / `keepMessages` | A hostname / 10 – 10 000 |
| `SendMessage.body` | UTF-8, ≤ the queue's `maxMessageBytes` (topics: 1 MiB); `attributes` ≤ 10, names as environment keys, values ≤ 1024 chars; `deduplicationId` ≤ 128 chars |
| `ReceiveMessages` | `max` 1 – 10 (default 1); `waitSeconds` 0 – 20 (default 0) |
| `DeleteMessages.receipts` | 1 – 10 |
| `SendEmail` | `from` an address (in `domain` when set); 1 – 50 recipients across `to`, `cc`, `bcc`; `subject` ≤ 998 chars without line breaks; `text` or `html` required, together ≤ 1 MiB |
| `run` on a transcoder | `key` required, ≤ 1024 chars, inside the input prefix |

### 9.5 Dependencies (CP-12)

`resource_refs` gains these references, each refusing deletion of its target with `RESOURCE_IN_USE`:
- queue → its dead-letter queue;
- topic → each subscribed queue, each signing secret;
- search, email, database → their network (as v0.4);
- email → its relay credentials secret;
- transcoder → its input and output buckets;
- cdn → its origin app or bucket, its zone.

### 9.6 Firewalls

Firewalls accept `search/<name>` and `email/<name>` wherever §7.3 allows a resource: as a target, and as a rule's source or destination (`L2-008` SRC-05, EML-07). Databases of every engine were already accepted as `database/<name>`. The allowed kinds are `RULES.firewallKinds`, and the kinds that take `network` and `subnet` are `RULES.placeableKinds`. Both are generated from `pkg/api` (ADR-010). Queues and topics are reached through the broker, and CDNs through the edge, so none of them is a firewall target.

## 10 · v0.7 additions (planned)

This section specifies the v0.7 contract (`L2-009`; ADR-035 – ADR-041). As each phase ships, this section says so, in the same PR as `pkg/api`, `api/openapi.yaml`, the generated console file, both console adapters and the handlers (ADR-010).

### 10.1 Kinds and identifiers

`Kind` gains `dashboard` (`dash_`), `alert` (`alrt_`), `channel` (`chan_`), `logexport` (`logx_`) and `secretstore` (`sst_`). Projects and users are not resources: they have their own endpoints (§10.3) and are named by their name (projects) or ID (users, `usr_`).

### 10.2 Projects and resources

```ts
interface Resource { /* v0.6 fields */ project: string }                  // 'default' for everything made before v0.7
interface CreateResourceRequest { kind: Kind; name: string; project?: string; spec: unknown }   // project default 'default'; immutable

interface Project { name: string; description: string; createdAt: string; resources: number }   // resources: how many, of every kind
interface ProjectList { items: Project[] }
interface CreateProject { name: string; description?: string }
interface UpdateProject { description: string }
```

- `GET /resources?project=P` lists one project; without it, every project the caller can see. `?name=N` with `project` finds one resource.
- Outputs that name engine objects follow ADR-035: in a project other than `default`, `internalHost` is `es-<kind>-<name>.<project>` and `privateDns` is `<name>.<kind>.<project>.internal`; broker subjects are `es.q.<project>_<name>` and `es.t.<project>_<name>`, users `q-<project>_<name>` and `t-<project>_<name>`, and streams `ESQ_<project>_<name>` and `EST_<project>_<name>`.
- A bucket's `credentialsSecret` output names its project's `objectstore-credentials`.

### 10.3 People, roles and tokens

```ts
type Role = 'viewer' | 'editor' | 'admin';
interface Session { /* v0.6 fields */ machineRole: Role | null; projectRoles: Record<string, Role>;
                    token: { role: Role; projects: string[] | null } | null }   // token: set when authenticated by a token
interface UserSummary { id: string; email: string; machineRole: Role | null; projectRoles: Record<string, Role>;
                        status: 'active' | 'invited'; createdAt: string; lastSignInAt: string | null }
interface UserList { items: UserSummary[] }
interface InviteUser { email: string; machineRole?: Role | null; projectRoles?: Record<string, Role> }
interface UpdateUser { machineRole?: Role | null; projectRoles?: Record<string, Role | null> }   // null removes; absent keeps
interface Invitation { user: UserSummary; url: string; expiresAt: string;   // url shown once
                     emailed: boolean; emailError?: string }   // emailed: the link was also sent by email (IAM-04)
interface InviteEmail { project?: string; service: string; from: string }   // an email service of the project (default: default)
interface MachineSettings { /* v0.3 fields */ inviteEmail?: InviteEmail | null }   // absent keeps it; null turns it off
interface Machine { /* earlier fields */ inviteEmail: InviteEmail | null }
interface InviteInfo { email: string; kind: 'invite' | 'reset' }
interface AcceptInvite { code: string; password: string }
interface ChangePassword { currentPassword: string; newPassword: string }
interface Token { /* v0.6 fields */ role: Role; projects: string[] | null; userEmail: string }   // scope stays: 'read' for viewer, else 'write'
interface CreateTokenRequest { name: string; role?: Role; projects?: string[] | null; expiresInDays?: number; scope?: 'read' | 'write' }   // role default: the owner's strongest role; scope accepted from v0.6 clients (read → viewer, write → strongest)
interface AuditEntry { id: number; at: string; actor: string; action: string; target: string; detail: string; clientIp: string }
interface AuditList { items: AuditEntry[]; nextCursor: string | null }
```

| Method and path | Who | Body → response |
|---|---|---|
| `GET /projects` | any | → `200 ProjectList` (projects the caller can see) |
| `POST /projects` | machine admin | `CreateProject` → `201 Project` |
| `GET /projects/{name}` | viewer of it | → `200 Project` |
| `PATCH /projects/{name}` | admin of it | `UpdateProject` → `200 Project` |
| `DELETE /projects/{name}` | machine admin | → `204`; `409 PROJECT_NOT_EMPTY` listing what's left; `default` → `409 ACTION_NOT_SUPPORTED` |
| `GET /users` | machine admin; a project admin sees users with a role in its projects | → `200 UserList` |
| `POST /users` | machine admin; a project admin for its projects, without `machineRole` | `InviteUser` → `201 Invitation`; an existing email → `409 EMAIL_TAKEN` |
| `GET /users/{id}` | as `GET /users` | → `200 UserSummary` |
| `PATCH /users/{id}` | as `POST /users` | `UpdateUser` → `200 UserSummary`; removing the last machine admin → `409 LAST_ADMIN` |
| `DELETE /users/{id}` | machine admin | → `204`; the last machine admin → `409 LAST_ADMIN` |
| `POST /users/{id}/reset` | machine admin | → `200 Invitation` (`kind: reset` for an active user, a fresh invite for an invited one) |
| `GET /invite?code=C` | public | → `200 InviteInfo`; unknown, used or expired → `410 INVITE_INVALID` |
| `POST /invite` | public | `AcceptInvite` → `200 Session` + cookie; throttled like sign-in |
| `PUT /session/password` | a signed-in user (session only) | `ChangePassword` → `204`; ends the user's other sessions |
| `GET /tokens?all=true` | machine admin | → every user's tokens; without `all`, the caller's |
| `DELETE /tokens/{id}` | owner; machine admin for any | → `204` |
| `GET /audit?limit=N&cursor=C` | machine admin | → `200 AuditList`, newest first (N 1 – 200, default 50) |

- A token can't grant more than its user has now; `role: admin` needs the user to be an admin somewhere.
- Every resource endpoint checks the caller's role in the resource's project (ADR-036): reading needs `viewer`; `GET /resources/{id}/value`, `GET …/runs/{runId}`, `GET …/messages`, `GET …/emails…` need `editor`; writes and actions need `editor`. A project the caller can't see answers `404 NOT_FOUND`; a role too weak answers `403 FORBIDDEN` with the role named in the message.
- `PATCH /machine` needs the machine admin role.
- **Invites by email (IAM-04).** With `inviteEmail` set, `POST /users` and `POST /users/{id}/reset` also send the link from `from` through that email service: subject "You're invited to <machine name>" or "Reset your password on <machine name>", the link, its expiry and who sent it. Sending is part of the request (10-second limit) but never fails it: `emailed` says whether it went and `emailError` why not, and the link is in the answer either way. `PATCH /machine` checks that the service exists and `from` is an address (`422`); an email service that only sends from its own domain refuses another `from` at send time (`emailError`).

### 10.4 Metrics

```ts
interface MetricPoint { t: number; v: number; max?: number }   // t: unix seconds; max on 15-minute rollups
interface MetricSeries { metric: string; unit: 'cores' | 'bytes' | 'bytesPerSecond' | 'perSecond' | 'count' | 'seconds' | 'milliseconds' | 'percent'; points: MetricPoint[] }
interface MetricData { from: string; to: string; stepSeconds: number; items: MetricSeries[] }
```

| Method and path | Who | → response |
|---|---|---|
| `GET /resources/{id}/metrics?metric=M&metric=M2&from=T&to=T` | viewer | `200 MetricData`; no `metric` → the kind's standard set; `from` default an hour ago, `to` default now (RFC 3339) |
| `GET /machine/metrics?metric=…&from=…&to=…` | any | `200 MetricData` for the machine |

Metric names (`METRICS` in the generated file, with unit, title and kinds):

| Metric | Unit | Kinds |
|---|---|---|
| `cpu`, `memory`, `memoryLimit`, `netRx`, `netTx`, `diskRead`, `diskWrite`, `restarts` | cores, bytes, bytes, B/s, B/s, B/s, B/s, count | app, database, cache, search, email, cdn, loadbalancer, vm (VMs: `cpu`, `memory`, `restarts`) |
| `requests`, `errors5xx`, `latency` | per second, per second, ms | app (through domains), cdn |
| `queueDepth`, `inFlight`, `oldestAge` | count, count, seconds | queue |
| `delivered`, `deliveryFailures` | per second | topic |
| `usedBytes`, `percentFull` | bytes, percent | volume |
| `cachedBytes` | bytes | cdn |
| machine: `cpu`, `memory`, `diskFree`, `diskPercentFull`, `resourcesFailed` | cores, bytes, bytes, percent, count | — |

`restarts` is the number of restarts in each point's interval.

### 10.5 New kinds

```ts
type Kind = /* v0.6 kinds */ | 'dashboard' | 'alert' | 'channel' | 'logexport' | 'secretstore';

interface Widget { title: string; metric: string; resources: string[] }   // "kind/name" of the same project
interface DashboardSpec { widgets: Widget[] }

interface AlertCondition {
  type: 'metric' | 'crash_loop' | 'failed' | 'run_failed';
  resources: string[];                 // "kind/name", 1 – 10
  metric?: string; operator?: 'above' | 'below'; threshold?: number; forMinutes?: number;   // metric: forMinutes default 5
  restarts?: number; windowMinutes?: number;                                                 // crash_loop: defaults 3 and 10
}
interface AlertSpec { condition: AlertCondition; channels: string[]; repeatMinutes?: number; enabled?: boolean }   // enabled default true
// outputs: { state: 'ok' | 'firing' | 'no_data' | 'disabled'; since: string; firing: number }

interface ChannelSpec {
  type: 'email' | 'webhook' | 'slack' | 'discord' | 'telegram';
  email?: { service: string; from: string; to: string[] };   // type email
  webhook?: { url: string; secret?: string };                 // type webhook
  secret?: string;                                            // slack, discord: the webhook URL; telegram: the bot token
  chatId?: string;                                            // telegram
}

interface LogDestination {   // exactly one
  bucket?: { bucket: string; prefix?: string };
  s3?: { endpoint: string; region?: string; bucket: string; prefix?: string; credentialsSecret: string };
  http?: { url: string; headersSecret?: string };
  otlp?: { endpoint: string; headersSecret?: string };
  loki?: { url: string; headersSecret?: string; labels?: Record<string, string> };
}
interface LogExportSpec { sources?: string[]; filter?: string; flushSeconds?: number; destination: LogDestination }
// outputs: { linesSent: number; bytesSent: number; lastSentAt: string | null; lagSeconds: number; lastError: string }

interface SecretStoreSpec {
  provider: 'vault' | 'onepassword' | 'aws' | 'doppler' | 'infisical';
  refreshSeconds?: number;   // default 300
  vault?: { address: string; mount?: string; namespace?: string; tokenSecret: string };   // mount default 'secret'
  onepassword?: { connectUrl: string; vault: string; tokenSecret: string };
  aws?: { region: string; credentialsSecret: string; endpoint?: string };                // {"accessKey","secretKey"}
  doppler?: { tokenSecret: string; endpoint?: string };
  infisical?: { siteUrl?: string; projectId: string; environment: string; path?: string; credentialsSecret: string };   // {"clientId","clientSecret"}
}

interface SecretSpec { value?: string; source?: { store: string; key: string; field?: string } }   // exactly one; responses show source, never the value
// a synced secret's outputs add: { syncedAt: string; store: string }
```

| Method and path | Kinds | Who | Body → response |
|---|---|---|---|
| `POST /resources/{id}/actions/test` | alert, channel, secretstore | editor | → `200 TestResult { ok: boolean; items: { target: string; ok: boolean; message: string }[] }` |
| `POST /resources/{id}/actions/refresh` | secret (with a source) | editor | → `202 Resource` |
| `GET /resources/{id}/incidents?limit=N` | alert | viewer | → `200 { items: Incident[] }`, newest first (N 1 – 200, default 50) |
| `GET /resources/{id}/notifications?limit=N` | channel | viewer | → `200 { items: Notification[] }`, newest first (N 1 – 100, default 50) |

```ts
interface Incident { id: string; subject: string; startedAt: string; resolvedAt: string | null; value: number | null; message: string;
                     notifications: { channel: string; event: string; status: 'pending' | 'sent' | 'failed'; error: string }[] }
interface Notification { id: string; event: 'firing' | 'resolved' | 'repeat' | 'test'; alert: string; subject: string;
                         status: 'pending' | 'sent' | 'failed'; attempts: number; lastError: string; createdAt: string; sentAt: string | null }
```

### 10.6 Error codes

| Code | HTTP | When |
|---|---|---|
| `PROJECT_NOT_EMPTY` | 409 | Deleting a project with resources; details list them |
| `LAST_ADMIN` | 409 | Removing or demoting the last machine admin |
| `EMAIL_TAKEN` | 409 | Inviting an email that already has a user |
| `INVITE_INVALID` | 410 | An invite or reset code that's unknown, used or expired |

`FORBIDDEN` (403, since v0.2) now also answers a role that is too weak.

### 10.7 Validation rules

| Field | Rule |
|---|---|
| `project` (resources, projects) | The name rules (§4); an existing project for resources; immutable |
| `Project.description` | ≤ 280 characters |
| `InviteUser.email` | An email address, ≤ 254 characters |
| roles | `viewer`, `editor` or `admin`; project names existing |
| `CreateTokenRequest.projects` | Existing projects, 1 – 50, or null for all; `role` no stronger than the user's strongest role |
| `AcceptInvite.password` / `ChangePassword.newPassword` | As the setup password (§4) |
| `metric` query | A metric of the resource's kind; `from` < `to`, at most 31 days apart |
| `dashboard.spec.widgets` | 1 – 24; `title` 1 – 80 characters; `metric` a known metric; `resources` 1 – 10 `kind/name` strings of kinds that have the metric |
| `alert.spec.condition` | `resources` 1 – 10 `kind/name`; metric: `metric` of every named kind, `operator`, a finite `threshold`, `forMinutes` 1 – 60; crash loop: kinds that restart, `restarts` 1 – 100, `windowMinutes` 1 – 120; run failed: job, backup or transcoder |
| `alert.spec.channels` | 1 – 10 existing channels of the project |
| `alert.spec.repeatMinutes` | 0 – 1440 |
| `channel.spec` | The block matching `type`, and no other. Email: an existing email service, `from` an address, 1 – 20 recipients. Webhook: `https://…`, or `http://` to `localhost` / `127.0.0.1`, ≤ 2048 characters; `secret` an existing secret. Slack, Discord, Telegram: `secret` an existing secret. Telegram: `chatId` 1 – 64 characters |
| `logexport.spec.sources` | 0 – 50 distinct `kind/name` of kinds that have logs |
| `logexport.spec.filter` | A valid regular expression (RE2), ≤ 512 characters |
| `logexport.spec.flushSeconds` | 5 – 300 |
| `logexport.spec.destination` | Exactly one block. bucket: an existing bucket; `prefix` as a transcoder's. s3: as a backup's (§6.5). http and otlp: `http://` or `https://` URLs ≤ 2048 characters. loki: the same, `labels` ≤ 10, names `[a-zA-Z_][a-zA-Z0-9_]*`. Header secrets exist |
| `secretstore.spec.provider` | One of the five, with its block and no other; immutable |
| `secretstore.spec.refreshSeconds` | 60 – 86 400 |
| store blocks | URLs `http://` or `https://` ≤ 2048 characters; `mount` and `path` printable, no `..`; `region` like `eu-west-1`; secrets exist |
| `secret.spec` | Exactly one of `value` and `source`; `source.store` an existing ready-or-not store of the project; `key` 1 – 512 characters; `field` ≤ 128 characters |

### 10.8 Dependencies (CP-12)

`resource_refs` gains these references, each refusing deletion of its target with `RESOURCE_IN_USE`:
- alert → each channel;
- channel → its email service, its signing or webhook secret;
- logexport → its bucket, its credentials and headers secrets;
- secretstore → its token or credentials secret;
- secret → its store.

Dashboards and alerts don't reference the resources they show or watch.

## 11 · v0.8 additions (planned)

This section specifies the v0.8 contract (`L2-010`; ADR-042 – ADR-048). As each phase ships, this section says so, in the same PR as `pkg/api`, `api/openapi.yaml`, the generated console file, both console adapters and the handlers (ADR-010).

### 11.1 Kinds and identifiers

`Kind` gains `stack` (`stk_`). Machines are not resources: like projects, they have their own endpoints (§11.2) and are named by their name; their IDs are `mch_…`, and `main` for the main machine.

### 11.2 Machines

```ts
type MachineState = 'waiting' | 'online' | 'offline';
interface MachineLink { machine: string; connected: boolean; lastHandshakeAt: string | null }
interface MachineSummary {
  id: string; name: string; title: string; main: boolean;   // main: name 'main', title the machine name from setup
  state: MachineState; since: string;                        // since: when it entered the state
  labels: Record<string, string>; schedulable: boolean;
  address: string | null;                     // what the agent advertised; the main machine's public address or null
  os: string; arch: string; agentVersion: string; engine: Engine;
  cpus: number; memoryTotalBytes: number; memoryReservedBytes: number;
  vms: VMSupport; lastSeenAt: string | null; resources: number;
  versionMismatch: boolean;                   // MCH-10: true when the agent's minor version differs
  links: MachineLink[];                       // XNET-06: one per other online machine
  message: string;                            // why it can't take placements, or ''
}
interface MachineList { items: MachineSummary[] }
interface AddMachine { name: string; labels?: Record<string, string> }
interface AddedMachine { machine: MachineSummary; joinCode: string; joinCommand: string; expiresAt: string }   // code shown once
interface UpdateMachine { labels?: Record<string, string> | null; schedulable?: boolean }   // labels {} clears them; absent or null keeps them
interface JoinRequest { code: string; csr: string; advertise: string; agentPort: number; tunnelPort: number;
                        os: string; arch: string; agentVersion: string }
interface JoinResponse { machineId: string; name: string; certificate: string; ca: string; server: string }
interface DrainResult { moving: string[]; pinned: string[] }   // "kind/name" (with "project/" outside default)
```

| Method and path | Who | Body → response |
|---|---|---|
| `GET /machines` | any signed-in | → `200 MachineList`; resource counts only cover projects the caller can see |
| `POST /machines` | machine admin | `AddMachine` → `201 AddedMachine` (state `waiting`) |
| `GET /machines/{name}` | any signed-in | → `200 MachineSummary` |
| `PATCH /machines/{name}` | machine admin | `UpdateMachine` → `200 MachineSummary` |
| `POST /machines/{name}/join-code` | machine admin | → `200 AddedMachine` with a new code; only while `waiting` → else `409 ACTION_NOT_SUPPORTED` |
| `POST /machines/{name}/drain` | machine admin | → `202 DrainResult`; the main machine → `409 ACTION_NOT_SUPPORTED` |
| `DELETE /machines/{name}?force=true` | machine admin | → `204`; anything placed there → `409 MACHINE_NOT_EMPTY` listing it; `force` on an online machine → `409 MACHINE_ONLINE`; `main` → `409 ACTION_NOT_SUPPORTED` |
| `POST /machines/join` | public, rate-limited like sign-in | `JoinRequest` → `200 JoinResponse`; unknown, used or expired code → `410 JOIN_CODE_INVALID` |
| `GET /machines/{name}/metrics?metric=…` | any signed-in | → `200 MetricData` (§10.4 machine metrics, for that machine: CPU, memory and failed resources of what runs there; the disk metrics are the main machine's data folder, so other machines' disk series are empty); `GET /machine/metrics` stays the main machine's |

- `GET /machine` keeps describing the main machine; `PATCH /machine` keeps its settings.
- `joinCommand` is `enclave agent join <server> --code <code>`, where `<server>` is the main machine's console URL, public address or listen address, in that order. A loopback address is replaced with a note that the other computer can't reach it.
- Events on a machine: `GET /machines/{name}/events` → `200 EventList` (`added`, `joined`, `online`, `offline`, `drain_started`, `removed`, `version_mismatch`).

### 11.3 Placement

```ts
interface Placed { machine?: string; machineLabels?: Record<string, string> }   // on AppSpec, DatabaseSpec, CacheSpec, JobSpec,
                                                                                // LoadBalancerSpec, VMSpec, VolumeSpec, SearchSpec, EmailSpec
// outputs of every placed resource: { machine: string, ... }; host is that machine's address
```

- `machine` names a machine; an unknown one → `422 FIELD_NOT_FOUND` on `spec.machine`. `machineLabels` has 1 – 10 entries, keys and values as labels (`[a-z0-9][a-z0-9._-]{0,62}`).
- A resource that uses a volume must name the volume's machine or none; another → `422` on `spec.machine` naming the volume. Volumes on two machines → `422` on `spec.volumes`.
- Not placed yet: `status.message` says why ("No machine has 1024 MiB free.", "No machine has the labels disk=ssd.", "No machine can run VMs.") and `outputs.machine` is absent.
- `GET /resources?machine=<name>` lists what runs on a machine.
- An app job (`JobSpec.app`) refuses `machine`: it runs where the app runs.

### 11.4 Moving

- `PATCH` with a new `machine` (or labels the current machine lacks) answers `202`, and the resource goes `updating` with steps `["move", …the kind's steps]`. `move`'s message counts bytes copied ("Copied 1.2 GB of 3.4 GB").
- Moving a resource with data (database, cache, search, email, volume, VM) whose machine is offline → `409 MACHINE_OFFLINE`: "rack-1 is offline, so the data of orders can't be read. Restore a backup to a new database instead."
- Moving to a machine that is offline, not schedulable or of another version → `422` on `spec.machine` with the reason.
- Events: `move_started` ("Moving from mac-mini to rack-1."), `moved` ("Moved to rack-1 in 41 s; unavailable for 28 s.") and `move_failed` ("Copying the data failed: …; still running on mac-mini.").

### 11.5 Catalogue

```ts
type ParameterType = 'string' | 'number' | 'boolean' | 'secret' | 'hostname';
interface TemplateParameter { name: string; title: string; description?: string; type: ParameterType;
                              default?: string | number | boolean; required?: boolean }
interface TemplateMember { kind: Kind; name: string; optional: boolean }   // name as created, with ${stack} shown as "<stack>"
interface CatalogueEntry { name: string; title: string; description: string; version: string; versions: string[];
                           builtIn: boolean; parameters: TemplateParameter[]; members: TemplateMember[]; outputs: string[] }
interface CatalogueList { items: CatalogueEntry[] }
interface AddTemplate { body: string }   // YAML or JSON
```

| Method and path | Who | Body → response |
|---|---|---|
| `GET /catalogue` | any signed-in | → `200 CatalogueList`, built-ins first, then own templates, each by name |
| `GET /catalogue/{name}` | any signed-in | → `200 CatalogueEntry` (`?version=` for an older one) |
| `GET /catalogue/{name}/template` | any signed-in | → `200` the template's source (`text/yaml`) |
| `POST /catalogue` | machine admin | `AddTemplate` → `201 CatalogueEntry`; an existing name with a higher version adds that version; the same version → `409 NAME_TAKEN`; a built-in's name → `409 NAME_TAKEN`; an invalid template → `422` with `details[].field` `line 12: resources[2].spec.port` |
| `DELETE /catalogue/{name}` | machine admin | → `204`; a built-in → `409 ACTION_NOT_SUPPORTED`; stacks that use it → `409 RESOURCE_IN_USE` listing them |

### 11.6 Template format

```yaml
name: wordpress                 # the catalogue name: resource name rules
title: WordPress
description: A WordPress site with its MySQL database and an uploads volume.
version: 1.0.0                  # semver; one entry keeps every version added
parameters:
  - { name: domain, title: Domain, type: hostname }               # optional unless required: true
  - { name: memoryMiB, title: Memory (MiB), type: number, default: 512 }
resources:
  - { kind: secret, name: salt, generate: 48 }                   # a random value of 48 characters
  - kind: database
    name: db
    spec: { engine: mysql, version: "8.4" }
  - kind: volume
    name: uploads
    spec: { type: filesystem }                                    # a folder: it has no size
  - kind: app
    name: ""                                                      # the stack's own name
    spec:
      image: wordpress:6.8.3-apache
      port: 80
      memoryMiB: "${param:memoryMiB}"                             # one placeholder: takes the number type
      env: { WORDPRESS_DB_HOST: "${host:database/db}", WORDPRESS_DB_USER: "${ident:db}", WORDPRESS_DB_NAME: "${ident:db}" }
      secretEnv: { WORDPRESS_DB_PASSWORD: "${name:db}-password", WORDPRESS_AUTH_SALT: "${name:salt}" }
      volumes: [{ volume: "${name:uploads}", path: /var/www/html/wp-content }]
  - kind: domain
    name: site
    when: "${param:domain}"                                       # left out when empty
    spec: { hostname: "${param:domain}", app: "${name:}", tls: auto }
outputs:
  url: "https://${param:domain}"
```

- **Placeholders**: `${stack}`; `${project}`; `${param:<name>}`; `${name:<member>}` and `${name:}` (the stack's own name); `${host:<kind>/<member>}` (`es-<kind>-<name>` in `default`, `es-<kind>-<name>.<project>` elsewhere); `${ident:<member>}` (hyphens become underscores). A `secret` parameter renders as the name of the secret the stack made for it (`<stack>-<param>`). `$$` is a literal `$`.
- **Validation**, before anything is created: unknown top-level or member keys, kinds, placeholders or parameters; duplicate member names; a `when` that isn't one placeholder; a parameter value of the wrong type or a missing required one. Then each rendered member's spec goes through the kind's validation. Errors name the member (`resources[2].spec.port`) and, for template errors, the line.
- **Limits**: 64 KiB per template, 50 members, 30 parameters, 20 versions per entry.

### 11.7 Stacks

```ts
interface StackSpec { template?: string; templateBody?: string; version?: string;   // exactly one of template, templateBody
                      parameters?: Record<string, string | number | boolean>;
                      secretParameters?: Record<string, string> }                 // write-only: never returned
// outputs: { template: string; version: string; members: StackMember[]; outputs: Record<string, string>; ready: number; total: number }
interface StackMember { kind: Kind; name: string; id: string; phase: Phase }
interface Resource { /* earlier fields */ stack: string | null }                  // the stack a member belongs to
```

- Creating a stack is `POST /resources` with `kind: 'stack'`. It answers `201` with the stack `pending` and every member created `pending` in the same transaction. Any error answers `422` and creates nothing, with the member and field in each detail (`members.db.spec.version`).
- `PATCH` a stack's `parameters`, `secretParameters` or `version` re-renders it: `202`, with members created, updated and marked `deleting` to match, in one transaction. `template` and `templateBody` are immutable (`409 IMMUTABLE_FIELD`).
- `PATCH` or `DELETE` of a member → `409 PART_OF_STACK`: "orders-db is part of the stack orders. Change or delete the stack instead." Actions on members work.
- `DELETE` of a stack → `202`; it is removed after its last member. A member something outside the stack uses → `409 RESOURCE_IN_USE` naming it.
- `GET /resources?stack=<name>` lists a stack's members.

### 11.8 Error codes

`MACHINE_NOT_EMPTY` (409), `MACHINE_ONLINE` (409), `MACHINE_OFFLINE` (409), `JOIN_CODE_INVALID` (410), `PART_OF_STACK` (409).

### 11.9 Validation rules

| Field | Rule |
|---|---|
| machine `name` | resource name rules; unique; `main` is the main machine's and can't be taken |
| `labels` | 0 – 10; key `[a-z0-9][a-z0-9._-]{0,62}`, value `[A-Za-z0-9._-]{0,63}` |
| `spec.machine` | an existing machine; the volume's machine when the resource uses volumes; omitted for an app job |
| `spec.machineLabels` | as `labels`, 1 – 10 |
| `JoinRequest.advertise` | an IPv4 address or host name; `agentPort`, `tunnelPort` 1 – 65535 |
| stack `template` | a catalogue name; `version` one of its versions |
| stack `templateBody` | as `POST /catalogue` |
| stack `parameters` | every key a parameter of the template; values of its type; `hostname` as a domain's hostname |

### 11.10 Dependencies (CP-12)

- A machine can't be removed while resources are placed on it (`MACHINE_NOT_EMPTY`).
- A catalogue template can't be removed while stacks use it.
- References between members of one stack don't block deleting the stack; references from outside do.

### 11.11 Agent protocol (internal)

Between the control plane and an agent only, over mutual TLS (ADR-042, ADR-043); never through the public API.

| Method and path | Body → response |
|---|---|
| `GET /agent/v1/info` | → `{ version, protocol, machineId, os, arch, engine, cpus, memoryTotalBytes, vms, tunnelPublicKey }` |
| `POST /agent/v1/driver/{Method}` | JSON arguments → JSON result, or a stream for the calls of ADR-042 |
| `POST /agent/v1/hypervisor/{Method}` | as above |
| `POST /agent/v1/calls/{id}/continue` | `{ error?: string }` → `204`: the answer to a `started` callback |
| `PUT /agent/v1/files/{sha256}` | a file → `204`; refused when the body's digest differs |
| `POST /agent/v1/renew` | → `{ csr }`; then `POST /agent/v1/certificate` `{ certificate }` → `204` |
| `POST /agent/v1/sweep` | `{ keep: string[] }` → `{ removed: string[] }` (ADR-046) |

Errors are `{ class: 'permanent' | 'unavailable' | 'no_vms' | 'job_missing' | 'no_ca' | 'transient', message }`, with HTTP status 422, 503, 503, 404, 404 and 500 in that order.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler Labs | Initial contract for console v0.1 (CON-01 – CON-12) |
| v2 | 2026-10-20 | Soupler Labs | Full control-plane contract for all clients: auth (cookie + tokens, scopes, CSRF), L0-003 error envelope and codes, idempotency, ETag/If-Match, rate limits, pagination, CRUD for five kinds, actions, events, logs, secret reveal, tokens, machine, validation rules, phase machine |
| v3 | 2026-10-20 | Soupler Labs | api/openapi.yaml written and kept equal to the router and the error codes by tests |
| v4 | 2026-10-20 | Soupler Labs | §6 v0.3 contract (planned): domain, job, backup kinds; health checks, strategy, Git source; runs, run and restore actions; machine settings and CA; new error codes and validation rules |
| v5 | 2026-10-21 | Soupler Labs | Phase 1 shipped: healthCheck, strategy and revision outputs moved into §3/§4; §6 marks what is served |
| v6 | 2026-10-21 | Soupler Labs | Phase 2 shipped: domains, machine settings, CA |
| v7 | 2026-10-21 | Soupler Labs | Phase 3 shipped: jobs, runs, run action |
| v8 | 2026-10-21 | Soupler Labs | Phase 4 shipped: backups, restore action, run artifacts; retention scoped to a backup's own runs; RUN_NOT_RESTORABLE as implemented; bodies rejected on actions that take none |
| v9 | 2026-10-21 | Soupler Labs | Phase 5 shipped: Git sources, build outputs, build log; all of §6 served |
| v10 | 2026-10-21 | Soupler Labs | §7 v0.4 contract (planned): network, firewall, loadbalancer, zone and record kinds; placement and replicas; domain zone; outputs, validation, OVERLAPS, dependencies |
| v11 | 2026-10-21 | Soupler Labs | §7 served (all five phases); attached output removed |
| v12 | 2026-10-21 | Soupler Labs | §8 v0.5 contract (planned): vm, volume and snapshot kinds; volume mounts; VM power actions and console; machine VM availability; VOLUME_ATTACHED, GROW_ONLY; validation; dependencies |
| v13 | 2026-10-21 | Soupler Labs | §8 served; as-built outputs linkPort, filledFrom, sizeGiB on disk snapshots |
| v14 | 2026-10-21 | Soupler Labs | §8.5: firewalls name VMs as targets and peers |
| v15 | 2026-10-21 | Soupler Labs | §9 v0.6 contract (planned): database engines; queue, topic, search, transcoder, cdn and email kinds; purge and transcoder run actions; message, delivery and email data endpoints; validation; dependencies |
| v16 | 2026-10-22 | Soupler Labs | §9 served. §9.6: firewalls accept search and email services as targets and peers; `RULES.firewallKinds` and `RULES.placeableKinds` generated from `pkg/api` |
| v17 | 2026-10-22 | Soupler Labs | §10 v0.7 contract (planned): projects on every resource, projects, users, invites, roles and scoped tokens, audit, metrics, dashboard, alert, channel, logexport and secretstore kinds, external secrets, test and refresh actions, incidents and notifications, new error codes, validation, dependencies |
| v18 | 2026-10-22 | Soupler Labs | §10 served. §10.3: a token without a role gets its owner's strongest role (`write` from v0.6 clients too) |
| v19 | 2026-10-22 | Soupler Labs | §10.3: invites and resets by email: `MachineSettings.inviteEmail` (absent keeps, null clears), `Machine.inviteEmail`, `Invitation.emailed` / `emailError` |
| v20 | 2026-10-22 | Soupler Labs | §11 v0.8 contract (planned): machines and joining, placement, moving, catalogue, template format, stacks, error codes, validation, dependencies, agent protocol |
| v21 | 2026-10-22 | Soupler Labs | §11 as built: `UpdateMachine.labels` `{}` clears; per-machine metrics (disk only on the main machine); the §11.6 example without a size on a folder volume; the catalogue by name; API tokens are `es_pat_…` (were still `hc_pat_…` after the rename) |
