# L2-002 · Command-Line Interface PRD

---
| Field | Value |
|---|---|
| Document ID | L2-002-cli-prd-v1 |
| Layer | L2 — Product Requirements |
| Status | Draft |
| Version | v15 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-22 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs. All rights reserved. |
| License | MIT |
---

## Purpose

What the `enclave` command must let a developer do from a terminal or a CI job. Parity with the console (`L2-001`) and the provider (`L2-003`) over the same control plane (`L2-004`). Engineering: `L3-001`, `L3-002`, `L3-003` ADR-008.

## 1 · Shape

One binary, `enclave`, that is both the server (`enclave server`) and the client. Commands read `noun verb` like the `aws` CLI:

```
enclave login [--server URL]            enclave token create|list|revoke
enclave db create orders --version 16 --wait
enclave cache create sessions --max-memory 512
enclave bucket create media --versioning
enclave secret create stripe-key --from-stdin
enclave app create api --image ghcr.io/acme/api:1.4 --port 8080 --env LOG_LEVEL=info --secret-env STRIPE_KEY=stripe-key --publish --wait
enclave app update api --image ghcr.io/acme/api:1.5 --wait
enclave app restart|redeploy api        enclave logs app api --tail 200
enclave ls [--kind database]            enclave get database orders
enclave events app api                  enclave delete cache sessions --yes --wait
enclave secret get stripe-key --reveal  enclave machine
```

## 2 · Requirements

