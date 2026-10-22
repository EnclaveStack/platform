# L7-000 · Forensics & Traceability Artifact Registry

---
| Field | Value |
|---|---|
| Document ID | L7-000-artifact-registry-v1 |
| Layer | L7 — Forensics & Traceability |
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

Registry of every artifact in layer **L7 — Forensics & Traceability** (master registry: audit → plan → execution → evidence → PR). This layer owns: *The permanent chain of custody from finding to merged fix*. It does not own: Anything it can link to instead.

**Every `.md` artifact under this folder — including numbered subfolders — must have a row below, in the same commit that creates it.** The Statutory Integrity gate (`bin/statutory-integrity.sh`) blocks the PR otherwise. When an artifact's own `Version` or `Last Updated` header changes, update its row here in the same commit.

The chain-of-custody table lives in `L7-001-master-registry-v1`; every remediation session adds one row there.

## Registered Artifacts

| Document ID | Title | Status | Version | Owner | Last Updated |
|---|---|---|---|---|---|
| L7-001-master-registry-v1 | Forensics & Traceability Master Registry | Draft | v1 | Soupler Labs | 2026-10-20 |

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Registry created by soupler-governance-template v1.2.1 |
