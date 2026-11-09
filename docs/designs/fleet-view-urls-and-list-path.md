# Fleet view URLs and list data path

* **Author:** Alex Kirs
* **Status:** Draft (review round 1 applied)
* **Date:** 2026-11-10
* **Brief:** [web-stack-brief.md](web-stack-brief.md) (measurements, alternatives, owner answers)

--------------------------------------------------------------------------------

## Overview

This design fixes how the operator app's lists behave when the operator changes a filter, grouping, search or selection, and defines the URL format that carries every view state. It has four parts:

1. **URL view contract.** One grammar for filters, grouping, search, presets, the focused row and the checked rows. A shared codec (`parseView` / `formatView`) used by the app and the coordinator, and a `view_link` operation for agents.
2. **List data path.** The normalized entity store that [operator-app](fleet-operator-app.md) decision D4 already chose, which the current code does not implement. Lists keep their rows on screen while new data loads, the list element is created once, and search sends one request per typing pause.
3. **`task_page` query.** A side table `task_derived`, kept by Postgres triggers, so a page read no longer computes four subqueries for every task in the table.
4. **Asserting check.** `tests/ui/filter.check.ts` gains an asserting mode that fails when any of the above regresses.

The Node toolchain cleanup from the brief is a separate, later slice (see Future Improvements).

### Terms

* **Task:** a unit of production work (`task` table). **Execution:** one run of an agent for a task (`execution` table); the latest execution is the one with the highest `seq`. **Harness:** the agent runtime of an execution (`codex`, `claude`, `antigravity`); the list column "Agent" shows it. **Host:** the machine an execution runs on. **Question:** an open decision addressed to the operator (`question` table).
* **Conversation:** the operator app's name for a session (`session` document); the Conversations view reads `sessions_list`.
* **Operation:** a named coordinator function in one table, served as `POST /api/ops/<name>` to the browser and as MCP tools to agents. The browser calls it through `op()` in `app/src/api.ts`; the coordinator dispatches it in `runOp`. Read operations (`READ_OPERATIONS`) run in a read transaction without a write lock.
* **`task_page`:** the operation that returns one page of the task list, plus counts per state and group summaries. **`list_membership`:** the operation that takes up to 1,000 changed ids and a list definition and returns which of them belong to that list now, with their rows and the list's counts. The client never decides membership itself (D4).
* **Change feed:** every write to a watched table adds a row to `change` through the trigger `fleet_change` (migration 8); the browser receives these over `/api/stream` and asks `list_membership` about the touched ids.
* **Scope:** the coordinator's write lock. Most writers hold GLOBAL; host ingest of one execution holds only that execution's scope and runs in parallel ([data foundation §12](fleet-data-foundation.md)). Row-level security limits every query to the projects in `fleet.project_ids`; the coordinator itself uses `*`.
* **Entity codes** such as `T-FLT-42` are decided in [design-system D17](../design-system/decisions.md) and allocated per [data foundation §2](fleet-data-foundation.md); they do not exist in the database yet.

## The Problem

Measured in the brief on local PGlite (in-process coordinator, generated tasks with every fifth task running, Chromium):

* Every filter, grouping or "All" change makes the state counters disappear, replaces the list with a loading skeleton, and creates the list element again. Scroll and focus are lost. Cause: `useLiveList` in `app/src/live.ts` calls `setPage(EMPTY)` on every key change; `StateCounters` draws nothing for undefined counts; `tasks.tsx` shows `<Skeleton>` while `!list.at`.
* Typing "hero" in search sends four `task_page` requests and blanks the list four times.
* `task_page` computes four correlated subqueries (current harness, last host, first start, open questions) for every task in the table before it filters, three times per call. At 20,000 tasks, grouping by agent takes 194 ms over HTTP and grows with history, not with the page. The operation layer adds about 1 to 2 ms and is not the cause.
* The URL carries only `state`, `group` and `q`. Presets live in local preferences, so a copied link does not reproduce the view, and agents have no documented way to build one.
* **Drift found:** D4 decided "one normalized entity store on Preact Signals". The current `live.ts` keeps each list in component state (`useState`) with no shared store. Going Back or switching nav tabs therefore starts from the IndexedDB copy or a skeleton.

## Goals

* A filter, grouping, search, preset or selection change keeps the filter bar, counters and current rows on screen. New data replaces them in place; the list element is never re-created after the first load.
* Back, Forward and returning to a view through the nav rail show the last known rows in the first frame, then refresh in place.
* Search sends one request per typing pause (no key for 300 ms); a newer request supersedes the older one.
* `task_page` over HTTP: median under 300 ms at 20,000 tasks for each filter and grouping case of the check, on local PGlite (asserted). The 20,000 → 100,000 growth factor is reported, not asserted, because counts and search stay proportional to the matching set (see §3).
* Every view state is in the URL in one canonical form. A link opens the same list, the same focused row and the same checked rows for any operator with access.
* Agents build and read view links through one operation, with the same code the app uses.
* `npm run check:filter` fails on any regression of the asserted goals.

