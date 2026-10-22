# L2-003 · OpenTofu Provider PRD

---
| Field | Value |
|---|---|
| Document ID | L2-003-opentofu-provider-prd-v1 |
| Layer | L2 — Product Requirements |
| Status | Draft |
| Version | v14 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-22 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs |
| License | Apache-2.0 |
---

## Purpose

What the `enclavestack/enclave` provider must let a team declare in OpenTofu (and Terraform, which uses the same plugin protocol). Parity with `L2-001` and `L2-002` over `L2-004`. Engineering: `L3-001`, `L3-003` ADR-009.

## 1 · Shape

```hcl
terraform {
  required_providers {
    enclave = { source = "enclavestack/enclave" }
  }
}

provider "enclave" {}   # server and token from ENCLAVE_SERVER / ENCLAVE_TOKEN

resource "enclave_database" "orders" {
  name    = "orders"
  version = "16"
}

resource "enclave_secret" "stripe" {
  name  = "stripe-key"
  value = var.stripe_key
}

resource "enclave_app" "api" {
  name       = "api"
  image      = "ghcr.io/acme/api:1.4"
  port       = 8080
  publish    = true
  env        = { DATABASE_HOST = enclave_database.orders.internal_host }
  secret_env = {
    STRIPE_KEY        = enclave_secret.stripe.name
    DATABASE_PASSWORD = enclave_database.orders.password_secret
  }
}

output "api_url" { value = "http://${enclave_app.api.host}:${enclave_app.api.host_port}" }
```

## 2 · Requirements

