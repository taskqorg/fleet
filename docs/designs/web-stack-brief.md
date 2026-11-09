# Fleet web stack and front-end runtime behaviour

* **Author:** Alex Kirs
* **Date:** 2026-11-10
* **Status:** Brainstorm brief (EGM, 7 lenses). Input for `/egm:egm-draft`.

## Overview

The operator app at `fleet.ufobe.com` looks right but does not behave right. When the operator changes a filter, grouping or page, the lists are fetched again. While that happens the state counters disappear, the list is replaced by a loading skeleton, and then the whole list is drawn again. The owner wants:

* nothing on screen disappears or jumps; data is updated in place; 100 to 300 ms of waiting after a filter change is acceptable when the old rows stay visible;
* every view state (filters, grouping, selection, presets) in the URL, in one agreed format, so an operator can send a link to another operator, and agents can build and read such links;
* a worktree that holds only our code plus a short, explicit list of outside pieces, with no `node_modules` junk per worktree, no install scripts, and builds limited to the area that changed;
* "20% of the effort for 95% of the result".

Volumes: active lists hold tens of tasks; a busy test system produces thousands of tasks within hours; archives are larger; analytics history can reach billions of records. There are few users.

We measured first. The measurements show that the stack is not the cause. The causes are (1) client code that resets the list on every filter change and (2) a server query whose cost grows with the size of the whole task table. Both can be fixed inside the current stack (Preact, Signals, Wouter, esbuild, `packages/ui`). The supply-chain and `node_modules` concern is real and is solved in the toolchain, not by rewriting the front end.

## Measurements

Setup: Apple-silicon Mac; coordinator in process on PGlite with generated tasks (every fifth task has a running execution); built app in Chromium through a local proxy that adds the credential. Harness: `tests/ui/filter.check.ts` (run `npm run build && npx tsx tests/ui/filter.check.ts 3000`). It prints a report and never fails. The live site could not be measured: our service token gets a Cloudflare Access redirect on `fleet.ufobe.com`. All numbers are local PGlite numbers; production Postgres has not been measured.

### Server and operation layer (median of 7, 100-row page)

| Tasks in table | Case | `store.tasks.page` in process | `POST /api/ops/task_page` over HTTP |
| --- | --- | --- | --- |
| 3,000 | all, group by priority | 6.6 ms | 8.9 ms |
| 3,000 | state = in_progress | 5.0 ms | 6.3 ms |
| 3,000 | group by agent | 17.8 ms | 19.1 ms |
| 3,000 | search "hero" | 9.6 ms | 10.6 ms |
| 20,000 | all, group by priority | 31.7 ms | 51.4 ms |
| 20,000 | state = in_progress | 20.5 ms | 34.8 ms |
| 20,000 | group by agent | 129 ms | 194 ms |
| 20,000 | search "hero" | 65 ms | 110 ms |

Each response is about 35 KB for 100 rows.

* **The shared operation layer is not the bottleneck.** Dispatch (`runOp`), the read transaction, Fastify and JSON together add about 1 to 2 ms at 3,000 tasks. At 20,000 tasks the gap grows to 15 to 65 ms, most likely because PGlite runs the query on the same single thread that serves HTTP.
* **The query is the bottleneck, and it grows with the table, not the page.** `task_page` (`packages/coordinator/tasks.ts`) runs three queries: rows, counts per state, and group counts. Each one reads the CTE `x`, which computes four correlated subqueries (current agent, last host, first start, open questions) for every task in the table. Filters are applied only after that. A 100-row page therefore costs as much as the whole table. At "thousands of tasks per hour", the 300 ms budget runs out within hours. `sessions.ts` has the same pattern, and the Overview page calls `task_page` once per project.

### Browser behaviour (3,000 and 20,000 tasks)

| Action | Counters vanish | Skeleton shown | List element replaced | Requests | Time until new rows are steady |
| --- | --- | --- | --- | --- | --- |
| Click a state counter | yes | yes | yes | 1 `task_page` | 28 ms (3k), 58 ms (20k) |
| Change grouping to Agent | yes | yes | yes | 1 `task_page` | 30 ms (3k), 245 ms (20k) |
| Click "All" (the Σ counter) | yes | yes | yes | 1 `task_page` | 43 ms (3k) |
| Select a row | no | detail pane only | no | 1 `task_get` | below one frame |
| Type "hero" in search (60 ms per key) | yes, per key | yes, per key | yes, per key | 4 `task_page` | n/a |

