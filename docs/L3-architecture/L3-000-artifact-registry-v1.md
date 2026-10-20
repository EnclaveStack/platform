# L3-000 · Architecture Artifact Registry

---
| Field | Value |
|---|---|
| Document ID | L3-000-artifact-registry-v1 |
| Layer | L3 — Architecture |
| Status | Final |
| Version | v1 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-20 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs. All rights reserved. |
| License | MIT |
---

## Purpose

Registry of every artifact in layer **L3 — Architecture** (system design, ADRs, API contracts, data model, security architecture). This layer owns: *How the system is engineered: boundaries, API contracts, data model, security*. It does not own: Runbooks or GTM strategy.

**Every `.md` artifact under this folder — including numbered subfolders — must have a row below, in the same commit that creates it.** The Statutory Integrity gate (`bin/statutory-integrity.sh`) blocks the PR otherwise. When an artifact's own `Version` or `Last Updated` header changes, update its row here in the same commit.

## Registered Artifacts

| Document ID | Title | Status | Version | Owner | Last Updated |
|---|---|---|---|---|---|
| L3-001-system-architecture-v1 | System Architecture | Draft | v1 | Soupler Labs | 2026-10-20 |

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Homecloud | Registry created by soupler-governance-template v1.2.1 |
