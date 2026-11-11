# Fleet Conversations

One thread shows the history of a task across its sessions and executions: operator messages, agent replies, tool calls, reasoning, questions and results in one order. A taskless session is its own thread; attaching it to a task joins the history without copying it. The thread opens at the newest entry, pages upward and supports a jump with entries on both sides, without a thread-size cap.

The executor produces one typed transcript projection; server reads, rendering, search and copy consume that projection instead of interpreting native harness records independently. This prevents a harness-specific raw-event shape from hiding an agent reply. Receipts remain durable across authentication/edge failures; a missing final receipt does not establish that an execution exited. In `go/executor/outbox.go`, a host-token 401 is a credential/edge condition: receipts remain queued and earlier failed 401 rows are requeued at startup. [Execution continuity](../execution-continuity.md) preserves prior evidence before continuation.

Thread membership is derived from tasks and sessions rather than stored as a second thread entity. `VirtualList` keeps the anchor while rows change; app selection state supports copying across rows outside its rendered window. The browser check measures timings and asserts behaviour without numeric speed budgets.

## Terms

- **OR-n:** the corresponding [owner rule](../design-system/owner-rules.md).
- **Service zone:** the compact row region with identity, state, agent, time, counters and actions.
- **Block key:** `(seq, index)`, the row sequence and position in its blocks array; one event may contain several blocks. Cursors, search hits and selection points use the block key, not `seq` alone.
- **`execution_totals`:** per-execution wall time, tokens and cost.

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
*   New agent operations. Retry, fork, hand off and switch use the existing agent tool operations (`go/coordinator/agent.go`).
*   Editing or deleting artifacts from the viewer. Artifact lifecycle stays in the data foundation.

## Defaults and compatibility

These are the existing presentation and membership defaults; this cleanup introduces no new choice.

*   **Model label source.** Default: `format.ts` turns the stored model id into the short label (`claude-opus-5-5` becomes `opus-5.5`). No model registry is added.
*   **Old events without blocks.** Old records are not migrated and no compatibility projection is kept. Events stored before blocks existed show no blocks. The executor `reproject` subcommand stays only as the tool that turns captured fixture records into the projection the executor writes.
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
*   **Then**: The artifact viewer opens in the work area. The image shows in a fixed-ratio frame; the log shows as text, read in `Range` pages, with a download link. The Results page lists both under the task.

## Technical Plan (Architecture)

### Ordering key

Every row the thread needs already has one shared ordering number. `document.seq` is a `bigserial`, and the `execution`, `question` and `artifact` tables take their `seq` default from the same sequence (`go/coordinator/schema.sql`). Events and operator messages are documents. Here `seq` is the row's `seq` column, not the per-execution `data.seq` that host events also carry. So `seq` gives one total order across sessions and executions that overlap in time. The thread cursor is `seq`. No new ordering column is needed.

`seq` is taken at insert, not at commit, so two transactions can commit out of `seq` order. The operator app already solves this for its live feed: a read and the feed cursor come from one snapshot, and the feed (`change` table, ordered by `(xid, seq)` up to `pg_snapshot_xmin`) never skips a slow transaction ([fleet-operator-app.md](fleet-operator-app.md)). Thread reads copy that pattern:

*   Each `thread_entries` read runs in one snapshot and returns the change-feed cursor of that snapshot (`feedCursor`).
*   The client follows the feed from `feedCursor`. For a fed change in the thread, the client fetches that row's blocks with `thread_blocks` by the change's kind and id (a change row names the document id, not its `seq`) and places them by block key.
*   A row from a slow transaction can therefore arrive with a key lower than the client's newest loaded key. It is still placed by key, and `VirtualList` anchor keeping holds the row being read in place (OR-6). Nothing is skipped and nothing on screen moves.

### Thread scope

A thread key is `{ task: id }` or `{ session: id }`; the routes are `/api/threads/task/:id/...` and `/api/threads/session/:id/...`.

*   **Task thread**: all executions with `execution.task_id = <task>` (indexed by `execution_task_seq`). Their sessions are the set of `session_id` values. This includes a taskless session that got the task later, because its later executions carry the `task_id`; its earlier executions join through the session (see below).
*   **Session thread**: one session that has no task. Once the session gets a task, its URL opens the task thread.

Membership rule: an entry belongs to task T's thread if its execution has `task_id = T`, or if its execution has no `task_id` and T is the first task its session got (the session's lowest-`seq` execution with a `task_id`). This keeps a session's early taskless history in the task thread once the session joins the task (the thread membership rule), and nothing is copied. The two-task case is an open question.

As a query: the session set is `SELECT DISTINCT session_id FROM execution WHERE task_id = T`; entries are rows of those sessions whose execution has `task_id = T`, plus rows whose execution has no `task_id` when T is that session's first task.

