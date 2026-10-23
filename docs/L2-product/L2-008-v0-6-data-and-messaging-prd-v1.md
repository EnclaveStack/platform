# L2-008 · v0.6 "Data and Messaging" PRD

---
| Field | Value |
|---|---|
| Document ID | L2-008-v0-6-data-and-messaging-prd-v1 |
| Layer | L2 — Product |
| Status | Draft |
| Version | v2 |
| Created | 2026-10-21 |
| Last Updated | 2026-10-22 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

This PRD sets what v0.6 adds so that a team can move data between its services and serve it to users, the way they would with SQS, SNS, RDS, OpenSearch, MediaConvert, CloudFront and SES on AWS, or Pub/Sub, Cloud SQL and Cloud CDN on GCP:
- queues: messages that wait until a worker takes them, with retries and a dead-letter queue;
- topics: messages fanned out to every subscriber: queues, webhooks and apps listening live;
- more database engines: MySQL, MongoDB and ClickHouse next to PostgreSQL;
- search: an OpenSearch service;
- transcoders: video and audio dropped into a bucket come out in the formats asked for;
- CDN: a caching layer with its own hostname in front of an app or a bucket;
- email: sending from apps, either caught inside EnclaveStack to look at, or relayed through the team's mail provider.

It traces to the roadmap in `L1-002` §4 (v0.6) and builds on:
- `L2-004`: the lifecycle rules apply to every new kind unchanged;
- `L2-005`: domains, the edge proxy, runs and backups;
- `L2-006`: networks and private DNS.

Product decisions taken with the owner on 2026-10-21:
- v0.6 covers the whole roadmap row, not only queues and topics.
- Queues and topics run on NATS JetStream (`L3-003` ADR-029).
- Search runs OpenSearch, for its compatibility with AWS OpenSearch and Elasticsearch clients (ADR-031).
- Email is caught or relayed through a provider; EnclaveStack never delivers mail straight from the machine, because mail from home connections is treated as spam (ADR-032).

The surface requirements live in their own PRDs:
- console: `L2-001` CON-37 – CON-44;
- CLI: `L2-002` CLI-29 – CLI-36;
- OpenTofu: `L2-003` TF-27 – TF-33.

Engineering: `L3-002` §9, `L3-003` ADR-029 – ADR-034, `L3-004` DAR-021 – DAR-026, `L3-005` §8.

## 1 · The user story

> A team runs a shop on EnclaveStack v0.5: `web`, `api`, a PostgreSQL `orders` database and a bucket `uploads`. When an order is placed, `api` publishes it on a topic `orders`. The topic fans out to a queue `emails`, read by a `mailer` worker that sends receipts through an email service `shop-mail` (caught inside EnclaveStack while testing, relayed through their provider in production), and to their accounting firm's webhook. A queue `emails-failed` collects receipts that failed five times. Analytics move to a ClickHouse database, a legacy service needs MySQL, and the catalogue search runs on OpenSearch. Product videos uploaded to `uploads/videos/` come out as 720p MP4, HLS and a thumbnail under `media/`, served to shoppers through a CDN at `cdn.shop.example`. They want all of it from any surface, and described in OpenTofu.

## 2 · New kinds and capabilities

| Kind / capability | What the user gets | Replaces (AWS / GCP) | Required input | Optional input | What the user reads back |
|---|---|---|---|---|---|
| Database engines (capability of `database`) | MySQL, MongoDB and ClickHouse databases, managed like PostgreSQL ones | RDS MySQL, DocumentDB, ClickHouse Cloud / Cloud SQL MySQL, Firestore-like | `engine` | `version` | engine, version, address, user, password secret; ClickHouse's native port too |
| `queue` (new kind) | A queue of messages: a worker receives one, and it is hidden from other workers until deleted or until its visibility timeout ends | SQS / Pub/Sub pull subscription | name | visibility timeout, retention, maximum message size, receives before dead-lettering, dead-letter queue | broker address, subject, user and password secret, messages waiting and in flight |
| `topic` (new kind) | A topic: each message published is delivered to every subscription | SNS / Pub/Sub topic | name | retention, subscriptions (queues, webhooks) | broker address, subject, user and password secret, delivery counts |
| `search` (new kind) | An OpenSearch service with a user and password | OpenSearch Service / Elastic on GCP | name | version, memory, network and subnet | address, URL, user, password secret |
| `transcoder` (new kind) | Media written to a bucket folder is converted to the chosen formats and written to another folder | MediaConvert / Transcoder API | input bucket, output bucket, at least one preset | input and output folders, watch on/off, timeout, memory | runs, one per file, with what each produced |
| `cdn` (new kind) | A hostname serving an app or a bucket through a cache on this machine, over HTTPS | CloudFront / Cloud CDN | name, hostname, origin | default cache time, cache size, TLS mode, DNS zone | URL, certificate state, cached size; purge |
| `email` (new kind) | An SMTP server for apps, with a user and password. It keeps every message to look at, and in relay mode delivers them through the team's provider | SES / third-party relays | name | mode (`capture` or `relay`), relay settings, sender domain, messages kept, network and subnet | SMTP address, user, password secret, messages |

