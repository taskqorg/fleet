# Fleet Conversations

*   **Author:** Alex Kirs
*   **Status:** In Review
*   **Date:** 2026-11-10
*   **Brief:** [conversations-brief.md](conversations-brief.md). Its "Owner Decisions" section is binding.

--------------------------------------------------------------------------------

## Overview

Conversations is the operator's main screen. This design rebuilds it so that one thread shows the whole history of one task: every operator message, agent reply, tool call, reasoning step, question and result from all of the task's sessions and executions, in one order. A session with no task is its own thread.

The screen opens at the newest entry and loads older entries as the operator scrolls up. A jump to any message loads entries on both sides of it. Threads have no size cap, and the row being read never moves (OR-23).

The work has three parts:

1.  A server read path that pages a thread in both directions from any point, and searches the whole thread.
2.  One typed transcript model, produced in one place, that the screen only renders.
3.  The screen itself: rows, agent mark, copy, question cards, result cards, the artifact viewer, a Results page and the controls.

## The Problem

The current screen loads a whole session at once and rebuilds the transcript on every render.

*   `session_get` (`packages/coordinator/app.ts:101`) reads all messages, executions, events and artifacts of a session in one response. `/api/sessions/:id/events` (`app.ts:103`) pages only forward, and it loads every event before it slices the page in memory. Opening a thread loads every event in the session, and the screen cannot open at the newest entry without loading everything before it.
*   A task's work is spread across several sessions and executions. The operator has to open each session to see the whole task.
*   `app/src/transcript.ts` reads reply text from `fleetNormalized.messages` and tool calls from the raw harness record `data.item`. Reading raw records caused the no-reply bug ([conversations-no-reply-rca.md](../conversations-no-reply-rca.md) §1). It also drops reasoning and plan items, sets every tool-call duration to 0, and filters all events once per execution, which is O(n²) per render.
*   `VirtualList` (`packages/ui/src/virtual-list.tsx`) keeps the anchor row when rows are inserted, but it has only `onEnd`. It cannot load upward, and nothing keeps position while older rows are added above.
*   Search filters loaded rows in memory, so it cannot find or jump to history that is not loaded.
*   There is no browser check that asserts Conversations behaviour (drift item "Conversations has no asserting browser check" in [drift-2026-11-10.md](reviews/drift-2026-11-10.md)).

## Terms

*   **OR-n**: owner rule n in [owner-rules.md](../design-system/owner-rules.md), for example OR-23 "Nothing jumps".
*   **Service zone**: the compact right-hand part of a row with id, state, agent, time, counters and actions (OR-21).
*   **`execution_totals`**: the per-execution table with wall time, tokens and cost.
*   **Drift item**: an entry in the drift reports under `docs/designs/reviews/` that lists gaps between designs and code.
*   **Lost-receipt case**: an execution whose final receipt from the host never arrived; fixture `d89cc199`.
*   **The book**: the design-system catalogue built by `pnpm run book`.
*   **Data foundation Phase 3**: Phase 3 of [fleet-data-foundation.md](fleet-data-foundation.md): the `artifact` table, artifact stores and placement, now on main.
*   **Block key**: `(seq, index)`, the event's `seq` and the block's position in that event's `blocks` array. One event can hold several blocks (a reply and a tool call), so every cursor, search hit and selection point uses the block key, not `seq` alone.
*   **Stages**: the work ships in five stages from the brief: 1 foundations, 2 single-session thread, 3 thread equals task, 4 results, 5 menu actions. The Detailed Implementation Plan lists the files of each stage.

## Goals

*   One thread per task; a taskless session is its own thread. A taskless session that later gets a task joins that task's thread, and nothing is copied.
*   The thread opens at the newest entry, loads older entries upward, and a jump to any entry loads entries on both sides of it. No size cap.
*   The row being read moves 0 px when rows load above, rows stream in, a row expands, or media finishes loading.
*   One typed block model (text, tool call, reasoning, question, result, boundary) produced by one normalizer. The screen only renders blocks.
*   Each reply shows its agent mark (harness icon, `opus-5.5 · high`); host, tokens, cost and duration show on hover.
*   Tool calls and reasoning are collapsed to one line, open on click and can be copied.
*   Copy works for one reply, one code block, a selection across messages, and the whole thread, all as markdown.
*   Question cards in the thread: open questions are answerable there or from Tasks with the same effect; answered questions stay, collapsed, with the answer.
*   Result cards in the thread and one shared artifact viewer (any file, plus image and video views). A Results page lists results and artifacts with Tasks-like filters.
*   Stop, steer and answer are always visible. Retry, fork, hand off and switch profile or model are in one menu.
*   Server-side search over the whole thread that can drive a jump.
*   An asserting browser check over real-shape fixtures that also measures and reports timings.

## Non-Goals

*   Numeric speed budgets or thread size limits. The check measures and reports; it asserts behaviour only.
*   A stored thread entity. The thread is derived from `task_id` or from the session.
*   A new windowing engine. `VirtualList` is extended.
*   Keeping the old view behind a flag.
*   New agent operations. Retry, fork, hand off and switch use the existing `agent-tools.ts` operations.
*   Editing or deleting artifacts from the viewer. Artifact lifecycle stays in the data foundation.

## Open Questions

Defaults below were chosen by the drafter so work is not blocked. The owner can overturn each one.

*   **Model label source.** Default: `format.ts` turns the stored model id into the short label (`claude-opus-5-5` becomes `opus-5.5`). No model registry is added.
*   **Old events without blocks.** Decided by the owner (2026-11-10): no. Fleet has no users yet, so old records are not migrated and no compatibility is kept. Events stored before blocks existed show no blocks; test tasks are deleted later. The executor `reproject` subcommand stays only as the tool that turns captured fixture records into the projection the executor writes.
*   **Session with executions for two tasks.** Default: its early taskless history belongs to the first task it got; the second task's thread shows the session from its first execution with that task. Alternative: show the early history in both threads.
*   **Steer while a question is open.** Default: steer text goes to the execution as a steer command, as today. It does not answer the question. The composer shows the open question card above it, so the operator sees that an answer is still pending.

