# L5-000 · Operations & GTM Artifact Registry

---
| Field | Value |
|---|---|
| Document ID | L5-000-artifact-registry-v1 |
| Layer | L5 — Operations & GTM |
| Status | Final |
| Version | v1 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-20 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs |
| License | Apache-2.0 |
---

## Purpose

Registry of every artifact in layer **L5 — Operations & GTM** (test playbook, runbooks, incident response, launch, support, SLOs). This layer owns: *How the product is tested, operated, launched and supported*. It does not own: System architecture or product requirements.

**Every `.md` artifact under this folder — including numbered subfolders — must have a row below, in the same commit that creates it.** The Statutory Integrity gate (`bin/statutory-integrity.sh`) blocks the PR otherwise. When an artifact's own `Version` or `Last Updated` header changes, update its row here in the same commit.

## Registered Artifacts

| Document ID | Title | Status | Version | Owner | Last Updated |
|---|---|---|---|---|---|
| L5-001-feature-test-playbook-v1 | Feature Test Playbook | Draft | v20 | Soupler Labs | 2026-10-22 |
| L5-002-test-strategy-v1 | Test Strategy | Draft | v17 | Soupler Labs | 2026-10-22 |
| L5-003-performance-baseline-v1 | Performance Baseline | Draft | v2 | Soupler Labs | 2026-10-20 |

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Registry created by soupler-governance-template v1.2.1 |
