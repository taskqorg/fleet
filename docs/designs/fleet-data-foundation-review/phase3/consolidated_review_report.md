# Consolidated review: Phase 3 artifact design

Target: `docs/designs/fleet-data-foundation.md` §3, §8 (artifact tools) and Phase 3 items, main cb36446. Stages 5-9 of the EGM Goldfish review ran with fresh-context reviewers (zoom-out, inventive-catalyst, comprehension, design, structural, style, implementation-readiness, codebase-details). Context given to every reviewer: `live-findings.md` and `docs/infrastructure-build.md` (cross-host store proof). Stage reports are in this folder.

## Applied to the design (spec first)

| Priority | Change | Sources |
|---|---|---|
| 🔴 | Loopback `local_endpoint` for the store's own host; tunnel for control plane and cross-host reads; CUJ 5 holds | ZO-1, IC-1, Struct-3, style |
| 🔴 | Agents never get store URLs or the Access token: `artifact_save` plus `FLEET_OUTPUT_DIR`/`FLEET_INPUT_DIR`; reserve/parts/commit are executor routes | ZO-2, Design-10/13, Stage 8 KEEP 2 |
| 🔴 | Staging key and promote-if-absent: a verified object is never overwritten; dedupe at reserve | Design-1 |
| 🔴 | Whole-file hash verification for multipart defined (P3-D1) | ZO-4, IC-4, Comp-3 |
| 🔴 | Store registration through the outbox; keys in the coordinator's secret directory; executors hold no S3 keys; static store file dropped | ZO-5, IC-2, Design-11, live 3 |
| 🔴 | Store protocol rules: Access headers after signing and never signed, tunnel only; `WHEN_REQUIRED` both ways | ZO-3, IC-3, Struct-3, live 1 and 5 |
| 🔴 | Leases, on-demand part URLs (15 min), abort of expired uploads, `pending -> stored | failed` | Design-2, Design-6 |
| 🔴 | Every base64 reader and writer listed with its replacement (`artifacts.read`), backfill item, migration 8, legacy adapter for `all('artifact')` | Stage 9 |
| 🟡 | Effective capacity (min of Garage and backing disk), `requires_logon`, `full` defined | ZO-6, Struct-4, live 4 |
| 🟡 | Success rule hooked where states are written; `waiting` with reason `artifacts` after the agent exits (P3-D2); required declared by the task (P3-D3) | Design-7, Struct-7, Stage 9 |
| 🟡 | `artifact.scope`; store derived from `artifact_copy`; `placed` computed; `legacy_id` keeps old links | Design-3, ZO-9, Struct-10, codebase |
| 🟡 | Delete safety (HEAD surviving copy, never the last); purge list and `purge_pending` | Design-4, Design-5, IC-7 |
| 🟡 | ffmpeg as the account user, no network, time and memory limits; capability via poll | Design-9, codebase |
| 🟡 | Single read rule in §3; §8 points to it; store selection by the coordinator | Struct-5, Struct-6 |
| 🟡 | Store calls outside write transactions; §12 rows for new routes | codebase-7 |
| 🟡 | Test item 30 with named cases and live acceptance for CUJ 3 and CUJ 5 | Stage 9 |
| 🟢 | Q2 marked answered; FD2, item 6 and Verification wording; 26b double Why; `nas` example; role `web` dropped; named settings and limits; Risks entries | Struct-2, ZO-12, Comp-1/9/11, style |

## Deferred (noted in the design)

Collection during long runs (CUJ 6 work); daily re-hash audit and `ListMultipartUploads` sweep (Risks); lab/prod location overrides; key rotation beyond re-registration; tool byte budget (Phase 6).

## Rejected

Staging copy on another host when the listed store is offline (breaks CUJ 5; uploads wait instead); executor read cache (conflicts with purge; downloads live in the working area); most-free-capacity store selection (no CUJ needs it).

## Open decisions for the owner

- **P3-D1, multipart hash verification:** (a) the store host's executor re-hashes the staging object over loopback (default); (b) per-part `x-amz-checksum-sha256` with a composite compare (needs a Garage 2.5.0 proof); (c) the coordinator downloads and hashes through the tunnel.
- **P3-D2, execution state while required uploads continue after the agent exits:** (a) `waiting` with reason `artifacts` (default); (b) stays `running`; (c) `succeeded` at once, with the artifact `failed` later and the task deciding.
- **P3-D3, where required artifacts are declared:** (a) the task's `completion_requirements.artifacts` only (default); (b) the agent's `required` flag on `artifact_save`; (c) both, the agent able only to add.
- **P3-D4, scope of Phase 3 for previews and retention rules:** (a) both in Phase 3 as ordered, previews sandboxed (default); (b) previews in, rules deferred; (c) both deferred.

## Traceability

Each row above names its source finding (ZO = zoom-out Stage 5, IC = inventive catalyst, Comp = comprehension, Design/Struct = Stage 7, Stage 8 = intermediate zoom-out KEEP list, Stage 9 = readiness and codebase).