## Non-Goals

* Holding the full active task set in the browser for local filtering.
* Derived columns for `sessions_list` (same pattern; next slice). The brief's goal for `sessions_list` is narrowed to the client part (Conversations uses the new store).
* Server-stored short links (`?s=<hash>`): added only when a real URL length limit is hit.
* Bulk actions. This design defines `sel`, a checkbox per row and keeps checked rows in the URL; the actions themselves are separate work.
* Entity code allocation and resolution. The codec passes codes through unchanged; resolving them (`entity.resolve`) and the redirect direction are the D17 slice's work.
* Node toolchain changes (vendoring, shared binaries, Bun trial).
* Full-text search indexes. `q` stays a substring match.

## Open Questions

* **Eviction of cached lists.** The store keeps the ordered ids of visited lists in memory. Whether and when to drop them is decided from a measurement of real memory use, not a number chosen now; until then a list's ids are dropped when the page reloads.
* **Live Postgres numbers.** Targets are set on PGlite. Production is re-measured after rollout with per-operation timing.

## Critical User Journeys (CUJs)

### CUJ 1: Change a filter without losing the screen

* **Given:** `/tasks` shows 100 rows grouped by priority, with counters.
* **When:** the operator clicks the "Failed" counter.
* **Then:** the URL becomes `/tasks?state=failed` (a new history entry); all counters stay visible with their counts; rows stay visible with an "updating" mark; within one server round trip the rows are replaced in place; the list element is the same DOM node; no skeleton appears.

### CUJ 2: Send a view to another operator

* **Given:** the operator has the "Needs me" preset active, grouped by agent, with task A focused and tasks B and C checked.
* **When:** they copy `/tasks/<A>?group=agent&preset=Needs%20me&sel=<B>,<C>&state=failed,waiting` and another operator opens it.
* **Then:** the second operator sees the same filters, grouping, focused row and checked rows, whatever presets they have saved. Rows they cannot see are absent; the link does not fail.

### CUJ 3: An agent links to a view

* **Given:** an agent wants to point the operator at failed tasks in project `fleet`.
* **When:** it calls `view_link` with `{ view: { view: "tasks", state: ["failed"], project: ["fleet"] } }`.
* **Then:** it receives `{ url: "https://fleet.ufobe.com/tasks?project=fleet&state=failed", path: "/tasks?project=fleet&state=failed" }`; calling `view_link` with `{ url }` returns the same view.

### CUJ 4: Type a search

* **Given:** `/tasks` is open.
* **When:** the operator types "hero" with less than 300 ms between keys.
* **Then:** one `task_page` request is sent after the pause; rows stay on screen while typing; the URL ends as `/tasks?q=hero`; one Back returns to `/tasks`.

### CUJ 5: Back and forward

* **Given:** the operator went from `/tasks` to `/tasks?state=failed` to `/tasks/<A>?state=failed`.
* **When:** they press Back twice.
* **Then:** each step shows the earlier list in the first frame from the store, then refreshes in place.

## Technical Plan (Architecture)

### 1. URL view contract

```text
/<view>[/<focus>]?<key>=<value>&...      keys in alphabetical order, defaults omitted
```

* **Views:** `tasks`, `conversations`, `hosts`, `overview`. Only `tasks` and `conversations` have keys today.
* **Focus (path segment):** the row shown in the detail pane. A UUID today. A code is passed through unchanged; the D17 slice decides resolution.
* **Keys, tasks view:**

  | Key | Value | Default (omitted) | `task_page` input |
  | --- | --- | --- | --- |
  | `group` | `priority`, `state`, `project`, `agent`, `host`, `none` | `priority` | `group` |
  | `preset` | preset name, a label only | none | not sent |
  | `project` | comma list of project ids; `-` means "no project" | all | `filter.projectId` (array; `-` becomes `null`) |
  | `q` | search text | empty | `filter.search` |
  | `sel` | comma list of checked rows | empty | not sent |
  | `state` | comma list of task states | all | `filter.state` |

