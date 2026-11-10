# People, access, subscriptions and connections

* **Author:** Alex Kirs
* **Date:** 2026-11-10
* **Status:** Brainstorm brief (EGM, 7 lenses). Input for data foundation amendments and one new design, `fleet-connections.md`, which replaces Pipe (see Key Decisions).

## Reviewer status

| Lens | Status |
| --- | --- |
| `egm-design` | completed |
| `egm-codebase-details` | completed |
| `egm-zoom-out` | completed |
| `egm-comprehension` | completed |
| `egm-implementation-readiness` | completed |
| `egm-structural` | completed |
| `egm-inventive-catalyst` | completed |

Notes on inputs:

* The seven lenses reviewed the people, access and subscriptions concept. Two later inputs were applied by the synthesizer and were **not** reviewed by the lenses: the terms research ([reviews/people-access/terms-research.md](reviews/people-access/terms-research.md)), now hard constraints in "Terms constraints", and the owner's scope extension (Fleet's own connections system, replacing Pipe), now the "Connections" part. Run an EGM review on `fleet-connections.md` before building it.
* `docs/designs/legacy-inventory.md` does not exist in this worktree, so the old accounts and projects pages were not compared.
* `packages/coordinator/connection_call.py` does not exist yet. It is planned in `fleet-lab.md` Stage 2 item 21. Today's client is `packages/coordinator/pipe_search.py`, called from `projects.ts:12-64`.
* No key or token files were opened.

## Overview

Fleet today has one person, the owner, and reaches outside APIs through Pipe, a separate service in the assetq repo. This brief covers two connected changes.

**People.** Let a few more people (Alina, Simon; fewer than 10 in total) work in Fleet:

* sign in by e-mail through Cloudflare Access and see only their own projects;
* attach their own machine and its file store;
* use their own subscription for their own work;
* see spend and provider limits, and ask for more budget;
* let the owner steer hosts and subscriptions with a few instant controls, and know when the queue waits because of a setting.

**Connections.** Fleet gets its own connections system and Pipe is deleted:

* a registry of providers and connections (subscriptions, API keys for Tavily, Meshy and model APIs);
* keys held by Fleet, with rotation;
* one accounting record for every call;
* HTTP, MCP and CLI call methods served from Fleet infrastructure;
* per-run tokens instead of hand-made grants.

The two changes share one model. Every subscription and every API key is a **connection**. Every use of a connection is an **execution** with measurements. One choice point picks the connection, and one budget shape applies to people, projects, experiments and connections.

Most of the people side is already designed in [fleet-data-foundation.md](fleet-data-foundation.md): §10 has `people.json`, `person`, grants, `share` and row-level security by `fleet.project_ids`; §3 has `artifact_locations` and per-host stores. Most of the connections side is designed in [lab-brief.md](lab-brief.md) "Pipe in Fleet" and Pipe's own [read lane](/Users/kirs/Projects/assetq/docs/read-lane.md). What is missing is small but load-bearing:

1. a **holder** on hosts, connections, stores and projects;
2. a **"directed by"** person on each execution, so the terms can be checked;
3. **per-host credentials**, because a person's machine is not trusted like the owner's;
4. **one late choice point**, `dispatch`, where Fleet picks host and connection when work starts, not when it is queued;
5. **limit readings** from the harnesses, which the executor throws away today;
6. **a Fleet gateway** with per-run tokens, which takes over Pipe's job.

## Terms constraints

These are hard constraints. They come from the [terms research](reviews/people-access/terms-research.md). They are the only places where Fleet refuses rather than guides, because the rule is the provider's, not Fleet's.

* **T1. A person's subscription runs only on that person's machines, through the unmodified provider CLI.**
  * Fleet never stores, copies between people, or relays anyone's OAuth tokens or session credentials.
  * Work goes to the machine that holds the login. Credentials never move to the work.
  * The holder signs in on their own machine; Fleet never performs or proxies the login.
  * This already matches FD5 (credentials in the account user's home on the host) and `harness.rs:109-126` (`native-current` credential home must be owned by the executor's user). Keep it. It rules out a CLIProxyAPI-style central hub for subscriptions.
* **T2. No pooling and no sharing.**
  * Several people's subscriptions are never pooled behind one endpoint.
  * Two people never use one subscription.
  * A personal subscription runs only work **directed by its holder**. This is the one hard filter in `dispatch`.
  * Work the owner directs for Alina's or Simon's project may run on the owner's subscriptions; the research rates this safe as long as they get no login, token or way to drive the owner's session.
