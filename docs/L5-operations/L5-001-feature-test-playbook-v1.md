# L5-001 · Feature Test Playbook

---
| Field | Value |
|---|---|
| Document ID | L5-001-feature-test-playbook-v1 |
| Layer | L5 — Operations & GTM |
| Status | Draft |
| Version | v31 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-25 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

How every shipped feature — and the current integrated slice — is verified, by a human or CI. Update it whenever shipped functionality, setup or verification steps change.

## 1 · Section template (one per feature)

### Feature: TODO
- **Prerequisites**: services running, seed data, accounts
- **Steps**: numbered, executable
- **Expected results**: observable outcome per step (UI state, response body, DB row, log line)
- **Error paths**: empty results, wrong status, missing data, boundary values
- **Automated checks**: the test files/commands that cover it
- **Known local limitations**: what this playbook cannot prove, and where that is verified instead

## 2 · Features

Automated coverage for everything below is listed in `L5-002`; these are the human walk-throughs run before declaring a release done.

### Feature: First run and sign-in — L2-001 CON-01 – CON-03, CON-12, ADR-007
- **Prerequisites**: `make build`; a fresh data directory: `./build/enclave server --data-dir /tmp/hc --driver fake`.
- **Steps and expected results**:
  1. The server prints `Setup code: XXXX-XXXX-XXXX` and `Console: http://127.0.0.1:7070`.
  2. Open the console → first-run setup. A wrong code → "That setup code isn't right." A right code, machine name, email and a 12+ character password → lands on Apps.
  3. Sign out, sign in with a wrong password 5 times → "Too many attempts. Try again in 15 minutes."
  4. Restart the server → no setup code printed; `GET /api/v1/setup` → `{"required":false}`.
- **Error paths**: password under 12 characters; setup attempted twice (409).

### Feature: Resources from the console — L2-001 CON-04 – CON-18
- **Prerequisites**: as above, signed in.
- **Steps and expected results**:
  1. Each section (Apps, Databases, Caches, Buckets, Secrets) shows its empty state with "New <kind>".
  2. Create secret `stripe-key` → Ready. Reveal → confirmation → value shown, hidden after 30 s.
  3. Create database `orders` → steps fill Pull → Create → Start → Healthy → Ready; Overview shows host, port, username and `orders-password`; Secrets now lists `orders-password` as managed.
  4. Create app `api` (image `nginx:1.27-alpine`, port 80, publish, secret env `STRIPE_KEY=stripe-key`) → Ready; Overview shows host:port.
  5. Edit the app's image → Updating → Ready; Events lists `updated`, step events and `ready`.
  6. Delete secret `stripe-key` → refused, listing `api`. Delete app `api` (type the name) → disappears.
  7. "Use from code" shows a CLI command and an OpenTofu block matching the resource.
  8. API tokens: create `laptop` (write) → shown once; list shows it; revoke → gone.
- **Error paths**: invalid name (inline message), duplicate name (409 shown on the field), stale edit (412 → Reload), failed resource → Retry.

### Feature: CLI — L2-002
- **Prerequisites**: server running; `./build/enclave login --server http://127.0.0.1:7070`.
- **Steps and expected results**: `enclave db create orders --wait` prints progress and a table; `enclave ls`; `enclave get database orders -o json` is valid JSON; `enclave secret create k --from-stdin <<< v`; `enclave app create web --image nginx:1.27-alpine --port 80 --publish --secret-env K=k --wait`; `enclave logs app web --tail 20`; `enclave delete app web` without `--yes` in a pipe exits 2; with `--yes --wait` exits 0; `enclave get app web` exits 3.
- **Error paths**: no token → exit 4 with "Run enclave login"; server down → retries then exit 1 with the address.

### Feature: OpenTofu — L2-003
- **Prerequisites**: `make provider-dev` then `export TF_CLI_CONFIG_FILE=$HOME/.tofurc.enclave`; server running; `ENCLAVE_SERVER` and `ENCLAVE_TOKEN` set.
- **Steps and expected results**: in `examples/opentofu/`, `tofu plan` shows 4 resources to add; `tofu apply` waits until all are ready; the app URL output answers HTTP 200; changing `database.version` plans a replacement; deleting the app in the console then `tofu plan` shows it will be created again; `tofu import enclave_cache.c <name>` adopts a console-created cache; `tofu destroy` removes everything.

### Feature: Recovery and drift — L2-004 CP-07 – CP-10
- **Steps and expected results**: with the Docker driver, create a database and stop the server during "Pull" → restart → it reaches Ready. `docker rm -f es-cache-<name>` → within 60 s the cache is back and Events shows `drift_detected`. Stop Docker Desktop → banner in the console, writes queue; start it → they apply.

## 2a · v0.3 walk-throughs (planned — run once each feature ships)

### Feature: Zero-downtime deploys — L2-005 DEP-01 – DEP-06, ADR-016 (shipped 2026-10-21)
- **Prerequisites**: Docker driver; app `web` (`nginx:1.27-alpine`, port 80, `--health http:/ --health-interval 1`). Until domains ship (phase 2), watch the app from inside the network: `docker exec es-system-probe sh -c 'while true; do wget -qO- http://es-app-web/ >/dev/null && echo ok || echo FAIL; done'`.
- **Steps and expected results**:
  1. Change the image to `nginx:1.28-alpine`. The events read: revision 2 started, health check passing, traffic switched, revision 1 stopped. The load tool reports 0 failed requests. `docker ps` shows only `es-app-web-r2`.
  2. Change the image to `nginx:does-not-exist`. The app goes `failed` with the pull error, and the site keeps answering from revision 2.
  3. Set the health check to `http /missing` (404). The new revision is discarded after its budget, the message names revision 2 as serving, and the site still answers.
  4. Kill the server while a new revision is in its health check, then restart it. The leftover revision is removed and the deploy starts again from the top, with the serving revision untouched throughout.
  5. `enclave get app web -o json`: `servingRevision`, `latestRevision`, `lastDeploy` match the history in `enclave events app web` (revision_started → health_check_passed → traffic_switched → revision_stopped).
- **Error paths**: a published app deploys with `recreate`, and the events say why; a `command` check with exit code 1.
- **Automated checks**: rolling deploy integration suite (real Docker) and the zero-drop k6 scenario (`L5-002` §6).

### Feature: Domains and HTTPS — L2-005 DOM-01 – DOM-09, ADR-015 (shipped 2026-10-21)
- **Prerequisites**: ports 80/443 free (`L4-001` §v0.3), or start the server with `--edge-http-port 8088 --edge-https-port 8443` and add those ports to the URLs below.
- **Steps and expected results**:
  1. Create domain `web-local` → `web.localhost`, app `web`. It becomes Ready with certificate state `issued`, issuer "EnclaveStack Local CA". `curl --cacert <(enclave domain ca) https://web.localhost` returns 200, and the app's log shows `X-Forwarded-Proto: https`.
  2. Create a domain for a hostname with no DNS. It stays `provisioning`, and the message names the A record and the address.
  3. Delete app `web`. This is refused and lists `web-local`.
  4. Stop the edge container. It is back within 60 s.
- **Error paths**: duplicate hostname → `HOSTNAME_TAKEN`; port 80 already taken → the edge is unavailable on `/machine` and the reason is shown on every domain.
- **Also check**: a public hostname (`api.example.com`) while the edge listens on 127.0.0.1 fails at once with the `--edge-address 0.0.0.0` instruction; `enclave machine set --public-address 203.0.113.7` makes the DNS message name that address.
- **Known local limitations**: public ACME needs a public IP and DNS. Automated tests use Pebble, the ACME test server.

### Feature: Scheduled jobs — L2-005 JOB-01 – JOB-08, ADR-017 (shipped 2026-10-21)
- **Steps and expected results**:
  1. `enclave job create tick --image busybox:1.37 --schedule "* * * * *" -- sh -c 'date; exit 0'`. `enclave job run tick --wait` prints the date at once. Within 2 minutes, `runs` shows two succeeded runs with the date as output.
  2. `enclave job update tick --run-timeout 5s -- sleep 120`, then `job run tick --wait`. The run is `timed_out` (exit 1).
  3. With overlap `forbid` and a 90 s sleep, the next minute's run is `skipped`.
  4. Stop the server for 3 minutes, then restart it. One `catch_up` run starts, and one skipped record counts the missed runs.
- **Automated checks**: scheduler unit tests with a fake clock; jobs integration suite.

