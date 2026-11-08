# Interface workflows

Workflows for the separate [operator and architect interfaces](../fleet.md#operator-interface). These define activities and information, not menus or screen layouts.

## Operator workflow

| Activity | Information and actions |
|---|---|
| Overview | Tasks by project, progress, results and situations needing attention; manage priorities and balance work. |
| Discussion | Ordinary agent sessions, including PM sessions, with links to related work. |
| Task inspection | Goal, executions, steps, questions, files, checks and costs. |
| Continuation | Interruption cause and available actions: answer, change account, hand off, retry or cancel within permissions. |
| Acceptance | Inspect results and changes; accept or return for rework under project rules. |

## Architect workflow

| Activity | Information and actions |
|---|---|
| Catalogue | Agents, skills, tools and pipelines: purpose, configuration, change history and recommendations. |
| Infrastructure | Environments, working areas, connections, access, limits and resource consumption. |
| Analysis | Execution history, tool calls, errors and costs, linked to the underlying work. |
| Laboratory | Create experiments from recorded executions, compare variants and record assessments. |
| Application | Update recommendations and apply selected improvements to existing work. |

The laboratory workflow is:

1. Configure an agent, method or pipeline.
2. Run experiments or benchmarks; retain each run's conditions and results.
3. Add comments, tags, comparisons, limitations and suitability conclusions.
4. Recommend a method for specific tasks within a project or across the system.

Follow the [experiment contract](experiments.md) when applying improvements. Assessments alone do not change recommendations or product results.

## Contextual actions

From a problem, expose its cause, supporting data and available actions without losing the associated work context. Use the [shared operations](system-operations.md), permissions and state.

- Exhausted quota: show the affected work and available continuation options.
- Large retained worktree: show its users, retention reason, occupied space and whether cleanup is available.
- Execution failure: show the failed operation, retained evidence and applicable recovery actions.

Supported actions and uncertain state remain visible; a displayed action does not establish that its operation has completed.
