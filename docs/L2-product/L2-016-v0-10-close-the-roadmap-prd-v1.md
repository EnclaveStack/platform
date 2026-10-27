# L2-016 · v0.10 "Close the Roadmap" PRD

---
| Field | Value |
|---|---|
| Document ID | L2-016-v0-10-close-the-roadmap-prd-v1 |
| Layer | L2 — Product |
| Status | Draft |
| Version | v1 |
| Created | 2026-10-26 |
| Last Updated | 2026-10-26 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

0.9.0 shipped everything from v0.3 to v0.9. The roadmap (`L1-002` §4) still had open rows and known gaps:
- functions (moved from v0.5);
- preview environments (named in §3.2);
- a control plane that survives its machine;
- database options people ask for (TLS, a setup script, reaching a database from another computer);
- volume backups (only databases and caches had them);
- notifications on events (channels only carried alerts);
- more connections after Supabase.

This PRD specifies all of them as one release, v0.10. Every capability works from the console, the CLI and OpenTofu (`L1-001`, ADR-003).

Builds on:
- `L2-005` jobs, backups and builds from Git;
- `L2-008` queues, topics and transcoders;
- `L2-009` alerts and channels;
- `L2-010` machines;
- `L2-014` public access;
- `L2-015` connections.

Engineering: `L3-002` §18, `L3-003` ADR-055 – ADR-060, `L3-004` DAR-044 – DAR-046, `L3-005` §12. Tests: `L5-001` §2k. User guide: `L5-004` §17.

Product decisions taken with the owner on 2026-10-26:

- **One release, one PR.** The owner asked for everything left to be built together.
- **A function is a container image** started for each event, not code uploaded for a built-in runtime. Any language works, and it reuses the job engine.
- **The control plane gets a standby with failover**, not a consensus cluster. A second machine keeps a recent copy and takes over when told to, or by itself after a chosen time.
- **Previews start from Git webhooks and the CLI.** Pull-request webhooks from GitHub and GitLab make, update and delete previews. `enclave app preview create` makes one by hand or from CI.
- **The new connections are Neon, Firebase, Auth0, Clerk and Sentry.** Stripe was not chosen.

## 1 · Functions (FN)

> **Story.** A team resizes images when they're uploaded. They make a function from their image `ghcr.io/acme/resize:1.4` with a bucket trigger on `uploads/originals/`. Each new file starts one container, which reads `/enclave/event.json`, resizes the file and writes it to `uploads/small/`. The function's page lists every run with its output.

| ID | Requirement | Priority |
|---|---|---|
| FN-01 | A new kind, **function**: an image, and optionally a command, environment, secret environment, memory (64–8192 MiB, default 256), a timeout (1–900 s, default 60), a concurrency (1–20 runs at once, default 5), a network and subnet, bucket access and a machine | Must |
| FN-02 | Up to 10 **triggers**, each exactly one of: a **schedule** (cron and time zone, at most one per function), a **queue** of the project, or a **bucket** folder (bucket and prefix) | Must |
| FN-03 | A **topic** reaches a function through a subscription that names it (`{"function": "resize"}`), beside the existing queue and webhook targets | Must |
| FN-04 | Every event is one **run**: one container, started from the image, that ends when the command exits. Runs are kept and listed as job runs are (JOB-04): trigger, status, exit code, timing and the last 64 KiB of output | Must |
| FN-05 | The container gets the event as JSON in the file `/enclave/event.json`. It also gets `ENCLAVE_EVENT_FILE`, `ENCLAVE_EVENT_SOURCE` (`schedule`, `queue`, `topic`, `bucket`, `invoke` or `manual`), `ENCLAVE_RUN_ID` and `ENCLAVE_FUNCTION`. Each source's shape is fixed in `L3-002` §18.1 | Must |
| FN-06 | **Invoking**: `POST /api/v1/resources/{id}/invoke` with a JSON body (≤ 256 KiB) starts a run and answers its ID. With `?wait=true` it waits up to 60 s and answers the finished run with its output | Must |
| FN-07 | At most **concurrency** runs at once. Extra events wait their turn: queue messages stay in their queue, new objects wait for the next listing, topic messages stay pending, a due schedule is skipped and recorded, and an invocation is refused with `429 FUNCTION_BUSY` | Must |
| FN-08 | **Outcomes.** Exit code 0 succeeds. A queue message is deleted when its run succeeds; otherwise it is received again after the queue's visibility timeout, and the queue's maximum receives and dead-letter queue apply. A topic message is acknowledged on success and retried with the topic's backoff on failure. A failed bucket or schedule run is recorded and not retried | Must |
| FN-09 | A queue trigger needs the queue's visibility timeout to be at least the function's timeout plus 30 s. Otherwise the function fails to provision and names the timeout to set | Must |
| FN-10 | A bucket trigger starts only for objects that appear after the trigger was added, as transcoders do (TRN-03) | Must |
| FN-11 | The **Run** action starts a run with the event `{"source":"manual"}` | Must |
| FN-12 | A function never runs privileged or on the host network, and its secrets never appear in responses, events or logs, as for jobs | Must |

