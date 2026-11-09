# Repaired Codex resume: native qualification

**Passed the bounded repaired-B acceptance.** Exactly one native turn was authorized and submitted in this dispatch, on the saved qualification thread. No retry, replacement thread or second turn occurred. The prior failed A/B evidence remains intact.

## Candidate and scope

Readback before and after the call matched executor SHA256 `5eece6b61e3d9421ea4cbc31863a1dc5dd68afe7cac2e80e0a6c398f14e89d5b`. No implementation change or rebuild occurred in this dispatch. Actual Fleet executor, isolated existing temporary data/session/workspace, scoped loopback coordinator and the existing private registry/account were reused. No installed Fleet service, live coordinator, authentication/global configuration, commit or push changed.

Execution: qualification-repaired-B. Native thread: `01a1be51-c6cf-7430-85be-4745759a09bf`. Workspace remained the existing temporary qualification-session directory. Native turn_context and thread-open response report `gpt-6.1-sol`, effort `low`; sandbox remains workspace-write, approval never, network disabled. Only two local command tools were observed; no network/subagent tools were observed.

[Sanitized proof](../evidence/multiharness-repaired-20261109/sanitized-proof.json), SHA256 `166d0ba07cd5f9bbd04374cf5f83193f296865d382924bed789557066e5e3939`, contains only qualification IDs, test fields, marker/tool readbacks, numeric usage and hashes. New raw outbound/incoming bodies were not copied into repository evidence.

## Exact acceptance readback

| Check | Observation |
|---|---|
| Outbound order | initialize → initialized → thread/resume → thread/inject_items → turn/start |
| Native model turns | one turn/start; one turn/started; one turn/completed |
| Resume text | developerInstructions is exact marker-B profile text |
| Injection | role=developer, input_text exactly equals resume supplied text |
| Instruction text SHA256 | 49c4d2ec71228040cd92af4560eedaf5648456f20110504a2d5a8ebbc85278da |
| Matching injection ack | request id 4, result {}, no error; incoming frame 11 |
| Turn ordering | candidate waits for that successful ack before emitting sole turn/start; turn/started is incoming frame 14 |
| Actual native developer history | exact injected B text persisted at 2026-11-09T03:35:44.043Z |
| New native turn context | persisted at 2026-11-09T03:35:45.167Z with matching model/effort/workspace |
| User task | refers only to current developer instructions; contains no expected B marker |
| Actual file and assistant | output/marker.txt is exactly B; checked by local read/assert; final answer B |
| Completion and closure | actual completed receipt; candidate emits it only after successful native root exit, native completion, stdout EOF and resolved owned cleanup |
| Process readback | sampled executor child PIDs were absent after completion; samples include snapshot helpers, so they are not presented as a complete native PID attribution record |

Native command arguments/results are retained privately and sanitized test commands are in the proof. First command created output/marker.txt with B and exited 0; second read its bytes, asserted b'B', printed B and exited 0. Shared normalized tool events retained the commands/results. Incoming raw log has 49 frames. Latest normalized usage is input_tokens 81790, cached_input_tokens 74624, output_tokens 347, total_tokens 82137, with zero reported cache-write/reasoning-output tokens. Scope is native thread cumulative total, non-additive; these values include retained history and are not this turn's standalone cost or token delta.

The update is **additive developer history**, not deletion or replacement of prior messages. Prior A developer content remains. This test proves current explicit B can supersede that prior qualification instruction through an acknowledged developer-role append; clearing profile instructions has not been proven to remove earlier instructions.

## Private retention

Host-private root: `/var/folders/sw/55r1dvwn2g30l77zs5nwp3_80000gn/T/fleet-native-multiharness-6kcz5pwr`.

Within data/sessions/qualification-session, `qualification-repaired-B.protocol-out.jsonl` is mode 0600, SHA256 `7dc70a3ce945fd9d0d74b54d046ba9a40aab8261863f4ad5b3972a2eeb9ad521`; `qualification-repaired-B.native.jsonl` SHA256 `d2108f58cbb3d96b5dca9e90bf12e15ea9f7dc248f2a4af377ea98d6e7ceb271`. Raw bodies, full repaired transport and native rollout remain host-private. The qualification harness did not read or copy auth files/token values; authentication remained native-owned. Only sanitized fields/hashes are published here. Original failed raw A/B files retain their previously recorded hashes, and their artifact readbacks remain A/A.

## Release-readiness and account setup checklist

- Retain this exact candidate/hash as a local qualified resume candidate. It is a debug build; deployment, installed service replacement and release packaging remain separate actions. The integrated ordinary UI and lab/delegation have deterministic/browser evidence, not a new production-native acceptance in this dispatch.
- Keep private outbound bodies out of normal output artifacts and sharing. Preserve native raw history, journal, acknowledgements and unknown/no-replay behavior. Do not claim clearing instructions erases history.
- Existing dedicated Fleet Codex identity is usable for this bounded test. A distinct second Codex identity still needs explicit host provisioning; prior Orca account-list inspection found zero managed aliases and only host-default OAuth metadata. Operator-supported routes are `orca account add --agent codex` or an explicit dedicated-home native `codex login`; no credential copying. Bind the resulting authorized isolated homes under a new host-registry name, then verify account/config independence rather than inheriting personal behavior.
- Claude remains unqualified for paying Fleet turns. Prior host-default auth status was loggedIn/claude.ai/firstParty, not isolated Fleet readiness; Orca had zero managed aliases. Operator-supported routes are `orca account add --agent claude` or dedicated-home `claude auth login`. Quota/model/tool grants and dedicated HOME/CLAUDE_CONFIG_DIR must be verified when owner permits Claude qualification. No automatic auth provisioning is authorized.
- Antigravity prior host-current presence and usageStatus=ok do not establish an isolated authorized Fleet identity. Orca documents saving the current agy login in Settings > AI Provider Accounts; its account-add command does not support Antigravity. AgentQ's current sign-in adapter explicitly rejects explicit-home login. Establish a supported isolated identity/account route with owner interaction before claiming readiness; do not invent a login command or copy host credentials/settings.
- Verify intended native model codes and exact permissions/tools for each provisioned non-Codex account. Claude's configured allowed tools/dontAsk and Antigravity's user-role instructions/native permissions are distinct native capabilities, not Codex hierarchy/sandbox parity. Antigravity system/developer-role requests remain refused.
- Non-Codex MCP/delegation profile mapping, native Claude/Antigravity task/resume/stop qualification, second-Codex-account routing and broader account/config independence remain gaps. Unsupported requested controls stay explicit. No automatic substitute, API fallback or cross-account resume assertion is allowed.