* **Keys, conversations view:** `archived` (`1` or omitted → `filter.archived`), `q` (→ `filter.search`), `sel`, `state` (→ `filter.state`).
* **Lists:** comma-separated. A comma, `%` or `&` inside a value is percent-encoded (`%2C`, `%25`, `%26`). List values are sorted and de-duplicated.
* **Canonical form:** keys in alphabetical order, list values sorted, defaults and empty lists omitted, no trailing `?`. Two equal views produce byte-identical URLs.
* **List key:** the canonical query string without `focus`, `sel` and `preset`, prefixed by the view (`tasks?group=agent&state=failed`). It identifies a list in the store and in IndexedDB. Checking rows, focusing and the preset label never create a new list.
* **Focus and `sel` are independent.** The focused row need not be in `sel`; focusing does not change `sel`; checking does not change focus.
* **Presets:** a link carries the preset expanded into explicit keys, plus `preset=<name>` as an inert label. The codec never validates it. The app's preset bar highlights a preset only when the receiver has a preset of that name whose keys equal the URL's keys (the matching that `tasks.tsx` does today); otherwise the label is ignored.
* **History:** a discrete change (counter, grouping, preset, focus, checking a row) pushes a new entry. Search pushes one entry when the operator starts typing and replaces it while they keep typing, so one Back leaves the search. Today every change replaces; this is a behaviour change.
* **Unknown keys** are kept in `extra`, written back in alphabetical order with the known keys, ignored by the app, and not part of the list key. No version key is added until an incompatible change happens.
* **Meaning:** a link is a live query, not a snapshot. Access is the receiver's.
* **Length:** no fixed limit. If a real limit is hit (chat tools, Cloudflare Access, MCP transport), it is recorded and short links are designed then. URLs are never cut.
* **Privacy:** URLs carry search text and preset names into chats and logs; the app renders them as text only.

**Codec.** `packages/view-url/view-url.ts`, a module with no imports, compiled by both builds (see Detailed Implementation, Phase 1):

```ts
type ViewName = 'tasks' | 'conversations' | 'hosts' | 'overview';
type View = { view: ViewName; focus?: string; group?: string; preset?: string; project?: (string | null)[];
  q?: string; sel?: string[]; state?: string[]; archived?: boolean; extra?: Record<string, string> };
parseView(pathAndQuery: string): View      // tolerant: unknown keys go to extra; invalid values for known keys are dropped
formatView(v: View): string                // canonical path + query, no origin; defaults omitted
listKey(v: View): string                   // view + canonical query without focus, sel, preset, extra
toPageParams(v: View): { filter: { state?: string[]; search?: string; projectId?: (string | null)[]; archived?: boolean }; group?: string }
```

The allowed keys, defaults and values per view are one constant table in the module.

**Agent operation `view_link`.** In the shared operation table and in the agent operation table (`agent.operations`), so it is available on `/api/ops`, `/api/tools` and the agent MCP endpoints. It is a read operation without database access.

* Input `{ view }` → `{ url, path }`. `url` uses the configured `browserOrigin`; without one, `url` is `null` and `path` is returned.
* Input `{ url }` → `{ view }`. A URL on another origin, or a path that is not a view, is rejected with 400 `Not a Fleet view URL`. Both or neither input → 400.

### 2. List data path (entity store, D4)

```text
 URL ─parseView─▶ View ─listKey─▶ lists[key]   : signal<{ ids, keys, counts, groups, nextAfter, at, error }>
                                  records[id]  : signal<row>        (one per task or session, shared by all lists)
                                  docs[name]   : signal<data>       (task_get / session_get details, overview, hosts)
  task_page / sessions_list ─page─▶ records updated, lists[key].ids replaced
  feed changes ─▶ list_membership(touched ids, each mounted list) ─▶ records updated once; ids placed by keys[id]
  IndexedDB (D3, app/src/offline.ts) ◀── save per list key (ids, keys, counts, groups, the rows) and per doc
```