### Feature: Backups and restore — L2-005 BAK-01 – BAK-09, ADR-018 (shipped 2026-10-21)
- **Prerequisites**: Docker running; `enclave server` (Docker driver); a write token in `ENCLAVE_TOKEN`. Without `psql` on the host, use `docker exec -e PGPASSWORD=… es-database-<name> psql -U <name> <name>`.
- **Steps and expected results** (walked live on 2026-10-21 with the built binary):
  1. Create database `shop` with a table of 2,000 rows, cache `sessions` with 100 keys, and bucket `vault`. Run `enclave backup create shop-minutely --source database/shop --bucket vault --schedule '* * * * *' --keep 2 --wait`, and the same with `--source cache/sessions`. Within 3 minutes, `backup runs` shows a scheduled run at each `:00`. Each says "Backed up database shop (postgres 16): 9.7 KiB." (Redis: "redis 7"), and the third adds "Removed 1 old backup." The removed run's message ends "Removed by retention."
  2. Delete 1,900 rows from `shop`, then `enclave backup restore shop-minutely --run <latest> --replace --yes --wait`. A `pre_restore` run appears first; `shop` is back to 2,000 rows and shows `restoredFrom`. The events show `restore_requested`, `restore_started` and `restored`.
  3. `FLUSHALL` the cache, then `enclave backup restore sessions-minutely --run <latest> --new sessions-copy --wait`. `sessions-copy` has 100 keys; `sessions` still has 0.
  4. Restore a run marked "Removed by retention." The command exits 5 at once with "This run's backup was removed by retention. Choose a newer run.", and nothing is created.
  5. Restart the server in the middle of the schedule. The schedule continues and nothing is lost.
  6. Console: on a database, open the Backups tab, then Set up backups, then Back up now. In Restore, "Into a new database" is preselected; Replace stays disabled until the name is typed.
- **Error paths**:
  - Wrong external S3 credentials: the run fails with the provider's message, and three failures in a row make the backup `failed` (Retry resumes it).
  - A damaged object: the restore stops before anything changes.
    - A new resource fails with "Delete <name> and restore from another run."
    - A replaced source fails with "Nothing was changed; use Retry …", and Retry brings it back unchanged.
  - Deleting a backed-up database or bucket: `RESOURCE_IN_USE`. Changing a backup's source: `IMMUTABLE_FIELD`.
  - A body sent to `restart` or `run`: `400 MALFORMED_REQUEST`.
- **Automated checks**:
  - `go test ./internal/reconciler -run 'Backup|Retention|SafetyBackup|FailedReplace'`
  - `make engine-docker` (real PostgreSQL, Redis and object store)
  - `make e2e` (500 rows through the CLI)
  - `make provider-acceptance`
  - `pnpm test`
  - `make console-e2e`
- **Known local limitations**: the fake engine keeps backups in memory, so they last only until the server stops. External S3 is tested against the local object store and the in-memory store, not a cloud provider.

### Feature: Build from Git — L2-005 BLD-01 – BLD-05, ADR-019 (shipped 2026-10-21)
- **Prerequisites**: Docker with BuildKit (the Docker Desktop default), network access to the repository's host, the server on the Docker driver, and a write token in `ENCLAVE_TOKEN`.
- **Steps and expected results** (walked live on 2026-10-21 with the built binary against GitHub):
  1. `enclave app create welcome --git https://github.com/docker/welcome-to-docker --ref main --port 3000 --publish --wait`.
     - `--wait` shows "Resolving main…", then the build steps, then the health check.
     - In about 30 s the app is Ready with `builtCommit`, `builtImage` `enclave.local/welcome:r1-<sha>` and `lastBuild succeeded`.
     - The published port serves the React app.
  2. `enclave logs app welcome --build` shows the BuildKit log: `#n` step lines, the `npm` output, then `exporting to image`.
  3. `enclave app redeploy welcome --wait` shows "Commit … and the build settings are unchanged; build skipped."; `lastBuild` is `skipped` and the serving revision goes up by one. The `redeployed` event reads "Nothing new since <sha>; a new revision of the same build deployed."
  4. Push a commit and redeploy: it builds again, and only the serving and previous images remain (`docker images enclave.local/welcome`).
  5. Console: New app → "A Git repository" → repository and ref → Create. The Overview shows "Source: <url> @ main" and the built commit, and the **Build log** tab shows the log.
- **Error paths**:
  - **Missing repository** (or a private one without credentials): fails at once, without retries, with "there's no repository at …, or it is private: a private repository needs a credentials secret". A rejected token says "check the credentials secret".
  - **Missing branch**: "the repository has no branch or tag \"no-such-branch\"".
  - **Broken Dockerfile**: `failed` with `build of <sha> failed: process "/bin/sh -c …" did not complete successfully: exit code: N` and "The serving revision keeps running"; the previous revision still answers. Retry builds again, rather than falling back to the old image.
  - **Validation**: `--image` together with `--git`, or neither, exits 2. A credentials secret in use can't be deleted.
- **Automated checks**:
  - `go test ./internal/build` (a real Git HTTP server with a token)
  - `go test ./internal/reconciler -run 'SourceApp|FailedBuild|SourceCredentials'`
  - `make engine-docker` (real BuildKit, a 2 MiB context, `.dockerignore`, deploying the built image, clean-up)
  - `make e2e` (a private repository, redeploy, a broken push)
  - `make provider-acceptance`
  - `pnpm test`
  - `make console-e2e`
- **Known local limitations**: the fake engine builds from an in-memory Git source (any URL resolves, and tests push commits to it), and its builds are simulated step by step. SSH repositories are covered by unit tests of key and known-hosts handling, not by a live SSH server.

## 2b · v0.4 walk-throughs (shipped 2026-10-21)

**Walked live on 2026-10-21** (Docker Desktop 29.7.2, the built binary, the CLI): a network with two subnets; a cache in the data subnet (`privateIp` 10.231.0.129, `live-kv.cache.internal`); a two-replica app; a firewall letting only the app reach the cache; a published load balancer. Scaling the app to 3 and back to 1 balanced over every replica and reported `healthyTargets` 3, then 1, when the deploy finished. After three redeploys the app's newest revision still reached the cache and an outsider in the same network was blocked. The server log had no errors. The DNS walk-throughs below still need a real Cloudflare or Route 53 zone, and the DNS-01 one a public Let's Encrypt order: they are covered against local fakes and Pebble only.

### Feature: Networks and private DNS — L2-006 NET-01 – NET-08, ADR-020
- **Prerequisites**: the server on the Docker driver; a write token in `ENCLAVE_TOKEN`.
- **Steps and expected results**:
  1. `enclave network create backend --cidr 10.40.0.0/16 --subnet data=10.40.1.0/24 --subnet apps=10.40.2.0/24 --wait`: Ready; `docker network inspect es-net-backend` shows the CIDR and gateway 10.40.0.1.
  2. `enclave db create orders --network backend --subnet data --wait`: `privateIp` is inside 10.40.1.0/24, `privateDns` is `orders.database.internal`.
  3. `enclave app create api --image traefik/whoami:v1.11 --port 80 --network backend --subnet apps --wait`; from a job in the network, `orders.database.internal:5432` and `api.app.internal:80` connect.
  4. An app on the default network can't reach `orders.database.internal` (name not found) or 10.40.1.x (timeout).
  5. `enclave app update api --network ""` deploys a new revision on the default network; the old one stops.
- **Error paths**: an overlapping CIDR → `422` with `OVERLAPS` on `spec.cidr`; a subnet outside the network → `422`; `network delete backend` while `orders` uses it → `409 RESOURCE_IN_USE` listing it; `--publish` on a network created with `--no-internet` → `422`.
- **Automated checks**: `go test ./pkg/api ./internal/reconciler -run Network`; `make engine-docker` (`network_test.go`); `make e2e`; `make provider-acceptance`; `pnpm test`; `make console-e2e`.

### Feature: Firewalls — L2-006 FW-01 – FW-09, ADR-021
- **Steps and expected results**:
  1. With `api`, `web` and `orders` from above: `enclave firewall create db-access --target database/orders --allow "tcp 5432 from app/api" --wait`.
  2. A connection from `api` to `orders:5432` succeeds; one from `web` (moved into `backend`) times out.
  3. `enclave app redeploy api --wait`: `api`'s new revision still reaches `orders` (its new address was allowed before it took traffic).
  4. `docker restart es-database-orders`: within 60 s the rules are back (`firewall_reapplied` event) and `web` is blocked again.
  5. `enclave firewall delete db-access --yes --wait`: `web` connects again.
- **Error paths**: a target that doesn't exist → `422 NOT_FOUND` on `spec.targets[0]`; deleting `api` while the firewall names it → `409`; `--allow "icmp 80 from any"` → `422` (ports with icmp).
- **Automated checks**: `go test ./internal/firewall ./internal/reconciler -run Firewall`; `make engine-docker` (`firewall_test.go`: real enforcement, restart repair, new-revision window measured).

### Feature: Replicas and load balancers — L2-006 LB-01 – LB-08, ADR-022
- **Steps and expected results**:
  1. `enclave app update api --replicas 3 --wait`: three containers of the new revision; `runningReplicas` 3.
  2. A domain for `api` answers from three different hostnames (`whoami` prints its container) over repeated requests.
  3. `enclave lb create api-lb --port 80 --target api --health http:/ --publish --wait`: `healthyTargets` 3; the published port answers round robin.
  4. `docker stop` one replica: within a few seconds the load balancer stops sending to it, `healthyTargets` drops to 2, and drift brings the app back to 3.
  5. A rolling deploy under `k6` load through the load balancer: 0 failed requests.
