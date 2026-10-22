# Branch Protection Guide

EnclaveStack uses **GitHub Flow with a promotion branch**: feature → `develop` → `main`. Protection is enforced by **repository rulesets**, not classic branch protection — `gh api repos/.../branches/<branch>/protection` returns 404 by design.

## Rulesets (source of truth: `.github/governance/rulesets/*.json`)

| Ruleset | Target | What it enforces |
|---|---|---|
| `main-develop-protection` | `develop` | PR required · squash/rebase only · linear history · no force-push · required checks |
| `main-protection` | `main` | PR required · merge commits only · no force-push · required checks incl. `Branch Policy` |
| `main-develop-undeletable` | `main`, `develop` | deletion blocked, no bypass actors |

Required checks: `Statutory Integrity` · `PR Metadata` · `Quality Checks` · `Tests` · `Secret Scan` · `Branch Policy` (main only).

## What a ruleset cannot do

A ruleset has no concept of a PR's head branch, so "only `develop` may PR into `main`" is enforced by `.github/workflows/branch-policy.yml`.

## Applying and changing

```bash
gov github .                                   # from soupler-governance-template: create/update all three
gh api repos/EnclaveStack/platform/rulesets      # inspect
```

When a required job is renamed in a workflow, update the matching ruleset JSON in the same PR — a renamed job that is not in the ruleset silently stops being enforced.

## Release

`develop` → `main` PR titled `release: vX.Y.Z …`, merge commit, then tag `main`. Every `v*` tag needs a `CHANGELOG.md` entry.
