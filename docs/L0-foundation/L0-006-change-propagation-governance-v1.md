# L0-006 · Change Propagation Governance

---
| Field | Value |
|---|---|
| Document ID | L0-006-change-propagation-governance-v1 |
| Layer | L0 — Foundation Standards |
| Status | Final |
| Version | v1 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-20 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

Defines which layer owns each kind of change, the exact order edits propagate, and the surface artifacts every change must touch. This is the document that stops layers drifting out of sync.

## 1 · The Golden Rule

**The layer that owns the problem owns the first edit. Downstream layers sync to it — never the reverse.**

If you feel the need to edit a downstream document (or code) before touching the upstream source, a design step has been skipped. Stop, identify the owning layer, and begin there.

## 2 · Classify first

Before touching anything, classify the change into exactly one primary category.

| Category | Definition | Triggers |
|---|---|---|
| **STRATEGIC** | Why the product exists, who it serves, how success is measured | pivot, scope change, ICP shift, new business model |
| **PRODUCT** | What must be built and how it behaves for users | new/removed feature, persona or role change, UX overhaul |
| **ENGINEERING** | How the system is built, structured, secured | bug fix, new pattern, security hardening, API or schema change |
| **OPERATIONAL** | How it is deployed, operated, monitored, sold | CI/CD, infra, incident SOP, GTM, governance |

## 3 · Propagation paths

### 3.1 Strategic — owner L1
1. **L1** artifact that owns the changed decision (vision, ICP, market, positioning…)
2. **L2** — re-scope PRD, re-rank priorities, re-sequence roadmap
3. **L5** — GTM artifacts if market entry changes
4. Surface: `README.md` (if the value proposition changed), `CHANGELOG.md`

### 3.2 Product — owner L2
1. **L2** — PRD / feature brief, prioritisation, roadmap
2. **L2.5** — UX artifacts if interface or interaction changes
3. **L3** — API contracts, data model, security architecture, new ADR — if system boundaries are touched
4. Surface: `CHANGELOG.md`; `README.md` if publicly visible; the **L5 test playbook** if verification changes

### 3.3 Engineering — owner L3 (architectural) or the code (isolated fix)
1. **L3** — a new **ADR** if the change alters *how the system works*, not merely that it works. A fix correcting one code path needs no ADR; a fix changing session handling, auth model or an API contract does. Update system architecture, API contracts, data model, security architecture, event schema as affected.
2. **L0** — if the change establishes a new org-wide pattern (error standards, branching)
3. Surface: `CHANGELOG.md` (always); `README.md` if stack/public behaviour changes; the L5 test playbook when setup or verification changes

### 3.4 Operational — owner L0 (new standard) or L4 (infra)
1. **L0** — if it codifies a new governance standard (or changes this propagation model)
2. **L4** — environment strategy, CI/CD, runbooks, secrets. **Cost**: any change that adds, removes or resizes a billed resource must update the cost-sizing artifact — its own runbook recording *why* is not enough; only the cost artifact shows the *total*, and it silently drifts when this is skipped.
3. **L5** — operational procedures, GTM
4. Surface: `CHANGELOG.md`; `README.md` if stack/setup changes

## 4 · Non-negotiable updates

| Update | Rule |
|---|---|
| `CHANGELOG.md` | Every change, dated. No exceptions. |
| Layer registry | Every new document registered in its layer's `-000-artifact-registry` in the same commit. |
| Registry row accuracy | When an artifact's `Version` or `Last Updated` changes, its registry row changes in the same commit. A registry naming the wrong version is as bad as no registration. |
| `README.md` | Whenever product description, stack or setup instructions change. |
| Test playbook (L5) | Whenever shipped functionality, setup or verification behaviour changes how it should be tested. |

## 5 · The governance gate

`bin/statutory-integrity.sh` (SIVE) enforces this on every PR. It verifies that: every layer has a registry; **every artifact anywhere in a layer's tree — numbered subfolders included — is registered**; no registry row points at a missing file; each row's Version and Last Updated match the file's own header; headers are complete and Document ID equals filename; `README.md` and `CHANGELOG.md` exist and are non-empty. A failing gate must not be merged around, and never bypassed with `--no-verify`.

*Why the recursion and freshness checks exist:* an earlier gate scanned only a layer's top directory, so documents in subfolders were never actually checked, and nothing compared registry versions with files. Both gaps surfaced only when a stale cost document was noticed by accident; the sweep that closed them found dozens of stale rows and entire remediation sessions that had shipped unregistered.

## 6 · Quick reference

| Change | Start here | Always touch | Open L6? |
|---|---|---|---|
| Isolated bug fix | the code | `CHANGELOG.md` | No |
| Systemic defect (a class of bug) | L3 ADR | `CHANGELOG.md` | Yes |
| New feature | L2 PRD | `CHANGELOG.md`, `README.md` if public | Only if security risk |
| Verification change | owning L2/L3 doc | `CHANGELOG.md`, L5 playbook | No |
| UX overhaul | L2.5 | `CHANGELOG.md` | No |
| New technical pattern | L3 ADR | `CHANGELOG.md` | If prior design obsoleted |
| API change | L3 api-contracts | `CHANGELOG.md` | No |
| Schema change | L3 data-model | `CHANGELOG.md` | No |
| Security hardening | L3 security architecture | `CHANGELOG.md` | Yes if compliance-driven |
| CI/CD change | L0-005, L4 | `CHANGELOG.md` | No |
| Infra change | L4 | `CHANGELOG.md`, cost artifact if billed | No |
| Strategy shift | L1 | `CHANGELOG.md`, `README.md` | If multi-layer drift |

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Initial release from soupler-governance-template v1.2.1 |
