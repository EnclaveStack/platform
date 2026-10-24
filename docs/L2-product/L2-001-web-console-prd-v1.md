# L2-001 · Web Console PRD

---
| Field | Value |
|---|---|
| Document ID | L2-001-web-console-prd-v1 |
| Layer | L2 — Product Requirements |
| Status | Draft |
| Version | v20 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-24 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
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
| CON-54 | A **Machines** page (everyone who sees the machine; changes for machine admins): each machine with its state, address, CPUs, memory used and reserved, engine and agent version, VM support and resource count. "Add machine" asks for a name and labels and shows the join command, copyable, with what the other computer needs (MCH-03); the page shows the machine as `waiting` until it joins, then `online`, without a reload (`L2-010` MCH-*) | Must — v0.8 |
| CON-55 | A machine's page: its facts, its tunnel link to each other machine (connected, last heard), what runs on it (linked), its events, and the actions **Not schedulable** / **Schedulable**, **Drain** (lists what moves and what is pinned) and **Remove** (refused with the list while anything runs there; `force` for an offline machine, asking first and naming what fails) (MCH-*) | Must — v0.8 |
| CON-56 | Every form of a placed kind has **Machine**: "Choose for me" (default) or a machine, with its free memory, and optional required labels. Every placed resource shows its machine in the header and in lists; a resource on an offline machine shows a banner "`<machine>` is offline since …" (PLC-*, FAIL-01) | Must — v0.8 |
| CON-57 | **Move to another machine** on placed resources: pick the machine; the dialog says what happens (a rolling deploy for apps; a stop, a copy and a start, with the size to copy, for data; the apps or VMs that move with a volume) and the progress shows the bytes copied. Refused while the machine is offline, with the reason (MOVE-*, FAIL-03) | Must — v0.8 |
| CON-58 | The **Machine** dashboard (`L2-009` DSH-04) gains a machine picker; alerts offer **Machine offline** as a condition with the machines to watch (MCH-06) | Must — v0.8 |
| CON-59 | A **Catalogue** page: the built-in entries and the machine's own templates as cards (title, description, what it creates); **Install** opens a form generated from the entry's parameters (name, project, values, an optional domain) and creates the stack. Machine admins **Add template** from a file (errors shown with the line) and remove their own (CAT-01 – CAT-04) | Must — v0.8 |
| CON-60 | A **Stacks** section: list with phase; a stack's page shows its template and version, its parameters, its outputs (links), and its members with their phases, linked. **Edit parameters**, **Upgrade** (when a newer version exists, with what changes) and **Delete** (lists members with data, asks first). A member's page says "Part of stack `<name>`" and hides edit and delete (CAT-05 – CAT-07) | Must — v0.8 |

### Console v4: finding your way, the overview and guides (built 2026-10-23)

Founder feedback (2026-10-22): the console is hard to navigate and doesn't say what each service is for. Twenty-odd sections sit in one flat list; there is no page with the whole picture; breadcrumbs aren't links; a resource opens beside a list instead of on a page of its own; status pills, chips and circled icons crowd every screen. Layout and look: `L2.5-002` §16 and `L2.5-001` §7. No API changes: the overview reads what the console already loads (resources, the machine, machine metrics, machines, alerts).

