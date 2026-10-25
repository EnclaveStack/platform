<!--
Title: a Conventional Commit, e.g. `fix(reconciler): retry a database stuck in provisioning after a restart`.
Target: `develop`. Only the `develop` → `main` promotion (titled `release: …`) targets `main`.
How to fill this in: CONTRIBUTING.md → "Opening a pull request".
-->

## Summary
- <!-- what changed and why, in a few bullets -->

## Issues
- Closes #<!-- the issue this finishes; it closes on merge -->
- Relates to #<!-- optional -->

## Surfaces
<!-- tick every one this touches; a capability isn't done until all three can use it -->
- [ ] Web console
- [ ] CLI (`enclave`)
- [ ] OpenTofu provider
- [ ] HTTP API / `pkg/api` contract
- [ ] None of these (docs, CI, internal only)

## Test plan
- `<!-- the command(s) you actually ran -->`
- <!-- what you checked by hand in `make demo`: the happy path and at least one error path -->

## Screenshots
<!-- before / after, for anything visible in the console; delete this section otherwise -->

## Docs update
- Status: `UPDATED`
- Notes: <!-- list the docs you touched (layer + ID), or explain why this change is exempt -->

> Keep the exact `- Status:` / `- Notes:` bullet format. CI parses these lines literally.
> `UPDATED` = the owning layer's docs, the registry row, and `CHANGELOG.md` changed in this PR.
> `EXEMPT` needs a reason in Notes (e.g. "typo fix inside a code comment").

## Merge notes
- <!-- optional: rollout order, follow-ups, residual risks -->

---

## Checklist

### Branch
- [ ] Cut from `develop`, not `main`; targeting `develop` (only the `develop` → `main` promotion targets `main`)
- [ ] Name follows `<type>/<slug>` (L0-002 §4); one logical change in this PR

### Governance
- [ ] Change classified first (strategic / product / engineering / operational) and the **owning layer** edited first
- [ ] Every new `.md` is registered in its layer's `-000-artifact-registry` (and edited artifacts' rows updated)
- [ ] `CHANGELOG.md` updated
- [ ] `bash bin/statutory-integrity.sh` passes locally

### Code
- [ ] Tests added/updated; at least one fails without the change
- [ ] Ran the feature in `make demo` and found no `"level":"error"` in the server log
- [ ] No secrets, no hardcoded local paths; secrets never reach responses, events or logs
- [ ] Shared logic lives in a shared package/module — nothing copied between places (L0-009)
