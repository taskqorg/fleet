# Antigravity: existing account and product implementation reuse

2026-11-09. Owner steering: «Посмотри, не надо сейчас, мне кажется, тут заново это придумывать все. Посмотри, эти продукты - они у нас скачаны, у нас есть репозитории.» The implementation reference is the already checked-out T3/Orca code, not a new configuration design. No Antigravity model call, login, account switch, credential export/copy, runtime download or global behavior change occurred.

## Existing implementation to take

T3 Code d0f1f7200dce1229f75314b3d9eb1d7ac3abb607 already contains Antigravity, not only Claude/Codex:

| Existing source | Concrete mechanism for Fleet |
|---|---|
| apps/server/src/orchestration-v2/Adapters/AntigravityAdapterV2.ts | Antigravity flavor over makeAcpAdapterV2, normalized tool events, model/runtime-mode switching and MCP capability. Reuse the protocol mapping; keep Fleet journal/authority. |
| packages/provider-acp/src/server/AcpSessionRuntime.ts:2198–2264 | initialize/auth/session setup, explicit cwd/mcpServers on session/new, load or advertised resume; no fabricated missing-capability fallback. |
| apps/server/src/provider/acp/AntigravityAcpSupport.ts | Actual model selection through setConfigOption("model", …) or setModel; cancellation/prompt normalization and client-version handling. systemPrompt.append is specifically gated to qualified 1.4.5, so it is not evidence for that channel on installed 1.5.3. Preserve Fleet's explicit user role. |
| apps/server/src/provider/acp/AntigravityProtocol.ts | Native tool arguments/results/status normalization already exists. Borrow exact field cases instead of inventing new tool schemas. |
| apps/server/src/provider/antigravityAuthSupport.ts | Separates profile GEMINI_HOME from runtime environment and uses AGY_ACP_FORCE_FILE_STORAGE=1 in its managed auth path. Do not copy its credential migration or change native-current storage implicitly. |
| apps/server/src/provider/Drivers/AntigravitySkills.ts | Explicit personal skills links exist in T3. Leave these out: Fleet does not inherit personal behavior. |

Orca 0b93cef44508c0bda503e6a1063cf4bf1eed92be supplies the complementary boundaries:

- src/main/antigravity/native-account-launch.ts rejects a selected native account launch when HOME/backend authority differs from the host. It does not treat changing HOME as harmless configuration isolation. Its selected-account prepareForLaunch can switch native credentials; Fleet must not invoke that side effect in a read-only/current-account qualification.
- native-credential-backend.ts identifies host .gemini/antigravity-cli file storage and macOS Keychain fallback behavior. No credential values need to be read to reuse that authority model.
- antigravity-acp-environment.ts and acp/acp-account-binding.ts separate private behavior/auth folders and scrub API/harness overrides. Orca's managed chat uses agy_acp_server.par 1.4.1, not Fleet's installed agy 1.5.3. Do not download/replace the runtime or assume managed-chat auth is native-current auth.

The next concrete implementation boundary is therefore a small Fleet binding to the **installed agy ACP session operations**, taking T3's setup/model/MCP/event cases and retaining Fleet process ownership/results. Do not continue guessing stream settings keys or import T3's Effect/product framework, credential copying, skills links, or Orca account switching.

## Observed existing-account readiness

Installed `/Users/kirs/.local/bin/agy` is 1.5.3. Supported read-only `agy models` was run with a clean environment, closed stdin and a bounded timeout. Output was filtered to exit/readiness/model IDs; no secrets or identity values were published.

| Scoped environment | Catalogue outcome |
|---|---|
| Fleet private HOME + private GEMINI_HOME | exit 1: Please sign in to view available models |
| Same private roots with USER/LOGNAME retained | same failure |
| Host HOME + Fleet private GEMINI_HOME | exit 0, current catalogue returned |
| Host HOME + native-current GEMINI_HOME | exit 0, same model IDs |

Thus the existing native login **is usable for read-only catalogue access**. A new/replacement login is not required by these observations. Read-only use was already authorized; no extra approval gate is introduced. Private-HOME failure is a credential-discovery/binding mismatch, not proof the existing account is signed out or invalid. The exact lower-level credential lookup was not inspected through token values.

Observed low-effort model IDs include gemini-3.8-flash-low, gemini-4-argon-low, claude-sonnet-5-5-low and claude-opus-5-5-low; all sanitized IDs are in evidence/antigravity-readiness-20261109/binding-compare.json. The repository profile's gemini-3.1-pro is absent from this returned catalogue; choose an explicitly observed intended model before a paid qualification, without automatic substitution. Catalogue success does not prove model-turn quota or runtime behavior.

## Actual configuration boundary and remaining blockers

`agy mcp list` under host HOME/private GEMINI_HOME returns no configured MCP servers; the private GEMINI_HOME remains empty. `agy --settings '{"mcpServers":{}}' mcp list` returns **Error: --settings: unknown setting "mcpServers"**. The stream settings path is not the evidenced invocation MCP bridge. Installed help exposes persistent mcp add/remove and per-session ACP support exists in T3; Fleet did not mutate the native current MCP store.

Controlled scratch-only config probes did not make sentinel MCP entries visible in guessed settings.json/mcp_config.json locations. They do not establish the complete stream configuration-loading roots. Do not infer complete personal-behavior isolation merely from GEMINI_HOME or the empty MCP list. No personal configuration file was read directly, and no native-current config file was written.

Current Fleet command_registry also requires HOME itself to be a private directory. The actual host HOME is mode 0755, so the metadata-working host-HOME binding is rejected by that existing Fleet guard even before a model call. This is a concrete driver/configuration engineering boundary, not an additional authorization requirement or a reason to launch OAuth. Preserve a private behavior root while deliberately representing native-current credential authority; use the existing product ACP mappings and explicit native root controls rather than chmod'ing the host home or copying auth.

Private fleet-antigravity registry entry remains unprovisioned and unchanged. A later parent native qualification needs the narrow ACP binding/config-source treatment and an explicit catalogue model choice; no Antigravity model call was authorized here. Claude provisioning is independent. Existing current-account use must not be conflated with a newly isolated principal.

Sanitized observations: evidence/antigravity-readiness-20261109/{scoped-readiness,binding-compare,config-probe}.json. Model/catalogue/MCP probes are non-generating. No implementation source or binary changed in this dispatch; the missing stream-to-ACP bridge is not labeled supported.

Follow-up: the existing AgentQ/T3 ACP mechanisms were implemented and [one fresh native turn passed](antigravity-acp-implementation.md), using explicit private behavior flags and native-current credential authority. The earlier stream limitations remain historical observations, not current ACP blockers.
