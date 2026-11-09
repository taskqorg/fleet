# Layered isolation model

2026-11-09. Goal: hundreds of concurrent sessions (lab: many worker variants under owner-configured balancing) without mixing subscriptions, context, tools or instructions. No new limits or gates are proposed; where a hard resource exists it is named and measured.

Sources: `crates/executor/src/{native,harness,codex,lab,main}.rs`, `packages/coordinator/{accounts,lab}.ts`, [resources and auth model](resources-and-auth-model.md), [multi-account same env](multi-account-same-env.md), [lab runtime](lab-runtime-build.md), T3 Code `d0f1f7200d` (`ClaudeHome.ts`, `ClaudeSkills.ts`, `antigravityAuthSupport.ts`), Orca `0b93cef4450` (managed per-account homes, computer use on the visible desktop). Evidence: [`evidence/layered-isolation-20261109/results.json`](../evidence/layered-isolation-20261109/results.json).

## Layers

| Layer | Owns | Lives on disk | May be shared by | Must never be shared |
|---|---|---|---|---|
| **Environment** (OS user / container / VM on a host) | Credential store (macOS login Keychain, Linux files), one GUI desktop, `TMPDIR`, port space, process table, `HOME` dotfiles | `/Users/<u>`, container FS | All bindings, harnesses and sessions inside it | Across environments: Keychain, GUI. Claude personal subscription: Mac/Windows environments only, never PDS/Linux/WSL |
| **Account binding** (account x environment) | Credential and its refresh; native session store; harness state DBs | Codex `CODEX_HOME` (`auth.json`, `sessions/`, `state_*.sqlite`, `tmp/`); Claude `CLAUDE_CONFIG_DIR` (+ Keychain item keyed by its path); Antigravity: shared Keychain item `gemini/antigravity` per OS user + `GEMINI_HOME` for behaviour | Every session and profile routed to that account (parallel use is allowed) | Two live copies of one credential (copied `auth.json`); two Antigravity accounts in one OS user; personal homes (`~/.codex`, `~/.claude`, `~/.gemini`) |
| **Harness** (codex / claude / agy + version) | Executable, built-in system prompt and built-in tools, global config lookup rules | `/usr/local/bin/codex` 0.167.0, `~/.local/bin/claude`, `~/.local/bin/agy` 1.5.3 | All bindings on the host | Nothing needs private copies, but the version must be recorded per run (built-in prompt/tools change between versions, see collaboration tools below) |
| **Agent profile / role** | Instructions, skills, MCP servers and tool grants, computer use, model/effort, delegation grants | Coordinator records (`agentConfig`, profiles); applied per process as flags/`-c` overrides, never written to a binding home | Any number of sessions and accounts (profile has no account binding) | Must not be inherited from the harness global config or a parent directory |
| **Session** | Working directory, native thread id, transcript, per-session files | Executor `FLEET_DATA_DIR/sessions/<id>` (live Mac: `~/.local/share/fleet-prototype/data/sessions/<id>`), marker files `.fleet-native-home.json` / `.fleet-harness-account.json`; transcript in the binding home | Only by explicit fork/resume within the same binding | One working tree written by two live sessions; resume across bindings (refused today by the marker check) |

How Fleet composes a run today (all per process; nothing written to shared global config):

- Process env: `env_clear()`, then only `PATH USER LOGNAME TMPDIR LANG LC_ALL SSL_*`, plus `HOME` and the binding root.
- Codex: private `HOME` + `CODEX_HOME`, `--ignore-user-config --ignore-rules`, `project_doc_max_bytes=0`, `skills.enabled=false`, `mcp_servers={}`, `--disable memories,hooks,plugins,apps,multi_agent,browser_use,computer_use,image_generation`, `--enable skip_host_skill_discovery`; profile instructions as `developer_instructions`/`developerInstructions`; profile MCP added as `-c mcp_servers.<id>.*`.
- Claude: `CLAUDE_CONFIG_DIR` = binding, **`HOME=/Users/kirs`** (credential home, needed for Keychain), `--setting-sources ""`, `--strict-mcp-config`, explicit `--tools`, `--system-prompt`.
- Antigravity: `GEMINI_HOME` + `--gemini-dir` private, **`HOME=/Users/kirs`**, `--include-default-customizations=false`.

## Collision list

