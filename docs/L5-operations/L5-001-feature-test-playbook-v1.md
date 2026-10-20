# L5-001 · Feature Test Playbook

---
| Field | Value |
|---|---|
| Document ID | L5-001-feature-test-playbook-v1 |
| Layer | L5 — Operations & GTM |
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

How every shipped feature — and the current integrated slice — is verified, by a human or CI. Update it whenever shipped functionality, setup or verification steps change.

## 1 · Section template (one per feature)

### Feature: TODO
- **Prerequisites**: services running, seed data, accounts
- **Steps**: numbered, executable
- **Expected results**: observable outcome per step (UI state, response body, DB row, log line)
- **Error paths**: empty results, wrong status, missing data, boundary values
- **Automated checks**: the test files/commands that cover it
- **Known local limitations**: what this playbook cannot prove, and where that is verified instead

## 2 · Features
⚠️ GAP: add a section per shipped feature.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Homecloud | Initial skeleton from soupler-governance-template v1.2.1 |
