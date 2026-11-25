# Session view

**Status.** Built ([FLT-49](https://youtrack.ufobe.com/issue/FLT-49)): `SessionView` in [`app/src/views/session.tsx`](../../../app/src/views/session.tsx), its transcript is `Thread quiet`, folded by [`packages/ui/src/controls/tool-fold.ts`](../../../packages/ui/src/controls/tool-fold.ts). Home mounts it at `/home/C-…` ([FLT-48](https://youtrack.ufobe.com/issue/FLT-48)); Conversations mounts it in its work area. The `/conversations/<uuid>` redirect to the code URL comes with the Conversations rebuild.

**App routes:**
* `/home/C-FLT-12`: opened from Home; the rail keeps Home highlighted.
* `/conversations/C-FLT-12`: opened from Conversations, from a project's Sessions facet, from a link or from Spotlight; the rail highlights Conversations.
* `/conversations/<uuid>` (the current route) redirects to the code URL ([OR-38](../owner-rules.md), UR-69).

Both URLs render the same view; only the frame around it differs (see **Frames**). The frame is an [owner decision](#owner-decisions).

**Purpose.** The one place where a person works with a bot, a project PM or a task run ([principles](../owner-rules.md#principles) 2). It answers "what is this session doing, what does it need from me, and what has it done?" in that order.

## Layout

From top to bottom, then the context panel on the right:

1. **Session head** (one line under the [shell header](../patterns/app-shell.md)):
   * the avatar (a bot's mark, or `P` in the project's colour for a PM), the session's name and its project chip;
   * the [state dot and word](../controls/state-badge.md#dot-and-word) (OR-55);
   * while the session has a timer, the [timer chip](../controls/context-panel.md#timer-chip) (`5m · in 3m`, `paused · 5m`); a click opens Context (owner pick A, 2026-11-25);
   * on the right, as icons: Search in the transcript, Stop (only while a run works), Fork, the row menu (Archive, Copy link, Details) and Close in Conversations ([OR-1](../owner-rules.md), [OR-15](../owner-rules.md)). The Context toggle is in the shell header, after search ([side panel](../controls/side-panel.md)).
   * No model badge, tokens, cost or ids ([OR-54](../owner-rules.md)). Details opens them in place, folded under the head ([OR-19](../owner-rules.md)).
2. **Open question** (only while one is open): the [question card](../controls/decision-block.md#card) pinned above the transcript, its choices as buttons and a free-text answer ([OR-53](../owner-rules.md)). A confirmation card for a risky action names its scope and the way back ([OR-61](../owner-rules.md)). The answer edits the card in place (UR-58); answered cards stay in the transcript at the point they were asked.
3. **Transcript**, oldest at the top, the newest at the bottom:
   * a [message](../controls/transcript-viewer.md) per person or agent turn: author line and the age (OR-2), then the body as prose; codes become [entity cards](../controls/entity-card.md) ([OR-44](../owner-rules.md), UR-61);
   * consecutive tool calls and system events as one [folded tool line](../controls/tool-line.md): what was done and how long it took, opened on click ([OR-52](../owner-rules.md), UR-57);
   * heartbeats, repeated attachments and internal notices are never shown.
4. **Composer** at the bottom, always reachable (UR-59). While a run works it says whether Enter queues the message or steers the run; Stop sits beside it. A `/trigger` command in it shows its preview above the text and applies instead of sending ([commands](../controls/composer.md#commands)).

**Context panel** ([context panel](../controls/context-panel.md), [FLT-30](https://youtrack.ufobe.com/issue/FLT-30)), a [side panel](../controls/side-panel.md) right of the transcript, its own scroller ([OR-25](../owner-rules.md)):
* Plan with done items checked, Timers (pause or resume, edit, cancel), Automations, Swarm members with their steps done, related Tasks; each section hidden when empty ([OR-29](../owner-rules.md)).
* A quiet last line: "Model, tokens, cost and ids: Details".
* Open or closed and its width are stored per person (`panels.session-context`, `widths.session-context`), the same for every session ([OR-41](../owner-rules.md)). An open panel with every section empty shows only the Details line, so it never comes and goes with the data.

## Frames

| Opened from | URL | Frame |
|---|---|---|
| Home (a session card, the composer, Wake) | `/home/C-…` | Full width right of the navigation; the shell header title is the session's name; Back returns to Home with its scroll ([OR-27](../owner-rules.md), UR-64). |
| Conversations, a project's Sessions facet, a link, Spotlight | `/conversations/C-…` | The work area of Conversations, the session list stays in the control column with the row selected ([OR-24](../owner-rules.md), [OR-47](../owner-rules.md)). |
| A phone (390 px) | either | The view fills the screen above the bottom bar; the context panel is a sheet opened from the Context toggle in the shell header ([OR-60](../owner-rules.md)). |

## States

* **Working:** the newest tool line shows its running form; the composer says "queued" or "steers".
* **Waiting for you:** the question card above the transcript, in the attention tone ([OR-56](../owner-rules.md)).
* **Failed:** the failed run's tool line is open with the error and Retry; the head's state is Failed in the danger tone; one click shows the cause (UR-63).
* **Asleep bot:** the transcript of earlier runs; the composer's Send wakes it ("Wake and send").
* **Loading:** message skeletons; the composer is usable at once.
* **Offline:** the last transcript stays, marked stale with its age and a retry ([offline marker](../controls/offline-marker.md)).

## Inventory

| Inventory item | Shown by |
|---|---|
| Messages, tool activity, results and questions with options | Transcript, folded tool lines, question card |
| Steer or queue a message to a running agent; stop | Composer, Stop in the head |
| Continue with another agent or account: handoff and fork | Fork in the head; the row menu |
| Session plan, timers, automations, swarm | Context panel |
| Execution boundary stats, resources, model, ids | Details, folded under the head ([OR-54](../owner-rules.md)) |

## Data

| Part | Read or action |
|---|---|
| Head and state | the URL code resolves to the session ([D17](../decisions.md#2026-11-10-d17-human-entity-codes)), then `session_get`; state from the one vocabulary |
| Transcript | `thread_entries` and `thread_blocks` with paging, `thread_search`; tool calls folded by the rule in **Tool lines** below |
| Question card | `questions_list` for the session; answered with `questions_answer` |
| Composer | `thread_composer`, `message_send`; Stop is `execution_stop` |
| Fork | `session_fork` |
| Context panel | `session_context {sessionId}`: `plan` (steps with `done`, from the session's work plan, else its task's), `timers`, `automations` (the session's bot), `swarm` (the other members of that board with their state), `tasks` (its own task, the plan's, the spawned ones and its bot's), each timer's `paused` and `canManage` |
| Timer actions | Pause, Resume and Cancel: `trigger_save` (`enabled`) and `trigger_delete` with the revision shown; Edit fills the composer with the trigger's command, which `session_command_preview` previews and `session_command` applies ([composer commands](../controls/composer.md#commands)); 409 when the trigger changed meanwhile |
| Automation actions | `bot_automation_save` (enable, pause, edit) |
| Details | `execution_totals`, scoped resource rollup |
| Live updates | `changes_since` over the session, its messages, questions and executions |

## Owner decisions

Decided by the owner on 2026-11-25 ([decisions](../decisions.md)):
1. **Frame from Home:** `/home/C-…`, full width, Home stays highlighted; from anywhere else `/conversations/C-…` with the list.
2. **Context panel:** a column right of the transcript; on a phone, a sheet.

## Review of the current build

Checked against the [product map](../product-map.md) rows for one conversation.
* **Keep:** transcript paging and search (a search icon in the session head, `thread_search`), copy of a message or a selection, attachments, Fork and Start fresh with context, handoff to another agent or account, Stop, steer or queue, Archive and Unarchive, the start picker of `/conversations/new`.
* **Change:** decision blocks become the question card above the transcript; tool rows fold into one line per run of tools.
* **Drop:** the metrics header and the inline resource summary (cost and tokens are not the question here, [OR-54](../owner-rules.md); they stay one click away in Details); per-boundary resource rows in the transcript (same reason); repeated attachments, heartbeats and internal notices ([OR-52](../owner-rules.md)).

## Tool lines

Decided in [FLT-46](https://youtrack.ufobe.com/issue/FLT-46): one folded tool line holds the tool and system blocks between two messages. A text block (a person's or an agent's), a question or a result ends the line, and so does the start of another execution. A long turn that talks between its tool calls shows several lines, each next to the words it explains; a turn that only works shows one.

The rule reads only the order of the blocks that `thread_entries` already returns, so the transcript read has no grouped form: the view folds the blocks it shows, and pages join because they are contiguous. The line says the tools it holds and the time from the first block's `startedAt` to the last block's `endedAt`.

Fleet's own notices to an agent (a watch timer, a wake or run request: an operator text block by `Fleet`) are system events and fold into the line, so a bot's transcript shows its answers, not every wake. Withdrawn and expired questions are internal notices and do not show.

## Blocks

* [Navigation rail](../controls/nav-rail.md), [shell header](../patterns/app-shell.md) and, on a phone, the bottom bar: the frame.
* [State dot and word](../controls/state-badge.md#dot-and-word), [avatar and project chip](../controls/session-card.md): the session head.
* [Question card](../controls/decision-block.md#card): the open question.
* [Message](../controls/transcript-viewer.md): one turn.
* [Folded tool line](../controls/tool-line.md): tool calls and events.
* [Composer](../controls/composer.md): the reply.
* [Context panel](../controls/context-panel.md) with plan items, timer rows and swarm members.
* In the Conversations frame, the catalogue row and list header of [Conversations](conversations.md#blocks).

## Design board

Canvas [screens/session.bl](../board/screens/session.bl): working in the Conversations frame, waiting for you in the Home frame, failed, and the phone with the context sheet. Names are real; content is illustrative. An approved state is rebuilt from `packages/ui` and the book (OR-13).
