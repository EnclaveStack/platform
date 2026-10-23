# L0-005 · GitHub Governance Standard

---
| Field | Value |
|---|---|
| Document ID | L0-005-github-governance-standard-v1 |
| Layer | L0 — Foundation Standards |
| Status | Final |
| Version | v2 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-20 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

The mandatory branch model, protection, PR and release rules that keep the repositories honest. Technical enforcement lives in `.github/governance/rulesets/` and the workflows named below.

## 1 · Branch model

| Branch | Purpose | Access |
|---|---|---|
| `main` | Production. Receives **only** `develop`. | PR only, merge commits |
| `develop` | Integration. All work lands here first. | PR only, squash/rebase, linear |
| `<type>/<slug>` | One logical change (`feat fix tech infra docs chore`) | author |

Naming and the three-tier ID rule: L0-002 §4. Hotfixes branch from `develop`.

## 2 · Pull requests

- Title is a Conventional Commit (`feat(auth): …`); a `develop` → `main` promotion starts with `release:`.
- Body uses `.github/pull_request_template.md`. `## Docs update` must contain `- Status: UPDATED|EXEMPT` and a real `- Notes:` line — CI (`PR Metadata`) parses it literally. Blank is not an answer. **Exception:** a PR opened by `dependabot[bot]` is exempt from the `## Docs update` rule — a version bump changes no owning document, and Dependabot writes its own body (release notes) that cannot carry the block. Its title and every other body rule still apply. A Dependabot PR that does need a doc change (a new language or runtime version, a stack change) is handled as a normal PR: a person adds the docs and the block.
- One logical change per PR. A PR touching `.github/workflows/` is also reviewed for pipeline impact.
- All required checks green: `Statutory Integrity` `PR Metadata` `Quality Checks` `Tests` `Secret Scan` .

## 3 · Enforcement map

| Rule | Mechanism |
|---|---|
| PR-only, force-push blocked, merge methods | rulesets |
| Required checks | rulesets (`required_status_checks`) — **names must equal the CI job names** |
| `main` only from `develop` | `branch-policy.yml` |
| Doc registration and freshness | `bin/statutory-integrity.sh` via `pr-governance.yml` |
| PR title and Docs-update block | `pr-governance.yml` → `PR Metadata` |
| Commit format and no AI attribution  | `.githooks/commit-msg` (install: `bash bin/install-hooks.sh`) |
| Secrets | `pre-commit` + `secret-scan.yml` (gitleaks) |

A guard that is only a convention will be broken; every rule above has a mechanism. When adding a rule, add its mechanism in the same change or record in Version history that it is convention-only.

## 4 · Releases

SemVer (L0-002 §5). `develop` is green → promotion PR → merge commit → tag `main` → `CHANGELOG.md` entry exists for the tag.

## 5 · Remediation governance

Security/compliance findings and defect classes follow the remediation track: **Audit → Plan → Execution (TIC + WE) → L7 row**, tracked by the docs and the PR(s) — not by labels nobody applies. Reference the audit ID inline in commits/PRs.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v2 | 2026-10-20 | EnclaveStack | §2 — Dependabot PRs exempt from the `## Docs update` body rule (title and other body rules still apply) |
| v1 | 2026-10-20 | EnclaveStack | Initial release from soupler-governance-template v1.2.1 |