## Critical User Journeys (CUJs)

### CUJ 1: Open a long task thread

*   **Given**: Task `T-FLT-42` has three sessions and nine executions with 40,000 events in total.
*   **When**: The operator opens the task in Conversations.
*   **Then**: The newest entries render first, at the bottom. Scrolling up loads older pages; the row the operator is reading does not move. Session and execution boundaries show as thin labelled rows.

### CUJ 2: Jump to a search hit

*   **Given**: The operator is at the bottom of the thread in CUJ 1.
*   **When**: The operator searches for `migration 6` and picks a hit from the first session.
*   **Then**: The server returns the hit's position. The list replaces its window with entries around the hit, the hit row is placed in view and marked, and scrolling either way loads more.

### CUJ 3: Answer a question in the thread

*   **Given**: An execution asked "Use Postgres 17?" and the question is open.
*   **When**: The operator clicks an option on the card in the thread.
*   **Then**: `questions_answer` runs with the card's request id. The card collapses to show the answer. The same question in Tasks shows answered from the live feed. A second click from Tasks with the same request id changes nothing.

### CUJ 4: Copy a cross-message selection

*   **Given**: The operator drags from the middle of reply 12 to the middle of reply 80, and most rows between them are outside the rendering window.
*   **When**: The operator presses Cmd+C.
*   **Then**: The clipboard holds markdown from the start offset in reply 12 to the end offset in reply 80, including the rows that were not in the page.

### CUJ 5: Stop one of two parallel executions

*   **Given**: Two executions of the task run at the same time.
*   **When**: The operator clicks Stop in one execution's boundary row.
*   **Then**: Only that execution gets a stop command. The composer still names the newest running execution as its target.

### CUJ 6: Open an artifact

*   **Given**: An execution produced a screenshot and a log file.
*   **When**: The operator clicks the result card in the thread.
*   **Then**: The artifact viewer opens in the side panel. The image shows in a fixed-ratio frame; the log shows as text, read in `Range` pages, with a download link. The Results page lists both under the task.

## Technical Plan (Architecture)

### Ordering key

Every row the thread needs already has one shared ordering number. `document.seq` is a `bigserial`, and the `execution`, `question` and `artifact` tables take their `seq` default from the same sequence (`packages/coordinator/db/schema.sql`). Events and operator messages are documents. Here `seq` is the row's `seq` column, not the per-execution `data.seq` that host events also carry (`packages/coordinator/recovery.ts:18`). So `seq` gives one total order across sessions and executions that overlap in time. The thread cursor is `seq`. No new ordering column is needed.

`seq` is taken at insert, not at commit, so two transactions can commit out of `seq` order. The operator app already solves this for its live feed: a read and the feed cursor come from one snapshot, and the feed (`change` table, ordered by `(xid, seq)` up to `pg_snapshot_xmin`) never skips a slow transaction ([fleet-operator-app.md](fleet-operator-app.md)). Thread reads copy that pattern:

*   Each `thread_entries` read runs in one snapshot and returns the change-feed cursor of that snapshot (`feedCursor`).
*   The client follows the feed from `feedCursor`. For a fed change in the thread, the client fetches that row's blocks with `thread_blocks` by the change's kind and id (a change row names the document id, not its `seq`) and places them by block key.
*   A row from a slow transaction can therefore arrive with a key lower than the client's newest loaded key. It is still placed by key, and `VirtualList` anchor keeping holds the row being read in place (OR-6). Nothing is skipped and nothing on screen moves.

### Thread scope

A thread key is `{ task: id }` or `{ session: id }`; the routes are `/api/threads/task/:id/...` and `/api/threads/session/:id/...`.

*   **Task thread**: all executions with `execution.task_id = <task>` (indexed by `execution_task_seq`). Their sessions are the set of `session_id` values. This includes a taskless session that got the task later, because its later executions carry the `task_id`; its earlier executions join through the session (see below).
*   **Session thread**: one session that has no task. Once the session gets a task, its URL opens the task thread.

Membership rule: an entry belongs to task T's thread if its execution has `task_id = T`, or if its execution has no `task_id` and T is the first task its session got (the session's lowest-`seq` execution with a `task_id`). This keeps a session's early taskless history in the task thread once the session joins the task (Owner Decision), and nothing is copied. The two-task case is an open question.

As a query: the session set is `SELECT DISTINCT session_id FROM execution WHERE task_id = T`; entries are rows of those sessions whose execution has `task_id = T`, plus rows whose execution has no `task_id` when T is that session's first task.

Joining a task (added 2026-11-10, owner request): `session_start` takes an optional `taskId` (a task of the same project); the session records it and every execution it starts carries that `task_id`. `session_task_attach({ sessionId, taskId })` attaches a taskless session: the session records the task, its next executions carry it, and its earlier taskless executions join the task thread by the membership rule; nothing is copied or rewritten. A session that already belongs to another task gets 409; the same call again returns `existing: true`. The attached task counts as the session's first task (D2). Both are agent tools as well (the PM agent and people-access use them); host agents may call them only for their own sessions and project.

Access: the thread operations run under the same project scope as every other read operation. A task's executions belong to the task's project, so plain scope filtering is enough.

### Read path: `thread_entries`

One coordinator operation, also served as `GET /api/threads/{task|session}/:id/entries`:

```text
thread_entries({ key, before?: BlockKey, after?: BlockKey, around?: BlockKey, limit })
  -> { entries: Block[], first: BlockKey, last: BlockKey, hasBefore: bool, hasAfter: bool, total?: number, feedCursor }
```