## 3 · Requirements

### Database engines

| ID | Requirement | Priority |
|---|---|---|
| DBE-01 | A database's `engine` is `postgres` (default), `mysql`, `mongodb` or `clickhouse`. Versions: PostgreSQL 16 and 17; MySQL 8.4; MongoDB 7.0 and 8.0 (7.0 is the default); ClickHouse 25.8 and 26.3 (both long-term releases). Each engine has a default version (17 is not the default for PostgreSQL: 16 stays it, as in v0.2) | Must |
| DBE-02 | Every engine gets one database and one user named after the resource (hyphens become underscores), and a managed password secret, as PostgreSQL does (CP-05) | Must |
| DBE-03 | The outputs give the same fields for every engine (`internalHost`, `internalPort`, `host`, `port`, `database`, `username`, `passwordSecret`) and add `engine`. ClickHouse's `port` is its HTTP interface; its native protocol is `nativePort` / `internalNativePort` | Must |
| DBE-04 | Engine and version can't change (CP-04) | Must |
| DBE-05 | Backups (BAK-*) cover every engine, each with its own logical format: `mysqldump` for MySQL, `mongodump` archives for MongoDB, and ClickHouse's own `BACKUP … TO File` archive. Restoring into a new database and over the source work as for PostgreSQL | Must |
| DBE-06 | Placement in networks, private DNS and firewalls apply to every engine (NET-*, FW-*) | Must |

### Queues

| ID | Requirement | Priority |
|---|---|---|
| QUE-01 | A queue keeps each message until a worker deletes it, or until its retention ends (1 – 336 hours, default 96) | Must |
| QUE-02 | A received message is hidden from other workers for the visibility timeout (1 – 43 200 s, default 30). Not deleted in time, it is delivered again, with a receive count one higher | Must |
| QUE-03 | `maxReceives` (1 – 1000, or 0 for no limit, the default) bounds how many times a message is delivered. With a `deadLetterQueue`, a message received that many times moves there; without one, it stays until its retention ends but is not delivered again, and the queue's events say so | Must |
| QUE-04 | A message is UTF-8 text up to `maxMessageBytes` (1 KiB – 1 MiB, default 256 KiB), with up to 10 attributes (name → text). A `deduplicationId` drops a repeat of the same message sent within 2 minutes | Must |
| QUE-05 | Apps send and receive through the broker with any NATS client, with the queue's own user and password, which reach only that queue. The control-plane API also sends, receives (long-polling up to 20 s, up to 10 messages), deletes by receipt, and peeks at waiting messages without receiving them | Must |
| QUE-06 | A queue reports how many messages wait, how many are in flight, and the oldest one's age | Must |
| QUE-07 | `purge` deletes every message at once | Should |
| QUE-08 | Settings can change in place; messages are kept | Must |
| QUE-09 | Deleting a queue that a topic subscribes or another queue dead-letters into is refused with them listed (CP-12) | Must |

### Topics

| ID | Requirement | Priority |
|---|---|---|
| TOP-01 | A topic delivers each published message to every subscription, at least once. Subscriptions are queues (`queue: <name>`) and webhooks (`url`) — up to 20 | Must |
| TOP-02 | A webhook receives an HTTP POST with the message as the body, its ID, topic and attributes as headers and, when the subscription names a secret, an HMAC-SHA256 signature of the body. A 2xx reply is delivery; anything else is retried with backoff, 10 times over about 17 minutes, then dropped with an event | Must |
| TOP-03 | A webhook is an `https://` URL; `http://` is accepted for `localhost` and `127.0.0.1` only | Must |
| TOP-04 | A new subscription receives messages published after it was added. Removing one stops its deliveries | Must |
| TOP-05 | A topic keeps published messages for its retention (1 – 336 hours, default 24), so a subscription that is failing can catch up | Must |
| TOP-06 | Apps publish through the broker with any NATS client and the topic's own user, and can listen to it live on its subject (a message reaches only listeners connected at the time). The control-plane API publishes too | Must |
| TOP-07 | A topic reports how many messages it delivered and how many deliveries gave up, per subscription | Should |
| TOP-08 | Deleting a topic never deletes its subscribed queues | Must |

