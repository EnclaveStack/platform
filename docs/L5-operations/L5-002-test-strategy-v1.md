# L5-002 · Test Strategy

---
| Field | Value |
|---|---|
| Document ID | L5-002-test-strategy-v1 |
| Layer | L5 — Operations |
| Status | Draft |
| Version | v16 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-22 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs. All rights reserved. |
| License | MIT |
---

## Purpose

What is tested at which level, with which tool, and what each level proves. Every requirement in `L2-001` – `L2-004` maps to at least one automated test; the playbook (`L5-001`) covers what a human still checks.

## 1 · Levels

| Level | Tool | Where | Proves | Command |
|---|---|---|---|---|
| Unit | `go test` | `internal/domain`, `internal/auth`, `internal/secretbox`, `internal/reconciler` (backoff, classification), `pkg/client` (retry) | Pure rules: validation, phase machine, hashing, encryption, backoff maths, retry policy | `go test ./...` |
| Unit (console) | Vitest | `console/src/**/*.test.ts(x)` | Formatting, filtering, validation mirrors, adapters against the contract; `design-system.test.ts` keeps all styling in `@enclavestack/ui` (ADR-014) | `pnpm --filter @enclavestack/console test` |
| Unit (design system) | Vitest + Testing Library | `packages/ui/src/*.test.ts(x)` | Component behaviour and accessibility (tabs keyboard, dialogs, drawer focus, toasts, copy fallback); token parity light/dark, every used token defined, no colour literals | `pnpm --filter @enclavestack/ui test` |
| Integration — store | `go test` + real SQLite | `internal/store` | Migrations, constraints, transactions, cursor pagination | `go test ./internal/store/...` |
| Integration — API | `go test` + `httptest` + real SQLite + fake driver | `internal/httpapi` | Every endpoint, status code and error code in `L3-002`; idempotency; ETag; scopes; CSRF; rate limits | `go test ./internal/httpapi/...` |
| Integration — reconciler | `go test` + real SQLite + fake driver with fault injection | `internal/reconciler` | Retry with backoff, permanent failure, recovery after restart, drift repair, concurrency of one-in-flight | `go test ./internal/reconciler/...` |
| Integration — Docker driver | `go test -tags docker` + real Docker | `internal/engine/docker` | Containers, volumes, network, health, logs, idempotent ensure, delete; buckets on MinIO | `go test -tags docker ./internal/engine/docker/...` |
| Auth and security | `go test` | `internal/httpapi/auth_test.go`, `security_test.go` | §3 below | `go test ./internal/httpapi/...` |
| CLI | `go test` (in-process server) | `internal/cli` | Commands, flags, env precedence, output formats, exit codes, `--wait`, confirmation | `go test ./internal/cli/...` |
| Provider acceptance | `terraform-plugin-testing` + `tofu` + in-process server | `internal/provider` | Create, read, update in place, replace, import, drift removal, destroy, sensitive values | `TF_ACC=1 go test ./internal/provider/...` |
| E2E — console | Playwright (system Chrome) + `enclave server --driver fake` | `console/e2e` | Real browser through the real server: setup, sign-in, create/edit/delete each kind, tokens, reveal, logout | `pnpm --filter @enclavestack/console e2e` |
| E2E — real engine | `go test -tags e2e` + built binaries + Docker + `tofu` | `tests/e2e` | Postgres accepts SQL, Redis answers PING, objects round-trip through a bucket, an app serves HTTP with its secret env; same through CLI and OpenTofu; kill/restart recovery | `go test -tags e2e ./tests/e2e/...` |
| Load and performance | k6 + `enclave server --driver fake` | `tests/load` | `L5-003` thresholds | `k6 run tests/load/api.js` |

## 2 · Parity matrix (ADR-003)

For every kind in `L2-004` §1, each surface must create, read, update and delete it. The matrix is executed by: console Playwright specs (`console/e2e/<kind>.spec.ts`), CLI tests (`internal/cli/<kind>_test.go`), provider acceptance tests (`internal/provider/resource_<kind>_test.go`). A kind is not done until its row is green in all three.

## 3 · Auth and security tests

Unauthenticated access to every protected route → 401 · read token cannot write → 403 · revoked and expired tokens → 401 · token secret never returned after creation · cookie write without CSRF header → 403 · setup with wrong code → 403 and with a right code once → 201 then 409 · sign-in throttling → 429 with `Retry-After` · unknown email and wrong password give the identical response · session cookie flags (`HttpOnly`, `SameSite=Strict`) · secret value never appears in any response body, event or captured log line · request body over 1 MiB → 413 · non-JSON body → 415 · unknown field → 400.

