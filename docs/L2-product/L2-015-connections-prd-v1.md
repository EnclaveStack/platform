# L2-015 · Connections PRD

---
| Field | Value |
|---|---|
| Document ID | L2-015-connections-prd-v1 |
| Layer | L2 — Product |
| Status | Draft |
| Version | v1 |
| Created | 2026-10-25 |
| Last Updated | 2026-10-25 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

Apps on EnclaveStack often use a service that runs somewhere else, such as a hosted Supabase project. Today the user copies its address and keys from the service's dashboard, makes one secret per key by hand, and then attaches each one to an app. Nothing groups them or shows which keys belong together.

This PRD adds **connections**: a catalogue category whose entries ask for an outside service's address and keys, and store them as secrets of one stack, ready to attach to any app. The first entry is **Supabase**. A connection runs nothing, so it costs no memory.

Builds on:
- `L2-010` the catalogue and stacks (CAT-01 – CAT-08);
- `L2-005` secrets and `secretEnv` on apps;
- `L2-014` catalogue categories and sign-up links (TUN-15).

Engineering: `L3-002` §17. Design: `L2.5-002` §22. Tests: `L5-001` §2i. User guide: `L5-004` §16.

Product decisions taken with the owner on 2026-10-25:

- **The hosted service, not a copy of it.** The user wants to use their live Supabase project, not run Supabase on EnclaveStack.
- **A catalogue entry, not a new resource kind.** A connection is a stack of secrets, so the console, CLI and OpenTofu install, update and delete it the way they already do with other entries.

## 1 · User story

> A team keeps its data in a hosted Supabase project. They open **Catalogue › Connections › Supabase**, name it `supabase`, and paste the project's address, its `anon` key and its `service_role` key from the Supabase dashboard. The stack `supabase` holds three secrets. Its page lists which variable each one is for (`SUPABASE_URL` → `supabase-url`, and so on), and they attach them to the app `api` as secret environment variables. When they rotate the `service_role` key, they paste the new one into the stack and restart `api`.

## 2 · Requirements

| ID | Requirement | Priority |
|---|---|---|
| CNX-01 | The catalogue has a **Connections** category (`connections`): outside services that apps use. A connection stores settings as secrets and runs nothing | Must |
| CNX-02 | A built-in **Supabase** entry, with a sign-up link to supabase.com. It asks for the project's address (a host name like `abcd.supabase.co`, required), the `anon` key (required), the `service_role` key (optional) and the database connection string (optional). Each setting says where to find it in the Supabase dashboard | Must |
| CNX-03 | Installing it makes the secret `<stack>-url` (`https://<project address>`) and one secret per key given: `<stack>-anonkey`, `<stack>-servicekey`, `<stack>-dburl`. Key values are never returned, logged or shown, as for every secret | Must |
| CNX-04 | The stack's outputs map each variable an app reads to the secret that holds it: `SUPABASE_URL`, `SUPABASE_ANON_KEY`, and `SUPABASE_SERVICE_ROLE_KEY` and `DATABASE_URL` only when those keys were given | Must |
| CNX-05 | In any template, an output that uses a `secret` parameter left empty is left out, so outputs never name a secret that doesn't exist | Must |
| CNX-06 | Updating the stack with a new key replaces that secret's value, and a key not given again stays as it was (CAT-06). Deleting the stack deletes its secrets; an app that still uses one blocks the delete, as for any secret in use | Must |
| CNX-07 | Templates of one's own can use `category: connections` | Should |

## 3 · Surfaces

| ID | Surface | Requirement | Priority |
|---|---|---|---|
| CON-81 | Console | The catalogue's **Connections** group with the Supabase card (Sign up, Install). Installing it opens the stack form, and the stack's page shows its outputs (CNX-04) | Must |
| CLI | CLI | No new commands: `enclave catalogue ls` shows the category, `enclave stack create supabase --template supabase --param project=… --secret-param anonKey=…` installs it, and `enclave stack get` prints the outputs (CLI-50, CLI-52) | Must |
| TF | OpenTofu | No new resources: `enclave_stack` with `template = "supabase"`, `parameters` and `secret_parameters`, and `enclave_catalogue` lists the category (TF-44, TF-45) | Must |

## 4 · Out of scope

- Checking that the address and keys work: EnclaveStack doesn't call Supabase.
- Attaching every secret of a connection to an app in one step.
- Other services' entries. Templates of one's own cover them until each gets a built-in entry.
- Running Supabase itself on EnclaveStack (its own Postgres image, config files and signed keys).

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-25 | Soupler | Connections: the catalogue category, the Supabase entry, outputs that skip empty secret parameters (CNX-01 – CNX-07); console CON-81 |
