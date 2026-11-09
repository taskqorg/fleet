# Work lifecycle proposal

Not adopted. Retained as a coverage study; it must not introduce mandatory work stages. Agent instructions, tools and observable execution remain the primary model under Fleet R6.

End-to-end responsibility map over existing [task states](../fleet.md#task-states), [execution operations](execution-contract.md) and [orchestration](orchestration.md). These activities are not additional statuses, mandatory screens or a universal pipeline.

## From intent to outcome

| Activity | Responsible party | Retained handoff |
|---|---|---|
| Discuss or receive an event | Person and selected agent, or configured event rule. | Session context or source event; no task for every message. |
| Accept an assignment | Authorized caller through Fleet operations; PM may prepare it. | Expected outcome, available inputs, requirements, result destination and originating context. |
| Prepare execution | Coordinator selects eligible resources; host resolves working materials and runtime preparation. | Actual configuration, input identity, working area, readiness and launch outcome. |
| Perform work | Agent, program or pipeline; coordinator manages declared dependencies and delegation. | Progress, questions, child work, intermediate results and resource use. |
| Resolve a question or interruption | Authorized agent or person decides; Fleet delivers the answer or continuation action. | Decision, affected work and confirmed delivery/outcome. |
| Inspect and verify | Assigned agent, checks and any project-required reviewer or person. | Accessible candidate, checks, limitations and acceptance outcome. |
| Apply or publish when required | Authorized project operation, agent or person. | Actual change in the destination system and supporting observations. |
| Complete and retain | Fleet evaluates the assignment's completion conditions; cleanup follows working-area ownership. | Outcome summary, final materials, checks, history, costs and retained-work reasons. |

The operator can follow or discuss these activities in a session and inspect the same work from its task view. PM, reviewer and publication steps are not universally required roles or stages.

## Completion means the assigned outcome

An investigation may finish with findings and a proposed fix. A fix assignment may require merged code. A delivery assignment may require deployment. A laboratory assignment may require compared candidates and measurements. The assignment and project rules determine completion; Fleet does not impose publication on every result.

Reaching a laboratory budget stops further work under its configured policy. It does not prove that the optimization target was achieved. Return actual candidates and limitations and evaluate the assignment's completion conditions separately.

Agent termination, a final message, a tool exit or a passed check alone does not establish completion. Required outcomes without supporting evidence remain unmet or unconfirmed.

## Changes during work

An instruction in conversation identifies its affected assignment and intended scope. Clarifications may continue the same work; a new independent outcome may become another assignment. Do not automatically create a task for every correction.

Deliver active input according to harness capabilities. Until accepted, retain the prior execution state and show pending delivery. Task changes do not silently alter the global method or imply that already running code used new instructions.

Rework retains previous results and checks, repeats the relevant work and includes affected downstream checks. A new candidate does not automatically replace an already published result.

## Waiting, failure and recovery

- A question or unavailable dependency waits at the affected scope. Runnable independent work can continue; show both progress and pending decisions without collapsing every child into one misleading status.
- A failed execution retains partial results. Continue under authorized recovery rules or show the next required decision; do not infer task failure from every failed attempt.
- Missing contact means an unconfirmed execution. Reconcile before launching a replacement that could duplicate its effects.
- Pause and cancellation follow existing task rules. They do not establish immediate process termination or undo published changes.
- Retention and cleanup continue independently of whether the final pipeline step ran. A cleanup failure remains visible without invalidating an otherwise completed result.

## Four coverage examples

| Scenario | Complete when | What may follow separately |
|---|---|---|
| Discuss and create a CSGO character | The assigned artifact, game checks and requested integration are satisfied. | Improve the reusable method or create another character. |
| Implement a project from a specification | Required implementation, checks and delivery conditions hold. | New product requirements or subsequent releases. |
| Investigate a monitoring alert | Required investigation and evidence-backed findings are returned. | Authorized remediation, if outside the original assignment. |
| Compare SDD or optimize a compiler | The agreed experiment output is delivered; target attainment is reported separately. | Adopt a method or apply selected results to a product. |

## Remaining design boundary

Exact UI presentation, external synchronization and implementation schemas remain open. This proposal connects agreed mechanisms; live cross-host and native qualification remains required.
