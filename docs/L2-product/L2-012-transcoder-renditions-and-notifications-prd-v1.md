# L2-012 · Transcoder renditions, notifications and tags PRD

---
| Field | Value |
|---|---|
| Document ID | L2-012-transcoder-renditions-and-notifications-prd-v1 |
| Layer | L2 — Product |
| Status | Draft |
| Version | v2 |
| Created | 2026-10-24 |
| Last Updated | 2026-10-24 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

A transcoder (`L2-008` TRN-*) converts media dropped in a bucket folder with fixed presets. That is enough to try it, not enough to run a video product on. Running Galiaraa's video service on EnclaveStack (it uses AWS Elemental MediaConvert today) showed what is missing:

1. **Its own outputs**: an adaptive HLS ladder (several qualities, portrait 9:16, set bitrates, keyframes every 2 seconds so players switch quality smoothly) with a master playlist, and single MP4 files of a chosen size.
2. **A "done" message**: the app learns that a video finished, or failed, and where the outputs are, instead of polling the bucket.
3. **Its own details carried through**: the app tags an upload (its video ID) and finds the tag in the message, and can choose where the outputs go from it.

This PRD adds those three to the existing transcoder, without copying MediaConvert's API: a transcoder stays a folder that converts what lands in it.

Builds on `L2-008` TRN-01 – TRN-08 (unchanged, except where TRX-01 and TRX-06 widen TRN-02 and TRN-06), topics and signed webhooks (`L2-008` TOP-*), secrets, and `L2-004` (lifecycle, CP-12). Engineering: `L3-002` §13, `L3-003` ADR-050, `L3-006` §12.

Product decisions taken with the owner on 2026-10-24:
- **Enhance EnclaveStack's own transcoder** rather than emulate the MediaConvert API, which is large and changes with AWS. An app moving from MediaConvert writes its upload to the transcoder's folder and listens for the "done" message.
- **Complete on every surface** (API, console, CLI, OpenTofu), with docs and tests at every level, including a real FFmpeg run.

## 1 · The user story

> Galiaraa's users upload portrait videos. The video service writes each upload to `media/incoming/` with the tag `video-id` set to its ID. A transcoder `videos` turns each into an HLS ladder (360p, 480p, 720p and 1080p, 2-second keyframes, 6-second segments) under `media/processed/<video-id>/hls/`, with `master.m3u8` the player loads. When a video is done, EnclaveStack calls the video service's webhook, signed with a shared secret, saying `succeeded`, the master playlist's key and `video-id`. A second transcoder, `selfies`, turns identity-check selfies into one private 720×1280 MP4 the same way.

## 2 · Requirements

