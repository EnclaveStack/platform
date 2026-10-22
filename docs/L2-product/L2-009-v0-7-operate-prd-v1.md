# L2-009 · v0.7 "Operate" PRD

---
| Field | Value |
|---|---|
| Document ID | L2-009-v0-7-operate-prd-v1 |
| Layer | L2 — Product |
| Status | Draft |
| Version | v5 |
| Created | 2026-10-22 |
| Last Updated | 2026-10-22 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs. All rights reserved. |
| License | MIT |
---

## Purpose

This PRD sets what v0.7 adds so that a team can run EnclaveStack day to day, the way they would with CloudWatch, SNS, IAM and Secrets Manager integrations on AWS, or Cloud Monitoring, Cloud Logging sinks and IAM on GCP:
- projects: separate namespaces for resources, with their own people and tokens;
- users and roles: more people than one admin, with admin, editor and viewer roles on the whole machine or on chosen projects;
- scoped tokens: API tokens limited to a role and to chosen projects;
- metrics and dashboards: CPU, memory, network, disk, request rates and queue depths for every resource, with graphs in the console;
- alerts: rules that fire when a metric crosses a line, an app keeps crashing, a disk is nearly full or a resource fails;
- notification channels: where alerts go, by email, webhook, Slack, Discord or Telegram;
- log export: app and service logs sent to a bucket, an HTTP endpoint, OpenTelemetry or Grafana Loki;
- external secret stores: secrets read from HashiCorp Vault or OpenBao, 1Password, AWS Secrets Manager, Doppler or Infisical.

It traces to the roadmap in `L1-002` §4 (v0.7) and builds on:
- `L2-004`: the lifecycle rules apply to every new kind unchanged;
- `L2-008`: email services deliver alert emails.

