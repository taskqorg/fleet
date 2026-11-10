# Architecture simplification review

Review of the root contract and its active supporting specifications, 2026-11-09. This records design changes and coverage, not implementation evidence. Canonical rules remain in the linked specifications.

## Boundary

The core coordinates work, execution, communication, material access, authority, recovery and observation. Instructions, skills, tools and pipelines supply domain behavior. Interfaces expose those mechanisms without requiring a screen or form for each concept.

## Corrections

| Finding | Correction and owner |
|---|---|
| Object navigation displaced actual conversation and autonomous work. | [Interfaces](interfaces.md#interaction-model) support both; object details are optional navigation. The character storyboard is an unapproved historical study. |
| Every assignment implied another task, including ordinary in-session actions. | [Communication](system-operations.md#agent-communication) tracks independent delegation while retaining ordinary actions in existing execution history. |
| Laboratory definitions required a prior step. | [Experiments](lab.md) accept new assignments and whole tasks or project implementations through ordinary execution. |
| Testing changes required committed revisions and a branch. | [Source rules](../fleet.md#knowledge-and-rules) allow explicitly selected drafts with actual content retained. Git remains the canonical definition store. |
| An agent always needed a human to set a recommendation. | [Evaluation](../fleet.md#observation-and-evaluation) permits explicit delegated architect authority; assessment alone still changes nothing. |
| Questions implied human recipients and an active PM conversation. | [Communication](system-operations.md#agent-communication) supports authorized agents and durable routing independent of open windows. |
| Monitoring could become a separate workflow engine. | [Events](system-operations.md#event-initiated-work) invoke existing operations; alert evidence and remediation authority stay distinct. |

## Preserved mechanisms

- Task, session and execution remain distinct: conversation can span work; execution can continue without an open window.
- Result identity, actual settings, checks and history remain available across roles and retries.
- Dependencies and pipeline transitions remain structured where Fleet coordinates execution.
- Access, recovery from uncertain external outcomes and working-area ownership remain explicit.
- Resource accounting, cleanup, scoped recommendations and project overrides remain supported.
- No mandatory methodology, dedicated SDD service, alert executor, PM process or domain result schema is introduced.

## Scenario coverage

| Scenario | Shared mechanisms |
|---|---|
| [Character iteration](use-cases.md#uc6-refine-a-character-through-conversation) | Session, project context, ordinary execution, results and rework. |
| [SDD comparison](use-cases.md#uc7-evaluate-a-development-approach) | Configured agents, assignments, experiment variants, observations and recommendations. |
| [Autonomous implementation](use-cases.md#uc8-execute-delegated-or-event-initiated-work) | Assignment, delegation, questions, authority and continuation. |
| [Monitoring investigation](use-cases.md#uc8-execute-delegated-or-event-initiated-work) | Event invocation of the same assignment and communication operations. |
| [Environment and account interruption](use-cases.md#uc3-handle-failure-and-interruption) | Existing continuation, access, connection and resource mechanisms. |

## Unverified boundaries

Logical coverage does not establish native harness fidelity or interface usability. Validate conversation events, question delivery, continuation and artifact presentation against real harnesses before choosing adapter contracts. Physical services, database schemas, file layouts and API names remain undecided.

Confirmed compatibility failures still require applicability to the requested configuration. The review does not replace compatibility checks, change authority grants or authorize real tasks and experiments.