- **Error paths**: `--replicas 2 --publish` → `422`; targets in two networks → `422`; deleting `api` while `api-lb` targets it → `409`.
- **Automated checks**: `go test ./internal/lb ./internal/reconciler -run 'Replica|LoadBalancer'`; `make engine-docker` (`lb_test.go`, `app_test.go` replicas); `make load-lb` (0 failed of about 36,000 requests over nine rolling deploys, measured 2026-10-21).

### Feature: DNS zones and records — L2-006 DNS-01 – DNS-08, ADR-023
- **Prerequisites**: a Cloudflare zone and an API token with `Zone:Read` and `DNS:Edit` on it, or a Route 53 hosted zone and an IAM key limited to it. Without them, the automated suites cover both APIs with local fakes.
- **Steps and expected results**:
  1. `enclave secret create cf-token --from-stdin` and `enclave zone create example --provider cloudflare --zone example.com --credentials cf-token --wait`: Ready with the zone's name servers.
  2. `enclave machine set --public-address 203.0.113.7`, then `enclave record create api --zone example --name api --type A --machine --wait`: the provider shows `api.example.com A 203.0.113.7`, commented "managed by enclave".
  3. Change the record at the provider: within 10 minutes it is put back (`record_repaired` event).
  4. `enclave record delete api --yes --wait`: gone at the provider.
- **Error paths**: a token without `DNS:Edit` → the zone fails naming the permission; a record that exists with other values → `failed` naming them; deleting the zone while records use it → `409`.
- **Automated checks**: `go test ./internal/dns/...` (both providers against local fakes); `go test ./internal/reconciler -run 'Zone|Record'`; `make provider-acceptance`; `pnpm test`.
- **Known local limitations**: CI never calls the real Cloudflare or Route 53 APIs; the live steps above are the only check against them.

### Feature: Certificates through DNS — L2-006 CRT-01 – CRT-05, ADR-024
- **Steps and expected results**:
  1. With the zone above and the edge on loopback: `enclave domain create wild --hostname '*.example.com' --app api --zone example --wait`: the certificate is issued by Let's Encrypt; no inbound port was opened.
  2. `curl --resolve x.example.com:443:127.0.0.1 https://x.example.com/` succeeds with a publicly trusted certificate.
  3. No `_acme-challenge` record remains at the provider.
- **Error paths**: `*.example.com` without a zone → `422`; a hostname outside the zone → `422`; credentials revoked → renewal fails with the reason, and the domain goes `failed` only near expiry.
- **Automated checks**: `go test ./internal/acme` and `make engine-docker` (`acme_dns_test.go`: Pebble DNS-01 through challtestsrv, a wildcard served by the edge).
- **Known local limitations**: the automated test uses Pebble; the live Let's Encrypt path is walked by hand.

## 2c · v0.5 walk-throughs (shipped 2026-10-21)

**Walked live on 2026-10-21** on an Apple M4 (macOS 26.4, Docker Desktop 29.7.2, QEMU 11.1.2 with HVF), with the built binary and the CLI:
- a network `backend` with a database and an app;
- a Debian VM `worker` placed in it with a first-boot web server, ready in 92 s (image download included);
- `enclave vm ssh worker` ran a command with passwordless sudo;
- the VM resolved and reached `orders.database.internal` and the internet, and a container in the network reached `worker.vm.internal:9000`;
- a firewall allowing only `api` on 9000: `api` connected, another container timed out, loopback SSH kept working;
- removing the link container outside EnclaveStack restored traffic in 10 s without restarting the VM (after the fixed-MAC fix found on this walk-through; before it, about three minutes);
- restarting the server kept the same QEMU process; `kill -9` of QEMU was repaired and SSH answered within 65 s; `sudo poweroff` inside the VM recorded `power: stopped` with an event and the VM stayed off;
- a 5 GiB disk attached (restart), formatted and mounted inside, grown live to 8 GiB; a disk snapshot taken while the VM wrote, in 0.6 s; a volume and a clone VM from snapshots booted with their own host name and new host keys and the source's files; both snapshots deleted while their copies kept working;
- the clone moved into a network without internet: new private IP, no restart, no internet, loopback SSH working, `--publish` refused;
- everything deleted: no QEMU process, link container, VM directory or disk file left. The server log had one error, from an app created by mistake during the walk-through (an image that exits at once), unrelated to v0.5.

Not walked: a Linux machine with `/dev/kvm` (CI runs `make engine-qemu` there from v0.5 on).

### Feature: Filesystem volumes and their snapshots — L2-007 VOL-04 – VOL-09, SNP-01 – SNP-04, ADR-027, ADR-028
- **Prerequisites**: the server on the Docker driver; a write token in `ENCLAVE_TOKEN`.
- **Steps and expected results**:
  1. `enclave volume create uploads --type filesystem --wait`: Ready; `docker volume ls` shows `es-vol-<id>`.
  2. `enclave app create web --image nginx:1.27 --port 80 --replicas 2 --volume uploads:/usr/share/nginx/html --wait`. Write a file through one replica (`docker exec`); the other replica serves it.
  3. `enclave app redeploy web --wait`: the new revision still serves the file (VOL-04).
  4. `enclave snapshot create before --from volume/uploads --wait`: Ready, `type` filesystem, `sizeBytes` > 0, `inUse` true.
  5. Delete the file; `enclave volume create restored --type filesystem --snapshot before --wait`; mount it in a job, and the file is there again.
  6. `enclave volume get uploads`: `attachedTo` is `app/web`; `usedBytes` grows after writing a 10 MiB file (within 5 minutes).
- **Error paths**: `volume delete uploads` while `web` mounts it → `409 RESOURCE_IN_USE` listing `app/web`; `--volume uploads:relative/path` → `422`; mounting a `disk` volume in an app → `422 NOT_ALLOWED`; `--size` on a filesystem volume → `422`; a database spec with `volumes` → `422`.
- **Automated checks**: `go test ./pkg/api ./internal/reconciler -run 'Volume|Snapshot'`; `make engine-docker` (`volume_test.go`); `make e2e`; `make provider-acceptance`; `pnpm test`; `make console-e2e`.

### Feature: VMs — L2-007 VM-01 – VM-12, IMG-01 – IMG-05, ADR-025
- **Prerequisites**: QEMU 8 or later on the PATH (`brew install qemu`; `apt install qemu-system qemu-utils` and membership of the `kvm` group on Linux); an SSH key in `~/.ssh/id_ed25519.pub`.
- **Steps and expected results**:
  1. `enclave machine`: VMs available, with the QEMU version and `hvf` or `kvm`.
  2. `enclave vm create worker --image debian-12 --ssh-key ~/.ssh/id_ed25519.pub --cpus 2 --memory 2048 --user-data ./init.sh --wait`: Ready within 2 minutes on first use (download included), within 60 s once the image is cached. `power` running; `sshAddress` 127.0.0.1:<port>; `sshUser` enclave.
  3. `enclave vm ssh worker -- 'nproc; free -m; cat /var/log/init-ran'`: 2 CPUs, about 2 GiB, the marker written by `init.sh`. `sudo true` works without a password.
  4. `enclave vm console worker`: the boot log ending in a login prompt.
  5. `enclave vm stop worker --wait`: `power` stopped within 60 s; no QEMU process for it. `enclave vm start worker --wait`: running again; files kept.
  6. Inside the VM, `sudo poweroff`: within 60 s `power` is stopped and the events say the VM shut itself down; it is not started again.
  7. `kill -9` the QEMU process: within 60 s it is running again with a `crash` start event.
  8. Stop the server (Ctrl-C) and start it again: the VM kept running (same QEMU PID) and is adopted.
  9. `enclave vm update worker --memory 4096`: the CLI says the VM will restart; afterwards `free -m` shows about 4 GiB. `--disk 20` grows the root disk live; after a restart `df -h /` shows about 20 GiB.
- **Error paths**: no QEMU on the PATH → the VM stays `pending` with "Install QEMU: brew install qemu" and `enclave machine` says the same, and installing it lets the VM proceed with no other action. `--memory` above the limit → `422 OUT_OF_RANGE`. `--disk 5` after 20 → `422 GROW_ONLY`. A URL image with a wrong `--sha256` → `failed`, permanent, naming both checksums. An image for another architecture can't be told apart before it boots: it fails to start and the boot console shows why (`L2-007` as built). Changing the keys (`PATCH` or OpenTofu) → `422 IMMUTABLE_FIELD`; `vm update` has no key flags.
- **Automated checks**: `go test ./pkg/api ./internal/reconciler ./internal/engine/qemu -run VM`; `make engine-qemu` (real QEMU: boot, seed, power, crash and adoption, live resize); `make e2e`; `make provider-acceptance`; `pnpm test`; `make console-e2e`.

