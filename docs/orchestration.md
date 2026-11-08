# Orchestration

Coordination semantics over the [execution contract](execution-contract.md). These do not prescribe a broker, workflow engine or service topology.

## Decisions and progression

An agent decides how to perform an open-ended assignment within its instructions and authority. A pipeline supplies explicit steps and transitions. Fleet records accepted work, evaluates declared dependencies and conditions, launches eligible executions, routes questions and delivers results.

These may be combined: a pipeline step launches an agent, and that agent may invoke a tool, pipeline or another agent. Fleet need not understand the domain reasoning behind each decision.

## Delegation

Delegated work identifies its parent, expected result, inputs, executor requirements and result recipient. Resolve applicable authority, resource limits and project rules from the existing context; delegation does not expand them. The system records these associations without requiring the operator to fill separate forms.

The parent may wait for the result or continue independent work. Child results and questions remain linked to the assignment. Parent completion depends on required child results and checks, not merely on a conversational turn ending.

Use a task or pipeline step where appropriate; no new delegation entity is required. Keep harness-internal actions in native execution history unless Fleet control is needed.

## Continuation without an active caller

Accepted work belongs to retained system state, not to the process that submitted it. Closing a conversation or ending the initiating turn does not cancel it. Keep result recipients and the next declared action available after reconnection.

When a result or answer arrives, continue the pipeline or activate the designated agent under the recorded rules. A waiting PM need not run continuously. If the next action requires judgment, invoke the appropriate agent; do not invent a pipeline transition from text alone.

If the initiating agent becomes unavailable, independent work may continue. Retain results for a supported continuation or handoff; expose a missing decision owner when no authorized route remains. Do not silently substitute an explicitly selected executor or method.

## Durable coordination

Retain accepted assignments, dependencies, pending questions, answers and execution links. After a coordinator restart, reconcile their observed state before issuing further work. Re-delivering an event or reconnecting to history must not create a second logical assignment; uncertain external effects follow the existing recovery rules.

Automatic progression depends on this state, not on continuously polling agents. Transport, persistence layout and notification implementation remain open.

## Operator control

Present the overall assignment and its nested work, with questions and interruptions requiring attention. Existing pause, cancellation, authority and budget rules apply across coordinated work. An available child result is retained even when its parent cannot continue.

## Examples

| Scenario | Decision maker | Fleet coordination |
|---|---|---|
| Build a project from a specification | Developer agent selects actions and may delegate. | Start work, retain child relationships, route questions, collect results and track completion. |
| Generate a game character | Pipeline orders declared stages; an agent handles open-ended adaptation. | Transfer outputs, advance eligible stages and preserve results across rework. |
| Optimize compiler performance | Agent or configured procedure selects each candidate. | Run trials, collect measurements, account for nested usage and apply budget controls. |
| Investigate an alert | Investigator classifies the issue and proposes an action. | Start from the configured event, deliver findings and route required decisions. |

