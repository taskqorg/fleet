# Fleet use cases

Scenarios for the [system specification](../fleet.md). These describe accepted behavior, not runtime evidence.

Each use case has a stable anchor (`#uc1` … `#uc11`) that never changes when a title changes; IDs are never renumbered. The Lab registry points at these anchors and stores each section's hash; `pnpm exec tsx scripts/lab-drift.ts` reports sections that changed since they were indexed ([fleet-lab.md](designs/fleet-lab.md), Stage 1).

<a id="uc1"></a>

## UC1. Create a website page

Status: accepted. Source: this section; Lab registry entry `fleet:UC1` in [`config/lab/use-cases/fleet.json`](../config/lab/use-cases/fleet.json).

Use the [Hello World example](website-example.md) as the first representative design exercise, then progress to deployment, revisions, fixes and more complex pages. Its example exchanges are not a universal required workflow.

An operator wants a new product page in an existing project. Project context and rules are available; the operator opens an ordinary agent session with the PM role.

### Work flow

1. Operator and PM discuss purpose, content and the expected result. The PM reads project context and rules, then clarifies material gaps.
2. The PM creates a task with completion requirements and a result destination, preserving explicit tool or method choices.
3. The system checks dependencies, authority and resources, assigns an agent and environment, and supplies materials and applicable instructions.
4. The worker creates the page. Unspecified tool choices follow the [catalogue selection rules](../fleet.md#tools-and-pipelines).
5. The worker and any project-required reviewer check the page and retain evidence. Running the page, checking behavior and capturing images are example checks, not a universal checklist.
6. Apply the project's acceptance, merge and publication rules, with rework where needed. The task completes when its requirements are met; otherwise show why it waits.

### Questions and visibility

A question at any step follows [work management](../fleet.md#work-management): task context reaches the addressee roles and the answer reaches the relevant execution. Closing the PM session does not prevent continuation.

The operator sees the task, agent, environment, progress and waiting reasons, and can open execution history or the agent's desktop when available.

### Expected result

The operator receives accessible page materials, check evidence and the [result summary](../fleet.md#operator-interface). Creation, verification and publication remain distinguishable.

### Handoff checks

- Conversation to task: preserve the intended outcome and conditions.
- Task to execution: supply the required materials, rules and capabilities.
- Execution to result: make materials accessible, checks clear and completion requirements satisfied.

<a id="uc2"></a>

## UC2. Compare and recommend 3D methods

Status: accepted. Source: this section; Lab registry entry `fleet:UC2` in [`config/lab/use-cases/fleet.json`](../config/lab/use-cases/fleet.json).

Two pipelines create 3D objects. An architect wants to compare their suitability for game characters and environment props, then make the findings available to project agents.

### Experiment flow

1. The architect, optionally with an agent, selects methods, example objects and comparison criteria.
2. The system runs both methods on the selected examples and retains actual settings, inputs, outputs and errors.
3. The architect and assisting agents inspect objects, add comments and tags, and compare quality, time and known costs. Conclusions link to supporting runs.
4. Through the [laboratory workflow](interfaces.md#architect-workflow), the architect recommends methods for specific uses within a project or across the system. Different methods may suit characters and props.
5. A worker receives a character task and follows the [catalogue selection rules](../fleet.md#tools-and-pipelines). With no explicit method choice, it reads applicable recommendations and usage conditions before choosing.
6. The worker runs the chosen method, retains the object and execution evidence, and checks the task's requirements.

### Recommendation checks

- Experiment to conclusion: supporting results are accessible, including failed runs.
- Conclusion to recommendation: the architect explicitly selects the method's use cases and project or system scope.
- Recommendation to work: human and agent views use the same catalogue; explicit choices and project rules retain precedence.

### Later comparisons

Repeat the experiment with a new method and update the recommendation when appropriate. Earlier runs and assessments remain available; new tasks read current information when choosing.

### Project outcome

The task produces an accessible 3D object with check evidence and a traceable method choice. The recommendation helps selection; it does not establish that this task's result meets its requirements.

<a id="uc3"></a>

## UC3. Handle failure and interruption

Status: accepted. Source: this section; Lab registry entry `fleet:UC3` in [`config/lab/use-cases/fleet.json`](../config/lab/use-cases/fleet.json).

A task has an active execution or external operation. The operator must see what happened and what can continue.

| Event | Expected behavior |
|---|---|
| An executor fails | Retain the error. Apply the [task transition rules](../fleet.md#task-states): safe authorized continuation, a required decision or failure. |
| Contact is lost after deployment starts | Show the outcome as unconfirmed. Observe the original operation; do not automatically repeat the deployment. |
| The operator pauses the task | Start no new steps. Show running operations and whether they can stop. |
| The operator cancels the task | End further work and request active executions to stop. Keep their actual state visible. |

Pausing or cancelling after merge leaves the merged changes intact. Any rollback needs a separate decision under the task rules.

<a id="uc4"></a>

## UC4. Run a pipeline with a failed step

Status: accepted. Source: this section; Lab registry entry `fleet:UC4` in [`config/lab/use-cases/fleet.json`](../config/lab/use-cases/fleet.json).

A project pipeline renders a batch of 3D assets in three steps: prepare inputs, render each asset, assemble a catalogue page. A worker chose the pipeline for a task under the [catalogue selection rules](../fleet.md#tools-and-pipelines).

### Run flow

1. The worker starts the pipeline through its tool description, passing run parameters. The run records the definition revision and resolves each step's instruction at that step's start ([agent execution](../fleet.md#agent-execution)).
2. The system checks granted connections, environment requirements and resource limits, then launches the prepare step; render steps start when prepare finishes and run concurrently.
3. One render step fails. Its dependant assembly step does not start; other render steps finish and retain their outputs. The run fails with the error retained.
4. The task applies the [transition rules](../fleet.md#task-states): the worker reads the error and reruns. The rerun reuses retained outputs of unchanged steps and repeats the failed render and the assembly step.
5. Environments are released after outputs reach the project's result storage; temporary working areas are cleaned under the [ownership and cleanup rules](execution-continuity.md#working-areas).

### Visibility

The operator sees the run as the definition combined with step states, including the failed step, its error and which steps were reused on rerun. Lost contact with a render environment shows that step as unconfirmed, not failed.

### Checks

- Definition to run: parameters, executors and instructions follow the [source rules](../fleet.md#knowledge-and-rules), including recorded drafts when explicitly selected; a pinned run uses its selected revision throughout.
- Failure to rerun: retained outputs survive the failed run; reuse follows the [input and configuration rules](../fleet.md#tools-and-pipelines). Repeat failed or changed steps and affected dependants unless a full rerun is forced.
- Run to result: the task completes only when assembled results reach their destination and the task's completion requirements hold.

<a id="uc5"></a>

## UC5. Qualify an agent and use it in project work

Status: accepted. Source: this section; Lab registry entry `fleet:UC5` in [`config/lab/use-cases/fleet.json`](../config/lab/use-cases/fleet.json).

An architect prepares an agent configuration and a pipeline for a catalogue kind of work. A project operator will use the recommended method through a PM session.

### Qualification flow

1. The architect prepares the configuration, skills, instructions and pipeline definition under the [source rules](../fleet.md#knowledge-and-rules), prepares a suitable environment and configures tool connections. Agents use tools without receiving credentials.
2. The architect runs compatibility checks for the selected combination. Retain tested revisions and outcomes; confirmed incompatibility blocks launch. Incomplete or unchecked results remain visible without being treated as incompatibility.
3. The architect runs representative examples through the shared execution mechanism. Retain full session history, outputs, tool calls, results, errors, time and cost, including failed runs.
4. The architect or a laboratory agent creates an assessment linked to those runs, describing criteria, comparisons, limitations and suitability. A later assessment preserves the earlier one.
5. The architect or an agent with delegated architect authority explicitly recommends the method for the project's kind of work. Without that authority, an agent proposes the decision through a question; an assessment alone does not change the recommendation.
6. The architect grants the project required capabilities and connections. The operator binds granted connections to concrete project resources.

### First project execution

1. The PM prepares a task under project rules. With no explicit method choice, the worker queries recommendations by project and kind of work; the response resolves scope fallback and usage conditions.
2. The worker starts the selected method within granted permissions. The execution records the revisions actually used; a recommendation does not bypass launch checks.
3. The task retains results and full execution history, then follows project verification, acceptance and delivery requirements. Laboratory success does not establish acceptance of this result.
4. The architect can use the project run as evidence for a new assessment. Changing the recommendation remains a separate action.

### Access and traceability checks

- Operators can inspect history within their project grants; they cannot read another project's history without its grant. Architects can inspect all execution history for evaluation.
- Human interfaces and agent queries enforce the same permissions. A recommendation does not grant access to its supporting project history.
- A result links to its execution and actual configuration; a recommendation links to its assessment and supporting runs.

<a id="uc6"></a>

## UC6. Refine a character through conversation

Status: accepted. Source: this section; Lab registry entry `fleet:UC6` in [`config/lab/use-cases/fleet.json`](../config/lab/use-cases/fleet.json).

An operator supplies references in a session and requests a playable character. The agent uses project requirements and an applicable method, or investigates without a predefined pipeline. Results and checks appear in the conversation with links to their executions.

The operator reports damaged hands. The agent reworks the relevant material and affected checks, retaining earlier results for comparison. The operator need not navigate separate step or experiment screens. Inspecting internals or comparing methods remains available through the same underlying records.

<a id="uc7"></a>

## UC7. Evaluate a development approach

Status: accepted. Source: this section; Lab registry entry `fleet:UC7` in [`config/lab/use-cases/fleet.json`](../config/lab/use-cases/fleet.json).

An architect supplies a specification and configures a developer with SDD instructions. The agent implements the project through ordinary execution. The architect reviews the product, records shortcomings and discusses changes in a session.

Run another variant with changed instructions, skills or orchestration; compare result quality, human intervention, repeated work and recorded costs under the [experiment contract](lab.md). The same process can evaluate PM behavior. SDD is a candidate method, not a Fleet requirement.

Once a method is useful, recommend it for relevant assignments within the selected scope. Project work uses that recommendation under the normal selection rules; previous results remain unchanged unless explicitly reworked.

<a id="uc8"></a>

## UC8. Execute delegated or event-initiated work

Status: accepted. Source: this section; Lab registry entry `fleet:UC8` in [`config/lab/use-cases/fleet.json`](../config/lab/use-cases/fleet.json).

An operator supplies a prepared specification and delegates implementation. Alternatively, a configured monitoring event initiates investigation under [event rules](system-operations.md#event-initiated-work). Both use ordinary assignments and execution.

The agent acts within its instructions and authority, optionally delegating work. Questions use the [communication rules](system-operations.md#agent-communication): available requirements, authorized agent decisions, specialist or PM assistance, then human decisions where needed. Independent work may continue while an answer is pending.

For an alert, return a concise finding, evidence, impact and proposed next action. A code defect, infrastructure issue, false alarm or insufficient evidence are possible outcomes. Remediation follows project authority; receiving an alert does not authorize a fix.

The operator sees progress, questions and results without keeping a session open. Project instructions may change planning style, agent roles and permitted automatic actions without changing the execution mechanism.

<a id="uc9"></a>

## UC9. Improve measurable work within a budget

Status: accepted. Source: this section; Lab registry entry `fleet:UC9` in [`config/lab/use-cases/fleet.json`](../config/lab/use-cases/fleet.json).

An architect assigns an optimization objective, requirements and budget using ordinary instructions. An agent iterates through changes, trials and measurements, optionally delegating work to other sessions. Follow [iterative improvement](lab.md#iterative-improvement) for shared budget accounting, comparison and stopping.

For a compiler, compare compilation time and resource use while retaining correctness checks. Return qualifying candidates, measurements and costs; the newest candidate need not be the best. Human comments remain available alongside numeric results.

<a id="uc10"></a>

## UC10. Analyze and improve project operations

Status: accepted. Source: this section; Lab registry entry `fleet:UC10` in [`config/lab/use-cases/fleet.json`](../config/lab/use-cases/fleet.json).

A person asks an agent where a project's work spends time and resources. Both inspect [work analysis](work-analysis.md), including preparation, questions, retries, merge, CI and cleanup, and drill down to supporting executions.

They discuss a suspected inefficiency, change a skill, configuration or workflow, and evaluate it through the same laboratory mechanism. Keep missing measurements visible and compare results before applying the improvement; a proposed saving is not a measured saving.

<a id="uc11"></a>

## UC11. Balance a distributed fleet

Status: accepted. Source: this section; Lab registry entry `fleet:UC11` in [`config/lab/use-cases/fleet.json`](../config/lab/use-cases/fleet.json).

A persistent Linux server coordinates project and laboratory work. Mac and Windows hosts provide additional environments and may disconnect. Register their capabilities and apply [allocation rules](work-allocation.md); a single-machine test does not qualify this scenario.

Through a PM session or direct controls, an operator keeps general work off the Mac, caps Windows concurrency, redirects eligible work during a host problem and later restores it. Prefer Codex while reserving Claude for explicitly requiring tasks; reduce or prohibit Antigravity use as configured. Reevaluate queued work without silently changing requirements or moving active executions.

Show the effective policy, resource and quota observations, selected placement and waiting reasons. Account for shared machine capacity and account quotas across project and laboratory work. An unreachable active executor remains unconfirmed; reconcile before any replacement execution.
