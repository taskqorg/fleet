# System operations

Human interfaces, pipelines and agents use the [shared core](../fleet.md#principles) within their permissions. This contract defines operation groups and interaction semantics, not API names or transport.

## Operation groups

| Group | Operations |
|---|---|
| Projects and tasks | Create, read and update projects and tasks; set dependencies and priorities. |
| Execution | Launch agents or pipelines, observe state, request stop or continuation. |
| Handoff | Prepare context, select a successor and transfer work and working area access. |
| Communication | Send messages, ask questions, answer and deliver results. |
| Environments and resources | Find suitable environments, allocate working areas, check availability and release resources. |
| Catalogue and history | Discover methods and recommendations; retrieve results, history and telemetry. |
| Configuration and access | Manage agent configurations, skills, pipeline definitions, connections and grants within role permissions. |
| Experiments and improvement | Create experiments from recorded work, compare variants, write assessments, set recommendations and apply selected improvements under project rules. |

## Method contracts

Define each method's authorized callers, inputs, outputs, state changes and how its outcome can be checked after lost contact.

Commands report acceptance separately from work starting and results becoming available. A lost response does not authorize duplicate work; callers must be able to determine the original operation's outcome.

## Agent communication

A work assignment with an expected result creates a tracked task or step. An ordinary message does not automatically create a work obligation.

A question requires an answer and may block continuation. A notification reports an event without requiring an answer. Route questions through the [addressee roles](../fleet.md#work-management).

Retain messages, questions, answers and results with their associated work where applicable. Make them available after reconnection under the same access rules as the underlying data.

Provide the context needed to act and access to further detail. Communication supports the [handoff contract](execution-continuity.md#continuation-and-handoff); delivery alone does not establish that work started or completed.
