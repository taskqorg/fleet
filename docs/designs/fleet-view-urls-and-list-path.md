# Fleet view URLs and list data path

* **Author:** Alex Kirs
* **Status:** Draft
* **Date:** 2026-11-10
* **Brief:** [web-stack-brief.md](web-stack-brief.md) (measurements, alternatives, owner answers)

--------------------------------------------------------------------------------

## Overview

This design fixes how the operator app's lists behave when the operator changes a filter, grouping, search or selection, and defines the URL format that carries every view state. It has four parts:

1. **URL view contract.** One grammar for filters, grouping, search, presets, the focused row and checked rows. A shared codec (`parseView` / `formatView`) used by the app and the coordinator, and a `view_link` operation for agents.
2. **List data path.** The normalized entity store that operator-app D4 already decided, which the current code does not implement. Lists keep their rows on screen while new data loads, the list element is created once, and search sends one request per typing pause.
3. **`task_page` query.** Stored columns on `task` maintained by Postgres triggers, so a page read costs in proportion to the page, not to the whole task table.
4. **Asserting check.** `tests/ui/filter.check.ts` gains an asserting mode, which fails when any of the above regresses.

The Node toolchain cleanup from the brief is a separate, later slice (see Future Improvements).

## The Problem

Measured in the brief (local PGlite, Chromium):

* Every filter, grouping or "All" change makes the state counters disappear, replaces the list with a loading skeleton, and creates the list element again. Scroll and focus are lost. Cause: `useLiveList` in `app/src/live.ts` calls `setPage(EMPTY)` on every key change; `StateCounters` draws nothing for undefined counts; `tasks.tsx` shows `<Skeleton>` while `!list.at`.
* Typing "hero" in search sends four `task_page` requests and blanks the list four times.
* `task_page` computes four correlated subqueries (current agent, last host, first start, open questions) for every task in the table before it filters, three times per call. At 20,000 tasks, grouping by agent takes 194 ms over HTTP; the time grows with history, not with the page. The shared operation layer adds about 1 to 2 ms and is not the cause.
* The URL carries only `state`, `group` and `q`. Presets live in local preferences, so a copied link does not reproduce the view, and agents have no documented way to build one.
* **Drift found:** operator-app D4 decided "one normalized entity store on Preact Signals". The current `live.ts` keeps each list in component state (`useState`) with no shared store. Going Back or switching nav tabs therefore starts from the IndexedDB copy or a skeleton.

## Goals

* A filter, grouping, search, preset or selection change keeps the filter bar, counters and current rows on screen. New data replaces them in place; the list element is never re-created after the first load.
* Back, Forward and returning to a view through the nav rail show the last known rows in the first frame, then refresh in place.
* Search sends at most one request per typing pause; a request superseded by a newer one is cancelled.
* `task_page` over HTTP: under 300 ms at 20,000 tasks for every filter and grouping case in the harness, on local PGlite. The 20,000 → 100,000 tasks growth factor of the median is recorded and must stay below 2 (cost depends on page size, not table size).
* Every view state is in the URL in one canonical form. A link opens the same list, the same focused row and the same checked rows for any operator with access.
* Agents build and read view links through one operation, with the same code the app uses.
* `tests/ui/filter.check.ts --assert` fails on any regression of the goals above.

## Non-Goals

* Holding the full active task set in the browser for local filtering.
* Stored columns for `sessions_list` (same pattern; next slice once this one is measured).
* Short links stored on the server (`?s=<hash>`): added only when a real URL length limit is hit.
* Bulk actions themselves. This design defines `sel=` and keeps it in the URL; the actions are separate work.
* Node toolchain changes (vendoring, shared binaries, Bun trial).
* Entity codes allocation (data foundation §2, D17). The contract accepts codes once they exist; until then it carries UUIDs.

## Open Questions

* **Eviction of cached view pages.** The entity store keeps the ordered ids of visited views. Whether and when to drop them is decided from a measurement of real memory use, not a number chosen now.
* **Live Postgres numbers.** The 300 ms target is set on PGlite. Production is re-measured after rollout; if production is slower, the target stays and the query is revisited.

## Critical User Journeys (CUJs)

### CUJ 1: Change a filter without losing the screen