### Search

| ID | Requirement | Priority |
|---|---|---|
| SRC-01 | A search service is one OpenSearch node, version `2` (2.19) or `3` (3.3), default `3`, with an `admin` user whose password is a managed secret. The version can't change (CP-04) | Must |
| SRC-02 | It takes `memoryMiB` (1024 – 16 384, default 2048, steps of 256); half goes to the Java heap. Memory can change, which restarts it | Must |
| SRC-03 | It speaks the OpenSearch REST API over HTTP with basic authentication on port 9200, reachable by private DNS inside its network and on a loopback port from the machine | Must |
| SRC-04 | Indexes survive restarts, recreation and upgrades of EnclaveStack (CP-13) | Must |
| SRC-05 | Placement, private DNS and firewalls apply (NET-*, FW-*) | Must |

### Transcoders

| ID | Requirement | Priority |
|---|---|---|
| TRN-01 | A transcoder names an input bucket and folder and an output bucket and folder. On the same bucket, neither folder may contain the other | Must |
| TRN-02 | Presets come from a fixed list: `mp4-480p`, `mp4-720p`, `mp4-1080p` (H.264 and AAC), `webm-720p` (VP9 and Opus), `hls-720p` (an HLS playlist and segments), `mp3` (audio only) and `thumbnail` (one JPEG). Video is never scaled up | Must |
| TRN-03 | With `watch` on (default), each object that appears in the input folder after the transcoder became ready is converted once. Objects already there are not converted | Must |
| TRN-04 | A file can be converted on request, by its key, whether or not it was converted before | Must |
| TRN-05 | Each file is a run (`L2-005` JOB-04): status, duration, the outputs written, and the end of FFmpeg's output when it failed. `timeoutSeconds` (60 – 86 400, default 3600) bounds one run. A file that isn't media fails its run with FFmpeg's reason; the transcoder stays ready | Must |
| TRN-06 | Outputs go to `<output folder>/<input key without the input folder and extension>/<preset>.<extension>`; HLS writes `<preset>/index.m3u8` and its segments | Must |
| TRN-07 | `concurrency` (1 – 4, default 1) runs that many files at once; `memoryMiB` (256 – 8192, default 1024) bounds each | Should |
| TRN-08 | Deleting a bucket a transcoder uses is refused (CP-12). Deleting a transcoder keeps what it wrote | Must |

### CDN

