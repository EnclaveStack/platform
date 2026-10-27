# L3-004 · Decision Analysis and Resolution (DAR) Records

---
| Field | Value |
|---|---|
| Document ID | L3-004-decision-analysis-records-v1 |
| Layer | L3 — Architecture |
| Status | Draft |
| Version | v10 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-26 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

The structured option analyses behind EnclaveStack's significant decisions. Each DAR states the criteria and their weights, the alternatives, a 1–5 score per criterion, the weighted total, and the resolution. The resulting decision is recorded as an ADR (`L3-001` §5, `L3-003`).

Method: weights sum to 100; score 5 = best fit. Weighted total = Σ(weight × score) / 100, so the maximum is 5.00.

## DAR-001 — Control-plane language (→ ADR-001)

| Criterion | Weight | Go | TypeScript (Node/Bun) | Rust | Python |
|---|---|---|---|---|---|
| Single static binary, cross-compiles to macOS/Linux × amd64/arm64 | 25 | 5 | 2 | 5 | 1 |
| Native ecosystem: Docker SDK, OpenTofu plugin framework, MinIO, Prometheus | 25 | 5 | 3 | 3 | 3 |
| Resident memory alongside user workloads | 15 | 4 | 2 | 5 | 2 |
| Delivery speed for this team | 20 | 4 | 5 | 2 | 4 |
| Open-source contributor pool for infra tools | 15 | 5 | 3 | 3 | 3 |
| **Weighted total** | 100 | **4.65** | 3.00 | 3.60 | 2.55 |

Resolution: **Go**. The provider requirement alone (`terraform-plugin-framework` is Go-only) makes Go the only language that covers all three binaries.

## DAR-002 — How three surfaces share capability (→ ADR-003)

| Criterion | Weight | One public HTTP API, all surfaces are clients | Console server-side actions + separate CLI API | Shared library linked into each surface |
|---|---|---|---|---|
| Guaranteed parity across surfaces | 35 | 5 | 2 | 3 |
| Works over the network (CI, remote laptop) | 25 | 5 | 3 | 1 |
| One security boundary to harden | 20 | 5 | 2 | 2 |
| Implementation effort | 20 | 3 | 4 | 3 |
| **Weighted total** | 100 | **4.60** | 2.65 | 2.30 |

Resolution: **API-first**.

## DAR-003 — Synchronous versus declarative/asynchronous provisioning (→ ADR-004)

| Criterion | Weight | Declarative spec/status + reconciler | Synchronous request holds until done | Job queue with imperative commands |
|---|---|---|---|---|
| Survives crashes and restarts mid-operation | 30 | 5 | 1 | 4 |
| Fits OpenTofu's read-back / drift model | 25 | 5 | 2 | 3 |
| Client simplicity | 15 | 3 | 5 | 3 |
| Self-healing (drift correction) | 20 | 5 | 1 | 2 |
| Implementation effort | 10 | 3 | 5 | 3 |
| **Weighted total** | 100 | **4.50** | 2.25 | 3.10 |

Resolution: **Declarative + reconciler** (the Kubernetes controller pattern).

## DAR-004 — Control-plane state store (→ ADR-005)

| Criterion | Weight | SQLite (embedded, pure Go) | PostgreSQL container | bbolt / BadgerDB (KV) | JSON files |
|---|---|---|---|---|---|
| Zero extra install / process | 30 | 5 | 1 | 5 | 5 |
| Transactions, constraints, queries | 25 | 5 | 5 | 2 | 1 |
| Crash safety | 20 | 5 | 5 | 4 | 1 |
| Cross-compilation without cgo | 15 | 5 | 5 | 5 | 5 |
| Throughput headroom at one-machine scale | 10 | 4 | 5 | 5 | 2 |
| **Weighted total** | 100 | **4.90** | 3.80 | 4.05 | 2.90 |

Resolution: **SQLite via `modernc.org/sqlite`**.

## DAR-005 — Container runtime integration (→ ADR-006)

| Criterion | Weight | Docker Engine SDK behind a driver interface | Shell out to `docker` CLI | Kubernetes (k3s) | containerd directly |
|---|---|---|---|---|---|
| Availability on developer Macs and Linux servers | 30 | 5 | 5 | 2 | 2 |
| Structured errors and testability | 25 | 5 | 2 | 4 | 4 |
| Footprint on the host | 20 | 4 | 4 | 2 | 5 |
| Effort | 15 | 4 | 5 | 2 | 2 |
| Future runtimes (Podman exposes the same API) | 10 | 5 | 3 | 3 | 2 |
| **Weighted total** | 100 | **4.65** | 3.85 | 2.60 | 3.10 |

Resolution: **Docker Engine SDK behind `engine.Driver`**, with a fake driver for tests.

## DAR-006 — Credentials for non-browser clients (→ ADR-007)

| Criterion | Weight | Personal API tokens (hashed, scoped, revocable) | Reuse session cookies | OAuth2 device flow + JWT | mTLS client certificates |
|---|---|---|---|---|---|
| Works in CI and OpenTofu without a browser | 30 | 5 | 2 | 3 | 5 |
| Revocation and least privilege | 25 | 5 | 2 | 4 | 3 |
| Implementation and operator effort | 25 | 4 | 5 | 2 | 1 |
| Familiar to users (GitHub, AWS, DigitalOcean do this) | 20 | 5 | 2 | 4 | 2 |
| **Weighted total** | 100 | **4.75** | 2.75 | 3.20 | 2.90 |

Resolution: **Personal API tokens**; sessions stay for browsers.

## DAR-007 — Where the OpenTofu provider lives (→ ADR-009)

| Criterion | Weight | Monorepo + release mirror | Separate repository from day one | Generated from OpenAPI spec |
|---|---|---|---|---|
| API / SDK / provider change atomically | 35 | 5 | 2 | 4 |
| Registry publishing requirements | 20 | 4 | 5 | 4 |
| Acceptance tests against the real control plane in-process | 25 | 5 | 2 | 3 |
| Plan quality (replacements, waits, sensitive values) | 20 | 5 | 5 | 2 |
| **Weighted total** | 100 | **4.80** | 3.20 | 3.35 |

Resolution: **Monorepo now, mirror repository at public launch**.

## DAR-008 — Edge proxy for domains and HTTPS (→ ADR-015)

