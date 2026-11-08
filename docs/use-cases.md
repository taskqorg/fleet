# Fleet use cases

Scenarios for the [system specification](../fleet.md). These describe accepted behavior, not runtime evidence.

## UC1. Create a website page

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

## UC2. Compare and recommend 3D methods

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

## UC3. Handle failure and interruption

A task has an active execution or external operation. The operator must see what happened and what can continue.

| Event | Expected behavior |
|---|---|
| An executor fails | Retain the error. Apply the [task transition rules](../fleet.md#task-states): safe authorized continuation, a required decision or failure. |
| Contact is lost after deployment starts | Show the outcome as unconfirmed. Observe the original operation; do not automatically repeat the deployment. |
| The operator pauses the task | Start no new steps. Show running operations and whether they can stop. |
| The operator cancels the task | End further work and request active executions to stop. Keep their actual state visible. |

Pausing or cancelling after merge leaves the merged changes intact. Any rollback needs a separate decision under the task rules.

## UC4. Run a pipeline with a failed step

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

- Definition to run: parameters, executors and instruction paths resolve from committed revisions; a pinned run uses one revision throughout.
- Failure to rerun: retained outputs survive the failed run; reuse follows the [input and configuration rules](../fleet.md#tools-and-pipelines). Repeat failed or changed steps and affected dependants unless a full rerun is forced.
- Run to result: the task completes only when assembled results reach their destination and the task's completion requirements hold.

## UC5. Qualify an agent and use it in project work

An architect prepares an agent configuration and a pipeline for a catalogue kind of work. A project operator will use the recommended method through a PM session.

### Qualification flow

1. The architect commits the configuration, skills, instructions and pipeline definition, prepares a suitable environment and configures tool connections. Agents use tools without receiving credentials.
2. The architect runs compatibility checks for the selected combination. Retain tested revisions and outcomes; confirmed incompatibility blocks launch. Incomplete or unchecked results remain visible without being treated as incompatibility.
3. The architect runs representative examples through the shared execution mechanism. Retain full session history, outputs, tool calls, results, errors, time and cost, including failed runs.
4. The architect or a laboratory agent creates an assessment linked to those runs, describing criteria, comparisons, limitations and suitability. A later assessment preserves the earlier one.
5. The architect explicitly recommends the method for the project's kind of work. An assisting agent may propose this decision through a question; an assessment alone does not change the recommendation.
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
