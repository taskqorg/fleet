# Conversations (operator)

## Target

**Status.** Target, rail section Conversations ([information architecture](../../designs/fleet-ia-and-journeys.md#1-closed-information-architecture)). Built (FLT-51): the default view, the rows and the idle work area. Not yet: the [session view](session.md) in the work area (FLT-49; the current conversation detail stands in) and URLs by human code (UR-69). The sections after **Current build** describe the rest of the page as it runs today.

**Purpose.** Every session, past and present: find one and continue it. Home shows the sessions that are active now; Conversations keeps all of them ([OR-50](../owner-rules.md)).

**Routes:** `/conversations` (idle), `/conversations/C-FLT-12` (one session in the [session view](session.md)), `/conversations/new` (start). Old `/conversations/<uuid>` links redirect (UR-69).

### Layout

Three columns ([OR-24](../owner-rules.md)): navigation, control column, work area. The shell header holds the title, New conversation (`plus`), the focus chip and search ([OR-43](../owner-rules.md)).

* **Control column** (`ListPagePanel`, [OR-48](../owner-rules.md)):
  1. List header: the count (`84 of 1,240`, [OR-11](../owner-rules.md)), the search icon ([OR-31](../owner-rules.md)) and the saved views: system presets Needs me, Running, and the person's own ([OR-33](../owner-rules.md)).
  2. Group and Sort as icons ([OR-35](../owner-rules.md)). Grouping, sorting and saved views stay set by each person, as in the current build ([owner decision](#owner-decision-target)); the default view groups Waiting for you first, then by project. The Group icon opens a short menu: project, state in attention order, agent, none.
  3. Rows: pin, state icon, the session's name (bot or PM name, else the first message) taking the free width, its code after it as a pair ([OR-22](../owner-rules.md)); the right zone: an open question in the attention tone, time since the last activity, the harness mark ([OR-37](../owner-rules.md)). The whole row opens the session ([OR-26](../owner-rules.md)).
* **Work area, idle** ([useful idle work area](../patterns/list-detail.md)): what needs the person first (sessions waiting for an answer, as question cards answered in place, [OR-53](../owner-rules.md)), then a summary by project and state as counters ([OR-29](../owner-rules.md)), then the latest finished sessions with their outcome line. Never a second copy of the list.
* **Work area, a session open:** the [session view](session.md) in its Conversations frame.
* **Work area, new:** the [start picker](../controls/start-picker.md) and the composer, as built today.

### States

* **Empty filter:** the list says what the filter hides and offers to clear it.
* **Archived:** the archive toggle in the list header, as built.
* **Phone (390 px):** with nothing open, the Waiting for you question cards lead, answered in place, and the list follows under them (the work area is hidden at this width, so the cards move to the top of the list; board state 5). A row opens the session full screen; the bottom bar keeps Conversations highlighted ([OR-60](../owner-rules.md)).

### Owner decision (target)

Grouping is not fixed by the design: grouping, sorting and saved views stay configurable per person, as they are today ([OR-33](../owner-rules.md), [OR-35](../owner-rules.md); owner, 2026-11-25). The default view is Waiting for you first, then by project.

### Review of the current build

Checked against the [product map](../product-map.md) rows for Conversations.
* **Keep:** the filter bar with query search, saved views and the presets Needs me and Running, Group and Sort with direction set per person, the Archived toggle, pins and the Pinned group, selection mode with its bar, the row menu, `p` and `x`, New conversation with the start picker.
* **Change:** the row title is the bot or PM name, else the first message, with the code after it as a pair; the idle work area leads with questions instead of the summary table.
* **Drop:** the code on its own second line (it sits after the title, [OR-22](../owner-rules.md)); the harness mark in each row (a technical detail, [OR-54](../owner-rules.md); it stays in the session's Details).

### Blocks

* [Navigation rail](../controls/nav-rail.md) and [shell header](../patterns/app-shell.md).
* [Filter bar](../controls/filter-bar.md#board) with search, view chips, Group and Sort; [catalogue row and list group](../controls/data-list.md#board).
* [State dot and word](../controls/state-badge.md#dot-and-word), [question card](../controls/decision-block.md#card), [group head](../controls/data-list.md).
* [Session view](session.md#blocks) blocks in the work area.

### Design board

Canvas [screens/conversations.bl](../board/screens/conversations.bl): idle with the default view, the Group menu open, a session open, and the phone list.

## Current build

**App routes:** `/conversations`, `/conversations/:id` and `/conversations/new` in the operator app (`app/src/views/conversations.tsx`, `app/src/views/conversation-new.tsx`). The app check (`tests/ui/app.check.ts`) verifies them. There is no static mockup.

**Purpose.** Talk to agents and steer running work. This page replaces the removed legacy session page.

**Layout.** [App shell](../patterns/app-shell.md), then [list and detail](../patterns/list-detail.md):
* **Left:** a list of conversations, using the [list view](../patterns/list-view.md) and [state counters](../controls/state-counters.md) with the `session` state family.
* **Header:** New conversation is the `plus` icon right after the section title in the [shell header](../patterns/app-shell.md) (OR-43); it opens `/conversations/new`.
* **Right, new conversation** (`/conversations/new`, linkable, Back returns; `?in=<project>` preselects a project, `?role=<role>` the first preset on that role): the [start picker](../controls/start-picker.md) in the work area (OR-27), like the new-session screens of Claude Code, Codex and Orca. Top to bottom: the project (focused, then recent, then the rest; ignored left out; "No project" is one choice), the presets under "What are we doing" (`session_presets`: the project's own or the system ones), the [composer](../controls/composer.md), and the start facts under it: project, agent, access and branch, the last two flipped by a click. Send calls `session_start` with the preset, access and branch shown, then opens the new conversation. The list stays as it is.
* **Right:** the conversation, showing a compact inline resource summary with known figures visible, any open [decision blocks](../controls/decision-block.md), the [live transcript](../controls/transcript-viewer.md) with per-boundary resource stats (`BoundaryRow`), and the [composer](../controls/composer.md).

| Inventory item (Conversations) | Shown by |
|---|---|
| Messages, tool activity, execution boundary stats (duration, tokens, tools, multi-currency/subscription cost) and questions with answer options | The transcript (`Thread` / `BoundaryRow`), plus decision blocks for open questions. |
| Scoped resource rollup (`own`, `delegated`, `spawned`, `total`, `elapsedMs` vs `executionWallMs`, `spawnedTasks` links, `hidden > 0`) | Inline summary (`ScopedResourceRollup`, `Totals`, `CostFigures`) with Related work opening scope rows and spawned tasks in the transcript area. The composer stays reachable and the transcript resumes at its reading position. |
| Start a conversation: project, what it does (preset: agent, access, branch, starter) | `/conversations/new`: the [start picker](../controls/start-picker.md) and the composer (`session_presets`, `session_start`). |
| Attach materials; assign work; create tasks | Not in this slice. |
| Fork or start fresh | The Actions menu offers **Fork** and **Start fresh with context** (`session_fork`). The child shows [routing and handoff details](../../execution-continuity.md#fresh-session-example). |
| Steer or queue a message to a running agent | The composer (`message_send`), and Stop for the running execution (`execution_stop`). |

**Message:** a head line with the author (the person, or the agent mark: harness, model, effort) and the age (OR-2, exact time on hover), then the body. Copy on one message, or Cmd+C on a selection, writes plain markdown and chat-safe HTML with a header line per message ([formats, Copy](../formats.md#copy)).

**Filter bar** (as on [Tasks](tasks.md)): search and saved views; the state quick filter on its own line; then the select square, Group and Sort chips ([filter bar](../controls/filter-bar.md)).
* System presets (OR-33): "Needs me" is waiting and failed, "Running" is preparing, running and waiting. A preset sets only the state filter; grouping, sort, projects and search stay. Personal saved views restore everything they saved, including the archived list.
* Group (OR-35): project (default; the sessions waiting for an answer come first as one Waiting for you group, `sessions_list` key empty, label `waiting`), state in attention order (waiting, failed, running, preparing, idle, succeeded, cancelled), agent (harness) or none. Headers show the group's count for the active filters.
* Sort (OR-35): last activity (default, latest first) or created, with a direction toggle. Last activity is the latest change of the conversation, its messages and events, or the start or end of its executions (`sessions_list` `activityAt`).
* Archived (last on the tools line): an icon-only toggle `Button` (`archive` icon, named "Archived", pressed while on) switches between active and archived conversations in one click. It sets `archived=1` in the view URL, so it is part of saved views and of the section view the navigation returns to; turning it on or off ends selection mode. In the archived list the state counters, group counts and the count count archived conversations only, pinned rows show only if archived, and the empty state says "No archived conversations." with "Show active".

**List row (OR-22, OR-37):** on a pinned row, the [pin](../controls/row-pin.md) first; the state icon; the title (stored or generated, else the first message), which takes the free width, with its code after it; the row facts on the right: the now line (`sessions_list` `now`, at most a quarter of the row, hidden on a row under 480 px), open questions as `1 question` in the attention tone, time since the last activity. No executions, harness mark or spend (OR-54): they are in the conversation's details. No buttons appear on hover: pin, select, copy and Archive are in the [row menu](../controls/row-menu.md); `p` pins and `x` selects the focused row.
* Pinned conversations are a Pinned group at the top, kept whatever the filter, search or grouping (OR-12).
* [Selection mode](../controls/selection.md): checkboxes only in the mode, as their own column left of the state; the bar acts on the selected rows: Pin, Unpin and Archive (confirmed inline; `session_archive` per conversation, refused while an execution is active). In the archived list Unarchive replaces Archive, in the bar and the row menu: `session_unarchive` per conversation (owner or full access), no confirmation, since it only returns the conversation to the active list; its worktree is prepared again at the next start.
