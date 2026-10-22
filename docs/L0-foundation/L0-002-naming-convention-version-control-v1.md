# L0-002 · Naming Convention, Versioning & Git Conventions

---
| Field | Value |
|---|---|
| Document ID | L0-002-naming-convention-version-control-v1 |
| Layer | L0 — Foundation Standards |
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

Defines how every artifact, branch, commit and release is named, so the repository is navigable by pattern and mechanically checkable.

## 1 · Artifact naming

```
[LL]-[NNN]-[slug]-v[N].md
```

| Part | Rule |
|---|---|
| LL | Layer code, uppercase: `L0 L1 L2 L2.5 L3 L4 L5 L6 L6.1 L7` |
| NNN | Three-digit sequence within the layer, from 001. **Never reused**, even if the artifact is deleted. `000` is reserved for the layer registry. |
| slug | Lowercase kebab-case, 2–5 words, unique within the layer. No underscores or camelCase. |
| vN | Starts at `v1` and **stays there** — it increments only for a breaking change that needs a new file (§2.2). It is not a revision counter; revisions bump the *header* `Version`. A file named `-v1.md` with header `Version: v40` is normal. |

Layer codes and owners:

| Code | Folder | Owns |
|---|---|---|
| L0 | `L0-foundation` | how artifacts are written and governed |
| L1 | `L1-strategy` | why the product exists; how success is measured |
| L2 | `L2-product` | what must be built |
| L2.5 | `L2.5-design-ux` | how it behaves and feels |
| L3 | `L3-architecture` | how it is engineered: boundaries, API contracts, data model, security, ADRs |
| L4 | `L4-infrastructure` | how it is provisioned and deployed |
| L5 | `L5-operations` | how it is tested, operated, launched, supported |
| L6 | `L6-remediation` | audits and corrective plans |
| L6.1 | `L6.1-remediation-execution` | what was done and the evidence |
| L7 | `L7-forensics` | permanent chain of custody |

Every layer has `LL-000-artifact-registry-v1.md`, created **before** any other artifact in that layer.

**L6 and L6.1 use session folders**: `L6-remediation/R-NNN-slug/` holding `R-NNN-slug-audit-v1.md` + `R-NNN-slug-plan-v1.md`; `L6.1-remediation-execution/L6.1-NNN-slug/` holding `TIC-NNN-slug-tactical-plan-v1.md` + `WE-NNN-slug-walkthrough-v1.md`. L7 follows the standard pattern.

## 2 · Versioning

### 2.1 Document versions
Any substantive edit: bump header `Version`, set `Last Updated`, add a Version-history row, update the registry row. Same commit.

### 2.2 New file vs edit in place
Edit in place for everything except a **breaking change** (a dependent layer's reading of the document would be invalidated). A breaking change creates `-v2.md`; the old file gets a deprecation notice pointing to it and its registry status becomes `Superseded`.

## 3 · Commits

Conventional Commits:

```
type(scope): short imperative description
```

Types: `feat fix docs chore tech infra refactor test ci build perf release`. Scope = the package, service, layer or subsystem affected (`ci`, `infra`, `L3`, `auth-service`).

Three-tier ID check before writing the subject:
1. A **tracking ID already exists** (issue, ticket) → reference it inline.
2. Else the work is **remediation-track work** → cite the audit ID inline: `fix(auth): R-041 — reject expired refresh tokens`.
3. Else **no ID** — a clear message suffices. Never invent IDs; never use a bracketed `[ID]` prefix.

**Commits and PR descriptions carry no AI attribution.** `.githooks/commit-msg` enforces it. A hook is used because the rule otherwise competes with whatever instruction a coding tool injects per session.

## 4 · Git branches

### 4.1 Naming

```
<type>/<short-slug>      feat/ fix/ tech/ infra/ docs/ chore/
```
All lowercase, hyphens only, 2–6 words. Same three-tier ID check: an existing tracking ID goes in the slug (`fix/gh-142-otp-limit`); remediation embeds `r-NNN` (`feat/r-034-security-remediation`); otherwise a plain slug.

### 4.2 Rules

- `main` = production. `develop` = integration. Both are PR-only.
- **Always cut the working branch from `develop`, never `main` — hotfixes included.** There is no fast path, because the fast path is what lets the branches drift.
- All PRs target `develop`. **`main` only ever receives `develop`**; the only PR into `main` is the `develop` → `main` promotion, titled `release: …`.
- `release/*` branches are not used; if ever reintroduced they merge into `develop` first.

### 4.3 What enforces it

| Rule | Enforced by |
|---|---|
| PR-only, no force-push, linear history on `develop`, merge-commit-only on `main` | rulesets `main-develop-protection`, `main-protection` |
| `main`/`develop` cannot be deleted | `main-develop-undeletable` |
| CI must pass | required status checks in both rulesets |
| **Only `develop` may PR into `main`** | `.github/workflows/branch-policy.yml` — a ruleset cannot see a PR's head branch |

These are **rulesets, not classic branch protection**; `gh api repos/.../branches/<b>/protection` returns 404 by design. Inspect with `gh api repos/EnclaveStack/<repo>/rulesets`.

## 5 · Releases (SemVer)

`MAJOR.MINOR.PATCH`. New deliverable → minor; fix only → patch; breaking public contract → major. Tag `main` after promotion; every `v*` tag has a `CHANGELOG.md` entry.

## 6 · Files

kebab-case for every new filename. Code/schema artifacts that belong to a layer still carry the `LL-NNN-slug-vN` prefix with their natural extension.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Initial release from soupler-governance-template v1.2.1 |
