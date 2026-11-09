# Dedicated Fleet accounts: prepared handoff

2026-11-09. No login/OAuth/device/browser flow or model call was started. No credential values, Keychain contents or personal configuration were read/copied. Help/version, documented Orca account metadata and narrowly selected AgentQ/Orca source were inspected. All new account entries remain unprovisioned.

## Prepared, inactive host configuration

Private registry: `/Users/kirs/Library/Application Support/Fleet/native-accounts.prototype.json`, mode 0600. This new file is not installed into a service or exported globally. A later scoped executor invocation may select it with FLEET_NATIVE_ACCOUNTS after parent review.

| Alias | Harness | State | HOME / nativeHome |
|---|---|---|---|
| fleet-codex | Codex | ready from accepted dedicated-account native proof | existing Fleet native-home / codex |
| fleet-codex-second | Codex | unprovisioned | accounts/codex-second/home / native |
| fleet-claude | Claude | unprovisioned | accounts/claude/home / native |
| fleet-antigravity | Antigravity | unprovisioned | accounts/antigravity/home / native |

New paths are under `/Users/kirs/Library/Application Support/Fleet`, mode 0700, with both directories of every unprovisioned entry verified empty. Executables are absolute installed paths: /usr/local/bin/codex, /Users/kirs/.local/bin/claude and /Users/kirs/.local/bin/agy. No auth store was symlinked. The executor refuses an explicitly non-ready entry before launching the native executable; unprovisioned entries cannot accidentally use a native fallback. Existing legacy registries without the new readiness field remain compatible. The readiness field is host admission, not proof of future quota or native success.

Fresh `orca account list --json` metadata showed no managed aliases for these three providers; Antigravity had a current host account. That does not mean Claude/Codex host-default login is absent. Earlier host-default Claude status and current Antigravity quota are not dedicated Fleet readiness.

## Next human login: Claude

The following is a prepared command for a human terminal, **not executed here**:

```sh
fleet_accounts="$HOME/Library/Application Support/Fleet/accounts"
env -i PATH="$PATH" USER="$USER" LOGNAME="$LOGNAME" \
  HOME="$fleet_accounts/claude/home" \
  CLAUDE_CONFIG_DIR="$fleet_accounts/claude/native" \
  /Users/kirs/.local/bin/claude auth login --claudeai
```

Choose the intended Claude subscription in the browser; complete any pasted-code step in the native terminal only. Do not send auth codes/tokens to Fleet or logs. The clean process environment excludes API-key/OAuth-token overrides. The explicit config root avoids importing personal settings. AgentQ registry/claude/signin.rs supports this browser flow and source-qualifies an explicit config-home-specific macOS Keychain service (`Claude Code-credentials-<config-path hash>`), rather than treating the default personal service as the target. This is not authorization to fingerprint/read Keychain values.

After the human login, use the same clean environment and roots with `claude auth status --json`, retaining only loggedIn/authMethod/apiProvider/account metadata. Expected subscription method is claude.ai, not Console/API billing. Confirm the intended principal and isolated settings before parent changes only fleet-claude readiness to ready. Actual quota/model/tool/session behavior still needs a separately authorized qualification when Claude quota permits.

Installed metadata: Claude Code 2.1.319; `claude auth login --help` confirms --claudeai is subscription/default and --console means API billing. `claude --help` documents model aliases and explicit --model; this inspection does not establish per-account model availability. Current repository profiles explicitly request sonnet; parent must confirm or deliberately select the intended available model. No invented `claude models` command is used.

## Next human login: second Codex

Prepared human-terminal command, **not executed here**:

```sh
fleet_accounts="$HOME/Library/Application Support/Fleet/accounts"
env -i PATH="$PATH" USER="$USER" LOGNAME="$LOGNAME" \
  HOME="$fleet_accounts/codex-second/home" \
  CODEX_HOME="$fleet_accounts/codex-second/native" \
  /usr/local/bin/codex login --device-auth
```