Product decisions taken with the owner on 2026-10-22:
- **Projects are full namespaces now**, not folders: two projects can each have an app called `web`. Everything that exists goes into a `default` project, which keeps today's addresses, private DNS names and OpenTofu IDs, so nothing that runs today breaks (`L3-003` ADR-035).
- **Metrics are stored by EnclaveStack itself**, in its own database: no Prometheus container. `/metrics` stays for anyone who runs Prometheus (ADR-037).
- **Secret stores**: Vault (and OpenBao), 1Password (through a Connect server), AWS Secrets Manager, Doppler and Infisical (ADR-041).
- **Log destinations**: a bucket (EnclaveStack's or any S3), an HTTP endpoint, OpenTelemetry (OTLP/HTTP) and Grafana Loki (ADR-040).

The surface requirements live in their own PRDs:
- console: `L2-001` CON-45 – CON-53;
- CLI: `L2-002` CLI-37 – CLI-45;
- OpenTofu: `L2-003` TF-34 – TF-41.

Engineering: `L3-002` §10, `L3-003` ADR-035 – ADR-041, `L3-004` DAR-027 – DAR-033, `L3-005` §9, `L3-006` §9, `L3-007` §12.

## 1 · The user story

> The shop team from v0.6 grows. They keep the shop in a project `shop`, and start a project `shop-staging` with the same names (`web`, `api`, `orders`), so their OpenTofu module is applied twice with only the project changed. A contractor is invited as an editor of `shop-staging` only; the accountant gets a viewer role on `shop` to look at dashboards. CI deploys with a token that is an editor of `shop` and nothing else. A dashboard `shop-health` shows the API's requests per second, CPU and memory of every app, and the depth of the `emails` queue. An alert `api-crashing` emails the on-call address through `shop-mail` and posts to their Slack channel when `api` restarts three times in ten minutes; `disk-nearly-full` fires when the `data` volume is 90 % full; `emails-backlog` fires when 1 000 receipts wait for more than five minutes. All logs of `shop` go to their Grafana Cloud through OpenTelemetry, and a nightly archive lands in the `logs` bucket. The Stripe key lives in their 1Password and the database password of a legacy service in Vault; both reach `api` as ordinary EnclaveStack secrets. They want all of it from any surface, and described in OpenTofu.

## 2 · New kinds and capabilities

| Kind / capability | What the user gets | Replaces (AWS / GCP) | Required input | Optional input | What the user reads back |
|---|---|---|---|---|---|
| Projects (capability of every kind) | Namespaces of resources, each with its own people and tokens | Accounts / projects | project name | description | resources per kind |
| Users and roles | People with machine-wide or per-project roles | IAM users, roles | email, role | project roles | invite link, last sign-in |
| Scoped tokens | Tokens limited to a role and projects | IAM access keys with policies | name, role | projects, expiry | prefix, last use |
| Metrics | Graphs for every resource and the machine | CloudWatch metrics | — | — | points over time |
| `dashboard` (new kind) | A named page of graphs | CloudWatch dashboards | name, widgets | — | — |
| `alert` (new kind) | A rule that fires and resolves, and notifies channels | CloudWatch alarms / alerting policies | name, condition, channels | duration, repeat, enabled | state, incidents |
| `channel` (new kind) | A place alerts go | SNS topics, Chatbot / notification channels | name, type and its settings | — | deliveries; test |
| `logexport` (new kind) | Logs sent somewhere else, continuously | Subscription filters / log sinks | name, destination | sources, filter, flush interval | lines sent, lag, last error |
| `secretstore` (new kind) | A connection to an outside secret manager | Secrets Manager / Secret Manager integrations | name, provider and its settings | refresh interval | reachable; test |
| External secrets (capability of `secret`) | A EnclaveStack secret whose value comes from a secret store | — | store, key | field | last synced, version |

## 3 · Requirements

### Projects

| ID | Requirement | Priority |
|---|---|---|
| PRJ-01 | A project has a name (the resource name rules) and an optional description of up to 280 characters. A project `default` always exists and can't be deleted or renamed | Must |
| PRJ-02 | Every resource belongs to exactly one project, chosen when it is created (default `default`). It can't move to another project | Must |
| PRJ-03 | A name is unique per kind **within a project**: two projects can each have an app `web`. Four things stay unique across the machine, because the outside world sees them: bucket names (the S3 bucket name), domain and CDN hostnames, DNS zones (the domain they serve), and network address ranges (one host can't route overlapping ranges) | Must |
| PRJ-04 | Every reference in a spec (a secret in `secretEnv`, a network, an app, a queue, a bucket, a channel, a store, …) names a resource in the same project. There are no references across projects | Must |
| PRJ-05 | Resources in `default` keep the container names, private DNS names, database names and outputs they have today. A resource in another project answers to `<name>.<kind>.<project>.internal` inside its network, and its containers carry the project in their name, so nothing of two projects ever collides | Must |
| PRJ-06 | Every project gets its own managed object-store credentials (`objectstore-credentials` in that project), which reach only that project's buckets. `default`'s keep reaching every bucket in `default`, as before | Must |
| PRJ-07 | A project is deleted only when it's empty; otherwise the refusal lists what's left | Must |
| PRJ-08 | Every list and count is per project, or across the projects the caller can see. A caller never learns that a resource exists in a project they can't see: it's "not found" | Must |
| PRJ-09 | OpenTofu import IDs and CLI names are `<name>` in `default` and `<project>/<name>` elsewhere; IDs (`app_…`) keep working everywhere | Must |

### Users and roles

| ID | Requirement | Priority |
|---|---|---|
| IAM-01 | There are three roles. **Viewer** reads resources, status, events, logs, runs, metrics, dashboards and alerts, but never secret values, run output, queue messages or emails. **Editor** does everything a viewer does, and creates, changes, deletes and acts on resources, and reveals secrets. **Admin** does everything an editor does, and manages people and tokens in its scope | Must |
| IAM-02 | A user has an optional **machine role**, which applies to every project, and any number of **project roles**. In a project, the stronger of the two applies. Only a machine admin manages projects, the machine's settings and other users' machine roles | Must |
| IAM-03 | The person who set the machine up is a machine admin. The last machine admin can't be removed or lose the role | Must |
| IAM-04 | A machine admin invites a person by email with roles. EnclaveStack shows a link with a one-time code, valid 7 days, to send them. Opening it, the person sets their password and is signed in. A new invite replaces the old code. When an email service and a `from` address are configured for invites, the link is also emailed | Must |
| IAM-05 | A project admin adds, changes and removes roles of existing users in its project, and invites people to its project only | Should |
| IAM-06 | Removing a user ends their sessions and revokes their tokens at once. Changing a user's roles applies to their next request | Must |
| IAM-07 | Every user can change their own password, which needs the current one and ends their other sessions. A machine admin can issue a reset link for another user (same flow as an invite) | Must |
| IAM-08 | Events, logs and the audit list name the person (email) who made each change, not only an ID | Must |
| IAM-09 | A machine admin can read an audit list: sign-ins, failed sign-ins, users, roles, tokens and projects created, changed or removed, and secret reveals, with who, when and from which address, newest first, kept 90 days | Should |
| IAM-10 | A request the caller's role doesn't allow is refused with `403 FORBIDDEN` naming the role needed. Pages and buttons the caller can't use are hidden or disabled with the reason | Must |

### Scoped tokens

| ID | Requirement | Priority |
|---|---|---|
| TOK-01 | A token has a name, a **role** (viewer, editor or admin), a list of **projects** or "all projects", and an optional expiry | Must |
| TOK-02 | A token never does more than its user may do now: its effective role in a project is the weaker of the token's role and the user's current role there | Must |
| TOK-03 | Tokens made before v0.7 keep working: `read` becomes viewer and `write` admin, on all projects. *As built:* `write` maps to admin, not editor, because a v0.6 `write` token could change machine settings and editor can't; a new token without a role gets its owner's strongest role | Must |
| TOK-04 | A machine admin can list and revoke every user's tokens | Should |
| TOK-05 | A token limited to projects can't see other projects in lists, counts or the machine's totals by project | Must |

### Metrics

| ID | Requirement | Priority |
|---|---|---|
| MET-01 | EnclaveStack samples every resource that runs something every 30 seconds: apps (all replicas together, and per replica), databases, caches, search and email services, CDNs, load balancers, VMs and the system containers. Each gets CPU (in CPU cores), memory used and its limit, network received and sent (bytes per second), disk read and written (bytes per second) and restarts | Must |
| MET-02 | Some kinds add their own: requests per second, 5xx responses per second and average response time for apps and CDNs served through the edge (by domain or CDN); queue depth, messages in flight and the oldest message's age for queues; deliveries and failed deliveries per second for topics; used bytes and percent full for volumes; cached bytes for CDNs | Must |
| MET-03 | The machine has its own: CPU used, memory used, disk free and percent full where EnclaveStack keeps its data, and resources by phase | Must |
| MET-04 | Points are kept at 30-second detail for 24 hours and as 15-minute averages and maximums for 30 days. Deleting a resource deletes its points | Must |
| MET-05 | A graph can show the last 1 hour, 6 hours, 24 hours, 7 days or 30 days, or a range in between; EnclaveStack picks a step that gives at most 500 points | Must |
| MET-06 | Sampling costs less than 2 % of one CPU and 30 MB of memory with 50 resources, measured on the reference machine (`L5-003`) | Must |
| MET-07 | `/metrics` adds every resource's metrics as Prometheus gauges labelled with project, kind and name, for people who run Prometheus | Should |

### Dashboards

| ID | Requirement | Priority |
|---|---|---|
| DSH-01 | A dashboard has a name and 1 – 24 widgets. A widget has a title, one metric and 1 – 10 resources of the dashboard's project (`kind/name`), drawn as lines over time | Must |
| DSH-02 | A widget naming a resource that no longer exists shows "deleted" for it, and the rest still draws | Must |
| DSH-03 | Every resource's page has a Metrics tab with its standard graphs, without making a dashboard | Must |
| DSH-04 | The machine has a dashboard of its own (MET-03), always present | Must |

### Alerts

| ID | Requirement | Priority |
|---|---|---|
| ALR-01 | An alert has one condition, one or more channels, and is `enabled` unless turned off | Must |
| ALR-02 | Conditions: **metric** (a metric of 1 – 10 resources is above or below a threshold for `forMinutes`, 1 – 60, default 5; each resource is judged on its own), **crash loop** (a resource restarted at least `restarts` times, default 3, within `windowMinutes`, default 10), **failed** (a resource entered the `failed` phase), and **run failed** (a run of a job, backup or transcoder failed or timed out) | Must |
| ALR-03 | "Disk nearly full" is a metric condition on a volume's or the machine's percent full (default 90); "queue backlog" is one on a queue's depth or oldest message's age. The console offers both as starting points | Must |
| ALR-04 | An alert is **ok**, **firing** or **no data** (no points for its metric for `forMinutes`). It notifies its channels when it starts firing and when it resolves; while firing, again every `repeatMinutes` (0, the default, means never) | Must |
| ALR-05 | Each firing is an incident: when it started and resolved, the resource, the value that fired it, and the outcome per channel. Incidents are kept 90 days | Must |
| ALR-06 | `test` sends a test notification through every channel of the alert and reports each outcome | Must |
| ALR-07 | Alerts are checked every 30 seconds; a condition met for its duration fires within one minute | Must |
| ALR-08 | After a restart of EnclaveStack, a firing alert stays firing without notifying again; one that resolved while EnclaveStack was down notifies the resolution | Should |

### Notification channels

| ID | Requirement | Priority |
|---|---|---|
| NCH-01 | A channel is one of: **email** (an email service of the project, a `from` address and 1 – 20 recipients), **webhook** (an `https://` URL; `http://` for `localhost` and `127.0.0.1` only; an optional signing secret), **slack** and **discord** (a secret holding the incoming-webhook URL), **telegram** (a secret holding the bot token, and a chat ID) | Must |
| NCH-02 | A notification says what fired or resolved, on which resource and project, the value and the threshold, when it started, and a link to the alert in the console (using the machine's public address when it has one) | Must |
| NCH-03 | A webhook receives a JSON body with the same facts, and with a secret an HMAC-SHA256 signature as topics use (TOP-02) | Must |
| NCH-04 | A failed delivery is retried 5 times over about 5 minutes; the last 100 deliveries of each channel, with their outcome, can be listed | Must |
| NCH-05 | `test` sends a test message and reports the outcome straight away | Must |
| NCH-06 | Deleting a channel an alert uses, or the email service or secret a channel uses, is refused (CP-12) | Must |

### Log export

| ID | Requirement | Priority |
|---|---|---|
| LOG-01 | A log export reads the logs of its **sources**, `kind/name` of the project, or every resource of the project that has logs when none are named (including ones created later) | Must |
| LOG-02 | Exactly one destination: **bucket** (a bucket of the project and a folder), **s3** (any S3-compatible endpoint, bucket, folder, region and a credentials secret), **http** (a URL that receives JSON lines by POST, with an optional secret of headers), **otlp** (an OTLP/HTTP logs endpoint, with an optional secret of headers), or **loki** (a Loki push URL, with an optional secret of headers, and extra labels) | Must |
| LOG-03 | Each line carries its time, project, kind, name, replica or container, stream (stdout or stderr) and message | Must |
| LOG-04 | Lines are sent in batches, every `flushSeconds` (5 – 300; default 10, or 60 for buckets) or sooner at 1 MiB. A bucket batch is one gzip file of JSON lines under `<folder>/<yyyy>/<mm>/<dd>/<hh>/` | Must |
| LOG-05 | An optional `filter` (a regular expression) keeps only matching lines | Should |
| LOG-06 | Delivery is at least once, in order per source. After a restart of EnclaveStack or of a container, it carries on from the last line sent. While the destination fails, it retries with backoff and catches up afterwards; lines the engine dropped meanwhile are reported as a gap (an event with the time range) | Must |
| LOG-07 | A log export reports lines and bytes sent, the time of the last line sent, how far behind it is, and the last error | Must |
| LOG-08 | The logs API and the console's Logs tabs keep working as before | Must |

### External secret stores

| ID | Requirement | Priority |
|---|---|---|
| EXT-01 | A secret store is one of: **vault** (HashiCorp Vault or OpenBao: address, KV v2 mount, namespace, and a secret holding a token), **onepassword** (a 1Password Connect server URL, a vault, and a secret holding an access token), **aws** (AWS Secrets Manager: region, and a secret holding an access key and secret key), **doppler** (a secret holding a service token), **infisical** (site URL, project ID, environment, and a secret holding a machine identity's client ID and secret) | Must |
| EXT-02 | A secret can take `source` instead of a value: a store of the same project, a `key` (Vault: the path; 1Password: the item; AWS: the secret's name or ARN; Doppler and Infisical: the secret's name) and an optional `field` (Vault and 1Password: the field; AWS: a key of a JSON value) | Must |
| EXT-03 | The value is read when the secret is created and again every `refreshSeconds` of its store (60 – 86 400, default 300), and on `refresh`. EnclaveStack keeps the last value, encrypted like any secret, so apps start even while the store can't be reached | Must |
| EXT-04 | When the value changes, the secret's version goes up and an event says so (without the value). Apps and jobs using it get the new value at their next deploy or run, as with a secret changed by hand | Must |
| EXT-05 | When the store can't be reached or the key is gone, the secret keeps its last value and says why in its message; an alert on `failed` covers a secret that never got a value | Must |
| EXT-06 | A synced secret's value can't be set in EnclaveStack; revealing it works as for any secret | Must |
| EXT-07 | `test` on a store checks that it can be reached and the credentials work, and reports the answer straight away | Must |
| EXT-08 | Deleting a store that secrets use is refused (CP-12) | Must |

## 4 · Applies to every new kind

`L2-004` CP-01 – CP-19 apply unchanged to `dashboard`, `alert`, `channel`, `logexport` and `secretstore`:
- one API, asynchronous phases, idempotent creates, `If-Match`, retry and backoff;
- crash recovery, drift repair, events, and deletion rules;
- parity across the console, CLI and OpenTofu.

Projects and roles apply to every kind, old and new.

## 5 · Order of delivery

1. **Projects, users, roles and scoped tokens.** Everything else is scoped by them.
2. **Metrics and dashboards.**
3. **Notification channels.**
4. **Alerts.**
5. **Log export.**
6. **External secret stores.**

**Delivery status (2026-10-22):** built on all three surfaces (API, CLI, OpenTofu provider, web console), in the order above. Verified by the Go suite with the race detector, the OpenTofu acceptance suite (`TestAccOperateStack`), the console's unit, flow and Playwright suites, and `tests/e2e/operate_test.go` on real Docker and OpenTofu (two projects, Docker metrics, an alert firing to a webhook, a log export, a secret read from Vault).

**As built — closed after the first delivery (2026-10-22):**
- IAM-04: with the machine's `inviteEmail` set (an email service and a sender), invite and reset links are emailed; the link is still shown, and a failed send says why without failing the invite.
- LOG-06: lines sharing one timestamp are each sent once (the checkpoint counts them), and lines the engine dropped before they were read are reported as an `export_gap` event and `enclave_log_export_gaps_total`.
- EXT-05: synced secrets refresh while the engine is down (reading a store doesn't need it).
- EXT-01: the Vault client is verified against real HashiCorp Vault 1.20 and OpenBao 2.4 servers (`TestVaultAndOpenBaoForReal`).

**As built — open:**
- ALR-03: a crash loop counts restarts from Docker's `RestartCount` changes between samples, so restarts faster than the sample interval can count as one.
- NCH-02: **Slack, Discord and Telegram verified for real** (2026-10-22): set up through OpenTofu on a running EnclaveStack, a test, an alert firing and its resolution each arrived in the real channel.
- NCH-02 wording, fixed the same day: messages use the metric's title and unit ("Disk full of this machine has been above 90.0% for 5 minutes (now 91.2%)"), say "this machine" and "1 minute", and link to the console only at an address others can open (`--console-url`, or a non-loopback listen address). The webhook JSON keeps machine-readable fields (`resource: "machine"`). Verified on Slack.
- Not yet walked against the real services, which need the owner's accounts: email through a real provider; 1Password Connect, AWS Secrets Manager, Doppler and Infisical (each tested against a faithful fake of its API).

## 6 · Out of scope for v0.7

- Single sign-on (OIDC, SAML), two-factor sign-in, custom roles, permissions per resource.
- References across projects, moving a resource to another project, quotas per project.
- A query language for metrics, metrics from inside apps (custom metrics), anomaly detection, log search inside EnclaveStack.
- PagerDuty, Opsgenie, Microsoft Teams and SMS channels (a webhook reaches most of them).
- Writing secrets back to an outside store, rotating them, secret stores of other projects.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-22 | Soupler Labs | v0.7 scope: projects as namespaces, users and roles, scoped tokens, metrics, dashboards, alerts, notification channels, log export, external secret stores (PRJ, IAM, TOK, MET, DSH, ALR, NCH, LOG, EXT requirements). Namespaces, built-in metrics, the five secret stores and four log destinations decided with the owner |
| v2 | 2026-10-22 | Soupler Labs | Delivery status: built; TOK-03 as built (`write` → admin); known gaps recorded (invites not emailed, log rotation, refresh while the engine is down, restart counting, real providers unwalked) |
| v3 | 2026-10-22 | Soupler Labs | Closed IAM-04 (emailed invites), LOG-06 (shared timestamps, rotation gaps), EXT-05 (refresh while the engine is down); Vault and OpenBao verified for real; open items listed |
| v4 | 2026-10-22 | Soupler Labs | Slack, Discord and Telegram verified against the real services; message wording to fix recorded |
| v5 | 2026-10-22 | Soupler Labs | NCH-02 wording fixed and verified on Slack; `--console-url` for links |
