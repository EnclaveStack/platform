# L4-001 · Local Development Setup & Smoke Runbook

---
| Field | Value |
|---|---|
| Document ID | L4-001-local-dev-setup-v1 |
| Layer | L4 — Infrastructure & Deployment |
| Status | Draft |
| Version | v12 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-22 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs. All rights reserved. |
| License | MIT |
---

## Purpose

How to run platform locally and prove it works. Update whenever prerequisites, bootstrap, smoke commands or troubleshooting change (L0-006 §3.4).

## 1 · Prerequisites

| Tool | Version | Needed for |
|---|---|---|
| Go | ≥ 1.26 (1.27 used; `go.mod` sets the minimum) | Control plane, CLI, provider, Go tests |
| Node + pnpm | Node ≥ 22.12, pnpm 9.15.9 | Console |
| Docker (Desktop or Engine) | ≥ 24 | Docker driver, engine e2e tests (not needed with `--driver fake`) |
| OpenTofu | ≥ 1.6 (1.12 used) | Provider acceptance tests and examples |
| Google Chrome | any current | Console Playwright e2e |
| k6 | ≥ 0.50 (2.3 used) | Load tests |

No env file is required. Optional: `console/.env.local` (`VITE_ENCLAVE_API`, see `console/.env.example`).

## 2 · Bootstrap

```bash
pnpm install --frozen-lockfile
bash bin/install-hooks.sh
make build            # console → console/dist → embedded; build/enclave and build/terraform-provider-enclave
```

### Run the control plane and console

```bash
./build/enclave server --driver fake --data-dir /tmp/hc      # no Docker needed; prints the setup code
./build/enclave server                                      # real Docker driver, data in ~/.enclave
# console: http://127.0.0.1:7070 (served by the binary)
pnpm dev                                                    # console dev server on :5173 with hot reload, proxying /api to :7070
VITE_ENCLAVE_API=mock pnpm dev                            # console alone against the in-memory mock
```

### CLI and OpenTofu against it

```bash
./build/enclave login --server http://127.0.0.1:7070
make provider-dev                                           # writes ~/.tofurc.enclave with a dev_overrides block (your ~/.tofurc is untouched)
export TF_CLI_CONFIG_FILE=$HOME/.tofurc.enclave
export ENCLAVE_TOKEN=$(./build/enclave token create --name opentofu)
cd examples/opentofu && tofu plan
```

## 3 · Smoke

```bash
pnpm lint && pnpm type-check && pnpm test     # console + go vet + go test -race
bash bin/statutory-integrity.sh
```

Full suites (provider acceptance, console e2e, engine e2e, load): `L4-002` §2.

## 4 · Known local limitations

- The Docker driver publishes ports on `127.0.0.1`; other machines cannot reach them unless the server runs with `--publish-address 0.0.0.0`.
- On macOS, Docker runs in a VM: container memory counts against Docker Desktop's limit, not the Mac's.
- The fake driver simulates provisioning; it proves the control plane, not the engines. The engine e2e suite proves the engines.

## 5 · Troubleshooting

| Symptom | Fix |
|---|---|
| `engine.available=false`, banner in the console | Start Docker Desktop; queued work resumes automatically |
| Setup code lost | Restart the server: until the admin account exists, every start prints a fresh setup code and the old one stops working |
| `tofu` uses a registry provider instead of the local build | Run `make provider-dev` and `export TF_CLI_CONFIG_FILE=$HOME/.tofurc.enclave` |
| Docker driver says the engine is unavailable on macOS | Start Docker Desktop. Without `DOCKER_HOST`, EnclaveStack uses `/var/run/docker.sock`, else Docker Desktop's `~/.docker/run/docker.sock` |
| A bucket fails at the `objectstore` step | The object store image must be pullable: `docker pull` the digest in `internal/engine/docker` (`DefaultObjectStoreImage`, ADR-013) |
| `listen on 127.0.0.1:7070: address already in use` | Another `enclave server` is running; stop it or pass `--listen 127.0.0.1:7171` |

## 6 · Server flags for development and automation

