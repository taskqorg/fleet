# Stage 6: Comprehension Test

Target: `docs/designs/fleet-conversations.md`. Reviewer: `egm-comprehension`, document only. Date: 2026-11-10. Resolutions applied after the review are listed at the end.

## What this proposes

**FeatureRange.** The design rebuilds Conversations, the operator's main screen, so that one thread shows a whole task. The thread merges every session and execution of the task into one order, using the existing shared `seq` sequence. A session with no task is its own thread.

- **Server:** it gains cursor-paged reads (`thread_entries` with before, after or around), whole-thread search (`thread_search`) and a markdown export (`thread_export`). These reads stop at the same `pg_snapshot_xmin` horizon the change feed uses, and live rows arrive only through the feed.
- **Executor:** it becomes the single normalizer. It writes typed `blocks` (text, tool, reasoning) per event, and the coordinator builds question, result and boundary blocks from its own tables. Old events are re-projected once.
- **Screen:** it only renders blocks. `VirtualList` gains upward loading and keeps the row being read from moving.
- **Features:** an agent mark, markdown copy (one reply, one code block, a cross-row selection held in app state, the whole thread), question cards, result cards, a hardened artifact viewer, a Results page, and a controls menu.
- **Testing:** a browser check over captured real fixtures asserts behaviour and reports timings without thresholds.

**DetailedRange.** Five stages:

1. Executor blocks, the `reproject` CLI and script, coordinator `threads.ts`, the routes, `VirtualList` changes, tests and fixtures, and the browser check in CI.
2. App window state replaces `transcript.ts`, with new block rows, the label formatter and the `DecisionBlock` collapsed state. `fleetNormalized.messages` is removed.
3. App-state selection and copy, plus the thread key in the URL.
4. Viewer hardening, CSP and header changes, the Results view, and the delivery-to-stage mapping.
5. The action menu.

Documentation updates and a verification plan come last.

## How the existing system works, per this document

- **Loading:** `session_get` returns a whole session in one response. The events endpoint pages forward only, and it loads every event before slicing the page.
- **Transcript:** `transcript.ts` builds it in the browser from `fleetNormalized.messages` plus raw `data.item` records. Reading raw records caused the no-reply bug. It also drops reasoning and plan items, reports every tool duration as 0, and is O(n²) per render.
- **Scrolling and search:** `VirtualList` keeps the anchor row on insert, but it can only load downward. Search filters only the rows already loaded.
- **Ordering and the feed:** `document`, `execution`, `question` and `artifact` all take `seq` from one bigserial sequence. The change feed reads by `(xid, seq)` up to the `pg_snapshot_xmin` horizon.
- **Already in place:** questions are answered idempotently through `questions_answer`/`tasks.answer`, which returns 409 when a different answer arrives. Data foundation Phase 3 artifacts are live, and `ResultViewer` already exists.

## Findings

| # | Severity | Finding | Resolution |
|---|---|---|---|
| C1 | Major | Alternative H still said `seq` is "a total commit order", contradicting the Ordering key section. | Fixed: "total order"; the horizon rule is cited. |
| C2 | Major | Stage 4 item put an `ArtifactViewer` in the side panel; everywhere else it is `ResultViewer`. | Fixed. |
| C3 | Major | Closing an open stream through a derived `boundary` block was vague. | Fixed: when an execution ends, the coordinator writes one final text block event that supersedes the open deltas; it is persisted and read like any final block. |
| C4 | Major | Re-projection: who runs it, how harness is known, how removal of `messages` is gated. | Fixed: operator runs `pnpm run reproject` once per deployment at the end of Stage 1; harness comes from `execution.harness`; `--check` must report zero events without blocks before the Stage 2 removal. |
| C5 | Major | Composer target undefined when nothing runs. | Fixed: with no running execution, the composer sends a new message to the thread's newest session (`message_send`), which starts an execution; steer is shown only while one runs. |
| C6 | Major | `thread_export` unspecified; diagram contradicted the copy text. | Fixed: signature, return shape and range rule for non-text blocks; diagram corrected. |
| C7 | Nit | OR-n never explained. | Fixed: Terms section. |
| C8 | Nit | Project terms undefined. | Fixed: Terms section; brief stages restated in one line each. |
| C9 | Nit | Membership edge cases. | Fixed: early taskless history joins the first task the session got; the session thread URL then opens the task thread. |
| C10 | Nit | Legacy artifacts unclear. | Fixed: legacy rows live in the `artifact` table (`legacy_id`); one source. |
| C11 | Nit | `around` with odd `limit` and short sides. | Fixed. |
| C12 | Nit | Thread key mixes two id kinds. | Fixed: routes `/api/threads/task/:id` and `/api/threads/session/:id`; key is `{ task }` or `{ session }`. |
