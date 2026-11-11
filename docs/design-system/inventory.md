# Interface inventory

What the operator and architect interfaces must let people see, configure, choose and decide. It is derived from `fleet.md`, `docs/designs/fleet-operator-app.md#interface-workflows`, `docs/use-cases.md` and `docs/designs/fleet-data-foundation.md`. It names information and actions, not screens. The design work decides how they are grouped into pages and which controls carry them.

Item kinds used below:

* **View**: information people read.
* **Setting**: a value people configure and that stays until changed.
* **Choice**: a pick among options at one moment, such as which agent should continue a task.
* **Decision**: an answer that work is waiting for: questions, acceptance, approvals.
* **Action**: an operation people start, such as pause, retry or share.
* **Signal**: something that needs attention: a problem, a warning or a waiting reason.

Every item is also available to agents through the same operations (R2). The interface adds no operations of its own.

## Cross-cutting needs

These apply to every workspace and are the main input for the controls:

* **Long lists that scroll well.** Tasks, executions, events, transcripts and artifacts grow without limit. The lists need smooth scrolling that does not jump while new rows arrive, a stable position when content above changes, and fast filtering and search.
* **Filters and grouping.** By project, task, experiment, host, account, agent or harness, state, kind, time range and cost. Filters are shareable in a link and remembered per person.
* **Live state.** Running work updates in place. Each item shows its observation state: confirmed, unconfirmed or last seen at a given time.
* **States everywhere.**
  * Task states: queued, in progress, waiting, paused, completed, failed, cancelled.
  * Execution states: preparing, running, waiting, succeeded, failed, cancelled.
  * Copy states: stored, placed, pending, unavailable.
  * Setting states: applied or apply error.
  * Every state shows its reason and, where one exists, its release condition.
* **Measurement basis.** Every number can be measured, estimated or unavailable, and the three must look different. An unknown value is never shown as 0.
* **Drill-down.** From any total, open its source records. From any problem, open its cause, its evidence and the actions available.
* **Decisions inline.** Questions, acceptance and approvals appear where the work is and in one overview of everything waiting. The addressee and the waiting reason are visible. Answers can be typed or picked from options.
* **Result viewers.** Text and reports, code diffs, images with zoom, video with a poster frame, 3D models later, and links to a running preview.
* **Light and dark, desktop and phone.** The web comes first; desktop apps reuse the web interface later.

## Operator workspace

### Overview

| Item | Kind | Source |
|---|---|---|
| Tasks across projects by state, priority and progress | View | `tasks.list` |
| Everything waiting for a person: questions, acceptance, approvals, problems | Signal, Decision | questions table, problems |
| Load and capacity by host and account, with placement reasons | View | `hosts.list`, executions, rate-limit measurements |
| Account and quota state per subscription: measured window use, estimate, reset time | View | `rate_limit` measurements |
| Change priority; pause or continue work | Action | `tasks.update` |

### Projects

| Item | Kind | Source |
|---|---|---|
| Purpose, context materials, rules | View, Setting | project config |
| Connections bound to repositories, boards, CI and services | Setting | project config, connections |
| Selected agents, environments, tools and recommendations | Setting | project config, catalogue |
| Artifact placement: `artifact_locations`, separately for lab and production | Setting | project config |
| Retention rules, with a preview of what a rule will affect and a confirm step | Setting, Decision | `artifact_rules` |
| Acceptance rules: which results need a person | Setting | project rules |
| Readiness checks and their results | View, Signal | `project_readiness` |

### Conversations (sessions, including with the PM)

| Item | Kind | Source |
|---|---|---|
| Messages, tool activity, results and questions with answer options | View, Decision | session events |
| Attach materials; assign work; create tasks for several projects | Action | session and task operations |
| Continue with another agent or account: handoff and fork | Choice, Action | `session_transition`, `session_fork` |
| Steer or queue a message to a running agent | Action | `message_send` |

### Tasks

| Item | Kind | Source |
|---|---|---|
| Expected outcome, inputs, requirements, dependencies | View, Setting | task |
| State with reason and release condition | View, Signal | task |
| Executions and steps as a tree, with per-step state, agent, host, account, time and cost (each with its basis) | View | executions, totals |
| Live transcript of a running execution, with paging and search | View | `transcript.read` |
| Results and their checks | View | artifacts, checks |
| Accept, request rework, pause, continue, cancel, retry, change account, hand off | Decision, Action | task and execution operations |
| Share the task or a result with an email, with an optional expiry | Action | `shares.create` |

### Results

| Item | Kind | Source |
|---|---|---|
| Find results by project, task, type and time | View | `artifacts.list` |
| Open in the right viewer, compare versions, annotate | View, Action | artifacts, previews |
| Copy state per location (stored, placed, pending, unavailable) | View, Signal | `artifact_copy` |
| Share with an email | Action | `shares.create` |

## Architect workspace

### Agents

| Item | Kind | Source |
|---|---|---|
| Agent configurations: harness, model, instructions, skills, tools, environment requirements | View, Setting | `config/agents/*` |
| Diff between revisions; where a configuration is used | View | git, executions |
| Harness facts and limitations per version, recorded rather than blocking | View | compatibility facts |

### Capabilities, methods, tools and pipelines

| Item | Kind | Source |
|---|---|---|
| Catalogue entries with purpose, inputs, results and recommendations | View, Setting | catalogue |
| Pipeline definitions with steps and transitions, combined with run state | View | pipelines, runs |

### Laboratory

| Item | Kind | Source |
|---|---|---|
| Experiments: variants, configuration differences, evaluators, task set | Setting | experiments |
| Trials per variant with scores (objective and judge), tokens, time and cost, each with its basis | View | executions linked to the experiment |
| Comparison of variants, with drill-down to individual runs | View | `usage.summary`, scores |
| Assessments (comments, limitations, suitability) and recommendations per kind of work | Decision, Action | assessments, recommendations |

### Hosts, environments and stores

| Item | Kind | Source |
|---|---|---|
| Hosts: platform, last contact, observation state, harness versions, readiness | View | `hosts.list` |
| Environments and account users per host | View | `hosts.list` |
| Stores: capacity, used and free bytes, tunnel state, fill per project | View, Signal | `hosts.list`, `host_metric` |
| Store capacity, service memory and CPU bounds, free-space warning | Setting | `hosts.set_setting` (applied_at or apply_error) |
| Low space: raise capacity, change a project's locations, or add a retention rule | Signal, Action | problems |

### Connections and access

| Item | Kind | Source |
|---|---|---|
| Connections: service, accounts, limits, consuming projects; API cost per call | View, Setting | connections, `connection_cost` |
| People and their roles and project grants (read-only; edited in `people.json`) | View | `person`, `person_grant` |
| Shares: email, object, expiry; revoke | View, Action | `share` |
| Access status: differences between Cloudflare and Fleet, and hand edits still needed | Signal | `access.status` |

### History and analysis

| Item | Kind | Source |
|---|---|---|
| Time, cost, tokens, CPU and waits by project, task, kind, agent, account, host and configuration | View | `usage.summary` |
| Drill-down from a total to executions, spans and transcripts | View | `executions.get`, `transcript.read` |
| Discuss findings with an agent; start an experiment from selected work | Action | sessions, experiments |
