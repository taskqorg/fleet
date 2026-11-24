# Fleet

A system for performing work through agents across projects. This specification defines the target architecture, not implementation status.

## Principles

- **R1 Shared rules.** [specq](https://github.com/taskqorg/specq) governs engineering, design and documentation. [Fleet document governance](docs/designs/fleet-docs-governance.md) defines the project paths and document kinds.
- **R2 Shared core.** Every system operation is a tool over one core state; a data layer serves queries. Human interfaces, pipelines and agents use the same tools and data, each within its own permissions. Interfaces add no operations of their own.
- **R3 System requirements.** [Reliability, authority and resources](#system-requirements) apply to every block.
- **R4 Design depth.** Agree on each level's parts, responsibilities and relationships before detailing any part. Revisit earlier levels when needed. Existing product implementations and contracts are evidence to assess, not a template for Fleet's structure.
- **R5 Observability.** Instrument every system component and operation, including agents, tools and infrastructure. Retain queryable history for diagnosis, resource attribution and improvement under [observation and evaluation](#observation-and-evaluation).
- **R6 Minimal mechanisms.** Agents act through supplied instructions, context and tools, invoking pipelines when useful. Fleet provides execution control, data access and observable history for improvement. Structure only what it must coordinate and recover; domain workflows and methodologies remain configurable, not mandatory system stages.
- **R7 Agentic first.** Every new capability reaches agents first, as tools, data and queries over the shared core (R2). Its human interface follows, designed for convenience. When an agent path and a UI path compete, build the agent path first.

## Product purpose

Fleet exists to run, evaluate and compare harnesses, models and coordination methods in Lab, and turn proven ones into controlled, observable project capabilities across four connected stages:

- **Fast Lab evaluation:** benchmark models, harnesses, prompts and pipelines on curated use cases, real work cases and code benchmarks to establish repeatable quality, latency and cost evidence.
- **Proven method catalogue:** register workflows and methods with explicit lifecycle statuses (`experimental`, `recommended`, `deprecated`), usage conditions and project or system defaults.
- **Project and task accounting:** execute and track real tasks across connected projects, retaining full execution history, costs and metrics to diagnose problems and convert recurring failures into new Lab cases.
- **Controlled agent tools:** expose configured pipelines and recommended methods as agent-callable tools over the shared core so agents and operators run proven methods under Fleet authority and accounting.

## Operator interface

Deliver the operator and architect interfaces on the web first, across all projects. Later desktop applications for Windows, macOS and Linux reuse the web UI and shared operations. Follow the [delivery order](docs/designs/fleet-stack.md#delivery-order-and-change-cycles).

The main sidebar includes Bots under Work for every signed-in person and Humans under System for the owner. See [information architecture](docs/designs/fleet-ia-and-journeys.md#1-closed-information-architecture) for the full section map.

Chromium must be able to install each built workspace with its Fleet name and shared Fleet icon, launch its start URL standalone, and keep navigation between built workspaces in the installed window (manifest scope `/`). The page advertises the current workspace's manifest on direct entry and after in-app navigation, including browser Back and Forward.

Manifest requests include the existing sign-in credentials through Cloudflare Access; installation does not grant access or cache authenticated API data.

Conversation with a product manager (PM) is the primary way to discuss and assign work. A PM is an ordinary session in a chosen agent with role-specific instructions, tools and project access. Sessions may span related projects; they are not permanent project channels. Conversation has no separate request object.

- Projects and tasks: create and configure projects, describe work and set priorities.
- Execution: balance work and track progress.
- Spend: show cumulative spend on each Tasks, Conversations and Projects list row, linking to its detail breakdown.
- Decisions: answer questions and resolve blockers.
- Results: inspect what was done, where to find it, what was checked and what remains; accept work under project rules.

List spend reads one batch per page from current stored execution valuations, preserving visible attribution, separate currencies and bases, and unknown or partial coverage, and updates quietly in place while retaining the operator's list and last confirmed values.

Server-backed lists show progress when first opened or when an operator changes or retries the query. Live updates keep the current list interactive and apply confirmed changes in place. If the server cannot confirm the current list, show its freshness or failure and a recovery path. [List view behaviour](docs/design-system/patterns/list-view.md#loading-and-live-updates) and [read policy](docs/designs/fleet-data-foundation.md#live-read-policy) define the presentation and data paths.

## Architect interface

A web interface for a separate architect role. Configure and qualify the agents and facilities available to operators.

- Agents: manage configurations, harnesses and models.
- Skills and tools: manage available capabilities and their assignment to agents.
- Environments: prepare execution conditions.
- Resources: manage capacity, provider quotas and spending limits.
- Evaluation: test compatibility, benchmark configurations, rerun checks after changes and analyze retained history to improve projects, pipelines, skills, tools and infrastructure.
- Access: grant projects shared capabilities and connections, with quotas.

The [architect workflow](docs/designs/fleet-operator-app.md#architect-workflow) connects configuration, experiments, assessment and scoped recommendations.

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

Task acceptance selects a specific eligible result. Automatic acceptance for a task-bound agent run requires a confirmed successful execution and a successful work report; an explicit acceptance request can also offer a confirmed successful execution whose work report was not filed, unless incomplete work was reported.

Required artifacts must be stored for that execution and the acceptance identifies the retained artifacts it offers. Active or queued continuation supersedes a pending acceptance. An operator may explicitly select an eligible earlier result after work ends, or supply an external result for a task without a Fleet execution.

An operator may reject an open offer after its result loses eligibility. The rejection applies to the offered result. Missing completion evidence leaves the task open without a generated question. A failed execution or explicit report of incomplete work can require an operator decision.

Task details show an informational note when the latest task-bound agent run ended with confirmed success and has no completion report. The note clears when work continues, an explicit offer covers it, or valid completion evidence is available. It leaves task state and decisions independent.

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

A question wait requires an open blocking question. When an execution is abandoned, withdraw the agent questions that depended on that execution and settle the task against its remaining work and questions. An unconfirmed active execution retains its last observed state and shows the missing confirmation.

Show review, merge and deployment as operations or stages alongside task state. Merge, publication and deployment use project connections; track their state and results even without an active agent. Running CI or deployment counts as work.

Projects define operations and transition conditions; shared state meanings do not change with the project or its providers.

After execution failure, retain the error and follow project rules: continue automatically where safe and authorized, wait for a required decision, or mark the task failed when work ends without its result.

Pause and cancellation prevent new steps. Show running operations and whether they can be stopped; cancellation requests their stop. Neither action proves immediate execution termination.

A host command issued before cancellation may already be in flight. Withhold further delivery of work for that execution, request stop on its owning host, and keep its last known state until execution evidence arrives.

Neither pause nor cancellation rolls back completed external effects. Rollback requires a separate decision.

#### Project publishing

Fleet models repository change publication through one shared **Publishing** contract (`project-publishing` / `publishing_*`) across GitHub pull requests and GitLab merge requests, including self-hosted GitLab.

- **Distinct lifecycle stages:** separate `publish draft` (push branch and open or reuse draft PR/MR), `code review` (verify exact-SHA review evidence), `ready`, `merge` (request or cancel provider merge), `release` and `host landing`.
- **Distinct boundaries:** keep ordinary Git access (deploy keys), read-only provider facts (PR/MR, checks, releases) and publication support (`project-publishing` authority) separate.
- **Authority and secret isolation:** publication authority is the intersection of host capability, project repo/profile grant and active execution branch; credentials stay inside host mediators and never reach harnesses or tool results.
- **Failed-job logs:** Both publishing MCP servers expose the read-only `ci_logs` tool. It takes a provider job ID and the exact SHA of the granted execution branch's live head. The provider job must belong to the bound repository and report that SHA and a failed conclusion.
  Return at most the first 16 KiB of log text as a redacted excerpt with a truncation flag. Remove exact values of the provider credential used by the reader, including partial matches cut by the read boundary. Provider errors return no raw response or credentials.
- **Policy and fencing:** mutations carry `requestId`, preserve `sourceExecutionId` provenance, fence against moved heads via `expectedHeadSha`, and enforce project review, CI and merge policy without admin bypass.
- **Rebased task branches:** `project-forge publish_draft` defaults to a regular push. An explicit force-with-lease names the expected remote commit and may update only the active session's granted `fleet/*` branch. The base branch and `main` are excluded.

### Agent execution

Run and control agents through their harnesses under the [runtime boundary](docs/runtime-integration.md). Preserve supported harness capabilities, including images, history and session controls, as harnesses evolve.

An agent configuration combines a harness, model and settings, instructions, skills, tools and environment requirements. Fleet prepares the harness configuration explicitly; personal host instructions, memory, tools, hooks and settings are not inherited.

A role defines an agent's purpose through instructions and tools, not a hard-coded agent type. Provide standard roles, including PM, worker and reviewer; projects may adapt them or add their own.

An execution binds a configuration version and role to inputs, an environment, permissions, a session and a task when applicable.

Resolve defaults from the selected agent and role, project settings, then explicit task choices. Follow [configuration rules](docs/configuration.md) for requirements, authority, observations and effective settings. Task choices override defaults, not mandatory project requirements; conflicting requirements need resolution.

Before launch, check environment compatibility and granted permissions. Resolve instructions, skills and configuration at execution start under the [source rules](#knowledge-and-rules); later repository edits affect only new executions. A run may pin one revision for all its steps; by default each step resolves current versions.

A session holds conversation and working context within a harness: messages, actions and interaction history. It may exist without a task.

An execution may start or continue a session according to harness capabilities. Task, execution and session lifecycles remain distinct. [Continuation and handoff](docs/execution-continuity.md#continuation-and-handoff) preserve work when accounts, agents or environments change.

Conversations exposes a [manual fresh-session boundary](docs/execution-continuity.md#fresh-session-example) with a retained routing decision, handoff packet and execution lineage.

Execution states are preparing, running, waiting, succeeded, failed and cancelled. Available controls depend on the harness or tool.

Conversation status is the state of its latest visible agent run by execution sequence. A conversation with no visible agent run is idle. Detail, list rows, membership, status filters, group and navigation counts, entity cards and search use this rule under their existing visibility scope. Tool calls remain in conversation history and resource accounting and do not determine conversation status.

**Universal Code Mode** ([design](docs/designs/fleet-code-mode.md)): every supported harness (`codex`, `claude`, `antigravity`, `acp`) runs JavaScript tool orchestration through one `code`/`poll` MCP runtime over granted Fleet tools, connections and host functions within execution authority, keeping intermediate results out of model context.

#### Persistent bots

A bot is a saved operator with a director, allowed projects, a Fleet profile, versioned instructions and SOUL, and durable project-scoped memory. Bot settings and memory grant no new authority. Use the profile and current project grants for placement, runtime, native tools and Code Mode. Pin the settings revision, memory revision and execution project for each turn.

Give each bot one stable Home in Conversations. Use the standard input queue, sender provenance, transcript, questions and execution links. Keep managed tasks in Fleet with their own histories.

Continue the native session when project, workspace, host, profile and account are compatible. Otherwise use the retained handoff with context scoped to the execution project. Recheck authority before launching queued work and before returning historical context or usage.

Run now, scheduled wakes and new Needs you entries use the same execution flow. Retain the cause and deduplication identity of each wake and admit one active bot run at a time. Persist the schedule and wake state across coordinator restarts.

Pause blocks future wakes. Stop separately requests cancellation through the execution lifecycle and reports the observed outcome. Keep quiet wakes and skipped, queued, failed and unconfirmed work inspectable in Runs.

Show active, waiting and historical managed tasks, bot runs, triggers, questions and errors in Web. Reuse Fleet analytics for all-time and period totals by bot and recorded execution project, including wall time, active time, tokens and costs with source execution links.

Bots → Automations manages independently named, versioned manual, daily and Needs you triggers. Each automation supplies its own instructions, runs in the bot's granted project, and inherits its Fleet profile and tools under current grants.

The event filter selects questions, execution problems or both in that project. Add trigger types through the shared operation contract and wake admission. The creation catalog lists only supported types.

Daily schedules use an explicit IANA time zone and fire at most once per local date. Recovery catches up today's eligible slot and skips earlier dates and nonexistent local times.

A retained event cursor and per-automation deduplication identities prevent repeat delivery. An automation starts watching at enable time. Stable event identities survive edits, pauses and restarts.

If a bot is busy, daily and manual wake attempts record a skip. Eligible Needs you work waits for the bot to become free. Pausing an automation suppresses future automatic wakes. Run now is an explicit request. Stop uses the existing execution control.

Run history retains the automation revision, cause, outcome and execution link. Existing bot settings schedules and event wakes keep their saved behavior.

Capture direct and delegated attribution when work is created. Count each execution once and preserve its attribution across reassignment and continuation. Retain cost basis, currency and unknown observations.

Refresh public standard API-equivalent model prices through the scoped, audited [provider pricing workflow](docs/designs/fleet-analytics.md#provider-pricing-refresh). Preserve historical tariff entries and distinguish subscription equivalents from billed amounts.

The first Fleet operations pilot uses the display name HAL0 (digit zero). Fleet bot display names remain configurable. HAL0 wakes on new Needs you entries and a schedule. It checks tasks, executions, telemetry and rendered Fleet Web.

HAL0 uses Fleet questions for genuine owner decisions and continues or creates authorized, deduplicated Fleet fixes for confirmed technical causes. It verifies those fixes on the affected surface, retains unconfirmed observations, and uses audited dismissal for proved obsolete or duplicate entries.

Qualify those cases in isolation before any bounded live pilot and coordinate that pilot with the existing queue operator.

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
- An agent-started pipeline retains the calling execution as lineage and the person whose grants authorize the call as its initiating actor, including when dispatch took the work onto another person's subscription.
- An agent step can return a review in its final reply. Fleet validates and retains the review outputs for downstream steps, so the reviewer can work with read-only tools.
- Pipeline history returns oldest-first pages. Request 1–100 runs (default 25) and pass the returned `nextCursor` to continue. A page may end early to keep the MCP reply below 16 MiB.
  Oversized history entries and `pipeline_run` replies carry `truncated: true`, run identity, state, a plan summary and the canonical result artifact reference when available. Read the full stored run through the HTTP `pipeline_get` operation.
- Conditional activation: ordered static v2 and conditional v3 definitions use one plan runtime. V3 conditions compare one earlier verified Boolean or finite enum result; invalid or unavailable evidence blocks progression. A declared step has an activation entry; an execution exists only after selection.
  Nonselection creates no call, output, duration or cost. V3 binds one last unconditional script finalizer result and verified input lineage.
- Pipeline persistence uses one current plan format; unsupported old run formats require a clean start rather than a compatibility runner or inferred backfill. Selection, cancellation and handoff retain a durable identity and current authority. An unconfirmed external effect never authorizes automatic replay or method substitution.

Architects manage the catalogue, pipelines and [connections](docs/connections.md); a connection holds service access, accounts, credentials and limits. Operators bind granted connections to project resources. Missing access is a question: binding changes address operators, new grants and connections address architects.

Independent steps run concurrently within resource limits. A failed step blocks its dependants; others finish before the run fails. Reruns use current definitions and repeat failed or changed steps and affected dependants. Reuse requires unchanged inputs, definitions and effective configuration, including instructions and skills; operators may force a full rerun.

Runs retain their caller and result route under [orchestration](docs/orchestration.md); the caller need not remain active. Recovery follows permissions and project rules; needed decisions become questions. Definitions may express branches and bounded review/rework loops. Laboratory and project runs use one run mechanism.

An explicitly requested [Lab local-suffix retry](docs/designs/fleet-lab.md#local-suffix-retry) creates a new attempt from a confirmed unchanged prefix under ordinary project grants; the unknown original remains history.

A definition declares run parameters, transitions and review iteration limits; each step names its executor, inputs, outputs, environment requirements and instruction path where applicable. Executors may be agents, tools, programs or pipelines. Operational retry policies, timeouts and credentials remain in project rules, resources and connections.

Humans and agents use one catalogue; agent configurations select available capabilities. Human views support inspection and discussion with a PM. Agents discover applicable descriptions, recommendations and instructions on demand, without browsing the human interface or loading the whole catalogue into context.

Honor explicit tool, method or pipeline choices made when planning work with the PM. Otherwise, agents choose using current project recommendations, falling back to system recommendations where the project defines none.

Provide agent operations to find and launch agents, select and use environments, create and configure projects, create and track tasks, run pipelines, retrieve results and query Fleet state within granted permissions.

**Universal Swarm coordination** ([design](docs/designs/fleet-swarm.md)): across any homogeneous or mixed set of Fleet harnesses (`codex`, `claude`, `antigravity`, `acp`), a coordinator decomposes a goal into structured threads and disjoint worker tasks using configurable strategies (sizing, verifier ratio, stall monitoring, `inherit`/`share`/`branch` workspaces, and per-worker tools, connections, roles and instructions).

Workers coordinate over a durable shared message board (`@mention`, `@all`, channels, event-driven wake without polling) with structured progress, blocker, `[done]` and screenshot reports, surfaced visually in Fleet's web app and evaluated in Lab alongside Code Mode.

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
- [Humans administration](docs/designs/fleet-people-access.md#humans-administration) is owner-only. Personal Lab is independently grantable, and promotion to shared Fleet requires architect authority.
- Authority: architects grant projects shared capabilities and connections; operators work within the grant. An execution's permissions are the intersection of the project grant, the agent configuration and explicit task choices. Instructions define behavior but do not grant permissions. Operator and architect are roles; one person may hold both.
- History access: execution history is project data. Operators read history within their project grants; architects can read all execution history for evaluation.
- Resources: account for CPU, memory, disk, provider quotas and budgets across concurrent work, including laboratory runs and preparation. [Balance work](docs/work-allocation.md) across eligible hosts and agents; expose selection and waiting reasons. Operators steer placement, harness use and concurrency through interfaces or authorized agents.

Routine work advances automatically. People resolve substantive decisions and exceptions beyond the system's authority; project acceptance rules still apply.

## Stack

Fleet's services move to Go, one language for all of them: the coordinator first, then the executor and the gateway together; the executor probe showed it builds without a C compiler (pull request #115).

The browser app stays TypeScript. Reason: one build from any machine for every platform, at equal speed for Fleet's workload; evidence and migration order are in the [stack specification](docs/designs/fleet-stack.md), the module table in [Go code](docs/designs/fleet-go-code.md#third-party-go-modules).

Go rules:

- Standard library first: `net/http` (routing, SSE), `encoding/json`, `log/slog`. Each third-party module has one row in the [module table](docs/designs/fleet-go-code.md#third-party-go-modules): module, what it replaces, why the standard library does not cover it.
- Allowed now: `github.com/jackc/pgx/v5`, used directly, not through `database/sql`, because LISTEN and connection options need its own API. ClickHouse through its HTTP interface with `net/http`. S3 (Garage) through a hand-written SigV4 signer; `minio-go` would add 19 compiled-in modules.
- Allowed for the executor: `modernc.org/sqlite` (SQLite without cgo, keeps `journal.sqlite`) and `github.com/ebitengine/purego` (macOS libproc calls without cgo).
- Allowed for error reporting: `github.com/getsentry/sentry-go`, the Sentry protocol of Bugsink, Fleet's error tracker ([error tracking](docs/designs/fleet-error-tracking.md)).
- Not built: a Linux run of the executor probe's signal and crash checks in CI; it precedes the executor port.
- `CGO_ENABLED=0` for every binary, checked in CI. Any need for C is a stop-and-ask.
- One Go module (`go/`), one `go.sum`; builds use `-mod=readonly`; the default checksum database stays on; `govulncheck` runs in CI as a warning.
- Contract types are generated from `contracts/*.schema.json` by `go/gen`; the Go files (`go/contract/*_gen.go`) are committed and CI fails when `go generate` changes them. A conflict in a generated file is resolved by running `go generate`, never by hand.
- Release binaries are built with `-trimpath` in one Linux CI job, each with a SHA-256 file: the coordinator for linux/amd64, darwin/arm64 and windows/amd64; the executor and the gateway for linux/amd64 and darwin/arm64. No macOS runner.
- Tests are outside tests over HTTP against a real Postgres (`tests/trial/`, `tests/go/`, the app checks in `tests/ui/`, all through `tests/harness.ts`); each names its requirement and risk.

## Verification

Pull requests reach `main` only through the GitHub merge queue. A worker runs `/code-review` before it opens its pull request and enables auto-merge on it after the `code-review` status; the orchestrator can take any pull request out of the queue. Branches are rebased on `main`, never merged with it, and are not stacked ([delivery pipeline](docs/designs/fleet-delivery-pipeline.md#8-workers-merge-and-watch-their-own-pull-requests)).

The full fallback merge-gate command is `pnpm install --frozen-lockfile && pnpm run build && (cd go && go generate ./... && test -z "$(git status --porcelain)" && go vet ./... && FLEET_REQUIRE_PG=1 FLEET_PG_FIXTURES=outside CGO_ENABLED=1 go test -race ./...) && pnpm test`. CI splits it into the jobs `go`, `app` and `browser` and builds the outside-test coordinator with `-race`.

`FLEET_REQUIRE_PG=1` fails a PostgreSQL Go test that would skip; `FLEET_PG_FIXTURES=outside` leaves the Go halves of outside tests to `pnpm test` ([coordinator verification](go/coordinator/README.md#verification)).

`pnpm run build` runs `app:build` (typecheck and bundle) and `go:build` (the four Go binaries in `go/bin/`). `pnpm test` runs the app unit tests and `tests/go/run.sh`: the binary on a fresh database of a local PostgreSQL server (server programs on `PATH`, `tests/harness.ts`), with `tests/trial` and `tests/go` over HTTP.

CI records an exact-candidate impact plan from `scripts/test-selection.ts`. The core runs contract lint, test metadata/self-checks, selector fixtures and documentation checks; candidates that execute app tests also run the pure view-URL check. Required `go`, `app` and `browser` statuses validate the plan and actual results before release.

After successful exact-base CI, prose uses the core; app/src and packages/ui use app and browser checks; executor, gateway, updater, obs and Go test-only edits use go; the merge queue's release build keeps native archive builds, selftests and migration checks.

Non-test coordinator changes, shared Go/runtime/contracts/config, new tests, selector changes and unknown history, impact, paths or comparisons run the full fallback. Mixed known changes use their union. Nonempty `FLEET_TEST_FULL` restores full execution.

Pull requests execute their selected families directly before entering the queue. Merge-queue runs enable matrix fail-fast and abort sibling heavy jobs as soon as any required family fails.

[Selected-gate qualification](docs/designs/fleet-delivery-pipeline.md#selected-gate-qualification) defines the narrow rollout and rollback. Native archives, migrations, provenance and signing remain checked before publication.

The full gate runs in CI on the pull request, never on the owner’s Mac: several agents work there at once, and each full run takes about 1.5 cores and 1 GB per test file. Locally run only the build and the checks the task names, each set once ([owner directive](AGENTS.md)); push and read the CI result for everything else.

The full gate has a diagnostic elapsed-time budget of 180 s on an idle Mac (arm64), including setup, build and tests. Exceeding it calls for diagnosis; it is never a timeout or a skip rule.

Each persistent test names the requirement and the risk it protects; follow [specq Testing](https://github.com/taskqorg/specq/blob/1fc3716cc0971f8ca11f313d383db09751ffc132/engineering/testing.md).

A worker adds a test only when its task asks for one; otherwise at most one invariant check where a check is needed. This project rule takes precedence over the specq rule to keep a regression check for non-trivial logic. CI fails on an outside test in `tests/go/` that misses either name and reports other tests as warnings.

## Linked detail

- [Use cases](docs/use-cases.md): end-to-end scenarios connecting the system blocks.
- [Execution continuity](docs/execution-continuity.md): working area lifecycle, resource visibility and handoff.
- [System operations](docs/system-operations.md): operation groups, contracts and agent communication semantics.
- [Experiments](docs/designs/fleet-lab.md): reproduce recorded work, compare changes and apply improvements to products.
- [Interface workflows](docs/designs/fleet-operator-app.md#interface-workflows): operator and architect activities and contextual actions.
- [Capabilities and pipelines](docs/configuration.md#capabilities-methods-and-tools): callable outcomes, alternative methods and system-executed workflows.
- [Swarm coordination](docs/designs/fleet-swarm.md): universal cross-harness coordinator strategies, durable shared board, visual Swarm GUI and Lab evaluation.
- [Code mode](docs/designs/fleet-code-mode.md): cross-harness JavaScript tool orchestration (`code` and `poll`) over Fleet tools, connections and host functions.

The connected delivery prototype uses repository project context, shared catalogue/search operations, durable operator decisions, managed account metadata and tracked native/program workers; [the slice contract](docs/project-pipeline-prototype.md) defines its supported subset. Source integration, task completion and publication remain separate observations.
