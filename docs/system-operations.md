# System operations

Human interfaces, pipelines and agents use the [shared core](../fleet.md#principles) within their permissions. This contract defines operation groups and interaction semantics, not API names or transport.

Agents choose operations according to their assignment and instructions. The groups below are capabilities, not a prescribed sequence. Expose relevant tools through supported harness integrations, including MCP, without requiring every tool call to become a task or pipeline.

## Operation groups

| Group | Operations |
|---|---|
| Projects and tasks | Create, read and update projects and tasks; set dependencies and priorities. |
| Execution | Launch agents or pipelines, observe state, request stop or continuation. |
| Handoff | Prepare context, select a successor and transfer work and working area access. |
| Communication | Send messages, ask questions, answer and deliver results. |
| Environments and resources | Register execution hosts and environments, inspect capacity, allocate working areas, steer placement and harness use, adjust concurrency and release resources. |
| Catalogue and history | Discover methods and recommendations; retrieve results, history and telemetry. |
| Configuration and access | Manage agent configurations, skills, pipeline definitions, connections and grants within role permissions. |
| Experiments and improvement | Create experiments from recorded work, compare variants, write assessments, set recommendations and apply selected improvements under project rules. |

## Method contracts

### Data access

Provide scoped search, summaries and detail retrieval for project context, configuration, methods, current work, results, history, resources and measurements. Return relevant facts with source references and explicit missing data; allow drill-down without loading whole histories or navigating human pages. Read-only queries do not initiate work or change configuration.

Keep queries and action tools connected through the same object identities. An agent can find a method, invoke it, inspect the run and compare its results without separate copies of system state. No particular MCP server layout is required.

### Action outcomes

Define each method's authorized callers, inputs, outputs, state changes and how its outcome can be checked after lost contact.

The [shared execution contract](execution-contract.md) defines start, observation, input delivery, interruption and outcome collection across interactive work, autonomous assignments and experiments.

[Orchestration](orchestration.md) coordinates delegation, dependencies and continuation without requiring an active caller session.

Commands report acceptance separately from work starting and results becoming available. A lost response does not authorize duplicate work; callers must be able to determine the original operation's outcome.

## Agent communication

An independently delegated assignment is tracked as a task or step. Ordinary conversation and actions within an existing assignment do not require separate tasks; retain them in session and execution history.

A question requires an answer and may block continuation. A notification reports an event without requiring an answer. Route questions through the [addressee roles](../fleet.md#work-management).

Agents use available requirements and decide within their assignment and authority. Route unresolved questions to a configured agent or person; technical versus product subject alone does not determine authority. Agent assistance may retrieve context, propose options or answer within delegated authority.

A question for a secret (an API key) collects the value in a field the answer path never sees; the asking agent receives only a reference to the stored secret ([key requests](designs/fleet-connections.md#2-keys)).

Keep one question and answer across session, task and attention views. Wait only on affected work. Routing and continuation do not require an open conversation window or a continuously running PM.

Retain messages, questions, answers and results with their associated work where applicable. Make them available after reconnection under the same access rules as the underlying data.

Provide the context needed to act and access to further detail. Communication supports the [handoff contract](execution-continuity.md#continuation-and-handoff); delivery alone does not establish that work started or completed.

## Event-initiated work

Configured events may invoke the same operations as people and agents, within granted authority. Retain the source event with the work. An alert may initiate investigation; it does not establish a defect or authorize remediation. Use project rules to continue automatically or request a decision.
