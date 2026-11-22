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

The finite sequential pipeline runtime persists plan revision, activation evidence, exact selected execution references and dispatch ownership in the same PostgreSQL transaction fence. Competing transitions cannot overwrite a newer plan. A selected host action reserves one native execution and command before handoff; token mint is deferred until current authority is checked. Repeated host delivery preserves that command's identity/body; the executor journal admits its handler once. API/publication handoff uses a committed claim before effects. An ambiguous effect stays unconfirmed and is not resubmitted under another identity.

Cancellation blocks pending entries, stops prepared commands before handoff and preserves selected references. Work already handed off remains unconfirmed until independent receipts establish its outcome; late events do not reopen the parent automatically. Publication recovery requires its durable immutable filesystem receipt and cannot restage an absent receipt. Explicit new-format prefix/local-suffix recovery verifies frozen definition/config/model/input/artifact and command/process evidence; missing proof returns 409. Reused work keeps original ownership and spend. General graph execution, concurrency and expression evaluation remain outside this finite sequential slice.

## Operator control

Present the overall assignment and its nested work, with questions and interruptions requiring attention. Existing pause, cancellation, authority and budget rules apply across coordinated work. An available child result is retained even when its parent cannot continue.

## Swarm coordination and Code Mode

For multi-agent teamwork across arbitrary Fleet harnesses (`codex`, `claude`, `antigravity`, `acp`), [Fleet Swarm](designs/fleet-swarm.md) builds on these coordination semantics: a coordinator profile decomposes a goal into structured threads and disjoint worker assignments using configurable strategies (sizing, verifier ratio, stall monitoring, workspace isolation `inherit`/`share`/`branch`, per-worker tools and instructions), and coordinates over a durable shared message board (`@mention`, `@all`, channels, cursor-based event wakes without polling) with structured progress, blocker, `[done]` and screenshot reports. Coordinators and workers combine this with universal [Code Mode](designs/fleet-code-mode.md) to orchestrate parallel turns in JavaScript and return compact syntheses into model context.

## Examples

| Scenario | Decision maker | Fleet coordination |
|---|---|---|
| Build a project from a specification | Developer agent selects actions and may delegate. | Start work, retain child relationships, route questions, collect results and track completion. |
| Generate a game character | Pipeline orders declared stages; an agent handles open-ended adaptation. | Transfer outputs, advance eligible stages and preserve results across rework. |
| Optimize compiler performance | Agent or configured procedure selects each candidate. | Run trials, collect measurements, account for nested usage and apply budget controls. |
| Investigate an alert | Investigator classifies the issue and proposes an action. | Start from the configured event, deliver findings and route required decisions. |

