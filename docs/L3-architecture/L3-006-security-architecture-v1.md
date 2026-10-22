# L3-006 · Security Architecture and Threat Model

---
| Field | Value |
|---|---|
| Document ID | L3-006-security-architecture-v1 |
| Layer | L3 — Architecture |
| Status | Draft |
| Version | v11 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-22 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs |
| License | Apache-2.0 |
---

## Purpose

How EnclaveStack protects the machine, its data and its secrets, and the threats each control answers (STRIDE). Controls are decided in ADR-007 and ADR-010 – ADR-012; verification is the auth and security test suite (`L5-002` §3).

## 1 · Trust boundaries

1. **Network → control plane.** Default listen address `127.0.0.1:7070`: nothing off the machine can reach it unless the operator passes `--listen 0.0.0.0:7070` (then TLS via a reverse proxy is required; the console warns when served over plain HTTP to a non-loopback address).
2. **Control plane → Docker.** The process can control Docker, which is root-equivalent on Linux. Only the control plane holds that power; clients never reach the Docker socket.
3. **Containers → each other.** Managed containers share the `enclave` network; published ports bind to `127.0.0.1` unless `--publish-address` is set.
4. **Data directory.** `~/.enclave` (0700) holds the database (0600) and `master.key` (0600, 32 random bytes).

## 2 · STRIDE

| Threat | Example | Control |
|---|---|---|
| **S**poofing | Someone claims a fresh install first | One-time setup code printed only to the server's terminal; setup endpoint closed afterwards |
| Spoofing | Password guessing | argon2id hashing; 5 failures / 15 min per email + IP → 429; uniform error for unknown email or wrong password; constant-time comparisons |
| Spoofing | Stolen token | Tokens hashed at rest, prefix shown for identification, scope + expiry, revocation, last-used visibility |
| **T**ampering | Cross-site request from another tab | `SameSite=Strict` cookie + mandatory `X-Enclave-CSRF` header on cookie writes + no CORS |
| Tampering | Lost update from two editors | `If-Match` / generation (ADR-010) |
| **R**epudiation | "Who revealed that secret?" | Events for every create/update/delete/action/reveal with the principal (user or token id) |
| **I**nformation disclosure | Secret in logs, events, errors or API responses | Values only in `secret_ciphertext`; write-only in the API; log redaction (ADR-011); tests assert absence |
| Information disclosure | Database file copied | Secrets AES-256-GCM encrypted with `master.key`; passwords and tokens only as hashes |
| Information disclosure | Internals in 500s | `INTERNAL` with request ID only; stack in server log |
| **D**enial of service | Request floods, huge bodies | Per-client rate limit, 1 MiB body limit, HTTP timeouts, bounded worker pool, `tail` ≤ 1000 |
| **E**levation of privilege | Read token used to delete | Scope check per route; token management needs session or write token |
| Elevation | Container escape via arbitrary image | Out of EnclaveStack's control (Docker's isolation); documented: run only images you trust. No `--privileged`, no host network, no host mounts are ever set |

## 3 · Cryptography

| Use | Algorithm | Parameters |
|---|---|---|
| Passwords | argon2id | m = 64 MiB, t = 3, p = 2, 16-byte salt, 32-byte key |
| Sessions, tokens, setup code | SHA-256 of 32 random bytes | Random from `crypto/rand` |
| Secrets at rest | AES-256-GCM | 12-byte random nonce per seal; key = `master.key` |
| Generated credentials | 24 random bytes, base64url | Database and object-store passwords |

## 4 · Known limitations (v0.2)

- One admin user; no roles.
- Buckets share the object store's root credentials (`objectstore-credentials`); per-bucket credentials are planned.
- Plain HTTP on loopback; TLS for remote access is the operator's reverse proxy until domains/HTTPS ship in v0.3.
- `master.key` sits next to the database; a full-disk thief gets both. Keychain / KMS integration is planned.
- Caches (Redis) have no password: they are reachable only on the `enclave` network and on loopback-published ports. A managed cache password (like databases have) is planned.
- The object store runs Chainguard's build of MinIO pinned by digest (ADR-013); its provenance is Chainguard's, not MinIO Inc.'s.

## 5 · v0.3 additions (planned)

