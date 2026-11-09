# Stage 5: Initial zoom out (fleet-operator-app.md)

Reviewers: egm-zoom-out and egm-inventive-catalyst. Totals: 3 Blockers, 8 Majors and 5 Nits. None of the findings asks to reverse D1–D5.

## Blockers

| # | Finding | Fix |
|---|---|---|
| B1 | `app.ts:33-36` wraps every GET in one `store.read` (REPEATABLE READ), so `/api/stream` would hold a single old snapshot, a pool connection, and vacuum for its whole life. It would never see new rows. | Add `/api/stream` to `longRoutes`. Use a short read per backlog and per notify. Add a test that commits after the stream is open. |
| B2 | `schema.sql` already has migration 7. | Use the next free number, which is assigned when the code lands. |
| B3 | Transcript `event` documents are ignored by the client, so open conversations cannot update live (G1, G2, CUJ 2). | Carry `event` changes with `session_id`. The open conversation fetches after its event cursor through `/api/sessions/:id/events?after=`. |

## Majors

| # | Finding | Fix |
|---|---|---|
| M1 | The overlap window does not guarantee that nothing is lost: execution-scope ingest commits in parallel with GLOBAL writes, so `seq` order is not commit order. The window is also an arbitrary limit. | Add a visibility watermark: an `xid8` column, and send only rows from transactions older than `pg_snapshot_xmin(pg_current_snapshot())`. The cursor moves only past finished transactions. |
| M2 | One notify per row floods every stream during event ingest. | Use a constant payload `pg_notify('fleet_change','')`: Postgres merges duplicates per transaction, so there is one wake per commit. |
| M3 | The trigger sketch uses `NEW.seq` and breaks on DELETE. | Use `OLD` or `NEW` by `TG_OP`. |
| M4 | Related rows are affected by changes elsewhere: the harness mark from executions, host counts from executions and observations, session questions, counters. Re-reading by matching kind and id misses them. | The trigger fills parent ids (`task_id`, `session_id`, `host_id`) on each change row. One server-owned map from change kind to affected client kinds and lists, with tests. |
| M5 | The stream's scope is per process (`store.ts:20`); per-request scope does not exist yet. | State that the first slice uses `*`, and keep the `(cursor, scope)` interface for Phase 6. |
| M6 | The prune inside GLOBAL stalls writers. | Delete in batches in a plain transaction outside GLOBAL. |
| M7 | There is no settings path for retention. | Use a config value for the first slice, readable through an op. |
| M8 | Several windows share one IndexedDB with no clear cursor. | Catch up from the lowest stored cursor among the restored lists; drop duplicates by `seq`. |

## Nits

| # | Finding | Fix |
|---|---|---|
| N1 | Each changed row costs one get plus a membership call per frame (N+1). | `list_membership` returns the rows themselves; add a batched get for details. |
| N2 | Resync edge cases: the log is empty after a prune, or the cursor is ahead after a restore. | Keep a `change_log_floor`; resync when the cursor is below the floor or above the max. |
| N3 | The `onRequest` list of exempt paths keeps growing. | One rule: everything outside `/api/*` is exempt, and `/legacy/*` follows today's rule. |
| N4 | Nothing changes `grantsVersion` in this slice. | Return a constant; it starts changing in Phase 6. |
| N5 | Backoff and window numbers belong in the plan. `hosts_list` differs from the planned `hosts.list`. | Move the numbers; align the name and shape with data foundation §11. |