| ID | Requirement | Priority |
|---|---|---|
| CON-61 | The sidebar opens with **Overview**, then groups the sections by what they're for: **Compute**, **Data**, **Networking**, **Messaging and media**, **Security**, **Install**, **Operate**, **Settings** (members in `L2.5-002` §16). A group opens and closes; the choice is remembered in this browser. The sidebar folds to a rail of icons, also remembered; folded, every control stays inside the rail and the button that opens it again is always in view | Must |
| CON-62 | **Find** (⌘K or Ctrl+K, and a search box in the top bar) jumps to any page or resource by name | Should |
| CON-63 | Every page has a header: a breadcrumb whose parts are links (machine › project › section › resource), the title, a one-line explanation and the page's actions. A resource's page has a Back button to its section | Must |
| CON-64 | A resource opens on a page of its own (`/<section>/<id>`), full width. A section lists its resources full width: a table on wide screens, cards on phones | Must |
| CON-65 | An **Overview** page at `/`: a one-line summary of health ("Everything is running" or what needs attention); headline numbers (resources running, needing attention, machines online, memory reserved); this machine's CPU, memory, disk and failures over the last 24 hours; **Needs attention** (failed resources and firing alerts, each with a way to fix or open it); **Recently changed**; resources by group; quick actions (new app, new database, the catalogue) | Must |
| CON-66 | Each section opens with a short summary: what it is in one sentence, its numbers by state, and a link to its guide; then the list | Must |
| CON-67 | Each section has a **guide** (`/guide/<section>`): colour-coded parts — **What it is**, **When to use it**, **How to use it** (console steps and the same as one CLI command), **Example** (a small real scenario) and **Good to know** (limits, what it costs this machine, common mistakes). Plain words; guides explain EnclaveStack on its own terms and never compare it with other companies' products. Each guide ends with a link, in a new tab, to the service's full guide on the EnclaveStack website (`enclavestack.soupler.com/services/<section>/`), which covers every setting, the API and OpenTofu | Must |
| CON-68 | A calmer screen: status is a small dot with a word; chips only where they carry meaning (the project when all projects show, the machine when there are several); icons without circles. Motion is CSS only — page fade, hover lift, groups opening, charts and numbers easing in — at most 300 ms, and none when the system asks for reduced motion. No animation library | Must |
| CON-69 | Every field of a form that creates or changes a resource has a one-line hint saying what it does and what to enter (sign-in, setup and account fields explain themselves) | Should |
| CON-70 | The app form and the job form (from an image) have **Bucket access**: rows of a bucket (picked from the project's buckets) and a level (**Read** / **Read and write**), with a hint naming the variables the app receives. A job that runs as an app says it uses the app's bucket access (`L2-011`) | Must |
| CON-71 | An app's or job's page shows a **Bucket access** card: each bucket with its level, a link to the credentials secret and the five variable names (`L2-011` BKA-08) | Must |
| CON-72 | The transcoder form has **Renditions**, an **HLS ladder** and **Notify when a file is done**; presets become optional (`L2-012`) | Must |
| CON-73 | The transcoder's page shows its presets, renditions, ladder (with each rung) and notify settings; a run lists the files it wrote, the master playlist first (`L2-012` TRX-10) | Must |
| CON-74 | A bucket's page has an **Objects** tab: browse folders, upload files with tags, download and delete (`L2-013`) | Must |
| CON-75 | Every resource page shows its details as a readable list, each with its explanation, copy buttons and links (`L2-013` DET-*) | Must |
| CON-76 | A **Tunnels** section in the Networking group: list with provider, state and what each serves; a create form that changes with the provider (quick, Tailscale, ngrok, custom, Cloudflare, relay); a tunnel's page with its state, connections or public address, its public domains, and an app link's URL with Copy ("Temporary" for a quick link) (`L2-014`) | Must |
| CON-77 | The domain form has **Public access** (only on this machine, or from the internet through a tunnel) with the zone, the **Gate** (Sign in, Password, Open to everyone after a confirmation), open paths and, for Cloudflare tunnels, **Cloudflare Access** emails; a public domain's page shows the public URL, DNS record, certificate, gate and Access (`L2-014` PUB-08) | Must |
| CON-78 | Machines has **Add an edge machine**: name and public address, then the join command with Copy and the ports to open; an edge machine's page shows its relays and no placement actions (`L2-014` EDG-*) | Must |
| CON-79 | The gate's sign-in page, password prompt and "This environment is private" page (`L2-014` GATE-*, `L2.5-002` §21) | Must |
| CON-80 | The catalogue's **Public access** category with one entry per way in, and **Make public** on an app's page with its public addresses (`L2-014` TUN-15, TUN-16) | Must |

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler | Console v0.1 on the mock backend: sign-in, first-run setup, resource sections, details, logs, secret reveal, sign out (CON-01 – CON-12). Row reconstructed from `L3-002` v1 and `L5-001` v2 |
| v2 | 2026-10-20 | Soupler | Console v0.2 on the real API: API tokens, create, edit, delete, retry, "Use from code" (CON-13 – CON-18). Row reconstructed from the CHANGELOG (v0.2 lists L2-001 v2) |
| v3 | 2026-10-20 | Soupler | v0.3 requirements CON-19 – CON-25: domains, root certificate, revisions and health checks, jobs, backups, Git source (L2-005) |
| v4 | 2026-10-21 | Soupler | CON-19 – CON-25 shipped (v0.3 phases 1–5) |
| v5 | 2026-10-21 | Soupler | v0.4 requirements CON-26 – CON-31: networks, firewalls, replicas and load balancers, DNS zones and records, DNS-01 certificates (L2-006). Restored the "Version history" heading and table header, lost in an earlier edit together with the v1 and v2 row(s), which are not reconstructed (see CHANGELOG) |
| v6 | 2026-10-21 | Soupler | CON-26 – CON-31 shipped; CON-29 as built (two DNS sections) |
| v7 | 2026-10-21 | Soupler | Reconstructed the lost CON-09 – CON-11 (machine card, theme, engine banner) from the built console, and the lost v1 – v2 history; CON-29 as built: one DNS section with each zone's records |
| v8 | 2026-10-21 | Soupler | v0.5 requirements CON-32 – CON-36: VMs, volumes, snapshots, restart warnings, VM availability (L2-007) |
| v9 | 2026-10-21 | Soupler | CON-32 – CON-36 shipped |
| v10 | 2026-10-21 | Soupler | v0.6 requirements CON-37 – CON-44: database engines, queues, topics, search, transcoders, CDN, email (L2-008) |
| v11 | 2026-10-22 | Soupler | CON-37 – CON-44 shipped |
| v12 | 2026-10-22 | Soupler | v0.7 requirements CON-45 – CON-53: projects, people, tokens, roles in the interface, metrics and dashboards, alerts, channels, log exports, secret stores, audit (L2-009) |
| v13 | 2026-10-22 | Soupler | v0.8 requirements CON-54 – CON-60: machines, a machine's page, placement in forms, moving, machine metrics and the offline alert, catalogue, stacks (L2-010) |
| v15 | 2026-10-23 | Soupler | CON-61 – CON-69 built; CON-69 scoped to resource forms |
| v14 | 2026-10-22 | Soupler | Console v4 requirements CON-61 – CON-69 (planned): grouped sidebar, find, clickable breadcrumbs, full-page resources, the overview, section summaries, guides, a calmer screen with CSS-only motion, field hints |
| v16 | 2026-10-24 | Soupler | CON-70, CON-71: bucket access in the app and job forms and pages (L2-011) |
| v17 | 2026-10-24 | Soupler | CON-72, CON-73: transcoder renditions, ladder and notify in the form and page (L2-012) |
| v18 | 2026-10-24 | Soupler | CON-74 bucket objects tab, CON-75 readable details (L2-013) |
| v19 | 2026-10-24 | Soupler | CON-61: folded, the rail keeps every control inside it and its expand button in view; CON-67: each guide links to its full guide on the website |
| v20 | 2026-10-24 | Soupler | CON-76 – CON-80: tunnels, public access on domains, edge machines, the gate's pages, Public access in the catalogue and Make public (L2-014) |
