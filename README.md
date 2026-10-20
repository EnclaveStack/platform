# homecloud-platform

Homecloud monorepo: Go control plane, machine agent and CLI, plus the TypeScript web console.

Part of **Homecloud**. Role: Product codebase (apps, services, shared packages).

## Governance

This repository follows Homecloud's documentation-governance system. Read `CLAUDE.md` first — it is the contract for both humans and coding agents.

```bash
bash bin/install-hooks.sh          # once per clone: commit-msg + pre-commit hooks
bash bin/statutory-integrity.sh    # the gate CI runs on every PR
```

Standards live in `docs/L0-foundation/`. Start with `L0-006-change-propagation-governance-v1.md` (what to edit, in what order).

## Contributing

Branch from `develop` (`<type>/<slug>`), open the PR against `develop`, fill in every section of the PR template (including `## Docs update`). Only the `develop` → `main` promotion targets `main`.
