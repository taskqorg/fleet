# Stage 6: Comprehension Test — fleet-data-foundation.md

Reviewer: egm-comprehension (fresh context, design doc only). FeatureRange 1..583, DetailedRange 584..1125.
Counts: 🔴 0 · 🟡 5 · 🟢 6.

## What this proposes

The document proposes a data foundation for Fleet, a system that runs AI coding agents, tools and pipelines on the owner's Mac, Windows and Linux machines. Every run becomes one typed "execution" record in Postgres. Production work becomes a flat "task" table with dependencies, and a task's cost and progress are derived from its executions. Results ("artifacts") of any size go into an independent single-node S3 store (Garage) on each host. A placement worker in the coordinator copies them between stores to meet each project's `artifact_locations` list.

The executor on each host uses per-harness adapters (Codex, Claude Code, Antigravity) to read, normalize and redact harness output, then sends it through its existing outbox. Events, spans and measurements go to ClickHouse; raw transcripts go to the store as artifacts. Each measurement says whether it was measured, estimated or unavailable. Each harness account gets its own OS user; project keys are handed to each execution separately. People and agents query through the same permission-filtered coordinator operations. Migration goes SQLite → Postgres `document` table → typed tables, in eight phases with a SQLite rollback.

## How the existing system works, per this document

A central TypeScript coordinator keeps all state as JSON documents of about 30 kinds in one SQLite table `records(kind, id, data)`. Run-like concepts are split across six kinds; there is no task record. A per-host Rust executor runs harnesses and sends events in batches through a sequence-numbered outbox. Artifacts are base64 inside records, capped at 16 MB. Rate-limit events are dropped; CPU is measured only for the leader process. A run whose upload failed with a 4xx is still marked successful. All agents share one OS user, so any agent can read every project key. Under `gitNetwork` the shared `.git` is writable and network is fully open.

## Findings

- 🟡 **"tight" never defined** (D1, D3, D6, Q8, items 3/4/8, NFR Durability). tight's D-numbers clash with this doc's D1–D6 ("tight D4" in D3). Fix: define once, link, prefix as `tight-D4`.
- 🟡 **Core actors not introduced** (Overview, Problem, Data flow). "profile", "environment", "project grant" never defined; project grant underpins §6/G6. Fix: short glossary.
- 🟡 **"experiment"/"variant" undefined** (G1, §1, CUJ 2, A3, item 19). Lab vs production hinges on `experiment_id`; CUJ 2 promises per-variant totals and scores but item 16 schema has no variant or score column.
- 🟡 **Store reachability inconsistent** (item 6 line 660 vs CUJ 5 line 194, §3, item 25). Stores on loopback reachable only via executor, yet cross-host presigned links and coordinator-side `CopyObject` are promised. No end-to-end network path.
- 🟡 **AssetQ, Pipe, t3code, Orca unexplained** (Q8, A8d, G9, §5, §8, item 35). No links or one-line descriptions; "copied from t3code" not checkable.
- 🟢 Forward reference "see item 35" (Q5, line 146) does not say it is in the Detailed Implementation Plan.
- 🟢 Open Questions out of order (Q1, Q1b, Q2, Q9, Q8, Q4…); Q3 missing; answered ones mixed with open.
- 🟢 "agy" and "ACP" not expanded.
- 🟢 "PDS18" never introduced; default `["pds18"]` host ID conflicts with §1 example calling host `pds18` non-existent.
- 🟢 Unexplained references: commits `23c5418`, `d3802cc`; "no extra entities" rule (A11); "the brief" (item 47); "remaining 5%" (A14); "EGM handoff" (item 49).
- 🟢 Rollback window fuzzy: does `reverse` (extended in Phase 2) export typed rows, so "until Phase 2 completes" holds?