## 2 · Preview environments (PRV)

> **Story.** The app `web` is built from `github.com/acme/web` on `main`. Its owner turns previews on, sets the domain suffix `preview.acme.dev` and adds the webhook URL from the app's page to the GitHub repository. Pull request 42 opens, and `web-pr-42` builds and starts at `web-pr-42.preview.acme.dev`. Each push to the pull request redeploys it, and merging the pull request deletes it. A push to `main` redeploys `web`.

| ID | Requirement | Priority |
|---|---|---|
| PRV-01 | An app built from Git can turn **previews** on, with a time limit (1–720 hours, default 72), a maximum number at once (1–20, default 5) and an optional domain: a suffix with its TLS mode, zone and tunnel | Must |
| PRV-02 | A **preview** is an app named `<app>-pr-<number>` for a pull request, or a name given by hand. It has the parent's spec except for the Git ref, which is the pull request's head. It runs one replica, is never published on a host port, and gets `ENCLAVE_PREVIEW=true` and `ENCLAVE_PREVIEW_REF`. The parent manages it: a preview can be redeployed, restarted and deleted, not edited | Must |
| PRV-03 | **Webhooks.** An app's Git source can name a webhook secret. EnclaveStack then accepts GitHub and GitLab webhooks for the app at `/api/v1/hooks/git/{appId}` and at `/.enclave/hooks/git/{appId}` on every public address it serves. A machine behind NAT can receive them through its tunnel. Requests are checked with the secret (GitHub's `X-Hub-Signature-256`, GitLab's `X-Gitlab-Token`) and refused without a match | Must |
| PRV-04 | Opening, reopening or pushing to a pull request makes or redeploys its preview. Closing or merging it deletes the preview | Must |
| PRV-05 | With **deploy on push** turned on, a push to the app's own branch redeploys the app | Must |
| PRV-06 | `POST /api/v1/resources/{appId}/previews` with a ref and an optional name makes a preview, or redeploys the one with that name. `GET` lists the app's previews | Must |
| PRV-07 | A preview not updated within the time limit is deleted. Making one past the maximum is refused (`409 PREVIEW_LIMIT`); a webhook that would pass it leaves an event on the parent | Must |
| PRV-08 | With a domain suffix, each preview gets the domain `<preview>.<suffix>`, managed by the preview and deleted with it. Through a tunnel it is gated (sign-in) by default, as every public domain is | Must |
| PRV-09 | Deleting the parent deletes its previews | Must |

## 3 · Standby control plane (SBY)

> **Story.** The control plane runs on `mini`. The owner adds a second Mac, `studio`, as a standby. `mini`'s disk dies one night. In the morning they run `enclave standby promote` on `studio`. The console opens on `studio` with every project, secret and setting as they were 15 s before `mini` failed. The apps from `mini` start on the other machines, and `mini` shows as offline.