| Criterion | Weight | Caddy container, config pushed by the reconciler | Traefik container, Docker-label discovery | Proxy inside the EnclaveStack binary (Go + certmagic) | nginx + certbot containers |
|---|---|---|---|---|---|
| Reaches apps on the private `enclave` network without publishing their ports (on macOS the host cannot route to container IPs) | 25 | 5 | 5 | 1 | 5 |
| Automatic ACME certificates **and** a local CA for `*.localhost` / LAN names | 20 | 5 | 3 | 4 | 2 |
| Our reconciler stays the single source of truth; whole-config reloads are atomic and do not drop connections | 20 | 5 | 3 | 5 | 3 |
| Traffic keeps flowing while the control plane restarts or upgrades | 15 | 5 | 5 | 1 | 5 |
| Footprint and operations | 10 | 4 | 4 | 5 | 2 |
| Licence and maintenance health | 10 | 5 | 5 | 4 | 3 |
| **Weighted total** | 100 | **4.90** | 4.10 | 3.10 | 3.50 |

Resolution: **Caddy** (Apache-2.0), used as a container image we configure through its JSON config. With labels, Traefik would hold the routing truth on containers rather than in our store. Running the proxy in-process fails on macOS networking, and would take sites down during every control-plane upgrade.

## DAR-009 — Where health checks run (→ ADR-016)

| Criterion | Weight | Docker `HEALTHCHECK` inside the app container | Probe helper container on the network, driven by engine exec | From the control-plane process | Exec inside the edge proxy |
|---|---|---|---|---|---|
| Works with any image (no `curl`/`wget` required inside it) | 30 | 2 | 5 | 5 | 5 |
| Works on macOS, where the host cannot reach container IPs | 30 | 5 | 5 | 1 | 5 |
| Independent of other features | 15 | 5 | 5 | 5 | 1 |
| Probe cost and latency | 10 | 4 | 4 | 5 | 4 |
| Implementation effort | 15 | 4 | 4 | 5 | 3 |
| **Weighted total** | 100 | 3.85 | **4.75** | 3.80 | 4.00 |

Resolution: **probe helper** for `http` and `tcp` checks. `command` checks run by engine exec inside the app container, because that is their meaning.

## DAR-010 — How apps are built from source (→ ADR-019)

| Criterion | Weight | Control plane clones (go-git), sends a tar context to the Engine build API | BuildKit daemon container driven by the BuildKit client | Shell out to the `docker build` CLI | Auto-detecting builders (Nixpacks / Railpack) |
|---|---|---|---|---|---|
| Needs nothing beyond the engine EnclaveStack already uses | 25 | 5 | 4 | 2 | 2 |
| Full Dockerfile features (multi-stage, cache and secret mounts) | 20 | 3 | 5 | 5 | 4 |
| Build secrets never land in image layers or logs | 15 | 3 | 5 | 4 | 3 |
| Implementation risk | 25 | 4 | 2 | 4 | 2 |
| Same behaviour on macOS and Linux | 15 | 5 | 4 | 4 | 4 |
| **Weighted total** | 100 | **4.05** | 3.85 | 3.70 | 2.85 |

Resolution: **Engine build API with a tar context**. Its weak point is BuildKit-only Dockerfile features: through the Go SDK, a BuildKit build needs a client session. A **spike** must settle this before builds are implemented:
- if the session works, use BuildKit;
- if not, use the classic builder and document that `RUN --mount` is unsupported.

If the spike fails outright, builds (`L2-005` BLD-*, *Should*) move to v0.4 with the BuildKit-container option.

**Spike outcome (2026-10-21): passed.** A BuildKit build with a client session works through the Go SDK on Docker Desktop 29.7.2 (macOS) and on Linux Docker Engine 29.9.1, including `RUN --mount=type=cache`. Failing steps are reported with their command and exit code. Builds ship in v0.3 on BuildKit; see ADR-019 for the details. The resolution stands, with one change found in the live check: the context reaches the Engine build API through the BuildKit session rather than as a tar request body, because Docker Desktop's API proxy truncates chunked build bodies.

## DAR-011 — Backup format and transport (→ ADR-018)

| Criterion | Weight | `pg_dump -Fc` via engine exec, streamed to S3 multipart | `pg_dumpall` plain SQL, gzipped | Physical copy (`pg_basebackup` / volume archive) | Continuous WAL archiving (point-in-time) |
|---|---|---|---|---|---|
| Restore into a new database is simple and survives minor and major version changes | 30 | 5 | 4 | 2 | 2 |
| Consistent while the database keeps serving | 25 | 5 | 5 | 4 | 5 |
| Size and speed | 15 | 4 | 3 | 5 | 4 |
| Implementation effort | 20 | 5 | 5 | 3 | 1 |
| Point-in-time recovery | 10 | 1 | 1 | 1 | 5 |
| **Weighted total** | 100 | **4.45** | 4.00 | 3.05 | 3.15 |

Resolution: **custom-format logical dumps, streamed**. Point-in-time recovery is a later option on top. Redis uses its own snapshot (`BGSAVE`, then stream `dump.rdb`); there is no meaningful alternative for it.

## DAR-012 — How a network is built on the engine (→ ADR-020)

| Criterion | Weight | One bridge network per network; subnets are ranges inside it, addresses assigned by the driver | One bridge network per subnet; resources multi-homed to reach other subnets | macvlan / ipvlan on the host's LAN | Overlay networks (Swarm) |
|---|---|---|---|---|---|
| Works the same on Docker Desktop (macOS) and Docker Engine (Linux) | 25 | 5 | 5 | 1 | 2 |
| Subnets of one network reach each other without extra plumbing | 20 | 5 | 2 | 4 | 4 |
| Networks are isolated from each other by default | 20 | 5 | 5 | 2 | 5 |
| Docker's embedded DNS gives private names for free | 15 | 5 | 3 | 4 | 5 |
| Implementation effort | 20 | 4 | 2 | 3 | 1 |
| **Weighted total** | 100 | **4.80** | 3.50 | 2.65 | 3.25 |

Resolution: **one bridge network per network**, with the network's CIDR as its IPAM subnet and the first subnet as Docker's automatic range. A resource in another subnet gets an address chosen by the driver from that subnet's range. Spiked on Docker Desktop 29.7.2: a container with `--ip` in a range outside `--ip-range` starts, both containers resolve each other, and a dotted alias (`api.app.internal`) resolves. Docker can't change a network's IPAM after creation, so the CIDR and subnets are immutable (`L2-006` NET-02).

## DAR-013 — Where firewall rules are enforced (→ ADR-021)

