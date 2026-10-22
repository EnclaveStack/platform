# platform

EnclaveStack monorepo: Go control plane, machine agent and CLI, the TypeScript web console, and the `@enclavestack/ui` design system (`packages/ui`).

Part of **EnclaveStack**. Role: Product codebase (apps, services, shared packages).

## What it is

EnclaveStack turns a Mac or Linux machine — or several — into your own small cloud: **apps** (any container image), **PostgreSQL databases**, **Redis caches**, **S3-compatible buckets** and **encrypted secrets**, plus private **networks**, **firewalls**, **load balancers**, **DNS records**, **VMs**, **volumes**, **snapshots**, and **projects, roles, metrics, alerts, log export and outside secret stores** to run it day to day, **more machines** joined over an encrypted tunnel, and a **catalogue** of ready-made stacks — managed from three equal surfaces over one API:

| Console | CLI | OpenTofu |
|---|---|---|
| `http://127.0.0.1:7070` | `enclave db create orders --wait` | `resource "enclave_database" "orders" { name = "orders" }` |

## Status

| Part | State |
|---|---|
| Control plane (Go) — API, reconciler, Docker driver | v0.2 – v0.8 built (unreleased; nothing published yet) |
| CLI (`enclave`) | v0.2 – v0.8 built (unreleased; nothing published yet) |
| OpenTofu / Terraform provider (`enclavestack/enclave`) | v0.2 – v0.8 built (unreleased; nothing published yet); local builds (registry publishing at public launch) |
| Web console | v0.2 – v0.8 built (unreleased; nothing published yet), on the `@enclavestack/ui` design system |
| Zero-downtime deploys with health checks, domains + automatic HTTPS, scheduled jobs, backups + restore, builds from Git | v0.3, built and tested on every surface (unreleased) |
| Networks with subnets and private DNS, firewalls (nftables), app replicas and load balancers (HAProxy), DNS zones and records (Cloudflare, Route 53), certificates through DNS-01 and wildcard domains | v0.4, built and tested on every surface (unreleased); the DNS providers are tested against local fakes of their APIs, not the live services |
| VMs run by QEMU (HVF on macOS, KVM on Linux) in your networks with SSH and first-boot scripts, volumes (disks for VMs, folders for apps and jobs), snapshots | v0.5, built and tested on every surface (unreleased); walked live on Apple silicon; Linux with KVM runs in CI from v0.5 on |
| MySQL, MongoDB and ClickHouse databases; queues and topics (NATS JetStream) with dead letters and signed webhooks; search (OpenSearch); email that is kept to look at or relayed through your provider (Mailpit); a CDN in front of an app or bucket (nginx); transcoders that convert media dropped in a bucket (FFmpeg) | v0.6, built and tested on every surface and on real Docker (unreleased); a relay through a real email provider is not yet walked by hand |
| Projects (namespaces with their own names and private DNS), people with viewer / editor / admin roles per project, invites and tokens limited to a role and projects; metrics sampled by EnclaveStack itself with graphs and dashboards; alerts (metric thresholds, crash loops, failures) to email, webhooks, Slack, Discord or Telegram; log export to a bucket, S3, HTTP, OpenTelemetry or Grafana Loki; secrets read from Vault / OpenBao, 1Password, AWS Secrets Manager, Doppler or Infisical | v0.7, built and tested on every surface and on real Docker (unreleased); real Slack/Discord/Telegram/email delivery and the real 1Password, AWS, Doppler and Infisical services are not yet walked by hand (Vault and OpenBao are verified for real) |
| More machines joined with `enclave agent join` (mutual TLS); resources placed on a machine by free memory, a name or labels; private networks across machines over WireGuard; apps restarted elsewhere when a machine goes offline; moving a resource with its data; draining a machine; a catalogue of six built-in stacks (WordPress, Ghost, n8n, Uptime Kuma, Gitea, Plausible) plus your own templates, installed as one `stack` | v0.8, built on every surface (unreleased); two machines tested for real with a Docker-in-Docker second machine; two physical computers not yet walked by hand |

## Quick start (from source)

Needs Go ≥ 1.26, Node ≥ 22.12 + pnpm 9.15.9, and Docker. VMs also need QEMU 8 or later (`brew install qemu`; on Linux `qemu-system` and `qemu-utils` with access to `/dev/kvm`).

```bash
pnpm install
make build                                   # build/enclave (console embedded) and build/terraform-provider-enclave
./build/enclave server                     # prints the console URL and a one-time setup code
# open http://127.0.0.1:7070, enter the setup code, create the admin
./build/enclave login                      # saves a token for the CLI
./build/enclave db create orders --wait
./build/enclave app create web --image nginx:1.27-alpine --port 80 --publish --wait
./build/enclave vm create box --image debian-12 --ssh-key ~/.ssh/id_ed25519.pub --wait
./build/enclave vm ssh box
./build/enclave queue create jobs --wait && ./build/enclave queue send jobs hello
./build/enclave ls
```

No Docker? `./build/enclave server --driver fake` simulates the engine.
OpenTofu: see `examples/opentofu/README.md`.

## Develop and test

```bash
pnpm lint && pnpm type-check && pnpm test    # console + Go (race detector)
make lint                                    # vet + staticcheck + console lint
make provider-acceptance                     # OpenTofu acceptance (needs tofu)
make console-e2e                             # Playwright, real server (needs Chrome)
make engine-docker && make e2e               # real Docker + OpenTofu (and a VM, with QEMU)
make engine-qemu                             # real QEMU (skips without it)
make load                                    # k6 performance thresholds
```

Architecture: `docs/L3-architecture/L3-001-system-architecture-v1.md`. API: `docs/L3-architecture/L3-002-console-api-contract-v1.md` and `api/openapi.yaml`. Local setup and troubleshooting: `docs/L4-infrastructure/L4-001-local-dev-setup-v1.md`.

## Governance

This repository follows EnclaveStack's documentation-governance system. Read `CLAUDE.md` first — it is the contract for both humans and coding agents.

```bash
bash bin/install-hooks.sh          # once per clone: commit-msg + pre-commit hooks
bash bin/statutory-integrity.sh    # the gate CI runs on every PR
```

Standards live in `docs/L0-foundation/`. Start with `L0-006-change-propagation-governance-v1.md` (what to edit, in what order).

## License

Apache License 2.0: see [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE). Contributions are accepted under the same licence. The licence doesn't grant use of the EnclaveStack name or logo.

## Contributing

Branch from `develop` (`<type>/<slug>`), open the PR against `develop`, fill in every section of the PR template (including `## Docs update`). Only the `develop` → `main` promotion targets `main`.