| ID | Requirement | Priority |
|---|---|---|
| SBY-01 | A machine can join with the role **standby**. It runs no resources, and there is at most one | Must |
| SBY-02 | The main machine sends the standby a **copy of the control plane** (its database and its key) over the machines' mutual-TLS channel. It sends one at most every 15 s, and only when something changed. The standby keeps the last two copies, written whole or not at all and readable only by its user | Must |
| SBY-03 | The standby's page shows when the last copy arrived and its size, and warns when it is more than 2 minutes old | Must |
| SBY-04 | **Promotion.** `enclave standby promote` on the standby makes it the main machine from its latest copy and starts the control plane there. With `--auto-promote-after` on the standby's agent, it promotes itself when the main hasn't reached it for that long. The default is off | Must |
| SBY-05 | After promotion the standby is the main machine. The old main is offline with no address, its apps restart on other machines (ADR-046), and its data resources wait for it | Must |
| SBY-06 | **Fencing.** Each promotion raises the control plane's epoch. Every agent remembers the highest epoch it has seen and refuses calls from a lower one. An old main that is refused stops changing anything: its API answers `503 CONTROL_PLANE_REPLACED` and `/readyz` fails | Must |
| SBY-07 | The guide says plainly what the copy doesn't hold: data on the main machine's own engine (its databases, volumes and buckets), VM snapshot copies and the build cache | Must |

## 4 · Database options (DBX)

| ID | Requirement | Priority |
|---|---|---|
| DBX-01 | **TLS** (PostgreSQL and MySQL). With `tls` on, the database serves a certificate signed by EnclaveStack's database CA and refuses connections without TLS. Its outputs add the CA certificate, and its connection string asks for TLS | Must |
| DBX-02 | The certificate names the database's container, private DNS name, `localhost` and the addresses it is reached at. It lasts a year and is renewed 30 days before it ends | Must |
| DBX-03 | Turning TLS on or off restarts the database. Its data stays | Must |
| DBX-04 | A **setup script** (up to 64 KiB) runs once, when the database is first created: SQL for PostgreSQL, MySQL and ClickHouse, JavaScript for MongoDB. If it fails, provisioning fails with its error. It can't be changed afterwards | Must |
| DBX-05 | **Reachable from other computers**: with `exposed` on, the database's port is open on every address of its machine, not only 127.0.0.1, and its outputs list those addresses. It needs TLS on, so it is available for PostgreSQL and MySQL | Must |

## 5 · Volume backups (VBK)

| ID | Requirement | Priority |
|---|---|---|
| VBK-01 | A backup's source can be a **filesystem volume**. A disk volume is refused, with a pointer to snapshots | Must |
| VBK-02 | A run writes the volume's files as a compressed tar archive while apps keep using it. Each file is copied whole, but files changed during the run may come from different moments | Must |
| VBK-03 | Restoring **replaces** the volume's files, then restarts the apps that mount it, or restores into a **new** volume | Must |

## 6 · Notifications on events (EVN)

| ID | Requirement | Priority |
|---|---|---|
| EVN-01 | An alert can watch **events**: 1–20 resources and 1–20 event names. Each matching event sends one notification to the alert's channels at once. There is no firing or resolved state and nothing repeats | Must |
| EVN-02 | The event names are `ready`, `redeployed`, `failed`, `built`, `build_failed`, `run_failed`, `run_timed_out`, `restored`, `rescheduled`, `moved`, `drift_detected`, `certificate_renewed`, `preview_created` and `preview_deleted` | Must |
| EVN-03 | An event alert starts from the newest event when it is made or turned on. Events from while it was off aren't sent | Must |

## 7 · Connections (CNX)

