# Consolidated EGM Review: fleet-data-foundation.md

Mode: uninterrupted (Stages 5 → 6 → 7 → 8 → 9). Author: Alex Kirs. Design doc and code not edited.
Split: FeatureRange 1..583, DetailedRange 584..1125 (`## Detailed Implementation Plan` at line 584).
GoldfishContext: `fleet-data-foundation-brief.md`, `fleet-data-foundation-goldfish-review.md` (design doc has no local links).

## Stage reports

| Stage | Report | 🔴 | 🟡 | 🟢 |
| --- | --- | --- | --- | --- |
| 5 Initial Zoom Out (zoom-out, inventive-catalyst) | `zoom_out_report.md` | 0 | 7 | 5 |
| 6 Comprehension | `comprehension_report.md` | 0 | 5 | 6 |
| 7 Critic (design, structural, style) | `critic_report.md` | 1 | 17 | 22 |
| 8 Intermediate Zoom Out | `intermediate_zoom_out_report.md` | 0 | 6 | 4 |
| 9 Implementation Readiness (readiness, codebase-details, style) | `implementation_readiness_report.md` | 7 | 17 | 13 |
| Raw total | | 8 | 52 | 50 |

All stages ran. No stage failed.

## Consolidated action items

### 🔴 Blockers

- **C1. Delivery versus task is undefined (M8).** `delivery` in code already acts like a task: it has a prompt, a `questionId`, a state machine and `steps[]` that point to executions (`packages/coordinator/delivery.ts:10-60`). Item 19 folds it into one `execution(kind=delivery)`, which means an execution that contains executions. The migration does not say where its fields go. Reviewers propose this fix: a delivery becomes a `task` row, its steps become executions with `task_id`, and the doc adds a field-mapping table. Sources: S5#1, S5#9, S7 M2, S8#1, S9#1, S9#5.
- **C2. Two question systems remain (M8).** `project-question` (answered by the operator, delivery depends on it) and `question` (raised by agents, `agent-tools.ts:128-136`) both survive, and each only gains a `task_id`. The doc also names a third link point, `decision`, which does not exist in code. Reviewers propose one typed `question` table that also records acceptance. Sources: S5#3, S7 M3, S8#2, S9#2.
- **C3. Item 19 changes the code that writes executions but not the code that reads them.** `agent-tools.ts`, `recovery.ts`, `ui.ts`, `lab-ui.ts`, `account-ui.ts`, `app.ts` and `delivery.ts` read execution documents directly. Source: S9#3.
- **C4. Rollback and migration gaps.** `reverse` exports only `document` rows, so a rollback after Phase 2 is impossible, which contradicts G7 and the NFR. Phase 2 has no mapping for each legacy kind. Phase 3 removes base64 artifacts that `delivery.ts` still reads. Sources: S9#4–6, S7 M10, S6 rollback nit.
- **C5. Deleting a deduplicated object.** Objects are deduplicated within a project, but deletes run per artifact row. Deleting one row can therefore remove bytes that another live row still uses. There is no reference count, and secret-bearing objects have no forced purge (L278, L289-307). Stage 8 notes that deferring the artifact lifecycle removes this problem. Source: S7 B1.
- **C6. Artifact state is wrong in commit (L829).** Source: S9#7.

### 🟡 Major

