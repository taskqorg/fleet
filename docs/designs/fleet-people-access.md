# Fleet people and access

People, identity, per-host secrets, holders, subscriptions and their logins, isolation layers, director and take, dispatch, intake, readings and budgets. API keys, the gateway and call methods are in [fleet-connections.md](fleet-connections.md). People, grants and `people.json` extend [fleet-data-foundation.md](fleet-data-foundation.md) §10; host settings extend its §11; per-host stores its §3.

Section numbers §1-§12 are cited by code; keep them.

## Principle

- Fleet guides and shows; it does not hard-code bans. Harness placement, fallback order and which subscription may serve which project are **settings** with clear visibility. Provider terms notes are **warnings** next to the setting they concern.
- One rule is architecture, not a setting: **a login stays on the host where its person signed in.** Fleet never stores, copies or relays subscription OAuth tokens or session credentials. Work goes to the login; the login never moves to the work.
- No invented numbers: no concurrency caps, default thresholds or queue bounds. Every limit is an owner setting. Refresh contention or a provider rejection is an observed failure, not a pre-emptive gate.
- Fewer than 10 people. No rule language or rule engine: settings are fields and ordered lists. No fine-grained RBAC: two roles, grants and holder.

## Terms

- **Person:** an e-mail in `people.json`. **Owner:** the one entry with `owner: true` (Alex); has access to everything.
- **Role:** `operator` (works in granted projects) or `architect` (also hosts, Lab, connections, accounts, recovery, settings).
- **Grant:** permission for a person to see and work in one project or object. Only about visibility.
- **Holder:** the person a host, subscription, API key connection, store or project belongs to.
- **Host:** a machine running an executor (owner Mac, owner Windows, PDS18, Alina's Mac, Simon's PC in WSL2). PDS18 runs the coordinator, Postgres, the main store and the nightly backup.
- **Environment:** a credential store on a host: an OS user, a container or a VM. WSL is a Linux environment separate from its Windows host.
- **Subscription:** one person's harness plan (Claude, Codex, Antigravity, another ACP harness), used through the provider's own CLI on the holder's host. Record kind `managed-account`.
- **Binding:** subscription × environment: one private profile root on that host. Credential kind (`interactive-login`, `copied-file`, `long-lived-token`, `api-key`), readiness, last check.
- **Director:** the person whose work an execution is (`directed_by`). Set by the coordinator only.
- **Take:** a subscription holder accepts another person's task and becomes its director.
- **Intake:** what a host or subscription accepts now: `all` or `paused`.
- **Dispatch:** the moment Fleet picks host and subscription for queued work. **Route decision:** its record. **Wait reason:** why queued work has not started.
- **Reading:** the latest provider limit value seen for a subscription, with its time. **Limit period:** a provider's 5-hour or weekly period.
- **Budget:** an owner-set limit on a scope with a unit and an action (`warn`, `pause`, `stop`).
- **Needs-you question:** a `question` row of kind `operator_decision` in the operator's needs-you list.

## Architecture

```text
 browser ──Access JWT──▶ guard ──X-Fleet-Person: <email>──▶ coordinator
                                                            │  person, grants (people.json)
                                                            │  SET LOCAL fleet.project_ids, fleet.person
 executor ──host secret──▶ coordinator ─ /api/hosts/poll ─▶ dispatch(host)
   (each host)                                              │  1 trust  2 director  3 placement
                                                            │  4 intake  5 readiness  6 order
                                                            │  7 budgets  8 claim  9 record
                                                            └─▶ route_decision, command or wait reason
```

## Design

### 1. Identity

