# L6-000 · Remediation Artifact Registry

---
| Field | Value |
|---|---|
| Document ID | L6-000-artifact-registry-v1 |
| Layer | L6 — Remediation |
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

Registry of every artifact in layer **L6 — Remediation** (audit + plan per remediation session). This layer owns: *Audits and corrective plans for drift, defects and compliance gaps*. It does not own: The fix itself (that is L6.1).

**Every `.md` artifact under this folder — including numbered subfolders — must have a row below, in the same commit that creates it.** The Statutory Integrity gate (`bin/statutory-integrity.sh`) blocks the PR otherwise. When an artifact's own `Version` or `Last Updated` header changes, update its row here in the same commit.

## Registered Artifacts

| Document ID | Title | Status | Version | Owner | Last Updated |
|---|---|---|---|---|---|
| L6-001-remediation-protocol-v1 | Remediation Protocol (remediation track) | Draft | v1 | Soupler | 2026-10-20 |

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Registry created by soupler-governance-template v1.2.1 |
