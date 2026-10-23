# Contributing to EnclaveStack

Thanks for helping. EnclaveStack is an open-source, self-hosted cloud: one install turns a Mac or Linux machine into a host for apps, databases, caches, buckets and secrets, driven from a web console, the `enclave` CLI and OpenTofu. This guide covers how to ask, report, propose and change things, and how a change gets merged.

By taking part you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).

## Contents

- [Ways to help](#ways-to-help)
- [Asking a question](#asking-a-question)
- [Opening an issue](#opening-an-issue)
- [How work is tracked](#how-work-is-tracked)
- [Setting up](#setting-up)
- [Making a change](#making-a-change)
- [Opening a pull request](#opening-a-pull-request)
- [Review and merge](#review-and-merge)
- [Releases](#releases)
- [Licence of contributions](#licence-of-contributions)

## Ways to help

- **Report a bug**: something doesn't work, or works differently from the docs.
- **Suggest a feature**: describe a problem you have, and we'll work out the shape together.
- **Improve the docs**: guides, the README, or the design documents under [`docs/`](docs/).
- **Fix an issue**: look for [`good first issue`](https://github.com/EnclaveStack/platform/labels/good%20first%20issue) and [`help wanted`](https://github.com/EnclaveStack/platform/labels/help%20wanted).
- **Add a catalogue template**: a ready-made stack (an app with its database, storage and domain).
- **Answer someone else's question** in [Discussions](https://github.com/EnclaveStack/platform/discussions).

For anything bigger than a small fix, **open an issue first** and wait for a maintainer to agree on the approach. That saves you from building something that can't be merged.

## Asking a question

Use [Discussions](https://github.com/EnclaveStack/platform/discussions), not issues: **Q&A** for "how do I…", **Ideas** for early thoughts, **Show and tell** for what you host. Issues are for work that will change the repository.

**Security problems are never reported in public.** Follow [SECURITY.md](SECURITY.md).

## Opening an issue

[Open an issue](https://github.com/EnclaveStack/platform/issues/new/choose) and choose a form. Blank issues are turned off, so every issue starts with the facts a maintainer needs.

| Form | Use it for | Label it gets |
|---|---|---|
| **Bug report** | Something is broken or behaves differently from the docs | `bug`, `needs-triage` |
| **Feature request** | A new capability, or a change to how something works | `enhancement`, `needs-triage` |
| **Documentation** | Something in the docs is wrong, missing or unclear | `documentation`, `needs-triage` |
| **Task** | Planned engineering, infrastructure or docs work (mostly maintainers) | `task` |
| **Governance support** | A problem with the doc layers, the governance gate or the CI checks on your PR | `governance` |

### Writing a good issue

- **Title**: what's wrong or what you want, in plain words. "Database stays in Provisioning after a restart", not "bug" or "help".
- **One problem per issue.** Two bugs are two issues, even if they look related.
- **Search first**, including closed issues. If it already exists, add a 👍 and any new detail there.
- **For a bug**, give the version (`enclave version`), your OS, the engine (`--driver docker` or `fake`), where it happened (console, CLI, OpenTofu or the API), the exact steps, what you expected, what happened, and the log lines. Run the server with `--log-format json` and paste the lines around the error, **with secrets removed**.
- **For a feature**, start with the problem and who has it, not the solution. Say which surfaces need it: a capability isn't done until the console, the CLI and OpenTofu can all use it.

### What happens after you open one

1. A maintainer **triages** it, usually within a week: confirms it, asks questions, or closes it as a duplicate or out of scope, with the reason.
2. A confirmed issue loses `needs-triage`, gets a priority and an area, and goes on the project board.
3. If it's open to contributors it gets `help wanted` (or `good first issue`). Comment **"I'd like to take this"** before you start, and a maintainer assigns it to you, so two people don't do the same work.
4. An assigned issue with no activity for 30 days may be unassigned so someone else can pick it up.

## How work is tracked

Every issue is on the **[EnclaveStack project board](https://github.com/orgs/EnclaveStack/projects/1)**, which is public.

| Field | Values | Meaning |
|---|---|---|
| **Status** | Todo → In progress → Done | Todo: confirmed and waiting. In progress: someone is assigned and working. Done: the pull request merged into `develop`. |
| **Priority** | P0 · P1 · P2 · P3 | P0: broken for everyone, or a security fix. P1: next up. P2: planned. P3: nice to have. |
| **Area** | Console · CLI · OpenTofu · Server · Engine · Networking · Data · Docs · CI | The part of the product it touches. |

New issues are added to the board automatically, and a merged pull request that says `Closes #N` moves the issue to **Done**. Only maintainers edit the board.

## Setting up

You need **Docker**, **Go 1.26+**, **Node 22.12+** with **pnpm 9**, and **QEMU 8+** for VMs (`brew install qemu`, or `qemu-system` and `qemu-utils` on Linux).

```bash
# Fork the repository on GitHub, then:
git clone https://github.com/<you>/platform.git enclavestack && cd enclavestack
git remote add upstream https://github.com/EnclaveStack/platform.git
git checkout develop

bash bin/install-hooks.sh   # once per clone: commit-message and pre-commit checks
pnpm install
make demo                   # a running server with sample data on a simulated engine, no Docker needed
```

`make demo` prints the console URL and the sign-in (`admin@enclave.local` / `enclave-demo`). For the console with hot reload, run `pnpm dev`. The full setup, every make target and troubleshooting are in [`L4-001`](docs/L4-infrastructure/L4-001-local-dev-setup-v1.md).

### Where things live

| Path | What |
|---|---|
| `pkg/api` | The wire contract: types, errors, validation. `make contracts` regenerates the console's copy. |
| `pkg/client` | The Go SDK used by the CLI and the provider |
| `internal/` | The control plane: HTTP API, services, domain model, SQLite store, reconciler, engine drivers |
| `cmd/enclave`, `internal/cli` | The CLI and `enclave server` |
| `cmd/terraform-provider-enclave`, `internal/provider` | The OpenTofu provider |
| `console/` | The web console (behaviour and copy) |
| `packages/ui` | The design system: every colour, size, spacing step and component |
| `api/openapi.yaml` | The machine-readable API contract |
| `docs/` | The design documents, by layer (below) |

## Making a change

### 1. Branch from `develop`

`develop` is where work lands; `main` holds releases and only ever receives `develop`. Always branch from an up-to-date `develop`, **hotfixes included**:

```bash
git fetch upstream && git checkout -b fix/db-stuck-provisioning upstream/develop
```

Name the branch `<type>/<short-slug>`, where type is one of `feat` `fix` `docs` `chore` `tech` `infra` `refactor` `test` `ci` `build` `perf`. If there's an issue, put its number in the slug: `fix/142-db-stuck-provisioning`.

### 2. Docs first

EnclaveStack keeps a design document for everything it does, in layers under [`docs/`](docs/). **The document that owns a behaviour changes before the code does**, and the code follows it. In practice:

| Your change | Update first |
|---|---|
| A bug fix where the code is wrong and the docs are right | Nothing, apart from `CHANGELOG.md` |
| A new or changed feature | The product requirement in `docs/L2-product/`, then the spec in `docs/L3-architecture/`, then the test steps in `docs/L5-operations/L5-001` |
| An API change | `L3-002`, `pkg/api`, `api/openapi.yaml`, the generated console file, both console adapters and the handlers, **together** (tests check they match) |
| How the interface looks or behaves | `docs/L2.5-design-ux/` |
| Setup, CI or release | `docs/L4-infrastructure/` |

When you add a new `.md` under `docs/`, add its row to that layer's `-000-artifact-registry` in the same commit. When you change a document's `Version` or `Last Updated`, update its registry row too. **Every change adds a line to `CHANGELOG.md`** under `## [Unreleased]`.

Not sure which layer owns your change? Ask in the issue. The full rules are in [`L0-006`](docs/L0-foundation/L0-006-change-propagation-governance-v1.md); the engineering principles are in [`L0-009`](docs/L0-foundation/L0-009-engineering-principles-v1.md). [`CLAUDE.md`](CLAUDE.md) is the same contract, written for people and coding agents alike.

### 3. Write the code

- **Go**: `gofmt`, `go vet` and `staticcheck` clean. Services never call state-changing engine methods; only the reconciler does (a test enforces it).
- **TypeScript**: strict mode, no `any`. Styling lives in `packages/ui` only; a test fails if `console/src` styles anything itself.
- **Don't copy code.** If two places need the same logic, put it in a shared package.
- **Secrets never appear** in API responses, events or logs. Tests check this.
- **Every capability works from all three surfaces**: console, CLI and OpenTofu.
- New files use kebab-case names. Match the naming, comments and style of the code around you.

### 4. Test it

Every change needs at least one test that **fails without it and passes with it**. Integration tests hit the real SQLite database and HTTP API, not mocks.

```bash
pnpm lint && pnpm type-check && pnpm test   # console + Go, race detector
bash bin/statutory-integrity.sh             # the governance gate: must print SUCCESS
make console-e2e                            # Playwright against the real server (needs Chrome)
make provider-acceptance                    # OpenTofu (needs tofu)
make engine-docker && make e2e              # real Docker + OpenTofu
```

Then **run it**: start `make demo`, use the feature the way a user would, try the error paths (empty input, a missing resource, a wrong state), and check the server log for `"level":"error"`. A test that passes isn't the same as a feature that works.

### 5. Commit

Use [Conventional Commits](https://www.conventionalcommits.org/): `type(scope): short imperative description`.

```text
fix(reconciler): retry a database stuck in provisioning after a restart

The reconciler skipped resources whose last attempt was before the restart.
Closes #142
```

- Types: `feat` `fix` `docs` `chore` `tech` `infra` `refactor` `test` `ci` `build` `perf`
- Scope: the area you changed (`console`, `cli`, `provider`, `api`, `reconciler`, `docs`…)
- Imperative, lowercase, no full stop: "add", not "added" or "adds"
- Put an issue number in the body (`Closes #142`), not as a prefix in the subject
- **Never bypass the hooks with `--no-verify`.** If a hook is wrong, open a Governance support issue.

## Opening a pull request

Push your branch to your fork and open the pull request **against `develop`**. A pull request against `main` fails the `Branch Policy` check and can't be merged.

**Title**: the same Conventional Commit format, for example `fix(reconciler): retry a database stuck in provisioning after a restart`. CI checks it.

**Description**: the template fills in automatically. Complete every section:

| Section | What to write |
|---|---|
| **Summary** | What changed and why, in a few bullets |
| **Issues** | `Closes #N` for the issue it finishes (this closes the issue on merge), `Relates to #N` for others |
| **Surfaces** | Which of console, CLI, OpenTofu and API this touches |
| **Test plan** | The commands you ran and what you checked by hand |
| **Screenshots** | Before and after, for anything visible in the console |
| **Docs update** | `- Status: UPDATED` with the documents you changed, or `- Status: EXEMPT` with the reason. CI reads these two lines exactly as written, so keep the format. |
| **Checklist** | Tick what's true. If something doesn't apply, leave it unticked and say why. |

Keep a pull request to **one logical change**. A bug fix and a refactor are two pull requests. Smaller ones get reviewed faster.

Open it as a **draft** while you're still working, and mark it **Ready for review** when CI passes and you're done.

### The checks every pull request must pass

| Check | What it runs |
|---|---|
| `Statutory Integrity` | The governance gate: registries, document metadata, changelog |
| `PR Metadata` | Title format, the Docs update lines, no AI attribution |
| `Quality Checks`, `Tests` | Console lint, types and tests |
| `Go Quality`, `Go Tests` | `go vet`, `staticcheck`, the Go tests with the race detector |
| `Provider Acceptance` | The OpenTofu provider against a real server |
| `Console E2E` | Playwright against the real server |
| `Engine E2E`, `Machines E2E` | Real Docker and OpenTofu; two machines |
| `Load Test` | k6 against the server |
| `Secret Scan` | No credentials in the diff |
| `Branch Policy` | Pull requests into `main` come only from `develop` |

Workflows on a first-time contributor's pull request wait for a maintainer to approve them before they run.

## Review and merge

- **Only the maintainer merges.** Every pull request into `develop` or `main` needs the code owner's approval (see [`.github/CODEOWNERS`](.github/CODEOWNERS)), and contributors don't have write access, so nobody else can merge.
- A review is approval, a request for changes, or a question. Answer every comment, push fixes as new commits (don't force-push during review), and resolve a conversation only once it's settled. A new push dismisses an earlier approval, so the approved code is the code that merges.
- **Merging into `develop`** is squash or rebase only, which keeps the history linear. Your pull request title becomes the commit message, so make it a good one.
- **Merging into `main`** happens only through a `develop` → `main` promotion pull request, titled `release: vX.Y.Z …`, merged with a merge commit by the maintainer.
- `main` and `develop` can't be force-pushed or deleted, and only the maintainer can create release tags (`v*`).
- After a merge, delete your branch. The issue closes itself through `Closes #N`.

The rules are kept as code in [`.github/governance/rulesets/`](.github/governance/rulesets/) and explained in [`.github/PROTECTION_GUIDE.md`](.github/PROTECTION_GUIDE.md).

## Releases

Versions follow [SemVer](https://semver.org/): a new capability is a minor release, a fix-only release is a patch, and a breaking change to a public contract (the API, CLI flags or the provider schema) is a major one. The maintainer promotes `develop` to `main`, tags `main` with `vX.Y.Z`, and moves the `## [Unreleased]` entries in `CHANGELOG.md` under that version.

## Licence of contributions

EnclaveStack is licensed under [Apache-2.0](LICENSE). As section 5 of the licence says, anything you submit for inclusion is licensed under the same terms, with no additional conditions. Only submit work you have the right to license this way.

Thank you for contributing.
