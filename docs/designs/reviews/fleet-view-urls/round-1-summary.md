# Review round 1: fleet-view-urls-and-list-path.md

Six Goldfish reviewers ran on the first draft (2026-11-10): zoom-out, inventive catalyst, comprehension, design critic, implementation readiness, codebase details.

## Blockers and how they were resolved

| Finding | Lenses | Resolution |
| --- | --- | --- |
| Triggers that update `task` from execution-scope writes take task row locks (waits, deadlocks) and can store stale values under concurrent writes | design, inventive, codebase | Owner chose a side table `task_derived`, recompute serialized with `pg_advisory_xact_lock` per task; execution writes never touch `task` |
| `SECURITY INVOKER` trigger under row-level security can update zero rows or count a partial set | design, inventive | Function is `SECURITY DEFINER` with fixed `search_path`; scoped-writer test added |
| One `GROUPING SETS` statement over one `WHERE` changes counter semantics | design, readiness, codebase | Three statements; per-state counts drop the state filter, as today |
| `project` is a list in the URL but a single value on the server | readiness, codebase | `projectId` accepts an array; `-` means no project |
| `formatView` cannot validate presets without their definitions | all | `preset` is an inert label; the app highlights by key match |
| Coordinator cannot import from `packages/ui` (root tsconfig excludes it) | codebase | Codec moved to `packages/view-url` |
| No phases | readiness | Six phases, each builds and passes tests |
| "Execution delete" has no store path | codebase | Raw SQL in the test |

## Majors applied

List key defined once (excludes focus, sel, preset, extra); sort keys per list, not in shared records; search debounce 300 ms with one push then replace; growth factor reported, not asserted; `updated_at` and backfill change rows stated; trigger column lists; expression index for the default order; `view_link` in the agent operation table, origin behaviour and errors; `useDoc` keeps `touches` and `refreshMs`; `applyMembership` signature; checkbox control specified; `filter.check.ts` CLI; `ListFrame` is in `patterns.tsx`; terms section for agent, harness, conversation, operation, membership, scope.

## Contested, kept by owner decision

* Zoom-out M1: measure a query rewrite before storing values. Owner chose stored values; recorded in Alternative B.
* Zoom-out M5: drop `sel` until bulk actions exist. Owner put `sel` in the contract.
* Zoom-out N1: `task_derived_check` operation is speculative. Kept: owner asked for it as an operator check.

# Review round 2 (design critic, codebase details, after the side-table change)

| Finding | Resolution |
| --- | --- |
| `SECURITY DEFINER` does not bypass `FORCE ROW LEVEL SECURITY`; policies read `fleet.project_ids` (both lenses, Blocker) | Function-level `SET fleet.project_ids = '*'`; Security text corrected; test uses `SET LOCAL ROLE fleet_app_test` (NOBYPASSRLS), as the existing RLS test does |
| Lock-order rule could not be followed (design, Blocker) | Stated the real invariant: only GLOBAL writers change trigger columns; recorded for §12; advisory lock kept as a second guard with ascending id order |
| `task_derived.project_id` stale after a task moves; child-before-task leaves it null | `AFTER INSERT OR UPDATE OF project_id ON task` trigger runs the recompute |
| Alternatives C and E rejection reasons inaccurate | Rewritten |
| Group expression cannot be a bind parameter | Interpolated from the fixed map, rewritten for `t.`/`d.` aliases |
| `view_link` placement for agents | Defined in `agent-tools.ts` (operations, descriptions, `bound()` branch, `browserOrigin` dep) |
| Indexes cited as existing; `offline.save` cursor; book page names; `app/tsconfig.json` entry; `CREATE OR REPLACE fleet_change` | Named and specified |
| Concurrent-writer test on PGlite | Runs only with a Postgres `DATABASE_URL`, otherwise skipped |
