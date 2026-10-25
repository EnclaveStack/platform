# L2-013 · Bucket objects and readable details PRD

---
| Field | Value |
|---|---|
| Document ID | L2-013-bucket-objects-and-readable-details-prd-v1 |
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

Two gaps the owner found on 2026-10-24 while using a bucket's page:

1. **A bucket's files can't be seen or changed from EnclaveStack.** Making a bucket works, but looking inside it, uploading a file (for example a video for a transcoder) or deleting one needs an S3 tool and the project's keys. The console, the CLI and OpenTofu each need to work with objects directly.
2. **A resource's details aren't readable.** The details on every page are tiles whose labels and values are cut or wrapped mid-word (`http://127.0.0.1:57` / `763`), and nothing says what "Credentials secret" or "Internal S3 endpoint" is for. Every detail must say what it is and how to use it, in the console and in the docs.

Builds on `L2-004` (resources), `L2-005` (buckets, ADR-012), `L2-012` (transcoder tags). Engineering: `L3-002` §14, `L3-006` §13, `L2.5-002` §19 – §20.

## 1 · The user stories

> Sam opens the bucket `media`, sees its folders and files with sizes and dates, opens `incoming/`, uploads `clip.mov` with the tag `video-id = v42`, and watches the transcoder convert it. Later Sam downloads a file to check it and deletes an old one.

> Sam reads a bucket's page and understands each line: the S3 endpoint is for code on this computer, the internal endpoint is for apps inside EnclaveStack, and the credentials secret holds the keys both use. Nothing is cut off, and each address can be copied with one click.

## 2 · Requirements — objects

| ID | Requirement | Priority |
|---|---|---|
| OBJ-01 | A bucket's objects can be **listed** one folder at a time (`/` separates folders): the folders in it, then its objects with key, size, last change and media type, in key order, at most 1000 per page with a cursor for the next | Must |
| OBJ-02 | An object can be **uploaded** to a key (up to 5 GiB), with **tags** (`x-amz-meta-<name>`, names in lowercase letters, digits and hyphens, values up to 256 characters, at most 10). An existing key is replaced. Its media type comes from the key's extension | Must |
| OBJ-03 | An object can be **downloaded** (its bytes, media type and tags) and **deleted**. Deleting a key that doesn't exist succeeds | Must |
| OBJ-04 | A key is 1 – 1024 bytes of UTF-8 without line breaks, doesn't start with `/`, has no empty, `.` or `..` part and doesn't end with `/` | Must |
| OBJ-05 | Objects are read and written by an editor or owner of the bucket's project (like message bodies, `L3-002` §9.3); each upload and delete is an event on the bucket naming the key and size, never the content | Must |
| OBJ-06 | The bucket must be ready; otherwise `409` saying so. Uploads and downloads stream: the control plane doesn't hold a file in memory, and a slow transfer isn't cut off by the API's usual time limits | Must |

## 3 · Requirements — readable details

| ID | Requirement | Priority |
|---|---|---|
| DET-01 | A resource's details are a list of label and value pairs: the label at its natural width, the value on the same line, wrapping only at sensible places (after `/`, `:` or `.`), never cut | Must |
| DET-02 | Every detail of every kind has a one-line explanation of what it is and what to do with it, shown with it | Must |
| DET-03 | Addresses, names of secrets and keys can be copied with one click; values that name another resource (a secret, a bucket, a network) link to it | Must |
| DET-04 | The user guide (`L5-004`) has a reference of every detail of every kind with the same explanations | Must |
| DET-05 | The list is a component of the design system (`@enclavestack/ui`, ADR-014), used by every page that shows details | Must |

## 4 · On each surface

| ID | Surface | Requirement | Priority |
|---|---|---|---|
| CON-74 | Console | A bucket's page has an **Objects** tab: a folder path with links back up, a table of folders and objects (name, size, last change; download and delete per object), **Upload** (one or more files into the current folder, with optional tags), **New folder** (a name that becomes part of the next upload's key), and an empty state that says how to add files | Must |
| CON-75 | Console | Every resource page shows its details as DET-01 – DET-03 describe | Must |
| CLI-55 | CLI | `enclave bucket objects NAME [FOLDER]` (`--recursive` lists every key below), `enclave bucket upload NAME FILE [KEY]` (`--tag NAME=VALUE`, repeatable; KEY defaults to the file's name; a KEY ending in `/` puts the file in that folder), `enclave bucket download NAME KEY [FILE]` (`-` for standard output), `enclave bucket delete-object NAME KEY --yes`. (`ls`, `get` and `rm` already name the bucket itself) | Must |
| TF-50 | OpenTofu | Resource `enclave_bucket_object`: `bucket`, `key`, one of `source` (a file path) or `content` (text), `tags`, and `source_hash` (any value; changing it uploads again, like `filesha256(...)`); computed `size`, `content_type`. Changing `bucket` or `key` replaces it; destroying it deletes the object; import as `<bucket>/<key>` | Must |

## 5 · Acceptance

- In the console on a real object store: upload two files into a new folder with a tag, see them listed with sizes, open the folder from its path, download one and compare bytes, delete one; the transcoder watching that folder converts the upload and its message carries the tag.
- The CLI and OpenTofu do the same; `tofu apply` twice in a row changes nothing.
- A viewer can't list or read objects (`403`). A key with `..` or a leading `/` is refused (`422`). A 5 GiB + 1 byte upload is refused (`413`).
- On every resource page, no label or value is cut; each detail has its explanation; the user guide lists them.

## 6 · Out of scope

- Object versions (the bucket's versioning keeps them; listing them is later), presigned URLs, bucket policies and lifecycle rules.
- Uploads larger than 5 GiB through the control plane (S3 clients upload any size directly).

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-24 | Soupler | Bucket objects OBJ-01 – OBJ-06, readable details DET-01 – DET-05; CON-74, CON-75; CLI-55; TF-50 |