*   No cursor: the newest `limit` entries (open at newest).
*   `before`: the `limit` highest-key entries with key lower than `before`, returned in ascending key order.
*   `after`: the `limit` lowest-key entries with key higher than `after`.
*   `around`: `floor(limit/2)` entries before `around`, and the rest from `around` onward, including it. A short side is not refilled from the other side; `hasBefore` and `hasAfter` say where more exists.
*   `limit` counts entries (blocks) and is the page size the client asks for. The server rejects only non-positive or non-integer values; there is no maximum.
*   `total` is the entry count, for the "84 of 1,240" count (OR-11). It is returned only for the open read and for `around`; the client adds live entries to it. It is a count over the thread's index range; the browser check reports its time.

The query finds the thread's member executions (the membership rule), then reads events and messages through `document_execution (kind, executionId, seq)`, questions through `question_execution` and artifacts through `artifacts.list({ executionId })`, which merges the `artifact` table and legacy `artifact` documents, merged by key. It joins `execution` (harness, model, effort) only for the page rows. It reads only the page, never the whole session. `session_get` stops returning `events` and `results`; it keeps the session header fields.

Live entries arrive through the existing change feed (`app/src/feed.ts`) from `feedCursor`. The client inserts a fed entry whose key falls inside its loaded window `[first, last]`, or after `last` when `hasAfter = false`; it ignores other fed entries, because a later page read returns them. The "N new" pill counts only entries past `last`.

### Search: `thread_search`

```text
thread_search({ key, q, before?: BlockKey, limit }) -> { hits: { key: BlockKey, snippet }[], hasMore }
```

It searches the plain text of text, reasoning, tool-call and question blocks in the thread, newest first. The first version uses `ILIKE` on the stored block text over the same index range. Its time is measured by the browser check; a text index is added only when that measurement shows it is needed. A hit is opened with `thread_entries({ around: hit.key })`.

### Block model

One normalizer produces typed blocks. The screen and copy code only read blocks.

```text
Block =
  | { kind: 'text',      key, executionId, role: 'operator'|'agent', markdown, plain }
  | { kind: 'tool',      key, executionId, name, input, output, state, startedAt, endedAt }
  | { kind: 'reasoning', key, executionId, markdown, plain }
  | { kind: 'question',  key, executionId, questionId, text, options, state, answer?, answeredBy? }
  | { kind: 'result',    key, executionId, artifactIds, delivery? }
  | { kind: 'boundary',  key, sessionId, executionId, harness, model, effort, state, startedAt, endedAt }
```

*   `text`, `tool` and `reasoning` come from the executor. `harness::project` (`crates/executor/src/harness.rs:450`) already writes `fleetNormalized` per event. It gains a `blocks` array with these three kinds for every supported harness. Tool duration comes from the harness start and end records.
*   Streaming: delta records (projection with `deltaOf`) are stored as documents of kind `event-delta`, not `event`, so pages, search and export never scan them. When the stream ends, the executor writes one final block on the completing `event`. An open stream is shown as one entry at the end of the newest page, built in full from its `event-delta` rows; it is never split across a page edge.
*   Execution end: on an execution's terminal transition the coordinator writes one `event` of type `execution.ended`. It carries no text; its `seq` places the result block. A stream cut off without a final block (the execution ended first) is shown as one closed entry built from its `event-delta` rows at read time, so the executor stays the only producer of text blocks.
*   Records with no raw data or an unknown shape get one `tool` block named after the event type, with the raw JSON as output, so every event has `blocks`.
*   `question` and `boundary` come from the coordinator, from the `question` and `execution` rows, at their own `seq`. `result` comes from the `artifact` rows and the delivery, at the key of the `execution.ended` event.
*   `plain` is the block's text with stable character offsets. Selection and copy use it.

Old stored events are not re-projected (owner decision); `transcript.ts` no longer reads `data.item` after Stage 2.

Implementation notes (Stage 1):

*   `plain` equals `markdown` in this version; the coordinator sets it, the executor stores only `markdown`.
*   Stream ids are `<kind>:<n>` per execution (`text:1`, `reasoning:2`). The first chunk of a stream carries `opens: true`; the record that closes streams carries `closes: [ids]`. Readers find streams through `opens` rows and never scan every chunk.
*   Lifecycle records (thread start, usage, protocol writes, turn end) get an empty `blocks` array. Executor bookkeeping events (preparation, resource observations, process closed) are not harness records and have no projection. "Unknown shape" means an `unparsed` line, a `*.failed` record or a Claude error result: one `tool` block named after the record type, state `failed`, raw record as output.

### Agent mark

The `boundary` block and each agent `text` block carry the execution's `harness`, `model` and `effort`. `effort` is already stored in `execution.config_ref` (`packages/coordinator/executions.ts:47`); the thread reads `config_ref->>'effort'`. The row shows the harness icon and the short label in the right service zone (OR-21). The boundary row shows duration and cost as visible chips (OR-19); host and tokens are in their tooltips, one tooltip per chip (OR-20). Reply rows show only the harness icon and label, with the model id on hover. Fleet has no cost attention rule today, so the cost chip stays neutral; this design invents no threshold (OR-18 applies once such a rule exists).

### Rendering and nothing-jumps

`VirtualList` gains:

*   `onStart`, the mirror of `onEnd`, called once per list length when the first rows come into view.
*   Position keeping on prepend: before the new items commit, it records the anchor key and its offset; after measurement, in the same layout effect, it sets `scrollTop` so the anchor offset is unchanged. The existing anchor logic is reused.
*   `scrollToKey(key, align)` for jumps.

Rules, one invariant ("the anchor row moves 0 px"):

