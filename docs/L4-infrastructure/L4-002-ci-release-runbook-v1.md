# L4-002 · CI Pipeline and Release Runbook

---
| Field | Value |
|---|---|
| Document ID | L4-002-ci-release-runbook-v1 |
| Layer | L4 — Infrastructure |
| Status | Draft |
| Version | v9 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-25 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

How platform is built, tested and released in CI, and how to reproduce each CI job locally. Test levels are defined in `L5-002`.

## 1 · Workflows

| Workflow | Trigger | Jobs |
|---|---|---|
| `ci.yml` — **Console Checks** (governance template) | PR into `develop`/`main`; manual | Quality Checks (`pnpm -r lint`, `pnpm -r type-check`), Tests (`pnpm -r test`): the JavaScript workspace only. Go is `go.yml`'s, so the race-enabled Go suite runs once per PR, not twice |
| `go.yml` — **Go and End-to-End Tests** | PR into `develop`/`main`; manual | **Go Quality** (vet incl. tagged suites, staticcheck, generated contract current), **Go Tests** (race, coverage), **Provider Acceptance** (OpenTofu), **Console E2E** (Playwright + Chrome; the server is compiled in its own step first); **Engine E2E** (Docker driver, the QEMU hypervisor with KVM, built-binary e2e including a VM), **Load Test** (k6, 30 s, 10 read + 2 write users, `L5-003` §2) |
| `pr-governance.yml`, `secret-scan.yml`, `branch-policy.yml` | Governance template | Unchanged, except `secret-scan.yml` is PR-only too |
| `release.yml` — **Release** | push of a `v*` tag | Build, sign and publish a release (§3, ADR-054) |

Every workflow except **Release** runs on pull requests only (plus **Run workflow** for a one-off), and every job of those PR workflows is a required check. There are no push or nightly runs: each commit reaches `develop` or `main` through a PR that already ran the full suite, so a push trigger only re-ran it on the same commit. `L5-002` §5 sets that the real-engine e2e and load test run on every PR.

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

## 3 · Release (ADR-054)

A release is a `vX.Y.Z` tag on `main`. `.github/workflows/release.yml` (**Release**) does the rest. It is the only workflow that runs on a push rather than a pull request.

1. **Promote.** Merge the `develop` → `main` release PR. Before that, on `develop`, move the `## [Unreleased]` entries in `CHANGELOG.md` under `## [X.Y.Z] — YYYY-MM-DD`; the workflow refuses a tag whose version has no section.
2. **Tag.** Only the maintainer can do this (`release-tags.json`):
   ```bash
   git fetch origin && git tag -a vX.Y.Z origin/main -m "vX.Y.Z" && git push origin vX.Y.Z
   ```
3. **The workflow** then:
   - checks the tag is `vMAJOR.MINOR.PATCH`, its commit is on `main` and `CHANGELOG.md` has its section;
   - runs `make release VERSION=vX.Y.Z`, which builds the console, then `enclave` and `terraform-provider-enclave` for darwin/linux × amd64/arm64 into `build/release/` (ADR-054 lists the files);
   - signs `SHA256SUMS` keyless with cosign (`SHA256SUMS.sigstore.json`);
   - creates the GitHub release, with that `CHANGELOG.md` section as its notes;
   - pushes `ghcr.io/enclavestack/enclave:X.Y.Z` and `:latest` (linux/amd64, linux/arm64);
   - commits `Formula/enclave.rb` to `EnclaveStack/homebrew-tap` (skipped, with a warning, without `HOMEBREW_TAP_TOKEN`).
