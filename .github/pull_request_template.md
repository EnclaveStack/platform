## Summary
- <!-- concise summary bullet -->

## Issues
- Closes #<!-- issue number, if one exists -->
- Relates to #<!-- optional -->

## Test plan
- `<!-- the command(s) you actually ran -->`

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
- [ ] No secrets, no hardcoded local paths
- [ ] Shared logic lives in a shared package/module — nothing copied between places (L0-009)