Joining a task: `session_start` takes an optional `taskId` (a task of the same project); the session records it and every execution it starts carries that `task_id`. `session_task_attach({ sessionId, taskId })` attaches a taskless session: the session records the task, its next executions carry it, and its earlier taskless executions join the task thread by the membership rule; nothing is copied or rewritten. A session that already belongs to another task gets 409; the same call again returns `existing: true`. The attached task counts as the session's first task (D2). Both are agent tools as well (the PM agent and people-access use them); host agents may call them only for their own sessions and project.

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
*   `limit` counts entries (blocks) and is the page size the client asks for. There is no product page-size maximum. The existing JSON-number boundary rejects non-number, non-positive or non-integer values and values above `2^53`.
*   `total` is the entry count, for the "84 of 1,240" count (OR-11). It is returned only for the open read and for `around`; the client adds live entries to it. It is a count over the thread's index range; the browser check reports its time.

The query finds the thread's member executions (the membership rule), then reads events and messages through `document_execution (kind, executionId, seq)`, questions through `question_execution` and artifacts through `artifacts.list({ executionId })`, which merges the `artifact` table and legacy `artifact` documents, merged by key. It joins `execution` (harness, model, effort) only for the page rows. It reads only the page, never the whole session. `session_get` stops returning `events` and `results`; it keeps the session header fields.

Live entries arrive through the existing change feed (`app/src/feed.ts`) from `feedCursor`. The client inserts a fed entry whose key falls inside its loaded window `[first, last]`, or after `last` when `hasAfter = false`; it ignores other fed entries, because a later page read returns them. The "N new" pill counts only entries past `last`.

### Search: `thread_search`

```text
thread_search({ key, q, before?: BlockKey, limit }) -> { hits: { key: BlockKey, snippet }[], hasMore }
```

It searches the plain text of text, reasoning, tool-call and question blocks in the thread, newest first. The first version uses `ILIKE` on the stored block text over the same index range. Its time is measured by the browser check; a text index is added only when that measurement shows it is needed. A hit is opened with `thread_entries({ around: hit.key })`.

### Block model

One normalizer produces typed blocks. The screen and copy code only read blocks. The types are `Block` and its six members in `contracts/ops-sessions.schema.json` (closed, told apart by `kind`); the app's `Block` and `packages/ui`'s `ThreadBlock` are the generated union, and the coordinator builds its blocks from the generated structs.

```text
Block =
  | { kind: 'text',      key, executionId, role: 'operator'|'agent', markdown, plain, at, by?, stream?, harness?, model?, effort? }
  | { kind: 'tool',      key, executionId, at, name, input, output, state, startedAt, endedAt }
  | { kind: 'reasoning', key, executionId, markdown, plain, at, stream? }
  | { kind: 'question',  key, executionId, questionId, text, options, state, askedAt, answeredAt, answer?, answeredBy? }
  | { kind: 'result',    key, executionId, artifactIds, artifacts }
  | { kind: 'boundary',  key, sessionId, executionId, harness, model, effort, state, startedAt, endedAt }
```

*   `text`, `tool` and `reasoning` come from the executor. `project` (`go/executor/harness_blocks.go`) writes `fleetNormalized` per event. It writes a `blocks` array with these three kinds for every supported harness. Tool duration comes from the harness start and end records.
*   Streaming: delta records (projection with `deltaOf`) are stored as documents of kind `event-delta`, not `event`, so pages, search and export never scan them. When the stream ends, the executor writes one final block on the completing `event`. An open stream is shown as one entry at the end of the newest page, built in full from its `event-delta` rows; it is never split across a page edge.
*   Execution end: on an execution's terminal transition the coordinator writes one `event` of type `execution.ended`. It carries no text; its `seq` places the result block. A stream cut off without a final block (the execution ended first) is shown as one closed entry built from its `event-delta` rows at read time, so the executor stays the only producer of text blocks.
*   Records with no raw data or an unknown shape get one `tool` block named after the event type, with the raw JSON as output, so every event has `blocks`.
*   `question` and `boundary` come from the coordinator, from the `question` and `execution` rows, at their own `seq`. `result` comes from the stored `artifact` rows, at the key of the `execution.ended` event.
*   `plain` is the block's text with stable character offsets. Selection and copy use it.

Old stored events are not re-projected (owner decision); the app no longer reads `data.item`.

Projection details:

*   `plain` equals `markdown` in this version; the coordinator sets it, the executor stores only `markdown`.
*   Stream ids are `<kind>:<n>` per execution (`text:1`, `reasoning:2`). The first chunk of a stream carries `opens: true`; the record that closes streams carries `closes: [ids]`. Readers find streams through `opens` rows and never scan every chunk.
*   Lifecycle records (thread start, usage, protocol writes, turn end) get an empty `blocks` array. Executor bookkeeping events (preparation, resource observations, process closed) are not harness records and have no projection. "Unknown shape" means an `unparsed` line, a `*.failed` record or a Claude error result: one `tool` block named after the record type, state `failed`, raw record as output.