| ID | Requirement | Priority |
|---|---|---|
| TF-01 | Resources `enclave_database`, `enclave_cache`, `enclave_bucket`, `enclave_app`, `enclave_secret`; data sources `enclave_machine` and `enclave_secret_value` | Must |
| TF-02 | `apply` waits until each resource is ready; a failure fails the apply with the resource's message. Create / update / delete timeouts are configurable (default 10 min) | Must |
| TF-03 | `plan` shows a replacement (not an in-place update) for changes the control plane cannot apply in place (`name`, database `version`, bucket `name`) | Must |
| TF-04 | Drift: a resource deleted outside OpenTofu is removed from state on refresh and recreated on the next apply | Must |
| TF-05 | `tofu import enclave_<kind>.<label> <name>` adopts a resource created in the console or CLI | Must |
| TF-06 | Secret values are `sensitive`; the provider never logs them; reading a secret value requires the explicit `enclave_secret_value` data source | Must |
| TF-07 | Provider configuration: `server`, `token` (sensitive); both fall back to `ENCLAVE_SERVER` / `ENCLAVE_TOKEN` | Must |
| TF-08 | Transient API errors are retried like the CLI (`L2-002` CLI-09) | Must |
| TF-09 | Works with OpenTofu ≥ 1.6 and Terraform ≥ 1.5 (plugin protocol v6) | Must |
| TF-10 | Published to the OpenTofu and Terraform registries as `enclavestack/enclave` | Should — at public launch |
| TF-11 | Resource `enclave_domain` (`name`, `hostname` — replacement on change, `app`, `port`, `tls`, `redirect_http`; computed `url`, `certificate_state`, `certificate_expires_at`); data source `enclave_ca` with the root certificate PEM; resource `enclave_machine_settings` (`public_address`, `acme_email`; a singleton, destroy clears them) (`L2-005` DOM-*) | Must — v0.3, shipped |
| TF-12 | `enclave_app` gains a `health_check` block and `strategy`; `apply` waits for the new revision to be serving and fails with the probe result if it is discarded (DEP-*) | Must — v0.3, shipped |
| TF-13 | Resource `enclave_job` (`schedule`, `time_zone`, `app` or `image`, `command`, `timeout`, `overlap`, `env`, `secret_env`); run history is not part of state (JOB-*) | Must — v0.3, shipped |
| TF-14 | Resource `enclave_backup` (`source` as `{ database = … }` or `{ cache = … }`, `bucket` or an `s3` block, `schedule`, `keep`, `keep_days`); restore is an operational action and not exposed as a resource (BAK-*). Also `prefix`, `time_zone`, `timeout_seconds`; computed `next_run_at`, `last_run_status`, `last_success_at`; changing `source` replaces the backup | Must — v0.3, shipped |
| TF-15 | `enclave_app` accepts a `source` block (`git`, `ref`, `dockerfile`, `context`, `credentials_secret`, `build_args`) as an alternative to `image` (exactly one, checked at plan time); computed `built_commit`. A changed source or build argument applies in place as a new revision; a failing build fails `apply` with the step (BLD-*) | Should — v0.3, shipped |
| TF-16 | Resource `enclave_network` (`cidr`, `subnet = [{ name, cidr }]`, `internet`; any change replaces it, which fails while resources use it). `network` and `subnet` on `enclave_app`, `enclave_database`, `enclave_cache` and `enclave_job`, applied in place; computed `private_ip`, `private_dns` (`L2-006` NET-*) | Must — v0.4, shipped |
| TF-17 | Resource `enclave_firewall` (`targets` as `"kind/name"` strings, `ingress = [{ from, protocol, ports }]`, `egress_default`, `egress = [{ to, protocol, ports }]`), all in place (FW-*) | Must — v0.4, shipped |
| TF-18 | `replicas` on `enclave_app`. Resource `enclave_load_balancer` (`port`, `protocol`, `publish`, `algorithm`, `target = [{ app, port, weight }]`, `health_check = { … }`; computed `internal_host`, `private_dns`, `host_port`, `healthy_targets`) (LB-*) | Must — v0.4, shipped |
| TF-19 | Resources `enclave_zone` (`dns_provider`, `zone`, `credentials_secret`; computed `zone_id`, `name_servers`; changing `dns_provider` or `zone` replaces it) and `enclave_record` (`zone`, `record_name`, `type`, `values` or `machine`, `ttl`; computed `fqdn`; changing `zone`, `record_name` or `type` replaces it). As built: nested values are list and object attributes (`subnet = [...]`) rather than blocks; `provider` is reserved by OpenTofu, hence `dns_provider`, and `name` is the resource's name, hence `record_name` (DNS-*) | Must — v0.4, shipped |
| TF-20 | `zone` on `enclave_domain`; wildcard hostnames are accepted with a zone (CRT-*) | Must — v0.4, shipped |
| TF-21 | Import by name (TF-05) and drift removal (TF-04) for every v0.4 resource | Must — v0.4, shipped |
| TF-22 | Resource `enclave_vm` (`image`, `image_sha256`, `ssh_keys` or `ssh_key_secret`, `cpus`, `memory_mib`, `disk_gib`, `user_data`, `network`, `subnet`, `disks` as volume names, `publish`, `power`; computed `private_ip`, `private_dns`, `ssh_address`, `ssh_user`). Changing `image`, `image_sha256`, `ssh_keys`, `ssh_key_secret` or `user_data` replaces it; the rest apply in place (`L2-007` VM-*, IMG-*, VMN-*) | Must — v0.5, shipped |
| TF-23 | Resource `enclave_volume` (`type`, `size_gib`, `snapshot`; computed `attached_to`, `used_bytes`). Changing `type` or `snapshot` replaces it; a smaller `size_gib` is an error at plan time (VOL-*) | Must — v0.5, shipped |
| TF-24 | `volumes = [{ volume, path, read_only }]` on `enclave_app` and `enclave_job`, applied in place (VOL-04, VOL-05) | Must — v0.5, shipped |
| TF-25 | Resource `enclave_snapshot` (`source` as `"kind/name"`; computed `type`, `size_bytes`, `taken_at`); any change replaces it (SNP-*) | Must — v0.5, shipped |
| TF-26 | Import by name (TF-05) and drift removal (TF-04) for every v0.5 resource | Must — v0.5, shipped |
| TF-27 | `engine` on `enclave_database` (default `postgres`; a change replaces it); computed `engine_port_native` for ClickHouse (`L2-008` DBE-*) | Must — v0.6, shipped |
| TF-28 | Resource `enclave_queue` (`visibility_timeout_seconds`, `retention_hours`, `max_message_bytes`, `max_receives`, `dead_letter_queue`; computed `broker_url`, `internal_broker_url`, `subject`, `username`, `password_secret`), all in place (QUE-*) | Must — v0.6, shipped |
| TF-29 | Resource `enclave_topic` (`retention_hours`, `subscriptions = [{ queue } | { url, secret }]`; computed `broker_url`, `internal_broker_url`, `subject`, `username`, `password_secret`), all in place (TOP-*) | Must — v0.6, shipped |
| TF-30 | Resource `enclave_search` (`version` — replaces on change, `memory_mib`, `network`, `subnet`; computed `url`, `internal_url`, `username`, `password_secret`) (SRC-*) | Must — v0.6, shipped |
| TF-31 | Resource `enclave_transcoder` (`input = { bucket, prefix }`, `output = { bucket, prefix }`, `presets`, `watch`, `timeout_seconds`, `concurrency`, `memory_mib`), all in place; runs are not part of state (TRN-*) | Must — v0.6, shipped |
| TF-32 | Resources `enclave_cdn` (`hostname` — replaces on change, `origin`, `origin_port`, `default_ttl_seconds`, `max_size_mib`, `tls`, `zone`; computed `url`, `certificate_state`) and `enclave_email` (`mode`, `relay = { host, port, tls, credentials_secret }`, `domain`, `keep_messages`, `network`, `subnet`; computed `smtp_host`, `smtp_port`, `internal_smtp_host`, `username`, `password_secret`) (CDN-*, EML-*) | Must — v0.6, shipped |
| TF-33 | Import by name (TF-05) and drift removal (TF-04) for every v0.6 resource | Must — v0.6, shipped |
| TF-34 | Provider setting `project` (or `ENCLAVE_PROJECT`, default `default`); every resource and data source gains an optional `project` that overrides it, and a change replaces the resource. Import IDs are `<name>` in `default` and `<project>/<name>` elsewhere (`L2-009` PRJ-*) | Must — v0.7 |
| TF-35 | Resource `enclave_project` (`name`, `description`); destroy fails while the project has resources (PRJ-01, PRJ-07) | Must — v0.7 |
| TF-36 | Resource `enclave_user` (`email` — replaces on change, `role`, `project_roles` as a map of project → role; computed sensitive `invite_url`, present until the invite is used) (IAM-*) | Must — v0.7 |
| TF-37 | Resource `enclave_dashboard` (`widgets = [{ title, metric, resources }]`); data source `enclave_metrics` (`resource`, `metric`, `since`) for the latest value and its points (MET-*, DSH-*) | Must — v0.7 |
| TF-38 | Resources `enclave_alert` (`condition = { type, metric, resources, operator, threshold, for_minutes, restarts, window_minutes }`, `channels`, `repeat_minutes`, `enabled`; computed `state`) and `enclave_channel` (`type`, `email = { service, from, to }`, `webhook = { url, secret }`, `secret`, `chat_id`), all in place except `type` (ALR-*, NCH-*) | Must — v0.7 |
| TF-39 | Resource `enclave_log_export` (`sources`, `filter`, `flush_seconds`, and exactly one of `bucket = { bucket, prefix }`, `s3 = { … }`, `http = { url, headers_secret }`, `otlp = { endpoint, headers_secret }`, `loki = { url, headers_secret, labels }`; computed `lines_sent`, `last_sent_at`) (LOG-*) | Must — v0.7 |
| TF-40 | Resource `enclave_secret_store` (`secret_provider` and its block: `vault`, `onepassword`, `aws`, `doppler` or `infisical`; `refresh_seconds`). *As built:* the provider's settings are one `settings` map with the API's field names (`address`, `tokenSecret`, …) instead of five blocks; defaults the server fills in stay out of state. `enclave_secret` gains `source = { store, key, field }` as the alternative to `value` (exactly one, checked at plan time); computed `synced_at` (EXT-*) | Must — v0.7 |
| TF-41 | Import by name (TF-05) and drift removal (TF-04) for every v0.7 resource | Must — v0.7 |
| TF-42 | Resource `enclave_machine` (`name` — replaces on change, `labels`, `schedulable`; computed `state`, `address`, and sensitive `join_command` while the machine waits for its agent). Destroy is refused while anything runs there. Data source `enclave_machines` lists machines with their state and free memory (MCH-*) | Must — v0.8 |
| TF-43 | Every placed resource gains `machine` (optional and computed: what EnclaveStack chose is read back without a diff) and `machine_labels`. Changing `machine` is an in-place update that moves the resource; apply waits for the move (PLC-*, MOVE-*) | Must — v0.8 |
| TF-44 | Resource `enclave_stack` (`template` — a catalogue entry — or `template_file`'s contents in `template_body`, `template_version`, `parameters` as a map and `secret_parameters` as a sensitive map; computed `members` (kind, name, ID, phase) and `outputs`), updated in place (CAT-04 – CAT-07) | Must — v0.8 |
| TF-45 | Resource `enclave_catalogue_template` (`body`, the template's YAML or JSON) for the machine's own templates; data source `enclave_catalogue` lists entries and their parameters (CAT-01 – CAT-03) | Must — v0.8 |
| TF-46 | A stack's members can be read with the existing data sources; managing a member as its own resource is refused with the stack's name (CAT-06) | Must — v0.8 |
| TF-47 | Import by name (TF-05) and drift removal (TF-04) for `enclave_machine`, `enclave_stack` and `enclave_catalogue_template` | Must — v0.8 |

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler Labs | The v0.2 provider: five resources, two data sources, waits, replacements, drift, import, sensitive values, configuration, retries, versions, registry (TF-01 – TF-10). Row reconstructed from the CHANGELOG (v0.2 lists L2-003) |
| v2 | 2026-10-20 | Soupler Labs | v0.3 requirements TF-11 – TF-15: domain, health_check and strategy, job, backup, Git source (L2-005) |
| v3 | 2026-10-21 | Soupler Labs | TF-11 – TF-14 marked shipped; TF-14 as built |
| v4 | 2026-10-21 | Soupler Labs | TF-15 shipped |
| v5 | 2026-10-21 | Soupler Labs | v0.4 requirements TF-16 – TF-21: network, firewall, replicas and load balancer, zone and record, DNS-01 domains (L2-006). Restored the "Version history" heading and table header, lost in an earlier edit together with the v1 row(s), which are not reconstructed (see CHANGELOG) |
| v6 | 2026-10-21 | Soupler Labs | TF-16 – TF-21 shipped as built: list attributes instead of blocks; dns_provider and record_name (reserved and taken names) |
| v7 | 2026-10-21 | Soupler Labs | Reconstructed the lost v1 history row |
| v8 | 2026-10-21 | Soupler Labs | v0.5 requirements TF-22 – TF-26: enclave_vm, enclave_volume, volume mounts, enclave_snapshot (L2-007) |
| v9 | 2026-10-21 | Soupler Labs | TF-22 – TF-26 shipped; attached_to is refreshed by the server periodically |
| v10 | 2026-10-21 | Soupler Labs | v0.6 requirements TF-27 – TF-33: database engine, queue, topic, search, transcoder, cdn, email (L2-008) |
| v11 | 2026-10-22 | Soupler Labs | TF-27 – TF-33 shipped |
| v12 | 2026-10-22 | Soupler Labs | v0.7 requirements TF-34 – TF-41: project setting and attribute, project, user, dashboard and metrics, alert and channel, log export, secret store and external secrets (L2-009) |
| v13 | 2026-10-22 | Soupler Labs | TF-40 as built: `enclave_secret_store.settings` map; TF-34 – TF-41 shipped and covered by `TestAccOperateStack` |
| v14 | 2026-10-22 | Soupler Labs | v0.8 requirements TF-42 – TF-47: machine resource and data source, machine on placed resources, stack, catalogue template and data source, import (L2-010) |