Why it happens, in the code:

* `useLiveList` in `app/src/live.ts` calls `setPage(EMPTY)` whenever the filter key changes. `EMPTY` has `counts: {}` and `at: 0`.
* `StateCounters` (`packages/ui/src/controls.tsx`) draws nothing for a state whose count is undefined, so all counters disappear.
* `app/src/views/tasks.tsx` shows `<Skeleton>` while `!list.at`, so the list element is removed and later created again. Scroll position and focus are lost.
* Search writes the URL and starts a request on every key press, with no debounce and no cancel.
* Every key change also re-subscribes to the change feed and re-reads the IndexedDB copy.
* Row selection does not remount the list; `/tasks` and `/tasks/:id` render the same `Tasks` component. The suspicion about route remounts is not confirmed for selection.

### Toolchain

| Item | Measured |
| --- | --- |
| `npm ci` (warm npm cache) | 1.9 s, 230 packages (67 dev), 144 MB `node_modules` in each worktree |
| Packages with install scripts | 5: `better-sqlite3`, `esbuild` (two copies), `fsevents` (two copies) |
| Front-end runtime dependencies | 6 packages, about 2.3 MB on disk: `preact`, `@preact/signals`, `@preact/signals-core`, `wouter-preact`, `regexparam`, `@tanstack/virtual-core` |
| Largest packages | `@electric-sql/pglite` 26 MB, `typescript` 23 MB, `playwright` + core 13 MB, `better-sqlite3` 12 MB, `@aws-sdk` 11 MB + `@smithy` 7 MB, `tsx` 10 MB |
| App bundle, esbuild binary called directly | 15 ms |
| App bundle through `npx esbuild` | 0.37 s (most of it is `npx` start-up) |
| App bundle with `bun build` | 43 ms |
| `node app/build.mjs` (bundle, CSS, service worker, manifests) | 70 ms; output 151 KB raw, 45 KB gzip |
| `tsc` whole repo | 1.5 to 1.65 s |
| `tsc` for `app/` only | 1.1 s cold, 0.65 s incremental |

The front-end build is already close to instant. The feeling of slow builds comes from installing `node_modules` per worktree, from `npx` start-up, from Playwright and Chromium downloads, and from `cargo`. The npm build does not call `cargo`; the Rust crates build separately.

## Analysis

All seven lenses (design, codebase details, zoom-out, comprehension, implementation readiness, structure, inventive catalyst) agree with the main finding: the lag comes from client code and from how the query is built. A different stack would bring both problems with it.

### Contradictions and how to remove them

* **"Nothing moves" against "results are always fresh".** Keep the previous rows and counters on screen. Mark them as updating (dimmed, with a small progress mark). Swap in the new page in place when it arrives. The list element is created once and stays mounted. A skeleton appears only when there is nothing to show yet (first load with no stored copy).
* **"Rich rows" against "cost per page".** Apply the filters first and compute the extra columns (agent, host, first start, open questions) only for the rows on the page. Compute counts per state and per group in one pass. If that is still not enough, keep agent and host as columns that are updated on write, with a check for drift. After this, read cost depends on page size, not table size.
* **"Complete URL" against "URL length".** Encode the full state in flat parameters. Do not invent a length limit. Write down the real limits we hit (chat tools, Cloudflare Access, the MCP transport) when we measure them. A server-stored short link (`?s=<hash>`) is the fallback only if a real limit is reached. Never cut a URL silently.
* **"Use the ecosystem" against "no node_modules".** Vendor the six front-end packages into the repo as hashed ESM files. Keep tool binaries (esbuild, tsc, Playwright and Chromium) in one shared store outside the worktrees, addressed by content hash. A fresh worktree then needs no install step to build the app.

### Ideal result

One mounted list per view, fed by page replacements and change diffs, that does not depend on how big the data is. Its full state is a URL that both people and agents can build. The worktree contains our code and a short, reviewed list of outside files.

### Secondary findings