| # | Collision | Layer | Severity | Status |
|---|---|---|---|---|
| 1 | Codex built-in **collaboration tools** (`spawn_agent`, `send_message`, `wait_agent`, `list_agents`, ...) are present in every Fleet Codex run although `multi_agent` and `multi_agent_v2` are disabled. No tested override removes them (`tools.collaboration=false`, `collaboration_modes`, `agents.max_threads=1`; `=0` is rejected: "must be at least 1"). A session can fan out child agents on the same subscription, invisible to Fleet's queue and accounting | Harness | **High** for hundreds of sessions | Measured statically and in a live answer (test b, c2) |
| 2 | Antigravity: one principal per OS user. `AGY_ACP_FORCE_FILE_STORAGE=1` with a private `GEMINI_HOME` does **not** give a separate login state on 1.5.3: `agy models` and ACP `session/new` still succeed on the shared principal, no credential file appears in the home | Environment | **High** (second agy account needs another OS user/container) | Measured (test d) |
| 3 | Computer use: one GUI desktop and one input focus per OS user/session; parallel GUI agents fight for the same pointer and windows. Fleet disables it everywhere today | Environment | **High** once enabled | Design; not tested |
| 4 | Claude and Antigravity run with `HOME=/Users/kirs`. Every session cwd is under `/Users/kirs`, so ancestor `/Users/kirs/.claude/` (`CLAUDE.md`, `settings.json`, `skills/`), `~/.claude.json`, `~/.gemini/` are reachable through home and up-tree lookup. Isolation rests on `--setting-sources ""` / `--include-default-customizations=false`; [claude binding evidence](claude-binding-evidence.md) checked ancestor `CLAUDE.md` but not ancestor `.claude/CLAUDE.md` | Binding/Harness | Medium | Unverified; needs one marker run each |
| 5 | Codex guards are load-bearing: with home isolation alone the session `AGENTS.md` (Fleet writes profile instructions there) is loaded as project doc, duplicating instructions. Personal `~/.codex` AGENTS lines, Memory pointer and personal skills appear only in the personal-default contrast run | Profile | Medium if a guard is dropped | Measured (test c) |
| 6 | Same-account refresh race: N runs in one `CODEX_HOME` / one Claude Keychain item may refresh concurrently | Binding | Medium | Not triggered: `auth.json` unchanged in tests a, e (no refresh due) |
| 7 | Antigravity builtin plugin `swarm` (with sidecars) is unpacked into every fresh `GEMINI_HOME` despite `--include-default-customizations=false` | Harness | Low-medium (unknown tools) | Observed (test d2); tool exposure not checked |
| 8 | Session working dirs are flat executor dirs, not git worktrees. Two sessions on one repository need distinct worktrees; executor refuses a second live command per session only | Session | Medium for project work | Design |
| 9 | Shared `TMPDIR`, ports (Codex login callback 1455 only during login; MCP servers are stdio), long-path Unix socket limit for features opening sockets under long homes | Environment | Low | Codex keeps scratch in `CODEX_HOME/tmp` |
| 10 | Executor hard-pins Codex runs to `gpt-6.1-sol` / `low` (`main.rs`), which blocks lab variants on model/effort | Profile | Medium for the lab | Existing code, proposal below |
| 11 | Provider prompt cache is not account-scoped: the second account got `cached_input_tokens=11520` in its first parallel run | Vendor | Informational | Observed (test a) |

## Test results (Mac, executor-equivalent spawns)

Method: exact `native.rs` env and flags (`codex exec --json`, `gpt-6.1-sol` low) against the live Fleet bindings, session dirs under `~/Library/Application Support/Fleet/isolation-test/sessions` (same ancestor tree as live sessions, removed afterwards). The Fleet coordinator/executor service was not used, to avoid interfering with the parallel live handoff work. No logins, no credential contents read.

| Test | Result |
|---|---|
| a. `fleet-codex` + `fleet-codex-second` in parallel | Both completed (5.9 s, 8.7 s). Threads `01a1bfd3-ae43-7a12-9a82-78181a72b851` / `01a1bfd3-ae3d-7660-8b45-904b5a71f633`; each rollout only in its own home. `auth.json` mtime unchanged in both homes and in `~/.codex` |
| b. Same account, two profiles in parallel | Alpha (marker + design-rules MCP) reported `PROFILE_ALPHA_7Q` and `mcp__design_rules`; Beta reported `PROFILE_BETA_3K`, no MCP. No cross-marker, no personal MCP (serena, pipe, agentq, ...), `SKILLS=NONE`. Both list the built-in collaboration tools (collision 1). Threads `01a1bfd3-f0fc-7b20-82fe-9facf2a3a8eb` / `01a1bfd3-f0fc-7740-941f-9852789d38e3` |
| c. Static bleed (`codex debug prompt-input`, `codex mcp list`) | Fleet-guarded: 0/5 personal AGENTS lines, no Memory pointer, no session AGENTS.md, no personal skills. Home-only: session AGENTS.md loaded. Personal default: everything leaks (contrast). Fleet home MCP list: empty. Claude not run |
| d. agy `AGY_ACP_FORCE_FILE_STORAGE=1` + private `GEMINI_HOME` | No separate login state: 13 models listed and ACP `session/new` succeeds with and without the flag; no credential file created. No login, no prompt |
| e. Burst, 10 parallel on `fleet-codex` | 10/10 completed, 10 distinct threads and rollouts, wall 5.9 s, latency min 3.73 / median 4.35 / max 5.90 s, no 429, `auth.json` unchanged |

