# L3-006 · Security Architecture and Threat Model

---
| Field | Value |
|---|---|
| Document ID | L3-006-security-architecture-v1 |
| Layer | L3 — Architecture |
| Status | Draft |
| Version | v15 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-24 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
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

## 11 · Per-app bucket access (2026-10-24, `L2-011`, ADR-049)

| Area | Threat | Control |
|---|---|---|
| Least privilege | One compromised app reading or deleting every bucket of its project | Each app or job with bucket access has its own object store user whose policy names only its buckets, with read or read-write; no bucket creation, deletion, policy or admin action |
| Keys | Keys seen where they shouldn't be | Kept only in a managed, encrypted secret owned by the resource; never in responses, events, logs or the console; `secret get --reveal` is audited |
| Revocation | Old keys working after access is taken away | Removing the access or the resource removes the user from the object store at once, which refuses its keys, also in a running container |
| Overrides | An app's own `env` replacing its keys with broader ones | With bucket access, `env` and `secretEnv` can't set the five AWS variables (`422`) |

## 12 · Transcoder renditions and notifications (2026-10-24, `L2-012`, ADR-050)

| Area | Threat | Control |
|---|---|---|
| The FFmpeg script | Shell injection through names or tags | Only validated values reach the script: names match `^[a-z0-9][a-z0-9-]{0,31}$`, numbers are bounded integers; tags never reach it (they only choose an output key and are copied as metadata) |
| Output keys from tags | A tag writing outside the output folder | A tag used in the folder must match `^[A-Za-z0-9._-]{1,128}$` and can't be `.` or `..`; the run fails otherwise |
| Webhooks | Calling internal services; leaking the body | `https://` only, or `http://` to localhost (TOP-03); no redirects; 10 s; replies read at most 4 KiB and never kept; signed with the named secret |
| The message | Secrets in a message | It carries keys, sizes, statuses and the input's own tags, never a credential |

## 13 · Bucket objects (2026-10-24, `L2-013`)

| Area | Threat | Control |
|---|---|---|
| Who reads files | A viewer reading data, or a token of another project | Editor or owner of the bucket's project only, as message bodies; project scoping as every resource route |
| Keys | Path tricks (`../`, `/etc`) or header injection | OBJ-04 rules; keys go to the object store as keys, never as file paths; tags are checked before they become headers |
| Size | Filling the disk or memory | 5 GiB per upload; bodies stream from the request to the object store and back, never buffered whole |
| Downloads | A stored HTML file running in the console's origin | `Content-Disposition: attachment` and `X-Content-Type-Options: nosniff` on every download |
| Logs and events | File contents in logs | Events and logs name the key and size only |

## 14 · Public access (`L2-014`, ADR-051 – ADR-053)

The point of this feature is to let the internet in, so every control below starts from "a stranger can reach this hostname".

