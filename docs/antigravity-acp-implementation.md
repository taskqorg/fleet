# Installed Antigravity ACP: implementation and one native qualification

2026-11-09. **One fresh native qualification passed.** Fleet now launches installed agy 1.5.3 through its supported ACP protocol. This is a local native executable mode, not a downloaded runtime, service replacement or new authority framework. No extra model turn, resume test, login/authenticate call, account switch, auth export/copy, deployment/restart, commit or push occurred.

## Existing mechanisms reused

- AgentQ d0f6bfa: runtime/src/agent/acp.rs initialize, explicit cwd/MCP setup on new/load, selected-model control, correlated prompt terminal and update/history shapes. AgentQ already had these mechanisms; Fleet's earlier stream choice used too little of that boundary.
- T3 Code d0f1f720: AntigravityAdapterV2.ts, AntigravityAcpSupport.ts, provider-acp/AcpSessionRuntime.ts and AntigravityProtocol.ts supplied the actual advertised resume/model/MCP/update cases. Fleet keeps its existing journal, grants, results and owned-process cleanup rather than importing Effect/product state.
- Orca 0b93cef4: native-account-launch.ts credential-HOME authority and the separate behavior-root/environment boundary. Its selected-account switching, managed runtime download, forced file-storage credential flow and personal skill links were not imported.

No upstream implementation source was copied verbatim; protocol/behavior shapes were adapted into the small Fleet state machine. No new dependency/framework was added. Source provenance is also in antigravity.rs. No upstream license-bearing file was vendored.

## Launch and protocol contract

Trusted registry entry fleet-antigravity-current explicitly declares credentialAuthority=native-current and owner-controlled host credentialHome. Private home/nativeHome requirements remain for behavior state; only this Antigravity authority can use a non-private but owner-controlled/non-writable-by-group-or-others credential HOME. Host HOME was not chmod'ed. Caller/model payloads cannot supply credential paths or environment values.

The installed command is agy acp with explicit --gemini-dir=<private nativeHome>, **relative** --app-data-dir=antigravity-cli, --include-default-customizations=false and requested --sandbox=true/false. Native rejects absolute app-data-dir; this was found and corrected during non-generating preflight, before spending the model call. No AGY_ACP_FORCE_FILE_STORAGE or personal skills/config links are supplied. The old private-HOME alias remains unprovisioned; only the explicit native-current alias was added/admitted and annotated with this result. Other account entries were preserved.

initialize requires protocol 1/version 1.5.3. Fleet selects session/new, or advertised session/resume with load fallback only when load is advertised. It supplies exact per-session MCP descriptors from existing trusted Fleet scripts. The requested model must appear in the returned catalogue; session/set_model and requested session/set_mode must acknowledge success before the sole session/prompt. Missing models/modes/refused or uncertain controls do not cause substitution or prompt replay.

The profile now explicitly selects observed gemini-3.8-flash-low instead of absent gemini-3.1-pro. Separate effort is refused in this ACP slice; the advertised variant selects native family/level. Settings include sandbox, mode and mandatory disabled default customizations; obsolete stream-only slash-command settings are refused, not silently dropped. The profile and schema keep explicit user-role supplied instructions, separately recorded from task text and labeled in native prompt input. Although installed handshake advertises systemPrompt.append, this profile does not request/use it and no stronger-role parity is claimed.

Native client file/terminal capabilities are not advertised, avoiding accidental host-side client operations. Unhandled permission requests are cancelled; no broad approval policy was imported. Existing scoped design-rules and optional Fleet delegation descriptors can be registered per session, but native delegation/permission acceptance is not qualified by this test. Fleet stop still proves owned native-tree termination and retains partial history; protocol-level graceful cancel is not claimed. ACP EOF before terminal is unknown with no replay; an intentional verified owned stop remains stopped.

Original incoming frames and private mode-0600 outbound intent bytes remain retained. Common messages/tools retain native IDs, arguments/results when supplied, lifecycle/errors and numeric usage with native scope. Native omitted fields stay unknown. Model/mode/capabilities updates remain available in native evidence.

## Before the model call

Installed help plus actual non-generating initialize/session/new/set-model/set-mode verified the explicit private behavior flags, version, existing native-current auth, exact Fleet MCP descriptor and selected controls. No authenticate/login or session/prompt was sent by these probes. The selected model appeared in native availableModels. Host current credentials were used natively with host HOME; private behavior roots were explicit ACP arguments, not an inference from an empty MCP list. No personal customization linking/copying was done.

Meaningful fake checks passed through the actual executor/external fake executable: new and advertised resume, model/mode before one user-role prompt, real existing read-only MCP transport, permission refusal, text/tool/results/usage projection, EOF unknown without duplicate prompt, private wire retention and owned stop with partial history. A strict retained-event sequence check also passed. Focused ACP/harness/profile tests, Codex actual-pipe and Claude MCP regressions, coordinator profile/delegation checks, build and browser flow passed. No broad native suite was run.