## Observed limits

- 10 concurrent Codex turns on one ChatGPT subscription: no throttling, no latency growth beyond about 2 s spread. The real per-subscription ceiling is unknown; it was not approached.
- Antigravity: 1 account per OS user (measured on 1.5.3).
- Computer use: 1 GUI per OS user (by construction).
- No other limit was observed. Nothing here justifies a pre-emptive cap.

## Proposals (code; for the owner of `crates/`)

1. **Collaboration tools.** Record harness version and the actual tool list per run (from `prompt-input` or the first turn) and expose a profile-level choice. Until Codex offers a switch, either accept and account child agents (they share the binding) or filter them through app-server dynamic tool config if available. Re-check each Codex upgrade.
2. **Neutral cwd ancestry for Claude/agy.** Place session roots outside `/Users/<u>` (e.g. `/Users/Shared/fleet/<host>/sessions` or a dedicated volume) or prove `--setting-sources ""` ignores ancestor `.claude/`; keep `HOME` only for Keychain.
3. **Profile-driven model/effort** instead of the fixed `gpt-6.1-sol`/`low` check in `main.rs`, so lab variants differ by profile.
4. **Per-session git worktree** for project sessions (one branch per session), created and removed by the executor.
5. **Environment = credential store** as a first-class record: second Antigravity account or parallel computer-use agents get their own macOS user or container, each with its own executor and data dir.
6. Report refresh failures as binding `login-absent` (already planned), no locks.

## Next tests for hundreds of sessions

1. Codex ramp on one account: 25, 50, 100 parallel cheap turns; record success, p50/p95 latency, 429 and the vendor reset time. Repeat split over two accounts.
2. Same, through the real coordinator and executor (queueing, journal, receipts, fd and RSS of the executor).
3. Claude Mac marker run (only on Mac): ancestor `/Users/kirs/.claude/CLAUDE.md` marker absent, `skills`/MCP absent; then two `CLAUDE_CONFIG_DIR`s in parallel once a second Claude account exists.
4. Codex collaboration: one bounded run asking to spawn a child; record which home, account and thread it uses.
5. Forced refresh: N parallel runs while the token is due for refresh; check every run keeps working and `auth.json` stays valid.
6. Antigravity per container/OS user on PDS: two principals concurrently, each with its own executor.
7. Worktree collision: two sessions on one repository with per-session worktrees, including cleanup.

## Implementation and live load (2026-11-09, through the real coordinator and executors)

Code (`crates/executor/src/{main,lab,native,harness}.rs`, `packages/coordinator/{app,lab}.ts`), commits `ccc2d24e023d`, `ffb1caf88e56`, `021ecc523468`, `3310458e3b41`, `4d3c16b2b686`:

