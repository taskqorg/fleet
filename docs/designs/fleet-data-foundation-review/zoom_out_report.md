# Goldfish Review Report: Stage 5 (Initial Zoom Out)

**Target:** `docs/designs/fleet-data-foundation.md` (lines 1-1125, full file)
**Context:** `fleet-data-foundation-brief.md`, `fleet-data-foundation-goldfish-review.md`
**Owner constraints respected:** no concurrency or admission caps; harness quirks are recorded facts, not blockers; the owner controls all hosts; existing products are preferred over custom ones; infrastructure on PDS18 follows `~/Projects/tight/tight.md`.

## Reviewer Status

| Reviewer | Status |
| --- | --- |
| `egm-zoom-out` | completed |
| `egm-inventive-catalyst` | completed |

## M8 Check (delivery is task-like; two question systems)

Both reviewers agree that M8 is only partly resolved.

| M8 item | Status |
| --- | --- |
| Delivery is task-like | Partly. Delivery becomes an execution kind (line 246). It is not split into a task and a run (see Major 1). |
| Two question systems | Not resolved. Both are kept and each only gains a `task_id` (line 267, item 18). See Major 3. |
| Task state stored twice | Not resolved. The design stores it, which contradicts G2 (see Major 2). |
| The word "task" used for a lab case | Resolved (lines 170, 254). |
| "Production" defined | Resolved. Production means an execution with no `experiment_id` (line 254). |
| Accepting a result that has no task | Not addressed (see Major 4). |

## Prioritized Findings

No blockers.

### 1. A legacy delivery has no defined task half - Severity: Major
* **Reviewer Lens:** `egm-zoom-out`, `egm-inventive-catalyst`
* **Affected Scope:** line 246, lines 366, 754, 785-788, item 19
* **Discrepancy Description**: A delivery record carries a task's text, project, method and question. It also carries run state and steps. The design maps it to a single `delivery` execution, so the task fields end up in `execution.data jsonb`. Migration creates no task row. The design also never says whether starting a delivery creates a task.
* **Rationale & Recommendation**: Split each delivery at its natural seam:
  * The intent becomes a `task` row. Task text goes to `title` and `expected_outcome`.
  * The run becomes a `pipeline_run` execution with `pipeline_step` children. The method goes in `config_ref`.
  * Item 19 should say that "start delivery" now means "create a task, then launch a pipeline run".
  * The migration must map one record to several rows. The reverse script must rebuild deliveries from those rows.

### 2. Task state is stored even though G2 says it is derived - Severity: Major
* **Reviewer Lens:** both
* **Affected Scope:** line 57 (G2), lines 262-263, item 16 (line 756)
* **Discrepancy Description**: `task.state` stores `queued`, `in_progress` and `waiting`, and `wait_reason` is stored too. All of these can be computed from the task's executions and open questions. Storing them twice is the source of the state mismatch that M8 named.
* **Rationale & Recommendation**: Store only the operator's intent and the final outcome: `open`, `paused`, `cancelled`, `accepted`, `failed`. Compute the other states in the view that already derives progress and cost (line 267). If the 200 ms list target (line 573) needs speed, add a cache column that triggers keep up to date. Treat that column as a cache, never as the source of truth, and make `tasks.update` reject writes to derived states.

### 3. Both question systems survive in untyped form - Severity: Major
* **Reviewer Lens:** both
* **Affected Scope:** lines 267, 367, 693, item 18 (line 778), item 20 (line 797)
* **Discrepancy Description**: `project-question` and `question` both stay as jsonb `document` kinds. Their `task_id` lives inside `data`, with no foreign key and no index, and `project_id` can be null. Even so, the waiting reason, the decision blocks and CUJ 1 all depend on these records. Item 20 does not say which store its "open questions" come from.
* **Rationale & Recommendation**: In Phase 2, add one typed `question` table with these columns:
  * `project_id` (not null), plus optional `task_id` and `execution_id`;
  * `asked_by`, `kind` and `options`;
  * `state` (`open`, `answered` or `withdrawn`), `answer` and `answered_by`.

  When an agent asks a question, its execution moves to `waiting`. Asking and answering also write `event` rows. Migrate both legacy kinds into this table.

