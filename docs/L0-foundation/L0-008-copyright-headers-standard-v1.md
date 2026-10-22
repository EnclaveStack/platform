# L0-008 · Copyright Headers Standard

---
| Field | Value |
|---|---|
| Document ID | L0-008-copyright-headers-standard-v1 |
| Layer | L0 — Foundation Standards |
| Status | Final |
| Version | v2 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-22 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs |
| License | Apache-2.0 |
---

## Purpose

Every governed document and source file declares ownership and licence consistently.

## 1 · Documents

The `Copyright` header field is always `© <current year> Soupler Labs` and `License` is `Apache-2.0`, the project's licence (`L1-001` §4; the full text is `LICENSE` at the repo root, with `NOTICE`). The year is the year of creation and is not rewritten on edits. When the project's licence changes, every header changes in one sweep; that sweep alone doesn't bump a document's version. No "All rights reserved": Soupler Labs keeps the copyright and the licence grants the rights, so reserving them all would contradict it. Private sibling repositories (`L0-010`) aren't open-licensed and keep "All rights reserved".

## 2 · Source files

New source files in first-party packages carry a one-line header comment:

```
// © 2026 Soupler Labs. Licensed under the Apache License 2.0.
```

Generated files, vendored code and third-party snippets keep their original notices and are never relabelled.

## 3 · Third-party material

Anything under a licence (fonts, icons, music, stock media) is recorded with its licence and permitted use before it ships.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Initial release from soupler-governance-template v1.2.1 |
| v2 | 2026-10-22 | Soupler Labs | License is Apache-2.0 (was MIT, `L1-001` v4); "All rights reserved" dropped from the copyright line, which contradicted the open licence; a licence sweep doesn't bump versions |
