# Multi-harness prototype candidate

Owner request: «давай сделаем уже какой-то прототип универсального ... способа запускать разные хардесы».

The integrated local candidate uses Fleet's existing commands, journal, sessions, lab, delegation, artifacts and identity-fenced cleanup. It adds a generic trusted-profile selector and independent named host-account field to the ordinary session UI. Existing profiles and historical Codex events retain compatible defaults and rendering. No deployed installation or historical evidence changed.

Native qualification update: the two-turn candidate test in [native evidence](multiharness-native-evidence.md) completed both turns on one thread, but resumed marker B did not enter developer context and the model repeated marker A. Current request construction is not proof of effective changed developer instructions. Do not accept that native requirement as passed.

Repaired native follow-up: [one authorized resume](multiharness-repaired-native-evidence.md) on candidate 5eece6b passed actual developer-role B, B artifact/final answer and completed lifecycle. Earlier failed A/B evidence remains valid for its old candidate. Broader multi-harness/account readiness is not established by this bounded result.

## Execution contract

Profiles supply harness, model, effort, instruction role/content and supported native tool grants. Accounts supply identity/authentication homes through host configuration only. Model, harness and effort conflicts are refused. Unmapped requested settings/environment are refused, never silently dropped. No credential copying, API fallback, cross-account resume, automatic replacement or replay after unknown outcome.

- Codex: local installed executable in app-server stdio mode; both fresh and resume use initialize, thread/start or thread/resume, then turn/start. Current instructions use explicit developerInstructions on both opens; task input remains separate. Existing CLI configuration overrides, exact MCP grants and never-approval policy carry into the local executable invocation. Native inbound requests are denied. Requested thread identity is checked; thread/turn notifications are correlated. No retry after a lost/failed acknowledgement.
- Claude: explicit system prompt, model, private HOME/CLAUDE_CONFIG_DIR, setting sources disabled, strict empty MCP configuration and configured native tools/allowedTools. The included worker profile grants Read/Edit/Write/Bash/Glob/Grep under dontAsk, without permission bypass. This is not a claim of Codex sandbox parity. Claude design-rules and scoped same-harness delegation grants are implemented; Antigravity stream MCP/delegation remains unavailable.
- Antigravity: actual agy stream-JSON stdin user frame, private HOME/GEMINI_HOME, model and optional conversation ID. Profiles explicitly declare instructionRole=user; instructions and task remain separate recorded fields and map to labeled user content. Explicit system/developer requests are refused. The included profile uses this declared user role. Tools/permission handling remain native; explicit unsupported tool allowlists are refused. This mapping is supported by inspected source/help, not proof that no other instruction mechanism exists anywhere.

New profiles are `claude-worker` and `antigravity-worker`. Their explicit model codes require parent qualification against the provisioned accounts; no catalogue availability is claimed. Existing Codex profiles are not migrated. Methodology remains profile instructions rather than Fleet policy.

## Account boundary

FLEET_NATIVE_ACCOUNTS names a private executor-owned JSON file. Its named entries contain {harness, home, nativeHome, executable}; all paths are host configured, executable is absolute, homes are private directories. Payloads/UI contain account names only. Two Codex entries can use the same profile with different provisioned homes. Omitted account on Codex preserves FLEET_NATIVE_HOME/FLEET_CODEX_HOME/FLEET_CODEX_BIN compatibility. Non-Codex needs an explicit named account. Resume binds harness/account/homes; UI refuses changing native harness/account.

No registry or authentication was provisioned here. Host configuration ownership does not prove account readiness or complete native configuration independence. In particular Antigravity's settings/discovery behavior under dedicated homes remains a native qualification requirement; user-role instruction mapping does not grant personal settings inheritance.

## History and reuse

Common fields include harness/session, text messages with role, tools with identity/name/arguments/result/status, lifecycle/error, and native usage with explicit unknown/non-additive scope. UI consumes these fields; original native records remain in JSONL and retained event payloads. Legacy Codex events still render. Cumulative counters are not summed or universally labeled turn usage. CPU/RSS coverage remains the native root process observation, not all workload descendants.

Behavioral references only, no source copied: AgentQ d0f6bfa795c0bc3f3b565e32e7056bad5a44809e runtime/src/agent/registry/{codex/app.rs,claude.rs,antigravity.rs,antigravity/parsing.rs}, agent/adapter.rs and acp.rs; Orca 0b93cef44508c0bda503e6a1063cf4bf1eed92be structured Codex open/correlation patterns. Kept out: AgentQ archive/telemetry/policy state, automatic MCP approvals, personal auth copying, replacement/recovery policy and Orca product framework. Fleet directly owns the native app-server child; no intermediary wrapper or new remote service.

Installed offline evidence: agy 1.5.3 help declares stream-json, conversation, model, settings and native modes; AgentQ acp.rs validate_launch explicitly refuses supplied instructions at that ACP boundary. Codex 0.167.0 generated v2 schema contains developerInstructions on both ThreadStartParams and ThreadResumeParams, and expected turn fields. Schema generation and help/version did not start model turns.