### Feature: VMs in networks — L2-007 VMN-01 – VMN-06, ADR-026
- **Steps and expected results**:
  1. With `backend` from v0.4: `enclave vm create worker2 --image debian-12 --ssh-key ~/.ssh/id_ed25519.pub --network backend --subnet apps --user-data ./serve-9000.sh --wait`: `privateIp` in 10.40.2.0/24, `privateDns` `worker2.vm.internal`.
  2. From `api` (same network): `wget -qO- worker2.vm.internal:9000` answers. From the VM: `getent hosts orders.database.internal` resolves and `pg_isready -h orders.database.internal` connects.
  3. From the VM, `curl https://deb.debian.org` answers (internet on).
  4. `enclave firewall create vm-access --target vm/worker2 --allow "tcp 9000 from app/api" --wait`: `api` connects, `web` times out, `enclave vm ssh worker2` still works.
  5. `docker rm -f es-vm-worker2`: within 60 s it is back with the same `privateIp`, and traffic resumes without the VM restarting (uptime unchanged).
  6. `enclave vm update worker2 --network other --wait`: new `privateIp`, uptime unchanged.
  7. In a network created with `--no-internet`: the VM can't reach the internet, but `enclave vm ssh` works.
- **Error paths**: `--publish 80` in a network without internet → `422`; `--publish 22` → `422`; deleting `backend` while the VM uses it → `409`.
- **Automated checks**: `make engine-docker` (`vmlink_test.go`: frames, DNS, checksums, firewall forward chain, gateway-only frame port); `make engine-qemu` (`network_test.go` on real QEMU and Docker).

### Feature: Disk volumes and disk snapshots — L2-007 VOL-01 – VOL-03, SNP-01 – SNP-06, IMG-04, ADR-027, ADR-028
- **Steps and expected results**:
  1. `enclave volume create data --type disk --size 50 --wait`; `enclave vm update worker --disk-volume data`: the CLI warns of a restart; afterwards `/dev/disk/by-id/virtio-data` exists, 50 GiB, unformatted. Format and mount it, and write a file.
  2. `enclave volume update data --size 60 --wait` while running: `lsblk` shows 60 GiB at once.
  3. `enclave snapshot create data-1 --from volume/data --wait` while the VM writes: Ready in seconds, `inUse` true; the VM never stalls for more than a second (a 100 ms `ping` loop from another container shows no gap).
  4. `enclave volume create data-copy --type disk --snapshot data-1 --wait`; attach it to a second VM: the file is there.
  5. `enclave snapshot create worker-1 --from vm/worker --wait`; `enclave vm create clone --image snapshot/worker-1 --ssh-key … --wait`: it boots with host name `clone`, new SSH host keys and the files of `worker`.
  6. `enclave snapshot delete data-1 --yes`: `data-copy` and `clone` keep working (SNP-02, SNP-06).
- **Error paths**: attaching `data` to a second VM → `409 VOLUME_ATTACHED`; `volume update data --size 40` → `422 GROW_ONLY`; deleting `data` while attached → `409`; `--snapshot` of a filesystem snapshot for a disk volume → `422`; killing the server during a copy and starting it again finishes the copy with no `.partial` file left.
- **Automated checks**: `go test ./internal/reconciler -run 'Disk|Snapshot'`; `make engine-qemu` (`disk_test.go`, `snapshot_test.go`); `make e2e` (`TestComputeStackForRealWithOpenTofu`).

## 2d · v0.6 walk-throughs (shipped 2026-10-22)

**Exercised on 2026-10-22** on an Apple M4 (Docker Desktop 29.7.2) by the built binaries, not by hand. `TestDataAndMessagingStackForRealWithOpenTofu` follows the steps below for queues, topics, MySQL, email, CDN and transcoders in 50 s, and reads the server log for errors. The real-Docker suite covers every engine, search, relay and the CDN's stale serving, and Playwright covers the console. Found on the way, and fixed:
- a restart moved published ports;
- the relay failed without TLS;
- FFmpeg had no `/out`;
- MongoDB 8 refused the kernel;
- `enclave get topic` didn't know the kind;
- search and email ignored their network and weren't firewall targets.

Not yet walked by hand: a relay through a real provider, and a CDN with a public certificate.

### Feature: Database engines — L2-008 DBE-01 – DBE-06, ADR-030
- **Prerequisites**: the server on the Docker driver; a write token in `ENCLAVE_TOKEN`.
- **Steps and expected results**:
  1. `enclave db create legacy --engine mysql --wait`, `enclave db create docs --engine mongodb --wait`, `enclave db create analytics --engine clickhouse --wait`: each Ready; `db get` prints a connection string without the password.
  2. Connect to each from the machine with its client and the revealed password; create a table/collection and a row.
  3. An app in the same network reaches each by its private DNS name.
  4. `enclave backup create legacy-nightly --source database/legacy --bucket backups`, `backup run --wait`, then `backup restore … --new legacy-copy --wait`: the row is there. Repeat for MongoDB and ClickHouse; replace-restore of one after deleting the row brings it back.
- **Error paths**: `--engine oracle` → `422`; `--engine mysql --version 16` → `422`; `db update legacy --version 9` → `422 IMMUTABLE_FIELD`.
- **Automated checks**: `go test ./pkg/api ./internal/reconciler -run 'Database|Backup'`; `make engine-docker` (`TestDatabaseEnginesStartDumpAndRestore`); `make e2e`.

### Feature: Queues and topics — L2-008 QUE-01 – QUE-09, TOP-01 – TOP-08, ADR-029
- **Steps and expected results**:
  1. `enclave queue create emails-failed --wait`; `enclave queue create emails --visibility-timeout 5s --max-receives 2 --dead-letter-queue emails-failed --wait`: Ready; `enclave machine` shows the broker available; `docker ps` shows `es-system-broker`.
  2. `enclave queue send emails hello --attr kind=receipt`; `queue peek emails` shows it; `queue receive emails --wait 5s` prints it with a receipt; not deleting it, it comes back after 5 s with `receiveCount` 2; after the second expiry it is in `emails-failed` (QUE-03).
  3. An app with `secretEnv NATS_PASSWORD=emails-queue-password` and a NATS client reaches `nats://es-system-broker:4222` as `q-emails`, receives and acks; publishing to `es.q.emails-failed` with those credentials is refused.
  4. `enclave topic create orders --queue emails --webhook https://<a request bin>,secret=hook-key --wait`; `topic publish orders '{"id":1}'`: the message reaches `emails` and the webhook with a valid `X-Enclave-Signature`; `topic get` deliveries count 1 each.
  5. Point the webhook at a URL that answers 500: deliveries retry with growing gaps; after 10 the topic's events say it gave up.
  6. Restart the server and the broker container (`docker restart es-system-broker`): messages waiting in `emails` are still there.
- **Error paths**: deleting `emails-failed` while `emails` dead-letters into it → `409`; a body over `maxMessageBytes` → `422`; `--webhook http://example.com` → `422`; receiving with another queue's receipt → reported in `failed`. A message past `maxReceives` moves to the dead-letter queue at the next receive, not the moment its timeout ends (QUE-03 as built).
- **Automated checks**: `go test ./internal/messaging/... ./internal/reconciler -run 'Queue|Topic|Deliver'`; `make engine-docker` (`TestBrokerStartsReloadsUsersAndGoesAway`, `TestARestartKeepsThePublishedPort`); `make e2e`.

### Feature: Search — L2-008 SRC-01 – SRC-05, ADR-031
- **Steps and expected results**: `enclave search create catalogue --memory 2048 --wait` (Ready within 3 minutes); `curl -u admin:<password> <url>/_cluster/health` answers green or yellow; index a document and search it; `enclave search update catalogue --memory 3072 --wait` keeps the index; a request without the password gets `401`.
- **Automated checks**: `make engine-docker` (`TestSearchNeedsItsPassword`, which also changes memory and finds the index again); `go test ./internal/reconciler -run JoinNetworks`.

### Feature: Email — L2-008 EML-01 – EML-07, ADR-032
- **Steps and expected results**: `enclave email create shop-mail --domain example.com --wait`; `enclave email send shop-mail --from shop@example.com --to a@b.test --subject Hi --text Hello`; `email messages shop-mail` lists it as `captured`; an app sends through `shop-mail.email.internal:1025` with the managed password and it appears too; `webUrl` opens the viewer after the same login. Switch to relay mode against a local SMTP test server (or the provider) and the next message is `relayed`.
- **Error paths**: `--from x@other.test` → `422`; SMTP without a password is refused; relay mode without `--relay-credentials` → `422`.
- **Automated checks**: `go test ./internal/mail/...`; `make engine-docker` (`TestEmailCapturesAndRelays`); `make e2e`.