*   Expanding a tool call or reasoning row opens downward; the clicked header stays where it is.
*   Media gets a fixed-ratio placeholder from the artifact's stored size before it loads.
*   Streaming grows only the newest row. Following stops when the operator scrolls up, and the "N new" pill shows (OR-7).
*   The loading bar keeps its space (as in commit `55746cc`).

### Copy

*   **Reply and code block**: a copy icon in the row's service zone and on each code block. Reply copy gives the block's `markdown`.
*   **Cross-message selection**: selection is app state, `{ start: { key, offset }, end: { key, offset } }`, set from pointer and Shift+click on rendered rows and from Cmd+A. The app draws the highlight. Cmd+C calls `navigator.clipboard.write([new ClipboardItem({ 'text/plain': promise })])` synchronously in the key handler, where the promise is `thread_export({ key, from: start, to: end })`. If the write fails, the status line says so and offers a retry button.
*   **Whole thread**: the same `thread_export({ key })` without bounds. There is one markdown builder, on the server. The HTTP route streams the markdown as `text/markdown`, so a long thread is not held in memory as one string.

```text
thread_export({ key, from?: { key, offset }, to?: { key, offset } }) -> { markdown: string }
```

Offsets cut `text` and `reasoning` blocks by their `plain` offsets. Other blocks (tool, question, result, boundary) are included whole when their key is inside the range, each as a short markdown line or a fenced block for tool input and output.

Native browser selection is kept inside one row, for copying part of a reply.

### Questions

The thread renders `question` blocks with the existing `DecisionBlock` pattern. Answering calls `questions_answer` (`app.ts:319`), the call Tasks uses. `tasks.answer` (`packages/coordinator/tasks.ts:79`) already returns the stored question when the same answer repeats and fails with 409 when a different answer arrives; the card then shows the stored answer and who gave it. Its read is not locked, so two different answers sent at the same moment can both pass; the update becomes `UPDATE ... WHERE state = 'open'` so only one wins. The card updates from the change feed. Answered cards collapse to one line with the answer.

### Results and the artifact viewer

Data foundation Phase 3 is on main: `artifact` rows, artifact stores and placement exist, and `artifact_list` and `artifact_get` exist (`app.ts:287`).

*   **Result block**: one per execution end that has artifacts or a delivery, at the key of the `execution.ended` event. A delivery or artifact that arrives later updates this block in place; it does not move (OR-23). It shows artifact names, the delivery-stage chip and links each artifact to its Results page entry.
*   **Delivery stage** (Owner Decision): the existing `stages` in `packages/ui/src/registry.ts`, rendered by `StageChip`. The coordinator maps the `delivery` record state to a stage key:

    | Delivery state | Stage key |
    |---|---|
    | `pr-open` | `pr_open` |
    | `integrating` | `merging` |
    | `merged`, `published` | `merged` |
    | any other state | no chip; the task state shows failures |

    The delivery record has no check status today, so `checks` and `checks_failed` are not shown until delivery records one. No check state is guessed.

*   **Viewer**: the existing `ResultViewer` (`packages/ui/src/controls.tsx:269`) gains image, video and text views. It opens in the side panel (OR-14). It chooses a view from the stored `media_type`: image, video, text (read in `Range` pages as the operator scrolls, no cap), and download-only for other types. HTML and SVG are shown as text or in a sandboxed `iframe` with no scripts and no same-origin access; they are never inlined into the page. Bytes come from the existing `GET /api/results/:id` (`app.ts:213`). The route's CSP drops `allow-scripts` and `script-src 'unsafe-inline'`, and the route gains `Range`, `X-Content-Type-Options: nosniff`, and `Content-Disposition: attachment` for types without a view.
*   **Results page**: a new app section listing `artifact` rows with task, project, execution, media type, state and time filters, using the Tasks list pattern (state icon first, OR-22). It uses the same `ResultViewer`. Conversations result cards link into it.

Artifacts live in two places: the `artifact` table and legacy `document` records of kind `artifact`. `artifacts.list` (`packages/coordinator/artifacts.ts:193`) already merges both, so the thread, the viewer and the Results page read artifacts only through it and `artifact_get`.

### Controls

Stop, steer and answer are always visible. The rest are in one left-aligned menu with icons (OR-13), in reserved space (OR-23).

*   **Composer target**: the newest running execution of the thread, named in the composer. When nothing runs, the composer sends a new message to the thread's newest session with `message_send`, which starts an execution, and steer is not shown. Each `boundary` row of a running execution has its own Stop (Owner Decision).
*   **Stop**: `execution_stop` (`app.ts:161`) on that execution.
*   **Steer**: `message_send` with `delivery: 'steer'` on the target execution's session; queued when the host cannot steer (`agent-tools.ts:91`).

Menu actions:

| Action | Operation | Kept | New | Where it shows |
|---|---|---|---|---|
| Retry | `message_send` with the last operator message, same session | session, task | execution | same thread, new boundary |
| Switch profile or model | `session_transition` | task, session when `reuse` or `switch_model_in_session` | execution; new session when `create_with_handoff` | same thread, new boundary |
| Fork | `session_fork` | original session unchanged, task | session and execution | same thread, new boundary |
| Hand off | `session_transition` with another harness or account (`create_with_handoff`) | task, context handoff record | session and execution | same thread, new boundary |

Hand off is the existing context handoff in `agent-tools.ts:46`. Quota-driven handoff is not part of this design (it is also out of scope in [fleet-data-foundation.md](fleet-data-foundation.md)).

### Data flow

```text
harness stdout --> executor harness::project --> event doc { data.fleetNormalized.blocks }
                                                      |
question / artifact / execution rows (same seq) ------+
                                                      v
                         coordinator thread_entries / thread_search / thread_export
                                                      |
             change feed (live) ---------------------+
                                                      v
     app: threads.ts window state --> VirtualList (onStart/onEnd/scrollToKey) --> row renderers
                                                      |
                              selection state --> copy (thread_export with range)
```