## Local acceptance and parent qualification

Deterministic checks cover native account routing/fake Claude/Antigravity processes, excluded secrets, stdin framing, native resume identity, developer instructions fresh/resume, correlated versus foreign turns, pending duplicate suppression, inbound request refusal, tool/message projections, profile-role refusal, generic API/UI selection and common history. Existing fake executor integration exercises app-server framing plus live observations, owned stop/descendant cleanup, unrelated process survival, artifact retries and restart unknown without replay. Coordinator tests retain lab/delegation/default website behavior. Desktop/mobile screenshots are visually inspected.

Parent must provision each account, verify CLI/model availability and quota, then qualify bounded Codex/Antigravity native calls and Claude when quota permits. Check fresh/resume current instruction role/content, effective tools/configuration, same account/session, raw frames, completion, scoped usage and stop. Protocol tests prove request construction, not actual model context application. Existing paid/live successful and failed evidence remains unchanged. No paid calls, deployment, service restart, auth/global change, commit or push occurred.

Final local evidence: `cargo test` 12 passed / 1 native test ignored; `npm test` 8 passed; Rust and coordinator builds passed; ordinary browser, multi-harness browser, lab browser, fake executor app-server lifecycle, lab MCP protocol, durable delegation transport and installed per-tool approval config parsing passed. Visual inspection covered ordinary desktop and multi-harness desktop/mobile plus lab desktop. Screenshots: evidence/multiharness-{desktop,mobile}.png and evidence/lab-desktop.png. CLI versions observed offline: Codex 0.167.0, Claude Code 2.1.319, agy 1.5.3. Native auth, quotas, chosen Claude/Antigravity model availability, account/config independence and actual resumed developer-context application remain unverified.

Final SHA256: local debug executor `f7ac24fa245b4991ebc1e13d45527a40a097022216f55938e4c2af82d2ff9c11`; packaged coordinator app.js `ebdfe59eb397538d4a37015b0d81948e9be368879ed5e562a774133f199dce03`; ui.js `330f52c71d0d6ba3b159dee899833d0c6d3b12b94588d9da14c297be25fce3bb`; lab.js `5118632ceb29e6143e4df3c7250e14023984acb01ede1b6fc8aa22930f90cef5`; lab-ui.js `df943016633ef8d65e31f0a326ce0e0cb869ed05a149662745cb4550ea9234bb`. No commit hash exists for this dirty candidate.

## Resume repair contract

For a resumed Codex execution with nonempty supplied instructions, append the exact current text through supported `thread/inject_items` as a developer-role Responses message after thread open and before turn/start. Preserve native history and identity; do not edit rollout files, clear context, create replacements or substitute user input. Await injection acknowledgement; failure/unknown blocks turn submission without replay. Native qualification of effective B still needs a separately authorized model budget. Retain private mode-0600 outbound JSONL before writes and expose only method, numeric request ID, byte count, hash and transport-write status in common history.

## Remaining native grants/onboarding slice

Claude profiles may select existing Fleet design-rules and scoped request_child MCP servers. Use strict explicit native MCP configuration, exact MCP tool names and dontAsk; retain Fleet journal/grant/result authority. Same-harness child executions inherit the explicitly selected parent account and retain that binding; cross-harness account substitution is refused. Antigravity stream mode exposes only evidenced native mode/sandbox/slash-command flags in this slice; ACP mcpServers support exists but is not integrated into Fleet's stream adapter, so MCP/delegation requests are explicitly unavailable there. Prepare private unready account entries only; no auth/login flows, auth copies, personal config imports or model calls.

Current follow-up: [native grants](multiharness-native-grants.md) and [concrete account onboarding](multiharness-account-onboarding.md) describe the newer deterministic candidate and explicitly unprovisioned accounts.

## Installed Antigravity ACP slice

Use installed agy 1.5.3 ACP with AgentQ/T3 session setup and event shapes. Explicit host-registry native-current credential authority may use its owner-controlled host HOME (not necessarily private); behavior HOME/nativeHome remain private and ACP receives explicit --gemini-dir, relative --app-data-dir and --include-default-customizations=false. No login/authenticate, auth copying, forced file-storage or personal skill links. Require advertised model/mode and confirmed setter before a single prompt; refuse missing controls. Supplied instructions retain explicit user role. Fleet retains durable admission, raw/private outbound evidence, unknown/no-replay and owned-process stop/cleanup. A separately authorized single fresh qualification may run only after native non-generating setup and fake checks pass.

Current Antigravity path is installed ACP, not the earlier stream prototype: [implementation and one actual qualification](antigravity-acp-implementation.md). That report supersedes earlier stream MCP/credential-HOME blockers for the explicit native-current/private-behavior binding, while preserving their evidence.