| ID | Requirement | Priority |
|---|---|---|
| CDN-01 | A CDN serves a hostname through the edge proxy, with the same TLS modes, redirects and DNS zone choice as a domain (DOM-03, DOM-04, CRT-*). A hostname belongs to one domain or one CDN | Must |
| CDN-02 | Its origin is `app/<name>` (port optional, default the app's) or `bucket/<name>`. A bucket behind a CDN serves its objects by key, read-only, to anyone who can reach the CDN | Must |
| CDN-03 | Responses to GET and HEAD are cached as the origin's `Cache-Control` says; when it says nothing, for `defaultTtlSeconds` (0 – 31 536 000, default 3600). `no-store`, `private` and responses that set cookies are never cached. Every response carries `X-Cache: HIT`, `MISS`, `EXPIRED` or `BYPASS` | Must |
| CDN-04 | The cache holds at most `maxSizeMiB` (64 – 102 400, default 1024), least recently used first out. It survives restarts | Must |
| CDN-05 | `purge` empties the cache | Must |
| CDN-06 | When the origin can't be reached, a cached copy is served even if it expired (stale), up to a day | Should |
| CDN-07 | A CDN reports its URL, certificate state and cached size | Must |

### Email

| ID | Requirement | Priority |
|---|---|---|
| EML-01 | An email service is an SMTP server for apps, on port 1025 inside its network and a loopback port on the machine, with a user and a managed password; apps send with any SMTP library | Must |
| EML-02 | In `capture` mode (default) every message is kept and nothing is delivered. In `relay` mode every message is also delivered through the relay: host, port (default 587), `starttls` (default), `tls` or `none`, and a secret holding the username and password | Must |
| EML-03 | The last `keepMessages` messages (10 – 10 000, default 500) can be listed and read: sender, recipients, subject, text and HTML bodies, and whether it was handed to the relay (a refusal by the provider shows in the service's logs) | Must |
| EML-04 | The control-plane API sends a message (from, to, cc, bcc, reply-to, subject, text and/or HTML) | Must |
| EML-05 | With `domain` set, the API refuses senders outside it. SMTP from apps is not checked; the relay provider checks senders | Should |
| EML-06 | Mode and relay settings can change in place; kept messages survive the change | Must |
| EML-07 | Placement, private DNS and firewalls apply (NET-*, FW-*) | Must |

## 4 · Applies to every new kind

`L2-004` CP-01 – CP-19 apply unchanged to `queue`, `topic`, `search`, `transcoder`, `cdn` and `email`:
- one API, asynchronous phases, idempotent creates, `If-Match`, retry and backoff;
- crash recovery, drift repair, events, and deletion rules;
- parity across the console, CLI and OpenTofu.

`GET /machine` (CP-16) counts the memory every new kind reserves, and reports the broker (`broker.available`, its message).

## 5 · Order of delivery

1. **Database engines.** MySQL, MongoDB and ClickHouse, with backups.
2. **Queues and topics.** The broker, queues, topics, subscriptions, dead letters.
3. **Search.**
4. **Email.**
5. **CDN.**
6. **Transcoders.**

**Delivery status (2026-10-22):** phases 1 – 6 are built and tested on every surface, on the fake engine and on real Docker (Apple M4, Docker Desktop 29.7.2). v0.6 is not released yet.

| Phase | Requirements | Built | Where it's recorded |
|---|---|---|---|
| 1 · Database engines | DBE-01 – DBE-06 | Yes. MongoDB defaults to 7.0 (below) | ADR-030 |
| 2 · Queues and topics | QUE-01 – QUE-09, TOP-01 – TOP-08 | Yes | ADR-029 |
| 3 · Search | SRC-01 – SRC-05 | Yes | ADR-031 |
| 4 · Email | EML-01 – EML-07 | Yes | ADR-032 |
| 5 · CDN | CDN-01 – CDN-07 | Yes | ADR-033 |
| 6 · Transcoders | TRN-01 – TRN-08 | Yes | ADR-034 |

**Tested end to end** (`tests/e2e`, `L5-001` §2d): OpenTofu applies the shop from §1 with the built binaries. Then:
- an app container publishes to the topic with the topic's own NATS user, and is refused on a queue's subject;
- the message reaches the queue and a signed webhook;
- a message received twice without being deleted moves to the dead-letter queue;
- MySQL answers SQL with the managed password;
- the CLI sends an email and lists it;
- a video put into the bucket is converted by the transcoder, and the CDN serves the thumbnail, from its cache the second time;
- a second plan is empty, and destroy removes the broker, CDN and email containers;
- the server logs no errors.

**As built:**
- DBE-02: MongoDB 7.0 is the default version. MongoDB 8.0 and later refuse to start on Linux kernels 6.19 – 7.0.13 (SERVER-121912), which includes Docker Desktop's at the time. 8.0 stays available. When it refuses, the database fails with a message saying to use 7.0 or update the kernel.
- QUE-03: a message moves to the dead-letter queue the next time a worker asks for messages after its last allowed receive, as in SQS. Nobody polling means it stays until its retention ends.
- TRN-04, TRN-07: a file asked for by hand while `concurrency` conversions are running is refused (`RUN_IN_PROGRESS`), so the caller tries again. Watched files over the limit wait for the next listing.
- SRC-05, EML-07: search and email services first ignored their network, and firewalls refused them as targets. Both were found by the requirement tests and fixed before release (L3-002 §9.6).
- EML-02: a relay without TLS is signed in to with LOGIN, because Mailpit refuses PLAIN without encryption.
- Every surface: published host ports are now fixed when a container is created. Docker Desktop picked a new port on each restart, and the addresses EnclaveStack reported went stale. Found while testing the broker; it applies to every kind.

## 6 · Out of scope for v0.6

- An SQS-, SNS- or Pub/Sub-compatible wire API: apps use NATS clients or EnclaveStack's API.
- FIFO queues with groups, delayed messages, message filters on subscriptions, push subscriptions to apps inside a network (subscribe a queue instead).
- Search clusters of several nodes, OpenSearch Dashboards, search snapshots to buckets.
- Delivering email directly from the machine (no relay), DKIM signing, receiving email.
- Purging single paths from a CDN, CDN in front of VMs, edge locations on other machines.
- Transcoding presets of the user's own, GPU encoding, live streams.
- Backups of queues, topics and search services.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-21 | Soupler | v0.6 scope: database engines, queue, topic, search, transcoder, cdn and email kinds (DBE, QUE, TOP, SRC, TRN, CDN, EML requirements). Scope, broker, search engine and email approach decided with the owner |
| v2 | 2026-10-22 | Soupler | Delivery status: phases 1 – 6 built and tested on every surface and on real Docker; as-built notes (MongoDB 7.0 default, dead letters at the next receive, manual transcodes refused at concurrency, search and email placement fixed, LOGIN relay auth, fixed host ports) |