- **Guard** (`fleet-gateway guard`, `go/gateway/guard.go`) verifies the Cloudflare Access JWT for any e-mail and returns `Authorization: Bearer <browser secret>` and `X-Fleet-Person: <verified e-mail>`. No owner e-mail check in the guard.
- **Caddy** (`deploy/infrastructure/fleet.caddy.candidate`) strips any client-sent `X-Fleet-Person` at site level next to `-X-Dashq-Subject`; `forward_auth` copies only `Authorization` and `X-Fleet-Person`. Machine routes (`@machine`) skip the guard. The coordinator listens on loopback only.
- **Access** allows the group `fleet-people`, edited by hand.
- **Two credential kinds never mix.** A person: browser Bearer plus `X-Fleet-Person`. A host: its host secret and no `X-Fleet-Person`; a host-secret request with the header is 403. A host secret equal to the browser secret is refused.
- **Coordinator.** A browser request without `X-Fleet-Person` is 401. An e-mail not in `people.json` is 403. The request context carries the person.
- **`people.json`** (`FLEET_PEOPLE_FILE`, `packages/coordinator/people.ts`): `[{email, name, roles, owner?, grants: [{kind, id}]}]`, exactly one `owner: true`. Loaded into tables `person` and `person_grant`; re-read when the file changes. A malformed file keeps the last valid load (also across restarts, from the `person` rows) and raises one needs-you question with the parse error. With no valid load, every browser request is 403.
- **`me`** returns e-mail, name, roles, owner flag and `grantsVersion`: a hash of roles, owner flag and grants, not of the name. The operator app clears its offline cache when `grantsVersion` changes (`app/src/offline.ts` `checkGrants`).
- **Loopback scripts** that call the coordinator with the browser Bearer send `X-Fleet-Person` from `FLEET_PERSON` (`scripts/lab-examples.ts`).
- **Actors.** Every audit `actor` is the person's e-mail; agents act as their execution ID; program workers as `program-worker`. Literal `'operator'` as a role (`askedBy` of agent questions) stays.

### 2. Visibility

- **Scope per transaction.** `store.setScope` gives the scope of the current request person, read per transaction. The owner, hosts, timers and program workers keep the connection's `*` scope and run no extra statement. Any other person gets `SET LOCAL fleet.project_ids` (their project grants; an architect also gets `''`, rows without a project) and `SET LOCAL fleet.person`. A scoped query outside a transaction runs in one. Host and account choice inside a person's request runs unscoped (`store.unscoped`). The change stream reads each wake in its viewer's scope.
- **Holder rows.** Row-level security on `document` also shows `host`, `managed-account` and `api-connection` rows whose `holder` is `fleet.person`; `store` rows carry a `holder` column with the same clause. `host-observation` rows carry the host's holder. A holder sees their own host and store without the architect role.
- **Operations per role.** Architect operations: `account_*`, `lab_*`, `connection*`, `recovery*`, `reconcil*`, `transport_*`, `artifact_rule*`, `profiles_list`, and routes `/api/lab`, `/api/accounts`, `/api/recovery*`, `/api/reconciliation`, `/api/profiles`. An operator call is 403. `hosts_list` and `stores_list` are operator operations; row-level security filters them.
- A write outside the scope (row-level security error 42501) is 403.
- Project settings and context come from config files, outside row-level security: a request naming a `projectId` the person has no grant on is 404; `project_list` lists only granted projects.
- `managed-account` is global (holder clause); `pipeline-run` and `project-account` carry `project_id`.
- **Held-row exception.** An operator works with a subscription they hold without the architect role: `account_list`, `account_create`, and any `account_*` operation whose `accountId` names a subscription they hold.

### 3. Hosts: records, secrets and trust

- **Host record** (`packages/coordinator/hosts.ts`): document kind `host` `{id, name, holder, platform, roles, secretHash, intake, harnesses: {<name>: {allowed}}, createdAt}`, stored under id `host:<id>` (document ids are unique across kinds; `host-observation` uses the bare id). Config keeps only the coordinator's own host ID. The coordinator creates no host rows by itself.
- **Roles:** `inspector-for:<hostId>` (runs account inspections and integration for that host) and `publication` (runs delivery). A host without a role runs agent work.
- **Secret per host.** Only the SHA-256 is stored. `scripts/host-secret.sh <file>` writes a new secret on the host itself into a 0600 file and prints only its hash; the owner calls `hosts_register {id, holder, roles, harnesses, secretSha256}` or `hosts_rotate_secret`. No secret leaves its host. Without `secretSha256` the coordinator mints a secret and returns it once. Rotation stops the old secret at once. Both operations are owner only.
- The executor reads `FLEET_HOST_SECRET_FILE`; the program worker reads `hostSecretFile` from its config. `FLEET_HOST_ID` is a label only.
- **Derived host.** The coordinator finds the host by the secret hash on every `/api/hosts/*` request; a body `hostId` that differs is 403. A host whose holder is no longer in `people.json` is refused.
- **Trust of a held host.** `maySee(host, projectId)`: a host held by the owner sees every project; a host held by someone else sees only projects its holder has a grant on. Applied at command delivery in `/api/hosts/poll`, artifact reads (`hostMayRead`) and account observations (`/api/hosts/accounts` needs an owner-held host). Fleet treats a held host as fully readable by its holder (data foundation §9).
- **Harness placement** is the host field `harnesses.<name>.allowed`: a harness may run on a host unless it is `false`. `hosts_update {id, intake?, harnesses?}` (holder or owner) changes intake and placement, one `setting-change` row per field. The executor's `FLEET_CLAUDE_PLACEMENT` (`mac`, `windows-wsl`) is the host's record of how Claude is installed and reports a mismatch as readiness, not as a rule. Claude is not allowed on PDS18 because no subscription is bound there; a corporate plan on PDS18 needs one setting change.
- **Work without a subscription** runs on the host's own native login, so it runs only when the host's holder is the director (409 otherwise).
- Not built: revocation. When a person's `grantsVersion` changes, stop running executions of dropped projects on hosts that person holds, remove their run tokens from the gateway snapshot, and raise one needs-you question per dropped project naming repository keys delivered to that host.
- Not built: repository keys through `maySee` (no repository-key path exists in the coordinator).
- Not built: a binding of subscription S on host H with `S.holder ≠ H.holder` shows "<holder> can read this login" at binding time and in every route decision. A warning, not a ban.

