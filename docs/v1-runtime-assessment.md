# V1 runtime assessment

Source review, 2026-11-09. Recommendation, not dependency adoption or native qualification.
Paths below are relative to the named repository under `/Users/kirs/Projects`.

## Evidence boundary

- AgentQ HEAD: `7c48127cd952d7b001207575f7c2ce7889c1ef51`.
- CoreQ HEAD: `bed3ab4e3682cd00483ef798d96628d04dc85dc9`.
- Both checkouts have modified `taskq.json`; neither was changed by this review.
- Inspected Fleet specification, first-version design/plan and delivery strategy.
- Source establishes candidate paths and mismatches, not working native delivery.
- No model calls, builds, tests, services, installation or deployment were run.

## Decisions by mechanism

| Mechanism | Decision | Source evidence and Fleet consequence |
|---|---|---|
| Harness boundary | Extract selected native mechanisms behind one Fleet adapter | `runtime/src/agent/adapter.rs:1009` defines Runtime; launch/input/cancel at 1035/1041/1064. Preserve opaque runtime/session identities and capability refusals. No Fleet runtime-name branches. Do not import the AgentQ application. |
| Configuration | New Fleet resolver | `adapter.rs:178-181` accepts MCP and instruction paths; `:224-228` constructs Launch with runtime defaults. These are launch inputs, not Fleet canonical-file resolution, mandatory requirements, role inheritance or actual preparation evidence. |
| Instructions | Extract mapping, Fleet records prepared content | The current mapping accepts the resolved file through Launch.instructions. Retain exact content, source revisions, precedence and digest before launch; a mutable path alone cannot prove what the harness loaded. Roles remain ordinary supplied instructions, including taskless PM sessions. |
| Skills | New explicit materialization | Launch's inspected preparation surface does not establish a Fleet skills resolver. Resolve canonical skill references and dependency availability into the execution workspace using native supported discovery; qualify actual loading before claiming support. Do not silently flatten skills into a prompt. |
| MCP | Adapt existing native mapping | Launch.mcp joins native tool preparation (`adapter.rs:178`). Fleet supplies only granted tools with execution-scoped credentials and retained non-secret metadata. A config path is not proof of connection, authority enforcement or tool use. |
| Readiness and preparation | Adapt, repairs off by default | `preflight.rs:39,51,56,68,78` gates readiness and builds native commands/home. Codex `registry/codex.rs:665-685` reads config layers and can write project trust when repair.workspace is enabled. Preparation must expose proposed writes; Fleet start must not inherit repair consent. |
| Launch | Adapt existing Runtime path | `adapter.rs:1529` delegates to registry table start; native transport owns flags. `stdio.rs:38-40` reaches owned spawn. Persist Fleet dispatch acceptance first; retain native identity/start separately. This path alone does not implement durable cross-host dispatch. |
| Input | Adapt, add Fleet command receipts | `adapter.rs:1533` handles runtime input; `:829` starts a native text turn. Active-turn steering is a separate control, not interchangeable input. `control.rs:61-87` retains sent/failure/retirement and uncertainty. Fleet must distinguish queued, written, observed and unknown, retaining original command identity. |
| Stop | Adapt owned cancellation | `adapter.rs:1666`, `agent.rs:448`, CoreQ `src/process.rs:22-34` provide cancellation and creation-identity-aware termination. Observe exit independently; cancellation acknowledgment cannot complete Fleet cancellation. Preserve partial results before cleanup. |
| History | Extract history interface/mapping; new retention integration | `adapter.rs:1017-1024` routes session enumeration/history/aliases. Keep native parsing behind the selected adapter boundary. `:486` explicitly returns unknown archive state without shipper receipt. Fleet needs durable retrievable history and cannot infer archived delivery. |
| Ownership/recovery | Adapt, not durable coordinator replacement | `agent.rs:357-385` checkpoints PID/start/log/scope; recovery is observation-only and rejects scope mismatch. `:405` binds session owner during poll. Preserve session/account authority; executor restart must reconcile rather than launch again. Full cross-host command recovery remains new Fleet code. |
| Protected host | Do not select for first slice | `host.rs:57-109,189,238-262` requires artifact/policy/preparation certification and bound authority; platform native launch appears at `:445`. This is a separate protected-host contract, not a portable executor transport or generic prerequisite for Fleet. |
| Process primitives | Reuse identity; bounded exchange only where appropriate | CoreQ `src/process.rs:22-34,306-316` supplies identity/termination and bounded helper I/O. Hidden limits at `:8-11`: 16 concurrent exchanges, 1 MiB input/output and 8 MiB response. Do not make these Fleet-wide admission or session-stream limits. |
| Resources | Reuse self sampler; new execution attribution | CoreQ `src/resources.rs:34-79` measures calling process and its storage, with unknowns. `:79-93` watches every 30 seconds; storage refresh is 300 seconds (`:44`). Executor self CPU/RAM is not native child-tree usage or per-execution attribution. |
| Cleanup | Adapt owned process cleanup; new workspace policy | `stdio.rs` owns frontend transport; `agent.rs:405-448` polls/freezes/stops. Fleet must separately own workspace, artifacts and preview leases. Never remove results merely because a harness turn ended; never kill by bare PID. |

## Dependencies and hidden policy

AgentQ runtime manifest identifies `agentq-runtime` version `0.261108.8`, Rust edition 2024.
It pins CoreQ tag `v0.261108.3`; inspected CoreQ HEAD is a separate evidence point, not proof of that dependency's exact source.
Dependencies include serde/serde_json, ring, toml `=1.1.8`, bundled SQLite through rusqlite and Unix libc.
Resolve and record actual locked revisions before adopting; no dependency was changed here.

