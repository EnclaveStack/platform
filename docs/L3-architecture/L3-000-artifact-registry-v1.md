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
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

Registry of every artifact in layer **L3 — Architecture** (system design, ADRs, API contracts, data model, security architecture). This layer owns: *How the system is engineered: boundaries, API contracts, data model, security*. It does not own: Runbooks or GTM strategy.

**Every `.md` artifact under this folder — including numbered subfolders — must have a row below, in the same commit that creates it.** The Statutory Integrity gate (`bin/statutory-integrity.sh`) blocks the PR otherwise. When an artifact's own `Version` or `Last Updated` header changes, update its row here in the same commit.

## Registered Artifacts

| Document ID | Title | Status | Version | Owner | Last Updated |
|---|---|---|---|---|---|
| L3-001-system-architecture-v1 | System Architecture | Draft | v18 | Soupler | 2026-10-24 |
| L3-002-console-api-contract-v1 | Control-Plane API Contract | Draft | v26 | Soupler | 2026-10-24 |
| L3-003-architecture-decision-records-v1 | Architecture Decision Records | Draft | v18 | Soupler | 2026-10-24 |
| L3-004-decision-analysis-records-v1 | Decision Analysis and Resolution (DAR) Records | Draft | v9 | Soupler | 2026-10-24 |
| L3-005-data-model-v1 | Data Model | Draft | v16 | Soupler | 2026-10-24 |
| L3-006-security-architecture-v1 | Security Architecture and Threat Model | Draft | v15 | Soupler | 2026-10-24 |
| L3-007-reliability-observability-v1 | Reliability and Observability | Draft | v14 | Soupler | 2026-10-24 |

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Registry created by soupler-governance-template v1.2.1 |
