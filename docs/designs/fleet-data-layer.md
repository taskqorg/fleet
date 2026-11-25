# Data layer: instant screens by default

Status: accepted (owner picked option A, 2026-11-25). Steps 0 to 2 are shipped; the remaining screens move one per pull request.

## Problem

On 2026-11-25 the web app loaded slowly and in pieces again, and reloaded on Alt-Tab, after an earlier fix. Measured on prod:

- A hidden tab reloaded itself on every release (59 a day), and the in-memory store was lost with it. Returning after Alt-Tab meant a cold load: 16-23 reads, 4-8 s.
- A focus during a coordinator restart cleared every view.
- Screens assembled 5-12 independent reads each and reread whole replies on every touching change. HAL0's `session_get` was 7.6 MB and was reread on every execution change.
- Server reads aggregated in Go and scanned whole tables. Under RLS they were 3-5 times slower than an EXPLAIN as postgres showed.

It drifted back because every guarantee lived in prose or in a per-view choice. Nothing measured requests per screen.

## Design

**A read mirror.** `app/src/persist.ts` keeps the last answer of every read, list and the person's settings in IndexedDB, under a hash of the person and grants that read them. A reload renders from it before any read answers. Another person or changed grants purge it.

**One read per screen.** A screen reads one view operation, marked `view: true` in its contract (`session_view` first). The server composes it from summaries: the head, the composer target, items as rows, cursors for lists. Details load on demand (`tool_output`, `execution_get`). `useView(name, request, {quiet})` in `app/src/store.ts` is that read. It takes no `every`. `contracts/gen-ts.mjs` generates `ViewName`.

**The change stream is the only reread.** A read is reread when a change's `touches` names its operation and the change concerns its entities (`store.ts:touchedBy`). The app never rereads on focus or visibility. A hidden tab reads nothing: reads asked for while it is hidden run once when it shows again, and `useOp` timers skip their ticks. A failed person check keeps the page. Only another person or changed grants clear it. A release never reloads a tab the person is away from. Reads keep object identity across rereads (`share.ts`).

**Server queries that indexes serve under RLS.** Measure as the app reads, under role `fleet_app` with RLS. A user condition can start from an index under RLS only when its functions are leakproof. Plain comparisons of timestamps and text are. Regular expressions, most casts and enum comparisons are not. So choose a page's rows by such columns first, then build only those rows (`ThreadRowsBefore`, `RunnerCallGroups`). Read costs priced once per execution (`execution_spend`) instead of pricing in Go.

## What keeps it from drifting

Help, not block: the right path is the easy one, and rules exist only where a regression would be silent.

- `useView` and `ViewName` are the documented read for a screen. `scripts/ui-context.ts` names them for every page.
- `go/contract/view_reply_test.go`: a view reply carries no stored blob field (`projectSnapshot`, `dispatch`, `workspaceSnapshot`, `agentConfig`, `botContext`, `nativeSessions`, `handoffs`, `definitions`, tool `input` or `output`).
- `tests/ui/mirror.check.ts` checks the following:
  - a reload renders before any read answers and reads each thing once;
  - another person's mirror stays hidden;
  - a failed person check keeps the page;
  - a hidden tab reads nothing on changes;
  - the update waits for the person.
- `scripts/page-budget.ts <path>...` measures a deployed screen: reads, bytes and settle time on open, reload, Alt-Tab and Back. Data-layer pull requests quote it. After a deploy, the migrated page and its neighbours are walked on prod (dark, 1440 and 390) and compared with their canvas.

## Proof so far

| Screen | Before | After |
|---|---|---|
| Reload (Tasks, Conversations) | 5.8-6.7 s, 16-23 reads | 0.5 s from the mirror, 0 answers before render |
| HAL0 session screen | `session_get` 7.6 MB, 2-14 s; 9-12 reads | `session_view` 15.5 KB; 5 reads |
| HAL0 thread page | 3.4 s server | ~1 s (rows by index, one publishing read per page) |
| `executions_runners` | ~4 s server | ~2.5 s; the rest is summing 20000 calls a poll |

## Next

- Move the remaining screens one per pull request: Home, Projects and Tasks, Library, Lab, the rest of Live and System.
- Incremental call groups for Runners: sums kept by the gateway ingest, not recomputed per poll.
- `ThreadTotal` counts blocks from each event's payload.
- An EXPLAIN check of view queries under `fleet_app` with RLS on a seeded schema, once seeded data makes plans meaningful.