| Area | Threat | Control |
|---|---|---|
| Edge proxy (ADR-015) | The machine is now reachable from the internet on 80/443 | The edge binds 127.0.0.1 unless the operator starts the server with `--edge-address 0.0.0.0`; public (`auto`) domains refuse to proceed on a loopback edge. Only hostnames with a domain resource are routed; unknown hosts get a 404 with a fixed body. The control plane itself is never routed through the edge unless the operator adds a domain for it. Caddy's admin endpoint listens only inside its container. Note: anyone who can reach an open edge can send any `Host` header, so `.localhost` domains are reachable from the network too once the edge is opened |
| Edge configuration | A hostname crafted to inject proxy configuration | Hostnames are validated (`L3-002` §6.5) and the config is built as JSON values, not templated text |
| Internal CA | The root key leaks and can sign certificates for any name | The CA key stays in `es-system-edge-data` and never leaves the container. Only the root **certificate** is served (`GET /machine/ca`, authenticated). Trusting it is the operator's choice, and the console explains the scope |
| ACME | The account email exposes personal data to the CA | The email is optional (`acmeEmail`), and the console states it is sent to Let's Encrypt |
| Engine exec (probes, backups, reloads) | Command injection through user input | Exec takes argument arrays, never a shell string. User-supplied `command` health checks run inside the user's own container, with the same rights as the app. The database password reaches `pg_dump` through the exec environment, never in argv |
| Jobs | Secrets in the output tail | Run detail needs a session or a write token, like reveal. The console warns that output is stored as printed |
| Backups | Dumps contain all application data | A bucket destination has the object store's access controls. An external S3 credential is a EnclaveStack secret (encrypted at rest). Restore `replace` needs the source name typed and takes a safety backup first. Encryption of dumps at rest is the destination's responsibility in v0.3 (known limitation) |
| Builds (ADR-019) | Malicious repository or Dockerfile | Builds run in the engine's isolation, with no host mounts or privileged mode. Clone credentials come from a secret, are used in memory, and are never written to the clone directory or the logs. SSH requires `knownHosts` (no trust-on-first-use). Build args are not for secrets; the form says so |

## 6 · v0.4 additions (built 2026-10-21)

| Area | Threat | Control |
|---|---|---|
| Networks (ADR-020) | Lateral movement between unrelated resources | Resources in different networks share no network. EnclaveStack's own services join every network but listen only for their own purpose: the edge on 80/443, the object store on its API port, the probe helper on nothing |
| Firewall helper (ADR-021) | A privileged helper becomes a way out of the container | The helper gets `NET_ADMIN` only, inside the target's namespace, with no mounts, no host namespace and no other capabilities. It runs a fixed script; the ruleset comes in an environment variable and is built only from validated prefixes, protocols and ports. It exits at once and is removed |
| Firewall rules | Rules silently lost (container restarted) | Drift detection compares container identity and start time with what was applied, and re-applies within 60 s (FW-09). A target with a firewall whose rules can't be applied is `failed`, not reported as protected |
| Firewall semantics | Operators assume the host port is filtered | The console, CLI help and PRD (FW-06) say that published host ports are reachable through the machine; firewalls govern traffic between resources and networks |
| Load balancers (ADR-022) | Configuration injection; the stats endpoint leaks topology | The configuration is rendered only from validated names and numbers. The stats listener binds inside the container's network only and is read by the control plane through the probe helper; it is never published |
| DNS provider credentials (ADR-023) | A leaked token can repoint the domain | Credentials are EnclaveStack secrets (encrypted at rest) and are decrypted only for one provider call. The console asks for a token scoped to the zone (Cloudflare `Zone:Read` and `DNS:Edit`; a Route 53 IAM policy limited to the hosted zone) and shows the scope to grant |
| Records | EnclaveStack overwrites records it doesn't own | A record with other values at the provider is never overwritten (DNS-04); EnclaveStack deletes only records it owns |
| DNS-01 certificates (ADR-024) | Private keys leak from the store or the edge | Keys are encrypted with `secretbox`, bound to the hostname, and are decrypted only to render the edge configuration, which is written with mode 0600 into the edge container. Challenge records are removed after every attempt |
| Wildcards | One wildcard certificate covers names the operator didn't mean to serve | A wildcard needs a zone the operator controls, and unknown names under it reach only the app the wildcard domain routes to |

## 7 · v0.5 additions (built 2026-10-21)

