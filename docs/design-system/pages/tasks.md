# Tasks (operator)

**Mockup routes:** `#/tasks`, `#/tasks/9989` (waiting), `#/tasks/9978` (failed) in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** Find, triage and control work. Accept or return results.

**Layout.** [App shell](../patterns/app-shell.md), then [list and detail](../patterns/list-detail.md): the [list view](../patterns/list-view.md) on the left and the task detail on the right. The page head holds "New task" (primary).

| Inventory item | Shown by |
|---|---|
| Expected outcome, inputs, requirements, dependencies | Detail: [outcome block](../controls/outcome-block.md) above the tabs (B2) |
| State with reason and release condition | [State icon](../controls/state-badge.md) in the list (reason in tooltip); badge with word and reason in the detail |
| Executions and steps as a recursive tree (`parentExecutionId`) with summarized `tool_run` children, agent, host, account, time and multi-currency/`subscription` cost with basis | Detail, Executions tab: [execution tree](../controls/execution-tree.md) and [number](../controls/number.md) |
| Live transcript with paging and search | Detail, Transcript tab: [transcript viewer](../controls/transcript-viewer.md), relative times |
| Results and their checks | Detail, Results tab: [result viewer shell](../controls/result-viewer.md) |
| Accept, request rework, pause, continue, cancel, retry, change account, hand off | [Decision block](../controls/decision-block.md) (accept or rework); [split button](../controls/split-button.md) Pause with Cancel, Change account and Hand off; [problem row](../controls/problem-row.md) Retry |
| Share the task or a result | Share icon in the detail head |

**Current work first:** show the backend task state and reason, linked deliverable and open decision text/options before the full outcome. Show the latest execution problem separately, including when that execution has no report. Show the latest available stored worker report with its report time with its reported outcome and summary; an execution report does not establish task completion, verification or publication. Withdrawn questions are history, never answerable next steps. Label answered decisions as previous answers. Preserve the complete outcome in the shared inline disclosure.

**Detail (OR-39):** the statement (title and [outcome block](../controls/outcome-block.md), with no requirement chip when there are no requirements), the lifecycle (state badge, author, age, known totals only: total elapsed span (`elapsedMs`) vs active execution wall (`executionWallMs`), CPU time (`cpuMs`), peak memory, tokens, tool calls/errors, and multi-currency / separated `'subscription'` cost via `CostFigures` without `.b-unavailable` cells in `Totals`; plus `ScopedResourceRollup` breakdown of `own`, `delegated`, and `spawned` work, links to `spawnedTasks`, a `hidden` count disclosure when `> 0`, and the recursive [execution tree](../controls/execution-tree.md)), the result (latest result, linked to Results) and the decision (open decision blocks, then the last answered ones with who and when). Note: `Totals` stays placed below `OutcomeBlock` and open decisions so urgent decisions and failures remain above folded rollups. The task UUID is a copy icon with the UUID in its tooltip, never shown as text.

**Catalogue row (OR-22, D19):** state icon; title, and on the second line its ID, the [harness mark](../controls/harness-counts.md) while running and the [delivery stage](../controls/stage-chip.md); then age, cost and pin on the right. There are no column heads. Priority is a grouping (D11).

**Open.** None from stage 3. Phone navigation (d) is still open.