* **Paging after a filter change.** If old rows stay on screen, `more()` in `live.ts` could fetch the next page with the new filter but the old page's cursor. Store the filter key with each page and ignore a page whose key no longer matches.
* **The offline cache key** is `JSON.stringify(params)`, which depends on field order. Derive it from the canonical URL state, plus a version number.
* **The detail pane** (`useLiveRecord`) also blanks while it re-reads. Apply the same rule: keep the previous record until the new one arrives.
* **Stale rows must look stale.** Show a visible updating state, a clear empty state, and an error that keeps the rows on screen and labels them as old.
* **Group counts on change.** For grouped lists, `live.ts` calls `task_page` with `limit: 1` after every change, only to get group counts. `list_membership` can return them instead.
* **Visibility.** There is no timing per operation, no slow-query log in `runOp`, and no measurement of client settle time. Add them so the latency budget can be checked in production.
* **No failing test.** The harness only prints. Add an asserting mode, run at 3,000 and 20,000 tasks. It fails when the list element is replaced, when a skeleton appears after the first load, when counters go empty, when search sends more than one request per typing pause, or when the 20,000-task case takes longer than 300 ms to settle.
* **`better-sqlite3`** needs its install script to build a native module. It blocks `--ignore-scripts`, vendoring and a Bun trial. Decide its future first: a prebuilt binary in the shared store, Node's built-in `node:sqlite`, or removal (PGlite already covers local runs).
* **Pins.** `playwright` is `^1.56.1`, the only dependency that is not pinned exactly.
* **Holding the active set in the browser** for zero-wait filtering is a second data path and conflicts with D4 (the server decides list membership). Once the query is fixed and old rows stay on screen, the owner's bar is met without it. It is deferred, not adopted.

## URL state convention (proposal)

* **Shape:** `/<view>[/<code>]?<params>`. The selected item is in the path, as its human code (D17), for example `/tasks/T-FLT-42?state=failed,waiting&group=agent`. Detail URLs with a UUID keep working and redirect to the code.
* **Parameters:** flat, named after the input fields of the list operation, so an agent can map a link directly onto `POST /api/ops/task_page`: `state`, `project`, `agent`, `host`, `group`, `q`, `preset`. Collapsed groups and sort get names when they are built.
* **Lists of values:** comma-separated, as today (`state=failed,waiting`); a literal comma in a value is percent-encoded. Today's links keep working.
* **Canonical form:** keys in a fixed alphabetical order; values sorted; defaults omitted (for example `group=priority`). Equal views therefore give byte-identical URLs, and the URL can serve as the list key and the offline cache key.
* **Row order:** the server order is total and stable: group key, newest first, then id (already true in `task_page`).
* **Presets:** a shared link carries the preset expanded into explicit parameters; `preset=<name>` is only a label. The receiver sees the same view whatever their saved presets are. Explicit parameters override a preset.
* **History:** typing in search replaces the current history entry, debounced together with the request (about 150 ms) and with the previous request cancelled. Discrete changes (a counter, a grouping, a selection) push a new entry, so Back undoes them.
* **Unknown keys:** kept and ignored, not an error. A `v=` parameter is added only when an incompatible change happens.
* **Meaning:** a link is a live query, not a snapshot. The receiver sees current data for that query, limited by their own access.
* **Length:** no fixed limit (owner rule: no invented limits). Record real limits when we hit them; then add the `?s=<hash>` short link.
* **Agents:** one module, `parseView` / `formatView`, in `packages/`, used by the app router and by the coordinator. The coordinator exposes it as an operation (`view_link`: encode a view to a URL, decode a URL to a view), so MCP agents use the same code. Tests: a round trip for every view, and today's links still parse.

## Goals vs. Non-Goals

* **In-Scope Goals:**
  * Filter, grouping, search and selection changes keep the filter bar, counters and old rows on screen and update them in place.
  * One request per typing pause in search; stale requests cancelled.
  * `task_page` and `sessions_list` cost grows with page size, not with table size; 20,000-task cases under 300 ms over HTTP.
  * The URL convention above, with one shared codec for the app and agents.
  * An asserting filter-path check in `tests/ui`.
  * A worktree with no per-worktree `node_modules` for the front end: vendored front-end packages, shared tool binaries, exact pins, no install scripts.
* **Out-of-Scope Non-Goals:**
  * Rewriting the front end in another framework or language.
  * Holding the full active task set in the browser (deferred; would amend D4).
  * Changing the server runtime away from Node in this phase (a Bun trial for tooling comes first).
  * Archive and analytics views over billions of records (a separate, openly asynchronous surface with estimated counts).

## Alternatives Weighed

