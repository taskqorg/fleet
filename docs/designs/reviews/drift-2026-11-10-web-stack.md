# Drift report: web stack (2026-11-10)

Designs checked against code on main `425d7a1`: [web-stack-brief.md](../web-stack-brief.md), [fleet-view-urls-and-list-path.md](../fleet-view-urls-and-list-path.md), and the parts of [fleet-operator-app.md](../fleet-operator-app.md) and [fleet-data-foundation.md](../fleet-data-foundation.md) that the web-stack work changed. Doc-side drift is fixed in the same PR as this report; code-side drift is listed as follow-ups.

## fleet-view-urls-and-list-path.md

Doc-side, fixed in this PR:
* §1 codec: `parseView` takes a full URL and an origin and returns `null` for a non-view; `viewProblem` (view_link validation) was missing.
* §1 `view_link`: field validation (400, never dropped) and "any origin when none is configured" were missing.
* §2 store: the document described a per-slot sequence; the code orders by change cursor per record and per list, keeps changes that arrive during a load, has the `holders` index, clears memory on sign-out, starts the feed at the server head (no stored cursors), restores stored rows after a failed load, and keeps unmounted lists without eviction. `useList` returns more fields (`total`, `at`, `shownView`, `reload`, `state`). `app/src/url.ts` was not mentioned.
* §3 functions: the document still described a function-level `SET fleet.project_ids` (the deploy blocker) and one function; the code has `task_derived_values`, `task_derived_recompute`, `fleet_task_derived` and `task_derived_drift()` with save and restore of the setting.
* §3 triggers: `seq` was missing from the execution column list; the backfill now runs before the change trigger (no change row per task) and sets `*` itself; `task_derived_agent`/`host` indexes were dropped and `execution_task` replaced by `execution_task_seq`; the page uses a LEFT JOIN only where needed and `summary()` serves counts and groups.
* Non-functional: "repair statement documented in the check's output" does not exist; the repair statement is now in the document.
* Implementation items 13, 25, 26 and the verification plan: store API, book section `list-pending` (not two sections), no backfill timing in the check, the CI jobs `filter-check` and `postgres-roles`.

Code-side, follow-ups:
* The asserting check (`filter.check.ts`) covers the Tasks view only; Conversations has the same data path and no browser assertion.
* The SSE path that updates a list on screen after a real change is not yet verified on PDS18 (owner: checked with the next real execution).
* 100,000 tasks (report-only): group-by-agent settles in about 490 ms; counts and search still scan the matching set. Planned: cached counts, a search index.

## web-stack-brief.md

Doc-side, fixed in this PR:
* The status now points to the design and names where the design decided otherwise (side table by triggers, D4 store, `sel` now).

Code-side, follow-ups:
* Key Decisions item 6, the Node toolchain cleanup (exact pins, `--ignore-scripts`, allowlist and audit policy, `better-sqlite3` decision, shared tool binaries outside worktrees, vendored front-end packages, Bun trial): not started; planned as its own slice.
* `sessions_list` still computes per-session subqueries over the whole table (same pattern as the old `task_page`).
* Overview calls `task_page` once per project.
* No per-operation timing or slow-query log in `runOp`.

## fleet-operator-app.md

Doc-side, fixed in this PR:
* §4 restore: "catch up from the lowest stored cursor of the restored lists" no longer holds; the feed starts at the head and restored or failed lists reload.
* §4 catch-up: `changes_since` is used after a stream reconnect and for polling, not on restore.
* §3 list shape: `{ name, filter, order, counts, cursor, endCursor }` replaced by the store's list shape.

## fleet-data-foundation.md

No drift detected.
