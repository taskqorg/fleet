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

An absent Claude native `tools` selection preserves ordinary file, shell and internal delegation tools; an explicit list, including an empty list, restricts them. The same selection reaches tool availability and noninteractive permission grants. Codex retains its native tool mapping without a Claude tools array. Personal configuration and credentials are not inherited.

Agents may organize their internal work. Retain available tool and child-agent observations without requiring each action to become a Fleet task. Delegated work needing Fleet lifecycle or budget control uses Fleet operations.

Native token usage is one observation, not total assignment cost or infrastructure consumption. Missing observations remain unknown under [work analysis](designs/fleet-lab.md#work-analysis).

AgentQ is a reuse candidate. The [source review](runtime-boundary-review.md) does not establish native qualification.

## Named MCP readiness

Named profile selections resolve through the existing private host/account registry and shared stdio boundary. Missing or unsafe required executables and unsupported transports fail preparation before model launch. Requested/applied receipts identify selected tools and dependency paths; actual discovery/call evidence remains unqualified until observed. Codex instruction receipts include the resolved appended skill text and individual skill hashes. Host-root, account, project and gateway grants still apply.

`fleet-executor qualify-mcp PROFILE FIXTURE_ROOT RECEIPT NATIVE_ACCOUNT HOST_REGISTRY CHECKS` is the model-free Lab checker. It requires the existing execution-owned `FLEET_MCP_OWNER` endpoint, a temporary HOME and declared source paths plus per-server calls/oracles. It uses the same registry/filter/guardian path as native launches. The Serena fixture calls its manual, observes onboarding independently, and verifies a known Go symbol. Receipts retain hashes and declared startup/work timing, never raw registry/tool payloads. USD remains unavailable; the checker does not establish native model behavior or all downstream provider consumption for an arbitrary MCP server.

Every local stdio MCP server gets a fresh mode-0700 HOME inside its execution-owned directory; nested owned dependencies inherit it. The harness credential HOME is unchanged. No personal MCP configuration, prompts or caches are copied. Verified owner cleanup removes this state; unknown cleanup retains it with the ownership evidence. This is configuration isolation for trusted selected servers sharing the executor UID, not a hostile-code sandbox. Internal design-rules and gateway adapters retain their explicit arguments and authorization.

Local MCP guardians are added to the executor's existing launch journal only after Unix peer/execution/incarnation checks, before useful server work. Private lifetime/status descriptors are close-on-exec and absent from server descendants. Evidenced escaping dependencies use the same guardian and registration through a fixed executable alias and generation lease; raw command stdio is inherited directly rather than copied. A dependency result requires its private exit-status receipt, not an empty successful transport. Queued native requests cannot hide control EOF; no mutation is automatically replayed after an uncertain response. Terminal/restart cleanup retains unknown ownership instead of signaling a reused/unrelated group or forgetting the execution fence. macOS/Linux process qualification is distinct from harness qualification; the new containment does not support Windows. Remote MCP transport/auth and additional native harness qualification require their own evidence, not inferred parity.

The trust boundary is the explicitly selected installed server under the executor UID. Private alias permissions and fixed target/generation checks protect against mistakes, not malicious same-UID code. An unbound daemon/process escape is unsupported; do not promote such a server as ready or silently add another containment mechanism.

## Resolved instruction delivery

The executor assembles one instruction payload after validating the selected profile and resolving frozen [project sources](configuration.md#pinned-instruction-sources): verified core text and its explicit pin/precedence header, profile and skill snapshots, a linked-source index, then frozen project instructions. This same payload supplies the preparation receipt and actual native request. A passive `AGENTS.md` alone is not proof that a native harness loaded the project contract.

Codex receives identical text through its CLI developer-instruction setting and protocol `developerInstructions`; resumed sessions receive explicit developer-role instruction delivery through the existing acknowledged injection path. Claude receives the assembled system prompt; ACP receives it through its existing prompt shape. Deterministic shape tests establish these mappings, not live harness compatibility or model compliance. Actual native observations and task acceptance remain separate evidence.

There is no durable native acknowledgement proving that an unchanged source bundle remains loaded across every continuation. Fleet therefore sends the resolved instructions explicitly again on resume; repeated core text and its token overhead are retained rather than silently deduplicated. A running execution keeps its frozen context; changing the project affects later launches, not that execution. Missing declared sources fail before native launch, with no runtime shell-fetch fallback. No source setting is required for legacy projects or program-only work.

### Native Git writes and terminal delivery

For a project execution with its existing Git network grant, the Codex adapter sends an explicit workspace-write turn policy scoped to the checkout's canonical common Git directory and its own linked-worktree admin directory. The worktree must belong to that checkout. The more specific admin directory is needed because the native workspace baseline protects Git metadata. The adapter does not grant the parent directory or disable the sandbox. Git commands receive no system/global configuration; repository hooks and fsmonitor are disabled through the execution environment.

A completed native turn, physical process closure and coordinator receipt are separate observations. The owning executor retains closure and receipts in its durable outbox until acknowledged. If an idempotent event batch exceeds the transport deadline, delivery falls back to individual retained rows before its terminal receipt. A timeout does not replay model work or an external publication, remove retained evidence, or establish coordinator completion.