- **C7. Task state is stored twice, and acceptance has no record (M8).** G2 says task state is derived, but `task.state` is stored and no transitions are defined. "Completed" is not "accepted", and results without a task are not covered. Sources: S5#2, S5#4, S7 M1, S9#8–9.
- **C8. Agents cannot reach tasks.** Item 44 adds no `task_*` MCP tools and no permission rule, which contradicts G6 and brief requirement 5. The tests do not cover M8. Source: S9#10–11.
- **C9. ClickHouse.** `ReplacingMergeTree` removes duplicates by a sort key that includes `ts`, not `seq`, and only when parts merge, so G6 ("nothing counted twice") is not guaranteed. Totals that are written once miss late events. Stage 5 and Stage 8 propose deferring ClickHouse and using Postgres with a unique `(execution_id, seq)` key. Stage 8 advises against reusing Langfuse's ClickHouse (tight D6/D7). Sources: S5#5, S7 M4–M5, S8#3.
- **C10. Row-level security does not apply to the table owner.** The coordinator connects as `fleet`, which owns the tables. Use `FORCE ROW LEVEL SECURITY` or a separate role that does not own them. Sources: S7 M6, S9#12.
- **C11. Store reachability.** The stores listen on loopback, yet cross-host presigned links and coordinator-side `CopyObject` are promised. Stage 8 proposes reaching the stores over the private host network. Sources: S6, S7 M8, S8#5.
- **C12. Isolation is a separate design.** Running agents under another uid with `systemd-run --uid` needs root or polkit, which may conflict with tight D4 ("no sudo"). Proposed fix: move isolation into `layered-isolation-model.md`. Sources: S5#7, S7 M9, S8#6, S9#13.
- **C13. Scope is wider than the thin slice the owner accepted.** Defer ClickHouse, the artifact lifecycle and isolation. Drop `tool_run`. Sources: S5#6, S5#8, S8.
- **C14. Redaction covers less than the doc claims.** Source: S7 M7.
- **C15. Undefined terms.** tight (its D-numbers clash with this doc's D1–D6), profile, environment, project grant, experiment and variant (CUJ 2 needs variant and score columns that the schema lacks), AssetQ, Pipe, t3code and Orca. Sources: S6 Majors, S5#10.
- **C16. Structure.** There is no Risks section. Open Questions are out of order and mostly answered, and Q3 is missing. "See item 35" points to nothing. §9 repeats itself, and the repeats disagree. Q5 and Q6 state facts without evidence. The performance claim goes beyond the evidence. Sources: S7 M11–M17, S6 nits.
- **C17. Code-detail errors in the plan.** Line references are wrong. Item 5 edits the wrong file. Stub schemas are missed (item 44). The executor's `FLEET_MAX_ARTIFACT_BYTES` limit is not removed. Insertion order is lost because `all()` orders by `rowid`. `put` conflict behaviour is not specified. The transaction rules ignore async and retried operations. Some SQLite users are missing from items 9, 50 and 52. Some values cannot be tested. Source: S9#14–24.

### 🟢 Nits

There are 50 raw nits across the stages, mostly wording, undefined abbreviations (agy, ACP, PDS18), commit hashes without context, and item 49 marked "Create" instead of "Modify". See each stage report.

## M8 verdict

M8 is not closed. All five stages agree that delivery is task-like and should become a `task`, and that the two question systems should merge into one typed table. They also agree that task state should be derived and that acceptance needs a record. Fixing M8 makes the model smaller, not larger (Stage 8).

## Traceability matrix

| Item | S5 | S6 | S7 | S8 | S9 |
| --- | --- | --- | --- | --- | --- |
| C1 | 1, 9 | | M2 | 1 | 1, 5 |
| C2 | 3 | | M3 | 2 | 2 |
| C3 | | | | | 3 |
| C4 | 11 | rollback nit | M10 | | 4, 5, 6 |
| C5 | | | B1 | 4 | |
| C6 | | | | | 7 |
| C7 | 2, 4 | | M1 | 2 | 8, 9 |
| C8 | | | | | 10, 11 |
| C9 | 5 | | M4, M5 | 3 | |
| C10 | | | M6 | | 12 |
| C11 | | Major 4 | M8 | 5 | |
| C12 | 7 | | M9 | 6 | 13 |
| C13 | 6, 8 | | | cuts | |
| C14 | | | M7 | | |
| C15 | 10 | Majors 1–3, 5 | | | 14 |
| C16 | 12 | nits | M11–M17 | risk list | |
| C17 | | | | | 15–24 |
