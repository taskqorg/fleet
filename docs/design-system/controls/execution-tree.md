# Execution tree

**Code.** `ExecTree` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Shows a task's executions and their steps, pipeline steps and tool calls as a tree, each with state, agent, host, account, time and cost with basis.

**Anatomy.** Recursive rows of `size-row` height ordered from `parentExecutionId` hierarchy. Each row: disclosure ▸/▾, compact [state badge](state-badge.md), name ("Reviewer · claude · mac"), then right-aligned [numbers](number.md): time, tokens, cost (`CostFigures` supporting multi-currency metered and subscription equivalents). Direct `tool_run` children of an execution group into one summarized node per parent (count, errors, and summed duration; collapsed unless running or failed, expanding to individual tool calls on demand). Children indent by 16 px with a 1 px `line` guide and expand cleanly in flow without nested scrollbars (OR-25).

**States.** Collapsed, expanded, selected, live (running rows update in place), reused (a rerun step reused from an earlier run shows "reused from run 2" in `fg-muted`), unconfirmed (badge overlay).

**Behaviour.**
* Default expansion: the path to anything waiting, failed or running is open; finished branches are closed.
* Selecting a row opens it in the detail area (transcript, result, error) without leaving the task.
* Large trees are virtualised like the [data list](data-list.md) with anchor keeping; live children appended inside an open node never move rows above them.
* Pipeline runs show the definition's steps combined with run state, including steps that have not started ("— not started").

**Keyboard.** Tree pattern: `↑`/`↓` move, `→` expands or goes to first child, `←` collapses or goes to parent, `Enter` opens, `*` expands siblings.

**Accessibility.** `role="tree"` with `treeitem`, `aria-expanded`, `aria-level`, `aria-setsize`, `aria-posinset`.

**Do.** Keep numbers aligned across levels. **Don't.** Draw a graph diagram for an ordinary run; hide failed steps.

**Used in.** Task detail, Conversations (when spawned executions exist), Laboratory trial detail, Runners drill-down.
