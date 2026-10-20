# L4-001 · Local Development Setup & Smoke Runbook

---
| Field | Value |
|---|---|
| Document ID | L4-001-local-dev-setup-v1 |
| Layer | L4 — Infrastructure & Deployment |
| Status | Draft |
| Version | v1 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-20 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs. All rights reserved. |
| License | MIT |
---

## Purpose

How to run homecloud-platform locally and prove it works. Update whenever prerequisites, bootstrap, smoke commands or troubleshooting change (L0-006 §3.4).

## 1 · Prerequisites
- Node 22.15.0, pnpm 9.15.9 (pin via Volta or `engines`)
- ⚠️ GAP: Docker services, env files, secrets source

## 2 · Bootstrap
```bash
pnpm install --frozen-lockfile
bash bin/install-hooks.sh
```

## 3 · Smoke
```bash
pnpm lint
pnpm type-check
pnpm test
bash bin/statutory-integrity.sh
```

## 4 · Known local limitations
⚠️ GAP: list what cannot be verified locally (e.g. CDN headers, push notifications, device-only behaviour) and where it *is* verified.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Homecloud | Initial skeleton from soupler-governance-template v1.2.1 |
