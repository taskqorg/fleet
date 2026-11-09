# Laboratory runtime candidate

Debug executable SHA256: `13284ab6e0aa8d6e172ff60432c8868f29d26f3f032f4e76fef53fabb96f8e73`.

Stage 1 of [the approved experiment](lab-first-experiment.md). No deployment, launchd changes, website generation, commits or pushes occurred.

## Stage 2 contract

`contracts/lab-config.json` owns the exact schema and complete `payloadFixture`. Read `config/agents/baseline.json` and `config/agents/design-rules.json` as complete `agentConfig` objects. Preserve each profile snapshot and digest; use the same fixture task text, fresh Fleet session/execution ids and separate workspaces for both trials. Outer payload `model` and `effort` remain required by the existing command path; they must be `gpt-6.1-sol` and `low`. Profile instructions are applied as native developer instructions. Legacy payload instructions retain their old behavior; the fixture sets them empty.

Required profile keys: `id`, `model`, `effort`, `instructions`, `mcpServers`; unknown keys refuse launch. Only model `gpt-6.1-sol`, effort `low`, an instruction string up to 16384 UTF-8 bytes and zero or one `{id:"design-rules",data:{...}}` grants are accepted. Data is limited to 16384 serialized bytes. Profile ids are nonempty strings up to 80 UTF-8 bytes. The JSON Schema string limits describe character lengths; executor byte limits additionally apply for non-ASCII inputs. No browser executable, environment, URL or CLI flag fields are accepted. `data` is opaque tool-returned experiment content, not launch configuration.

Each configured session freezes its profile. Identical follow-ups resume normally; changed profiles, removing configuration from a configured session, or adding configuration to an existing unconfigured native session refuse launch. Payloads without `agentConfig` otherwise retain the existing adapter path. Historical profile snapshots are immutable preparation events.

Preparation retains requested content, compact sorted-key JSON SHA256, raw instruction UTF-8 SHA256, grants SHA256, mapped model/effort/instructions/grants and embedded server source SHA256. `applied` means native launch mapping was supplied; `availability` explicitly remains unknown until native call/result evidence. Native settings observations qualify actual application separately.

## Mapping and packaging

Inspected installed Codex 0.167.0 and AgentQ `runtime/src/agent/registry/codex.rs` plus `runtime/src/agent/adapter.rs::turn_thread`. AgentQ maps developer instructions and MCP server configuration to native thread settings. Fleet's existing CLI adapter uses per-invocation `-c developer_instructions=...` and `-c mcp_servers.design-rules.command/args/required=...`. No user configuration or account state is changed. Reference: [official configuration reference](https://learn.chatgpt.com/docs/config-file/config-reference).

The read-only newline JSON-RPC MCP server is Python standard library code embedded into the Rust binary, invoked as host-owned `python3 -c <embedded source> <serialized data>`. Package the rebuilt executor and ensure trusted `python3` exists on its host PATH. No Python packages or separate server deployment are required. Profiles and contract must be packaged with the coordinator repository assets. Server data travels only in host-owned MCP arguments and tool responses; it is not copied into instructions or AGENTS.md.

Existing child environment filtering, process incarnation cleanup, raw JSONL fsync, durable event outbox and artifact upload code remain in use. Named native call/result events are preserved without conversion. MCP processes follow existing descendant cleanup.

Bootstrap: the exposed Serena tool list omitted `initial_instructions`; the installed Serena MCP was initialized through its existing Python stdio client and the actual tool called successfully, with scoped shutdown and no global configuration changes.

## Checks and native evidence