## 4 · Fault injection

The fake driver accepts per-resource failure scripts (`fail N times transiently`, `fail permanently`, `delay step`) via options in tests and via `ENCLAVE_FAKE_FAULTS` for the binary. Recovery tests stop the server mid-provisioning and start a new one on the same data directory.

## 5 · Quality gates

`go vet` and `staticcheck` clean · `go test -race` for unit and integration · console lint, type-check, tests · coverage reported for `internal/...` (target ≥ 75 % statements; domain, auth and reconciler ≥ 85 %) · SIVE 100 %. CI runs all levels except the real-engine e2e on every PR; the real-engine e2e and the load test run on `develop` pushes and nightly (`L4-002`).

## 6 · Implemented suites (2026-10-20)

| Level | Where | Tests | Result |
|---|---|---|---|
| Unit — contract, domain, auth, crypto, logging | `pkg/api`, `internal/{domain,auth,secretbox,observability}` | 22 | pass |
| Unit — SDK retry, idempotency, error mapping | `pkg/client` | 6 | pass |
| Integration — store (real SQLite) | `internal/store` | 10 | pass |
| Integration — API + auth/security (real HTTP + SQLite, fake engine) | `internal/httpapi` | 22 (incl. OpenAPI parity) | pass |
| Integration — reconciler resilience | `internal/reconciler` | 17 | pass (timing-sensitive ones 10× under `-race` and load) |
| CLI (built-in server) | `internal/cli` | 7 | pass |
| Provider acceptance (real `tofu` 1.12.6) | `internal/provider` | 5 | pass |
| Docker driver (real Docker 29.7.2) | `internal/engine/docker` | 6 | pass |
| Server, console assets, contracts, architecture | `internal/{server,webui,contracts,archtest}` | 7 | pass |
| E2E — built binaries + Docker + OpenTofu + SIGKILL recovery + drift | `tests/e2e` | 3 | pass |
| Console unit and flows (Vitest + Testing Library), incl. design-system guard | `console/src` | 121 | pass |
| Design system components and tokens | `packages/ui/src` | 21 | pass |
| Console e2e (Playwright, real Chrome → real server) | `console/e2e` | 10 | pass |
| Load (k6) | `tests/load` | 2 scenarios, 4 thresholds | pass (`L5-003`) |

Coverage across packages (`-coverpkg=./internal/...,./pkg/...`, with provider acceptance and the Docker suite): **81.3 %** of statements. Domain 96.9 %, auth 94.3 %, store 88.5 %, httpapi 87.0 %, reconciler 87.8 %, service 81.9 %, provider 81.8 %, Docker driver 73.2 %, `pkg/client` 91.9 %, `pkg/api` 90.7 %. The reconciler first measured 84.9 %, under its 85 % target; the gap was error branches (unreadable secrets, delete and restart failures, message truncation, bucket drift, drift while the engine is down). Fault-injection tests for each (`internal/reconciler/faults_test.go`) raised it to **87.8 %**.

**Flakes are bugs.** A test that fails only under load is fixed (made deterministic), never retried or skipped: `TestAttemptsRunOutThenFailed` raced its own fixture against an in-flight retry and was rewritten to edit state only inside a guaranteed quiet window.

## 6 · v0.3 suites (planned)

