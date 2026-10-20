# L0-010 · Repository Topology

---
| Field | Value |
|---|---|
| Document ID | L0-010-org-repo-topology-v1 |
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

Which repositories make up Homecloud, what each owns, and which direction truth flows. This is the document behind every "never modify a sibling from here" rule in the repos' `CLAUDE.md` files.

<!-- gov:begin topology -->
- **`homecloud-platform`** — profile `product`: Homecloud monorepo: Go control plane, machine agent and CLI, plus the TypeScript web console.
- **`homecloud-website`** — profile `site`: Public website for Homecloud.
- **`homecloud-assets`** — profile `assets`: Brand assets: logos, social templates and renders.
<!-- gov:end topology -->

Standards (L0) live in **`homecloud-platform`**.

## Rules

1. One repository owns each fact; others follow. Following never edits the owner.
2. Locally the repos are siblings under one parent directory (`../<repo>`); tooling and docs refer to siblings by that relative path.
3. A change that spans repos is made in the owner first, then in each follower, as separate PRs in their own repositories.
4. Cross-origin calls between a client repo and an API repo require the API's allowed-origins list to be updated in the same change.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Homecloud | Initial release from soupler-governance-template v1.2.1 |