- `cargo test --locked -p fleet-executor`: three passed, one explicitly ignored paid qualification.
- `cargo build --locked -p fleet-executor`: passed.
- `python3 tests/executor/lab_protocol.py`: initialize, notifications, tools/list, tools/call and unknown-tool refusal passed.
- `python3 tests/executor/fake_integration.py`: passed, including existing unconfigured path, live events, stop/abnormal-exit descendant cleanup, unrelated process survival, artifact retry/digest, restart unknown, secret exclusion, configured preparation, native-shaped call/result delivery and invalid profile refusal. Evidence: `/var/folders/sw/55r1dvwn2g30l77zs5nwp3_80000gn/T/fleet-fake-unqu3_mc`. Retained raw `data/sessions/lab/lab.native.jsonl` contains the tool name and result.
- Validation check covers forbidden executable fields, unsupported model, baseline without MCP, mapping and identical versus changed session profile.

Exactly one authorized native qualification ran: `cargo test --locked -p fleet-executor native_design_rules -- --ignored --nocapture`. Passed in 8.27 seconds. Scratch evidence: `/var/folders/sw/55r1dvwn2g30l77zs5nwp3_80000gn/T/fleet-lab-native-63962` (`preparation.json`, `native.jsonl`, `stderr.log`, `native-settings.json`). Thread: `01a1bdf1-7ec8-7e03-9ac5-3960e0a7d7eb`. Native item `item_1`: server `design-rules`, tool `get_design_rules`, arguments `{}`, status `completed`, non-null content containing heading, palette and layout. No website or shell action was requested or observed. Raw usage: 29224 input tokens, 25984 cached input tokens, 75 output tokens, zero reasoning output tokens; subscription allocation/cost unknown.

The corresponding retained native rollout reports actual model `gpt-6.1-sol`, reasoning effort `low`, approval policy `never`, workspace-write sandbox and disabled network. Profile instructions were found in a native developer message; the scoped extracted settings record retains that check. Native mapping was unchanged after qualification; subsequent preparation hash fields and deterministic tests were added without another paid call.

## Remaining acceptance

Parent owns coordinator/UI integration, profile packaging, deployment, public-browser baseline/configured website trials, instruction effect on HTML/CSS, preview and comparison persistence. The qualification proves actual tool invocation with this adapter, not deployed full-chain acceptance. The deterministic persistence fixture uses native-shaped events; it is not another paid call. The qualification's full native settings remain local; public runtime events retain the native exec stream, which does not itself include the complete settings record.

## Isolation reassessment — evidence only

Owner paused architecture and implementation decisions after stage 1. This follow-up changed no runtime code, profile policy, global configuration or account storage and made no model submission. The generic profile freeze remains in the current candidate; it is an implementation restriction, not an approved product principle, and should be reconsidered before integration. The earlier qualification proves invocation, not clean personal-context isolation.

Observed provenance, without private text:

- Native rollout `01a1bdf1-7ec8-7e03-9ac5-3960e0a7d7eb` contains a user instruction block with exact containment of the current `~/.codex/AGENTS.md` contents. It also contains the private Memory entrypoint reference. This establishes inherited personal instructions; it does not establish that Memory files themselves were read. No native tool call other than design-rules is present in exec JSONL.
- The native developer instruction block contains the requested profile instruction and a design-rules MCP namespace. Other developer blocks contain built-in harness/tool/role material. Their complete provenance was not established from exec output alone. No claim that all developer text comes from the profile is valid.
- Executor forwards personal HOME and CODEX_HOME after clearing other environment variables. In this worker environment CODEX_HOME equals the default personal `~/.codex`; config and auth files exist (contents not printed). The original qualification retained its rollout in that personal home.
- Installed `codex exec --help` explicitly defines `--ignore-user-config` as skipping `$CODEX_HOME/config.toml`, while auth still uses CODEX_HOME. `--ignore-rules` skips user/project **execpolicy `.rules`** files. It does not promise to suppress AGENTS.md. The retained personal AGENTS block confirms that distinction empirically.
- `developer_instructions` adds the supplied developer instruction; it does not replace all other instruction sources. Installed AgentQ maps instructions to `developerInstructions` in its thread parameters and maps MCP grants to `config.mcp_servers`. Its isolation extras disable selected capabilities; they do not provide a universal personal instruction/context boundary.
- A read-only fetch of upstream current `core/src/config/mod.rs` confirms distinct base-instruction, developer-instruction and project-doc size settings. Exact installed-version source was unavailable at the attempted public tag URL; upstream current source is supporting evidence, not proof of this binary's complete behavior. No daemon-inheritance conclusion was established. Top-level `--no-daemon` exists, but exec-local placement rejects it; it was not adopted.

