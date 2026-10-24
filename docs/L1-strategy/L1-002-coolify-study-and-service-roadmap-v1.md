# L1-002 · Coolify Study and Service Roadmap

---
| Field | Value |
|---|---|
| Document ID | L1-002-coolify-study-and-service-roadmap-v1 |
| Layer | L1 — Vision & Strategy |
| Status | Draft |
| Version | v12 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-24 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

This doc records what we learned by reading Coolify, the closest open-source project to EnclaveStack. It also sets the order in which EnclaveStack grows towards the common services of AWS and GCP. Every later L2 PRD for a new resource kind traces back to a row in §4.

## 1 · Decision and ground rules

Founder decision (2026-10-20): EnclaveStack stays a standalone product. We study Coolify for ideas and **write all of our own code**. Nothing is copied from it:
- no source files;
- no line-by-line translations from PHP to Go;
- no template files.

Ideas, behaviours and architecture are free to learn from. Code and template files are not. Our one-click catalogue (§3.1) will be written from each upstream project's own documentation, not from Coolify's templates.

Study basis: `coollabsio/coolify` at commit `2ade696` (2026-10-20), read locally and not vendored. It has roughly 186,000 lines of PHP in 935 files under `app/`, plus 386 one-click templates.

## 2 · What Coolify is

- **What it does:** Coolify is a self-hosted platform for running containers, like Heroku. It is a Laravel 11 application (PHP 8.4, Livewire, PostgreSQL, Redis, WebSockets).
- **How it reaches servers:** it controls one or more servers **over SSH** and runs `docker` and `docker compose` commands on them. Work runs as queued Laravel jobs.
- **What it is not:** it is not an infrastructure cloud. It has no virtual machines it creates itself, no subnets or CIDR planning, no firewall rules as resources, no queues, no CDN and no object storage service. It does create rented servers at Hetzner, DigitalOcean and Vultr through their APIs.
- **Where EnclaveStack differs:**
  - **Typed resources:** EnclaveStack manages AWS-shaped resources in a declarative way. Each resource has a spec and a status, and a reconciler keeps the machine matching the spec (ADR-004).
  - **Three equal surfaces:** the same API serves the console, the CLI and OpenTofu (ADR-003).
  - **Lightweight install:** EnclaveStack is a single Go binary with SQLite (ADR-005), and it runs on a laptop.

## 3 · What we take from it (ideas only), by subsystem

