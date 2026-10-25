# Branch Protection Guide

EnclaveStack uses **GitHub Flow with a promotion branch**: feature → `develop` → `main`. Protection is enforced by **repository rulesets**, not classic branch protection — `gh api repos/.../branches/<branch>/protection` returns 404 by design.

## Rulesets (source of truth: `.github/governance/rulesets/*.json`)

| Ruleset | Target | What it enforces |
|---|---|---|
| `main-develop-protection` | `develop` | PR required · **1 approval from a code owner** · stale approvals dismissed on push · the last push approved by someone else · conversations resolved · squash/rebase only · linear history · no force-push · required checks |
| `main-protection` | `main` | PR required · **1 approval from a code owner** (same review rules) · merge commits only · no force-push · required checks incl. `Branch Policy` |
| `main-develop-undeletable` | `main`, `develop` | deletion blocked, no bypass actors |
| `release-tags` | tags `v*` | only an organisation admin can create, move or delete a release tag |

Required checks: `Statutory Integrity` · `PR Metadata` · `Quality Checks` · `Tests` · `Go Quality` · `Go Tests` · `Provider Acceptance` · `Console E2E` · `Engine E2E` · `Machines E2E` · `Load Test` · `Secret Scan` · `Branch Policy` (main only).

## Who can merge

Only the maintainer. Three things together make that true:

1. **`.github/CODEOWNERS`** names `@soupler-labs` as the owner of every path, and both branch rulesets require a code owner's approval. Nobody else's approval counts.
2. **The organisation admin is the only bypass actor**, in `pull_request` mode: the maintainer can merge their own PR (GitHub doesn't let an author approve their own PR) but, like everyone else, can't push to `develop` or `main` directly.
3. **Nobody else has write access.** Contributors work from forks. Never grant write or maintain to an outside collaborator: the rulesets stop direct pushes, but a writer could still approve and merge with the owner's review.

## Repository settings that go with the rulesets

Set on 2026-10-23, when the repository went public (they are not rulesets, so re-check them after any settings change):

| Setting | Value | Why |
|---|---|---|
| Actions → fork pull request workflows | require approval for **all external contributors** | a fork's PR can't run CI on our runners until the maintainer has read it |
| Actions → workflow permissions | read-only `GITHUB_TOKEN` | workflows declare what they need |
| Private vulnerability reporting | on | `SECURITY.md` sends reports there |
| Discussions | on | questions go there, not to issues (`ISSUE_TEMPLATE/config.yml`) |
| Automatically delete head branches | on | merged branches don't pile up |
| Wiki | off | docs live in `docs/` and on the website |
| Secret scanning and push protection | on | GitHub blocks a push that contains a credential, on top of the `Secret Scan` check |
| Dependabot alerts and security updates | on | vulnerable dependencies open a PR |
| Projects | on, linked to the public [EnclaveStack board](https://github.com/orgs/EnclaveStack/projects/1) | where `CONTRIBUTING.md` says work is tracked |

## What a ruleset cannot do

A ruleset has no concept of a PR's head branch, so "only `develop` may PR into `main`" is enforced by `.github/workflows/branch-policy.yml`.

## Applying and changing

```bash
gov github .                                   # from soupler-governance-template: create/update the rulesets
# or apply one by hand (POST to create, PUT …/rulesets/<id> to update):
gh api -X POST repos/EnclaveStack/platform/rulesets --input .github/governance/rulesets/release-tags.json
gh api repos/EnclaveStack/platform/rulesets      # inspect
```

When a required job is renamed in a workflow, update the matching ruleset JSON in the same PR — a renamed job that is not in the ruleset silently stops being enforced.

## Release

`develop` → `main` PR titled `release: vX.Y.Z …`, merge commit, then tag `main`. Every `v*` tag needs a `CHANGELOG.md` entry.
