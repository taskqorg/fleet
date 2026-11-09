# Claude host binding qualification

2026-11-09. No login, token read/export/copy or account switch occurred. Existing owner login is Mac/Windows only; Fleet rejects Claude on Linux and the PDS18 program worker cannot run it.

T3 Code d0f1f7200dce1229f75314b3d9eb1d7ac3abb607, apps/server/src/provider/Drivers/ClaudeHome.ts43–44, explicitly preserves host HOME for macOS Keychain and sets CLAUDE_CONFIG_DIR for separate configuration. AgentQ d0f6bfa795c0bc3f3b565e32e7056bad5a44809e, runtime/src/agent/registry/claude.rs36,53,159–160, supplies the config-root/CLAUDE.md/settings/strict-MCP mapping. Mechanisms were reused; no source was copied and no global AgentQ/T3 policy was imported.

Fresh local status initially returned loggedIn=false with HOME/config/PATH alone, although the default user keychain was available. The parent's exact clean environment additionally set USER/LOGNAME; repeating once with OS-derived user metadata returned exit0, loggedIn=true, claude.ai, firstParty, no stderr. Fleet now supplies only those OS identity fields for Claude in addition to its existing environment allowlist. This is not a new credential source.

The dedicated config root has no CLAUDE.md, settings.json, skills, plugins, commands or agents inputs. The clean Fleet workspace has no ancestor CLAUDE.md files in the inspected path. Fleet passes empty setting sources, explicit system prompt, disabled slash commands, strict invocation MCP and explicit supported tools/allowedTools with dontAsk; no broad permission bypass. Installed2.1.319 help says safe-mode disables customizations including MCP and bare disables OAuth/keychain, so neither was silently substituted for this subscription/MCP launch. Admin-managed policy and native built-ins remain native authority; no universal sandbox parity is claimed.

Only private registry metadata was changed to the explicit native-current credential authority with host HOME and dedicated native config root after the fresh check. Managed host inspection and one bounded actual Mac tool/file turn remain the next native qualification, not an inference from login readiness.
