# L5-003 · Performance Baseline

---
| Field | Value |
|---|---|
| Document ID | L5-003-performance-baseline-v1 |
| Layer | L5 — Operations |
| Status | Draft |
| Version | v3 |
| Created | 2026-10-20 |
| Last Updated | 2026-10-22 |
| Owner | Soupler Labs |
| Reviewers | Soupler Labs |
| Copyright | © 2026 Soupler Labs |
| License | Apache-2.0 |
---

## Purpose

The control plane's performance targets (`L1-001` §5), how they are measured, and the latest measured baseline. Re-run and update after any change to the HTTP stack, the store or the reconciler.

## 1 · Targets

| Scenario | Load | Target |
|---|---|---|
| Reads: list resources, get resource, get machine | 50 virtual users, 60 s | p95 < 100 ms, error rate < 0.1 % |
| Writes: create + delete secrets | 10 virtual users, 60 s | p95 < 300 ms, error rate < 0.1 % |
| Reconcile throughput (fake driver, zero step latency) | 200 resources created at once | All `ready` within 30 s |
| Idle footprint | No load, 50 resources | RSS < 80 MB |

## 2 · Method

`enclave server --driver fake --data-dir <tmp>` on the developer machine; `k6 run tests/load/api.js` (scenarios `reads`, `writes`, thresholds encoded in the script so k6 exits non-zero on a miss). Throughput: `go test -run TestReconcileThroughput ./internal/reconciler`. Footprint: `ps -o rss` after the throughput test.

**In CI** the Load Test job runs the same script and thresholds at 10 read and 2 write virtual users for 30 s (`READ_VUS`, `WRITE_VUS`, `DURATION`). The scenarios send requests back to back with no pause, so at a fixed number of users p95 is set by how many requests per second the machine can serve: a shared GitHub runner served about 850 requests/s against about 6,960 here, and at 50 + 10 users its read p95 swung between 96 ms and 126 ms on the same commit. A fifth of the users keeps the job a regression check (a server several times slower still fails it) without the result depending on the runner it lands on. The full-load targets in §1 are measured on a developer machine and recorded in §3.

## 3 · Baseline

Run 2026-10-20 on an Apple M4 (10 cores, 16 GB), macOS, Go 1.27.3, k6 2.3.0, fake engine, 60 s.

| Scenario | Target | Measured | Result |
|---|---|---|---|
| Reads (50 VUs): list 40 %, get 50 %, machine 10 % | p95 < 100 ms, errors < 0.1 % | p95 **17.7 ms** (p99 25.4 ms), 0 errors, 372,356 requests | Met |
| Writes (10 VUs): create + delete secrets | p95 < 300 ms, errors < 0.1 % | p95 **29.1 ms** (p99 56.4 ms), 0 errors, 46,306 requests | Met |
| Overall throughput | — | ~6,960 requests/s | — |
| Reconcile throughput: 200 resources at once | all ready < 30 s | **186–201 ms** | Met |
| Idle footprint, 50 resources | < 80 MB | physical footprint **12.1 MB** (Go heap in use 4.9 MB) | Met |

Notes: macOS `ps` RSS read 90 MB because it still counts pages Go has returned to the OS; the physical footprint (`vmmap --summary`) is the real charge (L3-007 §7). Before the lazy timing-hash fix every CLI command peaked at 89 MB; after it, 22 MB. No server error was logged during the run.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-20 | Soupler Labs | Targets and method |
| v2 | 2026-10-20 | Soupler Labs | First measured baseline (Apple M4, 2026-10-20): every target met |
| v3 | 2026-10-22 | Soupler Labs | §2: CI runs the thresholds at 10 + 2 virtual users; at 50 + 10 a shared runner passed or failed the read p95 by chance |