### Browser check

`tests/ui/conversations.check.ts` runs the built app against a coordinator loaded with fixtures captured from real events: codex `6a55ab8b`, claude `127d6fd7` and the lost-receipt case `d89cc199`, with secrets removed. A coordinator test asserts that each fixture's shape matches what the executor writes, so a wrong fixture fails.

It asserts: newest entry visible on open; scroll up loads older pages with 0 px anchor drift; jump to a search hit; expand keeps the header in place; streaming append with the pill; one question answer; reply copy (cross-message copy is asserted from Stage 3, which builds selection); stop on one of two executions. The fixtures store the executor projection of each native record; the coordinator test checks it equals `fleet-executor reproject` output. It prints, without thresholds: time to first paint of the newest reply, page round-trip time, search time, and anchor drift.

## Alternatives Considered

### Alternative A: Numeric speed budgets in the check

*   **Pros**: A regression fails CI.
*   **Cons**: The numbers are not derived from measurements.
*   **Reason for Rejection**: Rejected by author ([brief](conversations-brief.md), Alternatives Weighed): no invented limits. The check measures and reports.

### Alternative B: Hand-written fixtures

*   **Pros**: Small and readable.
*   **Cons**: A wrong fixture shape hid the no-reply bug.
*   **Reason for Rejection**: Rejected by author: fixtures come from captured real events, with a shape check.

### Alternative C: One session per thread, task shown as links

*   **Pros**: No cross-session merge.
*   **Cons**: Drops the owner's premise that a thread is a task.
*   **Reason for Rejection**: Rejected by author. Thread equals task; the shared `seq` makes the merge a sorted read.

### Alternative D: A stored thread entity

*   **Pros**: One row to page from.
*   **Cons**: A second copy of membership that can drift from `task_id`.
*   **Reason for Rejection**: Rejected by author: derive the thread from `task_id` or the session.

### Alternative E: Normalizing in the browser from raw records

*   **Pros**: No executor change.
*   **Cons**: This is the current design and the cause of the no-reply bug; each harness repeats it.
*   **Reason for Rejection**: Rejected by author: one normalizer, in the executor, which already projects each event.

### Alternative F: Native browser selection across messages

*   **Pros**: No custom selection code.
*   **Cons**: Rows outside the rendering window are not in the page, so the selection cannot span them.
*   **Reason for Rejection**: Rejected by author: selection is app state with offsets.

### Alternative G: A separate windowing engine or the old view behind a flag

*   **Pros**: Isolates risk.
*   **Cons**: Two engines or two views to maintain.
*   **Reason for Rejection**: Rejected by author: extend `VirtualList`; fix forward at the dev stage.

### Alternative H: A new ordering column (wall-clock or per-thread counter)

*   **Pros**: Reads as event time.
*   **Cons**: Clocks drift between hosts, so rows could reorder after display; a counter needs a write path and a backfill.
*   **Reason for Rejection**: Rejected by author: the shared `seq` already exists and gives a total order; a snapshot read plus the feed cursor makes it safe to page and append.

--------------------------------------------------------------------------------

## Non-Functional Requirements

*   **Security**: Artifacts and tool output are untrusted agent output. The viewer reads text in `Range` pages, and shows HTML and SVG only as text or in a sandboxed `iframe` without scripts or same-origin access. Markdown in replies is rendered without raw HTML. Fixtures have secrets removed before they are committed.
*   **Performance**: No budgets. Every read is a page over an index range; no operation loads a whole session. The browser check reports timings so regressions are visible.
*   **Correctness**: Thread reads return a feed cursor from the same snapshot, so no row is skipped; late rows are placed by key under anchor keeping.
*   **Rollout**: One change set per stage, no flag. Old records are not re-projected (owner decision 2026-11-10); a pre-deploy `pg_dump` is the only safety step.

## Risks & Mitigations

| Risk | Mitigation |
|---|---|
| A slow transaction commits a row with a lower key after the client loaded newer rows. | Snapshot-plus-feed-cursor read; the row is placed by key and anchor keeping holds the reading position. Test case in `threads.test.ts`. |
| A reply cut off before its final block is not found by search (search reads only final blocks). | Accepted trade-off; it is shown in the thread. |
| Search with `ILIKE` is slow on long threads. | The browser check reports search time; a text index is added when the measurement shows the need. |
| `total` count is slow on long threads. | Returned only on open and jump; its time is reported. |
| An event has no raw record or an unknown shape. | It gets one `tool` block with its raw JSON, so nothing is hidden. |
| Untrusted artifact content runs script in the operator's browser. | Sandboxed `iframe` without scripts, `nosniff`, CSP without `allow-scripts`. |
| Clipboard write fails after the server round trip. | `ClipboardItem` with a promise created in the key handler; failure shown in the status line with retry. |
| Two different answers race. | `UPDATE ... WHERE state = 'open'`; the loser gets 409 and sees the stored answer. |

## Additional Resources

*   [conversations-brief.md](conversations-brief.md)
*   [fleet-data-foundation.md](fleet-data-foundation.md)
*   [fleet-operator-app.md](fleet-operator-app.md)
*   [../design-system/owner-rules.md](../design-system/owner-rules.md)
*   [conversations-no-reply-rca.md](../conversations-no-reply-rca.md)

## Future Improvements

*   **Text index for search**: add when the measured search time shows the need.

--------------------------------------------------------------------------------

## Detailed Implementation Plan

Stages follow the brief: 1 foundations, 2 single-session thread, 3 thread equals task, 4 results, 5 menu actions. Stage 4 can run in parallel with Stage 2, because Phase 3 of [fleet-data-foundation.md](fleet-data-foundation.md) is live. Until Stage 4 lands, result blocks carry artifacts and no delivery chip.

