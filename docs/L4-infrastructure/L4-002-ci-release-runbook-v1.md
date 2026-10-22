# L4-002 · CI Pipeline and Release Runbook

---
| Field | Value |
|---|---|
| Document ID | L4-002-ci-release-runbook-v1 |
| Layer | L4 — Infrastructure |
| Status | Draft |
| Version | v6 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-22 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs |
| License | Apache-2.0 |
---

## Purpose

How platform is built, tested and released in CI, and how to reproduce each CI job locally. Test levels are defined in `L5-002`.

## 1 · Workflows

| Workflow | Trigger | Jobs |
|---|---|---|
| `ci.yml` — **Console Checks** (governance template) | PR into `develop`/`main`; manual | Quality Checks (`pnpm -r lint`, `pnpm -r type-check`), Tests (`pnpm -r test`): the JavaScript workspace only. Go is `go.yml`'s, so the race-enabled Go suite runs once per PR, not twice |
| `go.yml` — **Go and End-to-End Tests** | PR into `develop`/`main`; manual | **Go Quality** (vet incl. tagged suites, staticcheck, generated contract current), **Go Tests** (race, coverage), **Provider Acceptance** (OpenTofu), **Console E2E** (Playwright + Chrome); **Engine E2E** (Docker driver, the QEMU hypervisor with KVM, built-binary e2e including a VM), **Load Test** (k6, 30 s) |
| `pr-governance.yml`, `secret-scan.yml`, `branch-policy.yml` | Governance template | Unchanged, except `secret-scan.yml` is PR-only too |

Every workflow runs on pull requests only (plus **Run workflow** for a one-off), and every job above is a required check. There are no push or nightly runs: each commit reaches `develop` or `main` through a PR that already ran the full suite, so a push trigger only re-ran it on the same commit. `L5-002` §5 sets that the real-engine e2e and load test run on every PR.

## 2 · Reproduce locally

```bash
pnpm lint && pnpm type-check && pnpm test             # locally: the JS workspace and Go together (CI splits them between ci.yml and go.yml)
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

## 5 · Two machines in CI (v0.8, planned)

`go.yml` gains **Machines E2E** (`make e2e-machines`): the server runs in a container on the runner's Docker, and a Docker-in-Docker container acts as the second machine (ADR-042 – ADR-047). It joins, places, reaches across machines through a private network, moves a cache with its data, and cuts the agent off so an app restarts elsewhere. It runs on every pull request and becomes a required check in the same PR that adds it. The real-Docker suite (`make engine-docker`) gains the two-engine mesh test, which also uses Docker-in-Docker; GitHub's Ubuntu runners allow privileged containers.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler Labs | Workflows, local reproduction, release plan |
| v2 | 2026-10-20 | Soupler Labs | Actual workflow and job names, make targets, staticcheck v0.8.2, OpenTofu acceptance environment |
| v3 | 2026-10-21 | Soupler Labs | v0.5: KVM and QEMU in Engine E2E, make engine-qemu (§4) |
| v4 | 2026-10-22 | Soupler Labs | Quality Checks and Tests run the JS workspace only (the Go suite ran twice per push); Go Quality, Go Tests, Provider Acceptance and Console E2E are required checks; workflows renamed to Console Checks (was CI) and Go and End-to-End Tests; all workflows PR-only (no push or nightly), Engine E2E and Load Test run on every PR and are required |
| v5 | 2026-10-22 | Soupler Labs | §5 two machines in CI (planned): Machines E2E job, the two-engine mesh test |
| v6 | 2026-10-22 | Soupler Labs | §5: what Machines E2E covers, as built |