| Flag | Default | Use |
|---|---|---|
| `--driver docker\|fake` | `docker` | `fake` simulates provisioning without Docker (demos, tests, load) |
| `--data-dir` | `~/.enclave` | Database, master key, and `tmp/` (0700), where a restore verifies a backup's checksum before using it; plan free disk for the largest backup you will restore |
| `--listen` | `127.0.0.1:7070` | API and console address |
| `--console-url` | none | The address people open the console at (e.g. `https://enclave.example.com` behind your TLS proxy). Alert notifications link to it and invite links use it. Without it, alerts carry no link when the server listens on loopback, and invite links use the address the admin is on |
| `--publish-address` | `127.0.0.1` | Where published ports bind |
| `--console-dir` | embedded, else `./console/dist` | Serve a console build from disk |
| `--rate-limit` | `100` | Requests/s per client (load tests raise it) |
| `--log-level`, `--log-format` | `info`, `auto` | JSON or text logs |
| `--fake-step-delay`, `--fake-faults` (hidden) | `300ms`, — | Fake driver timing and fault injection (`name:transient=N;other:permanent`) |
| `--setup-code-file` (hidden) | — | Also write the setup code to a 0600 file, for e2e automation |
| `--drift-interval`, `--poll-interval` (hidden) | `60s`, `2s` | Faster reconciler timing for tests |

## v0.3 prerequisites (planned)

- **Ports 80 and 443** must be free on the machine for the edge proxy. On macOS, check with `lsof -nP -iTCP:80 -sTCP:LISTEN` and `lsof -nP -iTCP:443 -sTCP:LISTEN`. EnclaveStack binds them only once the first domain exists.
- **`*.localhost` hostnames** resolve to the machine in current browsers and in `curl` with no DNS setup, and use the internal CA. To trust that CA:
  - **macOS:** `enclave domain ca > es-root.pem && security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db es-root.pem`
  - **Linux:** install it per your distribution's CA store.
- **The edge binds 127.0.0.1 by default.** Serving domains to other devices needs `enclave server --edge-address 0.0.0.0`. `--edge-http-port` and `--edge-https-port` move it off 80/443, for example when something else uses them.
- **Public HTTPS** (`tls: auto`) needs `--edge-address 0.0.0.0`, a public IP, ports 80 and 443 forwarded to the machine, and an A/AAAA record pointing at it. Set the address shown in DNS instructions with `enclave machine set --public-address <ip>`.
- **Automated ACME tests** run Pebble (Let's Encrypt's test server) in Docker; they need no public access.
- **Backups** need no extra setup with a EnclaveStack bucket. External S3 needs an `https://` endpoint and a secret holding `{"accessKey":"…","secretKey":"…"}`. `http://` is allowed only for `localhost` and `127.0.0.1`, for example a local MinIO.
- **Test suites leave nothing behind.** `make engine-docker` and `make e2e` remove the `es-system-probe` helper they started. If a real EnclaveStack was already running on the machine, its probe is left alone.
- **Builds** need BuildKit, the default in Docker Desktop and Docker Engine ≥ 23 (spiked on 29.7 and 29.9). Nothing else is installed: the control plane clones with go-git and sends the context over the build session. Clones go to the data dir's `tmp/` and are removed after each build; built images are `enclave.local/<app>:r<revision>-<commit>`, two per app.
- **Build tests** need `git` on the PATH (`internal/build` and `make e2e` serve real repositories with `git http-backend`). Without it they skip.

## v0.4 prerequisites (planned)

- **Networks** use private ranges. Pick CIDRs that don't overlap your LAN or VPN, or Docker refuses them (the error names the conflicting network). `docker network ls --filter label=enclave.managed` lists EnclaveStack's.
- **Firewalls** need the kernel's nftables support inside containers. Docker Desktop (macOS) and current Linux kernels have it (spiked on Docker Desktop 29.7.2). The helper image `enclave.local/netfilter` is built on first use from Alpine and needs network access once.
- **Load balancers** pull HAProxy 3.2 (pinned by digest) on first use.
- **DNS providers**: a Cloudflare API token with `Zone:Read` and `DNS:Edit` on the zone, or a Route 53 IAM user limited to `route53:GetHostedZone`, `route53:ListHostedZonesByName`, `route53:ListResourceRecordSets` and `route53:ChangeResourceRecordSets` on the hosted zone. Store it as a EnclaveStack secret (`{"apiToken":"…"}` or `{"accessKeyId":"…","secretAccessKey":"…"}`). None of this is needed for the automated tests.
- **DNS-01 tests** reuse Pebble and challtestsrv from the v0.3 ACME suite.