Shared conventions for every item:

*   **Block key** in code: the string `` `${seq}:${index}` ``, compared numerically by `seq` then `index`. Coordinator-made blocks (`question`, `boundary`, `result`) use index 0 on their own row; the `execution.ended` event has an empty `blocks` array, so the `result` block at its `seq` takes index 0 without a collision.
*   **Snapshot and feed cursor**: thread operations are in `READ_OPERATIONS`, so they already run inside `store.read`, one consistent snapshot (`packages/coordinator/store.ts:11`). `feedCursor` is `store.changes.since(null).cursor` (`store.ts:138`) called inside that same read. Nothing new is added to the store.
*   **Stored event shape**: an `event` document's `data` holds `executionId`, `sessionId`, `seq` (per execution), `type` and `data` (the native record); `data.data.fleetNormalized` is the projection.

### Stage 1: Foundations

#### 1. `crates/executor/src/harness.rs` (Modify)

*   **Type / Scope**: Rust executor.
*   **Proposed Changes**: `project()` (line 450) adds `blocks` for every harness it handles. Mapping, to be confirmed against the captured fixtures in item 12:

    | Harness | Native record | Block |
    |---|---|---|
    | codex | `item.completed` with `item.type = agent_message` | `text` |
    | codex | `item.completed` with `item.type = reasoning` or `plan` | `reasoning` |
    | codex | `item.completed` with any other `item.type` (command, tool call, file change) | `tool`; `startedAt` from the matching `item.started` |
    | claude | `assistant` message, content part `text` | `text` |
    | claude | `assistant` message, content part `thinking` | `reasoning` |
    | claude | `user` message with `tool_result`, matched to its `tool_use` by id | `tool` on the `tool_result` event |
    | antigravity | `session/update` `agent_message_chunk` | `text` delta |
    | antigravity | `session/update` `agent_thought_chunk` | `reasoning` delta |
    | antigravity | `tool_call_update` with a final status, matched to its `tool_call` | `tool` on the update event |
    | any | record with no raw data or unknown shape | one `tool` block named after the record type, raw JSON as output |

    A tool block lives on the event that ends the tool; the executor keeps the start time of open tool ids per execution. Delta records replace today's `"delta": true` (line 508) with `deltaOf: <stream id>`, where the stream id is the native message id, or `<executionId>:<turn>` when the harness has none. When a stream ends, the completing record carries the final joined block. `messages` stays until item 19.

#### 2. `crates/executor/src/main.rs` (Modify)

*   **Type / Scope**: Rust executor CLI.
*   **Proposed Changes**: Each live execution keeps a `harness::Blocks` state. `main()` checks `argv[1]` first: `reproject` reads JSON lines `{executionId, harness, timestamp, record}` on stdin, writes one projection per line on stdout (block state per execution), and exits before any `FLEET_DATA_DIR` read, `executor.lock` or journal open. The fixture test uses it.

#### 3. Re-projection script (Dropped)

*   Dropped by the owner decision on old records. Migration 15 instead extends the change-feed trigger to kind `event-delta` (reported as kind `event`), so streams are followed live.

#### 4. `packages/coordinator/threads.ts` (Create)

*   **Type / Scope**: coordinator module.
*   **Proposed Changes**:
    *   `thread_entries`, `thread_search`, `thread_export` with the signatures in the Technical Plan, plus `thread_blocks({ key, rows: { kind, id }[] }) -> { entries: Block[], removed: BlockKey[] }`, which the client uses to fetch the blocks of rows named by fed changes (change kind and id; a chunk returns its whole stream entry; a closing event lists the stream entries it replaces in `removed`).
    *   Session set and membership, one query:

        ```text
        WITH first_task AS (
          SELECT DISTINCT ON (session_id) session_id, task_id
          FROM execution WHERE task_id IS NOT NULL ORDER BY session_id, seq),
        members AS (
          SELECT id FROM execution
          WHERE task_id = $T
             OR (task_id IS NULL AND session_id IN (SELECT session_id FROM first_task WHERE task_id = $T)))
        ```

        The task side uses `execution_task_seq`; `first_task` reads `execution_session (session_id, seq)` for the sessions found. For a session thread, `members` is the session's executions.
    *   Rows: `event` and message documents through `document_execution (kind, executionId, seq)` for the member executions; questions through `question_execution`; artifacts through `artifacts.list({ executionId })` (both sources); boundaries from the member `execution` rows. Merged by block key; `execution` is joined only for page rows.
    *   Streams: the `event-delta` rows of each open or cut-off stream are joined into one block, placed at the key of the stream's first delta.
    *   `replyText(executionId)`: the agent reply text of one execution, from final `text` blocks plus cut-off delta runs. Items 7a to 7c use it.
    *   `thread_export` HTTP route: streams markdown by reading pages, each page in its own `store.read`. A row committed between pages appears in a later page or not at all; it is never repeated, because pages advance by block key.
    *   Project scope: as every read operation.

#### 5. `packages/coordinator/app.ts` (Modify)

*   **Type / Scope**: routes and operations.
*   **Proposed Changes**: Register the four operations and `GET /api/threads/{task|session}/:id/entries`, `/blocks?rows=kind:id,…`, `/search`, `/export?from=seq:index@offset&to=…`. Dropping `events` and `results` from `session_get` and removing `/api/sessions/:id/events` move to Stage 2 item 18, because the Stage 1 screen still reads them.

#### 6. `packages/coordinator/recovery.ts` (Modify)

*   **Type / Scope**: event ingest.
*   **Proposed Changes**: In `ingest` (line 17 onward), store an event whose projection has `deltaOf` as kind `event-delta`, others as `event`. The replay lookup (`previous`), the `seq` collision lookup and the reserved-sequence lookup read both kinds, so a resent delta is still found.