* **`app/src/store.ts` (new)** holds three maps of Preact signals: `records` by entity id, `lists` by list key, `docs` by name. A list holds ordered `ids`, a sort key per id (`keys[id]`, because the sort key depends on the grouping and cannot live in the shared record), counts, group summaries, `nextAfter`, `at` (time of the last server answer) and `error`.
* **Shown and wanted.** A mounted view has a slot with `wanted` (the list key from the URL) and `shown` (the last key that has data). When `wanted` changes and has data in memory or IndexedDB, it becomes `shown` at once. When it has none, the view keeps showing the `shown` list, marked pending, until the answer arrives; then it renders the new list in the same list element. Only when no list was ever shown does the view show the skeleton.
* **Never reset.** Revalidating a list replaces its `ids`, counts and groups in one signal write; nothing is cleared first.
* **Request order.** Each view slot has a sequence number and one `AbortController`. A new request for the slot aborts the previous one; an answer is applied only if it is the latest for its slot. The abort frees the browser; the server still finishes the query.
* **Paging.** `more()` reads `nextAfter` of the `shown` list and appends only if that list is still the one shown when the page arrives. It returns nothing; progress is visible through the list signal.
* **Changes.** One feed subscription for the whole store, started in `main.tsx`. For each change batch, the store collects touched ids per kind (tasks, sessions; `touched` in `lists.ts`) and calls `list_membership` once per mounted list (up to 1,000 ids per call; `chunks` in `lists.ts`). Answers update `records` once; each list places the ids by its own keys. If membership fails, the list revalidates with a full page, as today.
* **Group summaries on change.** `list_membership` returns `groups` for grouped task lists, so the extra `task_page` with `limit: 1` after each change goes away.
* **Unmounted lists** are not refreshed on changes. When their view mounts again, they render from memory and revalidate once.
* **Docs.** `useDoc(name, fetch, touches, refreshMs?)` replaces `useLiveRecord` with the same parameters. It keeps the previous value visible while re-reading. Task and session details, Overview and Hosts use it.
* **One mounted view.** `main.tsx` routes `/tasks/:id?` and `/conversations/:id?` as one route each, so focus changes never unmount the view. The list element is keyed by view only.
* **Search.** The input updates its own text at once; the URL and the request follow after 300 ms without a key press.
* **Visible states.** Pending: counters and rows keep their values and are dimmed with the existing muted text token, and the list header shows a thin progress bar. Error with rows: rows stay, an error banner says "Showing data from <time>". Answered and empty: the empty state.
* **Checkboxes.** Task and conversation rows get a leading checkbox cell before the state badge (OR-22 keeps the state badge as the first data column); Space on a focused row toggles it. Checked rows are the URL's `sel`.

### 3. `task_page` query: side table `task_derived`

Migration 10 in `packages/coordinator/db/schema.sql` (migration 9 is the artifact store):

* **Table** `task_derived(task_id text PRIMARY KEY REFERENCES task(id) DEFERRABLE INITIALLY DEFERRED, project_id text NULL, agent text NULL, host text NULL, started_at timestamptz NULL, open_questions integer NOT NULL DEFAULT 0)`, with row-level security like the other tables (policy on `project_id`, copied from the task).
  * `agent`: harness of the latest execution in `preparing` or `running`; null when none runs (shown in the "no agent" group).
  * `host`: host of the latest execution in any state.
  * `started_at`: earliest `started_at` of the task's executions.
  * `open_questions`: number of questions of the task in state `open`.
* **Function** `fleet_task_derived(p_task text)`, plpgsql, with the function-level settings `SET search_path = public` and `SET fleet.project_ids = '*'`. Every table has `FORCE ROW LEVEL SECURITY` and the policies read `fleet.project_ids`, so `SECURITY DEFINER` alone would not widen what the function sees; the function-level setting does, for the duration of the call only. The function takes `pg_advisory_xact_lock(hashtext('task_derived:' || p_task))`, reads the task's `project_id` and the four values from `task`, `execution` and `question`, and upserts `task_derived` only when a value differs (`ON CONFLICT ... DO UPDATE ... WHERE (...) IS DISTINCT FROM (...)`). It writes only `task_derived`. It never touches `task`.
* **Concurrency.** Today only GLOBAL writers change the trigger columns: execution-scope ingest writes `observation`, `last_seen_at`, `harness_version` and totals ([data foundation §12](fleet-data-foundation.md)), none of which fires the trigger. GLOBAL writers are serialized by the scope lock, so recomputes cannot race and derived-locks cannot form a cycle. This invariant is recorded in §12: *an execution-scope writer never writes `task_id`, `state`, `host_id`, `harness` or `started_at` of an execution, nor `task_id` or `state` of a question.* The advisory lock is kept as a second guard in case that invariant is broken later: it serializes recomputes of one task, and because it is taken before the reads, READ COMMITTED gives the second recompute a snapshot that includes the first's committed rows. When a trigger recomputes two tasks (a `task_id` move), it locks them in ascending id order.
* **Triggers:** `AFTER INSERT OR DELETE OR UPDATE OF task_id, state, host_id, harness, started_at ON execution` and `AFTER INSERT OR DELETE OR UPDATE OF task_id, state ON question`, `FOR EACH ROW`, calling the function for `NEW.task_id` and, when different, `OLD.task_id`; null task ids are skipped. The column lists keep heartbeats and output writes from firing the recompute.
* **Task triggers.** `AFTER INSERT OR UPDATE OF project_id ON task` calls the function, so every task has a `task_derived` row and its `project_id` follows the task when it moves between projects.
* **A child before its task.** Foreign keys are deferred, so an execution can be inserted before its task in the same transaction. The function then finds no task row and stores `project_id` null; the task's own insert trigger runs the function again and sets it.
* **Change feed:** a `task_derived_change` trigger calls `fleet_change`. The migration runs `CREATE OR REPLACE FUNCTION fleet_change()` with the whole one-line body of migration 8 restated with one more branch: table `task_derived` gives `k := 'task'; i := r.task_id; t := r.task_id;` and the row's `project_id`. The existing triggers stay as they are. Live lists refresh as for any task change. `task.updated_at` does not move on derived changes.
* **Backfill:** after the triggers are created, the migration inserts one `task_derived` row per existing task, computed by one `INSERT ... SELECT ... ON CONFLICT DO NOTHING`. It runs inside the migration's global write, so no writer runs in between. `task_derived_change` adds one change row per task; clients then resync from the change log, as after any large write. At 20,000 tasks the check records the backfill time.
* **Indexes (all created by migration 10):** `task_derived_agent ON task_derived(agent)`, `task_derived_host ON task_derived(host)`, and `task_priority_order ON task((CASE priority ...), seq DESC, id)` with the exact expression of the `priority` group, for the default page order. `execution_task_seq ON execution(task_id, seq DESC)` serves the recompute (only `execution_task ON execution(task_id)` exists today). `question_task` already covers questions.
* **Query.** `page` and `membership` in `packages/coordinator/tasks.ts` read `task t JOIN task_derived d ON d.task_id = t.id`; the CTE with correlated subqueries is removed. Filters: `state`, `projectId` as an array (`t.project_id = ANY($n)`, with `null` written as `t.project_id IS NULL`), `search`. Three statements, as today, each with filters on plain columns:
  * the page: `ORDER BY <group key>, t.seq DESC, t.id LIMIT n+1`;
  * per-state counts: the `WHERE` without the state filter, so every counter keeps its number;
  * group summaries: the full `WHERE`, `GROUP BY <group key>`.
  The group expression is taken from the fixed `GROUPS` map (rewritten for the aliases `t.` and `d.`, for example `priority`: `(CASE t.priority WHEN 'urgent' THEN '1' WHEN 'normal' THEN '2' ELSE '3' END)`, `agent`: `coalesce(d.agent,'~')`) and interpolated, as today; only map values are ever interpolated. The `label` expression in the group summary, which today interpolates the group name as a string literal, uses the same map instead.