### 4. Subscriptions

- **Record** `managed-account`: `name`, `harness`, `holder` (person e-mail; the creating person, only the owner names another), `kind` (`personal`; `team_seat` later), `plan`, `intake` (`all`, `paused`; §7), `take: {projects}` (§5), `switchPct` (§6; only for harnesses whose readings carry a percent), `comment`, `bindings` (per host: `binding`, `platform`, `readiness`, `roots`, `lastCheck`). Fleet stores only binding metadata: the paths `HOME`, `CODEX_HOME`, `CLAUDE_CONFIG_DIR`, `GEMINI_HOME`, never credential values. A binding belongs to one subscription only.
- Operations: `account_create`, `account_list`, `account_hosts`, `account_discover`, `account_connect`, `account_login_handoff`, `account_update {accountId, intake?, plan?, comment?, switchPct?, take?, holder?}` (holder or owner; only the owner changes `holder`; one `setting-change` row per changed field, shown in `account_list` history). Login handoff shows the exact executable, argv and environment for that host and never accepts codes or tokens; the person completes the native flow on the host, then Recheck.
- **Harnesses** `codex`, `claude`, `antigravity` and `acp`. Kind `acp`: the host binding (the native accounts registry entry) gives `executable`, `args` and `environment`, with `{nativeHome}` replaced, served by the same ACP session code, so another ACP agent joins by binding, not code. An `antigravity` entry without `args` uses the built-in binding (`go/executor/harness_acp.go`; argv in `harness.rs`: `agy acp --gemini-dir … --app-data-dir antigravity-cli --include-default-customizations=false`). No binary hash pinning.

#### Logins per harness

Log in once per subscription per environment that runs it. The table states provider behaviour that bindings rely on; code enforces only the per-binding roots. The holder signs in on their own host under the account's OS user (data foundation §9). Fleet shows the command; codes and tokens never pass through Fleet.

| | Codex | Claude Code | Antigravity |
|---|---|---|---|
| Credential | `$CODEX_HOME/auth.json`, file store, home 0700 | macOS Keychain item keyed by the exact `CLAUDE_CONFIG_DIR` path; `.credentials.json` 0600 on Linux, Windows or a locked Keychain | Shared native store `gemini/antigravity` per OS user |
| Isolation per subscription | One `CODEX_HOME` plus private `HOME` | One `CLAUDE_CONFIG_DIR`, one canonical absolute path for login and runs, same `USER` and same session type (GUI or SSH) | None inside an OS user: one subscription per OS user, container or VM; `GEMINI_HOME` isolates behaviour only |
| Login command | `codex login --device-auth` (headless) or browser | `claude auth login --claudeai` | `agy acp --login`; replaces the OS user's principal for every consumer |
| Copy | Only a one-time seed of `auth.json` to an environment that then refreshes alone; never two live copies (rotating refresh token) | Never copy an interactive login. A `claude setup-token` (one year, model requests only, no connectors) may serve several bindings; it lives only in the host's protected env file | Never |