Auth boundary: AgentQ `registry/codex/signin.rs::credentials` reads effective `cli_auth_credentials_store` through native `config/read` before treating `auth.json` as authoritative. It explicitly refuses unverified/non-file credential-store modes for file fingerprinting. A fresh CODEX_HOME therefore cannot be assumed to retain subscription auth. No credential file was copied or symlinked, no auth contents were logged, and no new authentication mechanism was selected.

Resume compatibility: installed `codex exec resume --help` accepts configuration overrides and model selection. AgentQ sends the caller's parameters through `thread/resume`, including its developer-instruction/MCP mapping, and its tests cover supplied resume settings. These establish that a universal profile freeze is unjustified as a claimed native necessity. They do not prove which changed MCP/instruction settings this installed CLI applies on resumed turns. Old conversation/tool-result content also remains in resumed history; changing a current grant cannot erase knowledge from earlier turns. No new paid resume test was authorized. A future implementation should record requested and observed settings per execution and expose specific unverified or unsupported changes, while retaining fresh independent trial identities.

Alternatives for owner decision, not implemented:

1. Keep existing subscription home and explicitly disable instruction discovery/memory/skill/config sources supported by the installed harness; verify actual context provenance. Smallest operational change, but shared account state and readable personal filesystem remain.
2. Use a persistent dedicated harness home/account binding, with an explicitly approved native login or other supported credential mechanism. Clean config ownership and reusable repository state, without creating a VM per trial; authentication provisioning and refresh ownership require a decision.
3. Add an OS-enforced read boundary when private filesystem exclusion is a requirement. A task-owned workspace alone is not a security boundary: current workspace-write settings permit broad filesystem reads. Instruction hygiene and access isolation are separate guarantees.

Repository-aware tools can keep persistent repository data outside fresh trial workspaces, with explicit repository identity, ownership, setup and readiness checks. That is a later actual Serena scenario; no indexing/bootstrap framework or VM/container fleet was added. Before parent submits the integrated pair, owner must choose the desired instruction/access guarantees and auth ownership; the current stage-1 candidate demonstrably inherits personal AGENTS instructions.

## Current follow-up candidate and auth decision

Current follow-up removes generic session-profile freezing and both launch-path lock checks. Preparation events retain each invocation's requested settings and hashes; previous conversation history persists and changed-setting native resume compatibility remains explicitly unqualified. Baseline profile now has no tool-usage instruction; configured profile adds the read-only tool plus instructions to call it and apply heading/palette/layout. The payload fixture matches the configured profile. Old sessions/results were not deleted or migrated.

New deterministic checks pass (three Cargo tests, protocol check; paid test remains ignored), and locked debug build passed. Current binary SHA256: `ae6483f43b0d630162b8bd57ee493150ef708142b1982a98e400d055ae3de918`. No installed binary was replaced. No new native model call has been made. This candidate still uses the old personal-home mapping and MUST NOT be represented as meeting the newly approved configuration-independence boundary.

Auth inspection found a concrete supported alternative to copying a refreshable personal auth store: installed app-server generated schema exposes `account/login/start` with `chatgptAuthTokens`, and `account/chatgptAuthTokens/refresh` exchanges access token plus account id. Its account-read schema explicitly assigns token refresh to the external client. That would require an explicit private credential-provider/broker and app-server transport lifecycle; Fleet's current adapter uses `exec`, not that protocol. No broker was added and no token read or copied. CLI also offers `login --with-access-token` stdin, but its token mode and refresh behavior have not been established as a safe independent binding for the current subscription.

