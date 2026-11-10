# Conversations screen redesign

* **Author:** Alex Kirs
* **Date:** 2026-11-10

## Overview

Conversations is the operator's main tool. One thread shows the whole history of one task: every message the operator sent, every reply, tool call, question and result from all of the task's sessions and executions, in order. A session that has no task is its own thread. The thread opens at the newest entry and loads older entries as the operator scrolls up. A jump to a message loads entries on both sides of it. The row being read never moves (OR-23), and threads have no size cap.

Each reply shows which agent wrote it in a short form: harness icon, model and effort (`opus-5.5 · high`). Host, tokens, cost and duration appear on hover. Tool calls and reasoning are collapsed to one line, open on click and can be copied. Copy works for one reply (markdown), a code block, a selection across messages and the whole thread (markdown).

Question cards (the question entity) sit in the thread. Open questions can be answered there or from Tasks. Answered questions stay in the thread, collapsed, with the answer. Result blocks (execution result, delivery stage, artifacts) sit in the thread too. Artifacts open in one viewer control that handles any file, with dedicated views for images and video. A separate Results page lists all results and artifacts with Tasks-like filters, and Conversations links into it.

Stop, steer and inline answer are always visible. Retry, fork, hand off and switch profile or model are in a menu. There are no speed thresholds: the screen is made as fast as possible, and a browser check measures and reports the timings. This also closes the drift item "Conversations has no asserting browser check".

## Analysis

The 7-lens Goldfish review (design, codebase details, zoom-out, comprehension, implementation readiness, structure, inventive catalyst) found these risks.

1. **No thread paging API (blocker).** `session_get` loads all events and artifacts at once (`packages/coordinator/app.ts`). `/api/sessions/:id/events` only pages forward, by slicing in memory after loading everything. It has no "before" cursor, no paging around a message and no paging across a task. "No cap", "open at newest" and "jump both ways" all need this first. Grouping by task is cheap in the database because `execution.task_id` and `(session_id, seq)` are indexed. The hard part is one ordering key across executions that overlap in time.
2. **OR-23 vs loading upward, streaming, expanding and late media.** `VirtualList` already keeps the anchor row before paint and supports variable row heights. It has `onEnd` and `follow`, but no `onStart`, and it does not keep position when rows are added above.
3. **Two data sources for one transcript.** Reply text comes from `fleetNormalized.messages` and tool calls from the raw `data.item`. Reading raw records caused the no-reply bug (`docs/conversations-no-reply-rca.md` §1). Adding reasoning, question and result blocks on raw records would repeat that bug for each harness. `app/src/transcript.ts` also does O(n²) work per render, drops reasoning and plan items, and sets tool-call duration to 0.
4. **Copy across messages fails under virtualization.** Rows outside the window are not in the page, so the browser's own selection cannot span them.
5. **No per-reply agent data.** Model, harness, host and totals are stored per execution (`execution_totals`). Effort has no column; it is only in launch config. Until effort is stored, the agent mark is per execution.
6. **Search covers only loaded rows.** It filters in memory, so with paging it misses unloaded history and cannot drive jump-to-message.
7. **Artifacts are untrusted agent output.** The viewer needs a size limit, MIME sniffing and a sandboxed preview (HTML and SVG in particular). It must read both sources: `document` kind `artifact` records and the `artifact` table.
8. **One question, two answer places.** Answers from Tasks and from the thread must be idempotent (reuse `requestId`), and the card updates from the live feed.

Resolutions for the main contradictions:

* **No cap vs virtualization vs selection.** Each message keeps a plain-text copy with stable character offsets. A selection is a range from (message, offset) to (message, offset) kept in app state, so it survives rows leaving the window. "Copy thread" builds its text from those copies or from the server. Cost: the app draws the selection highlight and handles Shift+click and Cmd+A.
* **Nothing jumps vs changing content.** Scroll position is pinned to the row being read. Height changes above that row are corrected in the same frame. Expanding opens downward and the clicked header stays in place. Media gets a fixed-ratio placeholder before it loads. Streaming grows only the bottom row and stops following once the operator scrolls up, showing an "N new" pill. This is one named invariant, tested by the browser check: the anchor row moves 0 px.
* **Much metadata vs a calm screen (OR-19, OR-21).** Each reply has one fixed-width service zone on the right. A chip takes an attention color only when it needs attention (OR-18), for example high cost. The rest is in the hover or focus panel.
* **Results page vs results in the thread.** One result and artifact model, one viewer component. The thread shows a compact card linking to the item; the Results page is a filtered list over the same items.
* **Many controls vs few visible.** Stop, steer and answer stay visible. The menu sits in reserved space so opening or filling it reflows nothing.

