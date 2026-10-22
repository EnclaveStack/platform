# L4-002 · CI Pipeline and Release Runbook

---
| Field | Value |
|---|---|
| Document ID | L4-002-ci-release-runbook-v1 |
| Layer | L4 — Infrastructure |
| Status | Draft |
| Version | v3 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-21 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs. All rights reserved. |
| License | MIT |
---

## Purpose

How platform is built, tested and released in CI, and how to reproduce each CI job locally. Test levels are defined in `L5-002`.

## 1 · Workflows

| Workflow | Trigger | Jobs |
|---|---|---|
| `ci.yml` (governance template) | PR, push to `develop`/`main` | Quality Checks (`pnpm lint`, `pnpm type-check` — both include Go and the generated-contract check), Tests (`pnpm test` — console + `go test -race ./...`) |
| `go.yml` | PR, push to `develop`/`main`, nightly 02:17 UTC | **Go Quality** (vet incl. tagged suites, staticcheck, generated contract current), **Go Tests** (race, coverage), **Provider Acceptance** (OpenTofu), **Console E2E** (Playwright + Chrome); on push and nightly only: **Engine E2E** (Docker driver, the QEMU hypervisor with KVM, built-binary e2e including a VM), **Load Test** (k6, 30 s) |
| `pr-governance.yml`, `secret-scan.yml`, `branch-policy.yml` | Governance template | Unchanged |

## 2 · Reproduce locally

```bash
pnpm lint && pnpm type-check && pnpm test             # what Quality Checks + Tests run
make lint                                              # vet (incl. tagged suites) + staticcheck v0.8.2 + console lint
make provider-acceptance                               # needs `tofu`; sets TF_ACC, TF_ACC_TERRAFORM_PATH, TF_ACC_PROVIDER_NAMESPACE=hashicorp, TF_ACC_PROVIDER_HOST=registry.opentofu.org
make console-e2e                                       # needs Chrome (Playwright uses channel "chrome")
make engine-docker && make e2e                         # needs Docker running and `tofu`
make engine-qemu                                       # needs QEMU 8+ with HVF (macOS) or KVM (Linux); skips with the reason otherwise
make load                                              # needs k6; DURATION=30s to shorten
```

## 3 · Release (at public launch)

1. Promote `develop` → `main` (`release:` PR).
2. Tag `vX.Y.Z` on `main`. GoReleaser builds `enclave` and `terraform-provider-enclave` for darwin/linux × amd64/arm64, embeds the console, signs checksums, and publishes a GitHub release.
3. The release job pushes the tag's sources to `EnclaveStack/terraform-provider-enclave` and publishes the provider release there for the OpenTofu and Terraform registries (ADR-009).
4. `install.sh` downloads the matching `enclave` build and verifies its checksum.

⚠️ GAP: GoReleaser config, signing key and mirror repository are created at public launch; until then releases are local builds (`make build`, version from `git describe` via `-ldflags`). Required-check names in the rulesets gain the `go.yml` jobs when the repository goes public.

## 4 · VMs in CI (v0.5)

The Engine E2E job opens `/dev/kvm` to the runner's user with a udev rule, installs `qemu-system-x86` and `qemu-utils`, and runs `make engine-qemu` before `make e2e`, so the e2e suite's VM part runs too (ADR-025). The real-QEMU tests download the Debian 12 image once per run into `~/.cache/enclave-test/` (about 350 MB). The job was added on 2026-10-21 and hasn't run in GitHub Actions yet: everything in it passed locally on an Apple M4 with HVF.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler Labs | Workflows, local reproduction, release plan |
| v2 | 2026-10-20 | Soupler Labs | Actual workflow and job names, make targets, staticcheck v0.8.2, OpenTofu acceptance environment |
| v3 | 2026-10-21 | Soupler Labs | v0.5: KVM and QEMU in Engine E2E, make engine-qemu (§4) |