### 4. Acceptance has no record - Severity: Major
* **Reviewer Lens:** `egm-inventive-catalyst` (and the open M8 item from `egm-zoom-out`)
* **Affected Scope:** line 16, line 262, line 302; brief line 115; earlier review M8 (lines 314, 320) and M9 (line 330)
* **Discrepancy Description**: The design treats acceptance as implied by `completed`. Nothing records who accepted which execution or artifact version, and results that have no task are not covered. The brief needs acceptance to be recorded as a human score.
* **Rationale & Recommendation**: Record acceptance as a human answer to a decision question (see finding 3), bound to an execution and an artifact. Store it as a `score` row (name, value, source, procedure version) or as an event, and derive the task's `accepted` outcome from it. For results with no task, decide whether acceptance creates a task automatically or is allowed without a task link, and write the choice down.

### 5. ClickHouse is added before the load is proven - Severity: Major
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** D3 (line 210), section 4, A10 (lines 517-521), Phase 4
* **Discrepancy Description**: The case for ClickHouse rests on a guess of about 1 million events per day. The brief (M13 and Direction 4) asked to rule out a Postgres-only setup first. The design also adds a second ClickHouse next to the one Langfuse already runs on PDS18.
* **Rationale & Recommendation**: Start with Postgres only, using a monthly-partitioned `event` table. Add ClickHouse when a measured query proves too slow. If ClickHouse is needed earlier, reuse the existing instance with its own database and user.

### 6. Scope is far wider than the thin slice the owner accepted - Severity: Major
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** whole document; brief lines 109-117 and 247; lines 270-297; items 25 and 28b
* **Discrepancy Description**: The owner accepted one thin end-to-end slice first. The design instead ships eight phases. They include:
  * a per-host placement worker;
  * a daily hash audit and copy commands;
  * retention rules with a preview-and-confirm step;
  * ffmpeg previews.

  CUJ 5 does make keeping a project's files on one host a requirement. The audit, retention and relocation work, however, is not needed for the slice.
* **Rationale & Recommendation**: Mark which phases the slice needs (probably Phases 0-4 with a single store) and defer the rest. Retention rules default to empty (line 297), so nothing needs them yet.

### 7. The isolation boundary is a separate design inside this one - Severity: Major
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** section 9 and Phase 5 (lines 395-435, items 38-42)
* **Discrepancy Description**: This part covers OS users, sudoers, git hardening and network policy. These topics carry their own risks and already have an owning document, `layered-isolation-model.md`.
* **Rationale & Recommendation**: Move this part to its own design. Here, keep only the interfaces the data layer depends on: the measurement basis and the secret set used for redaction.

### 8. `tool_run` overlaps with spans - Severity: Nit
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** lines 245-246
* **Discrepancy Description**: Line 245 says calls are spans, but `tool_run` makes some calls executions under a vague test ("billed or long-running").
* **Rationale & Recommendation**: Drop `tool_run` until the connection gateway design (Q9) needs it. A cost is already a `measurement` row.

### 9. "Delivery" names two different things - Severity: Nit
* **Reviewer Lens:** `egm-inventive-catalyst`
* **Affected Scope:** lines 246, 428
* **Discrepancy Description**: "Delivery" is an execution kind, and "delivery path" is also the isolation term for host-side git writes.
* **Rationale & Recommendation**: Once finding 1 is adopted, keep `delivery` only for the merge or publish step, or drop it. Rename the isolation term to something like "host-side git writer".

### 10. AssetQ/Pipe is rejected with a weak reason - Severity: Nit
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** A8d (lines 505-509)
* **Discrepancy Description**: AssetQ/Pipe is already deployed, with Postgres metadata and immutable storage. Calling it a "prototype" is not a technical reason to reject it, and the owner prefers existing products.
* **Rationale & Recommendation**: State the real gap (for example, no per-host placement), or say which parts of it are reused.

### 11. The rollback strategy doubles the migration work - Severity: Nit
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** lines 364-369, item 15
* **Discrepancy Description**: Phase 1 changes the storage engine and Phase 2 changes the data model, and each is a separate migration with a custom reverse export.
* **Rationale & Recommendation**: Choose either a dual-write window or forward-only migration after `verify`, and say which one.

### 12. Implementation detail appears in the architecture sections - Severity: Nit
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** items 1-2, 11, 24, 40, 53
* **Discrepancy Description**: Some low-level details read like decisions:
  * port numbers;
  * `createStore` signatures and `SERIALIZABLE` retries;
  * the 64 MiB multipart threshold;
  * git `-c` flags;
  * "measured size plus 10%".
* **Rationale & Recommendation**: Present these as implementation defaults, not decisions, and keep them out of the architecture sections.

## Severity Summary

| Severity | Count |
| --- | --- |
| 🔴 Blocker | 0 |
| 🟡 Major | 7 |
| 🟢 Nit | 5 |
