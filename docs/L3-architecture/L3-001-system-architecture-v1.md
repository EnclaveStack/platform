# L3-001 · System Architecture

---
| Field | Value |
|---|---|
| Document ID | L3-001-system-architecture-v1 |
| Layer | L3 — Architecture |
| Status | Draft |
| Version | v17 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-22 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs |
| License | Apache-2.0 |
---

## Purpose

The map of platform: components, boundaries, data ownership, runtime flows and the decisions that justify them. Update this whenever a boundary or diagram stops being true (L0-006 §3.3). Decisions: ADR-001 – ADR-002 below; ADR-003 onward in `L3-003`, with the option analyses behind them in `L3-004`.

## 1 · Shape of the system

EnclaveStack is **one Go binary** (`enclave`) that is both the control plane (`enclave server`) and its CLI, plus a second binary built from the same module, the OpenTofu provider (`terraform-provider-enclave`). Every surface is a client of one HTTP API (ADR-003).

```
 Browser (console SPA) ─┐                    ┌──────────────── enclave server (one process) ────────────────┐
 enclave CLI ─────────┼── HTTPS/HTTP ──▶   │ httpapi: router · middleware (request id, log, recover, auth,  │
 OpenTofu provider ─────┘   /api/v1 (JSON)   │          CSRF, rate limit, body limit) · handlers · console     │
   (all via pkg/client                       │ service: resources · auth · tokens  ── validation (domain)      │
    or the console's                         │ reconciler: scheduler · workers · backoff · drift  ─┐           │
    http adapter)                            │ store: SQLite (WAL)  ◀───────────────────────────────┤           │
                                             │ secretbox: AES-256-GCM (master key file)             │           │
                                             │ engine.Driver ── docker driver ──▶ Docker Engine API ─┴─▶ containers, volumes,
                                             │               └─ fake driver (tests, load, demos)          network "enclave"
                                             │ observability: slog JSON · /metrics · /healthz · /readyz        │
                                             └──────────────────────────────────────────────────────────────────┘
```

## 2 · Components and boundaries

