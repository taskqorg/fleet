# Conversations (operator)

**App routes:** `/conversations` and `/conversations/:id` in the operator app (`app/src/views/conversations.tsx`). The app check (`tests/ui/app.check.ts`) verifies them. There is no static mockup.

**Purpose.** Talk to agents and steer running work. This page replaces the removed legacy session page.

**Layout.** [App shell](../patterns/app-shell.md), then [list and detail](../patterns/list-detail.md):
* **Left:** a list of conversations, using the [list view](../patterns/list-view.md) and [state counters](../controls/state-counters.md) with the `session` state family.
* **Right:** the conversation, showing a compact header resource summary bar (expandable in-place without shrinking the thread below available height or creating nested scrollers per `OR-25`), any open [decision blocks](../controls/decision-block.md), the [live transcript](../controls/transcript-viewer.md) with per-boundary resource stats (`BoundaryRow`), and the [composer](../controls/composer.md).

| Inventory item (Conversations) | Shown by |
|---|---|
| Messages, tool activity, execution boundary stats (duration, tokens, tools, multi-currency/subscription cost) and questions with answer options | The transcript (`Thread` / `BoundaryRow`), plus decision blocks for open questions. |
| Scoped resource rollup (`own`, `delegated`, `spawned`, `total`, `elapsedMs` vs `executionWallMs`, `spawnedTasks` links, `hidden > 0`) | Expandable resource summary panel in detail (`ScopedResourceRollup`, `Totals`, `CostFigures`), preserving full transcript height by default. |
| Attach materials; assign work; create tasks | Not in this slice. |
| Continue with another agent or account: hand off and fork | "Fork" is an icon in the detail head (`session_fork`). Handoff and profile switch are not in this slice. |
| Steer or queue a message to a running agent | The composer (`message_send`), and Stop for the running execution (`execution_stop`). |

**Message:** a head line with the author (the person, or the agent mark: harness, model, effort) and the age (OR-2, exact time on hover), then the body. Copy on one message, or Cmd+C on a selection, writes plain markdown and chat-safe HTML with a header line per message ([formats, Copy](../formats.md#copy)).

**Filter bar** (as on [Tasks](tasks.md)): search and saved views; the state quick filter on its own line; then the select square, Group and Sort chips ([filter bar](../controls/filter-bar.md)).
* System presets (OR-33): "Needs me" is waiting and failed, "Running" is preparing, running and waiting. A preset sets only the state filter; grouping, sort, projects and search stay. Personal saved views restore everything they saved, including the archived list.
* Group (OR-35): project (default), state in attention order (waiting, failed, running, preparing, idle, succeeded, cancelled), agent (harness) or none. Headers show the group's count for the active filters.
* Sort (OR-35): last activity (default, latest first) or created, with a direction toggle. Last activity is the latest change of the conversation, its messages and events, or the start or end of its executions (`sessions_list` `activityAt`).
* Archived (last on the tools line): an icon-only toggle `Button` (`archive` icon, named "Archived", pressed while on) switches between active and archived conversations in one click. It sets `archived=1` in the view URL, so it is part of saved views and of the section view the navigation returns to; turning it on or off ends selection mode. In the archived list the state counters, group counts and the count count archived conversations only, pinned rows show only if archived, and the empty state says "No archived conversations." with "Show active".

**List row (OR-22, OR-37):** on a pinned row, the [pin](../controls/row-pin.md) first; the state icon; the first message as the title, which takes all free width, with its ID on the second line; the row facts on the right: open questions (attention tone), executions, time since the last activity, harness mark. No buttons appear on hover: pin, select, copy and Archive are in the [row menu](../controls/row-menu.md); `p` pins and `x` selects the focused row.
* Pinned conversations are a Pinned group at the top, kept whatever the filter, search or grouping (OR-12).
* [Selection mode](../controls/selection.md): checkboxes only in the mode, as their own column left of the state; the bar acts on the selected rows: Pin, Unpin and Archive (confirmed inline; `session_archive` per conversation, refused while an execution is active). In the archived list Unarchive replaces Archive, in the bar and the row menu: `session_unarchive` per conversation (owner or full access), no confirmation, since it only returns the conversation to the active list; its worktree is prepared again at the next start.
