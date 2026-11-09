# Several accounts of one harness in one environment

2026-11-09. Analysis only: no native calls, logins, credential reads or deploys. Sources: [resources and auth model](resources-and-auth-model.md), [account onboarding](multiharness-account-onboarding.md), [native grants](multiharness-native-grants.md), [second Codex routing](second-codex-routing-evidence.md), [Claude placement](claude-placement-proof.md), [Antigravity readiness](antigravity-readiness.md), `packages/coordinator/accounts.ts`, `crates/executor/src/{main,harness,native,codex}.rs`, T3 Code `apps/server/src/provider/**`, Orca `src/main/{codex-accounts,claude-accounts,antigravity}/**`.

Question: on one host and one OS user, can two accounts of the same harness (two Codex ChatGPT accounts, two Claude, two Antigravity) run **at the same time**?

## How Fleet runs a native turn today

- One executor per `FLEET_DATA_DIR` (`executor.lock`, `flock` in `main.rs`). It holds several live children at once (`live` map, one reader thread per child), so it can already run executions in parallel.
- Each execution spawns its own native process (`codex app-server`, `claude -p`, `agy acp`) with `env_clear()` and only `PATH/TMPDIR/LANG/SSL_*` passed through, plus `HOME` and the binding root: `CODEX_HOME` (`native.rs`), `CLAUDE_CONFIG_DIR` plus `USER/LOGNAME` (`harness.rs`), `GEMINI_HOME` plus `--gemini-dir`/`--app-data-dir` (`harness.rs`). No long-lived shared daemon is owned by Fleet.
- The coordinator stores only binding metadata (`HOME/CODEX_HOME/CLAUDE_CONFIG_DIR/GEMINI_HOME` absolute paths), never credentials (`accounts.ts`). Binding readiness is recorded per host.

## Desired architecture vs reality

| Wish | Codex | Claude Code | Antigravity (agy 1.5.3) |
|---|---|---|---|
| Account = identity, binding = private profile root per env | Matches: `CODEX_HOME` holds `auth.json`, config, sessions | Matches: `CLAUDE_CONFIG_DIR` selects both files and the macOS Keychain item `Claude Code-credentials-<hash of dir>` | **Does not match.** Credential is the shared Keychain item `gemini/antigravity` per OS user; explicit-home login rejected (`login-home-isolation-unsupported`, AgentQ). `GEMINI_HOME` isolates behavior only |
| Two accounts side by side on one OS user | Yes, two homes | Yes, two config dirs (two Keychain items) | One principal per OS user's native store |
| Two accounts running concurrently | Expected, **not proven** | Expected, **not proven** | Not possible via native store |
| Fleet never touches credentials | Holds | Holds | Holds only by reusing the current native principal (`fleet-antigravity-current`) |
| No personal config bleed | Holds if the private home has no inherited config (Fleet ignores config/execpolicy) | Holds via `--setting-sources ""`, `--strict-mcp-config`, explicit `--tools` | Behavior roots private via `--gemini-dir`, `--include-default-customizations=false`; complete config-source isolation not proven (readiness doc) |

## Per harness

### Codex

- Isolation: `CODEX_HOME` per account + private `HOME`, file credential store, dir mode 0700.
- Proven locally: two distinct ChatGPT identities on the Mac (`fleet-codex`, `fleet-codex-second`); second-home routing turn `second-account-routing-one`, native thread `01a1be7a-9709-7592-ae84-1bdd9d098b62`, history under `accounts/codex-second/native/sessions` ([proof](../evidence/second-codex-routing-20261109/sanitized-proof.json)). Both accounts ran **sequentially**.
- Collisions:
  - Refresh race: only if the same `auth.json` is used by two live copies (two homes of one identity, or a copied file). Two different identities in two homes do not share a refresh token.
  - Same account, two concurrent runs in one home: two app-servers rewriting the same `auth.json`/session index. Expected to work (Codex CLI is used this way), not proven under Fleet.
  - Daemon/socket: Orca writes a "daemon socket guard" into managed `config.toml` because long `CODEX_HOME` paths break the Unix socket path limit. Fleet roots under `~/Library/Application Support/Fleet/accounts/.../native` are long; watch for this when a Codex feature opens a socket under the home.
  - Ports: login callback 1455 only during `codex login`; device auth avoids it. Not a run-time collision.
- Concurrency: **assumption.** T3 (`supportsMultipleInstances: true`, one `CODEX_HOME` per instance) and Orca (per-account managed homes) both rely on it.

### Claude Code

