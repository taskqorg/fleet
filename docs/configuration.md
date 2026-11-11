# Configuration

Where each setting lives, how settings resolve, what a project file holds and what a host needs. Settings organise existing mechanisms: no new services, mandatory forms or universal inheritance tree. Allocation of hosts and accounts is in [work allocation](work-allocation.md); the harness boundary in [runtime integration](runtime-integration.md); connection records in [connections](designs/fleet-connections.md); people and host secrets in [people and access](designs/fleet-people-access.md).

## Canonical homes

| Home | Contains | References rather than copies |
|---|---|---|
| Shared catalogue | Agent configurations, role instructions, skills, tools, methods and pipelines. | Canonical definition files and their dependencies. |
| Project | Purpose, rules, repositories, environments, roles, workflow overrides, recommended methods, delivery rules. | Shared definitions, project definitions and project materials. |
| Agent configuration (profile) | Harness, model, effort, instructions, MCP servers, sandbox and mode. | Role and skill definitions, tool capabilities. |
| Method or pipeline | Input and output contract, implementation choices, steps and transitions. | Agent configurations, tools and required capabilities. |
| Host or environment | Installed software, project roots, working-area preparation, placement settings. | Connection identities, shared host capacity. |
| Connection | Service access, accounts, quota pools, permitted use. | Protected credentials; never copied into prompts or definition files. |
| Assignment | Outcome, inputs, explicit choices, requirements, budget. | Project context and selected definitions. |

The catalogue indexes definitions; it is not another editable copy. A project references a shared profile and adds its own requirements without cloning it. Checked-in files: `config/catalogue.json`, `config/agents/<id>.json`, `config/pipelines/*.json`, `config/projects/<id>/project.json` with `rules.md` ([config/README.md](../config/README.md)).

## Project context

The project repository is the default home for instructions, requirements, pipeline definitions and small materials; large assets stay elsewhere and are referenced from project files or task inputs. A project needs no code repository.

Tasks expose their inputs and results; pipelines expose their definitions and settings. Each step gets its relevant instructions and materials; other context stays discoverable. Context selection neither grants nor restricts access.

Ordinary sessions run one-off work and experiments without a pipeline. Formalise a method when repetition warrants it; repeated work uses accessible definitions, not conversation history. Execution history does not make an experiment a project standard: changing project rules or recommended methods is an explicit action. Validate this baseline on real projects before adding material catalogues or a prescribed project directory layout.

## Project settings

Every setting is per project; absent key means the default below. The coordinator reads every `config/projects/<id>/project.json` (no fixed list) and `projectSettings` in `go/coordinator/projects.go` validates it: a wrong type or enum value fails the project (500 `Invalid project setting <key>`); an unknown key is kept out and listed in `unknownKeys`, never dropped silently.