* **T3. Switching accounts is not a way around provider limits.**
  * Moving work between one person's own subscriptions or harnesses is a scheduling choice. It stays visible on every route decision and is configured by the owner. Fleet never adds it on its own.
  * It is a gray area. OpenAI forbids circumventing rate limits [terms-research §1, OpenAI (c)]. Anthropic's limits "assume ordinary, individual usage" and it names round-robin to beat limits as risky [§1, Anthropic (a), §4]. The routing screen shows this note next to any list that orders two personal subscriptions of the same provider and person.
  * Fleet never balances across accounts by "resets soonest" (the gist's strategy) and never spreads work round-robin.
  * For shared or heavy automated runs, the preferred fallback is an **API key connection with a budget**, or a **Claude Team / ChatGPT Business seat**. These are under commercial terms.
* **T4. Antigravity is the strictest provider.** See the next subsection.
* **T5. Keys Fleet holds are API keys only.** Fleet holds provider API keys (Tavily, Meshy, OpenAI API, Anthropic API) in its secret store. It never holds subscription OAuth tokens, and its gateway never relays them.

### How Fleet runs Antigravity today

* The executor starts the host's Antigravity CLI as a child process in its `acp` mode: `--gemini-dir <native home>`, `--app-data-dir antigravity-cli`, `--include-default-customizations=false` (`crates/executor/src/harness.rs:270-280`).
* Fleet acts as an Agent Client Protocol client. It identifies itself as `clientInfo: {name: "fleet"}` (`crates/executor/src/antigravity.rs:28`).
* With `credentialAuthority: native-current`, `HOME` is set to the native credential home, so the CLI uses its own login (`harness.rs:109-126, 270-272`).
* Fleet does not read, copy or relay the Antigravity OAuth token. No code was found that calls Google's services directly with that token.

**Flag (gray, leaning risky).** Google's terms call "using third party software, tools, or services to access the Service" a breach, with OpenClaw on Antigravity OAuth as the example [terms-research §2]. Fleet does not use the token outside the client, but it does drive the official client from third-party software over ACP. Google has banned users of third-party tools and makes a second violation permanent. Two further gaps:

* The executable path comes from host configuration (`harness.rs:133-138`). Fleet does not check that it is Google's unmodified binary.
* Lab and routing could send heavy automated runs to Antigravity.

**Recommendation (open question 4):** keep Antigravity on the owner's own account and machines only, never as an automatic fallback, until Google documents ACP or headless use as permitted. Pin the executable's hash in the host record. For heavy runs, use a Gemini API key connection instead.

Claude and Codex are driven through their unmodified CLIs (Claude Code over `stream-json`, `harness.rs:142`; Codex CLI). The research treats this as ordinary Claude Code and Codex use on the holder's own machine.

## Analysis

### What exists in code today

| Need | What exists | Gap |
| --- | --- | --- |
| Identity | Access checks e-mail at the edge. `deploy/infrastructure/origin_guard.py:19` accepts only `FLEET_ACCESS_OWNER` and replaces the token with one shared Bearer (line 35). | The coordinator never sees an e-mail. `me` returns `config.ownerEmail` (`app.ts:272`). Every `actor` is the literal `'operator'` (`accounts.ts:13,18`, `projects.ts:70-71`). |
| Visibility | Row-level security on every table (`db/schema.sql:171-348`). | `fleet.project_ids` is `'*'` for the whole pool (`store.ts:20`). The `store` policy allows only `'*'` (`schema.sql:336`), so a scoped person would see no stores, not even their own. |
| Subscription | `managed-account` is one provider login with per-host `bindings` (`accounts.ts:12,20`). | No holder and no kind (personal, team seat). Quota is the constant `'unknown'` (`accounts.ts:17,20`). |
| Hosts | Fixed list in config. `owned()` checks the claimed `hostId` against it (`app.ts:66`). | No host row and no host holder. All hosts share one `machineSecret` (`app.ts:57-59`). |
| Where files live | `artifact_locations` per project (`projects.ts:37`). The artifact placement worker copies to reachable listed stores and never deletes the last copy (`placement.ts:42-54`). | Fits as is. Executions that wait for files on an offline machine expire after the retry window (`placement.ts:83`). |
| Host and account choice | `enqueue` (`app.ts:122-133`) loops hosts at queue time. `pickAccount` (`app.ts:135`) takes the project preference or the first ready account. | The choice is fixed at queue time and ignores limits, priority and person. `account_preference` accepts only `hello-world` and `hello-world-alt` (`accounts.ts:13`). |
| Controls | `account.enabled`, audited as `account-action` (`accounts.ts:18`). | No host control, no "urgent only" and no "dedicated" mode. |
| Priority | Classes `urgent`, `normal`, `background` (`tasks.ts:33-35`). | Stored only; no scheduler acts on them. |
| Limits and spend | Token totals including `tokens_cache_read` and `tokens_cache_write` (`schema.sql:111-126`, `harness.rs:367-396`). The column `rate_limit_last` exists (`schema.sql:118`). `connection_cost` and `connection_cost_basis` exist on totals (`schema.sql:119`). | Nothing writes `rate_limit_last`. The executor drops Codex `rate_limits` and Claude `rate_limit_event`. |
| API calls | `pipe_search.py` loads Pipe's client library and calls `web_search` only (`pipe_search.py:24-28`). `projects.ts:54-64` accepts a one-step `pipe-read` pipeline and maps errors to `quota`, `unavailable`, `unknown`. `tool_run` exists as an execution kind (`schema.sql:34`, `executions.ts:33,108`). | No registry, no Fleet-held keys, no per-run token. Agent calls are attributed by a free-form label. |
| Notification | Questions of kind `operator_decision`; `detectProblems` (`app.ts:138`). | No push channel. |
| Readiness | `crates/executor/src/readiness.rs:55-64` reports login per account. | No holder, quota or person. |

Name trap: `packages/coordinator/placement.ts` places **artifacts**, not tasks. Task placement is in `app.ts` `enqueue` and `pickAccount`.

### Risks, by severity

**Blockers (must land before any non-owner machine joins):**

* **B1. Any host can pose as any host.** There is one shared `machineSecret`, and `owned()` and `stores.ts:27` trust the `hostId` in the request body. Alina's Mac could poll as PDS18 and receive its commands, presigned links and repo keys. She is an administrator on her Mac, so she can read whatever her executor receives.
  * Fix: one secret per host, stored as a hash, with `hostId` derived from the secret.
  * A host held by someone else receives only work, keys and files of projects its holder has access to.
* **B2. The coordinator does not know who is calling.**
  * Fix: the guard keeps verifying the Access JWT and passes the verified e-mail in a header. Caddy strips that header from client requests.
  * The coordinator maps the e-mail to a `person` or answers 403.
  * The Access app moves from one e-mail to the group `fleet-people`.
  * Every audit `actor` becomes the person.
* **B3. The terms rule has nothing to check.** Accounts have no holder and executions have no "directed by", so a fallback would silently run Alina's work on Simon's Max plan.
  * Fix: `connection.holder`, `connection.kind`, and `execution.directed_by`. Child executions inherit `directed_by`; the coordinator sets it, never an agent.
  * One hard filter in `dispatch` (T2).
* **B4. No late choice point.** Host and account are fixed at queue time (`app.ts:124-129`). So "start nothing new on the Mac", limits and offline machines cannot affect queued work.
  * Fix: queued work carries constraints, and `dispatch()` picks host and connection on each host poll.

**Major:**

* **M1. Limit data does not exist.** Charts, switching and "only one fallback left" need the executor to forward two signals:
  * Codex `rate_limits`: percent, window, `resets_at`.
  * Claude `rate_limit_event`: status and reset time, no percent.

  A reading is only as fresh as the account's last run. Show its age, and treat "no reading" as unknown, never as zero or "has room".
* **M2. Simon's PC cannot run Fleet today.**
  * There is no Windows executor; that is a data foundation non-goal.
  * Claude bindings are allowed only on `darwin` or `win32` (`harness.rs:140`, `accounts.ts`), and WSL2 reports `linux`.
  * FD5 on Windows needs `CreateProcessWithLogonW` with a stored password.

  Cheapest path: the Linux executor in WSL2, next to the existing Garage store. Change the Claude rule from "platform is Mac or Windows" to "host is not PDS18".
* **M3. Older records hide their project in JSON.** `managed-account`, `pipeline-run` and `project-account` keep the project inside `data`. Once `fleet.project_ids` is scoped per request, rows with an empty `project_id` either vanish for everyone or leak. List every record kind and its `project_id` before the visibility slice.
* **M4. Personal store and backups.** With `artifact_locations: ["alina-mac"]`, placement deletes the PDS18 copies once her copy is verified, and those files leave the nightly backup.
  * Changing a project's file location needs a preview and owner confirmation, like retention rules.
  * While the machine is off, executions that need its files wait instead of expiring.
* **M5. GitHub for a repo in Alina's own organization.** A deploy key gives clone and push, but cannot open pull requests. No GitHub API credential was found in the coordinator. Either start `desiign` with `integration.mode: "direct"`, or Alina adds a Fleet bot user as a collaborator.
* **M6. The gateway is a single point of failure.** If agents' web reads depend on the coordinator, a coordinator restart stops every agent's research. Pipe solved this with a separate process and no database on the request path (`pipe.md:54-56`). The Fleet gateway must copy that (see Connections).
* **M7. Document conflicts.** Amend these in the same pull request as the new design:
  * The data foundation non-goals forbid "admission caps of any kind" and defer the gateway (Q9).
  * The operator app forbids push notifications.
  * Budgets are defined twice (`fleet-lab.md` and `lab-brief.md` decision 10).
  * `lab-brief.md` "Pipe in Fleet" keeps Pipe as a gateway (P0-P4). The owner's extension supersedes that.
* **M8. Stall alerts can spam.** Five waiting tasks must produce one alert per cause, closed automatically when the cause clears.

**Nits:**

* `docs/designs/README.md` does not list the Lab docs.
* The operator app hard-codes one owner in `me` and `grantsVersion:'1'`.

### Contradictions resolved

| Contradiction | Resolution |
| --- | --- |
| Configurable rules, yet never block | Settings **rank** candidates and never fail work. Only the terms rules exclude. If no candidate is left, the execution goes to `waiting` with a reason (the existing `waitReason`) and raises one question. The 409 paths in `resolve()` become wait reasons. |
| Owner controls everything, yet a person controls what is theirs | Ownership is a column, `holder`. You may change a row you hold, and the owner may change any row. Every write is audited with its actor, and the holder is told when the owner overrides. There is no permission engine. |
| Tasks may use other subscriptions, yet personal ones are never shared | `directed_by` plus the one hard filter. "Other connections" means the person's own subscriptions, team seats, API key connections, or owner subscriptions for owner-directed work. |
| Automatic switching, yet no way around limits | Switching between two personal subscriptions of one person is never automatic by default. The owner must list both, and the list shows the terms note. The default fallback is a different kind of connection: an API key with a budget, or a seat. |
| Approximate accounting, yet visible budgets | Every number shows its basis (`measured`, `estimated`, `unknown`) and its age. Provider limits are the real hard limits, and Fleet only reads them. Owner budgets warn and ask, and stop only if the owner chose that action. |
| Files on a personal machine, yet tasks run elsewhere | Rank hosts that hold the project's files first, so Simon's project, files and Claude end up on one machine without a rule. Otherwise the existing copy path applies, and an offline machine means waiting. |
| Instant controls, yet no invented limits | Controls are modes, not numbers. The coordinator applies them at dispatch, so they act at once. Running work is never touched. |
| Keys held centrally, yet calls must survive a coordinator outage | The gateway is a separate process. It serves from a last-known-good snapshot of keys and token hashes, and spools call records locally (copied from Pipe's read lane). |
| Automatic choice, yet predictable | Each dispatch records a `route_decision`: the candidates, why each was skipped, and which won. The routing screen shows these explanations plus the ordered list that produced them. |

## Connections

This part replaces Pipe. It was written from the owner's scope extension, [lab-brief.md](lab-brief.md) "Pipe in Fleet", [fleet-lab.md](fleet-lab.md) §Connections, `pipe_search.py`, and Pipe's [pipe.md](/Users/kirs/Projects/assetq/pipe.md) and [read-lane.md](/Users/kirs/Projects/assetq/docs/read-lane.md). The lenses did not review it.

### Model

* **Provider:** a company or service: Anthropic, OpenAI, Google, Tavily, Meshy. It holds the facts about the service: units it bills in, where to read its usage, and its terms class (subscription OAuth, API key).
* **Connection:** one way to use a provider. Kinds:
  * `subscription`: today's `managed-account`. The credential stays on the holder's host (T1). It is used by an `agent_run`.
  * `team_seat`: a Claude Team or ChatGPT Business seat. Same mechanics as `subscription`, but its holder may be the organization.
  * `api`: a pool of one or more API keys held by Fleet (T5). It is used by `tool_run` calls (Tavily, Meshy) or, later, as a model endpoint for a harness.
* Each connection has a holder, an access list (people, projects, experiments allowed to use it), an intake mode and budgets.
* **One accounting path.** Every call is an execution: `agent_run` for harness turns, `tool_run` for API calls. Cost is a `connection_cost` measurement (amount, unit, basis), linked to the execution, its project, its `directed_by` and its experiment. This is the record already fixed in data foundation §Non-Goals line 141.

`managed-account` becomes `connection` with kind `subscription`. Storage stays a document kind or table as the data foundation prefers; there are no two parallel registries (open question 1).

### Keys

Copied from Pipe and "the key move" in `lab-brief.md`:

* Values live in the coordinator's private secret directory (0600, coordinator user). They are backed up with SOPS/age off-host, and restoring needs only that copy and the age identity.
* Postgres holds metadata only: key ID, 12-character fingerprint, connection, state (`active`, `draining`, `disabled`, `retired`), added date, disable reason.
* **Rotation:** add the new key, then mark the old one `draining` (no new calls), then retire it after the owner confirms or after an observed quiet period. No downtime.
* **Failover only on no HTTP answer.** Quota replies and 429 pass through unchanged with `Retry-After`. The gateway never switches keys to get past a quota (T3).
* **401, 402 or 403 disables that key.** The key stays disabled across restarts and raises a problem in Fleet, and only the owner re-enables it. If no usable key is left, the call refuses with `no-usable-key`.
* **Reconcile usage per key** against the provider's own usage figure, as Pipe's reconcile does. A positive difference means another client shares the key. Show it on the connection.
* Executors, agents, profiles and screens never receive provider keys.

### Gateway and call methods

* **One gateway process, `fleet-gateway`,** on PDS18, separate from the coordinator. It is the only holder of key values at run time, loaded at start, the way Pipe uses `LoadCredential`.
* **No database on the request path.** It authenticates against a snapshot that the coordinator publishes: token hashes, connection access lists, key states. A missing or stale snapshot never stops it; it serves the last valid one and reports its age. It drains on restart and keeps the socket open, so callers see a delay, not a refusal.
* **Records never block a call.** Each call is appended to a local spool. The coordinator ingests the spool as `tool_run` rows with `connection_cost`, and a persistent loss counter shows records that could not be stored.
* **No Fleet-invented queue bound.** Pipe has a bounded queue with `queue-full`. Fleet does not copy a fixed bound. Provider limits pass through, and an owner may set a bound on a connection if they want one.
* **Three call methods**, all thin clients of the same gateway:
  * **HTTP:** `POST /v1/call/<connection>/<tool>` on loopback on PDS18 and through the Cloudflare-guarded entry elsewhere (the `fleet-machines` service token plus the per-run token).
  * **MCP:** a Fleet MCP server that the executor adds to each run's MCP config for the connections the profile grants, and allowlists `mcp__<connection>__<tool>`. This replaces `pipe_mcp.py` and the planned `connection_call.py mcp`.
  * **CLI:** `fleet call <connection> <tool>`, body on stdin. Used by coordinator-side `api` steps and scripts. This replaces `pipe_search.py`.
* **Per-run tokens.** When an execution starts, the coordinator mints a token bound to the execution ID, its `directed_by`, its project, its experiment and the connections its profile grants. It expires when the execution ends. The gateway attributes each call to that execution with no label to trust. Child executions get their own tokens. This replaces Pipe's project grants, the grant snapshot and the shared host token file that same-host agents could read (`fleet-lab.md` line 199).
* **Coordinator-side long jobs** (Meshy submit, then poll) run through the same gateway in `job` mode. The job ID is kept in the step's `execution.data.jobId`, and an uncertain outcome is "unknown, never replay" (`fleet-lab.md:181`, `pipe.md` ASSETQ-R4).
* **Model APIs as connections (later stage).** A harness could run on an API key connection through the gateway as its base URL: Claude Code's `ANTHROPIC_BASE_URL` with the per-run token as the bearer, and a Codex custom provider `base_url`. The gateway injects the real key. This gives T3's preferred fallback (an API key with a budget) without the key reaching the agent. Verify both harnesses accept this before relying on it. It applies to API keys only, never to subscription OAuth (T5).
* **Codex built-in web search.** Pipe's installer disables Codex's built-in `web_search` so reads go through Pipe and are recorded. Fleet already runs Codex with `mcp_servers={}` under a Fleet-owned HOME. The per-run config must keep built-in web off unless the profile says `web_route: builtin`.

## One routing and budget model

### Dispatch

One function, run on each host poll, for both agent runs and API calls (the gateway applies the same rules from its snapshot):

1. **Hard filters (terms and trust):** a personal subscription only for work directed by its holder (T2); a subscription only on its holder's hosts (T1); a held host only for projects its holder can access (B1); Antigravity never as an automatic fallback (T4).
2. **Access:** the person, project or experiment is on the connection's access list.
3. **Intake mode** of host and connection.
4. **Readiness:** login present, host online, files reachable.
5. **Order:** the project's ordered list of connections. Lab and urgent work may override it at their level. Hosts that hold the project's files rank first.
6. **Switch point:** an optional per-connection percent set by the owner. If none is set, switch only on the provider's own rejection. Fleet sets no default.
7. **Record** a `route_decision` (candidates, skip reasons, winner). If no candidate is left, the work goes to `waiting` with a reason, and one question is raised per cause.

The default fallback order the routing screen suggests is: same person's subscription, then an API key connection with a budget or a team seat, then wait. Two personal subscriptions of one provider and one person in the same list show the T3 note.

### Budgets

One shape for everything, extending Lab's existing budget:

`budget { scope: person | project | experiment | connection, scope_id, unit, limit, period?, action: warn | pause | stop }`

* **Units:** whatever is measured for that scope: `usd` (API key connections), provider credits (Tavily, Meshy), tokens, `trials`, `wall_ms`, and `limit_pct` for a subscription's provider limit (read-only: Fleet shows it and never sets it).
* Every value carries its basis and age. Where Fleet cannot measure a unit, the budget shows "unverifiable" instead of a number.
* **Default action:** `warn` for person, project and connection scopes, which follows "guide, never block". Experiments keep Lab's decided default `pause` (R3.7). Open question 9.
* **Budget request:** a person asks for more through a question of kind `operator_decision`, and the owner's answer writes the new limit.
* With API keys as fallbacks, money is now real. Budgets on `api` connections, per person and per project, land in the same stage as the first API key fallback, not later.

### Controls and stall alerts

* **Intake** replaces `enabled` on hosts and connections. Modes: All work, Urgent only, Only `<projects>`, Paused (running work finishes). Ship All and Paused first.
* A **stall alert** is one problem row and one needs-you question per cause, created the moment queued work has zero candidates, and closed when the cause clears. Web push comes later.

## Goals vs Non-Goals

**Goals**

* E-mail sign-in through Access for people in `people.json`. Each person sees only projects they have a grant on.
* Architect views (hosts, lab, settings, connections) are visible only to the `architect` role. Selective lab access is a grant on a lab object.
* The owner can onboard a person's Mac now, and a Windows PC through WSL2, each with an executor and a store. A project chooses which hosts hold its files.
* A holder on hosts, connections, stores and projects. Holders manage their own rows.
* The terms constraints T1-T5 are enforced in one place and shown in every route decision.
* A connection registry for subscriptions and API keys, with keys held by Fleet and rotated without downtime.
* One gateway with HTTP, MCP and CLI methods and per-run tokens, available while the coordinator is down.
* One accounting record (execution plus `connection_cost`) for every call.
* One `dispatch` that picks host and connection late, from an ordered list, and explains the pick.
* One intake mode per host and per connection, applied at once.
* One budget shape across person, project, experiment and connection scopes, with budget requests.
* One alert per stall cause.
* A limits and spend view per connection and provider: provider limit used, reset countdown, reading age, "only one fallback left", spend per person and project, and the cache read share.
* Pipe deleted.

**Non-Goals**

* A rule language or rule engine.
* Concurrency caps, default thresholds, queue bounds or any number Fleet invents.
* A central hub for subscription credentials, copying OAuth tokens between machines, or relaying one person's login for another (T1, T5).
* Load balancing across accounts to stretch limits (T3).
* A native Windows executor (WSL2 path instead).
* Syncing the `fleet-people` group through the Cloudflare API (hand edit for fewer than 10 people).
* A bridge or coexistence period with Pipe.
* Pipe's asset library, generation worker and method registry. Lab already replaces method evaluation; asset storage is Fleet's artifacts.
* Web push in the first slices.
* Account choice per pipeline step.
* The Conversations screen redesign and the PM agent profile.

## Alternatives Weighed

| Topic | Options | Recommendation |
| --- | --- | --- |
| Roles | (a) Fine-grained RBAC per feature. (b) Two roles, `operator` and `architect`, scoped by project grants, plus `holder`. (c) No roles: owner plus shares on objects. | **(b)**. It is already in data foundation §10. (c) is elegant, but makes "hide architect views" a list of shares to maintain. |
| Subscriptions and API keys | (a) Separate `managed-account` and `connection` registries. (b) One `connection` with a kind. | **(b)**. It gives one routing and one budget model. Lab already defines Account as a harness subscription. |
| Gateway placement | (a) Inside the coordinator. (b) Separate `fleet-gateway` process with a snapshot (Pipe's read lane design). (c) Keep Pipe. | **(b)**. (a) makes a coordinator restart stop every agent's web reads. (c) contradicts the owner's decision. |
| Call attribution | (a) Caller-supplied labels (Pipe today). (b) Per-run tokens minted by the coordinator. | **(b)**. It closes the shared-token hole and needs no grants. |
| Key failover | (a) Round-robin. (b) Pipe's rule: switch only on no HTTP answer, pass quota through, disable on 401/402/403. | **(b)**. It is proven and matches T3. |
| Routing | (a) Rule engine, first match wins. (b) Ordered list per project plus an optional owner-set switch percent. (c) Fixed code policy. | **(b)**. It copies the fallback list of CLIProxyAPI and LiteLLM, without their cross-account balancing (T3). An ordered list cannot conflict. |
| When to choose | (a) At queue time (today). (b) At dispatch, on each host poll. | **(b)**. The cost is negligible with fewer than 10 people. |
| Controls | (a) On/off only. (b) Four intake modes. | **(b)** as one field. Ship All and Paused first. |
| Budgets | (a) A new budget system. (b) Lab's shape with a `scope`. (c) Usage only, no budgets. | **(b)**. Budgets for API key connections ship with the first API key fallback. Person and project budgets on subscriptions are usage-only until wanted. |
| Lab account level | Experiment, variant, step. | **Experiment default, variant override** through the existing `overrides`. Record the connection used on each trial and flag "account switched" as a confounder. |
| Windows | (a) Native Windows executor. (b) Linux executor in WSL2. | **(b)**, after one live Claude run under WSL2. |
| Antigravity | (a) Treat like Claude and Codex. (b) Owner-only, never a fallback, pinned binary. (c) Drop it and use a Gemini API key. | **(b)** now. Revisit (c) if Google's position stays unclear. |

## Key Decisions & Staging for Next Steps

**Documents.** One new design plus amendments, in one pull request, so no two documents disagree:

1. Amend [fleet-data-foundation.md](fleet-data-foundation.md):
   * Terms: hosts may be held by other people.
   * §9: per-host secrets and how far a host held by someone else is trusted.
   * §10: holder, `directed_by`, T1-T5.
   * §11: intake modes.
   * Non-Goals: "no built-in caps" in place of "no admission caps of any kind".
   * Q9: answered by `fleet-connections.md`.
2. Write `fleet-connections.md`: provider and connection registry, keys and rotation, gateway and per-run tokens, call methods, limit readings, dispatch and route decisions, intake modes, stall alerts, budgets and requests, limits and spend views, and the Delete Pipe stage.
3. Linked edits:
   * `lab-brief.md` "Pipe in Fleet": mark it superseded.
   * `fleet-lab.md`: Stage 2 calls the Fleet gateway, not Pipe; budgets point to the new design; account becomes a variant override and a confounder.
   * `fleet-operator-app.md`: the notification non-goal, `me`, the access and connection views.
   * `docs/designs/README.md`: list the Lab docs and the new design.
4. Add an onboarding runbook in `docs/` (not `designs/`): executor, store, tunnel, deploy key, per-host secret.

**People slices.** Each slice ends with an acceptance check.

1. **Identity.** The guard passes the verified e-mail, `people.json` loads into `person`, and the Access app uses `fleet-people`.
   *Check:* Alina's token with a forged owner header arrives as Alina; an unknown e-mail gets 403; a direct call without the guard gets 401.
2. **Per-host secrets.**
   *Check:* Alina's secret with `hostId=pds18-native` gets 403 on poll and on store registration.
3. **Visibility.** `SET LOCAL fleet.project_ids` per request from grants; operations listed per role; the `store` policy opened to grant-scoped rows; `project_id` filled on every record kind.
   *Check:* Alina sees only `desiign` rows and gets 403 on connection and lab operations. The owner sees everything.
4. **Holder and terms.** `holder` on host, connection, store and project; `connection.kind`; `execution.directed_by`; the hard filters.
   *Check:* a task directed by Alina never selects Simon's subscription and waits with the reason "terms: personal subscription of Simon". The Antigravity binary hash is checked.
5. **Alina's Mac.** Executor, store, deploy key, the `desiign` project file, and a file-location change with a preview.
   *Check:* readiness is all ok, one task runs on her Codex, and its files reach the chosen store.
6. **Dispatch.** Late choice, ordered list, `route_decision`, wait reasons, one alert per cause, intake All and Paused.
   *Check:* pausing the only connection for a ready task creates exactly one question, and resuming closes it.
7. **Limit readings and the limits view.**
   *Check:* saved Codex and Claude samples produce stored readings with their age. Dispatch skips a rejected subscription and records why. "Only one fallback left" appears when a project has one candidate.
8. **Simon's PC (WSL2).**
   *Check:* a Claude binding from Simon's host is accepted, the same report from PDS18 still gets 403, and one Claude run completes.

Slices 1 to 4 must land before any non-owner machine joins.

**Connections stages.** These run alongside the people slices. C1 needs people slice 4 (`directed_by`) for token scoping.

* **C0. Registry and keys.** `provider` and `connection` records; `managed-account` folded into `connection`. Tavily keys move into the coordinator secret directory, with metadata in Postgres and a SOPS/age backup.
  *Check:* `/connections` lists Tavily, Meshy and every subscription, with state and holder. A restore from the backup copy alone works on a clean container.
* **C1. Gateway with per-run tokens.** `fleet-gateway` on PDS18 serves `web_search` and `web_extract` from a snapshot; spool records become `tool_run` rows with `connection_cost`; executions get per-run tokens; HTTP, MCP and CLI clients ship.
  *Check:* a Lab trial and an agent step both show every web call as a `tool_run` with credits, attributed by token. Stopping the coordinator does not stop a running agent's web reads, and the spool is ingested after restart with a zero loss counter.
* **C2. Key lifecycle.** Rotation (add, drain, retire), disable on 401/402/403 with a problem raised, owner re-enable, and per-key usage reconcile.
  *Check:* one Tavily key is rotated with no failed calls. A key refused with 403 shows as disabled with its reason and is re-enabled only by the owner. The reconcile shows no unexplained difference.
* **C3. Meshy and budgets.** Meshy as a `job` connection; budgets on `api` connections by person, project and experiment; budget requests.
  *Check:* an asset pipeline runs Meshy through the gateway, and its charge appears as a `connection_cost`. A person over a `warn` budget sees a warning and can send a request, and the owner's answer raises the limit.
* **C4. Delete Pipe.** One cut, no bridge and no coexistence period.
  * In Fleet: delete `pipe_search.py`, the `pipe-read` connection, the Pipe branches in `projects.ts`, and Pipe client hash pins.
  * On every host: remove the Pipe MCP server and `~/.config/pipe/read.json`.
  * In assetq, on PDS18 and in Cloudflare: stop and remove the `pipe-read` units, users and directories; remove the read-grant snapshot, the project grants and the `pipe.ufobe.com` read-lane route, the guard exemption and the read-lane probe; revoke Pipe project tokens.
  * In DashQ: remove the shipper enrolment.
  * In assetq `pipe.md`: remove the read-lane section, or close the repo, whichever the owner decides.

  *Check:* `rg -i "pipe"` in Fleet finds no runtime reference. No host has a Pipe MCP server. Calls to the old read-lane route fail. A Lab web-research trial still completes through the gateway.
* **C5 (after verification). Model API keys as harness endpoints.** The gateway acts as the base URL for Claude Code and Codex on API key connections. This makes an API key with a budget the default fallback (T3).
  *Check:* one Claude Code run and one Codex run on an API key connection complete. The key never appears in the run's environment or files, and the cost is recorded.

**Later:** the Urgent only and Only `<projects>` modes with a priority-aware scheduler; charts (shared limit, cache read share, time series); web push; Lab account override.

## Glossary

* **Person:** an e-mail address listed in `people.json`.
* **Owner:** the one person with access to everything (`*` scope): Alex.
* **Role:** `operator` (works in granted projects) or `architect` (also sees hosts, lab, connections, settings).
* **Grant:** permission for a person to see and work in one project, or in one object such as a lab use case. Only about visibility.
* **Holder:** the person a host, connection, store or project belongs to. Holders manage their own rows, and the owner can manage any row. The word avoids a clash with "Owner".
* **Host:** a machine running a Fleet executor ("Alina's Mac", "Simon's PC", PDS18). In the product, say "host", not "machine".
* **Store:** the Garage file store on a host. In the product: "Files on: Alina's Mac".
* **Provider:** a company or service Fleet uses: Anthropic, OpenAI, Google, Tavily, Meshy.
* **Connection:** one way to use a provider. Kinds:
  * `subscription`: one person's plan, used through the provider's CLI on the holder's host. Called "account" in the product (`managed-account` today).
  * `team_seat`: a Claude Team or ChatGPT Business seat.
  * `api`: a pool of API keys held by Fleet.
* **Plan:** the paid tier behind a subscription ("Claude Max $100"). A field, not an entity.
* **Account user:** the OS user on a host that a subscription runs as (FD5).
* **Key:** one API key in an `api` connection. Its state is `active`, `draining`, `disabled` or `retired`.
* **Gateway:** `fleet-gateway`, the process that holds API keys at run time and serves calls.
* **Per-run token:** a credential minted for one execution. It lets that execution call the connections its profile grants.
* **Directed by:** the person whose work an execution is. The coordinator sets it, and child executions inherit it.
* **Terms rules:** T1-T5 in this brief.
* **Provider limit:** a limit the provider enforces.
* **Limit period:** 5-hour or weekly. Say "limit period", not "window", because the app already has windows.
* **Reading:** the latest provider limit value Fleet has seen for a subscription, with its time.
* **Shared limit:** what the concept called "pooled quota": one provider limit counted across models on one plan.
* **Budget:** a limit the owner sets, with a scope, unit, limit and action (warn, pause, stop).
* **Budget request:** a person's question to the owner asking for more budget.
* **Intake:** what a host or connection accepts: All work, Urgent only, Only `<projects>`, Paused (running work finishes).
* **Dispatch:** the moment Fleet picks the host and connection for queued work.
* **Route decision:** the record of a dispatch: the candidates, the skip reasons and the winner.
* **Wait reason:** why queued work has not started, for example "Simon's PC is Paused".

## Open questions for the owner

1. **Fold `managed-account` into `connection`?**
   *Recommended:* yes, one registry with a kind. This gives one routing, one budget and one screen.
2. **Who is "directed by" when Alina's PM agent creates tasks?**
   *Recommended:* Alina. Her tasks run on her subscription, on team seats or on API key connections she has access to, and never on Simon's. They run on the owner's personal subscriptions only when the owner explicitly takes a task on, which makes the owner its director.
3. **May the owner's personal subscriptions serve Alina's project at all?**
   *Recommended:* yes, for work the owner directs; the research rates this safe. Show the connection and "directed by" on the task.
4. **Antigravity: keep, restrict or replace?**
   *Recommended:* restrict. Use it only on the owner's own account and machines, never as an automatic fallback, with the executable hash pinned. Use a Gemini API key connection for heavy runs. Replace it entirely if Google keeps banning third-party clients.
5. **Automatic switching between one person's own personal subscriptions near a limit (for example the owner's two Claude plans)?**
   *Recommended:* only when the owner lists both, with the T3 note shown. Prefer an API key connection with a budget, or a seat.
6. **Buy Claude Team or ChatGPT Business seats for shared work?**
   *Recommended:* not yet. Start with each person on their own plan, the owner's subscriptions for owner-directed work, and API key connections with budgets as the fallback (C5). Buy seats if Alina or Simon direct heavy Claude work and have no plan.
7. **Where does the gateway run?**
   *Recommended:* one `fleet-gateway` on PDS18, as Pipe's lane does. Other hosts reach it through the guarded entry. A gateway per host only if PDS18 availability proves to be a problem.
8. **What happens to the rest of the assetq repo after C4?**
   *Recommended:* delete the read lane and its grants in C4. Archive the repo once nothing else in it is in use (asset storage is already rejected as Fleet's store, and Lab replaces method evaluation).
9. **Default budget action outside Lab?**
   *Recommended:* `warn`, plus a budget request, for person, project and connection scopes. Lab experiments keep `pause` (R3.7). `stop` is only ever set by the owner.
10. **Simon's PC: WSL2 or a native Windows executor?**
    *Recommended:* WSL2, after one live Claude run there proves that login and limits behave. Change the Claude rule to "never on PDS18".
11. **`desiign` delivery: direct push or pull requests?**
    *Recommended:* direct push with a deploy key first. Add a Fleet bot collaborator in her organization when reviews are wanted.
12. **When a project's files move to Alina's Mac, keep a copy on PDS18?**
    *Recommended:* yes. Keep PDS18 in `artifact_locations` alongside her Mac by default, so backups continue. She can drop it, with a preview showing what will be deleted.
13. **Lab account choice level?**
    *Recommended:* an experiment default with a variant override. Record the connection used on each trial and flag a switch as a confounder. No step level yet.
14. **Stall alert channel?**
    *Recommended:* one needs-you question per cause now. Web push later, through the operator app's planned service worker.
15. **Can Alina see her host's and store's status without the architect role?**
    *Recommended:* yes. A holder always sees the rows they hold, whatever their role.

## Owner Decisions, Round 2 (2026-11-10)

These answers close the open questions above. Where they conflict with earlier text in this brief, these win.

**Principle: configuration and visibility, not built-in bans.** Fleet must not hard-code prohibitions such as "Claude never on PDS18", "Antigravity never as a fallback" or "pin the binary hash". Every such rule is a setting the owner controls, with clear visibility of what is allowed where. Terms-of-service notes from the research are shown as warnings next to the relevant setting; they guide and do not block. The architecture rule stays: a login token stays on the host where its person signed in, and Fleet does not copy or relay it.

- **Antigravity:** it runs where the owner configures it: on any of his own machines, on tasks he allows or assigns explicitly, and as a fallback if he enables that. No binary hash pinning. Antigravity is connected over a general agent protocol (ACP); keep that path generic so other harnesses can join the same way (agentq had this; check what Fleet does now).
1. **Two lists, one ownership model.** Subscriptions (harness accounts: Claude, Codex, Antigravity, and other ACP harnesses) and API keys (services) get separate lists and separate screens, because they differ: harness choice, limits, comments, service. Both carry an owner (whose subscription or key it is). Both support selection rules: "only my subscription", "use this one for now", automatic.
2. **Whose work.** Alina, with her own subscription and PM agent, forms the tasks of desiign. The owner can allow his workers on his subscriptions to take that project's tasks: per task, per scope, or always for the project. With Simon, either person may take the other's tasks, under rules they set. More control is added as real use shows the need.
3. **The owner's subscriptions on other people's projects:** yes, and the owner can set this to happen automatically.
4. **Antigravity for heavy runs:** try it and see; no special rule now.
5. **Switching between one person's own subscriptions near a limit:** configurable.
6. **Claude Team or ChatGPT Business seats:** not now.
7. **Gateway:** one, in Fleet, on PDS18.
8. **assetq after Pipe is deleted:** delete the read lane, archive the repo.
9. **Default budget action outside Lab:** warn, and the person can ask for more. Lab keeps pause. Stop only when the owner sets it.
10. **Where each harness may run is a setting, not a rule.** Today: the owner's Claude on his Mac and his Windows; Simon's Claude on Simon's Windows; no Claude on PDS18, because there is no subscription for it there yet. A corporate Claude plan on PDS18 later must need only a setting change.
11. **Repositories and delivery:** projects live in different places (private GitLab, the owner's personal GitHub, organizations such as Alina's GitHub organization). Fleet supports each with configurable access and delivery (deploy key, bot collaborator, the person's own rights), and recommends the best option per case.
12. **Copy on PDS18 when a project's files live on a person's machine:** configurable, on by default.
13. **Lab account choice:** automatic mode, or a specific subscription chosen explicitly, at experiment level with variant override.
14. **Stuck-queue notice:** simplest form first (one needs-you question per cause); improve later.
15. **Alina sees her own host and store status without the architect role:** yes.