## Goals vs. Non-Goals

* **In-Scope Goals:**
  * Thread = task; taskless session = own thread.
  * Bidirectional cursor paging, jump to message, no size cap, 0 px anchor drift.
  * Typed transcript blocks: text, tool call, reasoning, question, result.
  * Agent mark per reply, telemetry on hover.
  * Copy: reply, code block, cross-message selection, whole thread, as markdown.
  * Inline question cards, answered ones kept collapsed.
  * Result blocks and a shared artifact viewer (generic file, image, video).
  * Results page with Tasks-like filters.
  * Controls: stop, steer, answer visible; retry, fork, hand off, switch profile or model in a menu.
  * Asserting Conversations browser check with timing measurements.
* **Out-of-Scope Non-Goals:**
  * Numeric speed budgets or thread size limits.
  * A stored thread entity.
  * A separate windowing engine.
  * Keeping the old view behind a flag.

## Alternatives Weighed

* **Numeric speed budgets in the check.** Rejected by author: no invented limits. The check measures and reports (time to first paint of the newest reply, page round-trip, anchor drift) and asserts behaviour only.
* **Hand-written browser fixtures.** Rejected: a wrong fixture shape hid the no-reply bug. Fixtures come from captured real events (codex `6a55ab8b`, claude `127d6fd7`, lost receipt `d89cc199`) with secrets removed, plus a check that the fixture shape matches what the executor writes.
* **Each screen building its own result card.** Rejected: one shared component for thread and Results page.
* **A separate windowing engine.** Rejected: extend `VirtualList`.
* **A stored thread entity.** Rejected: derive the thread from `task_id` or its absence.
* **Old view behind a flag.** Rejected by author: fix forward at the dev stage.
* **One session per thread, task shown as links.** Simpler, but drops the owner's premise. Rejected by author; thread = task stays, staged after single-session paging works.
* **Native browser selection for copy.** Rejected: cannot span virtualized rows.

## Key Decisions & Staging for Next Steps

Decisions from the owner: thread = task; open at newest, load older upward; jump loads both sides; no cap; tool calls and reasoning collapsed and copyable; copy formats as above; agent mark `opus-5.5 · high` with telemetry on hover; results first as a thread block, then a viewer control from generic file to image and video; separate Results page; controls split as above; answered questions stay collapsed; no thresholds, measure and report.

Open questions for `/egm-draft`:

* If a taskless session later gets a task, does its history join the task thread?
* Do Stop and Steer target the active execution or the task? Which one when executions run in parallel?
* How are session and execution boundaries shown in the thread?
* What does steer write, and what happens when the operator steers while a question is open?
* For fork, hand off, retry and switch model: what is kept, what is new, where it appears (one table). Is hand off the quota handoff from the data foundation?
* Define "delivery stage" (today "delivery" is a legacy run kind).
* Does `opus-5.5 · high` come from a model registry or from parsing harness strings?

Staging:

1. **Foundations.** Normalization emits typed blocks so `transcript.ts` only renders. Bidirectional cursor API, then server-side search. `VirtualList` gets `onStart` and keeps position when rows are added above. Real-shape fixtures and the asserting browser check with timing marks.
2. **Single-session thread on the new base.** Newest first, jump to message, collapsed tool calls and reasoning, copy reply and code, agent mark per execution, question cards via `DecisionBlock` and `questions_answer`, stop, steer, answer.
3. **Thread = task.** Merge the task's executions in one order with visible boundaries. Effort column on `execution`. Cross-message selection and whole-thread copy.
4. **After data foundation Phase 3.** Result and artifact model, shared viewer, result cards, Results page.
5. **Menu actions.** Retry, switch model, fork and hand off, after the operations table is agreed.

## Owner Decisions

These are defaults set by the owner on 2026-11-10. The owner can overturn any of them.

* **Data foundation Phase 3 is live.** Artifact stores and placement are already on main, so results and the Results page do not wait for it. Stage 4 can start as soon as stage 2 does.
* **Stop and Steer target.** Stop and Steer act on the execution the operator points at. The composer targets the newest running execution and shows which one it is. When executions run in parallel, each execution block has its own Stop.
* **Taskless session that later gets a task.** It joins that task's thread. Nothing is copied; the thread is derived from `task_id`.
* **Delivery stage.** The OR-22 chip: PR open, checks, merging, merged, plus failed.
