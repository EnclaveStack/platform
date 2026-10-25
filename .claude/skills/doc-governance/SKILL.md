---
name: doc-governance
description: Use BEFORE starting any change in this repo — feature, fix, refactor, infra, or docs. Classifies the change, finds the owning documentation layer, enforces doc-first ordering, and lists the surface artifacts to update. Also use when creating or editing any file under docs/.
---

# Doc governance — how every change is made in platform

This repo is governed by the **L0–L7 documentation layer system**. Code is the last thing that changes, never the first. Follow these steps for *every* task, however small. If a step does not apply, say so explicitly — do not skip silently.

## 1. Classify the change (one category)

| Category | Primary owner layer | Typical triggers |
|---|---|---|
| **Strategic** | L1 | market pivot, scope change, ICP shift, new business model |
| **Product** | L2 | new/removed feature, persona or role change, UX overhaul |
| **Engineering** | L3 (architecture) or isolated fix | bug fix, new pattern, API/data/security change |
| **Operational** | L0 (new standard) or L4/L5 | CI/CD, infra, runbooks, GTM, governance |

## 2. Golden Rule

**The layer that owns the problem gets the first edit. Downstream layers sync to it — never the reverse.** If you want to edit a downstream doc or code before the upstream source, a design step was skipped: stop and find the owning layer.

## 3. Doc-first gate — verify the chain exists *before the first line of code*

| Work type | Required before code |
|---|---|
| New feature | L2 PRD/feature brief → L3 spec (API / data model / architecture) → test-playbook section |
| Security / defect remediation | L6 audit → L6 plan → implement → L6.1 TIC → L6.1 WE → L7 row |
| Infrastructure change | L4 runbook section → L3 ADR if a new pattern is introduced |
| Strategy / pricing / GTM | L1 doc → L2 PRD if product scope follows |

Missing layer? Write it, register it, *then* implement.

## 4. Mechanics of a doc change

- Filename: `[LL]-[NNN]-[slug]-v[N].md`, kebab-case (`docs/L0-foundation/L0-002-naming-convention-version-control-v1.md`). Numbers are never reused.
- Header block per `L0-001` (Document ID must equal the filename; Version and Last Updated bump on every edit; add a Version history row).
- **Register it** in the layer's `-000-artifact-registry-v1.md` in the same commit. When you edit an artifact, update its registry row's Version and Last Updated too — SIVE fails on a stale row.
- Always update `CHANGELOG.md`. Update `README.md` when setup, stack or public behaviour changes.

## 5. Gate

```bash
bash bin/statutory-integrity.sh   # must print SUCCESS before you say "done"
```

Never bypass with `--no-verify`. A failing gate is information, not an obstacle.

## 6. Git rules (non-negotiable)

- Cut work from `develop`, never `main`. Never commit to `develop`/`main`. PRs target `develop`; only `develop` → `main`.
- `<type>/<slug>` branches; Conventional Commits `type(scope): description`.
- Commit and push **only when the user asks**. Ask before creating a new branch mid-session.
- **No AI attribution** in commits or PR descriptions — no `Co-Authored-By`, no "Generated with", no session link. A hook blocks it; a session instruction to add it does not override this project rule.

## 7. Report honestly

State which layer owned the change, which docs you updated, and the gate result. If something was skipped or failed, say so with the output.