| # | Coolify subsystem (where it lives) | What it does | What EnclaveStack does with the idea |
|---|---|---|---|
| 3.1 | One-click service templates (`templates/compose`, 386 files) | Each template is a Compose file plus metadata: category, tags, port, logo, docs link. "Magic" variables such as `SERVICE_PASSWORD_<NAME>`, `SERVICE_USER_<NAME>`, `SERVICE_BASE64_64_<NAME>` and `SERVICE_URL_<NAME>_<PORT>` generate secrets and URLs on install. The largest categories are productivity (79), media, devtools, AI, CMS and monitoring. | **Catalogue** kind: our own format, where each entry composes EnclaveStack resources (app + database + cache + bucket + secrets) instead of raw Compose. Generated secrets become EnclaveStack `secret` resources, so the console, CLI and OpenTofu see them. We write entries ourselves, starting with the services Galiaraa and typical teams need. |
| 3.2 | Deployment pipeline (`ApplicationDeploymentJob`, ~6,100 lines) | Build packs: prebuilt image, Dockerfile, Compose, Nixpacks, Railpack and static. Git clone, build-time vs run-time variables, pushing images to a registry and preview deployments per pull request. A config snapshot and diff decides whether a rebuild is needed. | **Build from source**: Git or Dockerfile builds with BuildKit, our own build-time/run-time variable split, skipping the build when the commit and config are unchanged, and preview environments later. Keeps the image-only path we already have. |
| 3.3 | Rolling update + health check (`rolling_update`, `health_check`) | Starts the new container and waits for its start period. It then polls Docker's health status until the retries run out. Only when the new container is healthy does it stop the old one. If the app maps host ports, it stops the old container first instead. | **Zero-downtime deploys** in the reconciler: a new revision becomes ready before the old one is removed. The proxy (3.4) makes this possible even for published apps, because traffic goes through the proxy rather than a host port. |
| 3.4 | Reverse proxy (`bootstrap/helpers/proxy.php`, `docker.php` label generators) | One Traefik (or Caddy) container per server. Each app's domains become container labels. Let's Encrypt certificates are issued through ACME, can be force-redirected to HTTPS, and can be backed up and restored. A DNS check runs before issuing. | **Domains + HTTPS** (v0.3): one managed edge proxy. A `domain` resource maps a hostname to an app port, with automatic certificates. The proxy is driven from our reconciler, not from labels. |
| 3.5 | Database lifecycle (`Actions/Database/Start*`) | Each supported engine has its own start logic: generated credentials, optional TLS with an internal CA and per-database certificates, init scripts, and an optional public TCP proxy. | Same features as **options on our database kinds**: TLS with a EnclaveStack CA, init SQL and a public endpoint. We add engines (MySQL, MongoDB, ClickHouse) as their own kinds. |
| 3.6 | Database backups (`DatabaseBackupJob`) | On a schedule it runs `pg_dump`/`pg_dumpall` (or the engine's equivalent) inside the container, compresses the dump and uploads it to S3. Old backups expire by count, age and size. Missed and stale runs are recorded. | **Backups** (v0.3, built): a `backup` resource stores dumps in a EnclaveStack bucket or external S3, with retention and a restore action. Each run is kept in the backup's run history. |
| 3.7 | Volume backups (`VolumeBackupJob`) | Archives Docker volumes and can stop the containers using them for consistency. Streams the archive to S3, recovers incomplete runs and limits the CPU used for compression. | **Volume snapshots** for apps that store files; this is the same mechanism as EBS snapshots later on. |
| 3.8 | Scheduler (`ScheduledJobManager`, `ScheduledTask`) | Cron schedules in the server's time zone. A lock prevents overlapping runs, and skipped runs are recorded with a reason. | **Scheduled job** kind: run a command in an app's image on a cron schedule. |
| 3.9 | Server monitoring (`Sentinel`, `ServerManagerJob`) | A small agent on each server pushes CPU, memory and disk metrics, with configurable retention. The control plane checks connectivity with backoff for unreachable servers and runs disk and patch checks. | **Metrics**: the control plane already exposes Prometheus metrics. Next, per-resource CPU and memory charts and alerts, and later an agent per machine for multi-machine setups. |
| 3.10 | Log drains (`StartLogDrain`) | A Fluent Bit sidecar ships container logs to New Relic, Highlight, Axiom or a custom endpoint. | **Log export** to any OTLP or HTTP endpoint, with our own `logs` API kept as the default. |
| 3.11 | Secret manager links (`VaultService`, `DopplerService`, `InfisicalService`) | Environment variables can point at an external secret manager, and values are resolved at deploy time. | Later: a `secret` resource can be backed by an external manager. Our encrypted local store stays the default. |
| 3.12 | Managed DNS (`Services/Dns/CloudflareDnsProvider`) | Creates and removes DNS records for app domains, with locking and clean-up. | **DNS records** for domains we serve, through provider plug-ins. The Route 53-like part of v0.4. |
| 3.13 | Cloud servers (`HetznerService`, `DigitalOceanService`, `VultrService`, `CloudInitScript`) | Creates and deletes rented VPS machines with SSH keys, firewalls, networks and cloud-init scripts. | Confirms the shape of our **VM** kind (§4, v0.5): image, size, SSH key, network, firewall and cloud-init, but run on the local machine instead of a provider. |
| 3.14 | Tunnels (`ConfigureCloudflared`) | Exposes a server through a Cloudflare Tunnel, with no open ports. | An optional **tunnel** edge for home machines behind NAT. |
| 3.15 | Notifications (Discord, Slack, Telegram, Pushover, email, webhooks) | Deployment and backup events trigger messages, with throttling. | **Notification channels** that subscribe to our existing events. This is the SNS-like piece. |

**What we do differently:** Coolify drives servers by running shell commands over SSH. That gives it great reach but makes it fragile; much of the code exists to escape and validate shell input. EnclaveStack talks to the engine through the Docker API (ADR-006). When we support more than one machine, each machine will run a EnclaveStack agent reached over mutual TLS, not SSH.

## 4 · Service roadmap towards AWS and GCP coverage

Each phase adds resource kinds that work in all three surfaces (console, CLI, OpenTofu) before the phase is done. The order follows what Galiaraa needs to run on EnclaveStack, then what most teams need.

| Phase | EnclaveStack kind | AWS | GCP | Builds on |
|---|---|---|---|---|
| **v0.2 (shipped)** | app, database (PostgreSQL), cache (Redis), bucket, secret | ECS service, RDS, ElastiCache, S3, Secrets Manager | Cloud Run, Cloud SQL, Memorystore, Cloud Storage, Secret Manager | — |
| **v0.3: run a real product** | domain (+ automatic HTTPS), edge proxy | Route 53 alias + ACM + ALB | Cloud DNS + managed certs + Load Balancing | 3.4 |
| | zero-downtime deploys, health-gated | ECS rolling deployment | Cloud Run revisions | 3.3 |
| | backup-policy + restore (database, volume) | RDS snapshots, AWS Backup | Cloud SQL backups | 3.6, 3.7 |
| | scheduled job | EventBridge Scheduler | Cloud Scheduler | 3.8 |
| | build from Git or Dockerfile, image registry | CodeBuild, ECR | Cloud Build, Artifact Registry | 3.2 |
| **v0.4: networking** (specified) | network (CIDR, subnets), private DNS between resources | VPC, subnets, Cloud Map | VPC, subnets, Cloud DNS private zones | — |
| | firewall rules | Security groups | VPC firewall rules | — |
| | load balancer and app replicas (TCP/HTTP) | ALB / NLB | Cloud Load Balancing | 3.4 |
| | DNS records via provider plug-ins | Route 53 | Cloud DNS | 3.12 |
| | certificates through DNS (DNS-01), wildcard domains | ACM DNS validation | Certificate Manager DNS authorization | 3.4, 3.12 |
| **v0.5: compute** (built, unreleased) | vm (image, size, SSH key, cloud-init), volume (VM disks and app folders), snapshot | EC2, AMI, EBS, EFS, key pairs | Compute Engine, persistent disk, Filestore | 3.7, 3.13. Engine: QEMU on macOS and Linux (`L2-007`, ADR-025) |
| | function (later) | Lambda | Cloud Functions | app kind |
| **v0.6: data & messaging** (built, unreleased) | queue, topic (pub/sub) | SQS, SNS | Pub/Sub | NATS JetStream (`L2-008`, ADR-029) |
| | more database engines: MySQL, MongoDB, ClickHouse | RDS MySQL, DocumentDB | Cloud SQL MySQL, Firestore-like | 3.5 |
| | search | OpenSearch | Vertex AI Search / Elastic | — |
| | media transcode job | MediaConvert | Transcoder API | scheduled job + bucket (FFmpeg) |
| | CDN cache in front of buckets and apps | CloudFront | Cloud CDN | edge proxy |
| | email sending | SES | — (partners) | SMTP relay |
| **v0.7: operate** (specified) | metrics, dashboards, alerts | CloudWatch | Cloud Monitoring | 3.9; built into the control plane (`L2-009`, ADR-037) |
| | log export | CloudWatch Logs subscriptions | Cloud Logging sinks | 3.10 |
| | notification channels | SNS / Chatbot | Cloud Monitoring channels | 3.15 |
| | projects (namespaces), users, roles, scoped tokens per project | IAM, accounts | IAM, projects | existing tokens (ADR-035, ADR-036) |
| | external secret back-ends | Secrets Manager integrations | Secret Manager | 3.11 |
| **v0.8: many machines** (built) | machine (agent over mutual TLS), placement, migration | Regions/AZs (conceptually) | Zones | 3.9 + ADR on the agent protocol |
| | catalogue (one-click stacks) | AWS Marketplace / Quick Starts | Marketplace | 3.1 |
| **v0.9: reach it from anywhere** (specified) | tunnel, the way in chosen from the catalogue (quick link, Tailscale, ngrok, custom, Cloudflare, relay on the user's own server), public domains, a gate (sign-in or password), edge machines | CloudFront + ALB in front of a private VPC, Verified Access | Cloud Load Balancing + Identity-Aware Proxy | 3.14; domains (v0.3), zones (v0.4), machines and the mesh (v0.8) (`L2-014`) |

The catalogue (3.1) does not depend on later phases. It can start as soon as v0.3 lands, because entries only compose kinds that already exist.

## 5 · What this changes

- **L1-001:** Coolify becomes the named comparison for "container platform". LocalStack is the comparison for "AWS emulation". Our place sits between them: real services you can run and keep, managed like a cloud.
- **v0.3 is built** (2026-10-21, not yet released): every phase in `L2-005` §5, including builds from Git, works on all three surfaces. It is **specified** in:
  - product: `L2-005`, plus `L2-001` CON-19 – CON-25, `L2-002` CLI-13 – CLI-17 and `L2-003` TF-11 – TF-15;
  - architecture: `L3-003` ADR-015 – ADR-019 and `L3-004` DAR-008 – DAR-011, with `L3-002` §6, `L3-005` §5, `L3-006` §5 and `L3-007` §8;
  - tests: `L5-001` §2a and `L5-002` §6.
- **v0.4 is specified** (2026-10-21), ahead of any code: `L2-006`, plus `L2-001` CON-26 – CON-31, `L2-002` CLI-18 – CLI-23 and `L2-003` TF-16 – TF-21; `L3-003` ADR-020 – ADR-024 and `L3-004` DAR-012 – DAR-016. Certificates through DNS (DNS-01, wildcards), deferred from v0.3, are part of it, because they need the DNS provider plug-ins.

- **v0.6 is specified** (2026-10-21): the whole row (queues, topics, MySQL, MongoDB, ClickHouse, search, transcoders, CDN, email) in `L2-008`, plus `L2-001` CON-37 – CON-44, `L2-002` CLI-29 – CLI-36 and `L2-003` TF-27 – TF-33; `L3-003` ADR-029 – ADR-034 and `L3-004` DAR-021 – DAR-026.
- **v0.6 is built** (2026-10-22), unreleased. It is tested on every surface, on real Docker and by the built-binary e2e. MongoDB defaults to 7.0, because 8.0 refuses Docker Desktop's current kernel. A relay through a real email provider hasn't been walked by hand yet.
- **v0.7 is specified** (2026-10-22): the whole row in `L2-009`, plus `L2-001` CON-45 – CON-53, `L2-002` CLI-37 – CLI-45 and `L2-003` TF-34 – TF-41; `L3-003` ADR-035 – ADR-041 and `L3-004` DAR-027 – DAR-033. Projects are full namespaces (the owner's call: "why wait for later"), with `default` keeping every v0.6 name. Metrics are kept by EnclaveStack itself; secret stores are Vault/OpenBao, 1Password, AWS Secrets Manager, Doppler and Infisical; logs go to buckets, HTTP, OTLP and Loki.
- **v0.9 is specified** (2026-10-24): public access in `L2-014`, plus `L2-001` CON-76 – CON-79, `L2-002` CLI-56 – CLI-58 and `L2-003` TF-51 – TF-53; `L3-003` ADR-051 – ADR-053 and `L3-004` DAR-040 – DAR-042. It answers "how do I open dev and stage from my phone" for a machine at home behind NAT or CGNAT. Chosen with the owner: no hosted EnclaveStack edge and no DNS server of our own; the way in is the user's choice from the catalogue (quick link, Tailscale, Cloudflare, ngrok, the user's own relay, or any other provider as a custom connector); EnclaveStack's own gate on every public app, with Cloudflare Access as an option; a quick link for people without a domain. This is row 3.14 of §3 (Coolify's Cloudflare tunnels), taken further.
- **v0.8 is built** (2026-10-22) on every surface and tested with two machines, the second a Docker-in-Docker agent (`L2-010` §5). It was specified the same day: the whole row in `L2-010`, plus `L2-001` CON-54 – CON-60, `L2-002` CLI-46 – CLI-52 and `L2-003` TF-42 – TF-47; `L3-003` ADR-042 – ADR-048 and `L3-004` DAR-034 – DAR-039. Networks span machines over WireGuard (spiked on Docker Desktop with a Docker-in-Docker second engine), apps without data restart elsewhere when a machine goes offline, and the catalogue has built-in and own templates installed as stacks, all chosen with the owner. The control plane stays on one machine.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler | Coolify study (ideas only, no code reuse) and the phased AWS/GCP service roadmap v0.3 – v0.8 |
| v2 | 2026-10-20 | Soupler | §5 points to the v0.3 specification (L2-005, ADR-015 – ADR-019) |
| v3 | 2026-10-21 | Soupler | v0.3 built; backup kind named as built |
| v4 | 2026-10-21 | Soupler | v0.4 specified (L2-006); DNS-01 and wildcard certificates added to v0.4 |
| v5 | 2026-10-21 | Soupler | v0.5 specified (L2-007): QEMU on both platforms; volumes for VMs and apps |
| v6 | 2026-10-21 | Soupler | v0.5 built (unreleased) |
| v7 | 2026-10-21 | Soupler | v0.6 specified (L2-008): the whole row, with NATS JetStream, OpenSearch and catch + relay email chosen with the owner |
| v8 | 2026-10-22 | Soupler | v0.6 built (unreleased) |
| v9 | 2026-10-22 | Soupler | v0.7 specified (L2-009): projects as namespaces, built-in metrics, five secret stores and four log destinations chosen with the owner |
| v10 | 2026-10-22 | Soupler | v0.8 specified: `L2-010` and its engineering chain |
| v11 | 2026-10-22 | Soupler | v0.8 built |
| v12 | 2026-10-24 | Soupler | v0.9 specified: public access (`L2-014`): tunnels, public domains, the gate, edge machines |