### Agent mark

The `boundary` block and each agent `text` block carry the execution's `harness`, `model` and `effort`. `effort` is stored in `execution.config_ref`; the thread reads `config_ref->>'effort'`. The row shows the harness icon and the short label in the right service zone (OR-21). The target boundary row shows duration and cost as visible chips (OR-19); host and tokens belong in their tooltips, one tooltip per chip (OR-20). Not built: the cost chip and host/token telemetry hover; the current `BoundaryRow` exposes duration and the agent mark. Reply rows show only the harness icon and label, with the model id on hover. Fleet has no cost attention rule today, so the cost chip stays neutral; this design invents no threshold (OR-18 applies once such a rule exists).

### Rendering and nothing-jumps

`VirtualList` gains:

*   `onStart`, the mirror of `onEnd`, called once per list length when the first rows come into view.
*   Position keeping on prepend: before the new items commit, it records the anchor key and its offset; after measurement, in the same layout effect, it sets `scrollTop` so the anchor offset is unchanged. The existing anchor logic is reused.
*   `scrollToKey(key, align)` for jumps.

Rules, one invariant ("the anchor row moves 0 px"):

*   Expanding a tool call or reasoning row opens downward; the clicked header stays where it is.
*   Media gets a fixed-ratio placeholder from the artifact's stored size before it loads.
*   Streaming grows only the newest row. Following stops when the operator scrolls up, and the "N new" pill shows (OR-7).
*   The loading bar keeps its space .

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

The thread renders `question` blocks with the existing `DecisionBlock` pattern. Answering calls `questions_answer`, the call Tasks uses. `answerQuestion` (`go/coordinator/tasks.go`) returns the stored question when the same answer repeats and fails with 409 when a different answer arrives; the card then shows the stored answer and who gave it. The update guards the open state so only one conflicting answer wins. The card updates from the change feed. Answered cards collapse to one line with the answer.

### Results and the artifact viewer

Artifact storage provides `artifact` rows, artifact stores and placement exist, and `artifact_list` and `artifact_get` exist (`go/coordinator/artifact_routes.go`).

*   **Result block**: one per execution end that has artifacts or a delivery, at the key of the `execution.ended` event. A delivery or artifact that arrives later updates this block in place; it does not move (OR-23). It shows artifact names, the delivery-stage chip and links each artifact to its Results page entry.
*   **Delivery stage**: the existing `stages` in `packages/ui/src/registry.ts`, rendered by `StageChip`. The coordinator maps the `delivery` record state to a stage key:

    | Delivery state | Stage key |
    |---|---|
    | `pr-open` | `pr_open` |
    | `integrating` | `merging` |
    | `merged`, `published` | `merged` |
    | any other state | no chip; the task state shows failures |

    The delivery record has no check status today, so `checks` and `checks_failed` are not shown until delivery records one. No check state is guessed.

*   **Viewer**: `ResultViewer` and `ArtifactView` (`packages/ui/src/controls.tsx`) open in the work area (OR-27). The [result-viewer control](../design-system/controls/result-viewer.md) owns the format table and Source/Preview choice. Renderable formats open rendered; copy/download take the source. Images and video use fixed-ratio frames; text loads through `Range` pages as the operator scrolls, without a cap. Unrecognised types are download-only. Raw bytes use `GET /api/results/:id`; interactive HTML uses the separate `GET /api/results/:id/preview` route. HTML scripts run on an opaque sandboxed origin, without same-origin app access; `connect-src 'none'` blocks fetch/XHR connections and the preview CSP explicitly lists permitted static-resource sources. SVG renders as an image, so its scripts do not run. Markdown has no raw HTML. These are the existing rendered-first viewer rules, not a new permission change.

*   **Results page**: the app section listing `artifact` rows with task, project, execution, media type, state and time filters, using the Tasks list pattern (state icon first, OR-22). It uses the same `ResultViewer`. Conversations result cards link into it.

Artifacts live in the `artifact` table. The thread, the viewer and the Results page read them only through `artifact_list` (`artifactList`, `go/coordinator/artifacts.go`) and `artifact_get`.

### Controls

Stop, steer and answer are always visible. The rest are in one left-aligned menu with icons (OR-13), in reserved space (OR-23).

*   **Composer target**: the newest running execution of the thread, named in the composer. When nothing runs, the composer sends a new message to the thread's newest session with `message_send`, which starts an execution, and steer is not shown. Each `boundary` row of a running execution has its own Stop (the thread membership rule).
*   **Stop**: `execution_stop` on that execution.
*   **Steer**: `message_send` with `delivery: 'steer'` on the target execution's session; queued when the host cannot steer (`go/coordinator/agent.go`).

