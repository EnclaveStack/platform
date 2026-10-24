# L2-011 · Per-app bucket access PRD

---
| Field | Value |
|---|---|
| Document ID | L2-011-per-app-bucket-access-prd-v1 |
| Layer | L2 — Product |
| Status | Draft |
| Version | v1 |
| Created | 2026-10-24 |
| Last Updated | 2026-10-24 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

Today every bucket in a project is reached with one shared key (`objectstore-credentials`): any app given that key can read and write every bucket in the project. AWS answers "which app may touch which data" with IAM roles for each service (an ECS task role that may read one bucket). This PRD gives EnclaveStack the useful core of that, without IAM's policy language: **an app or a job says which buckets it needs and whether it reads them or also writes them, and gets keys of its own that reach exactly that.**

It came from running a real product (Galiaraa, `L5-004` context) on EnclaveStack: its video service may write the media bucket and nothing else; its user service may only read it. With one key per project that can't be expressed.

Builds on `L2-005` (apps, jobs), `L2-004` (lifecycle and `RESOURCE_IN_USE`), `L2-009` (projects and the shared key, ADR-035), `L2-010` (machines). Engineering: `L3-002` §12, `L3-003` ADR-049, `L3-006` §11.

Product decisions taken with the owner on 2026-10-24:
- **Build the useful core of IAM, not IAM.** Two levels per bucket (read; read and write), on apps and jobs. No policy language, no conditions, no roles to assume.
- **Complete on every surface** before it ships: API, console, CLI, OpenTofu, docs and tests at every level (the owner asked for no partial features).
- **Secrets need nothing new.** An app already receives only the secrets named in its own `secretEnv`; it has no way to read any other. That is the per-app rule IAM would give for Secrets Manager.

## 1 · The user story

> The shop keeps product photos in the bucket `media` and invoices in the bucket `invoices`. The `uploader` app writes photos; the `storefront` app only shows them; the `billing` job writes invoices. They give `uploader` read-and-write on `media`, `storefront` read on `media`, and `billing` read-and-write on `invoices`. Each gets its own keys in the usual AWS environment variables, so their S3 code needs no change. When someone tries `storefront`'s keys on `invoices`, the object store says no.

## 2 · Requirements

| ID | Requirement | Priority |
|---|---|---|
| BKA-01 | An **app** or a **job** has **bucket access**: a list of up to 10 entries, each a bucket in the resource's own project and a level, `read` (list buckets' objects and read them) or `readwrite` (also write and delete objects). A bucket appears once. The level is always given; there is no default | Must |
| BKA-02 | A resource with bucket access gets an **object store user of its own** whose policy allows exactly those buckets at those levels: no other bucket, no bucket creation or deletion, no administration. It is made or changed before the app's new revision starts, or before a job's run starts | Must |
| BKA-03 | Its **keys** are made once, when access is first given, and kept in a managed secret named `<resource>-bucket-credentials`, owned by the resource, holding `{"accessKey", "secretKey"}`. Changing the list changes the policy, not the keys. The keys never appear in responses, events, logs or the console except through `secret get --reveal` (audited), like any secret | Must |
| BKA-04 | The app or job receives them as **standard environment variables**, so AWS SDKs and S3 tools work unchanged: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION` (`us-east-1`), `AWS_ENDPOINT_URL_S3` and `AWS_ENDPOINT_URL` (the object store's private address, `http://es-system-objectstore:9000`). Clients must use path-style addresses (`forcePathStyle` in the AWS SDKs). A resource with bucket access can't set any of these five names in `env` or `secretEnv` (`422`), so there is one source for each | Must |
| BKA-05 | A **job that runs as an app** (`app` set) uses that app's bucket access, as it uses the app's environment; it can't have bucket access of its own (`422`). A job from an image has its own | Must |
| BKA-06 | **Removing** every entry, or deleting the resource, removes its user and policy from the object store and deletes its credentials secret | Must |
| BKA-07 | A bucket in a resource's bucket access must exist in its project and not be being deleted (`422 NOT_FOUND` on the entry). **Deleting a bucket** that an app or job can access is refused (`409 RESOURCE_IN_USE`, naming them), as for any other reference (CP-12) | Must |
| BKA-08 | The resource's page and `get` show its bucket access (each bucket and its level), the credentials secret's name and the five variable names. An event records each change ("Bucket access: media read and write, invoices read") | Must |
| BKA-09 | It works wherever the resource runs: an app on another machine reaches the object store over the private network (`L2-010` XNET) with the same variables | Must |
| BKA-10 | Nothing changes for resources without bucket access: the project's shared `objectstore-credentials` stays, for apps that already use it and for EnclaveStack's own services (backups, transcoders, CDNs) | Must |

## 3 · On each surface

| ID | Surface | Requirement | Priority |
|---|---|---|---|
| CON-70 | Console | The app form and the job form (from an image) have **Bucket access**: rows of a bucket (picked from the project's buckets) and a level (**Read** / **Read and write**), with a hint naming the variables the app receives. A job that runs as an app says it uses the app's bucket access | Must |
| CON-71 | Console | An app's or job's page shows a **Bucket access** card: each bucket with its level, a link to the credentials secret and the five variable names | Must |
| CLI-53 | CLI | `app create`, `app update`, `job create` and `job update` take `--bucket NAME:read` or `--bucket NAME:readwrite` (repeatable); on update the list replaces the old one and `--bucket ""` removes all. `get` shows the access | Must |
| TF-48 | OpenTofu | `enclave_app` and `enclave_job` take `bucket_access = [{ bucket = "media", access = "read" }]`; `bucket_credentials_secret` is computed | Must |

## 4 · Acceptance

- With the real Docker engine and the real object store: an app with `read` on `media` lists and reads it and is refused writing to it; an app with `readwrite` on `media` writes and deletes in it; neither can list or read `invoices`; the project's shared key still reaches both.
- Changing an app's access from `read` to `readwrite` lets it write after its next revision starts, with the same keys.
- Removing the access, or deleting the app, removes its object store user: its old keys are refused.
- A job from an image with access reads its bucket; a job as an app uses the app's.
- Deleting a bucket an app can access is refused, naming the app.
- The console, CLI and OpenTofu each create, change and remove bucket access, and each shows it.

## 5 · Out of scope

- Access to anything other than buckets (databases have their own passwords; secrets are already per app).
- Prefix-level access inside a bucket, conditions, time limits and policy documents.
- Access across projects.
- Keys for people or scripts outside EnclaveStack (the shared project key and API tokens cover those).

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-24 | Soupler | Per-app bucket access: BKA-01 – BKA-10; console CON-70, CON-71; CLI-53; TF-48 |