| Criterion | Weight | nftables inside each target container's network namespace, applied by a short-lived helper | Host iptables (`DOCKER-USER` chain) | Network topology only (one network per allowed pair) | A proxy sidecar per container |
|---|---|---|---|---|---|
| Works on Docker Desktop, where the host's own packet filter never sees container traffic | 30 | 5 | 1 | 5 | 5 |
| Port- and protocol-level rules, stateful (replies allowed) | 20 | 5 | 5 | 1 | 3 |
| Needs no privilege on the host and no change to app images | 20 | 4 | 1 | 5 | 3 |
| Applies before a new revision takes traffic; survives control-plane restarts | 15 | 4 | 5 | 5 | 4 |
| Implementation effort and footprint | 15 | 4 | 3 | 3 | 1 |
| **Weighted total** | 100 | **4.50** | 2.70 | 3.90 | 3.45 |

Resolution: **nftables in the target's network namespace**. A one-off helper container joins the target's namespace (`--network container:<target>`) with only `NET_ADMIN`, loads one table atomically and exits. The rules live in the namespace, so they keep working when the helper and the control plane are gone. Spiked on Docker Desktop 29.7.2: a drop policy loaded this way blocked a connection from another container. The helper image is built on the machine from a pinned Alpine base with `nftables`, through the builder EnclaveStack already has (ADR-019). Its weak point is a short window after a container starts and before its rules load, measured in ADR-021.

## DAR-014 — What runs a load balancer (→ ADR-022)

| Criterion | Weight | HAProxy container per load balancer, replicas found through Docker DNS | Caddy (the edge) with the layer-4 module | Envoy container | Docker DNS round robin only |
|---|---|---|---|---|---|
| TCP and HTTP in one proxy, with active health checks | 30 | 5 | 2 | 5 | 1 |
| Picks up replicas added or removed by a deploy without a reload | 20 | 5 | 3 | 4 | 4 |
| Stock image, pinned by digest (no custom build) | 20 | 5 | 1 | 5 | 5 |
| Configuration we can render and validate before applying | 15 | 5 | 5 | 3 | 5 |
| Footprint | 15 | 4 | 5 | 3 | 5 |
| **Weighted total** | 100 | **4.85** | 2.90 | 4.20 | 3.60 |

Resolution: **HAProxy 3.2** (GPL-2.0, used unmodified as a container image) with `server-template` entries resolved through Docker's DNS every second. Spiked on Docker Desktop 29.7.2: two replicas behind one alias were found and balanced, and the stats CSV reported each as `UP`. The edge (Caddy) keeps serving domains and spreads requests across replicas with its dynamic `a` upstreams.

## DAR-015 — How DNS providers are reached (→ ADR-023)

| Criterion | Weight | Our own small clients behind one `Provider` interface (Cloudflare REST; Route 53 REST signed with SigV4 from `minio-go`) | `libdns` provider modules | lego's provider packages | The vendors' SDKs (`cloudflare-go`, `aws-sdk-go-v2`) |
|---|---|---|---|---|---|
| Few dependencies and a small binary | 25 | 5 | 4 | 2 | 1 |
| Records EnclaveStack owns are told apart from the rest; adoption and conflicts are explicit | 25 | 5 | 3 | 1 | 5 |
| Testable against a local fake of each API | 20 | 5 | 4 | 3 | 3 |
| Effort to add a provider later | 15 | 3 | 5 | 5 | 3 |
| Maturity | 15 | 3 | 4 | 5 | 5 |
| **Weighted total** | 100 | **4.40** | 3.90 | 2.85 | 3.30 |

Resolution: **our own clients** for the two v0.4 providers, behind `internal/dns.Provider` (list, upsert, delete records of one name and type). lego's providers are built to set and remove one challenge record and hide everything else, and the SDKs add tens of megabytes. Cloudflare and Route 53 are tested against local fakes written from their API references; no test reaches the real services.

## DAR-016 — How DNS-01 certificates are obtained (→ ADR-024)

| Criterion | Weight | The control plane runs ACME (lego core) through our DNS plug-ins and loads certificates into the edge | A custom Caddy build with DNS modules | Caddy's `dns` challenge with an external-DNS hook | A certbot container |
|---|---|---|---|---|---|
| Provider credentials stay in the control plane's encrypted store | 25 | 5 | 1 | 3 | 2 |
| Uses the same provider plug-ins as records (one implementation per provider) | 25 | 5 | 1 | 3 | 1 |
| The edge stays a stock image, pinned by digest | 20 | 5 | 1 | 5 | 5 |
| Testable offline against Pebble | 15 | 5 | 4 | 3 | 3 |
| Implementation effort | 15 | 3 | 4 | 2 | 3 |
| **Weighted total** | 100 | **4.70** | 1.90 | 3.25 | 2.65 |

Resolution: **ACME in the control plane** with the core packages of lego (MIT), issuing through our `dns.Provider` plug-ins. Certificates and their encrypted keys are kept in the store and handed to Caddy as loaded certificates, so Caddy doesn't try to obtain those names itself. Pebble and its challenge test server (already used for HTTP-01, `L5-002` §6) cover DNS-01 offline.

## DAR-017 — What runs a VM (→ ADR-025)

| Criterion | Weight | QEMU on the host, macOS and Linux | Apple Virtualization framework on macOS, QEMU on Linux | QEMU in a container with `/dev/kvm` (Linux only) | Firecracker / Cloud Hypervisor (Linux only) |
|---|---|---|---|---|---|
| Works on macOS (Docker Desktop has no `/dev/kvm`) and Linux | 30 | 5 | 5 | 1 | 1 |
| One implementation to build, test and support | 20 | 5 | 2 | 5 | 4 |
| Pure Go control plane, cross-compiled, no code signing | 15 | 5 | 1 | 5 | 5 |
| Standard cloud images and cloud-init boot unchanged | 15 | 5 | 4 | 5 | 2 |
| Live disk snapshots and resize built in | 10 | 5 | 2 | 5 | 3 |
| Nothing extra for the user to install | 10 | 2 | 4 | 4 | 2 |
| **Weighted total** | 100 | **4.70** | 3.25 | 3.70 | 2.65 |

Resolution: **QEMU on the host**, decided with the owner on 2026-10-21. The cost is that users install QEMU (`brew install qemu`, `apt install qemu-system`), which `GET /machine` asks for (`L2-007` VM-11). The Apple framework needs cgo, a signed binary with the virtualization entitlement and a second driver.

## DAR-018 — How a VM joins a network (→ ADR-026)