* **Given:** `/tasks` shows 100 rows grouped by priority, with counters.
* **When:** the operator clicks the "Failed" counter.
* **Then:** the URL becomes `/tasks?state=failed`; counters and rows stay visible with an "updating" mark; within one server round trip the rows are replaced in place; the list element is the same DOM node; no skeleton appears.

### CUJ 2: Send a view to another operator

* **Given:** the operator has the "Needs me" preset active, grouped by agent, with task A focused and tasks B and C checked.
* **When:** they copy the URL `/tasks/<A>?group=agent&preset=Needs%20me&sel=<B>,<C>&state=failed,waiting` and another operator opens it.
* **Then:** the second operator sees the same filters, grouping, focused row and checked rows, whatever presets they have saved. Rows they have no access to are absent; the link does not fail.

### CUJ 3: An agent links to a view

* **Given:** an agent wants to point the operator at failed tasks in project `fleet`.
* **When:** it calls `view_link` with `{ view: "tasks", state: ["failed"], project: "fleet" }`.
* **Then:** it receives `https://fleet.ufobe.com/tasks?project=fleet&state=failed`; calling `view_link` with that URL returns the same view object.

### CUJ 4: Type a search

* **Given:** `/tasks` is open.
* **When:** the operator types "hero" at normal speed.
* **Then:** one `task_page` request is sent after the pause; rows stay on screen while typing; the URL ends as `/tasks?q=hero`, and Back does not step through `h`, `he`, `her`.

### CUJ 5: Back and forward

* **Given:** the operator went from `/tasks` to `/tasks?state=failed` to `/tasks/<A>?state=failed`.
* **When:** they press Back twice.
* **Then:** each step shows the earlier list in the first frame from the entity store, then refreshes in place.

## Technical Plan (Architecture)

### 1. URL view contract

```text
/<view>[/<focus>]?<key>=<value>&...      keys in alphabetical order, defaults omitted
```

* **Views:** `tasks`, `conversations`, `hosts`, `overview`.
* **Focus (path segment):** the row shown in the detail pane. A UUID today; an entity code (`T-FLT-42`, D17) once codes exist. The codec accepts both. A code is resolved through `entity.resolve` and the URL is rewritten to the code.
* **Keys (tasks view)**, named after the `task_page` input fields so an agent can map a link directly onto the operation:

  | Key | Value | Default (omitted) |
  | --- | --- | --- |
  | `group` | `priority`, `state`, `project`, `agent`, `host`, `none` | `priority` |
  | `preset` | preset name (a label only) | none |
  | `project` | comma list of project ids | all |
  | `q` | search text | empty |
  | `sel` | comma list of checked rows (UUIDs or codes) | empty |
  | `state` | comma list of task states | all |

  The conversations view uses `q`, `sel`, `state` and `archived` (`1` or omitted). New keys are added to this table when a view gets the control.
* **Lists:** comma-separated. A comma, `%` or `&` inside a value is percent-encoded (`%2C`, `%25`, `%26`). Values in a list are sorted and de-duplicated.
* **Canonical form:** keys in alphabetical order, list values sorted, default values and empty lists omitted, no trailing `?`. Two equal views produce byte-identical URLs. The canonical query string (without focus) is the list key in the entity store and in IndexedDB.
* **Focus and `sel` are independent.** The focused row need not be in `sel`; focusing a row does not change `sel`; checking or unchecking does not change focus. Empty `sel` is omitted.
* **Presets:** a link carries the preset expanded into explicit keys, plus `preset=<name>` as a label so the preset bar can highlight it. Explicit keys define the view; a receiver without that preset sees the same view. If the explicit keys no longer match the preset's definition, `formatView` drops `preset`.
* **History:** typing in search replaces the current history entry (debounced together with the request). Discrete changes (counter, grouping, preset, focus, checking a row) push a new entry.
* **Unknown keys** are kept in the URL and ignored. No version key is added until an incompatible change happens.
* **Meaning:** a link is a live query, not a snapshot.
* **Length:** no fixed limit. If a real limit is hit (chat tools, Cloudflare Access, MCP transport), it is recorded and a server-stored short link is designed then. URLs are never cut silently.

**Codec.** `packages/ui/src/view-url.ts` exports:

```ts
type View = { view: 'tasks' | 'conversations' | 'hosts' | 'overview'; focus?: string; group?: string; preset?: string;
  project?: string[]; q?: string; sel?: string[]; state?: string[]; archived?: boolean; extra?: Record<string, string> };
parseView(pathAndQuery: string): View          // tolerant: unknown keys go to extra; invalid values are dropped
formatView(v: View): string                    // canonical path + query, no origin
listKey(v: View): string                       // canonical query without focus and sel: the entity-store list key
toPageParams(v: View): { filter: {...}; group: string }   // the task_page / sessions_list input
```

The module has no dependencies, so the coordinator imports it as well. It lives in `packages/ui` because the app and the design book already import from there.

**Agent operation.** `view_link` in the coordinator operation table (served as `POST /api/ops/view_link` and as an MCP tool):

* `{ view: {...} }` → `{ url, path }`: the canonical URL on the configured browser origin.
* `{ url: "..." }` → `{ view }`: the parsed view; unknown keys returned in `extra`.

It is a read operation and needs no database access.

### 2. List data path (entity store, D4)

```text
 URL ──parseView──▶ View ──listKey──▶ store.lists[key] : signal<{ ids, counts, groups, nextAfter, at, pending, error }>
                                     store.records[id] : signal<TaskRow | SessionRow>
                                            ▲                       ▲
  task_page / sessions_list  ──page──────────┘                       │
  feed changes ──▶ list_membership(ids, list key) ──rows + counts──▶ records updated once, ids placed by sort key
  IndexedDB (D3) ◀── save per list key + records ──▶ restore on first use of a key
```

* **`app/src/store.ts` (new)** holds two maps of Preact signals: `records` (by entity id, one per kind) and `lists` (by list kind plus `listKey`). A list holds ordered ids, counts, group summaries, `nextAfter`, `at` (time of last server answer), `pending` (a request is in flight) and `error`.
* **Revalidate, never reset.** Asking for a list key that is already in memory returns it at once and starts a refresh with `pending = true`. A new key with no memory copy is restored from IndexedDB; only if there is nothing at all is the list empty with `at = 0`, and only then the view shows the skeleton.
* **Swapping keys.** When the URL changes from key K1 to K2 and K2 has no data yet, the view keeps showing K1's rows and counts, marked `pending`, until K2 arrives. Then it renders K2 in the same list element. The view reads "shown key" and "wanted key" separately, so stale rows are always labelled.
* **Request order.** Each list key has a sequence number. An answer is applied only if it is the latest for its key. A superseded request is cancelled with `AbortController`, which `op()` in `app/src/api.ts` accepts as an option.
* **Paging.** `more()` reads `nextAfter` from the list for the shown key and appends only if the key is still the same when the page arrives.
* **Changes.** One feed subscription for the whole store (not one per view). For each change batch, the store collects touched ids per list kind and calls `list_membership` once per mounted list key. Answers update `records` once; every list that contains the id re-renders that row. `applyMembership` in `app/src/lists.ts` stays the merge rule and now works on ids plus the record map.
* **Group counts.** `list_membership` returns group summaries for grouped lists, so the extra `task_page` with `limit: 1` after each change goes away.
* **Unmounted lists.** Lists of views that are not mounted are not refreshed on changes. When their view mounts again, they render from memory and revalidate once.
* **Detail pane.** `useLiveRecord` reads `records[id]` first and keeps the previous record visible while re-reading. Task detail fields that are not in list rows (`task_get` data) are stored under the same id in a separate `details` map.
* **One mounted list.** `main.tsx` routes `/tasks` and `/tasks/:id` to one route with an optional segment, so `Tasks` is never unmounted when focus changes. The list element is keyed by view kind only, never by filter key.
* **Search.** The input updates local text at once; the URL and the request follow after 150 ms of no typing, with history replace.
* **Visible states.** `pending` shows a thin progress mark in the list header and dims rows to the existing muted token; `error` keeps rows and shows an error banner labelled "showing data from <time>"; an answered empty list shows the empty state.

### 3. `task_page` query: stored columns maintained by triggers

Migration 9 in `packages/coordinator/db/schema.sql`:

