# L1-001 · Product Brief

---
| Field | Value |
|---|---|
| Document ID | L1-001-product-brief-v1 |
| Layer | L1 — Vision & Strategy |
| Status | Draft |
| Version | v3 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-22 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs. All rights reserved. |
| License | MIT |
---

## Purpose

Why EnclaveStack exists, who it is for, and how we will know it works. Every L2 requirement traces back to a line here.

## Name

The product is **EnclaveStack** (renamed from Homecloud on 2026-10-22, because the Homecloud name wasn't available). Its code lives under the `EnclaveStack` GitHub organization as `platform`, `marketing` (the website) and `assets`. People type `enclave` (the CLI), configure `ENCLAVE_*` settings, and write `enclave_*` OpenTofu resources from the `enclavestack/enclave` provider. Containers and networks it manages start with `es-`, API tokens with `es_pat_`, and it keeps its data in `~/.enclave`.

## 1 · Problem

Small teams pay a public cloud for a handful of services every application needs — compute, a database, object storage, a cache, HTTPS and logs — on hardware they could run themselves. The bill grows with usage, the APIs are proprietary, and the data lives in someone else's region.

## 2 · Product

EnclaveStack is an **open-source, self-hosted cloud — our own AWS on our own hardware**: one install turns a Mac or a Linux machine into a host for apps and the common services they need. Every service can be created, changed and deleted from **three equal surfaces**, exactly as on a public cloud:

| Surface | Public-cloud equivalent | Who uses it |
|---|---|---|
| Web console | AWS Management Console | Click-ops, discovery, day-to-day operation |
| `enclave` CLI | `aws` CLI | Scripts, CI pipelines, terminal-first developers |
| OpenTofu / Terraform provider | `hashicorp/aws` provider | Infrastructure as code: declare, plan, apply, destroy |

All three talk to one control-plane API; nothing is possible in one surface that is impossible in another. It is built from proven open-source engines (Docker, PostgreSQL, Redis, an S3-compatible store, Caddy, Grafana/Loki, FFmpeg); EnclaveStack's own code is the control plane that provisions, wires, backs up and observes them.

## 3 · Who it is for

| Segment | Need |
|---|---|
| Indie developers and early-stage startups | Host a real product without a cloud bill that scales with traffic |
| Teams with data-residency needs | Keep data on hardware they control |
| Developers' dev / staging environments | A production-shaped stack on a laptop |

## 4 · Positioning

- **API-first**: the HTTP API is the product; the console, CLI and provider are clients of it.
- **Open source, no paid tier, no feature gates** (MIT). There is no pricing model.
- Familiar to anyone who has used a cloud console: each EnclaveStack service names the cloud service it replaces.
- First proof workload: the Galiaraa platform (13 services, Postgres, Redis, object storage, video encoding) on one machine.

## 5 · Success measures

| Measure | Target |
|---|---|
| Time from install to first app live on a domain | ⚠️ GAP: set after the first usability test |
| Galiaraa's full stack running on one machine | Yes / no — the v1.0 milestone |
| Surface parity | Every v0.2 service kind is creatable, readable, updatable and deletable from console, CLI and OpenTofu — enforced by the e2e suite (`L5-002`) |
| Control-plane API latency | p95 < 100 ms reads, < 300 ms writes at 50 concurrent clients (`L5-003`) |
| Community adoption | ⚠️ GAP: GitHub stars, installs, contributors — set at public launch |

## 6 · Scope by release

| Release | Service kinds | Surfaces |
|---|---|---|
| **v0.2 (this build)** | Apps (any container image), Databases (PostgreSQL), Caches (Redis), Buckets (S3-compatible), Secrets | Console, CLI, OpenTofu provider |
| v0.3 | Domains + HTTPS (Caddy, tunnel), Backups (scheduled, restorable) | Same three |
| v0.4 | Log search + metrics dashboards, media jobs | Same three |

## 7 · Non-goals (v0.x)

- Multi-region or multi-tenant hosting for third parties
- Re-implementing storage, database or container engines
- A hosted/managed EnclaveStack offering

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler Labs | Initial brief: problem, product, segments, open-source positioning, success measures |
| v2 | 2026-10-20 | Soupler Labs | "Our own AWS": three equal surfaces (console, CLI, OpenTofu provider), API-first positioning, scope by release, parity and latency measures |
| v3 | 2026-10-22 | Soupler Labs | Renamed Homecloud → EnclaveStack (name unavailable): GitHub org `EnclaveStack` with `platform`, `marketing`, `assets`; CLI `enclave`, `ENCLAVE_*` settings, `enclave_*` OpenTofu resources, `es-` resource names |
