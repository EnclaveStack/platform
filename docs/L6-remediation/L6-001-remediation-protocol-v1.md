# L6-001 · Remediation Protocol (remediation track)

---
| Field | Value |
|---|---|
| Document ID | L6-001-remediation-protocol-v1 |
| Layer | L6 — Remediation |
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

The lifecycle for security findings, defect classes, architecture corrections and compliance gaps.

## 1 · Steps (all six close a session)

| # | Step | Artifact | Location |
|---|---|---|---|
| 1 | Audit | what is broken, risk level, standard reference, affected files, root cause | `docs/L6-remediation/R-NNN-slug/R-NNN-slug-audit-v1.md` |
| 2 | Plan | ordered tasks: file, change, verification criteria, effort | `…/R-NNN-slug-plan-v1.md` |
| 3 | Implement | the code | PR(s) citing `R-NNN` |
| 4 | TIC | what was actually done, task by task; decisions made along the way | `docs/L6.1-remediation-execution/L6.1-NNN-slug/TIC-NNN-slug-tactical-plan-v1.md` |
| 5 | WE | before/after snippets and negative-test results proving each fix | `…/WE-NNN-slug-walkthrough-v1.md` |
| 6 | L7 row | final status and PR reference | `docs/L7-forensics/L7-001-master-registry-v1.md` |

Skipping WE is acceptable only when the PR is the sole evidence: mark the L7 row `Closed — git evidence` and note the gap.

## 2 · Rules
- Verify a finding is **true** before opening a session; then fix it in the same pass — no "watch later" bucket.
- Number sessions sequentially, never reuse.
- Every session's docs are registered in L6 / L6.1 registries in the same commit.
- Record **corrections** (a wrong earlier claim) in Version history with the cause.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Initial skeleton from soupler-governance-template v1.2.1 |