- Isolation: `CLAUDE_CONFIG_DIR` per account; macOS Keychain item keyed by that dir path (Orca `claude-accounts/keychain.ts`, AgentQ). Linux/Windows: `.credentials.json` inside the dir. Fleet passes `HOME` and real `USER/LOGNAME` (Keychain account name).
- Proven locally: one dedicated Claude account on the Mac, execution `4474aff0-bf92-4e4a-ad04-20d514339fbd` ([connected live evidence](connected-live-evidence.md)). Linux bindings rejected (403) by policy. No second Claude account exists.
- Collisions:
  - Keychain item name depends on the exact dir spelling (symlink/realpath). Orca checks several spellings; Fleet must pass one canonical absolute path for login and runs, or a run sees "not logged in".
  - Locked Keychain (SSH, launchd without a GUI session) makes Claude fall back to a file in the dir; login and runs must happen in the same session type.
  - Refresh: each run may refresh and rewrite its own Keychain item; concurrent runs of the **same** account race on one item. Different accounts have different items.
  - `--fork` copies transcripts inside one config dir only; cross-account fork/resume is not valid.
- Concurrency: **assumption** (T3 `supportsMultipleInstances: true`, Orca profile homes).

### Antigravity

- Isolation: none for credentials on the native store. `GEMINI_HOME`/`--gemini-dir`/`--app-data-dir` isolate behavior only.
- Proven locally: catalogue works with host `HOME` + private `GEMINI_HOME`, fails with private `HOME` ([readiness](antigravity-readiness.md), `evidence/antigravity-readiness-20261109/binding-compare.json`); one native ACP turn on the current principal ([ACP implementation](antigravity-acp-implementation.md), `evidence/antigravity-native-20261109/`).
- Collisions: one shared Keychain item per OS user; `agy acp --login` replaces it for every consumer of that user, including the owner's own agy. Orca's account switcher works by rewriting that item (serialized `AntigravityAccountService`), so switching mid-run changes the principal of other runs. Refresh of the shared item by several concurrent runs of the same principal is unproven.
- Untested lead: T3 sets `AGY_ACP_FORCE_FILE_STORAGE=1` with a private `GEMINI_HOME` per profile, which suggests per-home file credentials on its pinned runtime. Not verified on installed 1.5.3; would need its own login under that home.
- Concurrency: two **different** accounts at once: not possible on one OS user's native store. Same account, parallel runs: unproven.

Options for a second Antigravity account:

| Option | Separation | Cost / risk |
|---|---|---|
| Separate macOS user (e.g. `fleet-agy2`) running its own executor | Own login Keychain, own HOME; real isolation | Needs a GUI/login session or unlocked keychain for that user; separate executor + data dir; files/workspaces need cross-user access |
| Container / Linux VM (PDS, WSL, Docker) | Own filesystem store; one principal per container | Linux credential store not verified locally; login per container; no macOS Keychain |
| Full VM (macOS) | Complete | Heaviest; license and resources |
| Per environment only (one account per env: Mac = A, PDS = B) | Natural with current model | No two accounts on the Mac |
| `AGY_ACP_FORCE_FILE_STORAGE=1` per `GEMINI_HOME` | Per-home file credential, if it works on 1.5.3 | Unverified; plaintext file; may be version-specific |
| Orca-style switching | None (time-sharing) | Not concurrent; breaks running sessions |

## Recommended organization

- **Account** = one identity (harness, plan, policy). No credentials.
- **Binding** = account x environment = one private profile root on that env, created 0700, used for login and every run with the same canonical path. Fleet owns process env (`env_clear`), the native owns the credential.
- Required isolation:
  - Codex: own `CODEX_HOME` (+ private `HOME`), file store. Never two live copies of one `auth.json`.
  - Claude: own `CLAUDE_CONFIG_DIR`, canonical path, same `USER`, same session type (GUI vs SSH) for login and runs. Mac/Windows only.
  - Antigravity: own **OS user or container** per account. Within one OS user, at most one Antigravity account; private `GEMINI_HOME` for behavior.
- Concurrency of one account inside one binding is allowed by default (no invented limits); treat refresh contention as an observed failure (`login-absent`), not a pre-emptive gate.
- An environment is defined by its credential store (OS user / container / VM), not by the host. That makes "second Antigravity on the Mac" a new environment, which fits the existing binding model with no new concepts.

## Proposed qualification (proposal only)

1. **Two Codex accounts in parallel on the Mac.** Submit one low turn to `fleet-codex` and one to `fleet-codex-second` so both are live together. Check per execution: selected binding, effective `CODEX_HOME` marker, new rollout under the correct home only, distinct native thread ids, both terminal `completed`; afterwards `codex login status` in both homes still logged in, and `auth.json` mtimes changed only in their own home.
2. **Same Codex account, two concurrent turns** in one home: same checks plus status still logged in after both finish.
3. **Claude**, after a second Claude account is logged in: the same pair as step 1 with two `CLAUDE_CONFIG_DIR`s; confirm via `claude auth status --json` (metadata only) that each still reports its own account after the runs.
4. **Antigravity**: no parallel test on one OS user. If a second account is wanted, first choose an option above; optionally a non-generating check whether `AGY_ACP_FORCE_FILE_STORAGE=1` + private `GEMINI_HOME` gives a separate `agy models` login state on 1.5.3.

Each step reuses existing Fleet operations and evidence format; paid turns need the usual owner authorization.