### Feature: CDN — L2-008 CDN-01 – CDN-07, ADR-033
- **Steps and expected results**: `enclave cdn create assets --hostname assets.localhost --origin app/web --tls internal --wait`; `curl -k https://assets.localhost/` twice: `X-Cache: MISS` then `HIT`; a response with `Cache-Control: no-store` stays `BYPASS`/`MISS`; `cdn purge assets --yes` → next request `MISS`; stop `web` and a cached page is still served (`STALE`). A CDN on `bucket/uploads` serves an object by key and refuses listing.
- **Error paths**: a hostname a domain already serves → `409 HOSTNAME_TAKEN`; deleting `web` while the CDN uses it → `409`.
- **Automated checks**: `make engine-docker` (`TestCDNCachesPurgesAndServesStale`); `make e2e`.

### Feature: Transcoders — L2-008 TRN-01 – TRN-08, ADR-034
- **Steps and expected results**: `enclave transcoder create videos --input uploads/videos/ --output uploads/media/ --preset mp4-720p --preset thumbnail --wait`; upload a short MP4 into `videos/`: within 20 s a run starts and succeeds; `media/<name>/mp4-720p.mp4` and `thumbnail.jpg` exist; `transcoder run videos videos/<name>.mp4 --wait` converts it again; a text file uploaded as `.mp4` fails its run with FFmpeg's reason while the transcoder stays Ready.
- **Error paths**: input and output overlapping on the same bucket → `422`; deleting `uploads` while the transcoder uses it → `409`.
- **Automated checks**: `go test ./internal/reconciler -run Transcod`; `go test ./internal/reconciler -run Transcoder` (including concurrency); `make engine-docker` (`TestTranscodeConvertsAClipWithEveryPreset`); `make e2e`.

## 2e · v0.7 walk-throughs

### Feature: Projects, people, roles and tokens — L2-009 PRJ-01 – PRJ-09, IAM-01 – IAM-10, TOK-01 – TOK-05, ADR-035, ADR-036
- **Prerequisites**: the server on the Docker driver with v0.6 resources in it (an upgrade, not a fresh install).
- **Steps and expected results**:
  1. After the upgrade, `enclave ls --all-projects` shows every resource in `default`; their addresses, private DNS names and `tofu plan` are unchanged.
  2. `enclave project create shop-staging`; create `app/web`, `database/orders` and `secret/stripe` there with the same names as in `default`: each Ready; `docker ps` shows `es-app-web.shop-staging`; inside the network `orders.database.shop-staging.internal` answers.
  3. A bucket `uploads-staging` in `shop-staging`: its `objectstore-credentials` can read it and can't list a bucket of `default`.
  4. `enclave user invite contractor@example.test --project-role shop-staging=editor`: open the link in a private window, set a password; the contractor sees only `shop-staging`; a `default` resource's URL answers "not found"; deleting the project is refused (`403`).
  5. As the contractor, create a token with `--role viewer`: it reads `shop-staging` and every write answers `403 FORBIDDEN`.
  6. Remove the contractor: their session and token stop working at the next request.
  7. `enclave audit` lists the invite, the sign-in, the token and the removal.
- **Error paths**: deleting a non-empty project → `409 PROJECT_NOT_EMPTY` listing what's left; demoting the only admin → `409 LAST_ADMIN`; a used invite link → `410`; a reference to a secret of another project → `422`.
- **Automated checks**: `go test ./internal/service ./internal/httpapi -run 'Project|Role|Invite|Token'`; `go test ./internal/store -run Migrate`; `go test ./internal/cli -run 'Projects|InvitesAreEmailed'`; `go test ./internal/httpapi -run TestInvitesAreEmailed`; `pnpm test` (`operate-flows.test.tsx`, `mock-operate.test.ts`); the console's Playwright suite (`console.spec.ts`: projects, invite a viewer); `make e2e` (`TestOperateStackForRealWithOpenTofu`).

### Feature: Metrics and dashboards — L2-009 MET-01 – MET-07, DSH-01 – DSH-04, ADR-037
- **Steps and expected results**: with an app behind a domain and a queue, wait a minute: `enclave metrics app/api` shows `cpu`, `memory`, `netRx` and `requests` points; send load with `hey`/`curl` and `requests` rises; `enclave metrics queue/emails --metric queueDepth` follows sends and receives; the console's Metrics tab draws them for 1 h and 24 h. `enclave dashboard create shop-health --widget "title=API rps,metric=requests,resources=app/api"` and the Monitoring section draws it; deleting `api` shows "deleted" in the widget. `curl /metrics | grep enclave_resource_value` lists the series.
- **Error paths**: a metric the kind doesn't have → `422`; `from` after `to` → `422`.
- **Automated checks**: `go test ./internal/monitor -run Sample`; `make engine-docker` (`TestStatsAndLogsSinceOfARunningApp`); `make provider-acceptance` (`TestAccOperateStack`); `make e2e`; `L5-003` sampling budget.

### Feature: Alerts and channels — L2-009 ALR-01 – ALR-08, NCH-01 – NCH-06, ADR-038, ADR-039
- **Steps and expected results**:
  1. `enclave channel create ops-hook --webhook https://<a request bin> --secret hook-key`; `channel test ops-hook` reports delivered and the bin shows a signed JSON body. An email channel through `shop-mail` puts a message in its Messages tab.
  2. `enclave alert create api-crashing --crash-loop app/api --restarts 3 --window 10m --channel ops-hook`; make `api` exit on start: within a minute of the third restart the alert is Firing and one notification arrives; fix the app and a resolution arrives.
  3. A metric alert `--metric percentFull --resource volume/data --above 90 --for 1m` fires after filling the volume.
  4. Restart EnclaveStack while an alert fires: no second notification.
- **Error paths**: deleting a channel an alert uses → `409`; an unreachable webhook shows retries in the Deliveries tab, then failed with an event.
- **Automated checks**: `go test ./internal/monitor -run 'Alert|Notification|Channel'`; `go test ./internal/cli -run Monitoring`; `make e2e` (a machine alert fires to a webhook).

### Feature: Log export — L2-009 LOG-01 – LOG-08, ADR-040
- **Steps and expected results**: `enclave logexport create archive --bucket logs/shop --flush 10s`: within 20 s a gzip object appears under `shop/<date>/<hour>/` with the app's lines; an OTLP export to a local collector (`otel/opentelemetry-collector` with the debug exporter) prints the lines with `service.name`; a Loki export shows them in `{project="default"}`. Stop the collector for a minute, then start it: the lines from the gap arrive, none twice. Restart EnclaveStack: export carries on from its checkpoint.
- **Automated checks**: `go test ./internal/monitor -run LogExport` (incl. `TestLogExportKeepsLinesThatShareATimeAndReportsRotationGaps`); `make engine-docker` (`TestStatsAndLogsSinceOfARunningApp`); `make e2e`.

### Feature: External secret stores — L2-009 EXT-01 – EXT-08, ADR-041
- **Steps and expected results**: run a dev Vault (`hashicorp/vault` in dev mode) and put `secret/stripe key=sk_test`; `enclave secretstore create vault-dev --provider vault --address http://127.0.0.1:8200 --token-secret vault-token --wait` (Ready means the test passed); `enclave secret create stripe --from-store vault-dev --key stripe --field key --wait`; an app with `secretEnv STRIPE=stripe` sees `sk_test`. Change it in Vault and `secret refresh stripe`: version 2 and an event. Stop Vault: the secret keeps its value with a message saying why.
- **Error paths**: a wrong token → the store is Failed with the provider's message; a missing key → the secret is Failed; setting a value on a synced secret → `422`; deleting a store in use → `409`.
- **Automated checks**: `go test ./internal/secretstores/...` (fakes of all five APIs, SigV4 verified); `make engine-docker` (`TestVaultAndOpenBaoForReal`: real Vault 1.20 and OpenBao 2.4); `go test ./internal/httpapi -run TestSecretsSync` (refresh while the engine is down); `make e2e`.

## 2f · v0.8 walk-throughs (planned — run once each feature ships)

The second machine in these walks is either a real Linux computer on the same network, or a Docker-in-Docker container acting as one (`L4-001` §v0.8): `make machine2` starts it with the Linux `enclave` binary inside and prints the join command; on Docker Desktop, run the server with `make server-in-docker`.

