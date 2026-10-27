# L2-000 · Product Requirements Artifact Registry

---
| Field | Value |
|---|---|
| Document ID | L2-000-artifact-registry-v1 |
| Layer | L2 — Product Requirements |
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

Registry of every artifact in layer **L2 — Product Requirements** (personas, PRDs, feature requirements, priorities, roadmap). This layer owns: *What must be built for users and the business*. It does not own: How it looks, or how it is engineered.

**Every `.md` artifact under this folder — including numbered subfolders — must have a row below, in the same commit that creates it.** The Statutory Integrity gate (`bin/statutory-integrity.sh`) blocks the PR otherwise. When an artifact's own `Version` or `Last Updated` header changes, update its row here in the same commit.

## Registered Artifacts

| Document ID | Title | Status | Version | Owner | Last Updated |
|---|---|---|---|---|---|
| L2-001-web-console-prd-v1 | Web Console PRD | Draft | v21 | Soupler | 2026-10-25 |
| L2-002-cli-prd-v1 | Command-Line Interface PRD | Draft | v22 | Soupler | 2026-10-24 |
| L2-003-opentofu-provider-prd-v1 | OpenTofu Provider PRD | Draft | v18 | Soupler | 2026-10-24 |
| L2-004-control-plane-resource-lifecycle-prd-v1 | Control Plane and Resource Lifecycle PRD | Draft | v3 | Soupler | 2026-10-21 |
| L2-005-v0-3-run-a-real-product-prd-v1 | v0.3 "Run a Real Product" PRD | Draft | v2 | Soupler | 2026-10-21 |
| L2-006-v0-4-networking-prd-v1 | v0.4 "Networking" PRD | Draft | v2 | Soupler | 2026-10-21 |
| L2-007-v0-5-vms-and-volumes-prd-v1 | v0.5 "VMs and Volumes" PRD | Draft | v2 | Soupler | 2026-10-21 |
| L2-008-v0-6-data-and-messaging-prd-v1 | v0.6 "Data and Messaging" PRD | Draft | v3 | Soupler | 2026-10-24 |
| L2-009-v0-7-operate-prd-v1 | v0.7 "Operate" PRD | Draft | v5 | Soupler | 2026-10-22 |
| L2-010-v0-8-many-machines-prd-v1 | v0.8 "Many Machines" PRD | Draft | v4 | Soupler | 2026-10-23 |
| L2-011-per-app-bucket-access-prd-v1 | Per-app Bucket Access PRD | Draft | v1 | Soupler | 2026-10-24 |
| L2-012-transcoder-renditions-and-notifications-prd-v1 | Transcoder Renditions, Notifications and Tags PRD | Draft | v2 | Soupler | 2026-10-24 |
| L2-013-bucket-objects-and-readable-details-prd-v1 | Bucket Objects and Readable Details PRD | Draft | v1 | Soupler | 2026-10-24 |
| L2-014-public-access-prd-v1 | Public Access PRD | Draft | v1 | Soupler | 2026-10-24 |
| L2-015-connections-prd-v1 | Connections PRD | Draft | v1 | Soupler | 2026-10-25 |
| L2-016-v0-10-close-the-roadmap-prd-v1 | v0.10 "Close the Roadmap" PRD | Draft | v1 | Soupler | 2026-10-26 |

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Registry created by soupler-governance-template v1.2.1 |