4. **Website.** In `marketing`, copy the new `install.sh` and switch any version shown (`marketing`'s own runbook).
5. **Check it** (`L5-001` §2j): `brew install enclavestack/tap/enclave` and `curl -fsSL https://enclavestack.soupler.com/install.sh | sh` on a clean Mac and a clean Linux machine, then `enclave version`.

To build the same files locally without publishing anything, run `make release VERSION=v0.0.0-test`. The output goes to `build/release/`.

### 3.1 · Set up once, by hand

| What | How | Why |
|---|---|---|
| The tap repository | Create the public `EnclaveStack/homebrew-tap` with a `Formula/` folder and a README | `brew install enclavestack/tap/enclave` reads it |
| `HOMEBREW_TAP_TOKEN` | A fine-grained token with **Contents: read and write** on `EnclaveStack/homebrew-tap` only, saved as an Actions secret of `platform` | lets `release.yml` commit the formula; the default `GITHUB_TOKEN` can't write to another repository |
| The image's visibility | After the first release: the org's **Packages › enclave › Package settings › Change visibility › Public** | GHCR packages start private |

The workflow needs no other secret. Signing is keyless, `GITHUB_TOKEN` creates the release and pushes the image (`contents: write`, `packages: write`, `id-token: write`), and nothing has to be rotated.

### 3.2 · Check a download by hand

```bash
sha256sum --ignore-missing -c SHA256SUMS                     # shasum -a 256 --ignore-missing -c SHA256SUMS on macOS
cosign verify-blob SHA256SUMS --bundle SHA256SUMS.sigstore.json \
  --certificate-identity-regexp '^https://github.com/EnclaveStack/platform/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### 3.3 · When a release goes wrong

- **The workflow failed before the release was created:** fix the problem on `develop`, promote it, then tag the next patch version. Tags are never moved or reused.
- **A published release is bad:** mark it a pre-release on GitHub, so `releases/latest` and the install script skip it, and publish a fixed patch release. Homebrew picks up the next formula commit.

⚠️ GAP: the provider isn't on the OpenTofu registry yet. That needs the mirror repository `EnclaveStack/terraform-provider-enclave` and a registry entry (ADR-009). Until then, the release's provider zips install by hand (`L5-004` §8.9). apt, `.deb`/`.rpm` and Scoop aren't built. Required-check names in the rulesets gain the `go.yml` jobs when the repository's rulesets are next updated.

## 4 · VMs in CI (v0.5)

The Engine E2E job opens `/dev/kvm` to the runner's user with a udev rule, installs `qemu-system-x86` and `qemu-utils`, and runs `make engine-qemu` before `make e2e`, so the e2e suite's VM part runs too (ADR-025). The real-QEMU tests download the Debian 12 image once per run into `~/.cache/enclave-test/` (about 350 MB). The job was added on 2026-10-21 and hasn't run in GitHub Actions yet: everything in it passed locally on an Apple M4 with HVF.

## 5 · Two machines in CI (v0.8, planned)

`go.yml` gains **Machines E2E** (`make e2e-machines`): the server runs in a container on the runner's Docker, and a Docker-in-Docker container acts as the second machine (ADR-042 – ADR-047). It joins, places, reaches across machines through a private network, moves a cache with its data, and cuts the agent off so an app restarts elsewhere. It runs on every pull request and becomes a required check in the same PR that adds it. The real-Docker suite (`make engine-docker`) gains the two-engine mesh test, which also uses Docker-in-Docker; GitHub's Ubuntu runners allow privileged containers.

`make engine-docker` runs with `-timeout 30m` (`go test` stops a package after 10 minutes by default, and this package takes about that long; a run on 2026-10-23 was killed at 10 m while every test was passing, and left `es-system-probe` behind). The rolling-deploy test's failure message lists what Docker reports for each container, so a CI failure says why the driver and the daemon disagree.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler | Workflows, local reproduction, release plan |
| v2 | 2026-10-20 | Soupler | Actual workflow and job names, make targets, staticcheck v0.8.2, OpenTofu acceptance environment |
| v3 | 2026-10-21 | Soupler | v0.5: KVM and QEMU in Engine E2E, make engine-qemu (§4) |
| v4 | 2026-10-22 | Soupler | Quality Checks and Tests run the JS workspace only (the Go suite ran twice per push); Go Quality, Go Tests, Provider Acceptance and Console E2E are required checks; workflows renamed to Console Checks (was CI) and Go and End-to-End Tests; all workflows PR-only (no push or nightly), Engine E2E and Load Test run on every PR and are required |
| v5 | 2026-10-22 | Soupler | §5 two machines in CI (planned): Machines E2E job, the two-engine mesh test |
| v6 | 2026-10-22 | Soupler | §5: what Machines E2E covers, as built |
| v7 | 2026-10-22 | Soupler | Load Test runs at 10 + 2 virtual users (`L5-003` v3); Console E2E compiles the server before Playwright starts it |
| v8 | 2026-10-23 | Soupler | `make engine-docker` runs with a 30 m timeout; the rolling-deploy failure message lists Docker's container states |
| v9 | 2026-10-25 | Soupler | §3 rewritten as built (ADR-054): `release.yml` on a `v*` tag, `make release`, keyless signing, GHCR image, Homebrew tap; one-time set-up, checking a download, a bad release (issue #3) |