### Feature: Machines and the agent — L2-010 MCH-01 – MCH-12, ADR-042, ADR-043
- **Prerequisites**: the server on the Docker driver with v0.7 resources in it (an upgrade), listening where the other computer can reach it (`--listen 0.0.0.0:7070`).
- **Steps and expected results**:
  1. After the upgrade, `enclave machine ls` shows the main machine `online` with the machine's name; every resource reports `machine` = that name; `tofu plan` shows no change.
  2. `enclave machine add rack-1 --label disk=ssd` prints a join command; the console's Machines page shows `rack-1` `waiting`.
  3. On the other computer, run the command: within 15 s `rack-1` is `online` with its CPUs, memory, engine and agent version; the event list has `joined` and `online`.
  4. Run the same command again: `410 JOIN_CODE_INVALID`.
  5. `curl -k https://rack-1:7071/agent/v1/info` without a client certificate: the TLS handshake is refused.
  6. Stop the agent: after 30 s `rack-1` is `offline`, with an event; an alert with `--machine-offline rack-1` fires. Start it: `online`, and the alert resolves.
  7. `enclave machine update rack-1 --schedulable=false`: new resources never land there.
  8. `enclave machine remove rack-1` with something on it: refused with the list.
- **Error paths**: an expired code → `410`; an agent of another minor version → the machine shows the mismatch and takes no placements; removing the main machine → `409`.
- **Automated checks**: `go test ./internal/agent/... ./internal/machines/...` (mutual TLS refusals, join codes, renewal); `go test ./internal/httpapi -run Machine`; `make e2e-machines`.

### Feature: Placement and networks across machines — L2-010 PLC-01 – PLC-08, XNET-01 – XNET-08, ADR-044, ADR-045
- **Steps and expected results**:
  1. `enclave db create orders --machine rack-1 --wait`: `docker ps` on rack-1 shows `es-database-orders`; `get` shows `machine: rack-1` and `host` = rack-1's address.
  2. An app `api` on the main machine with `DATABASE_HOST=orders.database.internal` connects; `nslookup orders.database.internal` inside it gives the same address as on rack-1.
  3. A network `shop` (`10.40.0.0/16`) with `api` on the main machine and `worker` on rack-1: they reach each other by `.internal` name and private IP; a container in another network can't reach either.
  4. A firewall allowing only `api` to reach `worker` on 8080: `api` gets through, a second app on the main machine is blocked.
  5. `enclave machine get rack-1` shows its link to the main machine as connected.
  6. A domain on the main machine routes to an app on rack-1 over HTTPS.
  7. Create ten apps without `--machine`: they spread by free memory; an app with `--machine-label disk=ssd` lands on rack-1.
  8. `iperf3` between an app on each machine: record the throughput and added latency (`L5-003`).
- **Error paths**: `--machine nowhere` → `422`; an app mounting a volume on rack-1 with `--machine <the main machine's name>` → `422`; a machine without room → the resource waits with "No machine has … MiB free".
- **Automated checks**: `go test ./internal/engine/cluster/... ./internal/reconciler -run 'Placement|Mesh'`; `make engine-docker` (`TestMeshBetweenTwoEngines`, with a Docker-in-Docker engine); `make e2e-machines`.

### Feature: Offline machines and moving — L2-010 FAIL-01 – FAIL-06, MOVE-01 – MOVE-07, MCH-08, ADR-046, ADR-047
- **Steps and expected results**:
  1. With `api` (two replicas, no volume) and `orders` on rack-1, power rack-1 off (or `docker pause` its container): within 2 minutes `api` runs on the main machine with an event naming both; `orders` waits with "rack-1 is offline since …".
  2. Power it on: `api`'s old containers are gone from rack-1 (`docker ps`), `orders` carries on with its rows.
  3. `enclave move database/orders --to main --wait`: the CLI prints bytes copied and the downtime; the rows are there; `api` reconnects by the same name; `privateIp` is unchanged.
  4. `enclave move app/api --to rack-1` while `hey` sends requests through its domain: no failed request.
  5. A VM with a disk volume moves to rack-1 and boots there with its files; SSH works at the new `sshAddress`.
  6. `enclave machine drain rack-1 --wait`: everything not pinned is on the main machine; pinned resources are listed.
- **Error paths**: moving `orders` while rack-1 is offline → `409 MACHINE_OFFLINE`; a move that fails mid-copy (stop the agent during the copy) → `move_failed`, and `orders` runs on its old machine with its data.
- **Automated checks**: `go test ./internal/reconciler -run 'Offline|Reschedule|Move|Sweep|Drain'`; `make e2e-machines`.

### Feature: Catalogue and stacks — L2-010 CAT-01 – CAT-09, ADR-048
- **Steps and expected results**:
  1. `enclave catalogue ls` lists the built-ins; `catalogue show wordpress` lists its parameters and members.
  2. `enclave stack create blog --template wordpress --param domain=blog.localhost --wait`: `blog`, `blog-db`, `blog-content`, `blog-site` and their secrets are Ready; `https://blog.localhost` shows the WordPress installer; an upload survives a redeploy.
  3. `enclave db delete blog-db` → `409 PART_OF_STACK`; `enclave restart app/blog` works.
  4. `enclave stack update blog --param memoryMiB=768`: the app redeploys with 768 MiB; `--param domain=` removes `blog-site`.
  5. `enclave catalogue add ./shop-worker.yaml`; `stack create staging-worker --template shop-worker --project shop-staging`.
  6. `enclave stack delete blog` lists the database and the volume and asks; after it, nothing named `blog*` is left.
  7. In OpenTofu, `enclave_stack` with `template = "ghost"` applies, plans clean, and destroys.
- **Error paths**: a template with an unknown kind → `422` naming the line; a stack whose member name exists → `422`, nothing created; a parameter of the wrong type → `422`.
- **Automated checks**: `go test ./internal/catalogue/...` (every built-in renders and validates); `go test ./internal/service ./internal/httpapi -run Stack`; `make provider-acceptance` (`TestAccStack`); `pnpm test` (catalogue and stack flows); `make e2e-machines` (WordPress for real).

## 2g · Console v4 walk-throughs (built 2026-10-23)

Run against `make demo` (fake engine, sample data) in Chrome, light and dark, at desktop width and at 390 px.

### Feature: Navigation, page header and full pages — L2-001 CON-61, CON-63, CON-64; L2.5-002 §16
- **Steps and expected results**:
  1. The sidebar shows Overview, then Compute, Data, Networking, Messaging and media, Security, Install, Operate, Settings with the sections of `L2.5-002` §16 and their counts. Closing Data and reloading keeps it closed; opening a Data page opens it.
  2. Fold the sidebar to the rail: icons only, a tooltip names each; no button or link sticks out of the rail, and the expand button sits under the logo; reload keeps the rail; expand restores the full sidebar.
  3. Open Apps: a full-width table with name, state, key fact, project (All projects) and last change. Click `api`: its page opens at `/apps/<id>` with the breadcrumb `demo › All projects › Apps › api`; clicking **Apps** in the breadcrumb or **Back** returns to the list; clicking the machine opens Machines.
  4. New app → the side panel; on Create the new app's page opens.
  5. At 390 px the sidebar is a sheet from the menu button; the list is cards.
- **Error paths**: an unknown id (`/apps/app_nope`) shows "That resource doesn't exist" with a link back to Apps; a list that fails to load shows the message and Retry.
- **Automated checks**: `pnpm --filter @enclavestack/console test` (navigation groups, links in the breadcrumb, full-page routes); `pnpm --filter @enclavestack/console e2e` ("the sidebar folds to a rail with every control inside it, and opens again").

### Feature: Overview — CON-65
- **Steps and expected results**:
  1. `/` opens the Overview: a greeting, "Everything is running.", four stat tiles (Running 13, Need attention 0, Machines online 1 of 1, Memory reserved), the machine's charts, Recently changed, What's running.
  2. Make a resource fail (`--fake-faults orders:permanent` on a fresh `make demo`): the line reads "1 resource needs attention.", the tile turns red, **Needs attention** lists `orders` with its reason; **Retry** queues a reconcile.
  3. Switch the project to `shop`: every number follows it.
- **Automated checks**: `console/src/overview.test.tsx` (the summary line, the tiles, Needs attention with Retry, quick actions and none for viewers).

### Feature: Section summaries and guides — CON-66, CON-67
- **Steps and expected results**:
  1. Every section's header has its one-line blurb, **Guide**, and stat tiles; an empty section explains itself and links its guide.
  2. Each guide has the five parts in order with their colours and headings, a CLI command that runs as written against `make demo`, and no comparison with another company's product.
  3. At the end of a guide, **Full <section> guide on enclavestack.soupler.com** opens that service's page on the website in a new tab; the DNS records guide opens the zones page.
  4. Find (⌘K) → "data" lists Databases, its guide and resources named like it; Enter opens the first.