* **Cost after the change.** The page read is an index-ordered read of `n` rows. Counts and group summaries remain a scan of the matching rows on plain columns (no subqueries). This is why the growth factor is reported, not asserted.
* **Drift check:** `derivedDrift()` in `tasks.ts` recomputes the four values for every task and returns `{ checked, drifted: [{ taskId, stored, expected }] }`. Used by a test in `npm test` and exposed as the read operation `task_derived_check`.

### 4. Asserting filter check

`tests/ui/filter.check.ts [--assert] [--sizes 3000,20000,100000]`. Each size gets a fresh in-memory store and coordinator. Without `--assert` it only prints. With `--assert` it exits 1 when a rule fails at 3,000 or 20,000 tasks; the 100,000-task run is always report-only. Rules:

* the `[data-ui=data-list]` element is the same node before and after a filter, grouping, "All", preset, focus and check change;
* no `.skeleton` appears after the first load;
* every state counter that was visible stays visible during a change (checked each animation frame);
* typing "hero" with 60 ms between keys sends exactly one `task_page`;
* each filter and grouping case settles within 300 ms at 20,000 tasks;
* after Back, rows are present in the first animation frame after `popstate`;
* focusing a row does not change `sel`, and checking a row does not change focus;
* a URL copied from one page and opened in a second page reproduces focus, `sel`, filters and grouping;
* `task_page` over HTTP: median under 300 ms at 20,000 tasks per case.

It also prints the fixture load time, the migration 10 backfill time and the growth factor.

## Alternatives Considered

### Alternative A: Selection only in the path, or only in `sel=`

* **Pros:** one concept.
* **Cons:** the path alone cannot hold checked rows for bulk actions; `sel=` alone breaks every `/tasks/:id` link and the detail route.
* **Reason for Rejection:** Rejected by author: focus and checked rows are both needed and kept independent.

### Alternative B: Rewrite the query only, no stored values

* **Pros:** no schema change; computing values for page rows only is enough for display fields.
* **Cons:** grouping or ordering by agent or host needs the value for every matching task, so the unfiltered grouped list keeps a subquery per task and grows with execution history.
* **Reason for Rejection:** Rejected by author. The review (zoom-out lens) contests this and asks for a measurement first; the owner kept stored values, accepting their upkeep (triggers, row-level security on the side table, a drift check).

### Alternative C: Stored columns on `task`, written by triggers