| Area | Threat | Control |
|---|---|---|
| The home machine | Opening the computer to the internet | Nothing listens publicly at home: connectors and the mesh only dial out (TUN-06). The home address is never in a public DNS record for a tunnel; visitors see Cloudflare's or the edge machine's |
| Reaching more than intended | A visitor asks for the console, the API, another project's domain or a `.localhost` name through a tunnel | Each tunnel has its own listener on the edge proxy, whose routes are exactly its public domains, then a fixed 404 (TUN-07). The control plane is never a route target. Public domains aren't served on the machine's own edge ports, so the gate can't be skipped from the local network |
| Unprotected environments | Dev and stage left open with test data and debug pages | Every public domain and quick tunnel has a gate, `signin` by default; `none` needs an explicit choice and a confirmation, and the page says **Open to everyone** (GATE-01, GATE-05) |
| Gate sessions | A gate cookie stolen or reused elsewhere | Random 32 bytes, stored only as SHA-256; `__Host-` prefixed, `Secure`, `HttpOnly`, `SameSite=Lax`; valid for one hostname only, 12 hours; never accepted by the API, and console cookies and tokens never accepted by the gate. Roles re-checked every 30 s, so removal ends access within a minute |
| Gate sign-in | Password guessing; phishing with a look-alike page; a form posted from another site | The gate's own throttle (10 failures per address and hostname per 15 min, then `429` with `Retry-After`) and audit; the form is accepted only with this hostname's `Origin` (CSRF); the visitor's address comes from the edge proxy, which alone holds the edge key. The page names the hostname and is on that hostname, over HTTPS; `next` is a same-host path only (no open redirect) |
| Gate headers | A visitor forging `X-Enclave-User` to impersonate someone to the app | The edge proxy removes every `X-Enclave-*` request header before routing; only `/check`'s answer sets it (GATE-07) |
| Gate failure | The gate down means the app is open | Fail closed: the edge proxy waits 2 s to reach the gate and 2 s for `/check`, then answers `502` or `504`; a gate that can't check answers `503`. Nothing reaches the app (tested on real Docker with an unreachable gate) |
| Gate listener | Other containers or the network using it | Bound to an address only the computer's containers reach (ADR-053), it serves only the gate's routes, and every request needs the edge key, which is sealed in the store and written into the edge configuration with mode 0600 |
| Connectors | A connector compromised by a flaw in cloudflared | Pinned by digest, no capabilities, `no-new-privileges`, read-only root (its files in a volume of their own, writable directories as small tmpfs), 128 MiB; on `es-system-tunnel` with only the edge proxy and the probe helper, never on a user's network; its listener routes only its tunnel's hostnames and believes a visitor address only from that connector (all tested on real Docker) |
| Provider credentials | A leaked Cloudflare token repoints or deletes tunnels and records | A secret, encrypted at rest, decrypted for one provider call. The console asks for the narrowest token (Account › Cloudflare Tunnel › Edit, Zone › DNS › Edit for the zone, and Access › Apps and Policies › Edit only with GATE-09) and shows it. The tunnel's run token is fetched from Cloudflare each time its connector is made and passed in its environment, never stored, shown or logged |
| Cloudflare sees traffic | TLS ends at Cloudflare for Cloudflare tunnels, so Cloudflare can read requests | Stated in the console and `L5-004` §15. Users who don't accept that use a relay, where TLS ends at home |
| Edge machines | A rented server, run by a hosting company, compromised | It never holds a certificate key, secret or decrypted byte: the relay passes encrypted TCP through (EDG-04, DAR-041). Its agent identity can be removed at once (ADR-043). Nothing is ever placed on it (EDG-03). In the mesh it peers only with the main machine and learns only the edge proxy's address. On the main machine, traffic from its relays may open connections only to the edge proxy's relay listener ports (nftables, ADR-052), so it can't reach apps, databases or the gate directly (tested in `internal/mesh` and `internal/reconciler`) |
| The edge join | A man in the middle joining a rogue server, or a rogue control plane taking a server | Both sides prove the code with HMACs bound to both nonces, the request and the temporary certificate's key (ADR-052); the code is single-use and one hour; five failed proofs stop the agent listening |
| PROXY protocol | A container sending a forged PROXY header to spoof a visitor's address | Each relay listener accepts PROXY headers only from its relay's address |
| Zones that also serve production | EnclaveStack changing or deleting prod's records in a zone it shares with AWS or another host | It writes only its own domains' records and never overwrites a record with other values (DNS-04, PUB-10). A Cloudflare token can only be limited to a whole zone, so the guide suggests a subdomain zone (PUB-09) when prod's records must be out of the token's reach |
| Tailscale | A leaked auth key adds devices to the user's tailnet; a `private` link thought public, or the reverse | The key is a secret; the guide asks for a reusable key tagged for EnclaveStack, so the user's tailnet policy can limit what those devices reach. The connector container is on `es-system-tunnel` only, so the tailnet reaches the app's listener and nothing else on the machine. The link's page says **Private: only your tailnet's devices** or **Public on the internet** |
| ngrok | The free plan's warning page and shared limits surprise users | Stated on the catalogue entry and the link's page (TUN-13); the token is a secret |
| Custom connectors | A custom image is arbitrary code | Only editors and owners create tunnels, as apps (the same trust as running an app). The container gets no capabilities, `no-new-privileges`, a read-only root and no user network: it reaches only the internet and its own listener, so it can't reach other apps, the gate listener or the control plane. The gate still stands between it and the app |
| Quick links | A random URL guessed or shared | Gated like a domain (`signin` by default); the URL changes on every restart; the console calls it temporary |

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler | Trust boundaries, STRIDE table, cryptography, known limitations |
| v2 | 2026-10-20 | Soupler | Known limitations: caches without a password; object store image provenance |
| v3 | 2026-10-20 | Soupler | §5 v0.3 controls (planned): edge proxy exposure, config injection, internal CA, ACME email, engine exec, job output, backups, builds |
| v4 | 2026-10-21 | Soupler | Edge controls as built: loopback default, --edge-address opt-in, 404 for unknown hosts, Host-header note |
| v5 | 2026-10-21 | Soupler | §6 v0.4 controls (planned): networks, firewall helper and rule loss, published ports, load balancer config and stats, DNS credentials and record ownership, DNS-01 keys, wildcards |
| v6 | 2026-10-21 | Soupler | §6 controls built |
| v7 | 2026-10-21 | Soupler | §7 v0.5 controls (planned): QMP sockets, QEMU, VM link frame port, VM firewalls, SSH, first-boot scripts, images, snapshot data |
| v8 | 2026-10-21 | Soupler | §7 controls built; the frame-port guard and the socket-directory check are tested |
| v9 | 2026-10-21 | Soupler | §8 v0.6 additions (planned): broker isolation, message and email access, webhooks, database engine users, search auth, email relay, CDN bucket policy, transcoder sandboxing |
| v10 | 2026-10-22 | Soupler | §9 v0.7 additions (planned): roles, project isolation, invites, removal, last admin, audit, notifications, log export, secret stores, metrics |
| v11 | 2026-10-22 | Soupler | §10 v0.8 additions (planned): agent channel and CA, join codes, removed machines, secrets over the wire, tunnel, network crossing, helper privileges, catalogue templates, machine visibility |
| v12 | 2026-10-24 | Soupler | §11 per-app bucket access: least privilege, keys, revocation, overrides (ADR-049) |
| v13 | 2026-10-24 | Soupler | §12 transcoder renditions and notifications: the script, output keys from tags, webhooks, the message (ADR-050) |
| v14 | 2026-10-24 | Soupler | §13 bucket objects: access, keys, size, downloads, logs (L2-013) |
| v15 | 2026-10-24 | Soupler | §14 public access: home exposure, isolation per tunnel, the gate (sessions, sign-in, headers, failing closed, its listener), connectors, credentials, Cloudflare's view, edge machines, the edge join, PROXY protocol, quick links (L2-014) |
