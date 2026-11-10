# Project setup and access

2026-11-09. Research/design only: no native calls, logins, credential reads, code changes or deploys. Basis: [fleet.md](../fleet.md), [architecture breadth plan](architecture-breadth-plan.md), [resources and auth](designs/fleet-people-access.md#4-subscriptions), [multi-account same env](designs/fleet-people-access.md#4-subscriptions), [layered isolation](designs/fleet-people-access.md#isolation-layers), [PDS Codex environment](pds-codex-environment.md), [agent tools inventory](agent-tools-inventory.md), `packages/coordinator/{projects,delivery,agent-tools,accounts}.ts`, `delivery_worker.py`, `crates/executor/src/main.rs` (`git()`, `FLEET_PROJECT_ROOTS`, `worktree`), Orca `src/main/github/*` and repository GitHub account settings, T3 Code `apps/server/src/{vcs/GitVcsDriverCore,secrets/*}.ts`.

Owner question: the basic procedure for setting up a project so an agent can really work. No new limits or gates are proposed.

## How the reference tools do it

| | Orca | T3 Code | Fleet prototype |
|---|---|---|---|
| Project | A repo path on a runtime (local, SSH, WSL); worktrees per task | A project = workspace root on the server's environment | `config/projects/<id>/project.json` (id, capability, context files); ids hard-coded `hello-world`, `hello-world-alt` |
| Repo access | Uses the runtime's own git + SSH config; resolves SSH host aliases on that runtime | Server host's git/SSH; git runs non-interactive (`GIT_ASKPASS=""`, `SSH_ASKPASS_REQUIRE=never`); auth failures classified without echoing stderr | Executor: worktrees only under `FLEET_PROJECT_ROOTS`, hardened `git()` (`env_clear`, no global/system config, no hooks, `protocol.file.allow=never`). Program worker: clones `project.repository` from its host config and pushes `HEAD:main` with whatever credential the `fleet` user has |
| Git identity | User's own | User's own | Delivery commits as `Fleet delivery <fleet@ufobe.com>` (hard-coded); agent worktree commits have no configured identity (global config is `/dev/null`) |
| Forge account | Per-repo binding to a `gh` keyring account `(host, user)`; needs `gh` ≥ 2.40; env tokens (`GH_TOKEN`) cannot be bound, flagged as overriding | Source-control provider status per environment (authenticated, token source noted when `GH_TOKEN` wins) | None |
| Secrets | Not a Fleet-like store | Secret store with one-use refs; agent asks, owner answers; `file` request moves the value to an owner-only file for the agent's shell; orchestration records only status | Host-local env files (machine secret, Pipe connection, site read secret) read by worker code; never in records |
| Tools for agents | CLI + skills + injected preamble | Adapter MCP | Fleet MCP always attached (`/api/hosts/agent-tools`, session-bound); profile MCP via `-c mcp_servers.*`/`--strict-mcp-config` |

Takeaway: both references keep credentials **on the environment** in the tool's native store (git/SSH/`gh` keyring) and only record which account/identity is bound. Fleet should do the same: project records name resources and bindings; credentials stay where the native tool reads them.

## Model per layer

A project adds no new isolation mechanism. It references resources that live on the existing layers ([layered isolation](designs/fleet-people-access.md#isolation-layers)).

| Layer | Project resource | Where the credential lives | Fleet records |
|---|---|---|---|
| Project (coordinator) | Goal, rules, context files, repositories, connections, allowed environments, default profiles/roles, recommendations, result destinations | Nothing | The project definition (Git file) + bindings of connections to project resources |
| Connection (architect) | A service access: GitHub account/App, package registry, cloud account, site, Pipe | Per environment, in that tool's native store or a host-protected env file | Kind, scope, which environments hold it, readiness, expiry if known. Never the value |
| Environment (OS user/container on a host) | Git transport credential, `gh` login, registry auth (`~/.npmrc`, `~/.cargo/credentials.toml`, pip), cloud CLI login | Native store of that environment | Readiness per connection x environment (same shape as account bindings) |
| Account binding | Harness subscription | `CODEX_HOME` / `CLAUDE_CONFIG_DIR` / native agy store | Existing binding records |
| Profile / role | Instructions, skills, MCP/tool grants, model/effort, delegation | Git files; MCP server secrets via connection on the environment | Profile ids per project role |
| Session | Worktree of each project repo, branch, inputs/results | Session dir | Worktree path, branch, base commit |

Repo credential options (choose per connection, any may coexist):

| Kind | Use | Notes |
|---|---|---|
| SSH deploy key (per repo, per environment) | Simple push/pull of one repo from one host (PDS program worker) | Narrowest; key file 0600 in the environment; one repo per key on GitHub. No API access |
| GitHub App installation token | Many repos in an org, PRs/checks via API, bot identity | Short-lived tokens minted on the host from the App key; best for multi-repo and audit. Needs a small token helper (`GIT_ASKPASS` or credential helper) |
| Fine-grained PAT | Quick start, API + push for chosen repos | Owner-bound; expiry; store in host env file, expose via credential helper, not argv/URL |
| `gh auth login` (keyring) | Interactive owner Mac work, PR creation via `gh` | Orca model: bind repo to `(host, user)`; per environment; `GH_TOKEN` in env overrides keyring silently |

Recommended default: **SSH deploy key or `gh` login per environment for one repo; GitHub App when a project spans several repos or needs PR/API work.** Fleet records only kind + environment + readiness.

Isolation rule across projects: a session gets only the project's connections. Concretely, the executor passes git credentials per execution (credential helper env pointing at that connection, `GIT_SSH_COMMAND` with that key) instead of relying on the environment's ambient `~/.ssh`/`gh` default. Package registry and cloud configs are given the same way (explicit `NPM_CONFIG_USERCONFIG`, `CLOUDSDK_CONFIG`, ... per connection). Projects sharing one environment then do not see each other's credentials through the harness process env. (A shell tool can still read files of the same OS user; full separation means a separate environment, as for agy.)

## Setup procedure

1. **Create project** (operator/agent operation `project_create`): id, name, goal, rules/context files in Git (`config/projects/<id>/`), default roles → profiles, recommended methods.
2. **Add repositories**: for each, remote URL, default branch, base path name, push policy (branch + PR, or direct to branch), commit identity (name/email per project, e.g. bot).
3. **Bind connections**: pick existing connections (GitHub, registries, cloud, site, Pipe) or ask an architect for a new one (missing access = question, per fleet.md). Bind each to the project resource (repo X uses GitHub connection Y).
4. **Choose environments**: list environments allowed to run the project. For each: the repo checkout root under `FLEET_PROJECT_ROOTS`, and which connections it must hold.
5. **Prepare each environment** (in that environment's terminal, like account login handoff): clone repo into the project root; install connection credentials into the native store (deploy key, `gh auth login`, App key, registry login); install toolchains. Fleet shows the exact commands per OS; it never accepts the secret.
6. **Harness accounts**: for each role profile, the harness accounts that may run it and their bindings on the chosen environments (existing `account_*` operations). Missing binding = login handoff.
7. **Agent settings**: per role, profile (instructions, skills, MCP/tools, model/effort, computer use), project-level overrides. Fleet MCP is always attached; project MCP servers come from connections.
8. **Run readiness** (below). Each failed item is a question routed to operator (binding) or architect (grant/connection).
9. **Smoke task**: a bounded worker turn that reads `project_context`, edits a file in its worktree, commits with the project identity, pushes a branch and reads it back.

## Readiness checklist

One operation (`project_readiness {projectId}`), result per item `ok | missing | failed | unknown` + reason + who must act. Checks are read-only and run on the target environment through the executor.

Coordinator-side:
- Project definition parses; context files present; roles resolve to existing profiles; profile model/effort valid for its harness.
- Every repo has a bound connection; every connection is granted to the project.
- At least one allowed environment has every required connection and a ready account binding per role harness (Claude only on Mac/Windows environments).

Per environment (executor):
- Host heartbeat recent; platform reported.
- Project root exists, is inside `FLEET_PROJECT_ROOTS`, is a git repo with the configured remote and default branch.
- Repo read: `git ls-remote <remote> <branch>` with the connection's credential, non-interactive (T3 env: no askpass).
- Repo write: dry-run push (`git push --dry-run` of a scratch ref) or, for GitHub, App/token permission query (`contents: write`). No real push.
- `gh auth status` (if `gh` connection) for the bound `(host, user)`; flag `GH_TOKEN` override (Orca).
- Registry auth: `npm whoami --registry`, `cargo login` presence check, etc., only for declared registries.
- Cloud/site: declared CLI whoami (`wrangler whoami`, `gcloud auth list`) or site `checkUrl` HEAD with the site secret.
- Harness binding status per role: `codex login status`, `claude auth status --json` (metadata only), agy via existing readiness path.
- Toolchain present at declared versions (`node`, `cargo`, ...), if the project declares them.
- Worktree create/remove in a scratch branch succeeds.

Outputs never include secret values or stderr that may contain them (classify fixed patterns, as T3 does).

## Gaps in the prototype

1. Projects are hard-coded (`['hello-world','hello-world-alt']` in `projects.ts`); no `project_create`, no repository list in the project record.
2. Repository URL and publication roots live only in program-worker host config, not in the project definition.
3. Push uses the `fleet` user's ambient git credential; no connection record, no per-project credential selection, no record of credential kind.
4. Commit identity is hard-coded in `delivery_worker.py`; agent worktree commits have no identity (global git config is `/dev/null`), so an agent `git commit` fails or uses defaults.
5. Executor worktrees: repo must already exist under `FLEET_PROJECT_ROOTS`; no clone/fetch, no branch push, no merge-back.
6. No connection or secret objects; no secret request flow (T3 one-use refs).
7. No allowed-environment list per project; placement follows account binding only.
8. No project → role → profile mapping; profiles are global.
9. No readiness operation for projects (only account binding readiness).
10. Agent process env has no git/registry credential path at all, so a native agent cannot push or install private packages today.

## Implement first (small)

1. Project definition fields: `repositories[] {id, remote, defaultBranch, connection, commitIdentity}`, `environments[]`, `roles {pm, worker, reviewer: profileId}`. Read all `config/projects/*` instead of the hard-coded list.
2. Executor: set `GIT_AUTHOR_*/GIT_COMMITTER_*` from the project identity and pass one git credential per execution (`GIT_SSH_COMMAND` with the connection's key path, or `GIT_ASKPASS` helper) for the session's repo. Paths only, from host config keyed by connection id.
3. `project_readiness` with the coordinator checks + `ls-remote`, push dry-run, identity, binding status per environment.
4. Program worker: take repo and identity from the project definition; credential via the same connection key.

Deferred: GitHub App token helper, registries/cloud connections, secret requests, web UI for connections. Add when a project needs them.

## Owner questions

1. Default repo credential: SSH deploy key per environment, or a GitHub App for the org from the start?
2. Commit identity: one Fleet bot identity for all projects, or per project?
3. Push policy default: agent pushes a branch and opens a PR, or delivery pushes directly to `main` (current)?
4. Must projects on one environment be separated from each other's credential files (needs OS user/container per project), or is per-execution credential env enough?
5. Should agents be able to request secrets interactively (T3 secret-request flow) in V1?