* **Pros:** no join.
* **Cons:** every derived change would run `task_touch` (moving `task.updated_at`, which the list shows and the rollback export selects on) and add task change rows with no task edit; derived writes would contend with operator edits on the same task row; `task` row-level security and derived values would be coupled in one table.
* **Reason for Rejection:** Rejected by author after review: the side table keeps derived writes off the task row.

### Alternative D: Stored values written by coordinator code

* **Cons:** every write path, including legacy ones, must remember the update; a missed path drifts silently.
* **Reason for Rejection:** Rejected by author: triggers cover every path, as the change log does.

### Alternative E: Deferred recompute by the coordinator after commit

* **Pros:** no lock coupling.
* **Cons:** one more loop that must not stall; lists lag behind executions.
* **Reason for Rejection:** Rejected by author: triggers keep values current at commit with no extra loop.

### Alternative F: Per-component list state, or a page cache keyed by URL

* **Cons:** per-component state still starts from IndexedDB or a skeleton on Back; a URL-keyed page cache updates a changed task in each cached page only after membership runs for that page.
* **Reason for Rejection:** Rejected by author: D4's normalized store updates a record once for every list.

### Alternative G: Another front-end stack

* **Pros:** see the brief (fine-grained updates, no npm for WASM).
* **Cons:** the causes are the data path and the query, which carry over; the rewrite costs `packages/ui`, the design book and `check:design`.
* **Reason for Rejection:** Rejected by author.

--------------------------------------------------------------------------------

## Non-Functional Requirements