- Proposal 3: Codex model/effort come from the profile/payload (`thread/start`, `turn/start`); the fixed `gpt-6.1-sol`/`low` check and `-m`/effort args are gone. Profiles accept any model/effort string not starting with `-`. The default without a profile stays `gpt-6.1-sol`/`low`.
- Proposal 2: `FLEET_SESSIONS_ROOT` puts new session dirs outside `/Users/<u>` (Mac: `/Users/Shared/fleet/owner-mac/sessions`, 0700). Existing sessions keep their old dir, so resume markers still match. `HOME` stays the credential home only for Claude/agy `native-current` (Keychain). Live: Claude `fleet-claude` and agy `fleet-antigravity-current` completed turns from the new root (Claude reported no CLAUDE.md or user skills in context; its init tool list is only the granted tools plus `mcp__fleet__*`).
- Proposal 4: optional session `workingDirectory` (`POST /api/sessions/:id/profile`). When it is a git repo, the executor creates `<session>/worktree` on branch `fleet/<session>` and runs the harness there. The path is untrusted. It is canonicalized and must sit under `FLEET_PROJECT_ROOTS` (unset means no worktrees). It must not overlap the executor or session dirs, and the git toplevel and common dir get the same check. git runs with a cleared env, no system/global config, `core.hooksPath=/dev/null`, `core.fsmonitor=false`, `protocol.file.allow=never` and `GIT_CEILING_DIRECTORIES` set to the matched root. Worktree removal is not implemented.
- Proposal 1: every run emits a `harness` event with the version, the actual built-in tool list (Codex: `codex debug prompt-input` with the run's env and flags, cached per flag set), the Fleet MCP tools, cwd, worktree and platform. Profile booleans `fleetTools` and `collaborationTools` both default to true. `collaborationTools:false` adds `--disable multi_agent --disable multi_agent_v2`, and the recorded tool list shows whether that worked.
- The executor poll sends `platform` (`darwin`/`linux`). The coordinator now shows `pds18-native` as `linux`.
- Executor outbox: one pooled HTTP agent (keep-alive) and consecutive events of one execution posted as one batch.
- PDS tests: the build archive is now `git archive HEAD Cargo.toml Cargo.lock crates config/agents`. Tests ran on PDS: 21 pass (the fake-launch test asserts the Claude refusal on Linux).
- PDS infra: `codex-code-mode-host` 0.162.0 was missing, so PDS Codex had no shell tool. It was copied from the hermes 0.162.0 release dir, which has a byte-identical `codex` (SHA256 `4c1984f4…`).

Load: Codex turns ask "pwd; echo $HOME $CODEX_HOME; reply MARKER ...". Each run has its own session, marker and new thread. Driver: `/root/fleet-load.py` on PDS against the local coordinator. Latency is from session create to terminal state, as seen by the coordinator. "Turn" is the native `turn.durationMs`.

| Run | OK | Wall s | Latency p50 / max s | Turn p50 / max s | Max time to start s | 429 | Distinct threads | Isolation |
|---|---|---|---|---|---|---|---|---|
| Mac 5, 2 accounts (before batching) | 5/5 | 50.3 | 42.5 / 50.3 | 8.6 / 9.8 | 11.0 | 0 | 5 | own marker, own session cwd, HOME/CODEX_HOME of its own account |
| Mac 10, 2 accounts (before) | 10/10 | 100.0 | 78.2 / 100.0 | 8.6 / 10.6 | 22.6 | 0 | 10 | same |
| PDS 5 `fleet-codex-pds` (before) | 5/5 | 18.4 | 17.4 / 18.4 | 6.1 / 7.5 | 7.6 | 0 | 5 | same |
| PDS 10 (before) | 10/10 | 40.3 | 34.0 / 40.3 | 6.7 / 9.1 | 16.0 | 0 | 10 | same |
| Mixed 10 (4+3 Mac, 3 PDS) (before) | 10/10 | 72.7 | 32.1 / 72.7 | 7.8 / 11.2 | 45.5 | 0 | 10 | same, correct host per binding |
| Mac 5 (after batching) | 5/5 | 33.0 | 29.8 / 33.0 | 8.1 / 10.1 | 11.9 | 0 | 5 | same |
| Mac 10 (after) | 10/10 | 64.1 | 51.5 / 64.1 | 8.2 / 9.8 | 28.0 | 0 | 10 | same |
| PDS 10 (after) | 10/10* | 48.2 | 43.4 / 48.1 | 6.5 / 7.7 | 29.3 | 0 | 10 | same |
| Mixed 10 (after) | 10/10 | 58.3 | 34.9 / 58.3 | 8.5 / 11.0 | 44.9 | 0 | 10 | same |

\* The driver missed 2 final answers because of a read race. The native logs have the correct marker, cwd and home for both.

Findings:
- No 429 or throttling at 10 parallel turns on one or two subscriptions. Native turn time stays flat (about 6–11 s).
- The bottleneck is the executor's single loop, not the vendor. Starts come out about 2–2.5 s apart or in waves (PDS: 5 at once, then the rest after about 20 s). Each executor delivers events serially to the coordinator. Batching and keep-alive cut Mac 10 from 100 s to 64 s. Next fix: deliver events on a separate thread and accept all polled commands before flushing. No cap is needed.
- An event with a duplicate `seq` (first `harness` build used seq 0) blocks that executor's outbox for every session (409 retained forever). This was fixed and the one stuck row was removed. The outbox is still all-or-nothing per row order.
- Child agent (`spawn_agent`, Mac `fleet-codex`, profile `website-review`, collaboration on): parent thread `01a1c013-bb9a-7c91-a016-51486df5ffb2`, child thread `01a1c013-ce7b-7d13-820c-c82660e24fce`. The child has its rollout in the same `CODEX_HOME` (`fleet-codex`), the same session cwd and the same `HOME`. Nothing appears in the second account or in `~/.codex`. Fleet sees it only inside the parent's events: no separate execution and no accounting. With profile `baseline` the model refused, because its instructions say not to use subagents.
