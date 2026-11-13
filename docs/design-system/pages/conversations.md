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

**List row (OR-22):** state icon, then the first message with its ID, then the harness mark, then the open questions count.
