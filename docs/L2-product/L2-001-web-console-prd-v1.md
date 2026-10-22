# L2-001 · Web Console PRD

---
| Field | Value |
|---|---|
| Document ID | L2-001-web-console-prd-v1 |
| Layer | L2 — Product Requirements |
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

What the EnclaveStack web console must let an operator do. One of three equal surfaces (`L1-001` §2) over the control plane (`L2-004`); parity with the CLI (`L2-002`) and the OpenTofu provider (`L2-003`). Look and behaviour: `L2.5-001`, `L2.5-002`; engineering: `L3-001`, `L3-002`; verification: `L5-001`, `L5-002`.

## 1 · Users

A single **operator** (the machine's admin) in v0.2. Roles and multiple users are later scope.

## 2 · Requirements

### Access

| ID | Requirement | Priority |
|---|---|---|
| CON-01 | Sign in with email + password. A wrong credential shows an inline error and keeps the form filled; repeated failures are throttled and say when to retry | Must |
| CON-02 | First-run setup: enter the one-time setup code printed by `enclave server`, name the machine, create the admin account | Must |
| CON-03 | Unauthenticated visits to any console page redirect to sign-in, then return to the page asked for | Must |
| CON-12 | Sign out | Must |
| CON-13 | **API tokens** page: create a named token (read or read-write, optional expiry), shown exactly once with a copy button; list tokens with last-used time; revoke with confirmation. This is how the CLI and OpenTofu get credentials | Must |

### Machine and layout

*CON-09 – CON-11 were lost in an earlier edit. They are reconstructed (2026-10-21) from the console as built, which cites CON-10 in `console/src/lib/theme.ts`; the wording is new, the behaviour is what shipped.*

| ID | Requirement | Priority |
|---|---|---|
| CON-09 | The sidebar shows the machine: its name and hostname, whether the engine is online, and memory reserved by resources against the memory available to the engine (`L2-004` CP-16) | Must |
| CON-10 | Light, dark, or follow the operating system; the choice is remembered in this browser | Should |
| CON-11 | When the container engine is down, every page shows a banner saying changes are queued and will apply when it's back (`L2-004` CP-17) | Must |

### Resources

| ID | Requirement | Priority |
|---|---|---|
| CON-04 | Sidebar sections with live counts: Apps, Databases, Caches, Buckets, Secrets (v0.2 kinds, `L2-004` §1) | Must |
| CON-05 | Each section lists its resources with phase and a key fact; the list is searchable by name; `/` focuses search; an empty section explains what the kind is and offers to create one | Must |
| CON-06 | Selecting a resource shows its phase and message, provisioning steps, and tabs: Overview (connection details and settings), Logs (apps, databases, caches), Environment (apps), Events | Must |
| CON-07 | Apps: Restart and Redeploy; the list and detail update immediately and follow progress until ready | Must |
| CON-08 | Secret values are never displayed; a secret's detail offers Reveal, which asks for confirmation and hides the value again after 30 s | Must |
| CON-14 | **Create** each kind from "New <kind>" with a form that validates as you type using the same rules as the API (`L3-002` §4), shows server errors next to the field, and lands on the new resource following its progress | Must |
| CON-15 | **Edit** the fields that can change in place (app image, port, environment, secret environment, memory, publish; cache max memory; bucket versioning; secret value). Fields that cannot change are shown read-only with the reason | Must |
| CON-16 | **Delete** with a confirmation that requires typing the resource name and states that data will be lost (databases, caches, buckets). A refusal because of dependants lists them | Must |
| CON-17 | A failed resource shows its message and a Retry button (reconcile action) | Must |
| CON-18 | Each resource's detail shows the equivalent CLI command and OpenTofu block, so operators can move from clicks to code | Should |
| CON-19 | **Domains** section: list with hostname, target app, certificate state and expiry; create from an app's detail ("Add domain") or the section; a domain waiting on DNS shows the record to create and the IP to point it at; the section's settings set the machine's public address and ACME email (`L2-005` DOM-01 – DOM-09) | Must — v0.3, shipped |
| CON-20 | Download the EnclaveStack root certificate from the Domains section, with steps to trust it on macOS, Linux, iOS and Android (DOM-04) | Must — v0.3, shipped |
| CON-21 | App detail shows the serving revision and, during a deploy, the new revision's health-check attempts; a failed revision says the old one is still serving. The app form has a Health check section (type, path, timings) and a Deploy strategy choice (DEP-01 – DEP-06) | Must — v0.3, shipped |
| CON-22 | **Jobs** section: schedule shown in words ("Every day at 03:00, Asia/Kolkata") plus the next run; a Runs tab with status, duration, exit code and the output tail; Run now (JOB-01 – JOB-08) | Must — v0.3, shipped |
| CON-23 | **Backups** section and a Backups tab on databases and caches: runs, size, Run now; Restore offers "into a new database" first, and in-place restore requires typing the source name (BAK-01 – BAK-09) | Must — v0.3, shipped |
| CON-24 | App form offers Image or Git source; a source app shows the built commit and the build log (BLD-01 – BLD-05) | Should — v0.3, shipped |
| CON-25 | "Use from code" covers the new kinds (CLI command and OpenTofu block) | Must — v0.3, shipped |
| CON-26 | **Networks** section: list with CIDR, subnets, internet access and attached resources; create with a subnet editor that checks ranges as you type. App, database, cache and job forms gain a Network and Subnet choice; their detail shows the private IP and the private DNS name (`L2-006` NET-01 – NET-08) | Must — v0.4, shipped |
| CON-27 | **Firewalls** section: rules read as sentences ("Allow tcp 5432 from app/api"); the editor picks targets and sources from lists (resources, networks) or takes a CIDR; a resource's detail lists the firewalls that apply to it (FW-01 – FW-09) | Must — v0.4, shipped |
| CON-28 | App form gains Replicas and the detail shows running replicas. **Load balancers** section: protocol, port, targets with weights, algorithm and health check; detail shows healthy targets and the internal and published address (LB-01 – LB-08) | Must — v0.4, shipped |
| CON-29 | **DNS** section with zones and their records: create a zone (provider, credentials secret; shows its name servers), and records with type, name, values or "Point at this machine". A record that conflicts with one at the provider shows what is there (DNS-01 – DNS-08). As built: the sidebar's **DNS** section lists zones; each zone has a **Records** tab with Add record, and records keep their own pages | Must — v0.4, shipped |
| CON-30 | Domain form: "Get the certificate through DNS" with a zone choice; wildcard hostnames are accepted only then (CRT-01 – CRT-05) | Must — v0.4, shipped |
| CON-31 | "Use from code" covers the v0.4 kinds and fields | Must — v0.4, shipped |
| CON-32 | **VMs** section: list with power state, image, CPUs and memory, private IP and SSH address; create with an image picker (catalog, URL or snapshot), size presets that fill CPUs and memory (editable), SSH keys (paste or secret), first-boot script editor, network and subnet, data disks and published ports. The detail shows the SSH command to copy, `start`, `stop` and `restart` buttons, and a Boot console tab (`L2-007` VM-*, IMG-*, VMN-*) | Must — v0.5, shipped |
| CON-33 | An update that restarts a VM (CPUs, memory, disks, network) says so in the confirmation before it is sent (VM-01, VOL-02, VMN-05) | Must — v0.5, shipped |
| CON-34 | **Volumes** section: list with type, size, used space and what each is attached to; create a disk or filesystem volume, empty or from a snapshot. The app and job forms gain a Volumes editor (volume, path, read-only); the VM form a Disks picker that offers only free disk volumes (VOL-*) | Must — v0.5, shipped |
| CON-35 | **Snapshots** section: list with source, type, size and time; take one from a volume's or VM's detail page ("Take snapshot"); from a snapshot, "Create volume" or "Launch VM" opens the matching form prefilled (SNP-*, VOL-08, IMG-04) | Must — v0.5, shipped |
| CON-36 | The machine card shows whether VMs can run and, if not, the reason and the command to fix it; VM forms are disabled with the same message. "Use from code" covers the v0.5 kinds and fields (VM-11) | Must — v0.5, shipped |
| CON-37 | The database form offers an engine choice (PostgreSQL, MySQL, MongoDB, ClickHouse) with that engine's versions; the detail shows a connection string for the engine with the password left out, and ClickHouse's native port (`L2-008` DBE-*) | Must — v0.6, shipped |
| CON-38 | **Queues** section: list with messages waiting and in flight; create and edit the visibility timeout, retention, size limit and dead-letter settings. The detail has a **Messages** tab: send a message (body and attributes), peek at waiting ones, receive and delete, and purge (asks first) (QUE-*) | Must — v0.6, shipped |
| CON-39 | **Topics** section: list with subscriptions; the form edits subscriptions (a queue picked from a list, or a webhook URL with an optional signing secret). The detail publishes a message and shows deliveries and failures per subscription (TOP-*) | Must — v0.6, shipped |
| CON-40 | **Search** section: version, memory, network; the detail shows the URL, user and an example `curl` with the password left out (SRC-*) | Must — v0.6, shipped |
| CON-41 | **Transcoders** section: input and output bucket and folder, presets as checkboxes, watch, timeout, concurrency. The detail has a **Runs** tab (the key, status, outputs) and "Transcode a file" by key (TRN-*) | Must — v0.6, shipped |
| CON-42 | **CDN** section: hostname, origin (an app and port, or a bucket), cache time and size, TLS and zone, as on the domain form; the detail shows the URL, certificate state, cached size and a Purge button (asks first) (CDN-*) | Must — v0.6, shipped |
| CON-43 | **Email** section: mode, relay host, port, TLS and credentials secret, sender domain, messages kept. The detail has a **Messages** tab listing sender, recipients, subject and relay result; a message opens with its text and HTML (HTML shown sandboxed, without loading remote content); "Send a test message" (EML-*) | Must — v0.6, shipped |
| CON-44 | "Use from code" covers the v0.6 kinds and fields; the machine card shows the broker's state once a queue or topic exists | Must — v0.6, shipped |
| CON-45 | A **project switcher** at the top of the sidebar lists the projects the user can see, plus "All projects"; sections, counts and lists follow it, and "New <kind>" creates in the chosen project. A **Projects** page (machine admins) creates, describes and deletes projects; a refused delete lists what's left (`L2-009` PRJ-*) | Must — v0.7 |
| CON-46 | A **People** page: users with their machine role, project roles and last sign-in; invite (email, roles) shows the link to send, copyable; change roles; remove (asks first); issue a reset link. Project admins see it for their projects. An **/invite** page sets the password from a link (IAM-*) | Must — v0.7 |
| CON-47 | The account menu: change password; **Tokens** gains role and projects on create, and machine admins see every user's tokens (TOK-*) | Must — v0.7 |
| CON-48 | Pages and buttons follow the role: a viewer sees no create, edit, delete, reveal or messages; disabled controls say which role is needed (IAM-10) | Must — v0.7 |
| CON-49 | Every resource that runs something has a **Metrics** tab: its standard graphs with a range picker (1 h, 6 h, 24 h, 7 d, 30 d). A **Monitoring** section holds the machine's dashboard and the project's **dashboards**, with a widget editor (title, metric, resources) (MET-*, DSH-*) | Must — v0.7 |
| CON-50 | **Alerts** section: list with state (ok, firing, no data) and last change; the form picks a condition (metric with resources, threshold and duration; crash loop; failed; run failed), with "Disk nearly full" and "Queue backlog" as starting points, and channels. The detail has an **Incidents** tab and "Send a test" (ALR-*) | Must — v0.7 |
| CON-51 | **Channels** section: the form per type (email service and recipients; webhook URL and signing secret; Slack, Discord or Telegram with a secret); the detail has **Deliveries** and "Send a test" (NCH-*) | Must — v0.7 |
| CON-52 | **Log exports** section: sources picked from the project's resources (or all), destination per type, filter and flush interval; the detail shows lines sent, lag and the last error. **Secret stores** section: the form per provider and "Test connection"; the secret form offers "From a secret store" (store, key, field) next to a value, and a synced secret shows its store, key, last sync and "Refresh now" (LOG-*, EXT-*) | Must — v0.7 |
| CON-53 | "Use from code" covers the v0.7 kinds and the project of every resource; an **Audit** page (machine admins) lists the audit entries (IAM-09) | Must — v0.7 |

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler Labs | Console v0.1 on the mock backend: sign-in, first-run setup, resource sections, details, logs, secret reveal, sign out (CON-01 – CON-12). Row reconstructed from `L3-002` v1 and `L5-001` v2 |
| v2 | 2026-10-20 | Soupler Labs | Console v0.2 on the real API: API tokens, create, edit, delete, retry, "Use from code" (CON-13 – CON-18). Row reconstructed from the CHANGELOG (v0.2 lists L2-001 v2) |
| v3 | 2026-10-20 | Soupler Labs | v0.3 requirements CON-19 – CON-25: domains, root certificate, revisions and health checks, jobs, backups, Git source (L2-005) |
| v4 | 2026-10-21 | Soupler Labs | CON-19 – CON-25 shipped (v0.3 phases 1–5) |
| v5 | 2026-10-21 | Soupler Labs | v0.4 requirements CON-26 – CON-31: networks, firewalls, replicas and load balancers, DNS zones and records, DNS-01 certificates (L2-006). Restored the "Version history" heading and table header, lost in an earlier edit together with the v1 and v2 row(s), which are not reconstructed (see CHANGELOG) |
| v6 | 2026-10-21 | Soupler Labs | CON-26 – CON-31 shipped; CON-29 as built (two DNS sections) |
| v7 | 2026-10-21 | Soupler Labs | Reconstructed the lost CON-09 – CON-11 (machine card, theme, engine banner) from the built console, and the lost v1 – v2 history; CON-29 as built: one DNS section with each zone's records |
| v8 | 2026-10-21 | Soupler Labs | v0.5 requirements CON-32 – CON-36: VMs, volumes, snapshots, restart warnings, VM availability (L2-007) |
| v9 | 2026-10-21 | Soupler Labs | CON-32 – CON-36 shipped |
| v10 | 2026-10-21 | Soupler Labs | v0.6 requirements CON-37 – CON-44: database engines, queues, topics, search, transcoders, CDN, email (L2-008) |
| v11 | 2026-10-22 | Soupler Labs | CON-37 – CON-44 shipped |
| v12 | 2026-10-22 | Soupler Labs | v0.7 requirements CON-45 – CON-53: projects, people, tokens, roles in the interface, metrics and dashboards, alerts, channels, log exports, secret stores, audit (L2-009) |