* **Columns on `task`:** `agent text NULL` (harness of the latest execution in `preparing` or `running`), `host text NULL` (host of the latest execution), `started_at timestamptz NULL` (earliest `started_at` of its executions), `open_questions integer NOT NULL DEFAULT 0`.
* **Function `fleet_task_derived(task_id text)`** recomputes the four values for one task from `execution` and `question` and writes them with one `UPDATE task ... WHERE id = $1 AND (columns) IS DISTINCT FROM (new values)`. The `IS DISTINCT FROM` guard means unchanged values write nothing and produce no change row.
* **Triggers:** `AFTER INSERT OR UPDATE OR DELETE` on `execution` and on `question`, `FOR EACH ROW`, calling `fleet_task_derived` for `NEW.task_id` and, when it differs, `OLD.task_id`. Rows with a null `task_id` are skipped.
* **Change feed:** the trigger's `UPDATE task` fires the existing `task_change` trigger (migration 8), so the task gets a change row in the same transaction and live lists refresh. No feed code changes.
* **Row-level security:** the function runs as `SECURITY INVOKER` inside the writer's transaction, like `fleet_change`; the coordinator writes with operator scope.
* **Backfill:** the migration ends with one `UPDATE task` that sets all four columns from subqueries, for every existing row. It runs inside the migration's global write, like migration 7.
* **Indexes:** `task(priority, seq)`, `task(state)`, `task(agent)`, `task(host)`; and `execution(task_id, seq DESC)` and `question(task_id) WHERE state = 'open'` for the trigger function.
* **Query:** `task_page` and `membership` in `packages/coordinator/tasks.ts` read the columns from `task` directly; the CTE with correlated subqueries is removed. Rows, per-state counts and group summaries come from two statements: the page (`ORDER BY group key, seq DESC, id LIMIT n+1`) and one aggregate with `GROUPING SETS ((state), (group key))` over the same `WHERE`. The group key is chosen from the fixed `GROUPS` map; the group name is passed as a parameter instead of string interpolation.
* **Drift check:** `store.tasks.derivedDrift()` recomputes the four values for every task and returns the ids whose stored values differ. Used by a test in `npm test` and exposed as the read operation `task_derived_check` for operators.

### 4. Asserting filter check

`tests/ui/filter.check.ts` keeps its report and adds `--assert`, which exits non-zero when any rule fails. It runs at 3,000 and 20,000 tasks (and prints, without asserting, a 100,000-task run for the growth factor):

* the list element is the same node before and after a filter, grouping, "All", preset, focus and check change;
* no skeleton after the first load;
* counters never empty during a change;
* typing "hero" sends exactly one `task_page`;
* each filter or grouping case settles in under 300 ms at 20,000 tasks;
* Back and Forward show rows in the first frame;
* focusing a row does not re-create the list and does not change `sel`;
* a copied URL opened in a second page reproduces focus, `sel`, filters and grouping.

## Alternatives Considered

### Alternative A: Selection only in the path, or only in `sel=`

* **Pros:** one concept.
* **Cons:** path only cannot express checked rows for bulk actions; `sel=` only breaks every existing `/tasks/:id` link and the detail route.
* **Reason for Rejection:** Rejected by author: both needs exist; focus and checked rows are kept as two independent parts of the URL.

### Alternative B: Rewrite the query only, no stored columns

* **Pros:** no schema change.
* **Cons:** grouping by agent or host still needs the derived value for every matching task; an unfiltered grouped list keeps scanning executions and grows with history.
* **Reason for Rejection:** Rejected by author: does not meet the growth goal.

### Alternative C: Stored columns written by coordinator code

* **Pros:** logic in TypeScript.
* **Cons:** every write path (including legacy ones) must remember the update; a missed path drifts silently.
* **Reason for Rejection:** Rejected by author: triggers keep the columns right on every path, as the change log already does, and PGlite runs the same triggers in tests.

### Alternative D: Keep list state per component (stale-while-revalidate in the hook)

* **Pros:** smallest change.
* **Cons:** Back, Forward and nav return still start from IndexedDB or a skeleton; D4 stays unimplemented.
* **Reason for Rejection:** Rejected by author: fails "nothing jumps" on navigation.

### Alternative E: Page cache keyed by URL, rows duplicated per key

