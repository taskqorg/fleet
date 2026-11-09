# Harness session comparison

Read-only source research, 2026-11-09. Recommendation for owner decision, not implementation approval. No tests, builds, native/model calls, services or authentication changes were performed. Live Fleet and successful/failed native histories remain unchanged. Codex app-server is a mode of the installed executable speaking a local protocol over stdio; using it does not require a remote server, broker or new deployed service.

## Sources and revisions

- AgentQ: `/Users/kirs/Projects/agentq`, HEAD `d0f6bfa795c0bc3f3b565e32e7056bad5a44809e`. Existing dirty `taskq.json` is outside the inspected runtime sources.
- Orca: `/Users/kirs/Projects/orca`, HEAD `0b93cef44508c0bda503e6a1063cf4bf1eed92be`; working tree reported clean.
- T3 Code official upstream: [pingdotgg/t3code](https://github.com/pingdotgg/t3code), linked by [t3.codes](https://t3.codes/). Read-only temporary clone `/tmp/fleet-t3code-research-20261109`, HEAD `d0f1f7200dce1229f75314b3d9eb1d7ac3abb607`. No dependencies installed or project commands run. Paths below are relative to these exact revisions; source/test presence is not a passing execution claim.
- Installed Codex 0.167.0 source/schema investigation and Fleet live proof remain in `continuation-instruction-resume.md` and `continuation-live-evidence.md`. The latter proves same-thread delegation and result delivery, not application of changed developer instructions.

## What each implementation actually offers

| Boundary | AgentQ | Orca | T3 Code |
|---|---|---|---|
| Fresh and resumed Codex | One AppSession state machine selects thread/start or thread/resume, then a correlated turn | One structured session path opens start/resume and uses its managed app-server connection | One provider adapter/client handles ensureThread and resumeThread through app-server |
| Current instructions | Ordinary turn adapter explicitly sets developerInstructions from a supplied file | Inspected thread-open helper forwards threadConfig and permission policy; it does not explicitly pass developerInstructions | Inspected codexThreadRuntimeParams supplies model/cwd/config/MCP; it does not explicitly pass developerInstructions |
| Changed model/tools | Typed thread and turn parameters; ordinary MCP conversion is restrictive and rewrites approval policy | Resume intentionally omits model to preserve saved settings; separate message-options layer handles changes | Shared runtime-parameter builder used for fresh/resume; optional explicit model and per-thread MCP bindings |
| Stop and uncertainty | Caller-owned process hooks, protocol correlation, checkpoint/recover, pending-request refusal | Interrupt handling, submission echoes, separate root/workload close proof and acquisition refusals | Effect-scoped transport, termination signals, provider interrupt/state/recovery and retained usage |
| Harness neutrality | Rust runtime descriptors and launch/observe/control/history interfaces for multiple native harnesses | Host-owned journal/lease and structured adapter interface; Codex-specific implementation behind it | ProviderAdapterV2 plus separate Codex and Claude implementations and provider-core contracts |

Neither Orca nor T3 Code's inspected resume call is proof that an arbitrary changed instruction profile reaches developer context. AgentQ has the explicit request construction Fleet needs; the actual installed native context delta still needs qualification.

### AgentQ: strongest reusable Rust mechanism, not a drop-in policy

`runtime/src/agent/adapter.rs:624-653` selects the automatic protocol path for declared instructions/MCP/parts/control requirements. `turn_thread` reads the instruction file into **developerInstructions**, separately from turn input. It validates MCP servers to command/args/env and adds **default_tools_approval_mode="approve" to each selected server**. Fleet currently needs its narrower request_child-only approval and explicit tool allowlist. Directly adopting this ordinary API unchanged would lose that distinction and cannot pass Fleet's current MCP policy through its accepted fields.

`runtime/src/agent/registry/codex/app.rs:81-146` implements initialized handshake, common start/resume, and turn dispatch. Its request method refuses pending/unknown requests. The same file's test around 759-790 checks explicit developerInstructions/permissions passthrough, foreign-thread refusal, recover binding, no repeated uncertain turn/start and exclusion of unrelated turn usage. These are useful inspected regression cases, not tests rerun here. Raw selected frames/history and checkpoint/report mechanisms are present; model/tool/usage projection must retain native scope rather than infer total cost.

Dependency closure is larger than that file: `app.rs` uses parent registry types, native/history/sampling code, archive JournalRecord, telemetry Store and private stdio module. `runtime/src/agent/stdio.rs` owns streaming readers/process lifecycle and accepts caller process hooks. `runtime/Cargo.toml` pulls coreq from Git tag v0.261109.1, serde_json preserve_order, ring, toml, bundled rusqlite 0.40.2 and libc. Fleet already has another SQLite/runtime ownership layer. The runtime crate is publish=false: adoption means a pinned repository dependency/vendor decision, not an assumed stable published package.

`adapter.rs:100-135,146-177` exposes abstract policy, explicit home/env, and pre-spawn/spawned hooks. Those provide integration seams, not proof that a default launch clears personal environment or preserves Fleet isolation. `registry/codex.rs:80-86` maps isolation/subscription flags; a Fleet bridge would still own dedicated HOME/CODEX_HOME, no API-key fallback, exact tool grants, process identity, durable admission and result routing. Claude and other harness behavior stays in separate registry implementations; adopting the common API does not make their native instruction semantics equivalent.

### Orca: useful acquisition and process-ownership reference

`src/main/codex/structured/threads/codex-structured-thread-open.ts` centralizes start/resume. It forwards cwd, permission policy, threadConfig and shell environment settings; omits model on resume deliberately; verifies returned native thread identity. An exact no-rollout refusal may create a replacement only when supersedeIfUnsaved is explicitly allowed. Active-writer conflict becomes an acquisition refusal. Fleet must retain its stricter no-duplicate/unknown rule rather than import fallback behavior blindly.

`codex/structured/turns/codex-structured-message-options.ts` and its test cover per-message changes; `codex-structured-turn-cancellation.ts` implements bounded interrupt with failure distinctions. `codex/app-server/codex-app-server-connection.ts`, connection-types.ts and process-tree-kill.ts separate request/notification/server-request handling, stdout draining, handshake and managed process close. Close proof distinguishes root exit from unproven workload termination; relevant tests include codex-app-server-connection-tree-unproven.test.ts. This is more appropriate lifecycle evidence than assuming Python wrapper exit means Codex and descendants exited.

`native-chat/agent-session-wire/structured-agent-session-adapter.ts` explicitly leaves journal/lease authority with the host and models delivery uncertainty, account home, acquisition, provider handles, live options and stop outcomes. Its router and provider modules keep Claude/Codex differences behind a product boundary. Reusing the whole Orca layer would import Electron/desktop application types, account/vault/journal and remote-host policy. Extracting correlation and process-close lessons is much smaller than importing its host framework. Inspected sources do not establish Fleet's exact developerInstructions override or dedicated-home behavior end-to-end.

### T3 Code: clean protocol package, substantial product adapter

`packages/effect-codex-app-server/src/client.ts` provides typed raw requests, notifications, inbound requests/responses, termination observation and child stdio binding. `_internal/stdio.ts`, protocol.ts, replay.ts and generated schemas separate transport from protocol. Its private package.json has an Effect dependency and generated code/tooling; it is not a small dependency-free Rust component. client.test.ts and _internal/stdio.test.ts are source test evidence only.

`apps/server/src/orchestration-v2/Adapters/CodexAdapterV2.ts:1406-1453` builds common cwd/model/config plus T3 MCP session bindings. Lines 7160-7225 use that builder for start and resume; around 6830, archived resume can unarchive and retry. This is specific product recovery, not permission for Fleet to retry uncertain turns. Its roughly eight-thousand-line adapter imports contracts, provider-core, managed runtime/auth, MCP session/port/workspace and orchestration services. Do not extract it wholesale.

Its MCP config installs authenticated T3 HTTP bindings and preserves other launch-argument MCP servers. That policy is unsuitable for Fleet's explicit selected-tools-only scenario. `provider/CodexProvider.ts:472-482` resolves optional CODEX_HOME into the process environment; it does not establish Fleet's cleared environment or isolated instruction policy. `provider/CodexTurnTokenUsage.ts`, `CodexUsageHistory.ts` and the adapter retain native turn usage/history; importing their reporting semantics requires a separate scope check. The ClaudeAdapterV2 uses @anthropic-ai/claude-agent-sdk with its own query/resume/close semantics rather than pretending Codex RPC is universal.

## Options and recommendation

| Option | Benefit | Cost/risk | Assessment |
|---|---|---|---|
| Adopt AgentQ runtime directly | Existing Rust session, lifecycle/history and multi-harness seams | Dependency closure, two ownership/storage layers, incompatible ordinary MCP approval mapping; public/raw protocol seam must be assessed | Best long-term candidate if its supported API can express Fleet's exact policy without bypassing authority; not presently a drop-in |
| Bounded AgentQ extraction | Reuse demonstrated handshake/correlation/recovery and preserve Fleet host ownership | Private modules depend on wider registry/archive types; copied code has maintenance and divergence cost | Preferred starting design study for a narrow Rust session mechanism, with explicit upstream ownership/extraction boundary |
| Thin new Fleet adapter | No unrelated package/runtime migration; exact current policy | Reimplements high-risk correlation, cleanup, event mapping, history and recovery; wrapper may misattribute resources | Accept only if bounded extraction is demonstrably larger; avoid an unqualified Python wrapper shortcut |
| Import Orca or T3 adapter | Rich product behavior | TS/Effect or Electron/product dependency and policy coupling | Use as behavioral references, not runtime dependencies for this Rust executor |

Recommend one Codex session mechanism for **both fresh and resumed sessions** behind Fleet's existing executor boundary, subject to owner choice and a concrete extraction/API assessment. Start versus resume should differ in the thread-open request, not duplicate the whole event/lifecycle/policy stack. A temporary dual exec/app-server arrangement reduces the first diff but doubles qualified paths and makes settings/events differ by session age. It is a migration option, not the simplest steady state. This recommendation does not authorize implementation or replacement of the working live exec path.

## Evidence still needed before choosing and shipping

1. Demonstrate the smallest usable AgentQ API/module boundary and compile dependency closure without inheriting ordinary server-wide approve; decide direct dependency versus an upstream extraction. Read-only findings here do not prove integration feasibility.
2. Agree exact Fleet policy translation: dedicated homes/subscription, disabled personal discovery, per-tool MCP approval, explicit developerInstructions, model/effort/cwd and supported native requests. Keep developer instructions separate from actual user/child input.
3. Qualify fake fresh/resume identity, actual instruction request payload, tool arguments/results, per-turn usage, server-request refusal, interrupted/failed/unknown outcomes, stdout drain, descendant cleanup and no turn/start replay after uncertain acknowledgement. Native CPU/RSS must cover the server or explicitly identify intermediary-only observations.
4. Parent then owns one bounded native changed-profile continuation: inspect an actual developer-role instruction delta, same native thread/workspace, exact current model/tool mapping and retained old/new evidence. Existing successful Fleet delegation and user-driven correction remain valid independently; they do not prove changed developer instructions. The ignored skills.enabled warning remains an observed limitation, not an effective restriction.

No approach is approved by this report. No tests/builds were run; native instruction application and comparative performance remain unknown. Only this report was written in the Fleet workspace.

## Refined reuse decision

[Minimal reuse boundary](agentq-reuse-boundary.md) incorporates the owner's clarification that AgentQ is a prototype reference, inspects public/private APIs and licenses, and replaces the earlier extraction-first preference with concrete options. It recommends a thin Fleet-native protocol mechanism informed by all three references when the priority is the current Fleet slice; an upstream extraction is a separate refactor, not an already available package. Owner selection remains required.
