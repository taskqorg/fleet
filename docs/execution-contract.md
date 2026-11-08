# Shared execution contract

Operation semantics under the [runtime boundary](runtime-integration.md), not API names, schemas or verified runtime support.

## Operation families

| Operation | Caller supplies | Observable outcome |
|---|---|---|
| Start or continue | Assignment or conversational input, configuration, project context, materials, environment requirements and applicable authority/budget; existing session when continuing. | Execution identity, preparation and actual start, selected environment and session where applicable, or a specific unmet condition. |
| Observe | Execution or session identity and a history position. | Ordered available messages, tool activity, questions, results, usage and state; retained history after reconnect. |
| Send input or answer | New instructions, materials or an answer tied to a pending question. | Whether input was delivered, queued, refused or remains unconfirmed; acceptance does not imply the agent acted on it. |
| Request interruption | Target execution and requested control. | Requested versus observed stop, continuing external operations and retained work; available resume or handoff paths. |
| Collect outcome | Execution identity. | Actual outcome, accessible outputs, checks, history and known usage, including partial results after failure. |

Configuration and ordinary human interfaces may supply defaults; these are not mandatory fields the operator fills for each message. Tools and programs use the applicable operations without acquiring a synthetic conversational session.

## Preserve meaning across layers

- A message, a native turn and a task are different scopes. Completing one does not complete the others automatically.
- Observation reconnect reads history; it does not resend the assignment. Recover command outcomes before retrying under the existing recovery rules.
- Steering an active session and supplying the next turn differ. Expose the supported behavior and delivery state; do not silently claim active steering when input is only queued.
- Questions retain identity and answers across views. Native permission requests remain governed by access authority, not ordinary PM decision delegation.
- Outputs retain accessible material references and descriptions. Domain tools may return additional structured data; Fleet need not define every artifact's domain schema.
- Retain native details alongside common observations so additional harness capabilities remain accessible.

## Three scenario traces

| Scenario | Shared path | What must be demonstrated |
|---|---|---|
| Interactive character refinement | Start a session, observe output, send a correction, collect the revised material. | Ordered history and images; native-supported input handling; original and revised materials remain accessible. |
| Autonomous project implementation | Start an assignment, observe background work, route a question, deliver its answer, collect the outcome. | Closing the UI does not stop work; the answer reaches the intended execution once; agent completion is separate from task checks. |
| Budgeted improvement | Start an assignment, launch a trial or child execution, collect measurements, select another change and repeat. | Parent links and shared resource accounting; comparable measurements; stop behavior and retained best results at the budget boundary. |

The loop strategy belongs to instructions or a pipeline. Fleet provides execution and accounting, not a mandatory optimization algorithm.

## Evidence still needed

AgentQ source exposes relevant launch, input, history, event and control surfaces; see the [source review](runtime-boundary-review.md). Native tests must establish which combinations of harness, mode and environment support each scenario. No paid or native sessions have been run to qualify this contract.

Especially verify mid-turn input, question recovery, image accessibility, stop confirmation and child usage coverage. Unsupported observations remain visible gaps; they do not justify claiming complete control or accounting.
