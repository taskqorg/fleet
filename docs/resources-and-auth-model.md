# Resources and authentication model

2026-11-09. Research only: no native calls, logins, credential reads or deploys. Sources: official docs ([Codex auth](https://developers.openai.com/codex/auth), [Claude Code auth](https://code.claude.com/docs/en/authentication)), installed help/source evidence in multiharness account onboarding and Antigravity readiness, `packages/coordinator/accounts.ts`, and how `~/Projects/t3code` and `~/Projects/orca` handle accounts.

## Question

Owner: should each subscription be logged in once per environment (Mac, Windows, WSL, Linux PDS), or once and reused everywhere?

Short answer: **log in once per account per environment** by default. Copy credentials only where the vendor documents it (Codex `auth.json`), and only when one environment owns the refresh. Long-lived tokens (Claude `setup-token`) can be used on several Mac/Windows environments. Antigravity is login-per-environment, on the native store only.

## Resource model

- **Account**: one subscription identity (ChatGPT plan, Claude Pro/Max, Google/Antigravity), with harness, plan and policy (Claude personal subscriptions: Mac/Windows only, never Linux/PDS, including child executions). It stores no credential.
- **Host**: a machine reporting platform and heartbeat (owner Mac, Windows PC, PDS18).
- **Environment**: an execution context on a host with its own user profile and credential store. WSL is a separate Linux environment from its Windows host; a container is separate from its host.
- **Binding**: account x environment. It holds host-owned roots (`CODEX_HOME`, `CLAUDE_CONFIG_DIR`, `GEMINI_HOME`/HOME), executable path, credential kind (`interactive-login`, `copied-file`, `long-lived-token`, `api-key`), observed readiness, last check, and who refreshes it. The credential stays on the host. Fleet stores only metadata (already the rule in `accounts.ts`: only `HOME/CODEX_HOME/CLAUDE_CONFIG_DIR/GEMINI_HOME` paths, never values).

This matches Orca ("a login on your laptop does not carry over to the server"; register accounts from the server shell) and T3 Code (login runs "on the environment's machine", one config dir per account).

## Per-harness matrix

| | Codex (ChatGPT) | Claude Code (subscription) | Antigravity (agy) |
|---|---|---|---|
| Storage | `cli_auth_credentials_store = file\|keyring\|auto\|ephemeral`; file is `$CODEX_HOME/auth.json` (plaintext, contains tokens) | macOS: Keychain entry keyed by `CLAUDE_CONFIG_DIR` (falls back to `.credentials.json` 0600 if Keychain is locked, e.g. over SSH). Linux/WSL: `~/.claude/.credentials.json` 0600. Windows: `%USERPROFILE%\.claude\.credentials.json` | macOS: shared native Keychain service `gemini/antigravity` plus native file fallbacks (AgentQ source). Linux/Windows store not verified locally |
| Per-account isolation | Yes, one `CODEX_HOME` per account (proved: two Codex accounts on the Mac) | Yes, one `CLAUDE_CONFIG_DIR` per account (Keychain entry follows it) | No. Explicit-home login is rejected (`login-home-isolation-unsupported`); one principal per OS user's native store |
| Portable? | Yes, documented: copy `auth.json` to a headless host (file store only). API key and Enterprise access token are portable by design | Interactive login: not portable (Keychain on Mac). `claude setup-token`: one-year OAuth token, set as `CLAUDE_CODE_OAUTH_TOKEN`; model requests only (no Remote Control, no claude.ai connectors; not read by `--bare`) | Not documented. Treat as non-portable |
| Login per OS | Mac/Windows: `codex login` (browser). Linux PDS/WSL/SSH: `codex login --device-auth`, or SSH-forward port 1455, or copied `auth.json` | Mac/Windows: `claude auth login --claudeai` (browser; paste code if callback fails, common in WSL/SSH). Linux/PDS: **forbidden** for personal subscription | `agy acp --login` on that environment's native store; may replace the existing principal |
| Refresh risk when shared | High. ChatGPT sessions refresh automatically and rewrite `auth.json`; two copies refreshing independently can invalidate each other (rotating refresh token). Vendor's CI guide only covers one runner that keeps the refreshed file | Interactive login refreshes and rewrites the store: do not copy it. `setup-token` has no refresh; expires after one year, then a new token is needed. Several environments can use it without a refresh race | Unknown; native OAuth refresh into a shared store. Do not copy |
| Recommended | **Login per environment** with `--device-auth` on headless. Copy `auth.json` only as a one-time seed to an environment that then refreshes alone, never keep two live copies of one file | **Login per Mac/Windows environment** (interactive). Optionally one `setup-token` per account reused on eligible Mac/Windows bindings. Never PDS or WSL | **Login per environment**, native store; one Antigravity account per OS user |

Notes:
- Claude on WSL: WSL is a Linux environment. Under the "Mac/Windows only" rule, treat WSL as **not eligible** unless the owner explicitly decides otherwise. Native Windows Claude Code is eligible.
- Codex `ephemeral`/`keyring` store makes copying impossible; Fleet bindings should use `file` store under a private `CODEX_HOME` (mode 0700) so status/inspection works the same on all OSes.
- A `setup-token` is a bearer secret valid for a year. If used, it lives only in the host's protected env file for that binding, never in Fleet records, logs or prompts.

## Recommended owner workflow (fewest human logins)

Per account, the human logs in once on each environment that will run it:

| Account | Mac | Windows | WSL | PDS (Linux) |
|---|---|---|---|---|
| Codex primary | done | 1 (browser) | 1 (device auth) | 1 (device auth) |
| Codex second | done | 1 | 1 | 1 |
| Claude | 1 (pending) | 1 | not eligible | never |
| Antigravity | done (current native) | 1 | 1 if needed | 1 if needed |

Ways to reduce:
1. Only bind environments that will actually run that harness. E.g. Codex on Mac + PDS only = 1 extra login per Codex account.
2. Claude: instead of interactive login on each Windows environment, run `claude setup-token` once per account on the Mac and place the token in each eligible Mac/Windows binding. That is 1 human approval per account per year. Cost: no claude.ai connectors/Remote Control in those sessions.
3. Codex: device auth from the owner's phone/browser takes about a minute per environment; prefer it over copying. Copy `auth.json` only to seed a new environment from a Mac that will then stop using that copy.
4. Each login runs in that environment's terminal (local or SSH), under the binding's exact roots shown by Fleet. Codes and tokens never pass through Fleet.

Minimum today for current scope (Mac + PDS, Claude Mac only): **1 Claude login on Mac + 2 Codex device-auth logins on PDS** (+ Antigravity on PDS only if wanted). Windows adds 1 per account bound there.

## What Fleet UI/API should provide

Uses existing account operations (`account_create`, `account_hosts`, `account_discover`, `account_connect`, `account_login_handoff`, `account_enabled`); no new gates or invented limits.

- Resources page: accounts as rows, environments as columns, cell = binding readiness + credential kind + last check + expiry where known (e.g. `setup-token` issue date).
- Environments as first-class host entries (Windows, WSL, PDS) reported by their workers with platform; WSL reports `linux`.
- Login handoff per cell: the exact command and environment for that OS (`--device-auth` on headless Linux, browser flow on Mac/Windows), then Recheck. Never accepts codes/tokens.
- Binding records the credential kind and refresh owner, so the UI can warn when the same Codex identity appears as a copied file in two environments.
- Claude cells on Linux/PDS/WSL shown as not eligible by policy (existing `eligible` logic; WSL follows its `linux` platform).
- Antigravity: show that login replaces the environment's native principal.
- Expiry visibility: "login expired" from native status becomes binding reason `login-absent`, routed as a question to the owner.

## Decisions needed

1. Which environments each account binds to (Windows? WSL? Antigravity on PDS?).
2. Claude on Windows/Mac: interactive login per environment, or one `setup-token` per account (yearly, no connectors)?
3. Claude on WSL: confirm not eligible (recommended), since WSL is Linux.
4. Codex seeding by copied `auth.json`: allow as a one-time seed, or require device auth everywhere (recommended)?