- Two subscriptions of one harness side by side on one OS user work for Codex and Claude (two homes); Codex ran two in parallel and 10 parallel turns on one subscription with no throttling. Antigravity needs another OS user or container per subscription; `AGY_ACP_FORCE_FILE_STORAGE=1` does not separate logins on 1.5.3. Evidence: [isolation tests](../../evidence/layered-isolation-20261109/results.json), [second Codex routing](../../evidence/second-codex-routing-20261109/sanitized-proof.json), [Antigravity binding compare](../../evidence/antigravity-readiness-20261109/binding-compare.json).
- Same-subscription runs in parallel are allowed; a refresh failure surfaces as binding reason `login-absent`. Not built: routing `login-absent` as a question to the holder.

#### Isolation layers

| Layer | Owns | Never shared |
|---|---|---|
| Environment (OS user, container, VM) | Credential store, GUI desktop, `TMPDIR`, ports | Keychain or GUI across environments |
| Binding | Credential and refresh, native session store | Two live copies of one credential; personal homes `~/.codex`, `~/.claude`, `~/.gemini` |
| Harness | Executable, built-in prompt and tools | Version recorded per run |
| Agent profile | Instructions, skills, MCP, tool grants, model, effort | Not inherited from the harness global config or a parent directory |
| Session | Working directory, native thread, transcript | One working tree written by two live sessions; resume across bindings |

- Each run: `env_clear()`, then only `PATH`, `TMPDIR`, `LANG`, `SSL_*` (Codex also `LC_ALL`), `USER`, `LOGNAME`, `HOME` and the binding root. Profile settings are passed as flags per process, never written into a binding home.
- Codex: `--ignore-user-config --ignore-rules`, `project_doc_max_bytes=0`, `skills.enabled=false`, `mcp_servers={}`, disabled memories, hooks, plugins, apps, multi-agent, browser and computer use, image generation; profile MCP as `-c mcp_servers.<id>.*`; model and effort from the profile (default `gpt-6.1-sol`/`low`).
- Claude: `--setting-sources ""`, `--strict-mcp-config`, `--disable-slash-commands`, explicit `--tools`, `--system-prompt`; `HOME` is the credential home (Keychain).
- `FLEET_SESSIONS_ROOT` puts session directories outside the user's home (Mac: `/Users/Shared/fleet/<host>/sessions`, 0700), so no ancestor `.claude/` or `CLAUDE.md` is found.
- Optional session `workingDirectory` under `FLEET_PROJECT_ROOTS` gets a git worktree `<session>/worktree` on branch `fleet/<session>`, git run with cleared env and no hooks. At session finish the teardown script runs, then the worktree is removed unless dirty or teardown failed; the branch is deleted only if merged.
- Every run emits a `harness` event: version, built-in tool list (actual for Codex; Not built: actual list for Claude and ACP), Fleet MCP tools, cwd, worktree, platform. Codex collaboration tools (`spawn_agent` and others) cannot be removed by any tested flag; profile `collaborationTools:false` disables multi-agent, and the recorded list shows the result. A child agent runs in the parent's binding and is visible only inside the parent's events.
- Computer use is disabled; parallel GUI agents need one environment each.

### 5. Director and take

- **Director.** `task_create` and `session_create` set `directedBy` from the request person (the owner for timers and workers). A session created by an agent or pipeline run inherits it from the creating execution or run. Only the coordinator writes it. `execution.directed_by`, `task.directed_by`, `task.requested_by`, `task.taken_from` are nullable; rows from before slice 4 have none. Not built: Lab trials inherit the experiment creator.
- **Rule.** A personal subscription serves only work whose director is its holder. Helping is a take, not a shared login. Until §6 dispatch exists, `enqueue` applies it: `managed.resolve` refuses (409) a subscription whose holder is not the director unless its `take.projects` lists the project; `pickAccount` ranks the director's subscriptions first, then take-rule ones, then the project's `subscriptions` setting (ordered names or IDs).
- **Take:** any person with a grant on the task's project may take it; the take is one `setting-change` row on the task. Not built: the owner taking on a holder's behalf with notice to the holder.
  - **Per task:** `task_take {taskId}` writes `directed_by = taker` and `taken_from = previous` on the task; its executions that have not ended follow.
  - **Always for listed projects:** the subscription's `take.projects`. A take-rule candidate ranks after every candidate of the current director. When it wins, the execution's `directedBy` becomes the holder, `takenFrom` the previous director, and the session's task is taken, after the start command is written in the same request.