Smallest concrete setup alternative: owner performs a dedicated native subscription login into a persistent Fleet-owned home, outside Git:

```sh
mkdir -p "$HOME/Library/Application Support/Fleet/native-home" "$HOME/Library/Application Support/Fleet/codex"
chmod 700 "$HOME/Library/Application Support/Fleet/native-home" "$HOME/Library/Application Support/Fleet/codex"
CODEX_HOME="$HOME/Library/Application Support/Fleet/codex" codex -c 'forced_login_method="chatgpt"' login --device-auth
CODEX_HOME="$HOME/Library/Application Support/Fleet/codex" codex login status
```

This setup was prepared only, not executed. It creates an independent native authentication lifecycle, without copying personal auth files; owner must authorize/complete the interactive login. Runtime adaptation must then use the Fleet-owned HOME/CODEX_HOME and explicit settings, inspect generated context and verify the clean baseline/configured pair. Merely creating these directories is not qualification. Alternative: approve a bounded external-token app-server binding with clearly owned refresh instead of separate login. Choosing between these authentication lifecycles is the remaining decision; no secrets should enter reports, argv, repository files or public history.

## Ready for dedicated auth qualification

Approved implementation now requires explicit trusted host settings `FLEET_NATIVE_HOME` and `FLEET_CODEX_HOME`, each an absolute existing private directory (no symlink, mode has no group/other access). The child receives those as HOME/CODEX_HOME; the personal values are not forwarded. Missing settings fail preparation without falling back. The host must point them to the two Fleet-owned directories provisioned by parent; arbitrary browser values cannot set them. Subscription login remains native and independent; no personal auth data was copied.

The adapter additionally disables project document discovery (`project_doc_max_bytes=0`), skills and skill instructions, memories and hooks; it skips host skill discovery and starts with empty `mcp_servers` before profile grants. Config and execpolicy rules remain ignored. These are supported candidate settings, not yet proof of clean generated context. Each native workspace retains its explicit home binding. A pre-existing native session without that binding, or with a different binding, gets an explicit preparation failure; journals and historical results remain, and personal native history is not silently copied. This is a specific native identity/home compatibility limitation, not a configuration-profile lock.

Current deterministic result: four Cargo tests passed, one paid test ignored; build and fake transport/cleanup/artifact regression passed (`/var/folders/sw/55r1dvwn2g30l77zs5nwp3_80000gn/T/fleet-fake-j1b88vzb`). Current debug SHA256: `4dcbf22b006a52cfe739935168384b1199ecf1ad174fc9eca9e18efb15cfebce`. Native boundary allowance remains unused, pending parent confirmation of dedicated subscription login.

After parent confirms login, run once per variant, not as a routine test suite:

```sh
export FLEET_NATIVE_HOME="$HOME/Library/Application Support/Fleet/native-home"
export FLEET_CODEX_HOME="$HOME/Library/Application Support/Fleet/codex"
FLEET_LAB_VARIANT=baseline cargo test --locked -p fleet-executor native_design_rules -- --ignored --nocapture
# Take NATIVE_EVIDENCE from that single invocation:
python3 tests/executor/lab_provenance.py /absolute/evidence/directory baseline
FLEET_LAB_VARIANT=configured cargo test --locked -p fleet-executor native_design_rules -- --ignored --nocapture
python3 tests/executor/lab_provenance.py /absolute/evidence/directory configured
```

The provenance checker stores only booleans, thread identity and a rollout digest, not private instruction/auth text. It checks expected profile instruction, model/effort, private Memory pointer absence, personal memory directory absence, a synthetic-conflict marker absence and expected native tool evidence. The baseline check inspects generated context for absence of the design-rules namespace; it does not infer absence merely from an unused tool. Raw retained rollout remains private in the dedicated native home. No filesystem containment is claimed. Inspect any failed qualification before another model submission.

## Dedicated-home native qualification passed