| ID | Requirement | Priority |
|---|---|---|
| TRX-01 | A transcoder has any of: **presets** (TRN-02), **renditions** and an **HLS ladder**; at least one of the three | Must |
| TRX-02 | A **rendition** is one file: a `name` (lowercase letters, digits and hyphens, up to 32), a `container` (`mp4`: H.264 and AAC; `webm`: VP9 and Opus), a box it fits in (`maxWidth` × `maxHeight`, 16 – 7680, even), a video bitrate ceiling (`videoKbps`, 100 – 50 000), an audio bitrate (`audioKbps`, 32 – 512, default 128) and a keyframe interval (`gopSeconds`, 1 – 10, default 2). Video keeps its aspect ratio, fits inside the box and is never scaled up. Up to 8 renditions | Must |
| TRX-03 | An **HLS ladder** is one adaptive stream: a `name` (as a rendition's; default `hls`), `segmentSeconds` (2 – 10, default 6, a whole number of `gopSeconds`), `gopSeconds` (1 – 10, default 2), `audioKbps` (32 – 512, default 128) and 1 – 6 **rungs**, each a `name`, a box and a `videoKbps` as a rendition's. Every rung has keyframes at the same moments (closed GOPs at `gopSeconds`), so segments line up and a player switches rung at any segment. The ladder writes `<name>/master.m3u8` listing each rung with its bandwidth and actual resolution, and `<name>/<rung>/index.m3u8` with its segments | Must |
| TRX-04 | Names don't collide: renditions, the ladder and presets each write different files (`422` naming the clash) | Must |
| TRX-05 | **Notify**: a transcoder may name a **webhook** (an `https://` URL, or `http://` to `localhost`, and optionally a secret that signs it, as topic webhooks, TOP-03) and/or a **topic** of its project. When a run ends, succeeded, failed or timed out, EnclaveStack sends one JSON message: the transcoder, the run ID, the status, the message, the input (bucket, key, size), every output written (bucket, key, size), the HLS master playlist's key when there is one, the input's tags (TRX-06), and the start and end times | Must |
| TRX-06 | **Tags**: the metadata the uploader set on the input object (`x-amz-meta-<name>`; names in lowercase letters, digits and hyphens) is carried into the message's `metadata`, and onto every output object, so the outputs say what they came from | Must |
| TRX-07 | **Where outputs go**: the output folder may use `{name}` (the input's file name without folder or extension) and `{meta:<tag>}` (a tag of the input, TRX-06). With either, outputs are written straight under the folder it gives; without, they go under `<output folder>/<input name>/` as before (TRN-06). A run whose input lacks a tag the folder needs fails, saying which | Must |
| TRX-08 | A webhook is tried 3 times (at once, after 2 and after 8 seconds). A message that can't be delivered doesn't change the run's status; it is recorded as an event on the transcoder ("The webhook … failed after 3 tries: …"). A topic message is published once, like any publish | Must |
| TRX-09 | A secret or topic a transcoder names can't be deleted while it does (CP-12); each must exist in its project when set (`422`) | Must |
| TRX-10 | The transcoder's page, `get` and OpenTofu show its renditions, ladder and notify settings; each run shows its outputs (TRN-05) | Must |

## 3 · On each surface

| ID | Surface | Requirement | Priority |
|---|---|---|---|
| CON-72 | Console | The transcoder form has **Renditions** (rows: name, MP4/WebM, width × height, video kbps; advanced: audio kbps, keyframe seconds), an **HLS ladder** (on/off; name, segment and keyframe seconds, audio kbps; rows of rungs) and **Notify when a file is done** (webhook URL, signing secret, topic). Presets become optional | Must |
| CON-73 | Console | The transcoder's page shows a card per output kind (presets, renditions, ladder with each rung) and a **Notify** card; a run lists the files it wrote, the master playlist first | Must |
| CLI-54 | CLI | `transcoder create` and `update` take `--rendition NAME:CONTAINER:WxH:KBPS[:AUDIO_KBPS[:GOP_SECONDS]]` (repeatable), `--hls-rung NAME:WxH:KBPS` (repeatable) with `--hls-name`, `--hls-segment`, `--hls-gop`, `--hls-audio`, and `--notify-webhook URL`, `--notify-secret SECRET`, `--notify-topic TOPIC`; `--no-hls` removes the ladder; on update a repeated flag replaces its list, `""` clears it (`--preset`, `--rendition`, `--notify-webhook`, `--notify-topic`), and a ladder or notify setting not given is kept | Must |
| TF-49 | OpenTofu | `enclave_transcoder` takes `renditions = [...]`, `hls = { ..., rungs = [...] }` and `notify = { webhook_url, webhook_secret, topic }`; `presets` becomes optional | Must |

## 4 · The message

```json
{
  "event": "transcode.finished",
  "transcoder": "videos",
  "project": "default",
  "runId": "run_k3x…",
  "status": "succeeded",
  "message": "Converted incoming/v42.mov into 13 files.",
  "input": { "bucket": "media", "key": "incoming/v42.mov", "size": 48211337 },
  "outputs": [ { "bucket": "media", "key": "processed/v42/hls/master.m3u8", "size": 412 }, "…" ],
  "hlsMaster": "processed/v42/hls/master.m3u8",
  "metadata": { "video-id": "v42" },
  "startedAt": "2026-10-24T10:00:00Z",
  "finishedAt": "2026-10-24T10:01:12Z"
}
```

The webhook request carries `Content-Type: application/json`, `X-Enclave-Event: transcode.finished`, `X-Enclave-Transcoder`, `X-Enclave-Run-Id` and, with a secret, `X-Enclave-Signature: sha256=<HMAC-SHA256 of the body>`. A topic message has the same body and the attributes `event` and `status`.

## 5 · Acceptance

- With the real FFmpeg image: a portrait clip becomes a 4-rung ladder whose master playlist lists each rung with its bandwidth and real resolution, whose rungs start every segment on a keyframe at the same time, and a 720×1280 MP4 rendition; a landscape clip fits its box the other way; nothing is scaled up.
- Tags on the input reach the outputs and the message; `{meta:video-id}` puts the outputs in that folder; a missing tag fails the run saying which.
- A signed webhook arrives once per run with the right status, outputs and signature; a failing webhook is retried and then recorded; a topic gets the same message.
- The console, the CLI and OpenTofu each create, change and show renditions, the ladder and notify.

## 6 · Out of scope

- Captions, DRM, watermarks, audio-only ladders, DASH, and per-title or two-pass encoding.
- Copying MediaConvert's API or job documents.
- Thumbnails at chosen times (the `thumbnail` preset stays).

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-24 | Soupler | Renditions, HLS ladder, notify (webhook and topic), tags and output folders from tags: TRX-01 – TRX-10; CON-72, CON-73; CLI-54; TF-49 |
| v2 | 2026-10-24 | Soupler | CLI-54: optional audio and keyframe fields on --rendition, --no-hls, and how updates keep or clear settings |
