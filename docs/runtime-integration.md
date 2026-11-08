# Runtime integration

Responsibility boundaries for [agent execution](../fleet.md#agent-execution), not prescribed services or repositories.

| Layer | Responsibility |
|---|---|
| Native harness | Model interaction, native conversation, tool execution and supported session behavior. |
| Runtime adapter | Translate inputs and controls, expose actual capabilities, preserve native history and observations, report unsupported behavior. |
| Fleet | Assignments, project context, orchestration, durable questions, results, environment allocation, shared budgets and cross-run analysis. |
| Project configuration | Methodology, role instructions, domain checks, tools and delivery rules. |

Keep a common execution interface without discarding additional harness capabilities. Expose supported controls for the selected runtime and mode; unsupported controls remain explicit.

Use the [shared execution contract](execution-contract.md) for inputs, observations, controls and outcomes.

Native session resume differs from handoff to another harness using retained context and artifacts. Native permission prompts differ from product questions; delegated product decisions do not grant native tool access. A native turn finishing does not establish task acceptance or publication.

Agents may organize their internal work. Retain available tool and child-agent observations without requiring each action to become a Fleet task. Delegated work needing Fleet lifecycle or budget control uses Fleet operations.

Native token usage is one observation, not total assignment cost or infrastructure consumption. Missing observations remain unknown under [work analysis](work-analysis.md).

AgentQ is a reuse candidate. The [source review](runtime-boundary-review.md) does not establish native qualification.
