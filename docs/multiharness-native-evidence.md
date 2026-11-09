# Multi-harness native qualification

2026-11-09. **Changed developer instructions on Codex resume failed acceptance.** Exactly two authorized native model turns were submitted; both completed, but the second used marker A again. No further model call or retry occurred.

## Scope and candidate

An isolated temporary executor data root and loopback fake HTTP coordinator exercised the actual Fleet executor, not a standalone protocol client. A private temporary host registry referenced the existing dedicated Fleet HOME and CODEX_HOME directly; auth was not copied or inspected. Executable: /usr/local/bin/codex 0.167.0. No installed Fleet binary, launchd service, live coordinator, existing thread or credentials were intentionally changed. New native history was naturally written by the two authorized turns.

Native-tested debug executable SHA256: `1afcdefc8c3775352064a88ff31f1e82910c9c06aa53b61dbaf4270185efdb97`. Before qualification, thread configuration effort was changed to derive from the requested payload instead of a hardcoded low; the allowed profile/turn requested low. That source change was present in the tested binary.

Temporary root: `/var/folders/sw/55r1dvwn2g30l77zs5nwp3_80000gn/T/fleet-native-multiharness-6kcz5pwr`.

Retained evidence: [summary](../evidence/multiharness-native-20261109/summary.json), [A request](../evidence/multiharness-native-20261109/request-A.json), [B request](../evidence/multiharness-native-20261109/request-B.json), [A native frames](../evidence/multiharness-native-20261109/A.native.jsonl), [B native frames](../evidence/multiharness-native-20261109/B.native.jsonl), [normalized events](../evidence/multiharness-native-20261109/events.json), [native developer/context extraction](../evidence/multiharness-native-20261109/native-context-proof.json), [artifact readback](../evidence/multiharness-native-20261109/artifact-readback.json). Original JSONL and durable journal remain in the temporary root; the new native rollout remains in the existing dedicated Codex store. No authentication file is included in these artifacts.

## Actual results

| Observation | Fresh A | Resume B |
|---|---|---|
| Fleet execution | qualification-A | qualification-B |
| Native thread | 01a1be51-c6cf-7430-85be-4745759a09bf | same |
| Native context model/effort | gpt-6.1-sol / low | gpt-6.1-sol / low |
| Workspace | temporary data/sessions/qualification-session | same |
| Lifecycle | completed | completed |
| Raw incoming frames | 45 | 48 |
| Completed native local commands | 2 | 2 |
| Artifact marker.txt | A | A — expected B |
| Developer marker observed in native rollout | A | no B delta |
| Final assistant answer | A | A |

Both retained thread-open responses confirm the requested cwd/model, low reasoning effort, never approvals, workspace-write and networkAccess=false. The task required only local write/read/check and no network/subagents; no such network/subagent tools were observed. Each completed receipt was emitted by the actual executor branch requiring native root exit status success, native completion, drained stdout EOF and no unresolved cleanup. Thus native turn completion and owned process/stdout closure passed for these two executions, independently of instruction acceptance.

The second accepted command and preparation contain B; the source constructs developerInstructions for both opens. The retained native rollout contains A as developer content, two turn_context records with the same model/effort/cwd, no B marker, and an assistant statement that it will write A in the second turn. Both artifact snapshots confirm A. Outgoing protocol wire frames were not separately captured, so these artifacts do not establish a lower-level native root cause. Request/schema construction is not actual developer-context application. No automatic replacement, third turn, replay or retry was attempted.

Raw SHA256: A `75d97fa1c855096c778e50ff27098268c81077db971af3530fe42613ddb84dee`; B `f11082223c24bc6e4510408cba0a7743e9b4597492cb9ca7d725809ac81e1dfb`.

## Usage/UI correction after native calls