Complete the device link/code in a browser signed into the intended **second** subscription account. Keep codes in the native terminal/browser; do not copy auth.json or an access token. With the same clean roots, `codex login status` is the supported non-generating readiness check. It confirms login type, not by itself that two principals differ; parent should verify distinct account metadata using the supported local app-server account/read before admitting this alias. Model discovery is local app-server model/list after authentication, not a guessed `codex models` CLI command. No model/list/account/read call was made here.

Only after reviewed identity/subscription/config separation should parent mark fleet-codex-second ready, select the existing Codex profile and this independent account alias, and authorize a bounded account-routing acceptance. Existing fleet-codex directories/login stay untouched. Installed codex-cli is 0.167.0; login --device-auth and login status are present in installed help.

Orca's supported alternative is `orca account add --agent codex` (device flow) or `orca account add --agent claude` (browser flow). Those provision Orca-owned accounts/homes, so they are not silently substituted for the explicit Fleet paths prepared above. No Orca add/select/remove operation was executed.

## Antigravity: exact separation and review boundary

Installed agy 1.5.3 exposes `agy acp --login`, a browser/terminal auth flow. AgentQ registry/antigravity/signin.rs verifies the native terminal advertisement and supports the **native default** credential store; it explicitly rejects explicit-home login with login-home-isolation-unsupported. Its macOS source identifies shared native Keychain service/account gemini/antigravity plus native file fallbacks. These source facts do not require reading credentials.

Behavior state is a separate concern: installed ACP help exposes --gemini-dir, --app-data-dir, --cli_dir_name and --include-default-customizations; the Fleet stream path supplies its private HOME/GEMINI_HOME plus explicit mode/sandbox/slash-command controls. Separate behavior directories do not prove a separate Keychain principal. Conversely, explicitly approved use of a shared native credential store would not automatically mean personal behavior configuration is inherited. Store binding and behavior-source qualification must be reviewed independently.

**Concrete next login action:** parent first reviews whether a named native-current/shared credential binding is authorized; if reauthentication is needed and approved, the human runs `/Users/kirs/.local/bin/agy acp --login` against that native store. It may change the shared principal, so it is not launched automatically and is not presented as isolated Fleet login. The already current account may make login unnecessary after review. fleet-antigravity stays unprovisioned until an authorized store/principal binding and separate behavior configuration are established; setting that flag ready solely from quota availability is incorrect.

`agy models` is an installed authenticated catalogue command; its help was inspected but the command was not run. Orca account-add help points Antigravity registration to Settings > AI Provider Accounts from the current agy login. Orca's separate managed chat uses pinned agy_acp_server.par 1.4.1, private GEMINI_HOME folders and oauth-personal ACP authentication; that is a different executable/runtime from Fleet's installed agy 1.5.3. Its isolated chat implementation is evidence of the architecture boundary, not automatic proof that its auth can be reused by Fleet. No runtime download, account import or sharing change occurred.

## Admission after owner interaction

Parent owns the three explicit human interactions and readiness changes. Keep the selected profile independent of the alias. For Claude use claude-worker (scoped delegation), claude-design-rules (MCP) or claude-reviewer; same-harness child/continuation keeps its selected account. For Antigravity use its explicit user-role profile and qualified native options; MCP/delegation in Fleet's stream adapter remains unavailable. No account should be admitted until its actual principal/config behavior has been checked. Actual three-harness/account acceptance is still separate from the deterministic mapping evidence.

Current follow-up: parent completed second-Codex login and [one actual routing turn](second-codex-routing-evidence.md) passed. [Antigravity current-account observations](antigravity-readiness.md) show catalogue access works with host HOME/private GEMINI_HOME; no new login is required for the already authorized read-only use. Its remaining constraints are explicit Fleet configuration/ACP integration and model choice, not an invented new approval gate.

Latest Antigravity result: fleet-antigravity-current explicitly reuses the authorized existing native login with separate private ACP behavior roots; [one native qualification passed](antigravity-acp-implementation.md) without a login or credential copy. The original fleet-antigravity entry and Claude stay unprovisioned. No new/replacement Antigravity login is required by this working binding.
