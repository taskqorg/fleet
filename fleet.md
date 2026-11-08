# Fleet

A system for performing work through agents across projects. This specification defines the target architecture, not implementation status.

## Principles

- **R1 Shared rules.** [specq](https://github.com/taskqorg/specq) governs engineering, design and documentation.
- **R2 Shared core.** Human and agent interfaces use the same core operations and state.
- **R3 System requirements.** [Reliability, authority and resources](#system-requirements) apply to every block.
- **R4 Design depth.** Agree on each level's parts, responsibilities and relationships before detailing any part. Revisit earlier levels when needed.

## Operator interface

A desktop application for Windows, macOS and Linux, used across all projects.

Conversation with an interactive product manager (PM) is the primary way to discuss and assign work.

A PM is an ordinary session in a chosen agent, with role-specific instructions, tools and project access. Sessions may span related projects; they are not permanent project channels. Conversation has no separate request object.

- Projects and tasks: describe work and set priorities.
- Execution: balance work and track progress.
- Decisions: answer questions and resolve blockers.
- Results: inspect what was done, where to find it, what was checked and what remains; accept work under project rules.

## Architect interface

Configure and qualify the agents and facilities available to operators.

- Agents: manage configurations, harnesses and models.
- Skills and tools: manage available capabilities and their assignment to agents.
- Environments: prepare execution conditions.
- Resources: manage capacity, provider quotas and spending limits.
- Evaluation: test compatibility, benchmark configurations and rerun checks after changes.

Laboratory workflow:

1. Configure an agent, method or pipeline.
2. Run experiments or benchmarks; retain each run's conditions and results.
3. Add comments, tags, comparisons, limitations and suitability conclusions.
4. Recommend a method for specific tasks within a project or across the system.

Retain assessments as knowledge. Changing a recommendation is a separate architect action; comments and conclusions do not change it automatically.

## Shared core

### Work management

A project groups the goal, context, rules and work for a product or area.

Its configuration contains:

- Purpose and context: specifications and materials.
- Connections: repositories, task board, data sources, CI and external services.
- Workflow: project instructions, checks, review and completion conditions.
- Execution: references to agent configurations, environments, tools and catalogue recommendations.
- Delivery: result storage, merge, publication and deployment.

A project may use several repositories or contain no code. Repository, CI and deployment connections are independent.

Inherit shared defaults; store project differences and references to shared capabilities. Selecting a capability does not grant access to it.

Own work from task creation to an accepted result:

- Task: identity, project, expected outcome, dependencies, completion requirements and result destination.
- Execution: agent and environment assignment, state and execution history.
- Questions: retain questions and context with the task, deliver operator answers to the relevant execution and continue.
- Results: retain accessible materials and checks linked to their task and execution. Projects choose repositories, file or object storage, or external systems as destinations; Fleet need not store every artifact itself.
- Completion: distinguish creation, verification and publication; enforce the task's requirements under project rules.

The queue advances automatically when dependencies, a suitable executor and environment, granted access and resource limits permit. Human decisions or project rules may require a pause.

A task may have multiple executions for creation, review and rework. Later executions preserve earlier results and checks.

Created tasks continue without an open PM session; closing a session window does not cancel the task. An agent finishing does not establish task acceptance.

Manual acceptance is not required for every task. Project rules determine which results need human acceptance and which can be accepted automatically. Operators can inspect results and intervene.

#### Task states

| State | Meaning |
|---|---|
| Queued | Accepted for execution, awaiting launch. |
| In progress | An agent, tool or pipeline is doing the work. |
| Waiting | Continuation needs an answer, dependency result or external condition. |
| Paused | The operator intentionally halted progression; resumption requires permission. |
| Completed | The required result and completion conditions are satisfied. |
| Failed | Execution ended without the required result; automatic continuation has stopped. |
| Cancelled | A decision ended further work on the task. |

Show each wait's reason and release condition. Missing executors, environments, access or capacity before launch keep the task queued with the missing prerequisite visible.

Show review, merge and deployment as operations or stages alongside task state. Merge, publication and deployment use project connections; track their state and results even without an active agent. Running CI or deployment counts as work.

Projects define operations and transition conditions; shared state meanings do not change with the project or its providers.

After execution failure, retain the error and follow project rules: continue automatically where safe and authorized, wait for a required decision, or mark the task failed when work ends without its result.

Pause and cancellation prevent new steps. Show running operations and whether they can be stopped; cancellation requests their stop. Neither action proves immediate execution termination.

Neither pause nor cancellation rolls back completed external effects. Rollback requires a separate decision.

### Agent execution

Run and control agents through their harnesses. Preserve supported harness capabilities, including images, history and session controls, as harnesses evolve.

An agent configuration combines a harness, model and settings, instructions, skills, tools and environment requirements.

A role defines an agent's purpose through instructions and tools, not a hard-coded agent type. Provide standard roles, including PM, worker and reviewer; projects may adapt them or add their own.

An execution binds a configuration version and role to inputs, an environment, permissions, a session and a task when applicable.

Resolve settings in order: selected agent configuration and role, project rules and settings, then task requirements and explicit tool or method choices.

Task choices may override defaults, not mandatory project requirements. Conflicting requirements need resolution.

Before launch, check environment compatibility and granted permissions. Retain the configuration actually used with the execution.

A session holds conversation and working context within a harness: messages, actions and interaction history. It may exist without a task.

An execution may start a new session or continue an existing one, according to the work and harness capabilities. Task, execution and session lifecycles remain distinct.

Execution states are preparing, running, waiting, succeeded, failed and cancelled. Available controls depend on the harness or tool.

### Environments

An environment provides:

- Compute: machine, OS and available resources.
- Software: installed programs, dependencies and capabilities.
- Workspace: project files, checkout or worktree, inputs and results.
- Interaction: terminal, browser, APIs and desktop control through computer use (CUA).
- Access: available data, accounts and external services.

Manage its lifecycle: prepare, check readiness, make available for work, preserve or release. Working files are distinct from retained results; preserve required results and history before releasing an environment.

Agents specify environment requirements; the system selects a concrete local or remote environment with verified capabilities. An agent configuration need not bind to one machine.

Agents primarily work in dedicated environments. An environment may serve multiple tasks; isolate working files and concurrent actions where conflicts are possible. A separate machine per task is not required. People can connect to observe or intervene, including through a desktop when available.

A personal desktop may be explicitly connected as an environment; an agent can request access. Shared desktop work with a person is not a primary scenario.

### Tools and pipelines

- Catalogue: describe tools, methods and pipelines, their purpose, inputs, results, usage conditions and current recommendations.
- Connections: provide access to harness tools, MCP, external APIs and system services.
- Pipelines: structured text definitions compose tools, programs and agents through steps, dependencies, executors, inputs and outputs. Keep definitions, step instructions and run state separate; the system coordinates steps and result transfer.
- Execution and results: start operations, observe their state and retrieve results. Pipeline views combine the definition with run state; they do not maintain a separate workflow definition.

Configure service connections separately; projects and agents reference them. Store secrets outside versioned files. Request missing access through the operator question mechanism.

A pipeline may be exposed as an agent tool. Its definition owns the internal steps; the caller uses its operation description and receives its results.

Architects manage the catalogue, connections and pipelines. Agent configurations select available capabilities.

Humans and agents use one catalogue. Human views support inspection and discussion with a PM. Agents discover applicable descriptions, recommendations and instructions on demand, without browsing the human interface or loading the whole catalogue into context.

Honor explicit tool, method or pipeline choices made when planning work with the PM. Otherwise, agents choose using current project recommendations, falling back to system recommendations where the project defines none.

Provide agent operations to find and launch agents, select and use environments, create and track tasks, run pipelines and retrieve results within granted permissions.

### Knowledge and rules

- Specifications: define the product, accepted decisions and result requirements.
- Instructions: define agent behavior through shared rules, roles and project overrides.
- Skills: provide reusable methods for kinds of work.
- Context: supply task materials, related decisions, results and project information.

Skills, instructions, agent configurations and pipeline definitions are canonical Git repository files. The catalogue references them rather than storing editable copies.
Use current versions unless a version is explicitly selected. Executions record the repository, commits and paths actually used; separate copies are optional if referenced revisions remain accessible.

Provide applicable knowledge and rules with their sources. Each execution exposes its effective instructions and project overrides.

Each rule or decision has one canonical source; other locations reference it. Show conflicts that the override order does not resolve.

Keep built-in workflow requirements minimal. Provide standard behavior through default instructions and a mechanism for project overrides.

Project rules may override PM communication, task formulation, execution practices, when to ask for decisions, required results, result presentation and storage, and review, merge or publication policy.

PMs apply the project's effective rules when preparing tasks. Executing agents apply them when doing the work and delivering results.

Refine defaults through real work across projects, from conversation through execution to usable results.

### Observation and evaluation

- Live execution: agents, locations, progress, waits and problems.
- History and evidence: actions, results and verification materials.
- Changes: operators and architects can inspect diffs between skill and agent configuration revisions using existing version-control and diff tools.
- Compatibility: checked combinations of harnesses, models, skills, tools and environments.
- Quality: configuration performance on specific kinds of work.
- Operation: time, resource usage, failures and recovery in real work.

Retain check conditions, configurations and results for reproduction and comparison. Keep failed runs visible and link assessments to their supporting runs.

Updating a recommendation preserves earlier runs and assessments. New work reads current recommendations when choosing a method.

Operators use these records to manage work and accept results; architects use them to compare configurations and identify improvements.

Observation confidence is separate from execution state. Lost contact means current execution is unconfirmed, not failed or cancelled; show the last known state and the uncertainty.

## System requirements

- Reliability: preserve accepted work and results. After failure, check the original operation's state and outcome before resuming, safely retrying or requesting a decision. An uncertain external action outcome does not authorize repeating it.
- Authority: explicitly grant access to projects, data, tools and environments. Instructions define behavior but do not grant permissions.
- Resources: account for available capacity, occupancy, provider quotas and configured budgets. Allocate work automatically and explain resource waits. Operators control priorities and limits and may explicitly select agents or environments. A separate scheduling optimizer is not required.

Routine work advances automatically. People resolve substantive decisions and exceptions beyond the system's authority; project acceptance rules still apply.

## Linked detail

- [Use cases](docs/use-cases.md): end-to-end scenarios connecting the system blocks.
