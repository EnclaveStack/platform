# L0-001 · Master Document Template

---
| Field | Value |
|---|---|
| Document ID | L0-001-master-document-template-v1 |
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

The canonical skeleton for every governed document in every layer (L0–L7). SIVE (`bin/statutory-integrity.sh`) enforces the header block mechanically; the body structure is enforced in review.

## 1 · Header block (mandatory, first thing after the H1)

```
# LL-NNN · Title

---
| Field | Value |
|---|---|
| Document ID | LL-NNN-slug-vN |
| Layer | L3 — Architecture |
| Status | Draft | In Review | Final |
| Version | vN |
| Created | YYYY-MM-DD |
| Last Updated | YYYY-MM-DD |
| Owner | Full Name — Role |
| Reviewers | Full Name — Role |
| Copyright | © YYYY Org. All rights reserved. |
| License | Proprietary |
---
```

| Field | Rule |
|---|---|
| Document ID | Equals the filename (minus `.md`). SIVE fails on a mismatch. |
| Version | Equals the newest row of the document's own Version history. Header and table are two renderings of one fact. |
| Last Updated | Bumped on every edit. The layer registry row must match (SIVE checks it). |
| Owner | One accountable person or team — never a list. |
| Status | Draft → In Review → Final. Dependents may rely on Final only. |

## 2 · Body structure

1. **Purpose** — one paragraph: why this document exists and what it owns.
2. **Context / Definitions** — only what a new reader needs.
3. **Content sections**, numbered `1 · …`.
4. **Assumptions and gaps** — flag every assumption `⚠️ ASSUMPTION:` and every gap `⚠️ GAP:`; never paper over them.
5. **Related documents** — table of Document ID · title · relationship.
6. **Version history** — newest first: Version · Date · Author · Summary. A summary says *what changed and why*, including corrections to earlier errors — silence about an error is how it survives.
7. **Final artifact checklist** — tick before moving Status to Final.

## 3 · Quality gates before Final

- [ ] Header complete; Document ID equals filename
- [ ] Registered in the layer's `-000-artifact-registry` with matching Version/Last Updated
- [ ] No content duplicated from another layer — link to the owner instead (L0-007 rule 4)
- [ ] Assumptions and gaps flagged
- [ ] Version history row added
- [ ] `bash bin/statutory-integrity.sh` passes

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Initial release from soupler-governance-template v1.2.1 |
