<div align="center">

<a href="https://enclavestack.soupler.com"><img src="console/public/favicon.svg" alt="EnclaveStack" width="72" height="72" /></a>

# EnclaveStack

### Your own cloud, on your own machines.

**Apps, databases, caches, buckets, VMs and more — on a Mac or Linux box you already have, from a console, a CLI or OpenTofu.**

[![License: Apache-2.0](https://img.shields.io/badge/license-Apache--2.0-2C4BFF)](LICENSE)
[![Go 1.26](https://img.shields.io/badge/go-1.26-00ADD8)](go.mod)
[![Status: pre-release](https://img.shields.io/badge/status-pre--release-orange)](#status)
[![Runs on macOS and Linux](https://img.shields.io/badge/runs%20on-macOS%20%7C%20Linux-555)](#quick-start)

**[Website](https://enclavestack.soupler.com)** · **[Docs](https://enclavestack.soupler.com/docs/)** · **[Services](https://enclavestack.soupler.com/services/)**

[Quick start](#quick-start) · [What you get](#what-you-get) · [Three ways to drive it](#three-ways-to-drive-it) · [Many machines](#many-machines) · [How it works](#how-it-works) · [FAQ](#faq) · [Documentation](#documentation)

</div>

<br />

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/console-dark.png" />
  <img src=".github/assets/console-light.png" alt="The EnclaveStack console overview: what is running, what needs attention, the machines and a chart of each" width="1200" />
</picture>
</div>

---

Paying a cloud bill for side projects, a small team or a homelab? EnclaveStack turns the computers you already own into the services you'd rent:
- run any container as an app;
- give it a PostgreSQL database and a Redis cache;
- store files in S3-compatible buckets;
- keep its keys as encrypted secrets;
- put it behind a domain with HTTPS;
- and back it all up on a schedule.

It is one program. Install it and open the console. Everything after that is a click, a command or a line of OpenTofu away: networks, firewalls, load balancers, VMs, metrics, alerts. Add a second computer and your resources spread across both.

## Quick start

EnclaveStack is one program. Once running, it serves a web console on your computer (port 7070) and creates your databases, apps and buckets as containers on the Docker that is already installed there. Everything runs on your machine.

### Install it

On a Mac or a 64-bit Linux computer, with Docker running:

```bash
brew install enclavestack/tap/enclave                         # with Homebrew
curl -fsSL https://enclavestack.soupler.com/install.sh | sh     # or without it: checks the download before installing
enclave server                                                # then open http://127.0.0.1:7070 and enter the setup code it prints
```

That's all you need: no Go, Node or source code. The install script checks the release's checksums, and its signature too when `cosign` is installed. On a Linux server you can run it as a container instead: `docker run -d -v /var/run/docker.sock:/var/run/docker.sock -v enclave-data:/data -p 7070:7070 ghcr.io/enclavestack/enclave`. Every option, the OpenTofu plug-in and updating are covered in the [user guide, §1a](docs/L5-operations/L5-004-user-guide-v1.md#1a--install-it-without-building).

### Or build it from source

Build it yourself to change EnclaveStack, or to get work that isn't released yet. Pick what you want to do:

| I want to… | Use | You need |
|---|---|---|
| **Look around**: click through the console with sample data, and nothing real is created | [Option 1: try it](#option-1-try-it-with-sample-data) | Go, Node and pnpm |
| **Use it**: run real databases and apps on this computer | [Option 2: run it for real](#option-2-run-it-for-real) | the same, plus Docker running |

Go 1.26+, Node 22.12+ and pnpm 9 are needed by both. Only VMs need **QEMU** (`brew install qemu`, or `qemu-system` and `qemu-utils` on Linux); skip it until you want one.

#### Option 1: try it with sample data

```bash
git clone https://github.com/EnclaveStack/platform.git enclavestack && cd enclavestack
pnpm install     # once: downloads the JavaScript libraries the web console is built from
make demo        # builds the program, starts it with sample data (script: examples/demo/run.sh)
```

When it says *EnclaveStack is running*, open **http://127.0.0.1:7070** and sign in as `admin@enclave.local` with the password `enclave-demo`. It starts with sample apps, databases, a cache, a bucket, a job, a WordPress site and a second project, all on a simulated engine, so no Docker is needed. Press Ctrl-C to stop it and remove everything.

#### Option 2: run it for real

**1. Get the code.** Once:

```bash
git clone https://github.com/EnclaveStack/platform.git enclavestack && cd enclavestack
pnpm install     # downloads the JavaScript libraries the web console is built from
```

**2. Start it.** Make sure Docker is running, then:

```bash
make start
```

This does two things in one go. It **builds** the program (the web console, then the single `enclave` program with the console inside, saved as `./build/enclave`; the first time takes a few minutes), then it **runs** `./build/enclave server`. Leave this terminal open: it prints a web address and a one-time setup code, and EnclaveStack stops when you close it. Your data is kept in `~/.enclave`, so running `make start` again later picks up where you left off.

**3. Create your account.** Open **http://127.0.0.1:7070**, enter the setup code and choose an email and password. The machine name starts as your computer's own name, and you can change it later. You are now in the console, which is empty.

**4. Make something.** Everything below talks to the same server, so what you create one way shows up in the others.

- **The console.** Click **New** in any section. When it is ready, the page shows the connection details (host, port, password) to put in your own code.
- **The terminal.** In a second terminal, in the same folder, run `make install` once. It puts the `enclave` program on your PATH so any terminal understands the word `enclave` (without it, type `./build/enclave` instead). Then:

  ```bash
  enclave login                                   # sign the terminal in as you (asked once)
  enclave db create orders --wait                 # a PostgreSQL database
  enclave app create web --image nginx:1.27-alpine --port 80 --publish --wait   # a web app from a container image
  enclave stack create blog --template wordpress --param domain=blog.localhost --wait   # a WordPress site with its database and storage
  enclave ls                                      # list everything you have
  ```

  `--wait` makes the command wait until the thing is ready.
- **As code, with OpenTofu.** Describe what you want in `.tf` files and let OpenTofu create it. `make tofu` does the set-up for you: it builds the OpenTofu plug-in for EnclaveStack, signs you in (once), keeps a token and runs `tofu`. Try the included example, which makes a database, a cache, a secret and an app that uses all three:

  ```bash
  make tofu CMD=plan        # shows what would be created, and creates nothing
  make tofu                 # creates it (type yes when asked)
  make tofu CMD=destroy     # removes it again
  make tofu DIR=path/to/your/files CMD=plan    # the same for your own .tf files
  ```

**5. Stop it.** Press Ctrl-C in the terminal that is running `make start`. Containers you created keep running until you delete them.

> **The complete guide** — installing every tool, each command in detail, the console, the `enclave` command, OpenTofu, connecting your code, updating, removing everything and troubleshooting — is [`docs/L5-operations/L5-004-user-guide-v1.md`](docs/L5-operations/L5-004-user-guide-v1.md). This quick start is the short version.

### What each command does, and how often you run it

| Command | How often | What it does |
|---|---|---|
| `git clone …` and `pnpm install` | once per download | Gets the code, and the libraries the web console is built from. |
| `make start` | every time you want EnclaveStack running | `make build`, then `./build/enclave server`: runs EnclaveStack for real with Docker. Leave the terminal open. |
| Creating your account in the browser | once | Uses the one-time setup code `make start` prints. |
| `make install` | once, and again after you update the code | Copies `enclave` to a folder on your PATH (`/opt/homebrew/bin` or `/usr/local/bin`, or `BINDIR=…`) so any terminal knows the word `enclave`. |
| `enclave login` | once per computer | Signs this terminal in as you and saves a token in `~/.config/enclave`. |
| `enclave …` commands | whenever you make or change things | The terminal way to use EnclaveStack (below). |
| `make tofu` | whenever you want OpenTofu to apply your `.tf` files | Runs OpenTofu against your server (`examples/opentofu/run.sh`): builds the plug-in, signs in once, then `tofu <CMD>` in `DIR`. |
| `make demo` | only when you want to look around | `make build`, then a server with sample data on a simulated engine (`examples/demo/run.sh`). `DRIVER=docker` makes the samples real containers. |
| `make build` | rarely: `make start` and `make install` do it for you | Builds the console, the `enclave` program (`./build/enclave`) and the OpenTofu plug-in. |

### Using the `enclave` command

Every command has the same shape: `enclave <thing> <what to do> <name> [options]`.

```bash
enclave db create orders --wait      # make a database and wait until it is ready
enclave db list                      # list your databases
enclave db get orders                # show one
enclave db delete orders             # delete it (the data is lost)
enclave ls                           # everything you have, of every kind
enclave app create --help            # every option of a command
enclave --help                       # every kind of thing you can make: apps, databases, caches, buckets, secrets, domains, queues, VMs…
```

Useful options on any command: `-p shop` works in another project, `-o json` prints machine-readable output for scripts, and `--server` or `--token` (or the `ENCLAVE_SERVER` and `ENCLAVE_TOKEN` environment variables) point it at another EnclaveStack, for example from a script or CI.

### Using OpenTofu

OpenTofu is the "as code" way. You describe what you want in a text file, and OpenTofu makes EnclaveStack match it. Run it again after changing the file and only the difference is applied; delete a block and that resource is removed. Because it is a file, you can keep it in Git and review changes like code.

A file of your own looks like this (`main.tf`, in any folder):

```hcl
terraform {
  required_providers {
    enclave = { source = "enclavestack/enclave" }
  }
}

provider "enclave" {}        # finds your server and token from ENCLAVE_SERVER and ENCLAVE_TOKEN, which make tofu sets

resource "enclave_database" "orders" {
  name    = "orders"
  version = "16"
}

resource "enclave_app" "web" {
  name    = "web"
  image   = "nginx:1.27-alpine"
  port    = 80
  publish = true
  env     = { DB_HOST = enclave_database.orders.internal_host }   # one resource feeding another
}
```

Then, from the repo: `make tofu DIR=path/to/that/folder CMD=plan` to see what would change, and `make tofu DIR=path/to/that/folder` to apply it. `examples/opentofu/main.tf` is a larger working example, and every service page on the [website](https://enclavestack.soupler.com) shows its OpenTofu block and every setting it takes.

## What you get

| Service | What it is | Think of it as |
|---|---|---|
| **Apps** | Any container image, or a Git repository with a Dockerfile. Replicas and zero-downtime deploys with health checks | ECS / Cloud Run |
| **Functions** | A container that starts once for each event (a queue or topic message, a new file in a bucket, a schedule or a call) and stops when it's done | Lambda |
| **Previews and deploy on push** | A push to your Git branch redeploys the app; each pull request gets its own temporary copy of the app, deleted when the pull request closes | Amplify previews |
| **Databases** | PostgreSQL, MySQL, MongoDB or ClickHouse, with a managed password; optionally encrypted connections only (TLS), reachable from other computers on your network, and a setup script that runs once | RDS |
| **Caches** | Redis, persisted to disk | ElastiCache |
| **Buckets** | S3-compatible object storage; browse, upload and download files from the console, CLI or OpenTofu; each app or job can get keys of its own that reach only the buckets it needs, read-only or read-write | S3 + IAM roles for each service |
| **Secrets** | Encrypted values your apps get as environment variables, or read from Vault, 1Password, AWS, Doppler or Infisical | Secrets Manager |
| **Domains** | Hostnames with automatic HTTPS (Let's Encrypt, or a local CA) | ALB + ACM |
| **Jobs and backups** | Scheduled commands, and scheduled copies of databases, caches and volumes' files, with one-click restore | EventBridge + AWS Backup |
| **Networks and firewalls** | Private networks with subnets and private DNS, and allow rules between resources | VPC + security groups |
| **Load balancers and DNS** | HTTP or TCP balancing across replicas, and DNS zones and records at Cloudflare or Route 53 | ELB + Route 53 |
| **VMs, volumes and snapshots** | Whole machines on QEMU with SSH, plus disks and folders that outlive what uses them | EC2 + EBS |
| **Queues, topics, search, email, CDN and transcoding** | NATS JetStream, OpenSearch, SMTP with a message viewer, a caching edge and FFmpeg; transcoders make presets, renditions of your size and bitrate and adaptive HLS ladders, and send a signed webhook or topic message when each file is done | SQS, SNS, OpenSearch, SES, CloudFront, MediaConvert |
| **Operate** | Projects, people and roles, metrics and dashboards, alerts on metrics or on events as they happen (email, Slack, Discord, Telegram or webhooks) and log export | IAM + CloudWatch |
| **Public access** | Your apps from anywhere through a quick link, Tailscale, ngrok, Cloudflare or a server of your own, always behind a sign-in gate | API Gateway + CloudFront |
| **Catalogue** | WordPress, Ghost, n8n, Uptime Kuma, Gitea and Plausible, or your own template, installed as one stack; connections keep the keys of Supabase, Neon, Firebase, Auth0, Clerk or Sentry as secrets for your apps | Marketplace |

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/catalogue-dark.png" />
  <img src=".github/assets/catalogue-light.png" alt="The catalogue: ready-made stacks to install" width="1000" />
</picture>
</div>

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/function-dark.png" />
  <img src=".github/assets/function-light.png" alt="A function in the console: its trigger, last run and the Invoke and Run now actions" width="1000" />
</picture>
</div>

## Three ways to drive it

Everything goes through one API, and every capability works from all three. Use whichever fits the moment, and switch any time: each resource's page in the console shows its CLI command and its OpenTofu block.

<table>
<tr><th>Console</th><th>CLI</th><th>OpenTofu / Terraform</th></tr>
<tr valign="top">
<td>

Open `http://127.0.0.1:7070`: lists with search, live progress, logs, metrics and one-click actions.

</td>
<td>

```bash
enclave db create orders --wait
enclave app create api \
  --image ghcr.io/acme/api:1.4 \
  --port 8080 \
  --secret-env DB_PASSWORD=orders-password
```

</td>
<td>

```hcl
resource "enclave_database" "orders" {
  name = "orders"
}

resource "enclave_app" "api" {
  name  = "api"
  image = "ghcr.io/acme/api:1.4"
  port  = 8080
}
```

</td>
</tr>
</table>

See [`examples/opentofu`](examples/opentofu) for a full configuration.

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/app-dark.png" />
  <img src=".github/assets/app-light.png" alt="An app in the console: its deploy stages, address, machine and actions" width="1000" />
</picture>
</div>

## Many machines

Got an old laptop or a mini PC? Join it, and EnclaveStack places new resources wherever there is room.

```bash
enclave machine add rack-1 --label disk=ssd     # prints a one-time join command
# on the other computer:
enclave agent join http://<main machine>:7070 --code <code>
```

- **Placement**: by free memory, by name (`--machine rack-1`) or by label (`--machine-label disk=ssd`).
- **Private networks span machines** over an encrypted WireGuard tunnel, so an app on one reaches a database on the other by name.
- **When a machine goes offline**, its apps restart elsewhere, and data waits for its machine.
- **Move** anything, data included: `enclave move database/orders --to main`. **Drain** a machine before you unplug it.
- **A standby** keeps a copy of everything EnclaveStack knows, sent every 15 seconds. If the main machine is lost, run `enclave standby promote` on the standby and it takes over. Add one with `enclave machine add spare --role standby`.

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/machines-dark.png" />
  <img src=".github/assets/machines-light.png" alt="Machines: the main machine online, a second one waiting to join, and a standby" width="1000" />
</picture>
</div>

## How it works

```mermaid
flowchart LR
  subgraph surfaces[You]
    C[Console] & L[enclave CLI] & T[OpenTofu]
  end
  surfaces -->|one HTTP API| CP[Control plane<br/>API · reconciler · SQLite]
  CP -->|Docker Engine| M1[Main machine<br/>containers · VMs · edge]
  CP -->|agent over mutual TLS| M2[Other machines]
  M1 <-->|WireGuard| M2
```

You describe what you want, and the **control plane** stores it. A **reconciler** then makes it happen, retrying until it succeeds, and puts it back if something drifts. Resources run as Docker containers on your machines, and VMs run under QEMU. State lives in one SQLite file. Secrets are encrypted with a master key that never leaves your machine, and nothing phones home.

## Status

| Release | What it adds | State |
|---|---|---|
| v0.2 | Apps, databases, caches, buckets and secrets; the console, the CLI and the OpenTofu provider | Built |
| v0.3 | Zero-downtime deploys, domains with HTTPS, jobs, backups and restore, builds from Git | Built |
| v0.4 | Networks, firewalls, replicas and load balancers, DNS zones and records | Built |
| v0.5 | VMs, volumes and snapshots | Built |
| v0.6 | MySQL, MongoDB, ClickHouse, queues, topics, search, email, CDN and transcoding | Built |
| v0.7 | Projects, roles, metrics, dashboards, alerts, log export and outside secret stores | Built |
| v0.8 | Many machines, moving resources and the catalogue | Built |
| v0.9 | Public access (tunnels, the gate, edge machines), connections (Supabase) and installing without building | Released as 0.9.0 |
| v0.10 | Functions, deploy on push and previews, a standby that takes over, database TLS and setup scripts, volume backups, event alerts, and connections for Neon, Firebase, Auth0, Clerk and Sentry | Released as 0.10.0 |

Every release above is tested on all three surfaces and on real Docker. Not yet tried by hand:
- two physical computers joined over a network, including a standby taking over;
- a real GitHub or GitLab webhook making a preview;
- a few real third-party services: Slack, Discord and Telegram, a real email relay, 1Password, AWS, Doppler and Infisical, and the six connections with real accounts.

The full roadmap is in [`L1-002`](docs/L1-strategy/L1-002-coolify-study-and-service-roadmap-v1.md).

## FAQ

<details>
<summary><strong>Who is this for?</strong></summary>

Developers, small teams and homelabbers who want cloud-style services without a cloud bill or a cloud account, and without hand-writing Docker Compose files for every project.
</details>

<details>
<summary><strong>Does anything leave my machine?</strong></summary>

No: there is no account and no telemetry. Data leaves only where you send it:
- images you pull;
- certificates you request;
- destinations you set up yourself, such as backups to S3, alerts to Slack or log export.
</details>

<details>
<summary><strong>How is this different from Coolify or Dokku?</strong></summary>

Those focus on deploying apps. EnclaveStack aims at the whole small cloud: databases, buckets, networks, firewalls, VMs, queues, metrics and alerts. The console, the CLI and an OpenTofu provider all share one API, so you can click today and keep it as code tomorrow.
</details>

<details>
<summary><strong>Mac or Linux?</strong></summary>

Both. On a Mac, it uses Docker Desktop (or another Docker Engine), and QEMU with Apple's Hypervisor framework for VMs. On Linux, it uses Docker, and QEMU with KVM.
</details>

<details>
<summary><strong>Can I try it without Docker?</strong></summary>

Yes. `enclave server --driver fake` simulates the engine. For the console alone, `cd console && VITE_ENCLAVE_API=mock pnpm dev` runs it against an in-browser demo; sign in with `admin@enclave.local` / `enclave-demo`.
</details>

<details>
<summary><strong>Is it production-ready?</strong></summary>

Not yet: it is pre-release. Releases are published and every one is built and tested, but until 1.0 there is no upgrade promise: a 0.x release may change the API, the CLI or the provider.
</details>

## Documentation

- **Guides and every service, explained**: [enclavestack.soupler.com/docs](https://enclavestack.soupler.com/docs/)
- **Architecture**: [`L3-001`](docs/L3-architecture/L3-001-system-architecture-v1.md)
- **API**: [`L3-002`](docs/L3-architecture/L3-002-console-api-contract-v1.md) and [`api/openapi.yaml`](api/openapi.yaml)
- **Security**: [`L3-006`](docs/L3-architecture/L3-006-security-architecture-v1.md)
- **Local setup and troubleshooting**: [`L4-001`](docs/L4-infrastructure/L4-001-local-dev-setup-v1.md)
- **Using every feature, start to finish**: [the user guide, `L5-004`](docs/L5-operations/L5-004-user-guide-v1.md) (§17 covers v0.10)
- **What each feature does, step by step**: [`L5-001`](docs/L5-operations/L5-001-feature-test-playbook-v1.md)
- **Changes**: [`CHANGELOG.md`](CHANGELOG.md)

## Contributing

Issues and pull requests are welcome. **Read [CONTRIBUTING.md](CONTRIBUTING.md) first**: how to report a bug or suggest a feature, how work is tracked, and how a pull request gets reviewed and merged. In short: branch from `develop` (`<type>/<slug>`), open the PR against `develop`, and fill in every section of the template. Questions go to [Discussions](https://github.com/EnclaveStack/platform/discussions), security problems to [SECURITY.md](SECURITY.md), and everyone follows the [Code of Conduct](CODE_OF_CONDUCT.md).

This repository is governed by a layered documentation system. Read [`CLAUDE.md`](CLAUDE.md) first: it is the contract for people and coding agents alike. The standards are in [`docs/L0-foundation/`](docs/L0-foundation/).

```bash
bash bin/install-hooks.sh                    # once per clone
pnpm lint && pnpm type-check && pnpm test    # console + Go (race detector)
make provider-acceptance                     # OpenTofu acceptance (needs tofu)
make engine-docker && make e2e               # real Docker + OpenTofu
make e2e-machines                            # two machines (Docker-in-Docker)
bash bin/statutory-integrity.sh              # the governance gate CI runs
```

The repository holds:
- the Go control plane, agent and CLI (`cmd/`, `internal/`, `pkg/`);
- the OpenTofu provider;
- the web console (`console/`) and its design system (`packages/ui`).

## License

[Apache License 2.0](LICENSE); see also [`NOTICE`](NOTICE). Contributions are accepted under the same licence. The licence doesn't grant use of the EnclaveStack name or logo.