#### 7. `packages/coordinator/executions.ts` (Modify)

*   **Type / Scope**: execution state transitions.
*   **Proposed Changes**: In `transition()` (line 116), when the new state is terminal and the row was not terminal before, write one `event` document with id `ended:<executionId>` (agent runs only; host event ids are `event:…` and ingest refuses type `execution.ended`, so a host cannot forge it), type `execution.ended`, empty `blocks`, in the same transaction. A repeated terminal transition returns early (`row.state === state`) and the fixed id prevents a second row.

#### 8. `packages/coordinator/delegation.ts`, `packages/coordinator/agent-tools.ts`, `packages/coordinator/delivery.ts` (Modify)

*   **Type / Scope**: other readers of `fleetNormalized.messages`.
*   **Proposed Changes**: `delegation.ts:52` and `agent-tools.ts:42` take reply text (`delivery.ts` reads no reply text; nothing to change there) from `replyText()` in `threads.ts` instead of `fleetNormalized.messages` and `data.item`.

#### 9. `packages/coordinator/tasks.ts` (Modify)

*   **Type / Scope**: question answer.
*   **Proposed Changes**: In `answer` (line 77), the write at line 84 becomes `UPDATE question SET ... WHERE id = $1 AND state = 'open' RETURNING *`. When no row is returned, reread the question: if the stored answer equals the submitted one, return it; otherwise fail with 409. The hook side effects and task transition (line 85 onward) run only when a row was updated.

#### 10. `packages/coordinator/store.ts` (Modify)

*   **Type / Scope**: read-operation set.
*   **Proposed Changes**: Add `thread_entries`, `thread_blocks`, `thread_search`, `thread_export` to `READ_OPERATIONS` (line 172).

#### 11. `packages/ui/src/virtual-list.tsx` (Modify)

*   **Type / Scope**: UI control.
*   **Proposed Changes**: New props and method:

    ```text
    onStart?: () => void            // once per list length when the first rows come into view
    scrollToKey(key: string, align: 'start' | 'center' | 'end'): void
    ```

    `scrollToKey` maps the key to its index and calls the existing `scrollToIndex(..., { align })` (line 81). Position keeping on prepend uses the existing `anchor` ref (`{ key, delta }`, line 39): it is captured before the items change and restored in the same layout effect after measurement. `onEnd` and `follow` are unchanged.

#### 12. `tests/coordinator/fixtures/conversations/codex-6a55ab8b.json`, `claude-127d6fd7.json`, `lost-receipt-d89cc199.json` (Create)

*   **Type / Scope**: captured fixtures.
*   **Proposed Changes**: Real event documents of those executions, secrets removed.

#### 13. `tests/coordinator/threads.test.ts` (Create)

*   **Type / Scope**: unit tests.
*   **Proposed Changes**: Paging before, after and around (odd `limit`, short side); parallel executions interleave by `seq`; a taskless session that later gets a task shows its early blocks in the task thread; one event with two blocks pages by block key without loss or repeat; two overlapping transactions committing out of `seq` order lose no row when the client follows `feedCursor`; a cut-off stream shows as one block; a closed stream returns one block; search hits open with `around`; export with and without a range; `thread_blocks` by `seq`; each fixture's native records projected by `fleet-executor reproject` from the built binary give the expected blocks; `execution.ended` is written once.

#### 14. `tests/coordinator/recovery.test.ts`, `tests/coordinator/delegation.test.ts`, `tests/coordinator/agent-tools.test.ts`, `tests/coordinator/delivery.test.ts`, `tests/coordinator/multiharness.test.ts` (Modify)

*   **Proposed Changes**: Replay and collision of `event-delta`; readers use `replyText()`; `multiharness.test.ts` asserts `blocks` instead of `messages`. Add a race test for two different answers to the tasks tests.

#### 15. `tests/ui/conversations.check.ts` (Create), `package.json` and `.github/workflows/ci.yml` (Modify) — moved to Stage 2, because it asserts the new screen

*   **Type / Scope**: browser check, script and CI job.
*   **Proposed Changes**: The assertions and timing report in "Browser check" (Technical Plan). It reuses the anchor-drift measurement from `tests/ui/scroll.check.mjs`. Script `"check:conversations": "pnpm run build && node tests/ui/conversations.check.mjs"`; CI job `conversations-check` built like `filter-check` (`ci.yml:58`).

### Stage 2: Single-session thread

#### 16. `app/src/threads.ts` (Create); `app/src/transcript.ts` and `tests/ui/transcript.test.ts` (Delete)

*   **Stage 2 notes**: `session_get` drops `events` and `results`, `/api/sessions/:id/events` is removed (from Stage 1 item 5). Rows live in `Thread` in `packages/ui` (rows of item 19 plus the list), which the book demo also uses. Markdown renders through `packages/ui/src/markdown.ts` (parse) and `markdown.tsx` (Preact nodes, never HTML strings): headings, lists, tables, quotes, rules, fenced code with its own copy, inline code, emphasis, and links only for http, https and mailto. The conversation detail fills the viewport height below the top bar. Steer is `message_send` with `delivery: 'steer'` while an execution runs.

*   **Type / Scope**: app state.
*   **Proposed Changes**: Window state per thread key: loaded blocks, `first`, `last`, `hasBefore`, `hasAfter`, `feedCursor`; the live insert rule and jump from "Read path" in the Technical Plan. Nothing reads `data.item`.

#### 17. `tests/ui/threads.test.ts` (Create)

*   **Proposed Changes**: Window state: prepend, append, jump replaces the window, fed block inside the window is inserted, fed block past `last` with `hasAfter` counts in the pill only.

#### 18. `app/src/views/conversations.tsx` (Modify)