Parent confirmed native dedicated-home `login status`: Logged in using ChatGPT. Exactly two newly authorized native calls then ran, using FLEET_NATIVE_HOME=`~/Library/Application Support/Fleet/native-home` and FLEET_CODEX_HOME=`~/Library/Application Support/Fleet/codex`, no personal auth fallback or auth copying. Both used installed Codex 0.167.0 and actual model `gpt-6.1-sol`, effort `low`, approvals `never`, workspace-write and disabled network as observed in retained native settings. No website generation, deployment, installed binary update or service restart occurred.

| Variant | Thread | Duration | Native observation |
|---|---|---|---|
| Baseline | `01a1be03-0e52-70b1-95ba-df1e60dbbe9d` | 6.32 s | Supplied baseline instruction present; design-rules namespace absent; no MCP calls |
| Tool plus instructions | `01a1be03-80dc-7de0-a723-e8bd7e50bad4` | 7.85 s | Supplied tool-usage instruction present; `design-rules/get_design_rules`, arguments `{}`, completed with non-null rule content |

Private evidence directories: `/var/folders/sw/55r1dvwn2g30l77zs5nwp3_80000gn/T/fleet-lab-native-26840` and `/var/folders/sw/55r1dvwn2g30l77zs5nwp3_80000gn/T/fleet-lab-native-28009`; directories were restricted to 0700, retained files to 0600. They retain preparation, native JSONL, stderr, privacy-preserving provenance and actual native settings. Full rollouts remain in dedicated CODEX_HOME. Rollout SHA256 values: baseline `139ceb7b2e67a3126b184ce7b79e8a9fadd36e6c519d75d57bcbeb9c2eb3573a`; configured `3a68a867ceef299620ed942ebbc5fc95b1c2e1db215c1323399ba4c968ee0755`.

Both rollouts lacked personal AGENTS instruction block, private Memory pointer, personal memory directory pointer, skills instruction block and hook context. Expected supplied instructions were observed. The baseline had no selected MCP namespace; configured call/result demonstrates availability and use of its selected grant. No unselected MCP declaration or invocation was observed. One `mcp__ologs__get_profile` string in both contexts is a generic built-in tool-documentation example, not an active declaration. Built-in tools and harness instruction blocks remain; they are not represented as Fleet-authored content. No synthetic conflicting personal fixture was installed for these calls: its marker-absence check alone is not evidence that such a fixture was suppressed. Actual prior personal AGENTS inheritance versus its absence in these clean-home rollouts is the retained comparison.

Raw native usage, scope one turn each: baseline input 13130, cached input 11520, output 6, reasoning output 0; configured input 26545, cached input 24576, output 78, reasoning output 0. Subscription cost allocation remains unknown. No statistical or model-adherence claim follows from this pair.

Qualified executable SHA256 remains `4dcbf22b006a52cfe739935168384b1199ecf1ad174fc9eca9e18efb15cfebce`. Native adapter source SHA256 `3bd36d83a2dd6aabfda28a7d3236132e93c63772a122bbf1c29e543915ad1091`; laboratory source SHA256 `211d42c1706d1d47c60fac0d134682330623ae5d162676bed4b8689b72d015c1`. Complete source/profile hashes are retained privately as `source-hashes.json` with configured evidence.

Packaging: trusted installed Codex and python3, rebuilt executor, explicit private Fleet-owned HOME/CODEX_HOME host settings and already provisioned independent native subscription login. Profiles and contract must ship with coordinator assets; embedded Python server ships inside executor. No credential belongs in repository, argv, public preparation or report. Existing old-home native sessions are preserved but cannot resume through a mismatched home binding; explicit preparation failure explains this limitation without moving personal transcripts. Current execution config changes are allowed, but application of changed settings on native resume remains unqualified and prior conversation content persists. Parent still owns integrated coordinator/UI, deployment and public website pair acceptance. The two-call allowance is now fully used.