* **Pros:** instant back and forward with a moderate change.
* **Cons:** a changed task updates in each cached page only after membership runs for that page.
* **Reason for Rejection:** Rejected by author: D4's normalized store updates a record once for every list.

### Alternative F: Another front-end stack

* **Reason for Rejection:** Rejected by author: the causes are the data path and the query, which carry over to any stack (see the brief).

--------------------------------------------------------------------------------

## Non-Functional Requirements

* **Performance:** targets in Goals; the check measures them.
* **Write cost:** each execution or question write adds one function call and at most one `UPDATE task`. The harness records the fixture load time before and after migration 9 to show the cost.
* **Security:** `view_link` builds URLs only on the configured browser origin and never includes credentials. A shared link grants nothing: rows are filtered by the receiver's access as today.
* **Rollout and rollback:** migration 9 is additive (new columns, functions, triggers, indexes). The code that reads the columns ships in the same release. Fix forward if a defect appears; the drift check detects wrong values.

--------------------------------------------------------------------------------

### Additional Resources

* [web-stack-brief.md](web-stack-brief.md)
* [fleet-operator-app.md](fleet-operator-app.md) (D3 offline, D4 entity store, routing)
* [fleet-data-foundation.md §12](fleet-data-foundation.md) (concurrency model)
* [design-system decisions D17](../design-system/decisions.md) (entity codes)

### Future Improvements

* **Node toolchain slice (planned):** exact pins, `--ignore-scripts`, an allowlist and audit policy, a decision on `better-sqlite3`, a shared content-addressed store for esbuild, tsc and Playwright outside the worktrees, the six front-end packages vendored as hashed ESM, and a Bun trial for tooling. See the brief, Key Decisions item 6.
* **Stored columns for `sessions_list`**, the same trigger pattern on the session document.
* **Overview page:** one `task_page` per project today; one aggregate read instead.
* **Per-operation timing** in `runOp` and a slow-query log.

--------------------------------------------------------------------------------

## Detailed Implementation Plan

### Target Files to Create/Modify

#### 1. `packages/ui/src/view-url.ts` (Create)

* **Type / Scope:** TypeScript module, no dependencies.
* **Proposed Changes:** `View` type, `parseView`, `formatView`, `listKey`, `toPageParams` as in Technical Plan §1, with the key table per view as a constant.

#### 2. `packages/ui/src/index.ts` (Modify)

* **Proposed Changes:** export the view-url module.

#### 3. `tests/ui/view-url.test.ts` (Create)

* **Type / Scope:** unit test (`node:test`).
* **Proposed Changes:** round trip for each view; canonical ordering (keys and values); defaults omitted; percent-encoded commas; focus and `sel` independence; preset dropped when keys diverge; unknown keys kept; today's links (`?state=a,b&group=agent&q=x`, `/tasks/<uuid>`) parse.

#### 4. `app/src/store.ts` (Create)

* **Type / Scope:** entity store on Preact Signals.
* **Proposed Changes:** `records`, `details` and `lists` maps; `useList(kind, view)` returning `{ shown, wanted, rows, counts, groups, pending, error, more }`; `useRecord(kind, id, fetch)`; one feed subscription; IndexedDB save and restore per list key and per record; sequence numbers and `AbortController` per key.

#### 5. `app/src/live.ts` (Delete)

* **Proposed Changes:** replaced by `store.ts`. `freshness` moves to `store.ts`.

#### 6. `app/src/lists.ts` (Modify)

* **Proposed Changes:** `applyMembership` works on ordered ids plus the record map; `touched` and `chunks` unchanged.

#### 7. `app/src/api.ts` (Modify)

* **Proposed Changes:** `op(name, body, { signal })` passes an `AbortSignal` to `fetch`; an abort is not treated as offline.

#### 8. `app/src/main.tsx` (Modify)

* **Proposed Changes:** one route per view with an optional focus segment (`/tasks/:id?`), so focus changes never unmount the view.

#### 9. `app/src/views/tasks.tsx` (Modify)

* **Proposed Changes:** read the view with `parseView`, write with `formatView` (push for discrete changes, replace for search); `useList`; debounced search; skeleton only when `at = 0` and no rows; pending and error states; checked rows from `sel`; presets applied by expanding keys.