*   **Type / Scope**: view.
*   **Proposed Changes**: Renders blocks through `VirtualList` with `follow: 'bottom'`, `onStart`, `onEnd`. One row component per block kind (item 19). Composer with target execution, Stop, steer, as in "Controls". Server search with jump.

#### 19. `packages/ui/src/controls.tsx` (Modify)

*   **Type / Scope**: UI controls.
*   **Proposed Changes**: Replace `Entry` and `Transcript` (line 346) with `TextRow`, `ToolRow` and `ReasoningRow` (collapsed, expand downward, copy), `QuestionRow` (wraps `DecisionBlock`, line 239, which gains a collapsed answered state and shows the stored answer after a 409), `ResultRow` (artifact names, `StageChip`, links), `BoundaryRow` (agent mark, duration and cost chips, Stop), and code-block copy.

#### 20. `packages/ui/src/format.ts` (Modify) and `tests/ui/format.test.ts` (Modify)

*   **Proposed Changes**: Short agent label from harness, model id and effort; tests for codex, claude and antigravity model ids.

#### 21. `crates/executor/src/harness.rs`, `tests/ui/app.check.ts`, `tests/executor/antigravity_acp.py`, `tests/coordinator/fixtures/documents-main-93c2cc0.json` (Modify)

*   **Proposed Changes**: Remove `fleetNormalized.messages`; move the listed tests and fixture to `blocks`. No re-projection gate (owner decision).

### Stage 3: Thread equals task

#### 22. `app/src/selection.ts` (Create) and `tests/ui/selection.test.ts` (Create)

*   **Type / Scope**: app state and test.
*   **Proposed Changes**: Selection `{ start, end }` over `(key, offset)`, highlight for rendered rows, Shift+click, Cmd+A; Cmd+C and whole-thread copy (a toolbar button) as in "Copy". As built, cross-message selection takes whole rows (a plain click marks the start, Shift+click selects up to a row); native browser selection stays for part of one row. The key handler reads the selection through a ref, because effects run after paint. Tests for range order, offsets in text blocks, and whole non-text blocks.

#### 23. `app/src/url.ts` and `app/src/views/conversations.tsx` (Modify)

*   **Proposed Changes**: The URL stays `/conversations/<sessionId>`; `session_get` returns `threadKey`, the thread of the session's first task (D2) or the session itself, and the detail shows that thread, so any session of a task opens the whole task. Pages return `sessionIds` so the live feed follows every session of a task thread. (Changed in Stage 3: no second URL form or redirect is needed.) Boundary rows between sessions and executions.

### Stage 4: Results

*   **As built (Stage 4 notes)**: the viewer body is `ArtifactView` in `packages/ui` (image and video in fixed-ratio frames, text in 64 KB `Range` pages loaded on scroll, HTML and SVG as source with an optional `sandbox=""` preview, download otherwise) inside `ResultViewer`, whose download is a real link. `GET /api/results/:id` serves one `Range` part (206/416), drops scripts from its CSP and sends `Content-Disposition: attachment` for types without a view. `artifact_list` gains `states`, `since`, `q` (name), a media family (`image/`) and newest-first pages (`limit`, `before`, `nextBefore`); it lists the `artifact` table only, and legacy `artifact` documents (moved by the backfill) open by id. The delivery stage shows on the result of the session's newest ended run. Result cards link each artifact by name to its Results entry.

#### 24. `packages/ui/src/controls.tsx` (Modify)

*   **Type / Scope**: UI control.
*   **Proposed Changes**: `ResultViewer` (line 269) gains image, video and text views, text read in `Range` pages, and a sandboxed `iframe` for HTML and SVG.

#### 25. `packages/coordinator/app.ts` (Modify)

*   **Proposed Changes**: `GET /api/results/:id` (line 213) drops `allow-scripts` and `script-src 'unsafe-inline'` from its CSP and supports `Range` requests, `X-Content-Type-Options: nosniff`, and `Content-Disposition: attachment` for types without a view.

#### 26. `app/src/views/results.tsx` (Create), `app/src/main.tsx` (Modify), `packages/ui/src/registry.ts` (Modify)

*   **Type / Scope**: view and navigation.
*   **Proposed Changes**: Results section with its own registry icon (OR-3, "Navigation anchors"), `artifact_list` with filters, `ResultViewer` in the side panel.

#### 27. `packages/coordinator/delivery.ts` (Modify)

*   **Proposed Changes**: Export the delivery-state-to-stage mapping from the table under "Results and the artifact viewer"; `threads.ts` uses it for the result block.

### Stage 5: Menu actions

#### 28. `app/src/views/conversations.tsx` (Modify)

*   **Proposed Changes**: The action menu from the "Menu actions" table under "Controls", each entry calling the existing operation.

### Documentation

#### 29. `docs/design-system/README.md` and `docs/design-system/controls/` (Modify)

*   **Proposed Changes**: Document the new rows, the extended `ResultViewer` and `VirtualList` upward loading; the book is regenerated with `pnpm run book`; run `pnpm run check:design`.

#### 30. `docs/designs/README.md` (Modify)

*   **Proposed Changes**: Register this design.

### Verification Plan

*   **Compilation/Build**: `pnpm run build`, `cargo build -p fleet-executor`.
*   **Unit Tests**: `pnpm test`, `cargo test -p fleet-executor`.
*   **Static Analysis & Linting**: `pnpm run typecheck`, `pnpm run check:design`.
*   **Test Coverage Requirement**: Every block kind, every paging direction, the join of a taskless session, `event-delta` replay, and the repeated and conflicting answer have a test.
*   **E2E/Integration**: `pnpm run check:conversations` runs the assertions in "Browser check" and prints the timing report.

--------------------------------------------------------------------------------

*Drafted with EGM*
