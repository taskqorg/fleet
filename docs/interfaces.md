# Interface workflows

Workflows for the separate [operator and architect interfaces](../fleet.md#operator-interface). These define activities and information, not menus or screen layouts.

## Interaction model

Support both conversation-led work and autonomous assignments. Sessions show messages, tool activity, results and questions, including answer choices, while preserving supported harness capabilities. Tasks summarize outcomes, progress and decisions and link to relevant sessions and executions; neither view replaces the other.

People may discuss, inspect results and request rework within a session. Background work continues without an open conversation. Show questions in the work overview and relevant conversations as views of the same record. Reveal execution details when needed rather than requiring navigation through every object.

The objects below identify shared information and actions, not mandatory forms, screens or operator steps. Domain-specific content belongs to project tools and materials; Fleet does not require a universal schema for every kind of work.

## Operator workflow

| Workspace | Information and actions |
|---|---|
| Overview | Tasks by project, progress, results and situations needing attention; manage priorities and balance work across hosts and harnesses with visible load, quotas and placement reasons. |
| Projects | Purpose, materials, workflow rules, connections and selected capabilities; open related tasks and results. |
| Sessions | Ordinary conversations with a selected agent, including PM; related projects and tasks; continuation through another agent. |
| Tasks | Expected result, requirements, executions, steps, questions, files, checks and costs; control work and accept results or return them for rework under project rules. |
| Results | Find and inspect materials, code changes and checks; open the work that produced them. |

Surface questions and problems in the overview while keeping answers and actions attached to the affected work. Show interruption causes and available actions: answer, change account, hand off, retry or cancel within permissions.

A session need not belong to one project; it may discuss and create tasks for several projects. A task presents its outcome and progress with access to nested pipeline steps, agent executions and tool calls when needed.

A representative path is: interrupted task -> cause -> another available agent -> continued work -> result in the same task. These workspaces do not prescribe the final menu.

### Operator objects

| Object | Detail and actions |
|---|---|
| Project | Purpose, description location, rules, available methods and tasks. Discuss, configure and open related work. |
| Session | Conversation, selected agent, related projects and tasks. Attach materials, assign work and hand off to another agent. |
| Task | Expected outcome, inputs, requirements, progress, questions and results. Clarify, prioritize, pause, continue, cancel, accept or request rework. |
| Result | Material, producing execution, checks and comments. Inspect, compare, annotate and reuse as input. |

## Architect workflow

| Workspace | Information and actions |
|---|---|
| Agents | Configurations, harnesses, models, instructions, skills and assigned tools; inspect changes and usage. |
| Capabilities and methods | Expected outcomes, alternative implementations, tools, pipelines and recommendations. |
| Environments and resources | Register hosts and environments; inspect availability, software, working areas, preparation, cleanup, capacity, consumption and allocation rules. |
| Connections and access | Services, accounts, limits and project access. |
| History and analysis | Project-wide time, costs, resources, waits and repeated work; drill down to executions and discuss findings with an agent. |
| Laboratory | Compare variants from new or recorded work, inspect numeric and qualitative results, control budgeted improvement runs and apply selected improvements. |

These workspaces organize the architect's work, not services or repositories. Their objects link across workspaces; exact menus and screen layouts remain unspecified.

The catalogue follows [shared and project scopes](capabilities-and-pipelines.md#catalogue-scope). A project view includes its entries and available shared methods; the cross-project overview identifies each entry's scope. Ordinary sessions support [pipeline development](capabilities-and-pipelines.md#pipeline-development).

The laboratory workflow is:

1. Configure an agent, method or pipeline.
2. Run experiments or benchmarks; retain each run's conditions and results.
3. Add comments, tags, comparisons, limitations and suitability conclusions.
4. Recommend a method for specific tasks within a project or across the system.

Follow the [experiment contract](experiments.md) when applying improvements. Assessments alone do not change recommendations or product results.

### Architect objects

| Object | Detail and actions |
|---|---|
| Agent configuration | Purpose, harness, model, instructions, skills, assigned tools and environment requirements. Edit, inspect diffs, test and open executions. |
| Skill | Reusable content and the agents using it; browse independently or open from a configuration. |
| Capability | Input and result contracts, recommended methods and alternatives. Open a method to inspect its implementation. |
| Method | Implementation, settings, requirements, check results and real examples. Open its pipeline or agent configuration where applicable. |
| Tool or pipeline | Independently discoverable and editable. Pipeline detail exposes steps and transitions. |
| Environment | Supported work, available resources, active work and working areas. Open an area's users, occupied space, retention reason and available cleanup action. |
| Connection | Service, configured accounts, state, available limits and consuming projects and tools. Configure access; agent configurations reference tools without receiving copied credentials. |
| Execution | Task where applicable, caller, selected method, effective configuration, nested steps, history, results and costs. Open source objects, continue work or create an experiment. |
| Experiment | Source work, variants, configuration differences, criteria and run results. Repeat trials, compare, assess and apply an improvement; show its effect on future work and selected existing results. |

Cross-workspace navigation opens the same objects rather than independently maintained copies. A skill reached from an agent is the same skill shown in its catalogue. Result viewers remain available in execution context.

A pipeline-backed method may use one integrated detail view; distinct concepts do not require separate forms. Every interface action uses the same system operations available to agents under equivalent permissions.

A representative path is: expensive model generation -> execution -> costly step -> method and agent -> experiment with a different skill -> comparison -> application of the improvement.

## Character workflow prototype

The [CSGO storyboard](prototypes/csgo-workflow.html) is a historical navigation study with synthetic data. It overemphasizes sequential screens and does not establish the interaction model above, native harness fidelity or project acceptance. Do not use it as an approved layout or required workflow.

Inspect the same result across operator and architect views. Compare a replacement with its predecessor, then distinguish applying it to existing work from recommending its method for future work. Application follows project checks; it does not imply immediate publication.

## Result viewing

Both interfaces reuse viewers suited to the [result type](capabilities-and-pipelines.md#results-and-transfer): readable text and reports, zoomable images, interactive 3D models, and file changes with diffs.

Results may be stored materials or references to an external source; provide convenient inspection in either case. For a working product, provide access to its run or preview and checks where applicable; a code diff alone does not show product behavior.

## Contextual actions

From a problem, expose its cause, supporting data and available actions without losing the associated work context. Use the [shared operations](system-operations.md), permissions and state.

- Exhausted quota: show the affected work and available continuation options.
- Large retained worktree: show its users, retention reason, occupied space and whether cleanup is available.
- Execution failure: show the failed operation, retained evidence and applicable recovery actions.

Supported actions and uncertain state remain visible; a displayed action does not establish that its operation has completed.