## v0.5 prerequisites (checked 2026-10-21)

- **QEMU 8 or later** for VMs, on the machine that runs `enclave server`, not in a container:
  - **macOS:** `brew install qemu`. It uses Apple's Hypervisor framework; no setup or `sudo` is needed. Apple silicon runs arm64 images, and Intel Macs run amd64 images.
  - **Linux:** `sudo apt install qemu-system qemu-utils` (or your distribution's packages), with read-write access to `/dev/kvm`: `sudo usermod -aG kvm $USER`, then log in again. On arm64, also install the UEFI firmware (`qemu-efi-aarch64`).
  - `enclave machine` says whether VMs can run, and if not, what's missing. `--qemu <path>` points at a QEMU that isn't on the PATH.
- **Disk space:** the first VM downloads its image once (Debian 12 about 350 MB, Ubuntu 24.04 about 600 MB) into the data dir's `images/`. Root disks start at the size of what the VM writes. Snapshots are full copies under `snapshots/`, so each takes the space of its source's data.
- **Keep VMs running across server restarts.** QEMU runs in its own session. If you run the server under a service manager, don't let it kill the whole group: systemd `KillMode=process`, launchd `AbandonProcessGroup = true`.
- **The VM link image** `enclave.local/vmlink` is built on first use from pinned `golang` and `alpine` images, so it needs network access once, like the firewall helper.
- **Real-QEMU tests** (`make engine-qemu`) download the Debian 12 image once into `~/.cache/enclave-test/` and skip with the reason when QEMU or acceleration is missing. `make e2e` includes a VM when the machine can run one; that downloads the image again into the test's data dir.
- **SSH to a VM**: `enclave vm ssh <name>` runs your `ssh` with your agent and default keys. To use another key, add it to your agent (`ssh-add <key>`): arguments after `--` are the command run in the VM, not options for `ssh`.
- **Stuck VM process?** `cat <data dir>/vm/<id>/qemu.pid` gives the exact PID to look at; never kill QEMU processes by name.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Initial skeleton from soupler-governance-template v1.2.1 |
| v2 | 2026-10-20 | Soupler Labs | Console prerequisites, bootstrap, dev server, mock vs http modes, smoke, limitations |
| v3 | 2026-10-20 | Soupler Labs | Go, Docker, OpenTofu, Chrome, k6 prerequisites; build, server, CLI and provider dev workflow; troubleshooting |
| v4 | 2026-10-20 | Soupler Labs | Go 1.26 minimum; build/ output; provider dev override via TF_CLI_CONFIG_FILE; server flags for automation; more troubleshooting |
| v5 | 2026-10-20 | Soupler Labs | v0.3 prerequisites (planned): ports 80/443, *.localhost and the local CA, public HTTPS, Pebble, build engine |
| v6 | 2026-10-21 | Soupler Labs | Edge flags (--edge-address, --edge-http-port, --edge-https-port) |
| v7 | 2026-10-21 | Soupler Labs | Data-dir tmp for restores; backup prerequisites; test suites clean up the probe helper |
| v8 | 2026-10-21 | Soupler Labs | Build prerequisites |
| v9 | 2026-10-21 | Soupler Labs | v0.4 prerequisites (planned): network ranges, nftables in containers, HAProxy image, DNS provider credentials, DNS-01 tests |
| v10 | 2026-10-21 | Soupler Labs | v0.5 prerequisites (planned): QEMU per platform, disk space, service-manager kill mode, VM link image, real-QEMU tests |
| v11 | 2026-10-21 | Soupler Labs | v0.5 prerequisites checked on an Apple M4; SSH and stuck-process notes |
| v12 | 2026-10-22 | Soupler Labs | `--console-url`: where alert and invite links point |