- **Effects.** `requested_by` keeps the original director. Not built: a terms note on automatic takes (the subscriber doing work for someone else's project is the provider-safe case only while the requester gets no login, token or way to drive the session); person-scope budgets counting against the new director, stall questions to the new director, telling the requester, and the owner taking on the holder's behalf with notice.
- The owner's subscriptions on other people's projects, and Simon and the owner taking each other's tasks, are this rule. `take.projects` ships empty: nothing is taken until the holder lists a project.

### Status (slice 5)

*   **Project without the Mac.** `config/projects/desiign` (`Amoenum/desiign`, no repository connection: git runs with the host's own access, like `cs` (organization deploy keys are off), `integration.mode: direct`, environments `owner-mac` and `windows`; roles follow the project's D09: `worker` Claude, `designer` Codex). On the deployed coordinator, Alina is an operator in `people.json` (outside the repository) with a grant on `desiign` only, and `account_update` gives the owner's subscriptions `take.projects: ["desiign"]`, so her work runs on his plans. Her Mac, store and file locations come later.
*   **Live (2026-11-10, release `3ec7127e5ac1`).** Alina is in `people.json` (operator, `project:desiign`) and in the Cloudflare Access group `fleet-people`, so she signs in at `https://fleet.ufobe.com`. The owner's seven subscriptions have `take.projects: ["desiign"]`. Each host's `FLEET_PROJECT_ROOTS` lists its desiign checkout. Checks: as Alina, `project_list` returns only `desiign`, `/api/projects/cs` is 404 and `/api/accounts` is 403, while the owner sees every project. A task she directed ran on `claude-owner` on `owner-mac` through take (`directedBy` the owner, `takenFrom` Alina) and completed. Gap: `task.taken_from` stays empty; only the execution records the take.

### 6. Dispatch

Not built (all of §6). Today `enqueue` picks a host at queue time (§5 rule) and `managed.resolve` answers 409 for an unusable choice.

`dispatch(pollingHostId)` runs inside `/api/hosts/poll`, in its transaction, for queued agent executions, oldest first. Queue time records only constraints: project, harness, profile, director, priority, an optional explicit subscription or host. Queued means `state = 'queued'` and `host_id IS NULL`, with a partial index. A candidate is a pair (subscription, host where it has a binding), over all hosts.

1. **Trust.** Skip hosts whose holder may not see the project (§3).
2. **Director.** Keep subscriptions held by the director, then those whose `take.projects` lists the project (§5).
3. **Placement.** Skip pairs whose harness is not allowed on that host.
4. **Intake.** Skip a paused host or subscription.
5. **Readiness.** Login ready on that host; host online (poll within the last 30 s, the existing `connectivity` rule); project files reachable or copyable.
6. **Order.** Director's candidates before take-rule candidates; then the project's `subscriptions` order list (replaces `project-account` and `account_preference`), else the director's subscriptions in creation order. An explicit choice is a list of one. Among one subscription's hosts, hosts holding the project's files first, then host creation order. Skip a subscription whose latest reading is at or over its `switchPct`; a reading past `resetsAt` counts as unknown. Without `switchPct` or a percent, skip only after the provider rejects.
7. **Budgets.** A reached `pause` or `stop` budget covering the execution skips it; `warn` adds a warning.
8. **Claim.** The best candidate is the first unskipped pair. If its host is the polling host, claim with `SELECT … FOR UPDATE SKIP LOCKED` and a `host_id`-null-guarded update, then write the start command. Otherwise leave it for that host's poll. With no pair left, set `execution.wait_reason` to the first-ranked pair's skip reason.
9. **Record** `route_decision {executionId, at, hostId, candidates: [{subscription, host, skipped?, warnings}], winner?}` when the execution is claimed or its wait reason changes.

- **Terms warnings**, never enforced: two personal subscriptions of one provider and one holder in one order list; Antigravity over ACP; Claude on a host held by someone other than the subscription holder.
- An unusable explicit host or subscription makes the execution wait with that reason; it no longer fails the request.
- Fleet never balances across subscriptions to stretch limits; the owner may list subscriptions in any order. One host may win the whole queue; intake and the order list are the controls.

**As built (slice 6).** `enqueue` writes the execution with no host and a `dispatch` record (harness, project, explicit subscription, explicit host, last host, input); `dispatch.ts` runs at the start of every `/api/hosts/poll`, inside its transaction, and the start command is built at claim from the session as it is then. Candidates: the explicit subscription (or the codex default binding, the owner's configured default) as a list of one; otherwise the director's subscriptions, then take-rule ones, then the project's `subscriptions` order, then the host's own login (only for the host's holder). Hosts: the explicit one, else the session's last host, the coordinator default, then host creation order. Skips in order: trust, host intake, the subscription checks of `managed.resolve` (director, intake, placement, ready binding), online (a poll within 30 s), budgets. Not built yet: "hosts holding the project's files first" (slice 5 file locations) and the switch point (slice 7 readings). The claim needs no row lock: the poll runs under the coordinator's GLOBAL write lock. A queued execution that is stopped before dispatch is settled as stopped at once. Lab experiment budgets stay in Lab for now; the `budget` table carries person and project budgets, and spend is measured for `wall_ms` and `tokens` only (other units are unverifiable and never enforced). Questions gain `addressed_to`, readable by that person under row-level security; the owner-override notice (G4) uses it.

### 7. Intake and stall alerts

- **Intake** (`all`, `paused`) is a field on `host` and `managed-account`. A paused subscription is skipped by `pickAccount` and refused (409) as an explicit choice at the next start; running work is never touched.
- Not built: host intake taking effect (needs §6); `urgent` and `projects:<ids>` modes (wait for a priority-aware scheduler).
- Every change of intake, placement, take, `switchPct`, plan, comment, holder or a task's director writes one audit row `setting-change {target, field, previous, value, actor, at}`, visible to holders under row-level security. Not built: telling the holder when the owner overrides.
- Not built: **stall alert.** When dispatch leaves a queued execution with no candidate, upsert one open `operator_decision` question with a unique `cause` (`host:<id>:paused`, `subscription:<id>:limit`, `files:<store>:offline`, `budget:<id>:reached`), addressed to the director and naming the holder of the cause. Five tasks with one cause give one question; it closes when a dispatch finds the cause gone. Web push later.

### 8. Readings

Not built (all of §8); `quota` is reported as `unknown`.

- The executor forwards Codex `rate_limits` (used percent, limit period, `resets_at`) and Claude `rate_limit_event` (status, reset time, no percent) as `limit_reading` outbox events.
- They land in `execution_totals.rate_limit_last` and a per-subscription latest reading `{at, pct?, status?, period, resetsAt}` on the binding.
- Screens show a reading's age; no reading is "unknown", never zero. A subscription's provider limit is a reading, not a budget.

**As built (slice 7).** The executor turns Codex `account/rateLimits/updated` (it carries no thread, so it was dropped) and Claude `rate_limit_event` into `limit_reading` events `{harness, readings: [{period, pct?|status?, resetsAt}]}`. The latest reading per subscription and period is read from those events (joined to the execution's `account_id`), so host ingest writes nothing outside its execution; `execution_totals.rate_limit_last` takes the run's last one. Readings are accepted only from the host the execution runs on, and a percent outside 0-100 is ignored. `subscription_limits` serves the Hosts page panel "Subscription limits"; `switchPct` skips a subscription in dispatch with "switch point N %; reading M % (period), K min old". Also with this slice: the executor reports its `FLEET_PROJECT_ROOTS` in each poll (`projectRoots`), and dispatch skips a host where the project's checkout sits outside them with that reason, instead of a failing run; an automatic take on task-linked work records `task.taken_from`.

### 9. Budgets

Not built beyond Lab: experiments have `budgets` in `lab/experiments.ts`.

```text
budget { id, scope: person | project | experiment | connection, scopeId,
         unit, limit, period?: day | week | month, action: warn | pause | stop,
         setBy, setAt }
```

- Units: `usd`, provider `credits`, `tokens`, `trials`, `wall_ms`. Lab `pipe_calls` and `pipe_credits` become `calls` and `credits` with the connection in `scopeId`.
- Spend: sum of `connection_cost` measurements and token totals over scope and period, each with basis (`measured`, `estimated`, `unavailable`) and age. An unmeasurable unit shows "unverifiable".
- Default action: `warn` for person, project and connection scopes; Lab experiments keep `pause`; `stop` only when the owner sets it.
- Storage: a `budget` table; Lab's experiment `budgets` move into it with `scope = experiment`.
- Enforcement: the coordinator computes reached budgets; dispatch applies them to agent runs; the gateway applies them to API calls from a flag in its snapshot ([fleet-connections.md](fleet-connections.md) §6) and keeps the last flags while the coordinator is down.
- **Ask for more** creates an `operator_decision` question with the budget ID; the owner's answer writes the new limit, audited with both people.

### 10. Files on a person's machine

- Built: a project's `artifact_locations` lists stores by host; retention rule changes need confirmation (`placement.confirmRule`).
- Not built: a location change shows a preview (what moves, what is deleted where) and needs the project holder's or owner's confirmation.
- Not built: a new project gets `pds18` in `artifact_locations` next to the holder's store by default, so nightly backups continue; the holder may drop it after the preview.
- Not built: an execution needing files on an offline store waits with "files on <host> (offline)" instead of expiring after `artifactRetryWindowMs`.

### 11. Repositories and delivery

- Built: per repository `integration.mode: pr | direct`.
- Not built: `delivery.access: deploy-key | bot-collaborator | holder-rights` per repository. Recommendation shown: deploy key plus `direct` in a person's organization with no bot; bot collaborator plus `pr` when reviews are wanted. The owner's GitHub and private GitLab keep today's path. Holder rights run on the holder's host with their own git login and need no Fleet-held credential.

### 12. Screens

Built from `packages/ui` only, per `docs/design-system/owner-rules.md`.

- Built: hosts list and stores list filtered by row-level security.
- Not built: **Subscriptions** (`/subscriptions`): holder, harness, plan, intake, reading with age and reset countdown, hosts, take rule, comment, terms warnings. **Hosts:** holder, intake, placement, secret rotation. **Task and execution:** directed by, taken from, connection, route decision. **Project settings:** order list, file locations with preview, repository delivery. **Budgets** with spend, basis, age and Ask for more.

## Slices

Slices 1-4 land before any non-owner host joins.

| Slice | Content | State |
|---|---|---|
| 1 Identity | guard header, owner flag, `person`, actors | built |
| 2 Host secrets and trust | `host` rows, per-host secret, derived host, `maySee` | built |
| 3 Visibility | scope per transaction, holder clause, role tags | built |
| 4 Holder and director | subscription fields, `directed_by`, `requested_by`, take, removal of Claude 403s, `setting-change`, `acp` | built |
| 5 Alina's Mac | onboarding runbook, store, deploy key, file locations with preview | not built |
| 6 Dispatch | late choice, claim, route decision, stall questions, intake, budget shape | not built |
| 7 Readings | `limit_reading`, limits view | not built |
| 8 Simon's PC | WSL2 executor with Claude allowed | not built |

Tests: `tests/coordinator/people.test.ts` (slice 1), `host-secrets.test.ts` (slice 2), `visibility.test.ts` (slice 3), `director.test.ts` (slice 4).

## Provider terms

Sources reviewed 2026-11-10; not legal advice. They drive the warnings above.

- **Provider-safe:** own subscription, own machines, unmodified vendor CLI, work the person directs; several machines and parallel agents are fine within weekly limits. The owner's agents doing tasks for another person's project, while that person gets no login, token or way to drive the session.
- **Risky:** two people on one subscription or pooled accounts in one hub (Anthropic and OpenAI no-sharing clauses); Fleet storing or relaying OAuth tokens (Anthropic forbids intermediating Claude.ai credentials); round-robin across accounts to beat limits; third-party proxies for Claude or Antigravity OAuth (Google treats any third-party access to Antigravity as a breach and has banned accounts); datacenter or VPN sign-ins (reported, not a rule).
- **Alternatives:** Claude Team or ChatGPT Business seats; per-person or per-project API keys with budgets ([fleet-connections.md](fleet-connections.md)).
- Codex: OpenAI documents copying `auth.json` to headless machines but recommends API keys for automation.

## Open points

- Q1: confirm `take.projects` ships empty for the owner's subscriptions, rather than "on for projects he has a grant on".
- Q2: Simon and the owner get the same take rule as the owner and nothing more until use shows a need.
- Q3: holder of API key connections: every key is the owner's today; a person's own key would use the same `holder`.
- Q4: delivery per forge: the GitHub bot user and the private GitLab path need a check of which credentials Fleet would hold.
- No Team or Business seats now; `kind` allows them later. No native Windows executor: Simon's PC uses WSL2.
