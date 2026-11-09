# First-version technical design

Proposal for the [accepted first working slice](delivery-strategy.md#first-working-slice). This describes a buildable boundary without selecting prototype dependencies, languages, database or transport. Operation names below are proposed, not existing APIs. Existing canonical requirements take precedence.

The subsequent [implementation decisions](v1-implementation.md) select the initial stack and build order. This document remains the detailed boundary proposal; assessments do not establish runtime qualification.

## Process and module boundaries

Use one coordinator service and one executor process per participating host. Place the coordinator on PDS-18 for the target installation; a development installation may put both on one machine. Keep the agent-tool adapter within the coordinator's application boundary unless the selected tool protocol requires a separate process.

| Boundary | Owns | Does not own |
|---|---|---|
| Coordinator | Projects, tasks, session associations, configuration resolution, dispatch, durable commands, questions and result references. | Native harness protocols and host process implementation. |
| Executor | Host capabilities, local working areas, preparation, owned processes, pending observations, artifact delivery and cleanup. | Project task authority and global scheduling policy. |
| Harness adapter inside executor | Translate prepared input, native start/input/control, session identity, history and capability reporting. | Fleet task states or mandatory PM/developer/reviewer roles. |
| Agent-tool adapter | Expose scoped coordinator operations as discoverable tools and return their outcomes. | A separate work database or duplicate orchestration rules. |
| Preview facility | Serve selected website output and report address, access and lifetime. | Task acceptance or production publication. |

The preview may be executor-managed rather than another service. Native file, shell and browser tools remain usable without wrapping every call in Fleet. Source ownership can initially live in one Fleet repository; extraction into a shared library requires demonstrated value, not an existing product name.

## Stored objects

These are conceptual records, not one table or service per row.

| Record | Minimum contents and owner |
|---|---|
| Agent definition | Repository references to harness/model selection, instructions, skills, tools and environment requirements. Canonical files remain authoritative. |
| Project | Context/material references and applicable rules; coordinator indexes the project and references its sources. |
| Task | Assigned outcome, project, inputs, state and linked executions; coordinator owns it. Not required for every session message. |
| Session | Conversation identity, selected agent, related projects/tasks and native-session links; coordinator associates, harness retains native semantics. |
| Execution | Actual prepared configuration, inputs, host/working area, session/task links, state and outcome; coordinator owns the system record, executor supplies observations. |
| Command | Caller request identity, target, requested action and delivery/outcome; coordinator retains it, executor recognizes already received commands. |
| Event | Execution, source position, occurrence/receipt times, kind and payload or retained-native-data reference. Executor captures; coordinator retains delivered history. |
| Working area | Origin, associated work, path, occupied space and retention/release reason; executor manages it and reports to coordinator. |
| Result | Description, media type, source execution, location and accessibility; coordinator retains references, selected storage holds content. |

Questions and measurements are linked records within this model. A measurement includes scope, unit, source and coverage; an unavailable value is not zero. Keep process state, contact status and task acceptance distinct.

Repository edits affect subsequent preparations under the existing source rules. Recording the content used by an execution does not freeze future work. Native-loaded instructions and tool availability are separate observations from requested configuration.

## Agent-facing operations

All operations enforce caller scope. Defaults reduce input requirements; the following fields are semantic inputs, not mandatory operator forms. Definition changes use the existing repository workflow; no second editable configuration store is introduced.

| Proposed operation | Input | Output or effect |
|---|---|---|
| `context.read` | Project references and purpose. | Relevant rules, materials, available methods and missing context. |
| `agent.list` | Purpose, project scope and required capabilities. | Suitable definitions and known availability; no launch. |
| `agent.prepare` | Definition reference, context and environment preference. | Resolved configuration, source references, required capabilities and known gaps; no native launch. |
| `environment.list` | Required capabilities or host selection. | Registered environments, contact, capacity and eligibility reasons. |
| `execution.start` | Request identity, configuration reference, input, project context and optional task/session. | Execution identity and accepted/rejected outcome; subsequent events establish start. |
| `execution.read` | Execution identity and optional history cursor. | Current observations, ordered event page, result references, measurements and next cursor. |
| `session.send` | Request identity, session, message/materials and optional question reference. | Command identity and queued/delivered/refused/unknown outcome. |
| `execution.stop` | Request identity and execution. | Command identity and requested versus observed termination. |
| `command.read` | Request or command identity. | Existing delivery/outcome, including uncertainty after lost contact. |
| `result.read` | Result identity. | Material metadata, available content/access route and known unavailability. |

Agent definitions are edited through ordinary authorized file tools in the first version, then resolved through `agent.prepare`. Host registration is an installation operation. A task assignment may be created with `execution.start`; opening an ordinary session does not invent a task. Preparation is inspectable but not a mandatory extra call before every start.

An execution that delegates a new assignment calls the same start operation with its caller/parent association. The coordinator retains that association for history and accounting. Delegation does not require a pipeline engine or a permanently running PM.

## Executor exchange

An authenticated executor registers its identity and reports environments, capabilities, capacity and contact. Registration does not grant arbitrary project access. The coordinator dispatches only within configured authority and eligibility.

The initial exchange needs dispatch, input/control delivery, event upload, command readback and artifact delivery. Keep it behind one host protocol; choose its concrete transport after assessing reconnect and deployment requirements. A separate message broker is not assumed.

Persist accepted commands before acknowledging them. Preserve command identity across retries and retain executor receipts so reconnect does not launch work twice. Do not promise exactly-once external effects: after a crash, reconcile process/native evidence and report uncertainty rather than blindly replaying the action.

The executor retains undelivered observations locally. Event identity and source position allow upload retries without duplicate history; retain source order and timestamps without claiming a universal clock order across hosts. Client reconnection reads history rather than resubmitting input.

Disconnected executors follow the [existing continuity rules](deployment.md#disconnection-behavior). Stop remains pending until observed. This mechanism must not introduce a global lock that prevents unrelated work.

## Hello World path

1. An operator uses an ordinary agent session to discuss the website. The agent reads project context and saves the short specification with its native file tools.
2. When implementation is requested, the agent starts a developer assignment through Fleet. The coordinator records the assignment and execution, resolves the definition and selects an eligible executor.
3. The executor prepares a working area and makes the selected instructions, skills, tools and project inputs available. It records preparation, actual native launch and subsequent observations.
4. The developer edits and checks the website using its ordinary tools. Questions and replies retain their session/execution association; the native adapter reports supported input behavior.
5. The preview facility exposes the result. The developer checks actual rendering and returns sources, evidence and preview information. The executor preserves accessible results before releasing temporary files.
6. The caller retrieves the outcome and history through Fleet operations. Native completion and satisfaction of the assignment remain separate facts.

The supplied [instruction kit](../examples/hello-world/README.md) is example input for this path. It does not select a methodology, install tools or establish runtime support.

## Prototype assessment before adoption

Assess mechanisms against the boundaries above, not whole product names. Record the inspected revision and one conclusion: reuse unchanged, adapt in its owner, extract a bounded mechanism or implement anew.

| Mechanism | Questions to resolve | Evidence needed |
|---|---|---|
| Harness preparation and control | Can selected instructions/tools be supplied without inherited product roles? Are unsupported controls explicit? | Trace actual preparation/start/input/stop/history paths; later run one isolated native scenario. |
| Host/process primitives | Can ownership, resource observation and cleanup work without board or global-install assumptions? | Inspect call dependencies and failure cleanup; focused process tests before adoption. |
| Durable dispatch and storage | Can Fleet own state independently of issues and preserve uncertain command outcomes? | Trace acceptance/recovery writes and replay failure cases with a fake executor. |
| History and accounting | Can source events and native details be retained without duplicating charges or requiring every action to be a task? | Inspect ingestion/identity/aggregation and verify representative captured records. |
| Artifact/preview handling | Can a remote caller access results after the producing agent exits? | Inspect storage/lifetime behavior; verify retrieval and preview access in the first run. |

Do not copy modules until their dependencies, implicit policy and maintenance cost are understood. A matching API signature or passing prototype selftest does not establish suitability. Existing source reviews are pointers, not adoption decisions.

## Implementation decision and completion

Resolve the prototype assessment, then select the coordinator/executor implementation, database, transport and tool exposure together. Record a small deployment layout and the first executable build/test commands. Technology selection should close this package, not begin another general architecture discussion.

Build in the [planned order](first-version-plan.md#build-order), with observations from the first increment. Use the [accepted completion evidence](delivery-strategy.md#completion-evidence) as the PoC finish line. Broader platform support, full interfaces and advanced scheduling remain later iterations; they do not prevent a bounded first proof.