The native sample is thread/tokenUsage/updated with separate total and last objects. Final A total: inputTokens 26752, cachedInputTokens 24704, outputTokens 120; final B total: inputTokens 53971, cachedInputTokens 49536, outputTokens 233. These are retained cumulative native totals, not two independent turn costs. They are not summed. Last is not promoted to whole-turn usage.

Shared projection now supplies numeric snake-case counters from total, retains nativeUsage, and labels this known shape native thread cumulative total/non-additive. Other scopes remain unknown. UI retains the latest numeric usage when turn.completed has no usage, rather than erasing it, and never stringifies nested counters as [object Object]. Focused Rust and browser checks use the observed B counter values followed by an empty terminal event. Original raw samples remain unchanged. These deterministic corrections were built after the two calls; no further native qualification of the final binary occurred.

## Readiness without credential access

Installed CLIs: Codex 0.167.0, Claude Code 2.1.319, agy 1.5.3. Native account metadata was read through `orca account list --json`; token values were neither read nor printed. Only matching private AI Agents index/Codex/Claude notes and public source wiring were inspected.

- Codex: dedicated Fleet account completed both authorized calls. Orca reports zero managed Codex account aliases; host-default metadata reports hasAuth=true/authKind=oauth. A distinct second Codex account was not found/provisioned through this host integration. This does not prove the person lacks another account elsewhere. Supported explicit provisioning command: `orca account add --agent codex` (device authorization), or native `codex login` in an operator-chosen dedicated home. Neither was run.
- Claude: Orca reports zero managed aliases. Native `claude auth status --json`, filtered to readiness fields, reports loggedIn=true, authMethod=claude.ai, apiProvider=firstParty for the host default. This advisory host-default state does not establish Fleet-isolated auth, quota or account/config independence. Supported operator commands: `orca account add --agent claude` or dedicated-home `claude auth login`. Neither was run. Claude paying turns remain deferred.
- Antigravity: Orca reports zero managed aliases, a current host account present and usageStatus=ok. Alias for this evidence is host-current only; quota availability is not isolated Fleet authentication proof. Orca account-add help says Antigravity accounts are saved from the current agy login in Settings > AI Provider Accounts; its add command does not support agent=antigravity. AgentQ registry/antigravity/signin.rs implements browser authentication through agy acp on the native default store, explicitly refuses explicit-home sign-in with login-home-isolation-unsupported, and has no verified login-status command. No supported isolated-home provisioning route was established here; no login, token read, fingerprinting, auth copy or model turn was performed.

Required next decisions/actions: repair and requalify changed Codex developer-context application under a separately authorized native budget; provision a distinct second Codex account explicitly; establish supported dedicated Claude and Antigravity account routes without personal behavior inheritance. Successful lifecycle/tools/history/usage observations remain valid; universal account/config independence and current developer instructions on resume remain unproven/failed as described above. No deploy/restart/commit/push occurred.

Final deterministic candidate: executor SHA256 `41ba657968f9400ca4b4e0ce5bb58437bdb36d22410dc74900076a30491680e1`; packaged UI SHA256 `33ed7b17cc308122766c22f4f5921aaea4b2bfb3d756dbef4ee40e1b84afac99`. `cargo test`: 13 passed / 1 native test ignored; Rust/coordinator builds, focused numeric-usage browser replay and git diff --check passed. The regenerated desktop screenshot was visually inspected: input 53971/output 233 remain visible after the terminal event, without nested-object text. Dispatch code changes are limited to requested-effort mapping, numeric usage projection and UI retention/display plus their focused checks; docs and retained evidence record the actual failed native criterion.

Follow-up: [source-backed resume repair and private outbound evidence](multiharness-resume-repair.md) implements acknowledged developer-role injection and actual-pipe fake checks; it made zero native calls and does not change this failed native result.

Subsequent separately authorized [single repaired native resume](multiharness-repaired-native-evidence.md) passed B acceptance on candidate 5eece6b; it preserves the earlier failure/source investigation as historical evidence.