| Criterion | Weight | A link container per VM; QEMU's NIC streams frames to it over a published loopback port | QEMU user-mode networking with port forwards | `vmnet` / `socket_vmnet` on macOS, a host bridge on Linux | A VPN (WireGuard) from each VM into the network |
|---|---|---|---|---|---|
| Private IP, `.internal` name, firewalls and `internet: false` behave exactly as for containers | 35 | 5 | 1 | 2 | 3 |
| Same on Docker Desktop and Docker Engine | 25 | 5 | 5 | 1 | 4 |
| No root and no host network changes | 20 | 5 | 5 | 1 | 3 |
| Implementation effort | 10 | 3 | 5 | 2 | 2 |
| Throughput | 10 | 3 | 2 | 5 | 3 |
| **Weighted total** | 100 | **4.60** | 3.30 | 1.85 | 3.15 |

Resolution: **a link container per VM**. Spiked on Docker Desktop 29.7.2 with QEMU 11.1.2:
- the VM reached and was reached by containers through private DNS, at about 160 MB/s;
- it reached the internet once checksums were recomputed;
- firewall rules in a `forward` chain allowed one container and blocked another;
- traffic recovered within 1 s when the link container was recreated.

Two findings shaped ADR-026: link-local link addresses aren't forwarded, and Docker Desktop delivers unfilled checksums.

## DAR-019 — How volumes are stored (→ ADR-027)

| Criterion | Weight | Disks as qcow2 files, filesystems as engine volumes | One format for both (raw images, loop-mounted into containers) | Filesystems as host directories, bind-mounted | Disks as engine volumes holding an image file |
|---|---|---|---|---|---|
| Works on Docker Desktop, where the host and the engine are different machines | 30 | 5 | 1 | 2 | 4 |
| Thin, grows in place, live snapshots (disks) | 25 | 5 | 2 | 3 | 3 |
| Shared by replicas and revisions (filesystems) | 20 | 5 | 1 | 5 | 5 |
| No privilege needed | 15 | 5 | 1 | 4 | 4 |
| Implementation effort | 10 | 4 | 2 | 4 | 2 |
| **Weighted total** | 100 | **4.90** | 1.35 | 3.35 | 3.75 |

Resolution: **two storage types behind one kind**: each lives where its consumer runs. QEMU on the host opens files, and containers in the engine mount engine volumes.

## DAR-020 — What a snapshot is (→ ADR-028)

| Criterion | Weight | Full independent copy | qcow2 internal snapshot inside the source file | External overlay chain (copy-on-write) | Archive to a bucket |
|---|---|---|---|---|---|
| Independent of its source: either can be deleted (SNP-02) | 30 | 5 | 1 | 2 | 5 |
| Taken while in use, without pausing the VM (SNP-05) | 25 | 5 | 2 | 5 | 3 |
| Simple restore into a new volume or VM | 20 | 5 | 3 | 3 | 3 |
| Space used | 15 | 2 | 5 | 5 | 4 |
| Implementation effort | 10 | 4 | 3 | 2 | 2 |
| **Weighted total** | 100 | **4.45** | 2.45 | 3.40 | 3.65 |

Resolution: **full copies on the machine**. QMP `drive-backup` copies a running disk without pausing it (0.8 s for 1.13 GiB in the spike). Copies in buckets are a later step built on the same files.

## DAR-021 — What runs queues and topics (→ ADR-029)

| Criterion | Weight | NATS JetStream, one broker | RabbitMQ | Redis Streams in a cache | Built into the control plane (SQLite) |
|---|---|---|---|---|---|
| Queue semantics (visibility timeout, redelivery, dead letters) and fan-out in one system | 25 | 5 | 5 | 3 | 4 |
| Apps use ordinary clients, with credentials scoped to one queue or topic | 20 | 5 | 4 | 2 | 1 |
| Footprint on a home machine (memory, image size) | 20 | 5 | 2 | 4 | 5 |
| Messages survive restarts of the broker and of EnclaveStack | 15 | 5 | 5 | 4 | 5 |
| Configuration changes without restarting (users, streams) | 10 | 5 | 3 | 3 | 5 |
| Implementation effort | 10 | 4 | 3 | 3 | 2 |
| **Weighted total** | 100 | **4.90** | 3.80 | 3.15 | 3.65 |

Resolution: **NATS JetStream**, chosen with the owner on 2026-10-21. Spiked on Docker Desktop 29.7.2 with `nats:2.12.1-alpine`: bcrypt users with per-subject permissions; a queue user denied another queue's subject; work-queue retention; duplicate detection by message ID; redelivery after the ack wait; a max-deliveries advisory captured in a stream and readable later; a new user accepted after `SIGHUP` while an existing connection stayed up.

## DAR-022 — How more database engines are added (→ ADR-030)

| Criterion | Weight | The database kind gains an engine | A new kind per engine | One generic "container service" kind |
|---|---|---|---|---|
| Same lifecycle, outputs, backups and surfaces as PostgreSQL | 35 | 5 | 4 | 2 |
| Users learn one thing | 25 | 5 | 2 | 3 |
| Contract and code growth | 20 | 4 | 2 | 4 |
| Engine-specific settings later | 20 | 3 | 5 | 2 |
| **Weighted total** | 100 | **4.40** | 3.30 | 2.65 |

Resolution: **an `engine` field on `database`**, as AWS RDS does. Engine-specific settings can be added later as optional fields that only one engine accepts.

## DAR-023 — What runs search (→ ADR-031)

| Criterion | Weight | OpenSearch | Meilisearch | Typesense | Elasticsearch |
|---|---|---|---|---|---|
| Compatible with AWS OpenSearch and Elasticsearch clients and tools | 30 | 5 | 1 | 1 | 4 |
| Licence fits an Apache-2.0 project that ships images | 20 | 5 | 5 | 4 | 2 |
| Footprint on a home machine | 20 | 2 | 5 | 4 | 2 |
| Built-in authentication | 15 | 4 | 5 | 5 | 4 |
| Feature breadth (aggregations, analysers, vectors) | 15 | 5 | 3 | 3 | 5 |
| **Weighted total** | 100 | **4.25** | 3.50 | 3.10 | 3.35 |

Resolution: **OpenSearch**, chosen with the owner on 2026-10-21 for compatibility; the cost is 1 – 2 GiB of memory per service, which the form says.

## DAR-024 — How email is sent (→ ADR-032)