* **Performance:** targets in Goals; the check measures them.
* **Write cost:** an execution or question write that changes a watched column adds one function call, one advisory lock and at most one upsert of `task_derived`. The check records the fixture load time before and after migration 10.
* **Security:** `fleet_task_derived` sets `fleet.project_ids` to `*` for its own call, so it reads all of one task's rows whatever the writer's scope. It takes only a task id, writes only `task_derived`, and copies the task's `project_id`, so reads of `task_derived` and its change rows stay scoped. `view_link` builds URLs only on the configured origin and never includes credentials. A shared link grants nothing.
* **Rollout and rollback:** migration 10 is additive. The reading code ships in the same release. If values drift, `task_derived_check` finds them; re-running `fleet_task_derived` for the drifted ids repairs them (one SQL statement, documented in the check's output). The old query stays as the reference in the tasks test.

--------------------------------------------------------------------------------

### Additional Resources

* [web-stack-brief.md](web-stack-brief.md)
* [fleet-operator-app.md](fleet-operator-app.md) (D3 offline copy, D4 entity store, routing)
* [fleet-data-foundation.md](fleet-data-foundation.md) (§2 task, §12 concurrency model, G2 derived values)
* [design-system decisions](../design-system/decisions.md) (D17 entity codes, OR-22 via owner rules)
* Review reports: [reviews/fleet-view-urls/](reviews/fleet-view-urls/)

### Future Improvements

* **Node toolchain slice (planned):** exact pins, `--ignore-scripts`, an allowlist and audit policy, a decision on `better-sqlite3`, a shared content-addressed store for esbuild, tsc and Playwright outside the worktrees, the six front-end packages vendored as hashed ESM, and a Bun trial for tooling. See the brief, Key Decisions item 6.
* **`sessions_list`** with the same side-table pattern.
* **Overview** calls `task_page` once per project; it gets faster with §3, and one aggregate read would remove the per-project calls.
* **Per-operation timing** in `runOp` and a slow-query log, needed for the production re-measurement.
* **Counts at very large sizes:** cached counters if the reported growth factor shows counts dominate.

--------------------------------------------------------------------------------

## Detailed Implementation Plan

Six phases. Each ends with `npm run build` and `npm test` passing and is one commit.

### Phase 1: Codec

#### 1. `packages/view-url/view-url.ts` (Create)

* **Type / Scope:** TypeScript module, no imports.
* **Proposed Changes:** `ViewName`, `View`, the key table per view, `parseView`, `formatView`, `listKey`, `toPageParams` as in Technical Plan §1. Kept outside `packages/ui` because the root `tsconfig.json` excludes `packages/ui`; the root build compiles it to `dist/packages/view-url/view-url.js` for the coordinator, and esbuild bundles it for the app.

#### 2. `packages/ui/src/index.ts` (Modify)

* **Proposed Changes:** re-export the codec (`export * from '../../view-url/view-url'`) so the app keeps one import path.

#### 3. `app/tsconfig.json` (Modify)

* **Proposed Changes:** add `"../packages/view-url"` to `include` (paths there are relative to `app/`).

#### 4. `tests/ui/view-url.test.ts` (Create)

* **Type / Scope:** unit test (`node:test`, run by `npm test`).
* **Proposed Changes:** round trip per view; canonical ordering of keys and values; defaults omitted; percent-encoded comma; `project=-` as null; invalid values dropped; `archived`; empty `sel` omitted; focus not in `sel` kept; `preset` kept as given; unknown keys kept in `extra` and excluded from `listKey`; today's links (`/tasks?state=a,b&group=agent&q=x`, `/tasks/<uuid>`) parse.

### Phase 2: Derived table and query

#### 5. `packages/coordinator/db/schema.sql` (Modify)

* **Proposed Changes:** `-- migration 10: ...` section with the table, row-level security, `fleet_task_derived`, the triggers, the `fleet_change` replacement that adds the `task_derived` kind line, the indexes and the backfill (Technical Plan §3). The migration runner splits statements at a semicolon that ends a line, so each function is written on one line, as migration 8 does.

#### 6. `packages/coordinator/tasks.ts` (Modify)

* **Proposed Changes:** `page` and `membership` join `task_derived`; the CTE `base` is removed; `PageFilter.projectId` becomes `(string | null)[] | string | null` (a single value is still accepted); three statements as in §3; group name as a parameter; `derivedDrift()` added (the task insert trigger creates the `task_derived` row).

#### 7. `tests/coordinator/task-derived.test.ts` (Create)

* **Type / Scope:** coordinator test on PGlite.
* **Proposed Changes:**
  * Through store code paths: create, first execution, execution state changes, pause, resume, cancel, question ask and answer; after each step assert the `task_derived` values and an empty `derivedDrift()`.
  * Execution moved to another task and a deleted execution, through raw SQL (no store path deletes executions); both tasks recomputed.
  * A change row of kind `task` appears when a derived value changes and none when it does not; `task.updated_at` unchanged.
  * Row-level security, with the pattern of the existing test in `tasks.test.ts` (`SET LOCAL ROLE fleet_app_test`, a `NOBYPASSRLS` role, because PGlite's default user is a superuser): a writer scoped to `p1` writing an execution of a task whose questions are in project `q` stores the full count; a task moved from `p1` to `q` moves its `task_derived` row and is no longer visible to a `p1` reader.
  * An execution inserted before its task in one transaction: after commit, `task_derived.project_id` equals the task's.
  * Two concurrent transactions with raw SQL (two PGlite connections are not available, so this runs only when a Postgres `DATABASE_URL` is set, otherwise skipped with a message): both insert executions of one task; final values are correct.
  * Backfill: `migrate(9)`, write data, `migrate()`, assert values.

#### 8. `tests/coordinator/tasks.test.ts` (Modify)

* **Proposed Changes:** the old CTE query is kept in the test as the reference; for a fixed fixture, the new `page` returns the same rows, order, counts and groups for every group, with and without a state filter, with search, with one and several projects.

### Phase 3: Coordinator operations

#### 9. `packages/coordinator/app.ts` (Modify)

* **Proposed Changes:** `task_derived_check` in `typedOps`; `list_membership` returns `groups` for grouped task lists; `agentTools(...)` receives a new `browserOrigin` dependency from `config.browserOrigin`.

#### 10. `packages/coordinator/agent-tools.ts` (Modify)

* **Proposed Changes:** `view_link` is defined here (imports `../view-url/view-url.js`), because agent MCP calls dispatch only `agent.operations`, and `app.ts` spreads `agent.operations` into the shared table, so browser `/api/ops` gets it too. Add it to `operations`, to `descriptions` ("Build a Fleet operator-app link from a view, or parse a link into a view"), and to `bound()` as a branch that passes the arguments through without adding `sessionId` or checking ownership. A new `browserOrigin?: string` field in the `deps` type supplies the origin.

#### 11. `packages/coordinator/store.ts` (Modify)

* **Proposed Changes:** add `view_link` and `task_derived_check` to `READ_OPERATIONS`.

#### 12. `tests/coordinator/api.test.ts` (Modify)

* **Proposed Changes:** `view_link` both directions, foreign origin 400, missing origin returns `url: null`; `task_derived_check` returns no drift on fixtures; `list_membership` returns groups.

### Phase 4: App store and views

#### 13. `app/src/store.ts` (Create)

* **Type / Scope:** entity store on Preact Signals.
* **Proposed Changes:** `records`, `lists`, `docs`; `useList(kind, view)` returning `{ rows, counts, groups, pending, error, shownKey, more }`; `useDoc(name, fetch, touches, refreshMs?)`; one feed subscription (`startStore()`); IndexedDB through `offline.ts` `save(key, data, currentCursor())` / `load` with keys `list:<listKey>` and `doc:<name>` (the cursor is the feed cursor at the time of the answer, as today, so `lowestCursor()` keeps choosing where the feed resumes); per-slot sequence and `AbortController`; `freshness` moved from `live.ts`.

#### 14. `app/src/live.ts` (Delete)

* **Proposed Changes:** replaced by `store.ts` in the same commit as items 15 to 20.

#### 15. `app/src/lists.ts` (Modify)

* **Proposed Changes:** `applyMembership(ids: string[], keys: Record<string, SortKey>, changed: Iterable<string>, matched: Row[], complete: boolean): { ids, keys }`; the caller writes `records`. `touched`, `chunks` and `compareKeys` unchanged.

#### 16. `app/src/api.ts` (Modify)

* **Proposed Changes:** `op(name, body, { signal }?)`; an abort throws `OpError('aborted', 0)` and does not set `online` to false.

#### 17. `app/src/main.tsx` (Modify)

* **Proposed Changes:** `/tasks/:id?` and `/conversations/:id?` as single routes; `startStore()` next to `startFeed`.

#### 18. `app/src/views/tasks.tsx` (Modify)

* **Proposed Changes:** view from `parseView`, writes through `formatView` (push for discrete changes; one push then replace for search); `useList`; 300 ms search debounce; skeleton only when nothing was ever shown; pending and error states; checkbox cell and `sel`; presets applied by expanding keys and highlighted by key match.

#### 19. `app/src/views/conversations.tsx` (Modify)

* **Proposed Changes:** the same for the sessions list.

#### 20. `app/src/views/overview.tsx`, `app/src/views/hosts.tsx` (Modify)

* **Proposed Changes:** `useLiveRecord` replaced by `useDoc` with the same arguments.

#### 21. `app/src/prefs.ts` (Modify)

* **Proposed Changes:** saved views store `{ name, view: View }`. An old entry `{ name, states, group }` is read as `{ name, view: { view: 'tasks', state: states, group } }` and written back in the new form.

#### 22. `tests/ui/lists.test.ts` (Modify)

* **Proposed Changes:** the new `applyMembership` signature; ordering by per-list keys; the same record placed differently in a priority list and an agent list; anchor kept when rows are inserted above.

### Phase 5: Controls, book and check

#### 23. `packages/ui/src/controls.tsx` (Modify)

* **Proposed Changes:** `StateCounters` gets a `pending` prop (dimmed); a `RowCheck` control (checkbox cell, Space toggles).

#### 24. `packages/ui/src/patterns.tsx` (Modify)

* **Proposed Changes:** `ListFrame` gets a `pending` prop that shows the thin progress bar in its header.

#### 25. `packages/ui/book/book.tsx` (Modify)

* **Proposed Changes:** two new component sections in the section list: `['state-counters', 'State counters', 'state-counters', ...]` showing normal and pending, and `['list-frame', 'List frame', 'list-view', ...]` showing pending and `RowCheck` rows, so `npm run check:design` covers them. Screen pages in `pages.tsx` are unchanged.

#### 26. `tests/ui/filter.check.ts` (Modify)

* **Proposed Changes:** `--assert` and `--sizes` as in Technical Plan §4; backfill timing.

#### 27. `package.json` (Modify)

* **Proposed Changes:** `check:filter`: `npm run build && tsx tests/ui/filter.check.ts --assert`.

### Phase 6: Docs

#### 28. `docs/designs/fleet-operator-app.md` (Modify)

* **Proposed Changes:** the routing section links to this design for the URL contract and marks the code-redirect direction as owned by the D17 slice; D4 is implemented by `app/src/store.ts`.

#### 29. `docs/designs/fleet-data-foundation.md` (Modify)

* **Proposed Changes:** §2: the `task_derived` table; G2: derived values are stored once in `task_derived`, kept by triggers and checked for drift; §12: the invariant that execution-scope writers never write trigger columns, and the advisory lock as a second guard with ascending id order.

#### 30. `docs/designs/fleet-operator-app.md` §6 (Modify, same commit as item 28)

* **Proposed Changes:** `view_link` and `task_derived_check` added to the operation list there (the only document that lists operator-app operations by name); `list_membership` gains `groups`.

### Verification Plan

* **Compilation/Build:** `npm run build`.
* **Unit Tests:** `npm test` (view-url, lists, task-derived, tasks, api).
* **Static Analysis & Linting:** `npx tsc -p app/tsconfig.json --noEmit`; `npm run check:design`.
* **E2E/Integration:** `npm run check:filter`; `npx tsx tests/ui/app.check.ts` reports no missing parts with the merged routes.
* **Metrics:** settle time per case at 3,000 and 20,000 tasks, `task_page` median over HTTP, growth factor to 100,000, fixture load and backfill time. Recorded in the PR.

--------------------------------------------------------------------------------

*Drafted with EGM*
