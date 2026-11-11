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

Native token usage is one observation, not total assignment cost or infrastructure consumption. Missing observations remain unknown under [work analysis](designs/fleet-lab.md#work-analysis).

AgentQ is a reuse candidate. The [source review](runtime-boundary-review.md) does not establish native qualification.

## Named capability readiness

A selected Serena Go-navigation grant resolves through the existing profile and host launch path. Missing required executables produce a preparation failure with the dependency name before model launch. Requested/applied preparation receipts identify the selected tools and dependency paths; they mark native calls unqualified until actual history or a capability check supplies evidence. Host-root, account and project grants still apply.

`fleet-executor qualify-serena PROFILE FIXTURE_ROOT RECEIPT NATIVE_ACCOUNT HOST_REGISTRY` is the model-free program checker used by the Lab capability pipeline. It starts the same local sidecar in a temporary private HOME, initializes MCP, checks the selected advertised tools, calls the startup manual, observes onboarding separately and queries the known temporary Go symbol. Its retained receipt separates setup/startup and symbol work, includes source/profile/instruction/manual hashes and versions, and leaves USD unavailable. An actual model witness is required to qualify instruction following; no native cross-harness or platform parity is inferred from this checker. Desktop/browser, provider and task-release operations are outside the check.