| Key | Default | Values and effect |
|---|---|---|
| `id`, `name`, `contextFiles` | required | `contextFiles` are paths inside the project directory, served by `project_context`. |
| `capability`, `recommendedMethod` | none | Project recommendation for the catalogue. |
| `repos[]` | `[]` | `{id, remote, forge: github\|gitlab = github, defaultBranch = main, connection, path}`. `remote` required. |
| `environments[]` | `[]` | `{hostId, checkoutPath, repo?}`: hosts allowed to run the project and their checkout. `checkoutPath` must sit under the host's `FLEET_PROJECT_ROOTS`. |
| `roles` | `{}` | Role name to profile id (`pm`, `worker`, `reviewer`, ...). `session_start` takes a role or a profile id. |
| `worktree.envMode` | `worktree` | `worktree` \| `local`. Not built: `local`; a session in a git checkout always gets a worktree. |
| `worktree.baseRef` | `null` | Explicit base ref; wins over `startFromOrigin`. |
| `worktree.startFromOrigin` | `true` | Fetch, then branch from `origin/<defaultBranch>` of the first repo; a failed fetch falls back to `HEAD`. |
| `worktree.branchPrefix` | `fleet` | Session branch `<prefix>/<sessionId>`. |
| `worktree.linkPaths`, `worktree.sharedDirectories` | `[]` | Relative paths symlinked from the main checkout into the worktree (`.env`, `node_modules`); only existing, non-escaping paths. Secrets in them stay per environment, never in Git. |
| `scripts.setup` | `null` | Runs once in a new worktree with `FLEET_ROOT_PATH`, `FLEET_WORKTREE_PATH`, `FLEET_SESSION_ID`, a cleared environment (`PATH`, `HOME`, `SSH_AUTH_SOCK`, `LANG` only). Failure stops preparation (`setup-failed`). Per start `setup: run\|skip` (default run). |
| `setupStart` | `wait` | `wait` \| `background`. Not built: `background`; the agent always starts after setup. |
| `scripts.teardown` | `null` | Runs at session finish before worktree removal; same environment as setup. |
| `cleanup.keepWhen` | `["uncommitted","unpushed","teardown-failed"]` | Reasons to keep a worktree at teardown: `uncommitted` (dirty, Fleet links excluded), `unpushed` (commits on no remote branch; not checked on delivery, when the accepted work may sit on a deleted, squash-merged branch), `teardown-failed` (the teardown script failed). Otherwise it is removed and the branch deleted with `git branch -d`; after the task is accepted ([worktree on delivery](designs/fleet-system-management.md#worktree-on-delivery)) with `-D`, since a squash-merged branch is unmerged locally. |
| `integration.mode` | `pr` | `pr`: branch, pull request, merge after checks. `direct`: push to the default branch. |
| `integration.mergeMethod` | `squash` | `squash` \| `merge` \| `rebase`. |
| `integration.autoMerge` | `true` | Fleet merges the pull request itself after checks pass (`gh pr merge --<method>`); `false` leaves it open for a reviewer or the owner. |
| `integration.syncCheckoutAfterMerge` | `true` | After a verified merge each environment host fast-forwards its clean checkout (`base-updated`); see [Checkout sync](#checkout-sync). |
| `identity` | `null` | `{name, email}` for `GIT_AUTHOR_*`/`GIT_COMMITTER_*` of agent and delivery commits. |
| `artifact_locations`, `artifact_rules` | `null`, `[]` | Hosts that must hold the project's artifacts and retention rules ([data foundation](designs/fleet-data-foundation.md)). |

Not built: `integration.mode: direct` for agent sessions (`session_deliver` always opens a pull request; `direct` applies to artifact delivery only); `integration.mode: pr` on GitLab (the program worker uses `gh`); a merge method read from the repository default; `project_create` and a project editing UI (projects are files in Git).

Example (`config/projects/fleet-hello-world/project.json`, shortened):

```json
{"id":"fleet-hello-world","name":"Fleet Hello World","contextFiles":["rules.md"],
 "repos":[{"id":"main","remote":"git@github.com:taskqorg/fleet-hello-world.git","forge":"github","defaultBranch":"main","connection":"fleet-hello-world"}],
 "environments":[{"hostId":"owner-mac","checkoutPath":"/Users/kirs/Projects/fleet-hello-world"}],
 "roles":{"pm":"pm","worker":"claude-worker","reviewer":"website-reviewer"},
 "worktree":{"branchPrefix":"fleet","linkPaths":[".env.local"],"sharedDirectories":["node_modules"]},
 "scripts":{"setup":"mkdir -p dist"},
 "integration":{"mode":"pr","mergeMethod":"squash","autoMerge":true},
 "identity":{"name":"fleet-bot","email":"fleet-bot@users.noreply.github.com"}}
```

### Failures are visible

The system helps and never stalls silently. Setup failure, expired credential or binding, teardown failure, unknown effect, merge conflict, dirty checkout: each is recorded as an event on its execution with a reason code and is readable by people and agents through the same API and MCP. Nothing is retried or deleted silently; a kept worktree waits for a later cleanup procedure.

Not built: a single attention view over these failures that an operator, architect or supervisor agent can take and assign. Its model (new entity or existing execution states and events) is decided with the data model.

### Checkout sync

The coordinator never touches host checkouts. After a verified merge it sends `base-updated {sha}` to each environment host of the project. The executor (`go/executor/checkout_sync.go`) fetches and fast-forwards a checkout only if it is clean; otherwise it reports `checkout-dirty`, `fetch-failed` or `not-fast-forward` and leaves it untouched. Known property: these `git fetch` and `git merge` run inside the host checkout under that repository's own `.git/config` (hooks are off, system and global config are not read). A checkout is the operator's, so its config is trusted like the checkout itself; an agent with write access to the checkout's `.git` could set a command there that this sync later runs.

### Not decided

Needs seen in the owner's repositories with no setting yet: submodules (pilotq); per-worktree ports for parallel dev servers (cs, abuse-bot); a canonical check command `scripts.check`; deploy environments per branch with an explicit-request flag for production (cs `main` DEV, `live` PROD); a per-start `baseRef` for hotfixes; containerised harness wrappers; auto-merge per pull request source (bot versus agent). Production deploy and releases stay separate actions on an explicit owner request.

## Project setup

1. Write `config/projects/<id>/project.json` and `rules.md`: goal, context files, roles, repos, environments, identity, integration.
2. Bind each repo to a connection; ask an architect for a missing one (missing access is a question).
3. Prepare each environment in its own terminal: clone the repo to `checkoutPath`, install toolchains, place the connection credential. Fleet never accepts the secret.
4. Bind harness accounts per role on the chosen hosts (`account_*` operations; a missing binding is a login handoff).
5. Run `project_readiness {projectId}`; read results with `execution_status`.
6. Run a smoke task: edit, commit with the project identity, push the branch, deliver.

### Repository access

Credentials stay on the environment where the native tool reads them; Fleet records only which connection a repo uses. The executor gives each execution only the project's credential: the SSH key `<FLEET_CONNECTIONS_DIR>/<connection>/id_ed25519` with `<FLEET_CONNECTIONS_DIR>/known_hosts` (`go/executor/git_access.go`); the ambient SSH agent and `~/.ssh` config are not used for project git. Agent and delivery git runs non-interactive, with hooks off and cleared environment. Projects on one OS user can still read each other's files through a shell; full separation needs a separate environment.

Not built: GitHub App tokens, fine-grained PAT helpers, `gh` keyring bindings, package registry and cloud credentials per connection, and interactive secret requests.

### Readiness

`project_readiness` sends one read-only `readiness` command to each environment host (`go/executor/readiness.go`). Each check is `ok | failed` with a reason code; no raw stderr or secret leaves the host.

- `checkout`: `checkoutPath/.git` exists.
- `remote`: `git ls-remote --exit-code <remote> HEAD` with the connection's credential; reasons `ls-remote-{network|auth-denied|repo-missing|head-missing|unclassified}-exit-<code>`, `invalid-remote`.
- `setup`: a setup script given as a path exists in the checkout.
- `accounts`: each host account bound for a role harness is `ready`.
- `claude-placement` and `claude-login` for projects with a Claude role ([Host settings: Claude placement](#host-settings-claude-placement)).

Not built: push dry-run or token permission check, `gh auth status`, registry and cloud whoami, toolchain versions, scratch worktree create and remove, coordinator-side checks that context files and role profiles resolve.

## Consistent preparation

Prepare the selected agent definition and skill content from canonical files on every host; the same selection uses the same content everywhere. Machine copies are not configuration sources. Adapt local paths and harness wiring without changing instructions.

Fleet owns the execution configuration. Personal harness configuration, instructions, memory, skills, MCP servers and hooks never enter implicitly. Record selected sources and effective settings, separating supplied settings from observed application (`preparation` event: requested and effective). Built-in harness behaviour is version-specific and is not shown as Fleet-authored.

An installed harness executable may be reused. Account access is supplied separately from personal behavioural configuration; credentials never appear in profiles or history. Configuration independence is not an OS filesystem boundary.

Later launches use current definitions unless a version or draft is selected; edits are not injected into a running session. Supplying an instruction does not prove it was followed: assess adherence from run history, tool calls, results and checks.

## Different kinds of settings

| Kind | Resolution |
|---|---|
| Defaults and preferences | Profile and role, then project settings, then explicit task choices; explicit choices override defaults. |
| Requirements | Task, project and method requirements combine. Incompatible requirements need resolution, not silent replacement. |
| Authority and operational controls | Grants, budgets, eligibility and resource restrictions apply; task prose does not widen them. |
| Observations | Availability, measured load, reported quota and uncertainty are shown apart from configured policy. |

Method parameters describe the selected method, not another precedence layer: a method's required Blender capability is a placement requirement; its default quality is an overridable parameter. Free-form instructions stay text; structure only what Fleet must select, route, invoke, account or recover.

## Effective configuration

For planned work, resolve an inspectable preview from current definitions, project differences, explicit assignment choices and allocation rules; at launch, retain the actual selection. Show what was selected, why, and each setting's source; mark unresolved choices and unobserved prerequisites. The preview does not guarantee future availability. The view is derived, not an editable master: edit the source or the assignment. People and agents get the same explanation.

Not built: the effective-configuration preview; the launch record is the `preparation` event.

## Changes and scope

Ordinary sessions may request configuration changes. Resolve the target and scope: this assignment, project defaults, a shared definition or current resource routing; ask only when ambiguity changes the effect. Show the applied scope and source diff. Definition changes affect later launches; live control is a separate operation. Temporary allocation controls keep the previous setting for explicit restoration.

Architects manage shared definitions and grants. Operators manage project work and granted controls; authorised agents use the same operations. A conversation's role label grants no authority.

## Capabilities, methods and tools

| Concept | Meaning | Example |
|---|---|---|
| Capability | Expected outcome with an input and result contract. | Create a 3D model from a description and optional references. |
| Method | A way to provide the capability. | Direct generation API; an agent using Blender; generation with review. |
| Tool | The callable interface that requests it. | An operation taking a description and returning model files and check results. |

These need no separate services. Every method satisfies the advertised contract, including formats and check results. `config/catalogue.json` lists the methods of a capability and the pipeline each runs ([fleet.md](../fleet.md#tools-and-pipelines)).

### Catalogue scope

Entries are shared or project-specific. Project pipelines compose shared methods with project requirements, tools and checks; extract a shared method when reuse warrants it. People and agents see shared entries and their project's entries, not other projects'. Availability decides usable methods; recommendations guide the choice.

### Pipeline development

Develop pipelines in ordinary sessions: discuss, edit definitions, run trials, compare. Assess methods and select project or system recommendations in the [Lab](designs/fleet-lab.md). One-off work follows [Project context](#project-context).

### Pipeline execution

A pipeline is a process the system runs; the caller supplies inputs and gets results without coordinating steps. Steps call APIs through tools, run programs, launch agents or invoke pipelines; results return through the calling chain. An agent step gets an assignment and chooses its actions; expose a stage only when it needs coordination, comparison or repetition. Start coarse; split a step when its result, alternatives, reruns or resource use need separate control. A known procedure runs as a program without an agent. Improving the current result does not change the definition for future runs; that is a separate action.

A single-step pipeline is valid; a simple API call needs no pipeline. Direct tools and pipelines share invocation and observation. MCP exposes tools to agents; a tool may call an API or run a pipeline. Provider configuration, accounts and key pools belong to connections.

### Results and transfer

A capability declares named required and optional outputs. An output holds data or references material in its source system, with type and description; Fleet need not copy it. Downstream steps bind inputs to these outputs and the system makes the material reachable in the receiving environment within permissions. Availability and verification are separate: keep the artifact and its check report even when it needs rework. Code results reference repository changes with a diff. Result viewers are in the [operator app](designs/fleet-operator-app.md).

### Discovery, selection and fallback

Agents discover capabilities by outcome and get contracts, usage conditions, a recommended method and alternatives, each with purpose, limits, requirements and known quality, time and cost; unknowns are marked.

Selection: explicit task choice, else project recommendation, else system recommendation (`recommendationSource`: `project-selected`, `project`, `shared`). Routine calls do not ask the operator for a provider.

A capability may permit fallback methods: when the primary is unavailable use an eligible authorised fallback and report the substitution; without one, offer alternatives. Never replace an explicitly selected method without permission. An unconfirmed original outcome does not authorise another attempt through a different method. Retain selected methods, substitutions, effective settings, internal executions and results.

Not built: fallback methods and substitution reporting.

### Review and rework

A definition may branch on a review result and repeat rework up to a stated iteration limit; the system runs the transitions. A review requesting changes is a workflow outcome, not an execution failure. At the limit, return the artifact and its review outcome; exhausting attempts proves neither quality nor acceptance.

Not built: review branches and rework loops in pipeline definitions.

## Host settings: Blender

Lab Blender script steps (`scripts/lab/blender/run.sh`) run `FLEET_BLENDER`, else `blender` on `PATH`, headless. A pipeline pins such steps to a host with Blender through the step's `host` ([asset pipelines §4](designs/fleet-asset-pipelines.md#4-asset-pipelines-s4)).

## Host settings: Claude placement

Claude runs only where the host allows it ([Lab](designs/fleet-lab.md)). The setting is `FLEET_CLAUDE_PLACEMENT` in the executor environment on that host:

| Value | Allowed on | Host |
|---|---|---|
| `mac` | macOS | owner-mac |
| `windows-wsl` | Linux under WSL, detected from `/proc/sys/kernel/osrelease` naming Microsoft | windows (WSL user `alexk`) |
| unset | nowhere: Claude is refused | PDS18 |

A value that does not match the platform is refused (`claude-placement-platform-mismatch`). A WSL host with `windows-wsl` reports platform `win32`, so Claude gates treat it as Windows. Readiness reports `claude-placement` for projects with a Claude role.

On WSL the Claude login lives under Fleet's config directory: the account's `nativeHome` (Claude's `CLAUDE_CONFIG_DIR`) is `~/.config/fleet/claude`, the login file `~/.config/fleet/claude/.credentials.json`. Readiness checks only that `<nativeHome>/.credentials.json` exists for each Claude account in the host's native registry (`claude-login`); it never reads it. The owner logs in once with `CLAUDE_CONFIG_DIR=~/.config/fleet/claude claude` as `alexk`. The coordinator learns the binding from one host observation `POST /api/hosts/accounts` with the host secret: `{accountId, hostId, binding: "fleet-claude", platform: "win32", readiness: "ready", reason: "ready"}`; on the public host this path is behind Access, so it is sent on loopback. An account keeps one binding per host; work goes to the first ready host in the order session host, `owner-mac`, native hosts, unless a pipeline run sets `host`.

## Host settings: connections

A host needs no connection files. A run granted an API key connection gets its token and tools in the start command; the executor needs `FLEET_GATEWAY_URL` (PDS18: `http://127.0.0.1:8742`; other hosts: `https://fleet.ufobe.com` with the Access service token in `FLEET_CF_ACCESS_FILE`, default `~/.config/fleet/cf-access-machine.env`). The coordinator needs the same `FLEET_GATEWAY_URL` for pipeline api steps. Git keys of repo connections live in `FLEET_CONNECTIONS_DIR` ([Repository access](#repository-access)).

## Coordinator settings: API keys

API key connections ([connections](designs/fleet-connections.md)) keep key values in `FLEET_SECRET_DIR` (PDS18: `/home/fleet/.config/fleet/secrets`): `connections/<connection>/<fingerprint>`, one value per file, mode 0600. Add keys on the API keys screen (owner) or move a one-key-per-line file there with `scripts/connections-import.sh <file> <connection> <secret dir> fleet` as root; the coordinator registers new files at start. The coordinator writes the gateway snapshot (no values) to `FLEET_GATEWAY_DIR/snapshot.json` and ingests the gateway spool from `FLEET_GATEWAY_DIR/state/spool`. The gateway (`deploy/infrastructure/fleet-gateway.service`, user unit of `fleet`) reads `/home/fleet/.config/fleet/gateway.json`. The nightly backup encrypts the secret directory to `FLEET_BACKUP_AGE_RECIPIENT` (`/home/fleet/.config/fleet/backup-age.env`, with `FLEET_SECRET_DIR`).

## People and identity

The guard (`fleet-gateway guard`, `go/gateway/guard.go`) verifies the Cloudflare Access JWT and returns `Authorization: Bearer <browser secret>` and `X-Fleet-Person: <verified e-mail>`; Caddy strips a client-sent `X-Fleet-Person`. The coordinator reads `FLEET_PEOPLE_FILE` (required), the `people.json` of [people and access](designs/fleet-people-access.md): `[{email, name, roles, owner?, grants: [{kind, id}]}]` with exactly one `owner: true`. A browser request without the header is 401; an e-mail not in the file is 403; a host request with the header is 403.

## Hosts

Each host has its own secret. Make it on the host with `scripts/host-secret.sh <file>` (0600, prints only the SHA-256) and register the hash as the owner: `hosts_register {id, name?, holder?, platform?, roles?, harnesses?, secretSha256}`; `hosts_rotate_secret {id, secretSha256}` replaces it and the old secret stops at once. Roles: `inspector-for:<hostId>` (account inspection and integration for that host), `publication` (delivery). The executor reads `FLEET_HOST_SECRET_FILE`; `FLEET_HOST_ID` is only a label: the coordinator derives the host from the secret and answers 403 to a different body `hostId`. The program worker config names `hostSecretFile`. The executor's project roots are `FLEET_PROJECT_ROOTS` (`:`-separated absolute paths).

## Example: csgo

A shared developer profile provides development instructions and tools; the csgo project references it and adds game requirements and checks. A character method references shared generation tools and a csgo rig adaptation; its steps declare capabilities, not machine names. An assignment supplies references and any explicit method choice; a Mac-only check needs a Mac environment. Reserving Claude for required work is allocation policy, not a change to every profile; a Codex alternative must still meet the role, skills, tools and requirements. The person sees one explanation of planned or actual work without browsing every source.
