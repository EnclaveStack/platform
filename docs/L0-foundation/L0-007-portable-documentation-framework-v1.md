# L0-007 · Documentation Framework

---
| Field | Value |
|---|---|
| Document ID | L0-007-portable-documentation-framework-v1 |
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

The project-agnostic L0–L7 operating model: how a product thinks, decides, builds, verifies, remediates and audits itself. It is portable — only the org name, copyright and domain vocabulary change between products.

## 1 · Layers

| Layer | Name | Owns | Does NOT own |
|---|---|---|---|
| L0 | Foundation Standards | How artifacts are written and governed | Any product, tech or ops content |
| L1 | Vision & Strategy | Why the product exists; how success is measured | What must be built |
| L2 | Product Requirements | What must be built for users and the business | How it looks or is engineered |
| L2.5 | UX & Design | How it behaves and feels in the interface | Which features exist |
| L3 | Architecture | How it is engineered: boundaries, API contracts, **data model**, security, ADRs | Runbooks, GTM |
| L4 | Infrastructure & Deployment | How it is provisioned and deployed | Product intent, architecture decisions |
| L5 | Operations & GTM | How it is tested, operated, launched, supported | Architecture, requirements |
| L6 | Remediation | Audits and corrective plans | The fix itself |
| L6.1 | Remediation Execution | What was done, with evidence | Why the problem existed |
| L7 | Forensics & Traceability | Permanent chain of custody, finding → merged fix | Anything it can link to instead |

L2.6 (delivery stories) is intentionally absent: story-level tracking decays into a second, stale copy of the issue tracker. Data models live in L3; delivery state lives in the tracker.

## 2 · Export rules

| Export | Rule |
|---|---|
| L0 → all | standards and naming apply to every artifact |
| L1 → L2 | vision, personas, market constraints inform requirements |
| L2 → L2.5 / L3 | requirements inform UX and engineering |
| L2.5 → L3 / L4 | interaction decisions inform implementation |
| L3 → L4 / L5 | blueprints inform infrastructure and operations |
| L4 → L5 | provisioned infra informs runbooks |
| L6 → L4 / L7 | findings and plans inform implementation and the forensic registry |
| L7 → governance | traceability serves compliance and due diligence |

## 3 · Adoption model

| Model | Layers |
|---|---|
| Minimum (small team, early) | L0, L1, L2, L3, L4 |
| Mature (production traffic) | + L2.5, L5 |
| Regulated / audit-sensitive (privacy, finance, health) | + L6, L6.1, L7 |

Set `org.regulated` in `governance.json` to add L6/L6.1/L7 everywhere.

## 4 · Multi-repository model

A product usually spans repositories with different jobs. Each has a **profile** (`product`, `site`, `assets`). Standards (L0) live in **one** repository (`platform`); the others point to it. Source-of-truth direction is explicit — one repo *owns* a fact (brand tokens, legal text, API contracts) and the others *follow* it. Topology is in `L0-010`.

## 5 · Non-negotiable framework rules

1. Every layer has a `-000-artifact-registry` before any other artifact in it exists.
2. Every artifact follows L0-002 naming.
3. Layer ownership is stable once adopted; reassigning it is an L0 change.
4. Cross-layer **links** are encouraged; cross-layer **ownership duplication** is forbidden.
5. Extension layers reference source artifacts; they do not re-own them.
6. Execution evidence belongs in the implementation layer, not in strategy or audit layers.
7. A document must never describe itself as current when it has not been revised — when reality changes, correct the document and log the correction.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Initial release from soupler-governance-template v1.2.1 |