#### 10. `app/src/views/conversations.tsx` (Modify)

* **Proposed Changes:** same as tasks for the sessions list.

#### 11. `app/src/views/overview.tsx`, `app/src/views/hosts.tsx` (Modify)

* **Proposed Changes:** `useLiveRecord` replaced by `useRecord`; behaviour otherwise unchanged.

#### 12. `app/src/prefs.ts` (Modify)

* **Proposed Changes:** saved views store a `View` (all keys), not only `states` and `group`; existing saved views are read and converted on load.

#### 13. `packages/ui/src/controls.tsx` (Modify)

* **Proposed Changes:** `StateCounters` keeps showing the last known counts and gets a `pending` prop (dimmed). `ListFrame` gets a `pending` prop for the header progress mark. The design book (`packages/ui/book`) gets the pending state for both; `npm run check:design` must pass.

#### 14. `packages/coordinator/db/schema.sql` (Modify)

* **Proposed Changes:** migration 9 as in Technical Plan §3: columns, `fleet_task_derived`, triggers on `execution` and `question`, indexes, backfill. Each statement on one line where it contains semicolons inside a function body (the migration runner splits at a semicolon that ends a line).

#### 15. `packages/coordinator/tasks.ts` (Modify)

* **Proposed Changes:** `page` and `membership` read stored columns; the CTE `base` is removed; counts and groups in one `GROUPING SETS` statement; group name passed as a parameter; `derivedDrift()` added.

#### 16. `packages/coordinator/app.ts` (Modify)

* **Proposed Changes:** `view_link` and `task_derived_check` operations; `list_membership` returns group summaries for grouped task lists.

#### 17. `packages/coordinator/store.ts` (Modify)

* **Proposed Changes:** add `view_link` and `task_derived_check` to `READ_OPERATIONS`.

#### 18. `tests/coordinator/task-derived.test.ts` (Create)

* **Type / Scope:** coordinator test on PGlite.
* **Proposed Changes:** create tasks, executions and questions through store code paths (including pause, resume, cancel, question answer, execution delete); assert stored columns after each step and `derivedDrift()` empty; assert a change row for the task appears when a derived value changes and none when it does not; assert backfill by migrating to 8, writing data, then migrating to 9.

#### 19. `tests/coordinator/tasks.test.ts` (Modify)

* **Proposed Changes:** for a fixed fixture, the new `page` returns the same rows, order, counts and groups as the old query for every group and filter case (the old query is kept in the test file as the reference).

#### 20. `tests/ui/lists.test.ts` (Modify)

* **Proposed Changes:** membership on ids plus records; ordering; anchor kept when rows are inserted above.

#### 21. `tests/ui/filter.check.ts` (Modify)

* **Proposed Changes:** `--assert` mode with the rules in Technical Plan §4; 3,000 and 20,000 task runs; 100,000 report-only run; fixture load time printed.

#### 22. `package.json` (Modify)

* **Proposed Changes:** `check:filter` script: `npm run build && tsx tests/ui/filter.check.ts --assert`.

#### 23. `docs/designs/fleet-operator-app.md` (Modify)

* **Proposed Changes:** routing section links to this design for the URL contract; record that D4 is implemented by `app/src/store.ts`.

#### 24. `docs/designs/fleet-data-foundation.md` (Modify)

* **Proposed Changes:** §2 task: the four stored columns and their trigger; §12: trigger writes happen inside the writer's transaction.

#### 25. `docs/interfaces.md` (Modify)

* **Proposed Changes:** `view_link` and `task_derived_check` described for agents.

### Verification Plan

* **Compilation/Build:** `npm run build`.
* **Unit Tests:** `npm test` (includes view-url, lists, task-derived, tasks).
* **Static Analysis & Linting:** `npx tsc -p app/tsconfig.json --noEmit`; `npm run check:design`.
* **E2E/Integration:** `npm run check:filter` passes; `npx tsx tests/ui/app.check.ts` reports no missing parts.
* **Metrics:** settle time per case at 3,000 and 20,000 tasks, `task_page` median over HTTP, 20k → 100k growth factor, fixture load time before and after migration 9. Recorded in the PR.

--------------------------------------------------------------------------------

*Drafted with EGM*
