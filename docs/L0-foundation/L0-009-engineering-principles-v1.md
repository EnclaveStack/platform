# L0-009 · Engineering Principles

---
| Field | Value |
|---|---|
| Document ID | L0-009-engineering-principles-v1 |
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

The architecture and code principles every contributor — human or coding agent — follows when building. `CLAUDE.md` summarises these; this document is the full text and carries the reasoning. Each rule is here because skipping it has a known failure mode.

## 1 · Modular by boundary

- A service or package **owns its data and exposes a contract**. No module imports another's internals, schema or tables. Cross-service interaction goes through the contract (HTTP, events) — record the boundary rule as an ADR.
- Dependencies point one way: apps → services/clients → shared packages. A shared package never imports from an app.
- Prefer many small, single-purpose modules over one that knows everything. If a file needs a "misc" section, it needs a new module.

## 2 · Never duplicate code

- Logic used (or likely to be used) in more than one place goes into a **shared package**. "No shared helper exists" means *create one*, not copy.
- Before adding a component, hook, constant, validator or type: **search for an existing one**. Extend it if it fits; justify in the PR if you do not.
- Validators, error codes, event envelopes and API types are defined once (shared validators/contracts package) and imported by producers and consumers alike.
- Duplication across *repositories* is the same sin: consume the owner as a versioned package, or deliberately sync and record the sync (see §3).

## 3 · One source of truth per fact — the SSoT chain

Every fact (an enum, a colour, a legal clause, a limit) has exactly one owner; everything else derives from it or is tested against it.

- **Database constraint ↔ validator ↔ UI list.** The validator schema is the SSoT; the migration syncs the DB to it; UI options are a **strict subset** of the allowed values. Before writing a UI constant that maps to a constrained column: read the DB `CHECK`, read the schema, read the endpoint that populates it, and confirm the field is actually sent. *(Failure mode: a UI invented values the DB rejected — every save broke.)*
- **Migrations that rewrite a constraint**: copy the current constraint **verbatim** from the migrations, apply only the intended delta, cross-check against the validator, and add/remove the value in the validator *first*. Never reconstruct a list from memory. *(Failure mode: a rewritten constraint dropped most allowed values and every existing row violated it.)*
- **Content published in two places** (legal text, pricing, brand marks): one repository owns it; the other follows via a versioned package or a recorded sync log with SYNCED / OUTSTANDING status. Silence is the failure; disagreement that is logged is acceptable.

## 4 · Centralised design tokens and shared UI

- Colours, spacing, typography, radii, shadows, durations and sizes come from the app's token/theme module — **no inline literals**. A guard test (AST-based, red-teamed by planting a violation) is the right way to hold this line.
- "Design system" means the **centralised theme and shared components inside the app**, not a prototype repository. Read the app's existing components and conventions before creating anything new; prototype/reference repos inform, they do not override the live theme. If two sources disagree (e.g. an accent colour), ask which is authoritative — do not pick.

## 5 · Shipped clients never fall back to localhost

For any bundled client (mobile above all, and any plugin/SDK it embeds):

1. No `process.env.X ?? 'http://127.0.0.1:…'` at a call site. Loopback is reachable only behind a dev flag.
2. **One resolver per app** is the only module that reads environment variables; non-dev fallbacks are real infrastructure hosts. An architecture test fails the build if a call site reintroduces the pattern.
3. Every bundling entry point (build *and* over-the-air update) supplies the environment explicitly and **refuses to publish** a non-development channel if a value is missing or local.
4. When adding an SDK that takes a base URL, configure it from the build profile — never accept its default.

*(Failure mode: an OTA update bundled with no environment told every phone to connect to itself. Server dashboards stayed green because the traffic never left the device — assume nothing server-side can detect this class; gate at publish time.)*

## 6 · Verify before asserting

Do not claim a feature is restricted, gated, or behaves a certain way by analogy to a similar feature — read the actual handler. Do not act on an unverified finding; verify it is true first, then fix it.

## 7 · Fix now, don't defer

Real findings are implemented in the same pass. No "watch later" or "housekeeping" buckets. If a finding is genuinely out of scope, it gets a tracked item with an owner — not a mental note.

## 8 · Testing

- Integration tests hit real boundaries (DB, HTTP, bus) with no mocks at that level; unit tests cover pure logic, validators, transformers.
- Every change has a test that **fails without it**. Trace the whole flow and test consumers, not just producers.
- Integration tests that truncate tables run against a scratch database — never a shared dev or production-like one, and never against infrastructure other developers are using (shared caches/queues race the suite).
- Never skip, comment out or mark pending without a stated reason.

## 9 · Completing a change (self-review)

- For every **removal or rename**: grep the whole codebase for the symbol; every hit outside a migration down-path or an absence assertion is a gap.
- For every behaviour **added to a shared component**: grep **every consumer** and either apply it or exclude it with a stated reason. Passing tests prove the code that exists is correct, never that every place needing it was found.
- For every **new endpoint**: caller uses it, gateway routes it, auth is applied, response shape matches the caller.
- For every **schema change**: all queries, types and both migration directions.
- For every **event change**: producer, all consumers, registry validation.
- Loop until a full pass finds nothing.

## 10 · Operational safety

- Never run session-level `SET` against a pooled/shared database connection — it leaks onto other clients. Use a transaction-scoped read-only transaction for read-only inspection.
- Destructive or outward-facing actions (deleting data, force operations, publishing) require explicit confirmation and a look at the target first.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Initial release from soupler-governance-template v1.2.1 |
