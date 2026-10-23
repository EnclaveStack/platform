# L0-003 · Error Message & API Error Standards

---
| Field | Value |
|---|---|
| Document ID | L0-003-error-message-api-standards-v1 |
| Layer | L0 — Foundation Standards |
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

One error shape and one tone for every API and user-facing surface, so clients handle failure generically and users never see internals.

## 1 · API error envelope

```json
{ "error": { "code": "RESOURCE_NOT_FOUND", "message": "We couldn't find that itinerary.", "requestId": "req_8f3c…", "details": [] } }
```

| Field | Rule |
|---|---|
| `code` | Stable `UPPER_SNAKE` machine identifier. Clients branch on this, never on `message`. Adding a code is an API change (L3 api-contracts). |
| `message` | Human-readable, safe to display, no stack traces, SQL, hostnames or internal IDs. |
| `requestId` | Always present; the same ID is in server logs. |
| `details` | Optional field-level list for validation errors: `{ "field", "code", "message" }`. |

## 2 · Status codes

`400` malformed · `401` unauthenticated · `403` authenticated but not allowed · `404` not found (also used to avoid confirming existence of resources the caller may not see) · `409` state conflict · `422` valid shape, invalid semantics · `429` rate limited (with `Retry-After`) · `5xx` our fault — never carries internal detail.

## 3 · Message writing

- Say what happened and what to do next, in plain language, no blame ("Something went wrong on our side. Try again in a minute.").
- Never leak whether an account exists in auth errors.
- Validation messages name the field and the rule, not the library.

## 4 · Rules

1. Errors are defined **once** in the shared validators/contracts package and imported by every service and client — never re-declared.
2. A change to the envelope or to code semantics follows L0-006 as an engineering change: L3 first, then implementations.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | EnclaveStack | Initial release from soupler-governance-template v1.2.1 |