Menu actions:

| Action | Operation | Kept | New | Where it shows |
|---|---|---|---|---|
| Retry | `message_send` with the last operator message, same session | session, task | execution | same thread, new boundary |
| Switch profile or model | `session_transition` | task, session when `reuse` or `switch_model_in_session` | execution; new session when `create_with_handoff` | same thread, new boundary |
| Fork | `session_fork` | original session unchanged, task | session and execution | same thread, new boundary |
| Hand off | `session_transition` with another harness or account (`create_with_handoff`) | task, context handoff record | session and execution | same thread, new boundary |

Hand off is the existing context handoff (`makeHandoff`, `go/coordinator/agent.go`). Quota-driven handoff is not part of this design (it is also out of scope in [fleet-data-foundation.md](fleet-data-foundation.md)).

### Data flow

```text
harness stdout --> executor harness projection --> event doc { data.fleetNormalized.blocks }
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

It asserts: newest entry visible on open; scroll up loads older pages with 0 px anchor drift; jump to a search hit; expand keeps the header in place; streaming append with the pill; one question answer; reply copy (cross-message copy is asserted with selection); stop on one of two executions. The fixtures store the executor projection of each native record; the coordinator test checks it equals `fleet-executor reproject` output. It prints, without thresholds: time to first paint of the newest reply, page round-trip time, search time, and anchor drift.

## Non-Functional Requirements

*   **Security**: Artifacts and tool output are untrusted agent output. Text uses `Range` pages; raw bytes and interactive previews have separate CSPs. The viewer follows the [result-viewer control](../design-system/controls/result-viewer.md): HTML preview scripts have an opaque origin, no same-origin app access and `connect-src 'none'`; permitted static-resource sources remain explicit in the CSP. SVG uses an image; Markdown in replies has no raw HTML. Captured fixtures have secrets removed.
*   **Performance**: No budgets. Every read is a page over an index range; no operation loads a whole session. The browser check reports timings so regressions are visible.
*   **Correctness**: Thread reads return a feed cursor from the same snapshot, so no row is skipped; late rows are placed by key under anchor keeping.
*   **Compatibility**: Old records are not re-projected and the old view is not kept behind a flag. Captured fixtures are re-projected by the executor fixture tool; this is not a database migration.

## Risks & Mitigations

| Risk | Mitigation |
|---|---|
| A slow transaction commits a row with a lower key after the client loaded newer rows. | Snapshot-plus-feed-cursor read; the row is placed by key and anchor keeping holds the reading position. Test case in `threads.test.ts`. |
| A reply cut off before its final block is not found by search (search reads only final blocks). | Accepted trade-off; it is shown in the thread. |
| Search with `ILIKE` is slow on long threads. | The browser check reports search time; a text index is added when the measurement shows the need. |
| `total` count is slow on long threads. | Returned only on open and jump; its time is reported. |
| An event has no raw record or an unknown shape. | It gets one `tool` block with its raw JSON, so nothing is hidden. |
| Untrusted artifact content runs script in the operator's browser. | Separate raw-byte and preview routes; opaque sandbox origin, no same-origin app access, and explicit CSP restrictions. SVG is an image; Markdown has no raw HTML. |
| Clipboard write fails after the server round trip. | `ClipboardItem` with a promise created in the key handler; failure shown in the status line with retry. |
| Two different answers race. | `UPDATE ... WHERE state = 'open'`; the loser gets 409 and sees the stored answer. |

## Implementation

Owning implementation: block projection in `go/executor/harness_blocks.go`; `thread_entries`, `thread_search`, `replyText` and the feed cursor in `go/coordinator/threads.go`; the screen in `app/src/views/conversations.tsx`, `app/src/threads.ts` and `app/src/selection.ts`; the Results page in `app/src/views/results.tsx`; the viewer in `ResultViewer` (`packages/ui/src/controls.tsx`).

*   **Block key**: the string `` `${seq}:${index}` ``, compared numerically by `seq` then `index`. Coordinator-made blocks (`question`, `boundary`, `result`) use index 0 on their own row.
*   **Snapshot and feed cursor**: thread operations run in one read snapshot, and `feedCursor` is read inside it.
*   **Stored event shape**: an `event` document's `data` holds `executionId`, `sessionId`, `seq` (per execution), `type` and `data` (the native record); `data.data.fleetNormalized` is the projection.

### Verification

*   Build: `pnpm run build`; `cd go && go vet ./... && go test ./...`; `pnpm run check:design`.
*   Tests: every block kind, every paging direction, the join of a taskless session, `event-delta` replay, and the repeated and conflicting answer (`tests/ui/threads.test.ts`, `tests/go`).
*   Browser: `pnpm run check:conversations` runs the assertions in "Browser check" and prints the timing report.
