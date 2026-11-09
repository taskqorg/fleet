# Tasks (operator)

**Mockup routes:** `#/tasks`, `#/tasks/9989` (waiting), `#/tasks/9978` (failed) in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** Find, triage and control work. Accept or return results.

**Layout.** [App shell](../patterns/app-shell.md), then [list and detail](../patterns/list-detail.md): the [list view](../patterns/list-view.md) on the left and the task detail on the right. The page head holds "New task" (primary).

| Inventory item | Shown by |
|---|---|
| Expected outcome, inputs, requirements, dependencies | Detail: an "Outcome" tab is still to be added; the mockup shows the title only |
| State with reason and release condition | [State icon](../controls/state-badge.md) in the list (reason in tooltip); badge with word and reason in the detail |
| Executions and steps as a tree, with agent, host, account, time and cost with basis | Detail, Executions tab: [execution tree](../controls/execution-tree.md) |
| Live transcript with paging and search | Detail, Transcript tab: [transcript viewer](../controls/transcript-viewer.md), relative times |
| Results and their checks | Detail, Results tab: [result viewer shell](../controls/result-viewer.md) |
| Accept, request rework, pause, continue, cancel, retry, change account, hand off | [Decision block](../controls/decision-block.md) (accept or rework); [split button](../controls/split-button.md) Pause with Cancel, Change account and Hand off; [problem row](../controls/problem-row.md) Retry |
| Share the task or a result | Share icon in the detail head |

**List columns (OR-21):** title, then ID, state, agent, age, cost and pin.

**Open.** The Outcome and Inputs tab, and the priority column.