| Suite | Level | What it proves | Command |
|---|---|---|---|
| Domains on the fake engine — **shipped** | Integration (real API + SQLite): `internal/reconciler/domains_test.go` (3); CLI `TestDomainsAndMachineSettingsFromTheCLI`; provider `TestAccDomainCertificateImportReplaceAndMachineSettings`; console (`mock.test.ts`, `http.test.ts`, `validation.test.ts`, `codegen.test.ts`, `App.test.tsx`) and Playwright | Routes use the app alias; uniqueness, in-use, port, immutability and hostname rules; loopback guard; DNS-waiting message; settings; port conflict; drift repair; CA | `go test ./...`, `pnpm test`, `make console-e2e` |
| Rebuild migration — **shipped** | Integration (real SQLite): `internal/store/migrate_test.go` | A v0.2 database keeps every row, event and reference through `0003`; foreign keys still enforced; CHECK violations are errors, never `NAME_TAKEN` | `go test ./internal/store` |
| Edge with internal TLS — **shipped** | Integration (real Docker + Caddy): `internal/engine/docker/edge_test.go` | Internal certificate trusted via the root CA; 308 redirect with port; TLS-off route; `X-Forwarded-*`; 404 for unknown hosts; **0 failed of 16,192 HTTPS requests during a rolling deploy**; port conflict permanent and reported; stop with no routes | `make engine-docker` |
| ACME issuance — **shipped** | Integration (Pebble 2.8 + challtestsrv + Caddy): `internal/engine/docker/acme_test.go` | `auto` TLS obtains a certificate over HTTP-01 without the public internet | `make engine-docker` |
| Rolling deploy — **shipped** | Integration (real Docker): `internal/engine/docker/app_test.go` (3 tests); reconciler on the fake engine: `internal/reconciler/revisions_test.go` (2); CLI (`TestHealthCheckAndStrategyFlags`); provider acceptance (`TestAccAppHealthCheckStrategyAndFailedRevision`); console (`App.test.tsx`, `validation.test.ts`, `codegen.test.ts`) | New revision healthy before the switch; failed revision discarded with the probe result while the serving one keeps answering; numbers never reused; published apps recreate and keep their host port; drift restarts the serving revision; v0.2 containers adopted | `make engine-docker`, `go test ./...` |
| Zero-drop deploy | Load (k6) | 0 failed requests across 10 consecutive rolling deploys at 200 req/s | `tests/load/run.sh deploy` |
| Scheduler and jobs — **shipped** | Integration (real API + SQLite, fake engine): `internal/reconciler/jobs_test.go` (4, stable over 8 race runs); store `runs_test.go`, `migrate_test.go`; CLI `TestJobsFromTheCLI`; provider `TestAccJobScheduleImportAndValidation` (time zones checked through `next_run_at`); console `schedule.test.ts`, `validation.test.ts`, `codegen.test.ts`, `mock.test.ts`, `App.test.tsx`; Playwright | Manual runs with output, exit codes, events; timeout; overlap (skip and 409); app inheritance with overrides; validation codes; scheduled claim and next-run advance; catch-up as one skipped record plus one run; adoption starts a run exactly once; delete removes runs; runs pagination; output needs write access; text order of timestamps | `go test ./...`, `pnpm test`, `make console-e2e` |
| Jobs on real Docker — **shipped** | `internal/engine/docker/job_test.go`; built-binary e2e (`tests/e2e`: an app-based job sees the app's secret) | Output (stdout and stderr), env, exit codes, timeout kill, idempotent start, 64 KiB tail keeps the end, deleting a job stops its runs | `make engine-docker`, `make e2e` |
| Backups and restore — **shipped** | Integration (real API + SQLite, fake engine, in-memory storage): `internal/reconciler/backups_test.go` (5: stream, retain, restore new/replace, safety backup, checksum refusal, references, delete keeps objects, three failures → `failed`, per-backup retention, expired runs, safety backup never evicts the restored run, failed replace recoverable by Retry); `internal/backup` (keys, retention ownership, hashing); `pkg/api` validation tables; HTTP `TestBackupRunsAndRestoreOverHTTP`; CLI `TestBackupsFromTheCLI`; provider `TestAccBackupDestinationsImportAndReplace`; console `validation`, `codegen`, `mock`, `http` and `App` tests, plus a Playwright scenario | Every BAK rule on every surface; each regression test was checked to fail without its fix | `go test ./...`, `pnpm test`, `make provider-acceptance`, `make console-e2e` |
| Backups on real engines — **shipped** | `internal/engine/docker/backup_test.go`; built-binary e2e (500 Postgres rows → object store → new database) | `pg_dump` streamed with matching SHA-256; 1,000 rows restored new and over existing data; Redis restored through the AOF and surviving a restart; a 40 MiB upload that fails halfway leaves no object or multipart upload | `make engine-docker`, `make e2e` |
| Builds — **shipped** | Unit and integration: `internal/build` against a real Git HTTP server (`git http-backend`, shared as `internal/build/gittest`): branches, annotated and lightweight tags, commits, tokens, a missing repo or ref, unreachable hosts, `.dockerignore` via `fsutil`, symlink escapes, inputs hash. Store `builds_test.go`; reconciler `builds_test.go` (3: build, skip, rebuild, two images kept, drift reuses the image, delete removes images; failed build keeps the serving revision and Retry rebuilds; credentials refs, refused credentials, jobs use the built image); `pkg/api` validation; HTTP `TestBuildLogsOverHTTP`; CLI `TestBuildsFromGitThroughTheCLI`; provider `TestAccAppFromGitBuildsImportsAndSwitches`; console `validation`, `codegen`, `mock`, `http` and `App` tests, and Playwright | Every BLD rule on every surface | `go test ./...`, `pnpm test`, `make provider-acceptance`, `make console-e2e` |
| Builds on real engines — **shipped** | `internal/engine/docker/build_test.go` (real BuildKit); built-binary e2e `TestAppBuiltFromGitServesItsCommit` | Build arguments, cache mounts, a 2 MiB context through the session with an exclude, readable step log, a failing step permanent and named, the built image deploys without a pull, a missing built image is never pulled, clean-up keeps images in use; a private repository through the CLI, redeploy serves the new commit, a broken push keeps the old one serving, no token in the logs | `make engine-docker`, `make e2e` |
| Three-surface parity — **shipped for domains, jobs, backups** | Console Playwright, CLI, provider acceptance | `domain`, `job` and `backup` CRUD from every surface (`L5-002` §2 matrix) | as in §2 |

## 7 · v0.4 suites (shipped 2026-10-21)

All rows below are implemented and green on 2026-10-21: `go test -race ./...`; `make engine-docker` (the whole real-Docker driver suite, 142 s); `make e2e` (95 s, including `TestNetworkingStackForRealWithOpenTofu`); `make provider-acceptance` (with `TestAccNetworkingStack`); console Vitest 182 tests; Playwright 13 steps; design system 22. The k6 load balancer scenario runs on real Docker (`make load-lb`). The DNS provider suites use local fakes; nothing runs against the live Cloudflare or Route 53 APIs.

| Suite | Level | What it proves | Command |
|---|---|---|---|
| Validation | Unit: `pkg/api/validation_test.go` | CIDR canonical form, private ranges, sizes, subnet containment and overlap; firewall rule grammar; load balancer targets; record values per type; wildcard hostnames only with a zone | `go test ./pkg/api` |
| Networks on the fake engine | Integration (real API + SQLite): `internal/reconciler/networks_test.go`; CLI; provider acceptance; console (mock and Playwright) | Create, place, move, refuse delete in use, overlaps, immutable fields, `privateIp` / `privateDns` | `go test ./internal/reconciler`, `make provider-acceptance`, `pnpm test` |
| Networks on real Docker | `internal/engine/docker/network_test.go` | Isolation between networks, subnet addresses (static and automatic), `.internal` names, system services joined, internet off, database move keeps data | `make engine-docker` |
| Firewall rendering | Unit: `internal/firewall` | Policy union, implicit allows, egress deny, deterministic rulesets, nothing but validated values in the output | `go test ./internal/firewall` |
| Firewalls on real Docker | `internal/engine/docker/firewall_test.go` | Allowed and blocked connections (tcp, udp, icmp), replies allowed, egress deny, re-apply after restart, a new revision protected before traffic (the window measured), sources followed across a redeploy | `make engine-docker` |
| Replicas and load balancers | Integration: `internal/reconciler/lb_test.go`; real Docker `lb_test.go`, `app_test.go` | N-replica rolling deploys; edge spread across replicas; HAProxy config check, graceful reload, discovery within 2 s, unhealthy replica skipped, `healthyTargets` | `go test ./internal/...`, `make engine-docker` |
| Zero-drop through a load balancer | Load (k6) on real Docker | 0 failed requests across rolling deploys through a load balancer at 200 req/s; measured 0 of about 36,000 over nine deploys | `make load-lb` (`tests/load/lb.sh`) |
| DNS providers | Unit and integration: `internal/dns/cloudflare`, `internal/dns/route53` against local fakes of each API (written from the vendors' API references); SigV4 signatures checked against AWS's published test vectors | List, upsert, delete, ownership comments, adoption, conflicts, pagination, `429` with `Retry-After`, `401`/`403` | `go test ./internal/dns/...` |
| Records and zones | Integration (fake engine, test DNS provider): `internal/reconciler/records_test.go` | Lifecycle, machine records following `publicAddress`, drift repair, CP-12 | `go test ./internal/reconciler` |
| DNS-01 | `internal/acme` with Pebble 2.8 and challtestsrv in Docker; real Docker `acme_dns_test.go` | An order through the test provider, wildcard issuance, challenge records removed, renewal, keys encrypted in the store, the edge serving a loaded certificate | `make engine-docker` |
| Built-binary e2e | `tests/e2e` | A network with a firewall-protected database used by a three-replica app behind a load balancer, created by OpenTofu and checked from the CLI | `make e2e` |
| Three-surface parity | Console Playwright, CLI, provider acceptance | Every v0.4 kind from every surface (§2 matrix) | as in §2 |

## 8 · v0.5 suites (shipped 2026-10-21)

All rows below are implemented and green on 2026-10-21 (Apple M4, Docker Desktop 29.7.2, QEMU 11.1.2): `go test -race ./...`; `make engine-docker` (173 s); `make engine-qemu` (262 s, real boots); `make e2e` (274 s, including `TestComputeStackForRealWithOpenTofu` with a VM); `make provider-acceptance` (67 s, with `TestAccComputeStack`); console Vitest 194 tests; Playwright 14 steps; design system 22. The CI job that runs `make engine-qemu` on Linux with KVM was added the same day and hasn't run yet (`L4-002` §4).

| Suite | Level | What it proves | Command |
|---|---|---|---|
| Validation | Unit: `pkg/api/validation_test.go` | Images (catalog, URL, snapshot), SSH key formats, user-data size and header, CPUs and memory against the machine, grow-only sizes, mounts, disk name prefixes, publish rules | `go test ./pkg/api` |
| Seeds, QMP, images | Unit: `internal/engine/qemu` | The seed ISO read back (label `cidata`, Rock Ridge names, multi-part user-data with the `enclave` user), the QEMU command line per architecture and accelerator, QMP framing against a scripted fake monitor, the catalog's pinned URLs and checksums, socket-path fallback and its ownership check | `go test ./internal/engine/qemu` |
| VMs, volumes and snapshots on the fake engine | Integration (real API + SQLite + fake driver and hypervisor): `internal/reconciler/vms_test.go`, `volumes_test.go`, `snapshots_test.go`; CLI; provider acceptance; console (mock and Playwright) | Lifecycle, power actions, guest shutdown vs crash, adoption after restart, VMs pending while QEMU is missing, mounts and deploys, attachment rules, CP-12, restores, interrupted copies | `go test ./internal/reconciler`, `make provider-acceptance`, `pnpm test` |
| Link helper | Unit: `internal/engine/docker/vmlink` (frame codec, checksum repair against packets with known sums, DNS forwarding) | Frames and checksums byte-exact | `go test ./internal/engine/docker/vmlink` |
| Volumes and link containers on real Docker | `internal/engine/docker/volume_test.go`, `vmlink_test.go` | Shared mounts across replicas and revisions, archive snapshot and restore; a link container with a fake QEMU peer: DNAT, masquerade, DNS, gateway-only frame port, firewall `forward` chain, internal network through `es-vmlink` | `make engine-docker` |
| Real QEMU | `internal/engine/qemu/*_test.go` behind the `qemu` build tag; skipped with the reason when QEMU or acceleration is missing | Boot from the catalog image, seed applied, SSH, power, crash restart, adoption, live resize, `drive-backup` snapshot without a stall over 1 s, VM from a snapshot, networks end to end with a link container | `make engine-qemu` |
| Built-binary e2e | `tests/e2e` | A VM with a data disk in a firewalled network, an app with a filesystem volume, and snapshots of both restored into new resources, created by OpenTofu and checked from the CLI and over SSH | `make e2e` |
| Three-surface parity | Console Playwright, CLI, provider acceptance | Every v0.5 kind from every surface (§2 matrix) | as in §2 |

CI runs `make engine-qemu` on a Linux runner with `/dev/kvm` (`L4-002`); macOS HVF is covered by the walk-throughs in `L5-001` §2c.

## 9 · v0.6 suites (shipped 2026-10-22)

| Suite | Level | What it proves | Command |
|---|---|---|---|
| Validation | Unit: `pkg/api` | Engines and versions, queue and topic limits, subscription targets and URLs, transcoder folders and presets, CDN origins and hostnames, email relay and senders, message and email bodies | `go test ./pkg/api` |
| Broker semantics | Integration: `internal/messaging/natsbroker` against an embedded NATS server; the same table of cases against `internal/messaging/memory` | Visibility timeout and redelivery, receive counts, max receives and dead letters, deduplication, retention, peek, purge, permissions per user, topic fan-out with `DeliverNew` | `go test ./internal/messaging/...` |
| Kinds on the fake engine | Integration (real API + SQLite + fake driver, memory broker and mail): `internal/reconciler`, `internal/httpapi`; CLI; provider acceptance; console (mock and Playwright) | Lifecycle of every new kind, data endpoints and their access, deliveries and retries to a test webhook, dead letters, purge, transcoder watch and runs, CDN routes in the edge configuration, CP-12 | `go test ./...`, `make provider-acceptance`, `pnpm test` |
| Real Docker | `internal/engine/docker/*_test.go` | MySQL, MongoDB and ClickHouse up, dumped and restored; the broker's config reload and users; OpenSearch auth; Mailpit capture and relay to a local SMTP sink; nginx caching, stale and purge; an FFmpeg run producing every preset from a generated clip | `make engine-docker` |
| Built-binary e2e | `tests/e2e` | The shop of `L2-008` §1 created by OpenTofu and exercised from the CLI, a NATS client in an app container, a webhook, the CDN and the bucket (`TestDataAndMessagingStackForRealWithOpenTofu`, 50 s) | `make e2e` |
| Requirement coverage | `internal/reconciler` (`TestDataServicesJoinNetworksAndFirewalls`, `TestTranscoderConcurrencyBoundsManualRuns`), `natsbroker` (`TestRetentionIsTheStreamsMaximumAge`), `internal/cli` (`TestEveryKindHasAGenericName`) | Placement and firewalls for every engine and service; transcoder concurrency; retention as the streams' age; every kind named by the generic verbs. These came from listing the `L2-008` requirements no test named, and two of them found bugs | `go test ./...` |

**Results (2026-10-22, Apple M4, Docker Desktop 29.7.2):** all green.
- Go with `-race`.
- Console: 208 tests. Design system: 22 tests. Playwright: 15 journeys, including a v0.6 journey (MySQL, a queue fed by a topic, email).
- The real-Docker driver suite (about 5 minutes) and the built-binary e2e.
- A v0.4 firewall test was made deterministic. A redeploy reports ready just before it re-syncs firewalls, and a restart in that gap lost the event the test waited for.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler Labs | Levels, tools, parity matrix, auth/security list, fault injection, gates |
| v2 | 2026-10-20 | Soupler Labs | Implemented suites, counts and measured coverage; flake policy |
| v3 | 2026-10-20 | Soupler Labs | Design system suite (`packages/ui`) and the console styling guard (ADR-014) |
| v4 | 2026-10-20 | Soupler Labs | §6 v0.3 suites (planned): edge, ACME via Pebble, rolling deploy, zero-drop load test, scheduler, jobs, backups and restore, builds, parity |
| v5 | 2026-10-21 | Soupler Labs | Phase 1 suites shipped (rolling deploy across driver, reconciler, CLI, provider, console); console count 72 |
| v6 | 2026-10-21 | Soupler Labs | Phase 2 suites shipped: domains, rebuild migration, real edge, ACME via Pebble; console 86, Playwright 9 |
| v7 | 2026-10-21 | Soupler Labs | Phase 3 suites shipped; console 121, Playwright 10 |
| v8 | 2026-10-21 | Soupler Labs | Phase 4 suites shipped; console 141, design system 22, Playwright 11 |
| v9 | 2026-10-21 | Soupler Labs | Phase 5 suites shipped; console 156, design system 22, Playwright 12 |
| v10 | 2026-10-21 | Soupler Labs | §7 v0.4 suites (planned): validation, networks, firewalls, replicas and load balancers, DNS providers against local fakes, records, DNS-01 via Pebble, e2e, parity |
| v11 | 2026-10-21 | Soupler Labs | §7 suites shipped; counts: console 182, Playwright 13, real-Docker driver suite and built-binary e2e green |
| v12 | 2026-10-21 | Soupler Labs | Zero-drop through a load balancer built and measured (tests/load/lb.sh, make load-lb) |
| v13 | 2026-10-21 | Soupler Labs | §8 v0.5 suites (planned): validation, seeds and QMP, fake-engine integration, link helper, real Docker, real QEMU, e2e, parity |
| v14 | 2026-10-21 | Soupler Labs | §8 suites shipped and green, with timings |
| v15 | 2026-10-21 | Soupler Labs | §9 v0.6 suites (planned) |
| v16 | 2026-10-22 | Soupler Labs | §9 suites shipped and green, with results; requirement-coverage suite; firewall test race fixed |
