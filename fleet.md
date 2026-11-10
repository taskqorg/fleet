# Fleet

A system for performing work through agents across projects. This specification defines the target architecture, not implementation status.

## Principles

- **R1 Shared rules.** [specq](https://github.com/taskqorg/specq) governs engineering, design and documentation.
- **R2 Shared core.** Every system operation is a tool over one core state; a data layer serves queries. Human interfaces, pipelines and agents use the same tools and data, each within its own permissions. Interfaces add no operations of their own.
- **R3 System requirements.** [Reliability, authority and resources](#system-requirements) apply to every block.
- **R4 Design depth.** Agree on each level's parts, responsibilities and relationships before detailing any part. Revisit earlier levels when needed. Existing product implementations and contracts are evidence to assess, not a template for Fleet's structure.
- **R5 Observability.** Instrument every system component and operation, including agents, tools and infrastructure. Retain queryable history for diagnosis, resource attribution and improvement under [observation and evaluation](#observation-and-evaluation).
- **R6 Minimal mechanisms.** Agents act through supplied instructions, context and tools, invoking pipelines when useful. Fleet provides execution control, data access and observable history for improvement. Structure only what it must coordinate and recover; domain workflows and methodologies remain configurable, not mandatory system stages.

## Operator interface

Deliver the operator and architect interfaces on the web first, across all projects. Later desktop applications for Windows, macOS and Linux reuse the web UI and shared operations. Follow the [delivery order](docs/designs/fleet-stack.md#delivery-order-and-change-cycles).

Conversation with a product manager (PM) is the primary way to discuss and assign work. A PM is an ordinary session in a chosen agent with role-specific instructions, tools and project access. Sessions may span related projects; they are not permanent project channels. Conversation has no separate request object.

- Projects and tasks: create and configure projects, describe work and set priorities.
- Execution: balance work and track progress.
- Decisions: answer questions and resolve blockers.
- Results: inspect what was done, where to find it, what was checked and what remains; accept work under project rules.

## Architect interface

A web interface for a separate architect role. Configure and qualify the agents and facilities available to operators.

- Agents: manage configurations, harnesses and models.
- Skills and tools: manage available capabilities and their assignment to agents.
- Environments: prepare execution conditions.
- Resources: manage capacity, provider quotas and spending limits.
- Evaluation: test compatibility, benchmark configurations, rerun checks after changes and analyze retained history to improve projects, pipelines, skills, tools and infrastructure.
- Access: grant projects shared capabilities and connections, with quotas.

The [architect workflow](docs/interfaces.md#architect-workflow) connects configuration, experiments, assessment and scoped recommendations.

Retain assessments as knowledge. Changing a recommendation is a separate architect action; comments and conclusions do not change it automatically.

## Shared core

### Work management

A project groups the goal, context, rules and work for a product or area. Follow the [project context](docs/configuration.md#project-context) baseline for files, materials and reusable methods.

Configure these areas as needed, using shared defaults:

- Purpose and context: specifications and materials.
- Connections: bindings of granted [connections](#tools-and-pipelines) to concrete repositories, task board, data sources, CI and external services.
- Workflow: project instructions, checks, review and completion conditions.
- Execution: references to agent configurations, environments, tools and catalogue recommendations.
- Delivery: result storage, merge, publication and deployment.

A project may use several repositories or contain no code. Fleet owns task state and work history; issues and boards are optional integrations under [state ownership](docs/state-ownership.md). Inherit shared defaults and store project differences. Selecting a capability does not grant access to it.

Own work from task creation to an accepted result:

- Task: identity, project, expected outcome, dependencies, completion requirements and result destination.
- Execution: agent and environment assignment, state and execution history.
- Questions: retain context with the related work, route to an authorized person or agent and deliver the answer to the relevant execution. Show the addressee and waiting reason; follow [communication rules](docs/system-operations.md#agent-communication).
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

Run and control agents through their harnesses under the [runtime boundary](docs/runtime-integration.md). Preserve supported harness capabilities, including images, history and session controls, as harnesses evolve.

An agent configuration combines a harness, model and settings, instructions, skills, tools and environment requirements. Fleet prepares the harness configuration explicitly; personal host instructions, memory, tools, hooks and settings are not inherited.

A role defines an agent's purpose through instructions and tools, not a hard-coded agent type. Provide standard roles, including PM, worker and reviewer; projects may adapt them or add their own.

An execution binds a configuration version and role to inputs, an environment, permissions, a session and a task when applicable.

Resolve defaults from the selected agent and role, project settings, then explicit task choices. Follow [configuration rules](docs/configuration.md) for requirements, authority, observations and effective settings. Task choices override defaults, not mandatory project requirements; conflicting requirements need resolution.

Before launch, check environment compatibility and granted permissions. Resolve instructions, skills and configuration at execution start under the [source rules](#knowledge-and-rules); later repository edits affect only new executions. A run may pin one revision for all its steps; by default each step resolves current versions.

A session holds conversation and working context within a harness: messages, actions and interaction history. It may exist without a task.

An execution may start or continue a session according to harness capabilities. Task, execution and session lifecycles remain distinct. [Continuation and handoff](docs/execution-continuity.md#continuation-and-handoff) preserve work when accounts, agents or environments change.

Execution states are preparing, running, waiting, succeeded, failed and cancelled. Available controls depend on the harness or tool.

### Environments

An environment provides:

- Compute: machine, OS and available resources.
- Software: installed programs, dependencies and capabilities.
- Workspace: project files, checkout or worktree, inputs and results.
- Interaction: terminal, browser, APIs and desktop control through computer use (CUA).
- Access: available data, accounts and external services.

Manage its lifecycle: prepare, check readiness, make available for work, preserve or release. Track [working area ownership and cleanup](docs/execution-continuity.md#working-areas); preserve required results and history before release. A task completes only when its result reaches its destination.

Agents specify environment requirements; [work allocation](docs/work-allocation.md) selects eligible configurations and local or remote environments across registered hosts. Support the [distributed deployment](docs/deployment.md), including intermittently connected computers. An agent configuration need not bind to one machine.

Agents primarily work in dedicated environments. An environment may serve multiple tasks; isolate working files and concurrent actions where conflicts are possible. A separate machine per task is not required. People can connect to observe or intervene, including through a desktop when available.

A personal desktop may be explicitly connected as an environment; an agent can request access. Shared desktop work with a person is not a primary scenario.

### Tools and pipelines

Distinguish [capability, method and tool](docs/configuration.md#capabilities-methods-and-tools): the expected outcome, its implementation and its callable interface. A pipeline is a system-executed process implementing a method, not a list of instructions for its caller to execute.

- Catalogue: describe tools, methods and pipelines, their purpose, inputs, results, usage conditions and current recommendations.
- Connections: provide access to harness tools, MCP, external APIs, system services and Fleet's own operations and data. Credentials stay in connections, tools and environments, outside versioned files; agents receive capability through tools, never credentials.
- Pipelines: structured text definitions compose tools, programs and agents through steps, dependencies, executors, inputs and outputs. Keep definitions, step instructions and run state separate; the system coordinates steps and result transfer. A pipeline may be exposed as an agent tool: the caller uses its description and results, not its steps.
- Execution and results: start operations, observe their state and retrieve results. Pipeline views combine the definition with run state; they do not maintain a separate workflow definition.

Architects manage the catalogue, pipelines and [connections](docs/connections.md); a connection holds service access, accounts, credentials and limits. Operators bind granted connections to project resources. Missing access is a question: binding changes address operators, new grants and connections address architects.

Independent steps run concurrently within resource limits. A failed step blocks its dependants; others finish before the run fails. Reruns use current definitions and repeat failed or changed steps and affected dependants. Reuse requires unchanged inputs, definitions and effective configuration, including instructions and skills; operators may force a full rerun.

Runs retain their caller and result route under [orchestration](docs/orchestration.md); the caller need not remain active. Recovery follows permissions and project rules; needed decisions become questions. Definitions may express branches and bounded review/rework loops. Laboratory and project runs use one run mechanism.

A definition declares run parameters, transitions and review iteration limits; each step names its executor, inputs, outputs, environment requirements and instruction path where applicable. Executors may be agents, tools, programs or pipelines. Operational retry policies, timeouts and credentials remain in project rules, resources and connections.

Humans and agents use one catalogue; agent configurations select available capabilities. Human views support inspection and discussion with a PM. Agents discover applicable descriptions, recommendations and instructions on demand, without browsing the human interface or loading the whole catalogue into context.

Honor explicit tool, method or pipeline choices made when planning work with the PM. Otherwise, agents choose using current project recommendations, falling back to system recommendations where the project defines none.

Provide agent operations to find and launch agents, select and use environments, create and configure projects, create and track tasks, run pipelines, retrieve results and query Fleet state within granted permissions.

### Knowledge and rules

- Specifications: define the product, accepted decisions and result requirements.
- Instructions: define agent behavior through shared rules, roles and project overrides.
- Skills: provide reusable methods for kinds of work.
- Context: supply task materials, related decisions, results and project information.

Skills, instructions, agent configurations and pipeline definitions are canonical Git repository files. The catalogue references them rather than storing editable copies.
Use current versions unless explicitly selected otherwise. Record repository, revision and paths used. An explicitly selected draft need not be committed; retain its actual content and difference from the base. Referenced revisions need no copy while accessible. Missing sources remain visible; recovery does not rewrite execution history.

Provide applicable knowledge and rules with their sources. Each execution exposes its effective instructions and project overrides.

Each rule or decision has one canonical source; other locations reference it. Show conflicts that the override order does not resolve.

Keep built-in workflow requirements minimal. Provide standard behavior through default instructions and a mechanism for project overrides.

Project rules may override PM communication, task formulation, execution practices, when to ask for decisions, required results, result presentation and storage, and review, merge or publication policy.

PMs apply the project's effective rules when preparing tasks. Executing agents apply them when doing the work and delivering results.

### Observation and evaluation

- Live execution: agents, locations, progress, waits and problems.
- History and evidence: retain every execution in full, without default expiry: session history, agent output, actions, tool calls, questions, results and verification materials. This is the source for analysis and pipeline improvement.
- Changes: operators and architects can inspect diffs between skill and agent configuration revisions using existing version-control and diff tools.
- Compatibility: checked combinations of harnesses, models, skills, tools and environments, stored as versioned facts. Confirmed incompatibility blocks launch; incomplete or unchecked results are visible, not treated as incompatibility.
- Quality: compare agents, methods and methodologies through [experiments](docs/designs/fleet-lab.md), including numeric evaluations and budgeted iterative improvement at step, task or project scope.
- Operation: retain invocation counts, duration, cost, CPU, memory, disk usage, failures and recovery across agents, tools, Fleet components and infrastructure, including preparation and cleanup. Link records to components, tools, working areas and runs where applicable; expose missing observations.

Three records: a run with its conditions and results; an assessment of runs with criteria, comparison, limitations and suitability, appended and never rewritten; a recommendation for a catalogue kind of work within a project or system scope, linked to its assessment. Comments and tags are fields of runs and assessments. Keep failed runs visible.

Architects and assisting agents write assessments. Recommendation changes require architect authority, which may be delegated to an agent; otherwise the agent proposes the change as a question. Updating a recommendation preserves earlier runs and assessments; new work reads current recommendations when choosing a method.

Agents query recommendations by kind of work and project with scope fallback and usage conditions resolved; assessments remain reachable. Humans and agents use [work analysis](docs/designs/fleet-lab.md#work-analysis) to inspect the full work cycle, attribute resources and identify improvements to rules, tools and configurations.

Observation confidence is separate from execution state. Lost contact means current execution is unconfirmed, not failed or cancelled; show the last known state and the uncertainty.

## System requirements

- Reliability: preserve accepted work and results. After failure, check the original operation's state and outcome before resuming, safely retrying or requesting a decision. An uncertain external action outcome does not authorize repeating it.
- Authority: architects grant projects shared capabilities and connections; operators work within the grant. An execution's permissions are the intersection of the project grant, the agent configuration and explicit task choices. Instructions define behavior but do not grant permissions. Operator and architect are roles; one person may hold both.
- History access: execution history is project data. Operators read history within their project grants; architects can read all execution history for evaluation.
- Resources: account for CPU, memory, disk, provider quotas and budgets across concurrent work, including laboratory runs and preparation. [Balance work](docs/work-allocation.md) across eligible hosts and agents; expose selection and waiting reasons. Operators steer placement, harness use and concurrency through interfaces or authorized agents.

Routine work advances automatically. People resolve substantive decisions and exceptions beyond the system's authority; project acceptance rules still apply.

## Stack

Fleet's services move to Go, one language for all of them: the coordinator first, then the executor and the gateway together; the executor probe showed it builds without a C compiler (pull request #115).

The browser app stays TypeScript. Reason: one build from any machine for every platform, at equal speed for Fleet's workload; evidence, module table and migration order are in the [stack specification](docs/designs/fleet-stack.md).

Go rules:

- Standard library first: `net/http` (routing, SSE), `encoding/json`, `log/slog`. Each third-party module has one row in the [module table](docs/designs/fleet-stack.md#third-party-go-modules): module, what it replaces, why the standard library does not cover it.
- Allowed now: `github.com/jackc/pgx/v5`, used directly, not through `database/sql`, because LISTEN and connection options need its own API. ClickHouse through its HTTP interface with `net/http`. S3 (Garage) through a hand-written SigV4 signer; `minio-go` would add 19 compiled-in modules.
- Allowed for the executor: `modernc.org/sqlite` (SQLite without cgo, keeps `journal.sqlite`) and `github.com/ebitengine/purego` (macOS libproc calls without cgo).
- Not built: a Linux run of the executor probe's signal and crash checks in CI; it precedes the executor port.
- `CGO_ENABLED=0` for every binary, checked in CI. Any need for C is a stop-and-ask.
- One Go module (`go/`), one `go.sum`; builds use `-mod=readonly`; the default checksum database stays on; `govulncheck` runs in CI as a warning.
- Contract types are generated at build from `contracts/*.schema.json` by `go/gen`; generated files are not committed.
- Release binaries are built with `-trimpath` in one Linux CI job for linux/amd64, darwin/arm64 and windows/amd64, each with a SHA-256 file; no macOS runner for Go code.
- Tests are outside tests over HTTP against a real Postgres, as in `tests/trial/`; each names its requirement and risk.

## Verification

The merge-gate command is `pnpm install --frozen-lockfile && pnpm run build && cargo build -p fleet-executor && cargo test -p fleet-executor && pnpm test`.

The full gate runs in CI on the pull request, never on the owner’s Mac: several agents work there at once, and each full run takes about 1.5 cores and 1 GB per test file. Locally run only the build of what you changed and the test files that cover it (`node <test file>`); push and read the CI result for everything else.

The full gate has a diagnostic elapsed-time budget of 180 s on an idle Mac (arm64), including setup, build and tests. Exceeding it calls for diagnosis; it is never a timeout or a skip rule.

Each persistent test names the requirement and the risk it protects; follow [specq Testing](https://github.com/taskqorg/specq/blob/13772b779be68103d0581d61a5f8fe7af2b92dd1/engineering/testing.md). CI reports missing names as warnings.

## Linked detail

- [Use cases](docs/use-cases.md): end-to-end scenarios connecting the system blocks.
- [Execution continuity](docs/execution-continuity.md): working area lifecycle, resource visibility and handoff.
- [System operations](docs/system-operations.md): operation groups, contracts and agent communication semantics.
- [Experiments](docs/designs/fleet-lab.md): reproduce recorded work, compare changes and apply improvements to products.
- [Interface workflows](docs/interfaces.md): operator and architect activities and contextual actions.
- [Capabilities and pipelines](docs/configuration.md#capabilities-methods-and-tools): callable outcomes, alternative methods and system-executed workflows.

The connected delivery prototype uses repository project context, shared catalogue/search operations, durable operator decisions, managed account metadata and tracked native/program workers; [the slice contract](docs/project-pipeline-prototype.md) defines its supported subset. Source integration, task completion and publication remain separate observations.