| Component | Path | Owns | May call | Must not touch |
|---|---|---|---|---|
| Wire contract | `pkg/api` | Request/response types, kinds, phases, error codes — the single definition (L0-003 rule 1) | — | Anything internal |
| Go client SDK | `pkg/client` | HTTP client with auth, retries, idempotency keys, typed errors | The API | Server internals |
| CLI | `internal/cli`, `cmd/enclave` | Commands, config file, output formats, exit codes (`L2-002`) | `pkg/client`; `internal/server` only for `enclave server` | Store, engine |
| OpenTofu provider | `internal/provider`, `cmd/terraform-provider-enclave` | Resource schemas, plan modifiers, waits (`L2-003`) | `pkg/client` | Store, engine |
| Console | `console/` | Operator UI (`L2-001`): behaviour and copy only | The API through `EnclaveApi` (ADR-002); `@enclavestack/ui` for every visual | Everything else; its own stylesheets, inline styles or class names (ADR-014) |
| Design system | `packages/ui` (`@enclavestack/ui`) | Tokens (light/dark), typography, components and application layout (`L2.5-001`, `L2.5-002` §9) | React, lucide icons, Fontsource fonts | The API, routing, product copy |
| Edge proxy *(v0.3: shipped)* | `es-system-edge` container (Caddy); rendered by `internal/engine/docker/edge.go` | Routes for domains, TLS, certificates (ADR-015) | Configured only by the reconciler via engine copy + exec | The store |
| Scheduler *(v0.3: jobs and backups shipped)* | `internal/reconciler/scheduler.go`, `internal/reconciler/backups.go` | Due-run claiming, catch-up, adoption of running runs, job and backup execution, restores, run history (ADR-017, ADR-018) | `store`, engine driver (`StartJob`, `WaitJob`), secrets | Handlers |
| Backup storage *(v0.3: shipped)* | `internal/backup` | Streaming uploads to a EnclaveStack bucket or external S3 with SHA-256, object keys, per-backup retention, checksum-verified reads; an in-memory store for the fake engine (ADR-018) | minio-go | The store, the engine |
| Probe helper *(v0.3: shipped)* | `es-system-probe` container | HTTP/TCP health checks from inside the `enclave` network (ADR-016) | Engine exec | — |
| Builder *(v0.3: shipped)* | `internal/build`, `internal/reconciler/builds.go`, the driver's `Build` | Resolving refs and checkouts (go-git), checking the build context and `.dockerignore`, inputs hashes and image names (`internal/build`, which imports nothing of the control plane); building, skipping, build history and image clean-up (reconciler); BuildKit with the context over the build session (driver) (ADR-019) | go-git, BuildKit session, secrets | Handlers, the store (from `internal/build`) |
| Networks *(v0.4: built)* | The driver's network methods; `es-net-<name>` bridge networks | Address layout, subnet address choice, private DNS aliases, connecting system services to every network (ADR-020) | Engine API | The store |
| Firewall *(v0.4: built)* | `internal/firewall` (policy resolution and nftables rendering, imports nothing of the control plane), `internal/reconciler/firewalls.go`, the driver's `ApplyFirewall` | Turning firewalls into one policy per target, re-applying on deploys and drift (ADR-021) | Engine API (one-off `es-netfilter-*` helpers) | Handlers |
| Load balancers *(v0.4: built)* | `es-loadbalancer-<name>` containers (HAProxy); rendered by `internal/lb` | Listeners, backends, health checks, replica discovery (ADR-022) | Configured only by the reconciler via engine copy + signal | The store |
| DNS providers *(v0.4: built)* | `internal/dns` (Cloudflare, Route 53, a test provider), `internal/reconciler/records.go` | Zones, records, ownership and conflicts (ADR-023) | Provider HTTP APIs | The store, the engine (from `internal/dns`) |
| ACME DNS-01 *(v0.4: built)* | `internal/acme`, `internal/reconciler/certificates.go` | Orders, challenge records, the certificates table, renewal (ADR-024) | lego core, `internal/dns`, `secretbox` | Handlers |
| Hypervisor *(v0.5: built)* | `internal/engine/qemu` behind the `engine.Hypervisor` port; one QEMU process per VM under `<data dir>/vm/<id>/` | Requirements check, images and their cache, NoCloud seeds, QEMU processes, QMP monitors, power, adoption after restarts, disk files, disk snapshots (ADR-025, ADR-027, ADR-028) | QEMU (`qemu-system-*`, `qemu-img`) on the host, QMP | The store, handlers |
| VM link *(v0.5: built)* | `es-vm-<name>` containers running `es-vmlink` (`internal/vmlink`, built into its image from its own source; driven by `internal/engine/docker/vmlink.go`); rendered by the driver | A VM's place in its network: private IP and DNS name, frames between QEMU and a tap, DNS forwarding, checksum repair, published and SSH ports (ADR-026) | Engine API; QEMU connects to it over a loopback port | The store |
| Volumes and snapshots *(v0.5: built)* | `internal/reconciler/volumes.go`, `disks.go`, `snapshots.go`, `vms.go`, the driver's volume methods, the hypervisor's disk methods | Filesystem volumes (engine volumes), disk volumes (qcow2), mounts, attachment, snapshot copies and restores (ADR-027, ADR-028) | Engine API (archive copy), hypervisor | Handlers |
| Message broker *(v0.6: planned)* | `es-system-broker` container (NATS JetStream), configured by the driver's `ConfigureBroker`; `internal/messaging` (the `Broker` port, `natsbroker`, `memory`) | Users and permissions per queue and topic, streams, consumers, dead-letter capture (ADR-029) | NATS client protocol over the broker's loopback port | The store (from `internal/messaging`) |
| Delivery loop *(v0.6: planned)* | `internal/reconciler/deliveries.go` | Topic subscriptions to queues and webhooks, retries, dead letters, delivery counters (ADR-029) | `internal/messaging`, HTTP to webhooks, the store | Handlers |
| Mail client *(v0.6: planned)* | `internal/mail` (SMTP and Mailpit's API; an in-memory stand-in) | Sending, listing and reading an email service's messages (ADR-032) | The email container's loopback ports | The store, the engine |
| Monitor *(v0.7: planned)* | `internal/monitor` (sampler, alert evaluator, notifier, log shipper) | Metric points and rollups, alert state and incidents, the notification outbox, log export checkpoints (ADR-037 – ADR-040) | Read-only engine and hypervisor methods (`Stats`, `LogsSince`, edge metrics), `internal/messaging` stats, `internal/mail`, HTTP to channels and log destinations, the store | Handlers; state-changing engine methods |
| Secret stores *(v0.7: planned)* | `internal/secretstores` (Vault, 1Password Connect, AWS Secrets Manager, Doppler, Infisical clients); `internal/sigv4` (shared with Route 53) | Fetching outside secret values, used by the reconciler (ADR-041) | HTTPS to the providers | The store, the engine |
| Authorization *(v0.7: planned)* | `internal/service` (`Principal.Require`), `internal/store` (projects, members, invites, audit) | Roles per project and machine, token limits, invites, audit (ADR-036) | The store | — |
| Agent *(v0.8: planned)* | `internal/agent` (server on the other machine, `agent.Remote` client on the main one), `enclave agent` | Serving one machine's Docker driver and hypervisor over mutual TLS; joining; certificate renewal; uploads; sweeping (ADR-042, ADR-043) | The machine's own Docker Engine and QEMU | The store (it has none) |
| Engine router *(v0.8: planned)* | `internal/engine/cluster` | Sending each engine call to the machine of its resource; networks to every machine; `ErrMachineOffline`; announcing new containers to the mesh (ADR-044) | In-process drivers for `main`, `agent.Remote` for the others | The store (it reads machines through a port) |
| Machines *(v0.8: planned)* | `internal/machines` (health monitor, CA and join codes, placement queries), `internal/store` machines and address pools | Machine state, identity, health checks, offline detection, choosing a machine (ADR-043, ADR-044, ADR-046) | The router, the store | Handlers |
| Mesh *(v0.8: planned)* | `es-system-mesh` and `es-standin-*` containers running `es-mesh` (`internal/mesh`, built into its image from its own source); configured by the driver's `ConfigureMesh` from the reconciler's mesh sync | WireGuard peers, routes and proxy neighbours for remote addresses, isolation rules, stand-ins for remote names, address pools (ADR-045) | Engine API; WireGuard between machines | The store |
| Catalogue *(v0.8: planned)* | `internal/catalogue` (template parsing, placeholders, built-in templates), `internal/service` (stacks) | Templates, rendering, members of stacks (ADR-048) | Nothing: pure; the service validates and stores what it renders | The engine |
| Engine services *(v0.6: planned)* | The driver's database plans (MySQL, MongoDB, ClickHouse), `es-search-<name>` (OpenSearch), `es-email-<name>` (Mailpit), `es-cdn-<name>` (nginx), transcode runs (FFmpeg) | Containers, their configuration files, dumps and restores, cache purges, transcodes fed through the archive API (ADR-030 – ADR-034) | Engine API | The store |
## 3 · Data ownership

*Reconstructed 2026-10-21 from the code and `L3-005`; the original text of this section was lost (see Version history).*

| Data | Owner | Where | Everyone else |
|---|---|---|---|
| Resources, references, events, runs, builds, certificates, users, sessions, tokens, settings, idempotency records | `internal/store` | `<data dir>/enclave.db`, SQLite in WAL mode (ADR-005, `L3-005`) | Services and the reconciler use the store's methods; no other package opens the database or writes SQL |
| Desired state (`spec`, `generation`) | `internal/service` | `resources` | The reconciler reads it; only the service changes it, inside one transaction with its references and events |
| Observed state (`phase`, `steps`, `outputs`, `serving_revision`) | `internal/reconciler` | `resources` | Services only read it; `SaveStatus` refuses a write for a generation that has moved on |
| Secret values, certificate keys, the ACME account key | `internal/secretbox`, with `master.key` (32 random bytes, 0600) | `secret_ciphertext`, `certificates.key_ciphertext`, `settings` | Only the reconciler decrypts, at the moment a driver or provider needs the value; nothing is logged (`L3-006`) |
| Containers, volumes, networks, built images | The engine driver | Docker objects labelled `enclave.managed`, `enclave.resource-id` | Found by label, never by name alone; nothing else creates or removes them (`internal/archtest`) |
| Backup objects | `internal/backup` | A EnclaveStack bucket or external S3 | Restores read them back and verify their SHA-256 first |
| DNS records | `internal/dns` through the reconciler | The zone's provider | A record is changed or deleted only once EnclaveStack owns it (ADR-023) |
| CLI credentials | `internal/cli` | `~/.config/enclave/config.json` (0600) | Environment variables override it (`L2-002` CLI-02) |
| Console preferences (theme) | `console/` | The browser's `localStorage` | Nothing on the server |

## 4 · Runtime flows

*Reconstructed 2026-10-21 from the code; the original text of this section was lost (see Version history).*

1. **A change.** A client calls the API (`internal/httpapi`: request ID, auth, CSRF, rate limit, body limit). The service validates the spec with `pkg/api` and resolves its references. It writes the resource, references and event in one transaction, answers `202`, and enqueues the resource (ADR-004).
2. **Reconcile.** A reconciler worker claims the resource and resolves what the driver needs: secrets, placement, firewall policy and load balancer targets. It calls the driver, which inspects first and changes only what is missing or outdated, and records outputs, steps and phase. Transient errors retry with backoff, permanent ones fail with the reason, and an unreachable engine waits (ADR-012, `L3-007`).
3. **After a deploy.** An app that became ready re-renders the edge when domains use it, reconfigures or refreshes its load balancers, and re-applies the firewalls that name it (ADR-015, ADR-021, ADR-022).
4. **Drift.** Every 60 s the drift pass checks ready resources against the engine and sends missing or stopped ones back to provisioning. It also reloads lost firewall rules, refreshes certificate and load balancer facts, and compares DNS records with their provider at most every 10 minutes (CP-10).
5. **Schedules.** Every second the scheduler claims due job and backup runs and adopts runs that are still `running`, so a run starts once even across a crash (ADR-017, ADR-018).
6. **Recovery.** On start, the reconciler resumes every resource still in progress (CP-09). Interrupted app deploys are discarded and started again (ADR-016).
7. **Delete.** The service refuses while references exist (CP-12); otherwise the reconciler removes the engine objects, then the rows and the resources the deleted one owns.
8. **Many machines (v0.8).** The reconciler, services and monitor call one `engine.Driver` and `engine.Hypervisor`: the router. The router sends each call to the machine of its resource, through `agent.Remote` over mutual TLS for every machine but the main one. Every 5 s, and after a placed resource's containers change, the mesh sync gives each machine its tunnel peers, address pools and stand-ins. The machines monitor calls each agent every 10 s, marks machines offline, and has apps restarted elsewhere (ADR-042 – ADR-046).

## 5 · Architecture Decision Records

ADR-001 and ADR-002 are recorded here; ADR-003 onward are in `L3-003`, with their option analyses in `L3-004`.

*Reconstructed 2026-10-21: the original text of these two records was lost. They are rewritten from DAR-001, the code and the references to them across the repository. Their decisions are the ones the code has followed since v0.1.*

### ADR-001 — Go for the control plane, the CLI and the OpenTofu provider
- **Status**: Accepted (DAR-001)
- **Context**: EnclaveStack ships three programs: the control plane, the CLI and an OpenTofu provider. Each must install as a single file on macOS and Linux (amd64 and arm64), and run next to the user's own workloads.
- **Decision**: One Go module (`go.mod`, Go ≥ 1.26) holds all three. `cmd/enclave` is both the server (`enclave server`) and the CLI (ADR-008); `cmd/terraform-provider-enclave` is the provider. They share the wire contract in `pkg/api` and the client in `pkg/client`. The provider requirement settles the choice: `terraform-plugin-framework` exists only in Go.
- **Consequences**: static binaries, built by `make build` into `build/`; native libraries for Docker, MinIO, Prometheus and OpenTofu; a resident footprint measured in `L5-003`. The console is the one TypeScript part, and it receives the contract generated from Go (ADR-002).

### ADR-002 — The console reaches the control plane only through `EnclaveApi`, with an http and a mock adapter
- **Status**: Accepted
- **Context**: The console came first (v0.1), before the control plane existed. It had to be built and tested without a server, and later switch to the real API without any component changing.
- **Decision**: Components get one interface, `EnclaveApi` (`console/src/api/client.ts`), from `useApi()`, and never call `fetch` themselves. Two adapters implement it:
  - `http` speaks the contract of `L3-002`;
  - `mock` keeps everything in memory, simulates the reconciler and follows the same rules and errors.

  `VITE_ENCLAVE_API` chooses the adapter: `mock` by default in development, `http` in production builds. Kinds, phases, error codes and validation rules are not written in TypeScript by hand: `cmd/gencontracts` generates `console/src/api/generated.ts` from `pkg/api`, and `internal/contracts` fails when that file is stale.
- **Consequences**:
  - UI tests and offline demos run against the mock; Playwright runs against the real server (`L5-002`).
  - Every contract change touches `pkg/api`, `api/openapi.yaml`, the generated file, both adapters and the handlers in one PR (`L3-002`), and `console/src/api/mock.test.ts` holds the mock to the server's contract.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Initial skeleton from soupler-governance-template v1.2.1 (row restored from the scaffold commit `b7a06f7`) |
| v2 – v5 | 2026-10-20 | Soupler Labs | The v0.2 architecture: shape of the system, components and boundaries, data ownership, runtime flows, ADR-001 – ADR-002; v4 was current at the v0.2 release (CHANGELOG). Row reconstructed: the individual summaries were lost |
| v6 | 2026-10-20 | Soupler Labs | v0.3 components (planned): edge proxy, scheduler, probe helper, builder (ADR-015 – ADR-019) |
| v7 | 2026-10-21 | Soupler Labs | Scheduler lives in the reconciler package (jobs shipped) |
| v8 | 2026-10-21 | Soupler Labs | Backups shipped: internal/backup and the restore path in the reconciler; probe helper shipped |
| v9 | 2026-10-21 | Soupler Labs | Builder shipped |
| v10 | 2026-10-21 | Soupler Labs | v0.4 components (planned): networks, firewall, load balancers, DNS providers, ACME DNS-01 (ADR-020 – ADR-024). Restored the "Version history" heading and table header, lost in an earlier edit together with sections 3 onward (data ownership, runtime flows, ADR-001 – ADR-002) and the v1 – v5 rows; they are not reconstructed here |
| v11 | 2026-10-21 | Soupler Labs | v0.4 components built |
| v12 | 2026-10-21 | Soupler Labs | Reconstructed the lost sections 3 – 5 (data ownership, runtime flows, ADR-001, ADR-002) from DAR-001, the code and references across the repository, and the lost v1 – v5 history; each reconstruction is marked. The edge row names the real renderer (`internal/engine/docker/edge.go`) |
| v13 | 2026-10-21 | Soupler Labs | v0.5 components (planned): hypervisor, VM link containers, volumes and snapshots |
| v14 | 2026-10-21 | Soupler Labs | v0.5 components built |
| v15 | 2026-10-21 | Soupler Labs | v0.6 components (planned): message broker, delivery loop, mail client, engine services for the new engines and kinds (ADR-029 – ADR-034) |
| v16 | 2026-10-22 | Soupler Labs | v0.7 components (planned): monitor (sampler, alerts, notifier, log shipper), secret stores and the shared SigV4 signer, authorization (ADR-035 – ADR-041) |
| v17 | 2026-10-22 | Soupler Labs | v0.8 components (planned): agent, engine router, machines, mesh, catalogue; runtime flow 8 (ADR-042 – ADR-048) |