| ID | Requirement | Priority |
|---|---|---|
| CNX-08 | **Neon**: the pooled connection string (required) and the direct one → `DATABASE_URL`, `DATABASE_URL_UNPOOLED` | Must |
| CNX-09 | **Firebase**: the project ID (required), the web API key and a service account's JSON → `FIREBASE_PROJECT_ID`, `FIREBASE_AUTH_DOMAIN`, `FIREBASE_API_KEY`, `FIREBASE_SERVICE_ACCOUNT` | Must |
| CNX-10 | **Auth0**: the tenant domain and client ID (required) and the client secret → `AUTH0_DOMAIN`, `AUTH0_ISSUER_BASE_URL`, `AUTH0_CLIENT_ID`, `AUTH0_CLIENT_SECRET` | Must |
| CNX-11 | **Clerk**: the secret key (required) and the publishable key → `CLERK_SECRET_KEY`, `CLERK_PUBLISHABLE_KEY` | Must |
| CNX-12 | **Sentry**: the DSN (required) and an auth token → `SENTRY_DSN`, `SENTRY_AUTH_TOKEN` | Must |

## 8 · Surfaces

| ID | Surface | Requirement | Priority |
|---|---|---|---|
| CON-82 | Console | **Functions** under Compute: list, a form with triggers, the run history and an Invoke dialog | Must |
| CON-83 | Console | Topic subscriptions can target a function | Must |
| CON-84 | Console | An app's **Previews** panel: turn previews on, the webhook URL and secret, deploy on push, the list of previews and Create preview | Must |
| CON-85 | Console | Machines: add a standby. The standby's page shows its last copy | Must |
| CON-86 | Console | Database form: TLS, setup script and Reachable from other computers. The detail shows the CA certificate | Must |
| CON-87 | Console | Backup form: a volume as the source | Must |
| CON-88 | Console | Alert form: the Events condition | Must |
| CLI-59 | CLI | `enclave function create / ls / get / update / rm / invoke / run / runs` | Must |
| CLI-60 | CLI | `enclave app preview create / ls / rm`, and `--webhook-secret`, `--deploy-on-push` and `--previews…` flags on `enclave app create/update` | Must |
| CLI-61 | CLI | `enclave machines add --role standby`, `enclave standby status`, `enclave standby promote`, and `--auto-promote-after` on the agent | Must |
| CLI-62 | CLI | `enclave database create/update --tls --exposed --init-script-file` | Must |
| CLI-63 | CLI | `enclave backup create --volume` | Must |
| CLI-64 | CLI | `enclave alert create --event NAME …` and `enclave topic` subscriptions with `function=NAME` | Must |
| TF-54 | OpenTofu | `enclave_function` | Must |
| TF-55 | OpenTofu | `enclave_app`: `previews`, `source.webhook_secret`, `source.deploy_on_push` | Must |
| TF-56 | OpenTofu | `enclave_database`: `tls`, `exposed`, `init_script` | Must |
| TF-57 | OpenTofu | `enclave_backup`: `source.volume`; `enclave_alert`: the `event` condition with `events`; `enclave_topic`: subscriptions with `function` | Must |
| TF-58 | OpenTofu | Connections need nothing new: `enclave_stack` with the new templates | Must |

## 9 · Out of scope

- Warm function containers, and functions written as code for a built-in runtime.
- A consensus cluster of three or more control planes. Copying the main machine's own data (databases, volumes, buckets) to the standby.
- Previews from Bitbucket or Gitea webhooks (the CLI covers them). Comments posted back on pull requests.
- TLS for MongoDB and ClickHouse. Reaching a database from the internet through a tunnel.
- Notifications on a run's success.
- Stripe and other connections (templates of one's own cover them).

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-26 | Soupler | v0.10: functions, previews, standby control plane, database options, volume backups, event notifications and five connections (FN, PRV, SBY, DBX, VBK, EVN, CNX-08 – CNX-12); surfaces CON-82 – CON-88, CLI-59 – CLI-64, TF-54 – TF-58 |
