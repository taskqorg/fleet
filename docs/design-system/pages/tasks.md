# Tasks (operator)

**Target home.** The Tasks facet of a project: `/projects/<code>/tasks` and `/projects/<code>/tasks/T-FLT-42` ([projects](projects.md#target)); the rail item Tasks leaves ([OR-46](../patterns/navigation-and-columns.md#rules-kept-here)). The list and detail below keep their rules inside the facet.

**Mockup routes:** `#/tasks`, `#/tasks/9989` (waiting), `#/tasks/9978` (failed) in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** Find, triage and control work. Accept or return results.

**Layout.** [App shell](../patterns/app-shell.md), then [list and detail](../patterns/list-detail.md): the [list view](../patterns/list-view.md) on the left and the task detail on the right. The page head holds "New task" (primary).

| Inventory item | Shown by |
|---|---|
| Expected outcome, inputs, requirements, dependencies | Detail: [outcome block](../controls/outcome-block.md) above the tabs (B2) |
| State with reason and release condition | [State icon](../controls/state-badge.md) in the list (reason in tooltip); badge with word and reason in the detail |
| Executions and steps as a recursive tree (`parentExecutionId`) with summarized `tool_run` children, agent, host, account, time and multi-currency/`subscription` cost with basis | Detail, Executions tab: [execution tree](../controls/execution-tree.md) and [number](../controls/number.md) |
| The task's conversations: the one that created it, its workers and reviewers, a bot's conversation about it ([conversation model](../../designs/fleet-conversations.md#entities)) | Detail, History tab (the first tab; [`screens/history.bl`](../board/screens/history.bl)): the task's [history](history.md) (`history_view {taskId}`), newest first by day: its conversations' runs, questions and state moves interleaved; a row opens its conversation |
| Live transcript with paging and search | Detail, Transcript tab: [transcript viewer](../controls/transcript-viewer.md), relative times |
| Results and their checks | Detail, Results tab: [result viewer shell](../controls/result-viewer.md) |
| Accept, request rework, pause, continue, cancel, retry, change account, hand off | [Decision block](../controls/decision-block.md) (accept or rework); [split button](../controls/split-button.md) Pause with Cancel, Change account and Hand off; [problem row](../controls/problem-row.md) Retry |
| Share the task or a result | Share icon in the detail head |

**Current work first:** show the backend task state and reason, linked deliverable and open decision text/options before the full outcome. Show the latest execution problem separately, including when that execution has no report. Show the latest available stored worker report with its report time with its reported outcome and summary; an execution report does not establish task completion, verification or publication. Withdrawn questions are history, never answerable next steps. Label answered decisions as previous answers. Preserve the complete outcome in the shared inline disclosure.

**Detail head (J4):** the project, then **Open session** (the latest execution's session, `/conversations/<id>`), Pin and the state action. Under the title the state line adds the [delivery stage](../controls/stage-chip.md) of the task's first pull request, and each of its pull requests, the first (lowest number) first, follows as a [live row](../controls/live-row.md) kind delivery that opens it in Delivery. A pull request is the task's when its head branch is the branch of one of the task's sessions (`<branch prefix>/<session id>`), however the worker opened it, so workers take no extra step; `publishing_status` and `delivery_status` items name its `sessionId` and `taskId`, and `publishing_status` with `taskId` gives one task's pull requests. Delivery keeps a merged pull request for 24 hours, so an older one leaves the head.

**Detail (OR-39):** the statement (title and [outcome block](../controls/outcome-block.md), with no requirement chip when there are no requirements), the lifecycle (state badge, author, age, known totals only: total elapsed span (`elapsedMs`) vs active execution wall (`executionWallMs`), CPU time (`cpuMs`), peak memory, tokens, tool calls/errors, and multi-currency / separated `'subscription'` cost via `CostFigures` without `.b-unavailable` cells in `Totals`; plus `ScopedResourceRollup` breakdown of `own`, `delegated`, and `spawned` work, links to `spawnedTasks`, a `hidden` count disclosure when `> 0`, and the recursive [execution tree](../controls/execution-tree.md)), the result (latest result, linked to Results) and the decision (open decision blocks, then the last answered ones with who and when). The compact totals appear directly below the current outcome. Scope rows appear when there is delegated or spawned work. Requirements remain visible below the expected outcome. The task UUID is a copy icon with the UUID in its tooltip, never shown as text.

**Catalogue row (OR-22, D19, OR-37):** on a pinned row, the [pin](../controls/row-pin.md) first; the state icon; the title, which takes all free width, and on the second line its ID and the [harness mark](../controls/harness-counts.md) while running; the row facts on the right: open questions, the [delivery stage](../controls/stage-chip.md) (the task row's `stage`, its first pull request's, while Delivery holds it; the list rereads on a delivery change), run evidence that needs a look (stale, stalled, unconfirmed), spend, then the shared [work facts](../controls/work-facts.md) last (`×N` attempts, agent, time in work, the time the rightmost fact, OR-64; the latest attempt and the time in the state on hover; outliers against the page's open tasks, OR-63). A finished task shows how long ago it reached its state instead of a time in work. No fixed "running too long" limit. No buttons appear on hover: pin, select and state actions are in the [row menu](../controls/row-menu.md), and checkboxes appear only in [selection mode](../controls/selection.md). There are no column heads. Priority is a grouping (D11); pinned tasks are a Pinned group at the top.

**Filter bar:** search and saved views; the state quick filter on its own line; then the select square, Group and Sort chips ([filter bar](../controls/filter-bar.md)).

For coordinator-local publication leaves, the execution tree names the coordinator service instead of a native host and shows the configured publication authority separately. A configured authority is not a live host observation.

**Open.** None from stage 3. Phone navigation (d) is still open.