AgentQ contract assigns native ownership to its library; CoreQ removed `coreq::agent`.
These describe prototype ownership, not Fleet dependency selection. The user explicitly overrides specq's existing AgentQ ownership convention for this assessment.
Retain one native adapter boundary, abstract permissions, MCP product tools and explicit unsupported/unknown outcomes; its implementation need not be current AgentQ.

Native account/home selection, readiness modes, transport capabilities and repair settings are consequential launch policy.
Record their actual effective values in preparation; do not allow ambient host configuration to silently broaden grants.
Codex descriptor (`registry/codex.rs:19`) advertises app-server controls and explicitly unsupported background operations.
A long-lived executor can own its process; a descriptor does not establish client-independent recovery.
No board, issue label, fixed PM/worker/reviewer type or acceptance workflow belongs in the executor.

## Implementation alternatives

| Option | Concrete dependencies and risks | Assessment |
|---|---|---|
| Wrap current library | Fastest access to Runtime controls/history, but inherits the full manifest above, registry/preflight/account/session-store coupling and native transport defaults. Requires proving repairs stay disabled, configuration is inspectable and Fleet recovery can retain authority independently. | Viable fallback if extraction closure is too large; source shape alone does not justify adoption. |
| Extract relevant native mechanisms | Start with one harness's mapping, stdio/control/history and process identity paths cited above. Internal types, ownership/session store, registry dispatch and CoreQ telemetry calls form a dependency closure that must be measured before copying; separate retained-content handling from metadata. Fleet owns upstream tracking and native-version maintenance. | Recommended bounded implementation direction: preserves inspected control/uncertainty behavior without adopting unrelated product policy. Not a claim that these files copy independently. |
| New native implementation | Needs native protocol/CLI integration, permission mapping, process ownership, question occurrence binding, history decoding and recovery. Smaller initial manifest is possible, but all observed edge cases must be reimplemented and qualified. | Use for a demonstrated extraction mismatch, not merely to reduce package count; largest native correctness burden. |

The recommendation follows the observed useful control paths and the preparation/resource mismatches, not repository names or shared ownership convention.
Before implementation selects an extraction set, trace its imports and lockfile closure for one harness; keep a wrapper as the explicit alternative if that work shows extraction costs more than isolating existing policy.

## Minimal implementation boundary

Implement one Fleet host executor with one native adapter boundary; prefer bounded extraction as assessed below.
Coordinator owns canonical definitions, grants, assignment/execution/session association, durable command identity and admission.
Executor owns preparation snapshots, workspace lease, native handles, local command/event journal, resource observations and result retention.
The selected adapter owns native configuration mapping, launch, controls, history and native permission enforcement.
Extract or reuse qualified process primitives only; avoid onboarding, update, enrollment or service machinery.

Use one selected harness on one authorized host first; select it only after inspecting the complete example configuration mapping.
Host protocol carries dispatch/input/stop, durable receipts, ordered events and artifact references.
Keep protocol independent of native Runtime types and transport; preserve opaque native identities inside executor records.
Executor acknowledges accepted commands only after local persistence, deduplicates retries and retains undelivered observations.
A lost launch response becomes reconciliation work, not permission to relaunch.

## Qualification gates and unresolved gaps

1. Static fixture: canonical definition plus project/task requirements resolves to exact instructions, skills, MCP tools and environment. Refuse machine-checkable incompatibilities; surface semantic uncertainty under Fleet rules rather than rejecting arbitrary prose conflicts. Record defaults and sources.
2. Fake executor: crash before/after acceptance, native spawn and receipt upload. Retry same identity; prove no second spawn and explicit unknown when reconciliation lacks evidence.
3. Isolated native launch, later authorized: record harness version/account/home, actual loaded instructions/skills, one granted MCP invocation and denial of an ungranted action.
4. Native interaction: send to intended session; demonstrate delivered versus queued/unknown, distinct steering behavior, duplicate refusal and taskless session support.
5. Native stop/failure: verify owned process and descendants terminate, unrelated process survives, partial history/files remain retrievable. PID reuse and owner loss refuse unsafe control.
6. History: page live and finished sessions through the native adapter; retain unknown records, source ordering and reconnect cursor; verify archive/result retrieval after client close and executor restart.
7. Resources: compare executor self samples with owned native-process/tree samples; retain unsupported CPU/memory/I/O/token/cost fields as unknown. Do not sum duplicate usage sources.
8. Cleanup/delivery: retrieve website sources, browser evidence and operator-accessible preview after agent exit; expire only declared leases after retention confirmation.
9. Remote extension: repeat acceptance/reconnect/stop across host disconnection and coordinator restart; pending stop remains pending until observed. Mac/Linux/Windows require separate evidence.

Implementation decisions to close: select the first harness and extraction dependency closure, command journal schema/reconciliation rules, result storage and preview ownership. These do not prevent starting fake-executor dispatch and recovery work behind the adapter contract.
Native qualification gaps: actual instruction/skill loading, permission enforcement, process-tree cleanup, history recovery and preview access. Child resource attribution and coverage remain explicit unknowns until measured; they do not block fake-executor implementation. Native dispatch must not be represented as qualified while these checks remain absent.
The concrete source paths support a bounded integration candidate; none of these native or distributed qualification gates is demonstrated by this review.