| Area | Threat | Control |
|---|---|---|
| QMP sockets (ADR-025) | Another local user drives a VM (reads memory, attaches disks) | QMP is only on UNIX sockets inside 0700 directories owned by the server's user, never on TCP. The short fallback directory `/tmp/enclave-<uid>` is refused unless it is owned by that user with mode 0700, which defeats a directory planted in advance |
| QEMU processes | A guest escapes into the host user | QEMU runs as the server's user, never as root, with hardware acceleration and only virtio devices. No host directories are shared into guests. Keeping QEMU current is the operator's job, and `GET /machine` reports its version |
| VM link (ADR-026) | Another container injects Ethernet frames into a VM through the frame port | The frame port is accepted only from the network gateway, where published-port traffic arrives, and is published on `127.0.0.1` only. The link container has `NET_ADMIN` and `/dev/net/tun` only, and no mounts |
| VMs and firewalls | A VM bypasses its firewall, or a network's `internet: false` | All of a VM's traffic crosses its link container, where its firewall lives in the `forward` chain. An internal network's link also joins `es-vmlink` only for its published ports, and forwarding between that network and the VM is dropped |
| SSH access | Password guessing, keys taken from the API | Guests get no password login. Keys are public keys. A key secret's value is never echoed. The SSH port is published on loopback only |
| First-boot scripts | Secrets in `userData` leak | `userData` is spec, not a secret. It is shown back to anyone who can read the VM, and the console and CLI help say to use secrets for passwords: pass them in by `sshKeySecret` or fetch them at first boot. The seed ISO is mode 0600 |
| Images | A tampered download | Catalog images are pinned by SHA-256 and checked before use. URL images are checked when `imageSha256` is given, and the VM's events warn when it isn't |
| Snapshots and disks | Data left behind after delete | Deleting a volume or snapshot removes its file or engine volume. Files are mode 0600 in 0700 directories. Deleted data isn't scrubbed from the host's disk (the host's own disk encryption is the control, `L4-001`) |

## 8 · v0.6 additions (planned)

| Area | Threat | Control |
|---|---|---|
| Broker (ADR-029) | One app reads or deletes another's messages | Each queue and topic has its own NATS user whose permissions name only its subjects and its own consumer; no user may create, delete or read streams except the control plane's `system` user. Passwords are managed secrets and appear in the broker's configuration only as bcrypt hashes. The client port is published on `127.0.0.1` only; the monitoring port is never published |
| Message bodies and emails | Reading data an app sent | Peeking at messages and reading emails needs a session or a write token, as revealing a secret does. Bodies are never logged or put in events |
| Webhooks (TOP-02, TOP-03) | Forged deliveries; the control plane used to reach internal services | Deliveries can be signed (HMAC-SHA256 over the body with a secret). URLs are `https://`, or `http://` only to `localhost`; redirects aren't followed; a delivery's response body is read at most 4 KiB and never stored |
| Database engines (ADR-030) | Default or shared superusers | MySQL's root password is random and never stored; MongoDB's root is the service's own user; ClickHouse's default user is disabled. Passwords reach dumps through the exec environment, never argv |
| Search (ADR-031) | An open OpenSearch | The security plugin stays on and every request needs the `admin` password; the REST API is plain HTTP inside networks and on loopback only |
| Email (ADR-032) | Open relay; leaked provider credentials | SMTP requires the service's user and password; the relay's credentials are a secret written into the container's configuration file (0600) and never returned by the API. Delivery only goes through the user's relay |
| CDN in front of a bucket (ADR-033) | Objects readable without credentials | A bucket behind a CDN becomes readable by key (no listing) on the object store, inside EnclaveStack's networks and on loopback, because the CDN serves it publicly anyway. The CDN form says so, and the policy goes when the last CDN on the bucket goes |
| Transcoders (ADR-034) | A crafted media file exploits FFmpeg | Runs have no network, no credentials, a memory limit and a timeout; the input is copied in and outputs copied out by the control plane, and only file names the control plane chose reach the command |

## 9 · v0.7 additions (planned)

| Area | Threat | Control |
|---|---|---|
| Roles (ADR-036) | A person or token doing more than intended | Three roles checked in the service on every call, from the current rows; a token is the weaker of its role and its user's; a token limited to projects sees nothing else. Projects the caller can't see answer 404, so names don't leak |
| Projects (ADR-035) | One project reaching another's data | References resolve inside a project only. Each project's bucket credentials are a MinIO user whose policy names only its buckets; queues and topics already have their own broker users. `default` keeps the store's root credentials, as in v0.6, so a team that needs isolation from it uses other projects |
| Invites and resets | Stolen or guessed codes | 32 random bytes, shown once, stored as SHA-256, single use, 7 days; accepting is throttled like sign-in. A reset ends the user's sessions |
| Removing people | Access lingering after removal | Removing a user deletes their sessions and tokens in the same transaction; a role change applies to the next request |
| Last admin | Locking everyone out | The last machine admin can't be removed or demoted (`LAST_ADMIN`) |
| Audit (IAM-09) | Changes nobody can trace | Sign-ins, failures, people, roles, tokens, projects and secret reveals append to `audit_log` with the actor and client address; only machine admins read it |
| Notifications (ADR-039) | Secrets in messages; the control plane used to reach internal services | Messages carry names, values and thresholds, never secret values. Webhooks follow the topic rules (https, or http to localhost; no redirects; replies read ≤ 4 KiB and not stored); Slack, Discord and Telegram URLs and tokens live in secrets |
| Log export (ADR-040) | Logs leaving the machine | Exports need an editor; destinations and header secrets are visible to viewers by name only. Lines go where the user points them; the console says so on the form |
| Secret stores (ADR-041) | Outside credentials leaking; values exposed | Store tokens and keys are EnclaveStack secrets; fetched values are encrypted at rest like any secret and never logged, put in events or returned except by reveal (editor). Provider error bodies are cut to 512 bytes and scrubbed of the request's credentials before they reach a message |
| Metrics | Information about other projects | Metrics follow the resource's project; the machine's dashboard shows totals only |

## 10 · v0.8 additions (planned)

| Area | Threat | Control |
|---|---|---|
| Agent channel (ADR-042, ADR-043) | Someone else commanding an agent; an agent impersonated | Mutual TLS 1.3 only, from an EnclaveStack CA: an agent accepts only the control plane's client certificate (CN `enclave-control-plane`), and the control plane accepts only the certificate recorded for that machine (URI SAN and serial). The agent has no other listener |
| CA key | Signing rogue agents or a rogue control plane | ECDSA P-256 key sealed with the master key in the store; never written in clear, never sent. The control plane's own certificate lives 30 days in memory only |
| Join codes | A stolen or guessed code joining a rogue computer | 20 random bytes, shown once, stored as SHA-256, single use, one hour; `POST /machines/join` is throttled like sign-in and records each attempt in the audit list. A machine admin sees the join as an event and can remove it |
| Removed machines | A removed computer keeping access | Its row is deleted, so its certificate is never trusted again. Its stand-ins and peers disappear from every machine at the next mesh sync (5 s), which cuts it out of the tunnel |
| Secrets over the wire | Secret values seen on the network | Engine inputs with secret values cross only the mutual-TLS channel; the agent never logs bodies or arguments. Values live in memory on the agent only as long as the call |
| Tunnel (ADR-045) | Traffic between machines read or forged | WireGuard (Curve25519, ChaCha20-Poly1305); each machine makes its key and keeps it in its mesh volume; the control plane hands out public keys only. A peer accepts packets only from the addresses placed on that machine (allowed IPs) |
| Crossing networks | Machines joining networks that should stay apart | The mesh forwards only when source and destination are in the same network's range, with `drop` as the policy; `internet: false` networks get no route out through the mesh. Stand-ins forward only towards the mesh |
| Mesh and stand-in privileges | A helper container used to escape | Both run with `NET_ADMIN` only (no other capability, no privileged mode, no host network, no mounts except the mesh's key volume), from images built on the machine with pinned bases; their programs are standard-library Go that renders rules from validated addresses only |
| Catalogue (ADR-048) | A template doing more than its installer may | Templates are data, never code: members are API specs validated like a hand-made create, with the installer's role, in the installer's project. Only machine admins add templates. Built-in images are pinned by version |
| Machines in the API | Learning about other projects through machines | A machine's resource count covers only projects the caller can see; the list of what runs there is filtered the same way |

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler Labs | Trust boundaries, STRIDE table, cryptography, known limitations |
| v2 | 2026-10-20 | Soupler Labs | Known limitations: caches without a password; object store image provenance |
| v3 | 2026-10-20 | Soupler Labs | §5 v0.3 controls (planned): edge proxy exposure, config injection, internal CA, ACME email, engine exec, job output, backups, builds |
| v4 | 2026-10-21 | Soupler Labs | Edge controls as built: loopback default, --edge-address opt-in, 404 for unknown hosts, Host-header note |
| v5 | 2026-10-21 | Soupler Labs | §6 v0.4 controls (planned): networks, firewall helper and rule loss, published ports, load balancer config and stats, DNS credentials and record ownership, DNS-01 keys, wildcards |
| v6 | 2026-10-21 | Soupler Labs | §6 controls built |
| v7 | 2026-10-21 | Soupler Labs | §7 v0.5 controls (planned): QMP sockets, QEMU, VM link frame port, VM firewalls, SSH, first-boot scripts, images, snapshot data |
| v8 | 2026-10-21 | Soupler Labs | §7 controls built; the frame-port guard and the socket-directory check are tested |
| v9 | 2026-10-21 | Soupler Labs | §8 v0.6 additions (planned): broker isolation, message and email access, webhooks, database engine users, search auth, email relay, CDN bucket policy, transcoder sandboxing |
| v10 | 2026-10-22 | Soupler Labs | §9 v0.7 additions (planned): roles, project isolation, invites, removal, last admin, audit, notifications, log export, secret stores, metrics |
| v11 | 2026-10-22 | Soupler Labs | §10 v0.8 additions (planned): agent channel and CA, join codes, removed machines, secrets over the wire, tunnel, network crossing, helper privileges, catalogue templates, machine visibility |
