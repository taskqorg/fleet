# Conversations (operator)

**App routes:** `/conversations` and `/conversations/:id` in the operator app (`app/src/views/conversations.tsx`). The app check (`tests/ui/app.check.ts`) verifies them. There is no static mockup.

**Purpose.** Talk to agents and steer running work. This page replaces the legacy session page (`/legacy`).

**Layout.** [App shell](../patterns/app-shell.md), then [list and detail](../patterns/list-detail.md):
* **Left:** a list of conversations, using the [list view](../patterns/list-view.md) and [state counters](../controls/state-counters.md) with the `session` state family.
* **Right:** the conversation, showing any open [decision blocks](../controls/decision-block.md), the [live transcript](../controls/transcript-viewer.md) and the [composer](../controls/composer.md).

| Inventory item (Conversations) | Shown by |
|---|---|
| Messages, tool activity, results and questions with answer options | The transcript, plus decision blocks for open questions. |
| Attach materials; assign work; create tasks | Not in this slice. |
| Continue with another agent or account: hand off and fork | "Fork" is an icon in the detail head (`session_fork`). Handoff and profile switch are not in this slice. |
| Steer or queue a message to a running agent | The composer (`message_send`), and Stop for the running execution (`execution_stop`). |

**List row (OR-22):** state icon, then the first message with its ID, then the harness mark, then the open questions count.