| ID | Requirement | Priority |
|---|---|---|
| CLI-01 | `enclave login` asks for email and password (password hidden), creates a personal API token and stores it with the server URL in `~/.config/enclave/config.json` (mode 0600). `--token` accepts a token created in the console instead | Must |
| CLI-02 | Server and token can also come from `ENCLAVE_SERVER` / `ENCLAVE_TOKEN`, so CI needs no config file. Flags beat environment, environment beats the file | Must |
| CLI-03 | Every service kind supports create, get, list, update (where meaningful) and delete; apps also restart, redeploy and logs | Must |
| CLI-04 | `--wait` blocks until the resource is ready (or gone, for delete), printing progress; `--timeout` bounds it; a failure prints the resource's message | Must |
| CLI-05 | `--output json` prints the API's JSON exactly, for scripting; the default is a readable table | Must |
| CLI-06 | Exit codes are stable: 0 success · 1 error · 2 usage · 3 not found · 4 authentication · 5 conflict · 6 wait timed out | Must |
| CLI-07 | Destructive commands (delete) ask for confirmation on a terminal and require `--yes` when not interactive | Must |
| CLI-08 | Secret values are read from stdin or a hidden prompt, never required on the command line; `secret get --reveal` is the only way to print one | Must |
| CLI-09 | Network failures and `429/502/503/504` are retried with backoff (max 3 attempts, honouring `Retry-After`); creates are safe to retry (idempotency key) | Must |
| CLI-10 | Errors print the server's message and request ID, never a stack trace | Must |
| CLI-11 | `enclave server` starts the control plane with flags for listen address, data directory and engine driver; it serves the console on the same port | Must |
| CLI-12 | `enclave version` prints client version; `enclave machine` prints server version and engine status | Should |
| CLI-13 | `enclave domain create api-site --hostname api.example.com --app api [--port 8080] [--tls auto|internal|off] --wait`, plus `get`, `ls`, `delete`; `enclave domain ca` prints the root certificate; `enclave machine set --public-address <ip|host> --acme-email <email>` (empty value clears) (`L2-005` DOM-*) | Must — v0.3, shipped |
| CLI-14 | `enclave app create|update` gains `--health http:/healthz|tcp|cmd:<command>|none`, `--health-interval`, `--health-retries`, `--health-start-period` and `--strategy rolling|recreate`; `--wait` prints health-check attempts and fails with the probe result (DEP-*) | Must — v0.3, shipped |
| CLI-15 | `enclave job create cleanup --app api --schedule "@hourly" [--time-zone Z] [--run-timeout 1h] [--overlap allow] -- ./bin/cleanup --older-than 7d`, plus `update` (a command after `--` replaces it), `run [--wait]` (prints the output, exits 1 unless the run succeeded), `runs`, `logs <job> [--run ID]`. The global `--timeout` stays "how long --wait waits", so the run limit is `--run-timeout` (JOB-*) | Must — v0.3, shipped |
| CLI-16 | `enclave backup create orders-nightly --source database/orders --bucket backups [--s3-endpoint … --s3-bucket … --s3-credentials <secret>] [--schedule …] [--keep 7]`, plus `update` (everything but the source), `run [--wait]`, `runs`, `restore <backup> --run ID --new <name>` or `--replace [--yes]` (asks first; without a terminal `--yes` is required), with `--wait` on each. `--run-timeout` sets a run's limit, as for jobs. Deleting a backup says its stored backups are kept (BAK-*) | Must — v0.3, shipped |
| CLI-17 | `enclave app create api --git https://github.com/acme/api --ref main [--dockerfile path] [--git-credentials <secret>]`; `enclave logs app api --build` (BLD-*). Also `--context` and `--build-arg K=V` (repeatable, not for secrets). Exactly one of `--image` / `--git` (else exit 2). `app update` with a Git flag merges it into the current source and drops `image`, while `--image` drops the source. `app redeploy` builds the ref's latest commit | Should — v0.3, shipped |
| CLI-18 | `enclave network create backend --cidr 10.40.0.0/16 [--subnet private=10.40.1.0/24]… [--no-internet]`, plus `get`, `ls`, `delete`; `--network` and `--subnet` on `app`, `db`, `cache` and `job` create and update (an empty value moves back to the default network; `db update` exists for this) (`L2-006` NET-*) | Must — v0.4, shipped |
| CLI-19 | `enclave firewall create db-access --target database/orders --allow "tcp 5432 from app/api"… [--egress deny --allow-out "tcp 443 to any"…]`, plus `update` (each given list replaces the stored one), `get`, `ls`, `delete`. A rule is `<protocol> [ports] from|to <source>[,<source>…]`, ports `80` or `8000-8100` comma-separated (FW-*) | Must — v0.4, shipped |
| CLI-20 | `enclave app create|update --replicas N`; `enclave lb create api-lb --port 80 [--protocol http|tcp] --target api[:8080][@weight]… [--algorithm round_robin|least_conn|source] [--health tcp|http:/healthz] [--publish]`, plus `update`, `get`, `ls`, `delete` (LB-*) | Must — v0.4, shipped |
| CLI-21 | `enclave zone create example --provider cloudflare|route53 --zone example.com --credentials <secret>`; `enclave record create api --zone example --name api --type A (--machine | --value V…) [--ttl 300]`, plus `update`, `get`, `ls`, `delete` for both (DNS-*) | Must — v0.4, shipped |
| CLI-22 | `enclave domain create|update … --zone <zone>`; with a zone, `--hostname '*.example.com'` is accepted (CRT-*) | Must — v0.4, shipped |
| CLI-23 | `ls`, `get`, `events`, `delete` and `--wait` cover every v0.4 kind | Must — v0.4, shipped |
| CLI-24 | `enclave vm create worker --image ubuntu-24.04|https://…|snapshot/<name> [--sha256 H] --ssh-key ~/.ssh/id_ed25519.pub… | --ssh-key-secret <secret> [--cpus 2] [--memory 4096] [--disk 20] [--user-data ./init.yaml] [--network N --subnet S] [--disk-volume data]… [--publish 80]… [--stopped]`, plus `update`, `get`, `ls`, `delete` (`L2-007` VM-*, IMG-*, VMN-*) | Must — v0.5, shipped |
| CLI-25 | `enclave vm start|stop|restart <name>`; `enclave vm ssh <name> [-- command…]` runs the system `ssh` against the VM's SSH address and user; `enclave vm console <name> [--follow]` prints the boot console (VM-05, VM-06, VM-09) | Must — v0.5, shipped |
| CLI-26 | `enclave volume create data --type disk|filesystem [--size 50] [--snapshot <name>]`, plus `update` (size), `get`, `ls`, `delete`. `--volume <name>:<path>[:ro]` on `app` and `job` create and update; each given list replaces the stored one (VOL-*) | Must — v0.5, shipped |
| CLI-27 | `enclave snapshot create before-upgrade --from volume/data|vm/worker`, plus `get`, `ls`, `delete` (SNP-*) | Must — v0.5, shipped |
| CLI-28 | `ls`, `get`, `events`, `delete` and `--wait` cover every v0.5 kind; `enclave machine` shows VM availability and cached images (VM-11, IMG-05) | Must — v0.5, shipped |
| CLI-29 | `enclave db create analytics --engine postgres|mysql|mongodb|clickhouse [--version V]`; `db get` prints the engine's connection string without the password (`L2-008` DBE-*) | Must — v0.6, shipped |
| CLI-30 | `enclave queue create emails [--visibility-timeout 30s] [--retention 96h] [--max-message-size 256KiB] [--max-receives 5 --dead-letter-queue emails-failed]`, plus `update`, `get`, `ls`, `delete`; `queue send <queue> [BODY|-] [--attr K=V]… [--dedup-id ID]`, `queue receive <queue> [--max 10] [--wait 20s] [--delete]` (prints one JSON message per line with its receipt), `queue delete-message <queue> RECEIPT…`, `queue peek <queue>`, `queue purge <queue> [--yes]` (QUE-*) | Must — v0.6, shipped |
| CLI-31 | `enclave topic create orders [--retention 24h] [--queue emails]… [--webhook URL[,secret=NAME]]…`, plus `update` (each given list replaces the stored one), `get`, `ls`, `delete`; `topic publish <topic> [BODY|-] [--attr K=V]…` (TOP-*) | Must — v0.6, shipped |
| CLI-32 | `enclave search create catalogue [--version 3] [--memory 2048] [--network N --subnet S]`, plus `update`, `get`, `ls`, `delete` (SRC-*) | Must — v0.6, shipped |
| CLI-33 | `enclave transcoder create videos --input uploads/videos/ --output uploads/media/ --preset mp4-720p --preset hls-720p --preset thumbnail [--no-watch] [--run-timeout 1h] [--concurrency 2] [--memory 2048]` (bucket, then folder), plus `update`, `get`, `ls`, `delete`; `transcoder run <transcoder> KEY [--wait]`, `transcoder runs <transcoder>` (TRN-*) | Must — v0.6, shipped |
| CLI-34 | `enclave cdn create assets --hostname cdn.example.com --origin app/web[:8080]|bucket/uploads [--default-ttl 1h] [--max-size 1024] [--tls auto|internal|off] [--zone Z]`, plus `update`, `get`, `ls`, `delete`; `cdn purge <cdn> [--yes]` (CDN-*) | Must — v0.6, shipped |
| CLI-35 | `enclave email create shop-mail [--relay-host smtp.example.com --relay-port 587 --relay-tls starttls --relay-credentials <secret>] [--domain example.com] [--keep 500]`, plus `update` (`--capture` returns to capture mode), `get`, `ls`, `delete`; `email send <email> --from A --to B… --subject S [--text T|--text-file F] [--html-file F]`, `email messages <email>`, `email message <email> ID` (EML-*) | Must — v0.6, shipped |
| CLI-36 | `ls`, `get`, `events`, `delete` and `--wait` cover every v0.6 kind; `enclave machine` shows the broker's state | Must — v0.6, shipped |
| CLI-37 | A global `--project <name>` (or `ENCLAVE_PROJECT`, or `enclave project use <name>` stored in the config file) picks the project; the default is `default`. `ls` shows a PROJECT column with `--all-projects`. `enclave project create|ls|get|delete <name> [--description D]` (`L2-009` PRJ-*) | Must — v0.7 |
| CLI-38 | `enclave user invite <email> [--role admin|editor|viewer] [--project-role shop=editor]…` prints the invite link; `user ls`, `user get`, `user update <email> [--role R|--no-role] [--project-role P=R|P=none]…`, `user remove <email> [--yes]`, `user reset <email>` (a reset link); `enclave password` changes your own (IAM-*) | Must — v0.7 |
| CLI-39 | `enclave token create <name> [--role viewer|editor|admin] [--project P]… [--expires-in 30d]`, `token ls [--all-users]`, `token revoke <id>`; `enclave login` creates a token with the person's strongest role on all projects unless `--role` / `--project` say otherwise (TOK-*). *As built:* strongest role, not editor, so an admin's CLI can still change machine settings | Must — v0.7 |
| CLI-40 | `enclave metrics <kind>/<name> [--metric cpu]… [--since 1h|--from T --to T]` prints a table of points (or `--output json`), and `enclave metrics machine`; `enclave dashboard create <name> --widget "title=API rps,metric=requests,resources=app/api"…`, plus `update`, `get`, `ls`, `delete` (MET-*, DSH-*) | Must — v0.7 |
| CLI-41 | `enclave alert create api-crashing --crash-loop app/api [--restarts 3 --window 10m] --channel ops-mail…`, or `--metric cpu --resource app/api… --above 0.9 [--for 5m]`, `--failed kind/name…`, `--run-failed kind/name…`; `--repeat 1h`, `--disabled`; plus `update`, `get`, `ls`, `delete`, `alert incidents <alert>`, `alert test <alert>` (ALR-*) | Must — v0.7 |
| CLI-42 | `enclave channel create ops-mail --email shop-mail --from alerts@shop.example --to oncall@shop.example…`, `--webhook URL [--secret S]`, `--slack <secret>`, `--discord <secret>`, `--telegram <secret> --chat-id ID`; plus `update`, `get`, `ls`, `delete`, `channel deliveries <channel>`, `channel test <channel>` (NCH-*) | Must — v0.7 |
| CLI-43 | `enclave logexport create to-grafana [--source app/api]… --otlp https://… [--headers-secret S]` (or `--bucket logs[/folder]`, `--s3-endpoint … --s3-bucket … --s3-credentials S`, `--http URL`, `--loki URL [--label k=v]…`), `[--filter REGEX] [--flush 10s]`; plus `update`, `get`, `ls`, `delete` (LOG-*) | Must — v0.7 |
| CLI-44 | `enclave secretstore create vault-main --provider vault --address https://vault:8200 --token-secret vault-token [--mount secret] [--namespace N]` (and the 1Password, AWS, Doppler and Infisical flags) `[--refresh 5m]`, plus `update`, `get`, `ls`, `delete`, `secretstore test <store>`; `enclave secret create stripe-key --from-store op-main --key "Stripe" [--field credential]`, `secret refresh <secret>` (EXT-*) | Must — v0.7 |
| CLI-45 | `ls`, `get`, `events`, `delete` and `--wait` cover every v0.7 kind; `enclave audit [--limit N]` (machine admins) lists audit entries (IAM-09) | Must — v0.7 |

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler Labs | The v0.2 CLI: shape, login and tokens, every kind, waits, JSON output, exit codes, retries, `enclave server` (CLI-01 – CLI-12). Row reconstructed from the CHANGELOG (v0.2 lists L2-002) |
| v2 | 2026-10-20 | Soupler Labs | v0.3 requirements CLI-13 – CLI-17: domain, health/strategy flags, job, backup, Git source (L2-005) |
| v3 | 2026-10-21 | Soupler Labs | CLI-15 as shipped: --run-timeout, run --wait |
| v4 | 2026-10-21 | Soupler Labs | CLI-16 as shipped |
| v5 | 2026-10-21 | Soupler Labs | CLI-17 as shipped |
| v6 | 2026-10-21 | Soupler Labs | CLI-13 and CLI-14 marked shipped (phases 1–2) |
| v7 | 2026-10-21 | Soupler Labs | v0.4 requirements CLI-18 – CLI-23: network, placement flags, firewall, replicas and lb, zone and record, DNS-01 domains (L2-006). Restored the "Version history" heading and table header, lost in an earlier edit together with the v1 row(s), which are not reconstructed (see CHANGELOG) |
| v8 | 2026-10-21 | Soupler Labs | CLI-18 – CLI-23 shipped; `db update` moves a database |
| v9 | 2026-10-21 | Soupler Labs | Reconstructed the lost v1 history row |
| v10 | 2026-10-21 | Soupler Labs | v0.5 requirements CLI-24 – CLI-28: vm, vm power/ssh/console, volume and mounts, snapshot (L2-007) |
| v11 | 2026-10-21 | Soupler Labs | CLI-24 – CLI-28 shipped; --ssh-key takes a key or a .pub path; vm ssh files host keys per VM |
| v12 | 2026-10-21 | Soupler Labs | v0.6 requirements CLI-29 – CLI-36: database engines, queue, topic, search, transcoder, cdn, email (L2-008) |
| v13 | 2026-10-22 | Soupler Labs | CLI-29 – CLI-36 shipped; `get`, `list` and `delete` name the v0.6 kinds too |
| v14 | 2026-10-22 | Soupler Labs | v0.7 requirements CLI-37 – CLI-45: --project and project commands, users, scoped tokens, metrics and dashboards, alerts, channels, log exports, secret stores, audit (L2-009) |
| v15 | 2026-10-22 | Soupler Labs | CLI-39 as built: `login` and a token without `--role` get the strongest role |