* **Svelte or Solid.** Fine-grained updates, small bundles. Rejected by author: the measured lag is a reset in our data hook and a slow query, which carry over unchanged; a rewrite costs `packages/ui`, the design book and `check:design`, and still needs npm packages.
* **Rust/WASM (Leptos).** Removes npm from the front end. Rejected by author: larger first load (WASM binary), slower edit-build cycle through `cargo`, which goes against "page work must not wait on Rust builds", and the same data-flow and query problems remain.
* **Go + htmx, server-driven UI.** Very small client, no front-end packages. Rejected by author: every filter change becomes a server round trip that re-renders HTML, which makes "nothing jumps" harder, not easier; the live change feed, offline copy and later image, video and 3D viewers need client code anyway.
* **Deno as the single runtime.** One binary, permission model. Rejected by author for now: higher compatibility risk with Fastify, PGlite, `pg` and native SQLite than Bun; can be re-tried later.
* **Bun as the server runtime now.** Rejected by author for this phase: Fastify, PGlite, `pg` and SQLite must be verified first. Bun for tooling (build, test, running TypeScript, hard-linked global cache, empty `trustedDependencies`) is staged as a trial.
* **Holding the whole active set client-side.** Zero-wait local filtering. Rejected by author for now: second data path, conflicts with D4, not needed once old rows stay and the query is fixed.
* **A fixed URL length limit (for example 2 KB).** Rejected by author: an invented limit; record real ones instead.
* **Server-stored named presets in links (`?p=<code>`).** Rejected by author: needs server state; expanding the preset into parameters gives the same result.
* **Removing the shared operation layer for the web.** Rejected by author: measured overhead is about 1 to 2 ms at 3,000 tasks.

## Key Decisions & Staging for Next Steps

1. **Stack (confirms design-system D4).** Keep Preact, Signals, Wouter, esbuild and `packages/ui`. Revisit only if another stack beats the fixed current stack, measured at 20,000 tasks, on settle time, bundle size, worktree size and install time, including the cost of porting `packages/ui`.
2. **URL convention.** Adopt the convention above; write it as its own decision and as `docs/designs/view-urls.md`, linked from the routing section of `fleet-operator-app.md`. Build `parseView` / `formatView` and the `view_link` operation first, because the list key and the offline key depend on it.
3. **Client behaviour.** Rewrite `useLiveList`: keep the previous page and counts marked as updating; skeleton only on first load; key stored with each page for `more()`; one feed subscription per list; IndexedDB restore only on mount; separate empty, error and stale states. Debounce and cancel search. Apply the same to `conversations.tsx` and `useLiveRecord`.
4. **Query.** Behind a flag, with a test that old and new queries return the same rows on the 20,000-task fixture: filters before enrichment, enrichment for page rows only, counts and groups in one pass, indexes `execution(task_id, seq DESC)` and `question(task_id) WHERE state = 'open'`. Then `sessions.ts` and the Overview page. Denormalized columns only if measurements still require them. Re-measure on production Postgres. Record in `fleet-data-foundation.md` §12.
5. **Checks.** Add the asserting mode to `tests/ui/filter.check.ts`; add per-operation timing and a client settle-time measurement.
6. **Toolchain, in stages, each reversible with `npm ci`:**
   1. Exact pins everywhere; `--ignore-scripts`; an allowlist and audit policy in one file; decide `better-sqlite3`; review `@clickhouse/client` and `@aws-sdk` (plain `fetch` may replace them).
   2. Shared, content-addressed store outside the worktrees for esbuild, tsc, Playwright and Chromium.
   3. Vendor the six front-end packages as hashed ESM files, with a check against the lockfile and one update command.
   4. Bun trial on a branch for tooling; the server runtime only after Fastify, PGlite, `pg` and SQLite pass.
   * Page work never triggers `cargo`; this is already true and stays a rule.
7. **Owner answers (2026-11-10):**
   * Multi-value parameters use comma lists (`?state=running,waiting`); a comma inside a value is escaped.
   * `sel=` is part of the contract now: a link opens the same list with the same selected row.
   * Next step: `/egm:egm-draft` for a short design doc, `docs/designs/fleet-view-urls-and-list-path.md`, covering the URL view contract, the list data path, the `task_page` query rewrite and the asserting mode of `tests/ui/filter.check.ts`. The Node toolchain cleanup (item 6) is a separate, later slice.