## Actual one-turn readback

[Sanitized proof](../evidence/antigravity-native-20261109/sanitized-proof.json) contains only test fields, native session/authority/model metadata and hashes. Full raw bodies, the new native conversation DB and brain state remain private.

| Observation | Actual result |
|---|---|
| Native-tested executor SHA256 | 1461d15342a3dd006ec73e4ea36fdf3b546c133b7322938151b7740025efc484 |
| Installed agy 1.5.3 executable SHA256 | 5e4b545bff4a83a93354fc50c54631cf3e8cd02e1a469087af00929f3fdc4d51 |
| Model turns | exactly one session/prompt, no retry or second turn |
| Execution/native session | antigravity-native-one / ffccb80a-d923-4890-8376-6252e4e19df6 |
| Authority | fleet-antigravity-current, native-current host HOME; no isolated Google principal asserted |
| Behavior/history | native conversation DB and brain under private nativeHome/antigravity-cli |
| Requested model | gemini-3.8-flash-low |
| Actual native config update | model=gemini-3.8-flash, thought_level=low, mode=accept-edits |
| Native input role | explicit user-role profile instructions plus separate task text |
| Observed tools | one design-rules MCP call, native file edit and native file read, all completed |
| Local artifact/readback | exact 14 bytes AGY_NATIVE_ONE; final assistant text AGY_NATIVE_ONE |
| End state | stopReason=end_turn; completed receipt after native root success/stdout EOF/resolved owned cleanup |
| Permission callbacks | zero in this native test; no blanket approval was used |
| Raw incoming frames | 22 |

Native MCP client events expose only toolSummary and completion, not structured call arguments or the full result. These common fields remain unknown rather than fabricated as {} or copied from requested configuration. Independently, the selected native conversation step 2 contains the **exact configured design-rules JSON**; its payload hash is retained in sanitized proof. This establishes delivery in retained native history without adding an unverified protobuf decoder. The wire omitted the native file-read result as well; direct artifact bytes and completed read observation provide the readback. File edit diff content is retained by common projection.

Native prompt-result usage: inputTokens 31012, outputTokens 223, cachedReadTokens 4046, cachedWriteTokens 0, thoughtTokens 0, totalTokens 35281. Native cache categories are kept distinct; no Codex-style cache-subset assumption, total recomputation or repeated-event summation is made. CPU/RSS coverage remains the native root observation, not full language-server/MCP workload accounting.

Private qualification root: `/var/folders/sw/55r1dvwn2g30l77zs5nwp3_80000gn/T/fleet-antigravity-native-one-frcj6p3k`. Incoming SHA256 `8afcbcf28ff1c911e7a033106c88806cab463c9fa0b835f0fc026fbada0ce3ba`; outgoing `8a921dfad99b5a404b40b7dba708ecff8448eafe5cd6a38f0a0b2cf032a14930`. The native-tested binary is preserved there as fleet-executor-1461d153. Earlier accepted Codex binaries/evidence remain preserved in their qualification roots.

## Final deterministic corrections and remaining qualification

After the one model call, focused actual-sample checks added separate cachedRead/cachedWrite/thought counters, kept diff content when rawOutput is empty, and merged duplicate non-Codex preparation records into one sequence-valid record with the selected account alias. The native work itself was unaffected; the fake loopback had accepted both old preparation records, so production-coordinator acceptance of that old event stream is not claimed. Strict fake retained-event checks prove the corrected path. No additional native call was made after these corrections.

Final executor SHA256: eb3214a4ecfcb08c834857aad0b35cc142680bf6f3df7926dde0f775e990f56d. This final build has deterministic post-call projection/preparation fixes; the actual native proof belongs to preserved 1461d153. Registry changes are limited to the explicit native-current alias; native auth values and global store were not read/exported/copied by Fleet. No new login/account switch was invoked.

Native resume/load, interrupted native stop, Antigravity delegation permissions and stronger instruction-role application remain unqualified; fake protocol support is not presented as paid/native proof. Existing current authority can change outside Fleet, so this is not a new isolated/pinned Google identity or cross-account resume assertion. The proof verifies explicit root/control behavior, not an exhaustive audit of vendor/OS policy. Claude remains independent/unprovisioned. Installed live Fleet was not replaced or restarted.

Common ACP text chunks are now explicitly marked deltas and assembled into readable messages in ordinary/related/lab UI instead of separate assistant rows. Browser replay and lab browser checks passed; the regenerated common-history desktop screenshot was visually inspected. Packaged ui.js SHA256 ae24ef9ecbcc0131b9534e686a70c52f73f84c48f13fe4b7a6ad2b045fb3536e; lab-ui.js faea5680b0933c81ab91d2ca7b7b4193b76485a179fa6062b7adf5d1c5227206; lab.js 67b2f4266807445f693ae0b160f226a9d9619d6a49528ed8d5c452c11219f97c.