| Criterion | Weight | Mailpit per service, relaying through the user's provider | Postfix relay per machine | Direct delivery (own MTA) | Control-plane SMTP server |
|---|---|---|---|---|---|
| Mail actually reaches inboxes | 30 | 5 | 5 | 1 | 5 |
| Messages kept and viewable (testing) | 25 | 5 | 1 | 1 | 3 |
| Apps use plain SMTP with their own credentials | 20 | 5 | 4 | 4 | 4 |
| Footprint and effort | 15 | 5 | 3 | 2 | 2 |
| Isolation between services | 10 | 5 | 2 | 2 | 3 |
| **Weighted total** | 100 | **5.00** | 3.20 | 1.85 | 3.65 |

Resolution: **Mailpit per email service**, catch + relay as chosen with the owner on 2026-10-21.

## DAR-025 — What caches for a CDN (→ ADR-033)

| Criterion | Weight | nginx per CDN behind the edge | A cache module in the edge (Caddy + Souin) | Varnish per CDN | Caching in the control plane |
|---|---|---|---|---|---|
| Standard HTTP caching (Cache-Control, stale on error, locking) | 30 | 5 | 4 | 5 | 2 |
| Edge stays a stock, pinned image (ADR-015) | 20 | 5 | 1 | 5 | 5 |
| Cache isolated and sized per CDN | 20 | 5 | 2 | 5 | 3 |
| Footprint | 15 | 5 | 5 | 3 | 4 |
| Implementation effort | 15 | 4 | 2 | 3 | 2 |
| **Weighted total** | 100 | **4.85** | 2.85 | 4.40 | 3.10 |

Resolution: **nginx per CDN**; the edge keeps routing and certificates.

## DAR-026 — How media is transcoded (→ ADR-034)

| Criterion | Weight | FFmpeg runs fed through the archive API | FFmpeg reading presigned URLs, uploading with curl | A transcoding service (e.g. Tdarr) | FFmpeg in the control plane |
|---|---|---|---|---|---|
| The run needs no network and no credentials | 30 | 5 | 2 | 2 | 5 |
| Works with any bucket size and Docker Desktop | 20 | 4 | 5 | 4 | 3 |
| One more thing for the user to install | 20 | 5 | 5 | 3 | 1 |
| Reuses runs, scheduler and bucket client | 20 | 5 | 4 | 1 | 3 |
| Implementation effort | 10 | 3 | 4 | 2 | 3 |
| **Weighted total** | 100 | **4.60** | 3.80 | 2.40 | 3.20 |

Resolution: **FFmpeg runs fed and emptied by the control plane** through the engine's archive API, streaming both ways.

## DAR-027 — How projects separate resources (→ ADR-035)

| Criterion | Weight | Namespaces; `default` keeps v0.6 names | Folders (names unique per machine) | Namespaces; every name gains its project | One EnclaveStack per project |
|---|---|---|---|---|---|
| The same module applies to two projects unchanged (same names) | 30 | 5 | 1 | 5 | 5 |
| Nothing that runs today changes (addresses, DNS, IDs) | 25 | 5 | 5 | 1 | 2 |
| One place to see and manage everything | 15 | 5 | 5 | 5 | 1 |
| Isolation between projects (credentials, names, lists) | 15 | 4 | 3 | 4 | 5 |
| Implementation effort | 15 | 3 | 4 | 3 | 2 |
| **Weighted total** | 100 | **4.55** | 3.35 | 3.55 | 3.20 |

Resolution: **namespaces, with `default` keeping its names**, chosen with the owner on 2026-10-22 ("why wait for later"). Buckets, hostnames, zones and network ranges stay machine-wide because the outside world or the host's routing sees them.

## DAR-028 — How access is granted (→ ADR-036)

| Criterion | Weight | Three ordered roles, machine-wide or per project | Policies of allowed actions (AWS IAM style) | Owner/member per project only | Keep one admin; tokens per project |
|---|---|---|---|---|---|
| Covers the story (viewer accountant, project editor contractor, CI token) | 30 | 5 | 5 | 3 | 2 |
| Easy to understand and to show in the console | 25 | 5 | 2 | 5 | 5 |
| Safe defaults; hard to grant too much by mistake | 20 | 5 | 2 | 4 | 4 |
| Room to grow (custom roles later) | 15 | 4 | 5 | 2 | 1 |
| Implementation effort | 10 | 4 | 1 | 5 | 5 |
| **Weighted total** | 100 | **4.75** | 3.25 | 3.75 | 3.30 |

Resolution: **viewer, editor and admin**, on the machine or on a project; tokens narrow their user's roles and never widen them.

## DAR-029 — Where metrics live (→ ADR-037)

| Criterion | Weight | Sampled by the control plane, in SQLite | A managed Prometheus container | VictoriaMetrics container | Only `/metrics` (bring your own) |
|---|---|---|---|---|---|
| Nothing more to run or to break on a home machine | 30 | 5 | 2 | 3 | 5 |
| Graphs and alerts in the console with no setup | 25 | 5 | 4 | 4 | 1 |
| History (30 days) and query speed for a few hundred series | 20 | 4 | 5 | 5 | 3 |
| Memory and disk | 15 | 5 | 2 | 4 | 5 |
| Implementation effort | 10 | 3 | 3 | 3 | 5 |
| **Weighted total** | 100 | **4.60** | 3.20 | 3.80 | 3.60 |

Resolution: **sampled by the control plane and kept in SQLite**, chosen with the owner on 2026-10-22; `/metrics` stays for Prometheus users.

## DAR-030 — How alerts are evaluated (→ ADR-038)