- **Automated checks**: `console/src/guides.test.tsx` (every section has a complete guide, no comparisons with other companies' products, Find, section summaries, the guide page, the website link); every guide command was run against `make demo` on 2026-10-23 and works as written (zones and secret stores need a real account at their provider).

### Feature: Calmer screen, hints and motion — CON-68, CON-69
- **Steps and expected results**: list and detail pages show status as a dot and a word; no circled nav icons; with "Reduce motion" on in the OS nothing animates; every form field has a hint.
- **Automated checks**: the design-system test (no styling in `console/src`); `console/src/form-hints.test.ts` (every field of a resource form has a hint); `packages/ui/src/pages.test.tsx` (count-up with and without motion, Find, guide parts).

### Feature: Per-app bucket access — L2-011 BKA-01 – BKA-10, CON-70, CON-71, CLI-53, TF-48

- **Golden path (real Docker, real object store)**: buckets `media` and `invoices`; app `reader` with `media:read`, app `writer` with `media:readwrite`. `reader` lists and reads `media`; `writer` writes and deletes in it; neither reaches `invoices`; the shared project key reaches both.
- **Change**: `reader` to `readwrite` → after its new revision it writes, with the same keys (the credentials secret's value is unchanged).
- **Removal**: `--bucket ""` or deleting the app → its user is gone; its old keys are refused; the credentials secret is deleted.
- **Jobs**: an image job with access reads its bucket; a job with `app` uses the app's access; giving it its own → `422`.
- **Error paths**: an unknown bucket → `422 NOT_FOUND`; the same bucket twice → `422`; level other than `read`/`readwrite` → `422`; `env` with `AWS_ACCESS_KEY_ID` and bucket access → `422`; deleting `media` while `reader` can access it → `409 RESOURCE_IN_USE` naming `app/reader`.
- **Secrets**: the keys appear in no response, event or log; only `secret get <app>-bucket-credentials --reveal` shows them.
- **Surfaces**: console, CLI and OpenTofu each create, change, show and remove the access.

### Feature: Transcoder renditions, HLS ladder, notify and tags — L2-012 TRX-01 – TRX-10, CON-72, CON-73, CLI-54, TF-49

- **Golden path (real FFmpeg)**: a portrait clip tagged `video-id=v42` lands in `incoming/`; the transcoder (4-rung ladder, a 720×1280 MP4 rendition, output folder `processed/{meta:video-id}/`) writes `processed/v42/hls/master.m3u8` listing 4 rungs with bandwidth and real resolutions, each rung's playlist and segments, and `processed/v42/selfie.mp4`; every output carries `video-id`; the signed webhook arrives once with `succeeded`, the outputs, `hlsMaster` and `metadata`.
- **Keyframes**: segment 1 of every rung starts on a keyframe at the same timestamp.
- **Never scaled up**: a 640×360 input stays 640×360 in a 1280×720 box.
- **Failure**: a file that isn't media fails its run and the webhook says `failed` with FFmpeg's reason; a missing tag the folder needs fails the run naming it.
- **Delivery**: a webhook that answers 500 is tried 3 times and recorded as an event; the run's status doesn't change. A topic gets the same body.
- **Error paths**: no presets, renditions or ladder → `422`; a rendition named like a preset → `422`; `segmentSeconds` not a multiple of `gopSeconds` → `422`; an odd width → `422`; an unknown secret or topic → `422 NOT_FOUND`; deleting the secret or topic a transcoder names → `409`.
- **Surfaces**: console, CLI and OpenTofu each create, change and show renditions, the ladder and notify.

### Feature: Bucket objects and readable details — L2-013 OBJ-01 – OBJ-06, DET-01 – DET-05, CON-74, CON-75, CLI-55, TF-50

- **Golden path (real object store)**: upload two files with a tag into a new folder from the console; they list with sizes; download one and compare bytes; delete one; a transcoder watching the folder converts the upload and its message carries the tag. Same with `enclave bucket upload/objects/download/delete-object` and `enclave_bucket_object` (a second apply changes nothing).
- **Error paths**: a viewer lists → `403`; a key with `..` or a leading `/` → `422`; over 5 GiB → `413`; a bucket still being made → `409`; a missing object → `404`.
- **Details**: on every kind's page, no label or value is cut at 1280 px or 375 px; every detail shows its explanation; copy buttons copy the raw value.

## 2h · Public access walk-throughs (`L2-014`)

Run the live checks with a phone on **mobile data** (not the home Wi-Fi), so the request really comes from the internet.

### Feature: Quick links and the gate — L2-014 TUN-01, TUN-04 – TUN-07, GATE-01 – GATE-08, CON-76, CON-79, CLI-56, TF-51, ADR-051, ADR-053
- **Golden path (live, no account)**: app `web` (`nginx:1.27-alpine`); `enclave tunnel create demo --provider quick --app web --wait` prints a `trycloudflare.com` URL. On the phone: the sign-in page naming the host → sign in as the admin → nginx's page. The app's log shows `X-Forwarded-For` with the phone's address.
- **Restart**: stop the connector by its exact container ID (`docker ps --filter label=enclave.resource-id=<tunnel id>`): the tunnel goes **Reconnecting**, comes back, and the console shows a new URL with a toast; the old URL no longer answers.
- **Gate**: a person with no role in the project signs in → "This environment is private."; ten wrong passwords → throttled, all in the audit log; **Sign out** → back to sign-in; remove a person from the project while they're signed in → their next request after a minute goes to sign-in; a request with `X-Enclave-User: admin@…` from the phone → the app sees the real user or none.
- **Password and open gates**: `--gate password --gate-password-secret demo-pass` → the browser's prompt; the right pair passes, a wrong one doesn't; change the secret → the old pair stops working within a minute. `--gate none` → the CLI asks to confirm; with `--yes` the page opens for anyone; the console shows **Open to everyone**.
- **Isolation**: through the quick URL, `/api/v1/machine` reaches the app (nginx's 404), never EnclaveStack; with `Host` set to another domain or `localhost` → the fixed 404.
- **Error paths**: `--provider quick` without `--app` → `422`; a missing app → `422 NOT_FOUND`; deleting `web` while the tunnel uses it → `409 RESOURCE_IN_USE`.

### Feature: Tailscale, ngrok, custom connectors, the catalogue and Make public — L2-014 TUN-12 – TUN-16, CON-80, CLI-56, TF-51, ADR-051, DAR-043
- **Tailscale private (live)**: a free tailnet set up as `L4-001` asks; **Catalogue › Public access › Tailscale › Install** for app `web`, mode **Private** → within a minute the link shows `https://default-web.<tailnet>.ts.net`. A phone with Tailscale signed in opens it (no gate by default); a phone without Tailscale can't resolve or reach it.
- **Tailscale public**: switch the link to **Public** → the gate becomes **Sign in** unless changed; a phone without Tailscale, on mobile data, reaches the sign-in page, then the app. Restart the connector by its exact ID → the same address afterwards. Delete the link → the device is gone from the Tailscale admin console.
- **Tailscale errors**: HTTPS certificates off in the tailnet → the link `failed` saying where to turn them on; Funnel not allowed → `failed` naming the policy attribute; a revoked key → `failed` asking for a new key.
- **ngrok (live)**: install with a free token → the account's address serves the app after ngrok's warning page; the page shows the plan's limits. A second ngrok link on the same free account → `failed` with ngrok's reason.
- **Custom**: a custom connector with `cloudflare/cloudflared`, command `tunnel --url $ENCLAVE_UPSTREAM` → it runs and the app answers at the URL it logs (typed as `url`); `env` setting `ENCLAVE_UPSTREAM` → `422`.
- **Make public**: from an app's page, pick an installed way in → the link is created and listed under **Public addresses**; pick Cloudflare → the domain form opens prefilled.

### Feature: Cloudflare tunnels and public domains — L2-014 TUN-02, TUN-08 – TUN-11, PUB-01 – PUB-10, GATE-09, CON-76, CON-77, CLI-56, CLI-57, TF-51, TF-52, ADR-051
- **Setup (live)**: a Cloudflare account with a test zone, a token as `L4-001` asks, `enclave zone create test --provider cloudflare …`, `enclave tunnel create home --provider cloudflare --credentials cf --account-id <id> --wait`. Cloudflare's dashboard shows tunnel `enclave-default-home`, **Healthy**.
- **Golden path**: `enclave domain create dev.<zone> --app web --tunnel home --zone test --gate signin --wait` → a proxied CNAME to `<id>.cfargotunnel.com` and a routing rule appear at Cloudflare. On the phone, `https://dev.<zone>` → sign-in → the app; the certificate is Cloudflare's. Records that were already in the zone (stand-ins for prod) are unchanged afterwards. A second project with a subdomain zone `stage.<zone>` (Route 53 delegation) and its own tunnel → its own domain and gate (a dev-only member can't pass stage); naming another project's tunnel or zone → `422 NOT_FOUND`; a subdomain zone that isn't a zone at the provider → the zone `failed` with PUB-09's message.
- **Access**: add `--access-email <your email>` → Cloudflare asks for the one-time code first, then EnclaveStack's sign-in. Remove it → the Access application is gone.
- **Drift**: delete the CNAME and edit the routing rules in Cloudflare's dashboard → both are back within 10 minutes, with events.
- **Moving**: `domain update dev.<zone> --tunnel ""` → the record and rule are removed and the domain is local again; back with `--tunnel home`.
- **Teardown**: delete the domains, then the tunnel → nothing EnclaveStack made is left at Cloudflare (tunnel, records, rules, Access).
- **Error paths**: a token without Tunnel Edit → the tunnel `failed` with the permission to add; a zone at Route 53 with a Cloudflare tunnel → `422` with PUB-02's text; `--tls internal` with a tunnel → `422`; deleting `home` while domains use it → `409` naming them; a zone in another Cloudflare account → the domain `failed` saying so.

### Feature: Edge machines and relays — L2-014 EDG-01 – EDG-06, TUN-03, PUB-03, CON-78, CLI-58, TF-53, ADR-052
- **Setup (live)**: a VPS with Docker and the four ports open; the main machine at home behind its router, nothing forwarded. `enclave machine add vps1 --edge --address <vps ip>` prints the command; run it on the VPS → within 10 s the machine is **Online** with the **Edge** chip; `docker exec es-system-mesh wg show` on both sides shows a recent handshake.
- **Golden path**: `enclave tunnel create edge --provider relay --machine vps1`; `enclave domain create stage.<zone> --app web --tunnel edge --zone r53 --wait` (zone at Route 53) → an A record to the VPS; Let's Encrypt issues the certificate through the relay; the phone opens `https://stage.<zone>` → gate → app; the app's log shows the phone's address.
- **Nothing on the VPS**: `docker exec` into the relay and the VPS's files hold no certificate, key or secret; `tcpdump` on the VPS shows only TLS on 443.
- **Placement**: create ten apps → none lands on `vps1`; `machine update vps1 --schedulable` → `422`.
- **Failure**: power off the VPS → the tunnel and `stage` show **Reconnecting** and the machine-offline alert fires; power on → back with no action.
- **Join error paths**: a wrong code on the VPS → the main machine keeps trying and nothing joins; five wrong proofs → the agent stops listening; an expired code → "The join code expired" on the machine; deleting `vps1` while the relay names it → `409 MACHINE_NOT_EMPTY`.
- **Automated stand-in**: `make e2e-machines EDGE=1` runs this walk with a Docker-in-Docker edge machine and Pebble as the CA (the join from the main side, the relay, the certificate through it, the visitor's address, nothing on the edge machine, removal refused); run it before the live walk.
- **Surfaces**: the console, CLI and OpenTofu each add the edge machine, create the relay tunnel and the public domain, and show them.

## 2i · Connections walk-throughs (`L2-015`)

### Feature: A Supabase connection — L2-015 CNX-01 – CNX-07, CON-81, CLI-50, CLI-52, TF-44, TF-45
- **Golden path (live, free Supabase project)**: **Catalogue › Connections › Supabase › Install**, name `supa`, the project address and anon key from the Supabase dashboard → the stack is ready with secrets `supa-url` and `supa-anonkey`, and **Outputs** shows `SUPABASE_URL · supa-url` and `SUPABASE_ANON_KEY · supa-anonkey` only. An app `api` (`curlimages/curl:8.11.1`, command `sh -c 'curl -s -o /dev/null -w "%{http_code}\n" "$SUPABASE_URL/rest/v1/" -H "apikey: $SUPABASE_ANON_KEY"; sleep 3600'`) with those two secret variables → its log shows `200`.
- **Keys**: add the `service_role` key on **Edit parameters** → `supa-servicekey` and its output appear and the anon key is unchanged; rotate it at Supabase, paste the new one, restart `api` → it still answers `200`. No key value is in the console, `enclave stack get supa -o json`, events or `enclave logs`.
- **CLI and OpenTofu**: `enclave catalogue ls` shows `supabase` under `connections`; `enclave stack create supa2 --template supabase --param project=abcd.supabase.co --secret-param anonKey=…` and an `enclave_stack` with `template = "supabase"` and `secret_parameters` make the same secrets; `enclave stack get` prints the outputs.
- **Error paths**: no anon key → `422` naming `spec.secretParameters.anonKey`; `https://abcd.supabase.co` as the address → `422` (a host name); deleting the stack while `api` uses `supa-url` → `409 RESOURCE_IN_USE`; after `api` is gone, deleting it removes every `supa-*` secret.
- **Automated**: `internal/catalogue` (`TestSupabaseConnection`, `TestOutputsSkipEmptySecretParameters`), `internal/httpapi` (`TestASupabaseConnection`), the console's `mock-machines.test.ts` and `machines-flows.test.tsx`.

## 3 · Earlier versions

The console runs against the in-memory mock with `VITE_ENCLAVE_API=mock pnpm dev`: demo admin `admin@enclave.local` / `enclave-demo`; `/apps?first-run` walks setup with code `DEMO-SETU-PCOD`; names starting `fail-` fail on purpose so Retry can be tried.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Initial skeleton from soupler-governance-template v1.2.1 |
| v2 | 2026-10-20 | Soupler | Web console v0.1 section (mock backend) |
| v3 | 2026-10-20 | Soupler | v0.2 walk-throughs: first run, console CRUD for five kinds, tokens, CLI, OpenTofu, recovery and drift |
| v4 | 2026-10-20 | Soupler | OpenTofu walk-through uses TF_CLI_CONFIG_FILE; console demo setup code |
| v5 | 2026-10-20 | Soupler | §2a v0.3 walk-throughs (planned): zero-downtime deploys, domains and HTTPS, jobs, backups and restore, builds |
| v6 | 2026-10-21 | Soupler | Zero-downtime deploys walk-through runnable (phase 1 shipped) |
| v7 | 2026-10-21 | Soupler | Domains walk-through runnable (phase 2 shipped); edge ports and loopback guard |
| v8 | 2026-10-21 | Soupler | Jobs walk-through as shipped (--run-timeout, run --wait) |
| v9 | 2026-10-21 | Soupler | Backups walk-through as shipped and walked live |
| v10 | 2026-10-21 | Soupler | Build-from-Git walk-through as shipped and walked live |
| v11 | 2026-10-21 | Soupler | §2b v0.4 walk-throughs (planned): networks and private DNS, firewalls, replicas and load balancers, DNS zones and records, DNS-01 certificates |
| v12 | 2026-10-21 | Soupler | §2b v0.4 walk-throughs shipped; networks, firewalls, replicas and load balancers walked live on Docker Desktop |
| v13 | 2026-10-21 | Soupler | Load balancer load test as built (make load-lb) |
| v14 | 2026-10-21 | Soupler | §2c v0.5 walk-throughs (planned): filesystem volumes and snapshots, VMs, VMs in networks, disk volumes and snapshots |
| v15 | 2026-10-21 | Soupler | §2c shipped and walked live on an Apple M4; measured results; architecture error path as built |
| v16 | 2026-10-21 | Soupler | §2d v0.6 walk-throughs (planned): database engines, queues and topics, search, email, CDN, transcoders |
| v17 | 2026-10-22 | Soupler | §2d shipped: exercised by the built-binary e2e, the real-Docker suite and Playwright; what was found and fixed; what is not yet walked by hand; automated checks name the real tests |
| v18 | 2026-10-22 | Soupler | §2e v0.7 walk-throughs (planned): projects and roles, metrics and dashboards, alerts and channels, log export, secret stores |
| v19 | 2026-10-22 | Soupler | §2e: walk-throughs live; automated checks name the tests that exist |
| v20 | 2026-10-22 | Soupler | §2e: checks for emailed invites, rotation gaps, real Vault/OpenBao, refresh while the engine is down |
| v21 | 2026-10-22 | Soupler | §2f v0.8 walk-throughs (planned): machines and the agent, placement and networks across machines, offline machines and moving, catalogue and stacks |
| v22 | 2026-10-22 | Soupler | §2f: `make server-in-docker` for Docker Desktop; the WordPress walk names `blog-content` and changes memory, not a folder size |
| v23 | 2026-10-22 | Soupler | §2g console v4 walk-throughs (planned): navigation and full pages, overview, section summaries and guides, calmer screen and motion |
| v24 | 2026-10-23 | Soupler | §2g built: the automated checks named |
| v25 | 2026-10-23 | Soupler | Placement error path no longer uses the machine name `main` |
| v26 | 2026-10-24 | Soupler | Per-app bucket access: golden path, change, removal, jobs, error paths, secrets, surfaces (L2-011) |
| v27 | 2026-10-24 | Soupler | Transcoder renditions, ladder, notify and tags: golden path, keyframes, scaling, failure, delivery, errors, surfaces (L2-012) |
| v28 | 2026-10-24 | Soupler | Bucket objects and readable details: golden path, errors, details (L2-013) |
| v29 | 2026-10-24 | Soupler | §2g: the rail keeps every control inside it and opens again; guides link to the website's full guide |
| v30 | 2026-10-24 | Soupler | §2h public access walk-throughs: quick links and the gate, Cloudflare tunnels and public domains, edge machines and relays, each with live checks from a phone on mobile data (L2-014) |
| v31 | 2026-10-25 | Soupler | §2i connections: the Supabase connection walk-through (L2-015) |