| Criterion | Weight | Evaluator loop over stored points, state in SQLite | Evaluate inside the sampler only (in memory) | Prometheus rules (needs DAR-029's Prometheus) |
|---|---|---|---|---|
| Survives restarts without re-announcing | 30 | 5 | 1 | 4 |
| Event conditions (failed, run failed, crash loop) next to metric ones | 25 | 5 | 3 | 2 |
| No extra service | 25 | 5 | 5 | 1 |
| Implementation effort | 20 | 4 | 5 | 3 |
| **Weighted total** | 100 | **4.80** | 3.30 | 2.55 |

Resolution: **an evaluator over the stored points**, with alert state and incidents in SQLite.

## DAR-031 — How notifications are delivered (→ ADR-039)

| Criterion | Weight | Persisted outbox with retries | Send inline from the evaluator | Through a topic (ADR-029) with webhook subscriptions |
|---|---|---|---|---|
| Survives restarts and outages of the receiver | 30 | 5 | 1 | 5 |
| Email, Slack, Discord, Telegram formats | 25 | 5 | 5 | 1 |
| Works without the broker running | 25 | 5 | 5 | 1 |
| Implementation effort | 20 | 4 | 5 | 3 |
| **Weighted total** | 100 | **4.80** | 3.80 | 2.60 |

Resolution: **a persisted outbox** sent by the notifier.

## DAR-032 — How logs are exported (→ ADR-040)

| Criterion | Weight | Shipper in the control plane following engine logs | A Fluent Bit / Vector sidecar per export | Docker logging drivers (syslog, Loki plug-in) |
|---|---|---|---|---|
| Keeps `enclave logs` and the console's Logs working | 25 | 5 | 5 | 1 |
| Resumes without loss after restarts (checkpoints) | 25 | 4 | 5 | 2 |
| Nothing more to run; no host plug-ins | 20 | 5 | 2 | 1 |
| All four destinations | 15 | 4 | 5 | 2 |
| Implementation effort | 15 | 3 | 3 | 2 |
| **Weighted total** | 100 | **4.30** | 4.10 | 1.55 |

Resolution: **a shipper in the control plane**, reading the engine's log API with checkpoints. Fluent Bit was close; the control plane already reads the same logs for the console, and adds no container per export.

## DAR-033 — How outside secret managers are read (→ ADR-041)

| Criterion | Weight | Plain HTTP clients, values cached encrypted | Vendor SDKs, values cached | Read at deploy time only, nothing cached |
|---|---|---|---|---|
| Apps start while the store is down | 30 | 5 | 5 | 1 |
| Binary size and dependencies (AWS SDK, 1Password SDK) | 20 | 5 | 2 | 5 |
| Rotations show up without a redeploy request | 20 | 5 | 5 | 2 |
| Tested against local fakes of every API | 15 | 5 | 3 | 5 |
| Implementation effort | 15 | 3 | 4 | 4 |
| **Weighted total** | 100 | **4.70** | 3.95 | 3.05 |

Resolution: **plain HTTP clients**, with the last value cached encrypted like any secret (ADR-041). 1Password is read through a Connect server, whose REST API needs no SDK.

## DAR-034 — How the control plane talks to other machines (→ ADR-042)

| Criterion | Weight | The control plane calls an agent over mutual TLS | The agent dials the control plane and keeps one connection | SSH and shell commands (as Coolify) | The other machine's Docker API over TLS |
|---|---|---|---|---|---|
| The engine contract and its drivers are reused unchanged on the machine | 25 | 5 | 5 | 1 | 4 |
| Security: both ends authenticated, no root shell handed out | 25 | 5 | 5 | 2 | 3 |
| Can run QEMU and reach host files (VMs, disks, snapshots) | 20 | 5 | 5 | 4 | 1 |
| Reachability (machines reach each other on UDP anyway, XNET) | 15 | 4 | 5 | 3 | 3 |
| Implementation effort | 15 | 4 | 2 | 2 | 4 |
| **Weighted total** | 100 | **4.70** | 4.55 | 2.30 | 3.00 |

Resolution: **the control plane calls the agent**, one HTTPS request per engine call, with mutual TLS from an EnclaveStack certificate authority. An agent that dials out would only help machines that can't be reached, and those can't carry the tunnel either (XNET-04).

## DAR-035 — How networks span machines (→ ADR-045)

| Criterion | Weight | A mesh container per machine: WireGuard, answers ARP for remote addresses; stand-ins carry remote names | Docker Swarm overlay networks | VXLAN over WireGuard bridged into the engine's bridges | Routed: each machine gets its own slice of each network |
|---|---|---|---|---|---|
| Same addresses, names, isolation and firewalls as on one machine (XNET-01 – XNET-03) | 30 | 5 | 4 | 5 | 2 |
| Works on Docker Desktop for Mac, where the engine runs in a VM behind NAT and its host namespace is out of reach | 25 | 5 | 1 | 2 | 5 |
| Encrypted, with keys handed out by the control plane | 15 | 5 | 3 | 5 | 5 |
| Networks made before v0.8 keep working, unchanged | 15 | 5 | 1 | 4 | 2 |
| Effort and moving parts | 15 | 3 | 3 | 2 | 4 |
| **Weighted total** | 100 | **4.70** | 2.50 | 3.65 | 3.50 |

Resolution: **a mesh container per machine with stand-ins**. Spiked on 2026-10-22 on Docker Desktop 29.7.2 (kernel 7.0.12-linuxkit, which has WireGuard) with a Docker-in-Docker 28.5.1 engine as the second machine, the same network `10.77.0.0/24` on both:
- a container reached the other machine's container by address, both ways, through the two mesh containers' WireGuard tunnel; the receiver saw the real source address (conntrack), so firewalls keep matching;
- a 4 MB transfer arrived intact (checksums equal), so path-MTU discovery works through the tunnel's 1420-byte MTU;
- a stand-in holding the remote address and name made `b1.internal` resolve on the first machine. Docker announces an endpoint's address when it starts, even with `com.docker.network.advertise_addr_nmsgs=0`, which pointed neighbours at the stand-in. A stand-in that drops its own copy of the address and forwards to the mesh works whichever device a neighbour cached, across three stand-in restarts.

## DAR-036 — How private addresses stay unique across machines (→ ADR-045)

| Criterion | Weight | Pools of addresses per machine, handed out by the control plane | A fixed range per machine, set when the network is created | Each engine chooses; duplicates repaired | The control plane picks each address during container creation |
|---|---|---|---|---|---|
| No duplicate address, ever (XNET-02) | 35 | 5 | 4 | 2 | 5 |
| Networks made before v0.8 keep working (their engine ranges are fixed) | 20 | 5 | 2 | 5 | 5 |
| Small subnets (/28, /29) still usable on several machines | 15 | 4 | 1 | 5 | 5 |
| No round trip to the control plane per container | 15 | 5 | 5 | 5 | 1 |
| Implementation effort | 15 | 3 | 4 | 4 | 2 |
| **Weighted total** | 100 | **4.55** | 3.30 | 3.80 | 3.95 |

Resolution: **pools per machine**. Each machine takes the lowest free address of its pool for every container it connects; the control plane keeps every pool topped up.

## DAR-037 — How data moves to another machine (→ ADR-047)

| Criterion | Weight | The engine volume as a tar stream, disks as qcow2 files, through the control plane | A logical dump and restore, as backups do | rsync between the machines over the tunnel | A backup to the object store, then a restore |
|---|---|---|---|---|---|
| An exact copy of everything (search indexes, captured email, owners and modes) | 30 | 5 | 3 | 5 | 3 |
| Works for every kind with data: every database engine, caches, search, email, volumes, VM disks | 25 | 5 | 2 | 4 | 2 |
| Uses paths that already exist and are tested (volume snapshots, disk copies) | 20 | 4 | 5 | 1 | 4 |
| Speed | 15 | 4 | 3 | 5 | 2 |
| Implementation effort | 10 | 4 | 4 | 2 | 3 |
| **Weighted total** | 100 | **4.55** | 3.25 | 3.65 | 2.80 |

Resolution: **the volume or disk itself, streamed through the control plane**, with the source stopped so the copy is consistent.

## DAR-038 — How templates describe stacks (→ ADR-048)

| Criterion | Weight | YAML or JSON of API specs, with `${…}` placeholders from a fixed list | Go `text/template` rendering text, then parsed | OpenTofu modules run by the control plane | Helm-style charts |
|---|---|---|---|---|---|
| A template can only build what a user could by hand (members are API specs, validated as such) | 30 | 5 | 4 | 4 | 3 |
| Easy to write and read for someone who knows the API | 25 | 5 | 3 | 3 | 2 |
| Errors shown before anything is created, with a line | 20 | 5 | 3 | 4 | 3 |
| Safe: no code, loops, file or network access | 15 | 5 | 3 | 2 | 3 |
| Implementation effort | 10 | 4 | 4 | 1 | 2 |
| **Weighted total** | 100 | **4.90** | 3.40 | 3.15 | 2.65 |

Resolution: **specs with placeholders**: `${stack}`, `${project}`, `${param:…}`, `${name:…}`, `${host:…}`, `${ident:…}`, and `when` to leave a member out.

## DAR-039 — How a machine is chosen (→ ADR-044)

| Criterion | Weight | The machine with the most free memory, among those that fit | Fill the main machine first | Round robin | The user always chooses |
|---|---|---|---|---|---|
| A resource fits where it lands (memory per machine, CP-16) | 30 | 5 | 5 | 2 | 3 |
| Spreads risk: losing one machine takes less with it | 25 | 4 | 1 | 4 | 3 |
| Predictable, and explainable in a message | 20 | 4 | 5 | 3 | 5 |
| One-machine setups unchanged | 15 | 5 | 5 | 5 | 2 |
| Implementation effort | 10 | 5 | 5 | 5 | 5 |
| **Weighted total** | 100 | **4.55** | 4.00 | 3.45 | 3.45 |

Resolution: **most free memory**, the main machine on a tie, filtered by version, labels, kind (VMs need VM support) and the volume a resource uses.

## DAR-040 — How traffic from the internet reaches a machine behind NAT (→ ADR-051, ADR-052)

A machine at home has no address strangers can connect to: the router drops unasked-for connections (NAT), and many providers share one address between homes (CGNAT), so no port can be forwarded. Outgoing connections always work (`L2-014` §1).

| Criterion | Weight | Port forwarding + dynamic DNS | A provider's tunnel (Cloudflare): a connector at home dials out to the provider's edge | A relay on the user's own server, reached over the mesh the machine dials out on | An edge EnclaveStack hosts for everyone (`*.enclavestack.dev`) |
|---|---|---|---|---|---|
| Works behind CGNAT with no change on the router | 30 | 1 | 5 | 5 | 5 |
| We operate nothing on the internet (`L1-001`: self-hosted, no service of ours) | 25 | 5 | 5 | 5 | 1 |
| The home address stays hidden and nothing is opened at home | 15 | 1 | 5 | 5 | 5 |
| A way in with no third party in the path | 10 | 5 | 1 | 5 | 3 |
| Cost and setup for the user | 10 | 3 | 5 | 2 | 5 |
| Effort and moving parts for us | 10 | 5 | 4 | 3 | 1 |
| **Weighted total** | 100 | 3.00 | **4.50** | **4.50** | 3.40 |

Resolution: **both the provider tunnel and the relay**, behind one `tunnel` kind. They tie, and they serve different users. Cloudflare is free and has nothing to rent. The relay keeps every byte off third-party servers. The owner chose both for the first version (2026-10-24). Port forwarding stays possible, as today (`--edge-address 0.0.0.0`), but it is not part of this feature. A hosted edge is out of scope: we would be running internet infrastructure for strangers, with its cost and abuse handling.

## DAR-041 — Where a relay's TLS ends (→ ADR-052)

| Criterion | Weight | Pass TCP through to the edge proxy at home, the visitor's address in a PROXY protocol header | Terminate TLS on the rented server and re-encrypt over the mesh | Route by SNI on the rented server, TLS still ending at home |
|---|---|---|---|---|
| Private keys and decrypted traffic never on the rented server | 35 | 5 | 1 | 5 |
| Certificates and renewals stay where they are today (the edge proxy, ADR-015, ADR-024) | 20 | 5 | 2 | 5 |
| The app sees the visitor's real address (PUB-07) | 15 | 5 | 5 | 5 |
| The relay needs no configuration per domain | 15 | 5 | 2 | 2 |
| Effort | 15 | 4 | 3 | 3 |
| **Weighted total** | 100 | **4.85** | 2.25 | 4.25 |

Resolution: **TCP passthrough with PROXY protocol v2**. The relay forwards ports 80 and 443 to one pair of listeners on the edge proxy. It never sees a hostname, a key or a decrypted byte, so it needs no change when domains change.

## DAR-042 — How the gate checks a visitor (→ ADR-053)

| Criterion | Weight | The edge proxy asks a gate listener in the control plane (`forward_auth`); sessions in the store | Only the edge proxy's own HTTP basic authentication | A separate gate container with its own copy of users | A signed cookie checked by a plug-in compiled into the edge proxy |
|---|---|---|---|---|---|
| Uses EnclaveStack accounts and project roles, and access ends within a minute when it's taken away (GATE-02, GATE-03) | 30 | 5 | 1 | 3 | 3 |
| Keeps the stock, pinned edge proxy image (ADR-015) | 20 | 5 | 5 | 5 | 1 |
| Adds nothing reachable from the internet beyond the gate's own pages | 20 | 4 | 5 | 4 | 5 |
| Works the same on Docker Desktop and Linux | 15 | 4 | 5 | 5 | 5 |
| Effort | 15 | 4 | 5 | 2 | 2 |
| **Weighted total** | 100 | **4.50** | 3.80 | 3.75 | 3.15 |

Resolution: **`forward_auth` to a gate listener in the control plane** for `signin`, and the edge proxy's own basic authentication for `password`, where it is all that's needed. The gate listener must be reachable from the edge container and from nothing outside the computer. On Docker Desktop that means `host.docker.internal`; on Linux, the host's address on the default network's bridge. This is a spike before step 1 of `L2-014` §7, recorded in ADR-053 when it's done.

## DAR-043 — How ways in plug in (→ ADR-051)

The owner wants the way in to be the user's choice, installed from the catalogue (`L2-014` TUN-15).

| Criterion | Weight | Built-in plug-ins for the common providers, plus a `custom` connector for any other, all behind one `tunnel` kind | Built-in plug-ins only | Only `custom`: every provider is an image and settings the user supplies | Each provider a catalogue template of an ordinary app running its connector |
|---|---|---|---|---|---|
| A user can pick any provider, including ones we never heard of | 25 | 5 | 2 | 5 | 4 |
| Common providers work with no knowledge of their connector (addresses read back, settings checked, errors explained, clean-up at the provider) | 25 | 5 | 5 | 1 | 2 |
| The gate and isolation hold whatever the provider (every connector reaches only its own listener on the edge) | 20 | 5 | 5 | 5 | 2 |
| One way of making an app public on every surface | 15 | 5 | 5 | 4 | 3 |
| Effort | 15 | 3 | 4 | 5 | 4 |
| **Weighted total** | 100 | **4.70** | 4.10 | 3.85 | 2.95 |

Resolution: **built-in plug-ins plus `custom`**. Quick link, Tailscale, Cloudflare, ngrok and the relay are plug-ins. `custom` runs any other connector image with `ENCLAVE_UPSTREAM`. A connector as an ordinary app was rejected, because an app joins user networks and would bypass the per-tunnel listener and the gate. The catalogue entries are templates whose members are a credentials secret and a `tunnel` (ADR-048), so installing one and creating one from **Tunnels** are the same thing.

## DAR-044 — What a function is (→ ADR-055)

| Criterion | Weight | A container image started per event, on the job engine | Code uploaded for built-in runtimes (Node, Python, Go) | Both: images, plus a runtime shortcut built from Git |
|---|---|---|---|---|
| Any language and any dependency | 25 | 5 | 2 | 5 |
| Reuses what exists (runs, adoption, limits, output, alerts) | 25 | 5 | 3 | 4 |
| Nothing new to keep patched (base images per runtime) | 20 | 5 | 1 | 2 |
| Quick to write for small glue code | 15 | 3 | 5 | 5 |
| Effort | 15 | 5 | 2 | 2 |
| **Weighted total** | 100 | **4.70** | 2.50 | 3.70 |

Resolution: **a container image**, chosen with the owner. Builds from Git (ADR-019) already turn code into an image, so a runtime shortcut can come later without changing the kind.

## DAR-045 — What starts a preview (→ ADR-056)

| Criterion | Weight | Git webhooks (GitHub, GitLab) plus the CLI | The CLI and console only | Polling the Git host's API for open pull requests |
|---|---|---|---|---|
| Previews follow pull requests with no one's help | 30 | 5 | 1 | 4 |
| Works behind NAT | 20 | 4 (through a tunnel) | 5 | 5 |
| No API token for the Git host to keep | 20 | 5 | 5 | 1 |
| Works with any Git host | 15 | 3 (CLI for the rest) | 5 | 2 |
| Effort | 15 | 3 | 5 | 2 |
| **Weighted total** | 100 | **4.20** | 3.80 | 3.00 |

Resolution: **webhooks plus the CLI**, chosen with the owner. Webhooks arrive at the control plane, or through any tunnel at `/.enclave/hooks/`.

## DAR-046 — How the control plane survives its machine (→ ADR-057)

| Criterion | Weight | A standby fed with copies, promoted by command or after a timeout, with epoch fencing | A consensus cluster of three or more (Raft) | Scheduled backups of the control plane to a bucket, restored by hand |
|---|---|---|---|---|
| Keeps ADR-005 (one SQLite file) | 25 | 5 | 1 | 5 |
| Little lost when the main dies | 20 | 4 (about 15 s) | 5 | 2 (hours) |
| Quick to take over | 20 | 4 | 5 | 1 |
| Can't end with two control planes driving the agents | 20 | 4 (fencing) | 5 | 3 |
| Effort and risk | 15 | 4 | 1 | 5 |
| **Weighted total** | 100 | **4.25** | 3.40 | 3.20 |

Resolution: **a standby with fencing**, chosen with the owner. A cluster would replace the store and the reconciler's single-writer design.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler | DAR-001 – DAR-007: language, surface architecture, provisioning model, state store, runtime integration, client credentials, provider placement |
| v2 | 2026-10-20 | Soupler | DAR-008 – DAR-011 for v0.3: edge proxy, health-check placement, builds, backup format |
| v3 | 2026-10-21 | Soupler | DAR-012 – DAR-016 for v0.4: network model, firewall enforcement, load balancer proxy, DNS provider clients, DNS-01 issuance (spikes recorded) |
| v4 | 2026-10-21 | Soupler | DAR-017 – DAR-020 for v0.5: hypervisor, VM networking, volume storage, snapshot form (spikes recorded) |
| v5 | 2026-10-21 | Soupler | DAR-021 – DAR-026 for v0.6: broker, database engines, search, email, CDN cache, transcoding (broker spike recorded) |
| v6 | 2026-10-22 | Soupler | DAR-027 – DAR-033 for v0.7: project separation, access model, metrics storage, alert evaluation, notification delivery, log export, outside secret managers |
| v7 | 2026-10-22 | Soupler | DAR-023 criterion reads "an Apache-2.0 project" (`L1-001` v4); OpenSearch is Apache-2.0 itself, so the scores stand |
| v8 | 2026-10-22 | Soupler | DAR-034 – DAR-039 for v0.8: agent transport, networks across machines (spike recorded), unique addresses, moving data, template language, choosing a machine |
| v9 | 2026-10-24 | Soupler | DAR-040 – DAR-042 for public access: the way in (provider tunnel and own relay), where a relay's TLS ends, how the gate checks a visitor (L2-014) |
| v10 | 2026-10-26 | Soupler | DAR-044 – DAR-046 for v0.10: what a function is, what starts a preview, how the control plane survives its machine (`L2-016`) |
