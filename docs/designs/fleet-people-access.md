# Fleet people and access: people, subscriptions, routing, controls and budgets

*   **Author:** Alex Kirs
*   **Status:** Proposed
*   **Date:** 2026-11-10
*   **Brief:** [people-access-brief.md](people-access-brief.md). Its section "Owner Decisions, Round 2" is binding and overrides earlier brief text.
*   **Companion:** [fleet-connections.md](fleet-connections.md) (API keys, the Fleet gateway, call methods, deleting Pipe).

--------------------------------------------------------------------------------

## Overview

Fleet has one person today, the owner. This design lets a few more people (Alina, Simon; fewer than 10 in total) work in Fleet with their own machines and their own subscriptions, and gives the owner a few instant controls over where work runs.

It extends what [fleet-data-foundation.md](fleet-data-foundation.md) already decided (§10 people, grants and `people.json`; §11 host settings; §3 per-host stores). It adds five things:

1.  **Identity per request.** The coordinator knows which person calls.
2.  **One secret per host.** A machine held by another person cannot pose as PDS18.
3.  **A holder** on hosts, subscriptions, stores and projects, and a **director** on each execution.
4.  **One late choice point, `dispatch`.** Fleet picks host and subscription when work starts, not when it is queued, from settings the owner and holders control.
5.  **Readings and budgets.** Provider limit readings from the harnesses, and one budget shape across people, projects, experiments and connections.

### Principle (owner decision, round 2)

Fleet guides and shows; it does not hard-code bans. Rules such as "Claude never on PDS18" or "Antigravity never as a fallback" are **settings** with clear visibility. Provider terms notes appear as **warnings** next to the setting they concern.

One rule is architecture, not a setting: **a login stays on the host where its person signed in.** Fleet never stores, copies or relays subscription OAuth tokens or session credentials. Work goes to the login; the login never moves to the work.

### Owner decisions used here

From the brief's "Owner Decisions, Round 2 (2026-11-10)". Numbers are the brief's.

| # | Decision |
| --- | --- |
| Principle | Configuration and visibility, not built-in bans; terms notes are warnings. A login stays on its host. |
| A | Antigravity runs where the owner configures it, including as a fallback; no binary pinning; keep ACP generic. |
| 1 | Subscriptions and API keys: two lists and screens, one ownership model, selection rules "only mine", "this one for now", automatic. |
| 2 | Alina forms `desiign` tasks; the owner may let his subscriptions take them per task, per scope or always; Simon and the owner may take each other's tasks. |
| 6 | No Team or Business seats now. |
| 7, 8 | One gateway in Fleet on PDS18; delete the read lane and archive assetq ([fleet-connections.md](fleet-connections.md)). |
| 3 | The owner's subscriptions may serve other people's projects, automatically if he sets it. |
| 9 | Budget default outside Lab: warn plus request; Lab keeps pause; stop only when the owner sets it. |
| 10 | Where each harness may run is a setting. |
| 11 | Repository access and delivery configurable per repository. |
| 12 | PDS18 copy of a person's project files: configurable, on by default. |
| 13 | Lab: automatic or explicit subscription, experiment level with variant override. |
| 14 | Stuck queue: one needs-you question per cause. |
| 15 | A holder sees their own host and store without the architect role. |

### Glossary

*   **PDS18:** the Linux server that runs the coordinator, Postgres, the main store and the nightly backup.
*   **Coordinator / executor:** the central Fleet service, and the per-host agent runner that polls it.
*   **Store:** the Garage file store on a host (data foundation §3).
*   **Needs-you question:** a row in table `question` of kind `operator_decision`, shown in the operator's needs-you list.
*   **Lab, experiment, variant, trial:** Fleet's evaluation system (fleet-lab.md): an experiment compares variants of a pipeline by running trials.
*   **Profile:** an agent configuration under `config/agents/` (harness, model, tools, granted connections).
*   **FD5:** data foundation §9: one OS user per harness account; the login lives in that user's home on the host.
*   **Person:** an e-mail listed in `people.json` (data foundation §10).
*   **Owner:** the person with access to everything: Alex.
*   **Role:** `operator` (works in granted projects) or `architect` (also sees hosts, Lab, connections, settings).
*   **Grant:** permission for a person to see and work in one project or one object. Only about visibility.
*   **Holder:** the person a host, subscription, API key connection, store or project belongs to. The word avoids a clash with "Owner".
*   **Subscription:** one person's harness plan (Claude, Codex, Antigravity, or another ACP harness), used through the provider's own CLI on the holder's host. Today's record kind `managed-account`; "account" in older screens.
*   **API key connection:** a pool of service API keys that Fleet holds (Tavily, Meshy, model APIs). Defined in [fleet-connections.md](fleet-connections.md).
*   **Director:** the person whose work an execution is (`execution.directed_by`). The coordinator sets it; agents never set it; child executions inherit it.
*   **Take:** a subscription holder accepts another person's task, which makes the holder its director. Owner decision 2.
*   **Intake:** what a host or subscription accepts now: All work or Paused; Urgent only and Only `<projects>` come later with a scheduler.
*   **Dispatch:** the moment Fleet picks host and subscription for queued work.
*   **Route decision:** the record of one dispatch: candidates, skip reasons, warnings, winner.
*   **Wait reason:** why queued work has not started, for example "Simon's PC is Paused".
*   **Reading:** the latest provider limit value Fleet has seen for a subscription, with its time.
*   **Limit period:** a provider's 5-hour or weekly period. Say "limit period", not "window".
*   **Budget:** a limit the owner sets on a scope, with a unit and an action (`warn`, `pause`, `stop`).

## The Problem

Five gaps in today's code stop a second person from joining. Each one is load-bearing; the line references are from `origin/main` at `f69b5dc`.

*   **Nobody is identified.** The guard accepts one e-mail (`deploy/infrastructure/origin_guard.py`, `OWNER`) and replaces the Access token with one shared Bearer. `me` returns `config.ownerEmail` (`app.ts:330`). Every audit `actor` is the literal `'operator'` (`accounts.ts`, `app.ts:285,309,313`).
*   **Any host can pose as any host.** All hosts share one `machineSecret` (`app.ts:66`). `owned()` (`app.ts:73`) and `/api/hosts/stores` trust the `hostId` in the request body. Alina is an administrator on her Mac, so she can read whatever her executor receives; with the shared secret she could poll as PDS18 and receive its commands, presigned links and repository keys.
*   **Subscriptions have no holder and executions have no director.** `managed-account` has `name`, `harness`, `enabled`, `bindings` (`accounts.ts`). Nothing records whose plan it is or whose work a run is, so nothing can keep Alina's work off Simon's plan.
*   **The choice is made too early and is hard-coded.** `enqueue` (`app.ts:129-142`) loops hosts at queue time and `pickAccount` (`app.ts:145`) takes the project preference or the first ready account. Pausing a host, a limit, or an offline machine cannot affect work already queued. `account_preference` accepts only `hello-world` and `hello-world-alt`. Claude placement is refused with 403 in four places ("never PDS", `accounts.ts`) on top of the host setting `FLEET_CLAUDE_PLACEMENT` (`harness.rs:309-320`); the owner decided this must be a setting only (decision 10).
*   **Limits are invisible.** `quota` is the constant `'unknown'`. The executor drops Codex `rate_limits` and Claude `rate_limit_event`. Lab budgets exist only at experiment scope (`lab/experiments.ts:59`).

## Goals

*   **G1.** A person in `people.json` signs in by e-mail through Cloudflare Access. The coordinator receives the verified e-mail on every browser request and maps it to a `person`, or answers 403.
*   **G2.** Each person sees only the projects they have a grant on, plus every row they hold. Architect views (hosts, Lab, connections, settings) need the `architect` role, except a holder's own rows (decision 15).
*   **G3.** Each host has its own secret. A host held by someone other than the owner receives only work, keys and files of projects its holder has a grant on.
*   **G4.** Hosts, subscriptions, stores and projects have a `holder`. A holder changes their own rows; the owner changes any row; every write is audited with the person as `actor`, and a holder is told when the owner overrides.
*   **G5.** Every execution has `directed_by`. A subscription runs work directed by its holder. A holder can **take** other people's tasks of projects they have a grant on: one task at a time, or always for listed projects (decisions 2, 3).
*   **G6.** One `dispatch` picks host and subscription on each host poll, from settings, and writes a route decision that names every skipped candidate, every terms warning and the winner.
*   **G7.** Where each harness may run is a host setting (decision 10). Today's state (owner's Claude on his Mac and Windows; Simon's Claude on Simon's PC; no Claude on PDS18) is data, and a corporate plan on PDS18 later needs one setting change.
*   **G8.** Intake (All, Paused) on hosts and subscriptions, applied at the next dispatch. Running work is never touched.
*   **G9.** One budget shape for person, project, experiment and connection scopes, and a budget request that a person sends and the owner answers.
*   **G10.** Readings from Codex and Claude, with their age, shown per subscription; "no reading" is shown as unknown.
*   **G11.** One needs-you question per stall cause, closed when the cause clears.
*   **G12.** Onboarding Alina's Mac and Simon's PC (Linux executor in WSL2) with an executor, a store and a per-host secret, from a runbook.

## Non-Goals

*   A rule language or rule engine. Settings are fields and ordered lists.
*   Concurrency caps, default thresholds, queue bounds or any number Fleet invents. Every number is the owner's.
*   Built-in prohibitions on harness placement, fallback or binaries. Terms notes are warnings (round 2 principle).
*   Moving, copying or relaying subscription logins, or a central subscription hub (CLIProxyAPI style).
*   Balancing across accounts to stretch limits ("resets soonest", round-robin). Fleet never invents such an order; the owner may list subscriptions in any order.
*   A native Windows executor.
*   Syncing the `fleet-people` group through the Cloudflare API for people (hand edit, data foundation §10). Share sync stays as §10 defines it.
*   Team or Business seats now (decision 6). The `kind` field allows them later.
*   A priority-aware scheduler. Urgent only and Only `<projects>` modes come with it later.
*   Web push. Charts beyond the limits view. Account choice per pipeline step.
*   API keys, the gateway and deleting Pipe: see [fleet-connections.md](fleet-connections.md).

## Open Questions

*   **Q1. Default "take" rule for owner subscriptions.** Decision 3 says the owner may set his subscriptions to take other people's projects automatically. This design ships the setting with no project listed (nothing is taken until the owner lists a project). Confirm or ask for "always on for projects he has a grant on".
*   **Q2. Simon and the owner taking each other's tasks.** Decision 2 says "under rules they set". This design gives each holder the same take rule as the owner, and nothing more, until use shows a need.
*   **Q3. Holder of API key connections.** Today every key is the owner's. A person's own Tavily key is possible with the same `holder` column, but no one has asked for it.
*   **Q4. Repository delivery per forge** (decision 11). Deploy key, bot collaborator or the person's own rights. This design records the choice per repository and recommends one; the GitHub bot user and the private GitLab path need a separate check of what credentials Fleet would hold.

## Critical User Journeys (CUJs)

### CUJ 1: Alina signs in and sees her project

*   **Given:** `people.json` lists Alina with role `operator` and a grant on `desiign`; her e-mail is in the Access group `fleet-people`.
*   **When:** she opens Fleet.
*   **Then:** `me` returns her e-mail and roles. Lists show only `desiign` rows and the host and store she holds. Connection, Lab and settings operations return 403. An e-mail not in `people.json` gets 403 from the coordinator. A request with no valid Access JWT gets 403 from the guard; a request that reaches the coordinator without `X-Fleet-Person` (it skipped the guard) gets 401.

### CUJ 2: Alina's Mac joins

*   **Given:** the owner registers host `alina-mac` with holder Alina and mints its secret.
*   **When:** her executor polls with that secret.
*   **Then:** the coordinator derives `hostId` from the secret. A poll with her secret claiming `pds18-native` gets 403. Her host receives only commands of projects Alina has a grant on, and readiness shows her Codex subscription as ready.

### CUJ 3: Alina's work runs on her plan

*   **Given:** Alina's PM agent creates a `desiign` task. It is directed by Alina. Her Codex subscription is in the project's order list.
*   **When:** her Mac polls.
*   **Then:** `dispatch` ranks all candidates and her Mac with her subscription wins. The route decision lists the owner's and Simon's subscriptions as skipped with "directed by Alina; not taken by holder". If the owner's Mac polls first, it does not claim the task, because a better runnable candidate exists.

### CUJ 4: The owner takes a task

*   **Given:** a `desiign` task is queued and Alina's Mac is off.
*   **When:** the owner presses Take on that task, or has listed `desiign` in his subscription's `take.projects`, and Alina's subscription is not runnable (her Mac is offline).
*   **Then:** the task is directed by the owner from that moment, audited with the previous director. It runs on the owner's subscription and host. The task view shows "Directed by Alex (taken from Alina)".

### CUJ 5: Pausing a host stops new work at once

*   **Given:** two tasks are queued for Simon's PC.
*   **When:** Simon sets his host intake to Paused.
*   **Then:** the next dispatch skips the host with "Simon's PC is Paused". Running work finishes. If no other candidate exists, each task waits with that reason and exactly one needs-you question is raised for the cause, addressed to the tasks' director and naming Simon as the holder to ask. Setting intake back to All closes the question.

### CUJ 6: A limit is near

*   **Given:** the owner's Codex subscription reported 92 % of its weekly limit 20 minutes ago, and the owner set its switch point to 90 %.
*   **When:** a task directed by the owner is dispatched.
*   **Then:** the subscription is skipped with "switch point 90 %; reading 92 %, 20 min old". The next candidate in the list runs it. If the next candidate is another personal Codex subscription of the owner, the route decision carries the terms warning, and the work still runs.

### CUJ 7: A person asks for more budget

*   **Given:** project `desiign` has a budget `{unit: usd, limit: 20, period: month, action: warn}` on API key spend, and spend reached 20.
*   **When:** Alina opens the project.
*   **Then:** she sees the warning and a "Ask for more" action. It creates a question of kind `operator_decision`; the owner's answer writes the new limit, audited.

## Technical Plan (Architecture)

```text
 browser ──Access JWT──▶ guard ──X-Fleet-Person: <email>──▶ coordinator
                                                            │  person, grants (people.json)
                                                            │  SET LOCAL fleet.project_ids
 executor ──host secret──▶ coordinator ─ /api/hosts/poll ─▶ dispatch(host)
   (each host)                                              │  1. trust: host holder's grants
                                                            │  2. director: subscription holder or take rule
                                                            │  3. harness placement field of host
                                                            │  4. intake of host and subscription
                                                            │  5. readiness, files reachable
                                                            │  6. order list, switch point
                                                            │  7. budgets (action)
                                                            └─▶ route_decision, command or wait reason
```

### 1. Identity

*   **Guard.** `origin_guard.py` keeps verifying the Access JWT. It drops the single-owner check and returns two headers to Caddy: the existing `Authorization: Bearer <browser secret>` and `X-Fleet-Person: <verified email>`. Caddy's `forward_auth` copies only these from the guard response, and strips any client-sent `X-Fleet-Person` before forwarding.
*   **Access.** The Access application allows the group `fleet-people` instead of one e-mail. Hand edit, as §10 says.
*   **Strip everywhere.** Caddy removes any client-sent `X-Fleet-Person` at site level, next to the existing `-X-Dashq-Subject`, so machine routes never carry one. The coordinator listens on loopback only (today's Caddy front).
*   **Two credential kinds never mix.** A person is accepted only with the browser Bearer plus `X-Fleet-Person`; a host only with its host secret and no `X-Fleet-Person`. A host-secret request with the header is 403.
*   **Owner.** `people.json` marks one entry `owner: true` (Alex). Owner-only operations and the `*` scope check this flag. `FLEET_ACCESS_OWNER` is removed from the guard and from `server.ts`.
*   **Coordinator.** A browser request without `X-Fleet-Person` is 401. An e-mail not in `person` (and not a live `share`) is 403. The request context carries `person`. Every audit `actor` becomes the person's e-mail; agents keep their execution ID as actor, and program workers keep `program-worker`.
*   **`me`** returns the person, roles and a `grantsVersion` that is a hash of the person's `people.json` entry.
*   **Loopback scripts.** A script that calls the coordinator with the browser Bearer directly (on PDS18 loopback, for example `scripts/lab-examples.ts import`) sends `X-Fleet-Person` itself, from `FLEET_PERSON`. The coordinator re-reads `people.json` when its mtime, size or inode changes; after a restart with a broken file it serves the last valid load from the `person` rows.
*   **Until slice 3.** A person other than the owner may call only `me`; everything else is 403 until per-person visibility (slice 3) lands. Slice 3 removes this gate.

### 2. Visibility

*   Today the scope is fixed per pool connection (`store.ts:20`, `-c fleet.project_ids=*`; pglite at `store.ts:30`). It becomes per transaction: `store.tx`, `store.read` and `store.atomic` take a `scope {projectIds, person}` argument and run `SET LOCAL fleet.project_ids` and `SET LOCAL fleet.person` first. `runOp` (`app.ts:334`) passes the request person's scope (`*` for the owner). Machine routes, `dispatch`, timers and program workers pass `{projectIds: '*', person: null}`; they filter by `hostMaySee` in code.
*   **Holder rows.** The policies for `host`, `store` and `managed-account` rows also allow rows whose `holder` is the request person (`fleet.person`). The `store` policy today allows only `'*'`; it gains this clause.
*   **Records that hide their project in JSON.** `managed-account`, `pipeline-run` and `project-account` keep the project inside `data`. Before scoping is switched on, every document kind gets `project_id` filled or is listed as global (architect only). Implementation item 6 lists them.
*   **Operations per role.** Each operation in `app.ts` `operations` and each route is tagged `operator` or `architect`. An operator call to an architect operation is 403, unless the operation targets a row the person holds.

### 3. Hosts: records, secrets and trust

*   **Host record.** A new document kind `host` replaces the fixed host list in config (`hostId`, `integrationHostId`, `publicationHostId`, `nativeHostIds`): `{id, name, holder, platform, secretHash, intake, harnesses: {<name>: {allowed: boolean}}}`. Settings the executor applies on the host (store capacity, service limits) stay in data foundation §11 `host_setting`, built in slice 2. `config` keeps only bootstrap values (the coordinator's own host ID). On first start, the coordinator creates `host` rows for today's config IDs with holder = owner.
*   **Program-worker and inspector hosts.** Today `integrationHostId` is the "inspector" that runs account inspections for `owner-mac`, and `publicationHostId` runs delivery (`accounts.ts:7-8`, `delivery.ts:49,57`). They become host field `roles` (`inspector-for:<hostId>`, `publication`), so no config host list remains.
*   **Secret per host.** `hosts.register` (owner only) creates the row and returns a new random secret once; only its SHA-256 is stored. The executor sends it as today's machine Bearer. The coordinator looks up the host by the hash and **derives `hostId` from the secret**; a body `hostId` that differs is 403. `owned()`, `/api/hosts/stores`, artifacts and receipts all use the derived ID. `hosts.rotate_secret` replaces the hash; the old secret stops at once.
*   **Cut-over for existing hosts.** One release: before deploy, the owner runs `scripts/hosts-bootstrap.ts`, which creates `host` rows for today's config IDs with holder = owner and writes each new secret to that host's `FLEET_HOST_SECRET_FILE`. The executor's `FLEET_HOST_TOKEN` and the coordinator's `FLEET_MACHINE_SECRET` (also read by `delivery_worker.py:51-53`) are removed in the same release; the delivery worker reads the publication host's secret file.
*   **Trust of a held host.** When `host.holder` is not the owner, the coordinator gives that host only commands whose project the holder has a grant on, presigned links only for those projects' artifacts, and repository keys only for those repositories. The check is one function, `hostMaySee(host, projectId)`, called at command delivery in `/api/hosts/poll`, in `hostMayRead` and in the key path. It ships in slice 2, before any held host joins, and `dispatch` (slice 6) reuses it as its first filter. This is the data foundation §9 boundary seen from the coordinator: Fleet treats such a host as fully readable by its holder.
*   **Revocation.** When a person's grants change (`grantsVersion` changes), the coordinator stops running executions of the dropped projects on hosts that person holds, removes their run tokens from the gateway snapshot, and raises one needs-you question per dropped project naming the repository keys delivered to that host, which should be rotated. Presigned links stay short-lived as today.
*   **Another person's login on a held host.** A binding of subscription S on host H where `S.holder ≠ H.holder` (for example the owner signs in on Simon's PC) shows a warning at binding time and in every route decision: "Simon can read this login". It is a warning, not a ban.
*   **Harness placement is a host field.** `host.harnesses.<name>.allowed` replaces the hard-coded 403s in `accounts.ts` and the `eligible` rule. The executor-side `FLEET_CLAUDE_PLACEMENT` check (`harness.rs:309-320`) stays: it is the host's own record of how Claude is installed there (`mac`, `windows-wsl`), and it reports a mismatch as readiness, not as a rule. Initial data: Claude allowed on the owner's Mac and Windows hosts and on Simon's PC; not set on PDS18. The settings screen shows the Anthropic terms note next to Claude placement on a host held by someone other than the subscription holder.

### 4. Subscriptions

*   **Record.** `managed-account` stays the record kind (one registry, no rename churn) and gains:
    *   `holder`: person e-mail. Initial value: owner, for every existing row.
    *   `kind`: `personal` (default) or `team_seat` (later).
    *   `plan`: free text, for example "Claude Max $100".
    *   `intake`: replaces `enabled` (see §7).
    *   `take`: the holder's take rule, see §5.
    *   `switchPct`: optional number set by the holder or the owner (§6). It applies only to harnesses whose readings have a percent (Codex today); the screen says so for Claude.
    *   `comment`: free text (decision 1).
*   **Harnesses over ACP.** Antigravity is driven over the Agent Client Protocol (`crates/executor/src/antigravity.rs`, behavior copied from agentq and t3code). The harness list is closed today (`harness.rs:38`, `accounts.ts` `['codex','claude','antigravity']`). This design adds harness kind `acp` with a host-side binding `{executable, args, environment}`, served by the same ACP session code, so another ACP agent joins through a binding and not a code change. Antigravity becomes the first `acp` binding with its current args (`acp --gemini-dir … --include-default-customizations=false`). No binary hash pinning (round 2).
*   **Logins.** Unchanged: the holder signs in on their own host, under the account user (FD5). Fleet shows the login command and never receives a code or token.

### 5. Director and take

*   **Setting the director.** `task_create`, `session_create` and the questions path set `directed_by` from the request person. Agent-created tasks and child executions inherit the creating execution's `directed_by`. Lab trials inherit the experiment creator. The field is written only by the coordinator.
*   **The rule in dispatch.** A personal subscription is a candidate only when `subscription.holder = execution.directed_by`. This is how the owner's decision "a login is used by its person" becomes code. It is not a ban on helping: the holder **takes** the work.
*   **Take.** Two ways, both by the holder (or the owner on the holder's behalf, audited and notified). Both need the taker to have a grant on the task's project; the Take action and `take.projects` validation refuse a project without one and say so.
    *   **Per task:** Take on a task or a waiting execution. It writes `directed_by = taker` and `taken_from = previous` on the task; queued executions of the task follow.
    *   **Always for listed projects:** the subscription's `take.projects`. A take-rule candidate is ranked after every candidate of the current director (§6). When it wins, dispatch writes `directed_by = holder` and `taken_from` on the task in the same transaction, before the start command, and the route decision records the take.
*   **What a take changes.** The task keeps `requested_by` (the original director, never rewritten). Person-scope budgets count against the new director; stall questions go to the new director; the requester is told of the take. Automatic takes carry a terms note: the research rates the subscriber doing work for someone else's project as safe ([terms research](reviews/people-access/terms-research.md) §4 "Looks safe", second bullet) only while the requester gets no login, token or way to drive the session.
*   The owner's subscriptions on other people's projects (decision 3) are this take rule. Simon and the owner taking each other's tasks (decision 2) are the same rule on their own subscriptions.

### 6. Dispatch

`dispatch(pollingHostId)` runs inside `/api/hosts/poll`, in the same transaction, for queued agent executions, oldest first. Queue time records only constraints: project, harness, profile, director, priority, an optional explicit subscription or host.

Queued means `execution.state = 'queued'` and `host_id IS NULL`; `execution_state` gains the value `queued`, with a partial index for it. `detectProblems` (`app.ts:148`) skips host-less queued rows.

For each queued execution, Fleet builds the candidate list over **all hosts**, not only the polling one. A candidate is a pair (subscription, host where it has a binding).

1.  **Trust.** Skip hosts whose holder may not see the project (`hostMaySee`, §3).
2.  **Director.** Keep subscriptions whose holder is the director, then subscriptions whose `take.projects` lists the project (§5).
3.  **Placement.** Skip pairs whose harness is not allowed on that host (`host.harnesses`).
4.  **Intake.** Skip a Paused host or subscription.
5.  **Readiness.** Login ready on that host; host online (today's existing rule, not a new number: a poll within the last 30 s, `accounts.ts` `connectivity`); project files reachable from that host or copyable.
6.  **Order.** Rank by: director's candidates before take-rule candidates; then the project's `subscriptions` order list (new project setting, replacing `project-account`), else the director's subscriptions in creation order. An explicit choice on the session or Lab variant ("use this one for now") is a list of one. Between pairs of one subscription on several hosts, hosts that hold the project's files come first, then host creation order. Skip a subscription whose latest reading is at or over its `switchPct`, if set; a reading whose `resetsAt` has passed counts as unknown. With no `switchPct` or no percent, skip only after the provider rejects.
7.  **Budgets.** A reached `pause` or `stop` budget that covers the execution skips it with the budget as reason; `warn` adds a warning only.
8.  **Claim.** The **best candidate** is the first unskipped pair in rank order. If its host is the polling host, the poll claims the row with `SELECT … FOR UPDATE SKIP LOCKED` and an update that sets `host_id` only where it is still null, then writes the start command; a concurrent poll skips the locked row. If it is another host, leave the execution queued for that host's poll. If no pair is left, set `execution.wait_reason` (new column, the counterpart of `task.wait_reason`) to the skip reason of the first-ranked pair.
9.  **Record** a `route_decision` row `{executionId, at, hostId (the polling host), candidates: [{subscription, host, skipped?: reason, warnings: []}], winner?}` when the execution is claimed or its wait reason changes, not on every poll.

**Terms warnings** are attached, never enforced: two personal subscriptions of one provider and one holder in the same order list (Anthropic and OpenAI rate-limit terms, [terms research](reviews/people-access/terms-research.md) §1); Antigravity driven over ACP (§2); Claude on a host held by someone other than the subscription holder.

The 409 paths in `managed.resolve` become skip reasons. An explicit host or subscription that is not usable makes the execution wait with that reason; it no longer fails the request.

### 7. Intake and stall alerts

*   **Intake** is a field on `host` and on `managed-account`. Values: `all`, `paused` now; `urgent`, `projects:<ids>` later with the scheduler. Every change of intake, placement, take or `switchPct` writes one audit row of kind `setting-change` `{target, field, previous, value, actor, at}` (it replaces `account-action`) and acts at the next poll.
*   **Stall alert.** When dispatch leaves a queued execution with no candidate, the coordinator upserts one open `question` of kind `operator_decision` with a unique `cause` key (`host:<id>:paused`, `subscription:<id>:limit`, `files:<store>:offline`, `budget:<id>:reached`). No separate problem table. It is addressed to the director of the waiting work and names the holder of the cause as the person to ask. Five waiting tasks with one cause give one question. When a dispatch finds the cause gone, the question closes.

### 8. Readings

*   The executor forwards two harness events that it drops today:
    *   Codex `rate_limits`: used percent, limit period, `resets_at`.
    *   Claude `rate_limit_event`: status and reset time (no percent).
*   They arrive in the outbox as `limit_reading` events and land in `execution_totals.rate_limit_last` (already in `schema.sql`) and in a per-subscription latest reading `{at, pct?, status?, period, resetsAt}` on the `managed-account` binding.
*   A reading is only as fresh as the subscription's last run. Screens show its age. No reading is "unknown", never zero.

### 9. Budgets

One shape, extending Lab's `budgets` (`lab/experiments.ts`):

```text
budget { id, scope: person | project | experiment | connection, scopeId,
         unit, limit, period?: day | week | month, action: warn | pause | stop,
         setBy, setAt }
```

*   **Units:** `usd` and provider `credits` (API key connections), `tokens`, `trials`, `wall_ms`. A subscription's provider limit is a reading, not a budget: Fleet shows it and never sets it.
*   **Spend** is the sum of `connection_cost` measurements ([fleet-data-foundation.md](fleet-data-foundation.md) Non-Goals, `connection_cost`) and token totals over the scope and period. Every number shows its basis (`measured`, `estimated`, `unavailable`) and age. A unit Fleet cannot measure shows "unverifiable".
*   **Default action** when unset: `warn` for person, project and connection scopes; Lab experiments keep `pause` (R3.7, decision 9). `stop` only when the owner sets it.
*   **Storage:** a `budget` table; Lab's experiment `budgets` JSON moves into it with `scope = experiment`. Lab units `pipe_calls` and `pipe_credits` become `calls` and `credits` with the connection named in `scopeId` (see [fleet-connections.md](fleet-connections.md)).
*   **Enforcement:** the coordinator computes which budgets are reached. `dispatch` applies them to agent runs; the gateway applies them to API calls from a "blocked by budget" flag in its snapshot ([fleet-connections.md](fleet-connections.md) §6). While the coordinator is down, the gateway keeps the last flags.
*   **Budget request:** "Ask for more" creates a question of kind `operator_decision` with the budget ID. The owner's answer writes the new limit, audited with both people.

### 10. Files on a person's machine

*   A project's `artifact_locations` lists stores by host. Changing it shows a preview (what moves, what is deleted where) and needs the project holder's or owner's confirmation, like retention rules (`placement.confirmRule`).
*   **PDS18 copy** (decision 12): a new project gets `pds18` in `artifact_locations` next to the holder's store, so nightly backups continue. The holder can drop it after the preview.
*   An execution that needs files on an offline store waits with "files on Alina's Mac (offline)" instead of expiring after `artifactRetryWindowMs`.

### 11. Repositories and delivery

Per repository in project settings: `delivery.access: deploy-key | bot-collaborator | holder-rights` and `integration.mode: pr | direct` (exists). The project screen recommends: deploy key plus `direct` for a repository in a person's organization with no bot; bot collaborator plus `pr` when reviews are wanted. The owner's personal GitHub and private GitLab keep today's path. Holder rights mean the work runs on the holder's host with their own git login, which needs no Fleet-held credential.

### 12. Screens

Built from `packages/ui` only, per `docs/design-system/owner-rules.md`:

*   **Subscriptions** list (`/subscriptions`): holder, harness, plan, intake, latest reading with age and reset countdown, hosts, take rule, comment; terms warnings inline.
*   **Hosts** (existing page): holder, intake, harness placement settings, secret rotation.
*   **Task and execution:** directed by, taken from, connection used, route decision.
*   **Project settings:** subscription order list, file locations with preview, repository delivery.
*   **Budgets** on project, person, experiment and connection pages, with spend, basis, age and "Ask for more".

## Alternatives Considered

### A1: Hard filters from the terms (the brief's T1-T4)

*   **Pros:** no way to break the terms by a setting.
*   **Cons:** invented bans the owner rejected (Claude placement, Antigravity fallback, binary pinning); a corporate plan on PDS18 would need a code change.
*   **Reason for rejection:** brief, "Owner Decisions, Round 2", Principle: settings with warnings. Only "a login stays on its host" stays architecture.

### A2: Owner-only consent instead of take

*   **Pros:** one switch per subscription ("may serve project X").
*   **Cons:** it makes the owner's plan run Alina-directed work, which the research rates as risky (account made available to someone else). Take keeps the subscription's holder as the director, which the research rates safe.
*   **Reason for rejection:** take keeps the subscriber as the person doing the work, the case the research lists under "Looks safe" (§4), and records the requester in `requested_by`. Automatic takes still show the terms note (§5).

### A3: Choose at queue time (today)

*   **Pros:** no change.
*   **Cons:** intake, limits and offline machines cannot affect queued work.
*   **Reason for rejection:** dispatch on poll costs a few queries per poll with fewer than 10 people.

### A4: Rule engine for routing

*   **Pros:** expressive.
*   **Cons:** rules can conflict; the owner asked for no rule language.
*   **Reason for rejection:** an ordered list plus an optional switch point copies the fallback lists of LiteLLM and CLIProxyAPI without their cross-account balancing, and cannot conflict.

### A5: Fine-grained RBAC

*   **Reason for rejection:** two roles plus grants plus holder are already decided in data foundation §10 and cover every journey above.

### A7: Cloudflare service token or mTLS per host instead of a host secret

*   **Pros:** identity at the edge; Fleet stores no secret hash.
*   **Cons:** one Access service token per host to create and rotate by hand in Cloudflare; the coordinator still maps the token to a host; mTLS needs a CA. The shared `fleet-machines` token keeps its job of reaching the edge.
*   **Reason for rejection:** a per-host secret is one row and one file, and rotation is one operation in Fleet.

### A6: Native Windows executor for Simon

*   **Reason for rejection:** the Linux executor in WSL2 exists (`FLEET_CLAUDE_PLACEMENT=windows-wsl`, fleet-lab R3.8). Native Windows stays a data foundation non-goal.

## Risks & Mitigations

*   **Two polls claim one execution.** `FOR UPDATE SKIP LOCKED` and a null-guarded `host_id` update (§6).
*   **One host wins the whole queue.** Ranking can send every queued execution to one laptop. Fleet adds no capacity number (owner rule: no invented caps); the holder or owner uses intake, the order list or another subscription, and the route decision shows why. Revisit with a scheduler.
*   **Coordinator down.** Nothing starts, because dispatch runs in the coordinator. Running work continues, and API calls continue through the gateway ([fleet-connections.md](fleet-connections.md)).
*   **Row-level security leaks through the holder clause.** Slice 3 tests assert per-kind visibility for a non-owner person.
*   **A holder loosens their own host.** A holder may change intake and harness placement on their host; every change is a `setting-change` row and the owner is told.
*   **Malformed `people.json`.** The coordinator keeps the last valid load and raises one needs-you question with the parse error. With no valid load at all, every browser request is 403.
*   **Stale readings.** A reading past `resetsAt` counts as unknown (§6).
*   **A run token read by another run of the same OS user.** See [fleet-connections.md](fleet-connections.md) Risks.

## Non-Functional Requirements

*   **Security.** Per-host secrets stored as hashes; `hostId` derived, never trusted. `X-Fleet-Person` is set only by the guard and stripped from client requests. A held host is treated as readable by its holder. Logins never leave their host.
*   **Performance.** `dispatch` runs per poll: candidates are at most hosts × subscriptions (under 50 rows). No index beyond `execution(state)`.
*   **Rollout.** Early prototype, no shims: each slice switches fully. People slices 1-4 land before any non-owner host joins.

### Additional Resources

*   [people-access-brief.md](people-access-brief.md), [terms research](reviews/people-access/terms-research.md)
*   [fleet-data-foundation.md](fleet-data-foundation.md) §3, §9, §10, §11
*   [fleet-lab.md](fleet-lab.md) Budgets, Claude on WSL (R3.8)
*   [docs/connections.md](../connections.md), [docs/work-allocation.md](../work-allocation.md)

### Future Improvements

*   Urgent only and Only `<projects>` intake with a priority-aware scheduler.
*   Web push for stall alerts (operator app service worker).
*   Charts: shared limit, cache read share, time series.
*   Team or Business seats as `kind: team_seat` with an organization holder.

--------------------------------------------------------------------------------

## Detailed Implementation Plan

Slices are ordered. Each ends with its check. Slices 1-4 land before any non-owner host joins; the held-host trust filter ships in slice 2.

| Slice | Content | Check | Needs |
| --- | --- | --- | --- |
| 1 Identity | guard header, owner flag, `person`, Access group, actors | a client-sent `X-Fleet-Person` is replaced by the verified e-mail; a host-secret request with the header is 403; unknown e-mail 403; no header 401 | — |
| 2 Host secrets and trust | `host` rows with roles, intake and harness placement fields, bootstrap cut-over, per-host secret, derived `hostId`, `hostMaySee`, `host_setting` | Alina's secret with `hostId=pds18-native` gets 403 on poll and store registration; her host gets no other project's command | 1 |
| 3 Visibility | `fleet.project_ids` per request, holder clause, `project_id` on every kind, role tags | Alina sees only `desiign` and her rows; architect operations 403 | 1 |
| 4 Holder and director | subscription fields, `directed_by`, `requested_by`, take, removal of Claude 403s, `setting-change`, `acp` harness kind | Alina-directed task never selects Simon's subscription; Antigravity runs through an `acp` binding with unchanged args | 2, 3 |
| 5 Alina's Mac | onboarding runbook, store, deploy key, file locations with preview | one task runs on her Codex; files reach the chosen stores | 4 |
| 6 Dispatch | late choice, ranking, claim, route decision, stall questions, intake, budget shape | pausing the only subscription gives one question; resuming closes it | 4 |
| 7 Readings | `limit_reading`, limits view | saved Codex and Claude samples stored with age; reset readings count as unknown | 6 |
| 8 Simon's PC | WSL2 executor, Simon's host row with Claude allowed | one Claude run completes on Simon's PC | 6 |

Connections stages C0-C4 run alongside ([fleet-connections.md](fleet-connections.md)); C1 needs slice 4 and mints tokens in `enqueue` until slice 6 moves start commands into `dispatch`.

### Target Files to Create/Modify

Paths are from `origin/main` at `f69b5dc`. Migrations continue after `-- migration 12` in `schema.sql`.

#### 1. `deploy/infrastructure/origin_guard.py`, `test_origin_guard.py`, `fleet.caddy.candidate` (Modify) — slice 1

*   Guard: verify the JWT without the owner match; return `X-Fleet-Person: <claims.email>` with the Bearer. Remove `FLEET_ACCESS_OWNER`.
*   Caddy: site-level `request_header -X-Fleet-Person` next to `-X-Dashq-Subject`; `forward_auth` `copy_headers Authorization X-Fleet-Person`. The `@machine` matcher becomes `path /api/hosts/*` (today it lists `/api/hosts/artifacts` exactly and misses `stores`, `agent-tools`, `accounts`, `artifacts/*`).
*   Guard test: the returned `X-Fleet-Person` equals the JWT e-mail; an e-mail other than the owner's is now accepted.

#### 2. `packages/coordinator/people.ts` (Create); `app.ts`, `server.ts`, `tasks.ts`, `projects.ts`, `pipelines.ts`, `lab/experiments.ts`, `accounts.ts`, `sessions.ts` (Modify) — slice 1

*   `people.ts`: load `FLEET_PEOPLE_FILE` (`[{email, name, roles, owner?, grants: [{kind, id}]}]`) into `person` and `person_grant`, watch it, keep the last valid load on a parse error (Risks). Exports `personFor(req)`, `scopeFor(person)`, `isOwner(person)`, `can(person, role, row?)`.
*   `app.ts`: the `onRequest` hook (`app.ts:66`) splits browser and machine auth (§1); `me` (`app.ts:330`) returns the person, roles, owner flag and `grantsVersion`; `runOp` (`app.ts:334`) passes the scope.
*   Actor rewrites: every `'operator'` that names who acted becomes the person's e-mail, passed in as an argument: `app.ts:285,300,303,309,313`, `tasks.ts:110,180,202-204`, `projects.ts:45,55,56,58,59`, `pipelines.ts:92`, `lab/experiments.ts:199`, `accounts.ts` (`account_preference`, `account_enabled`). Literal `'operator'` used as a role (for example `askedBy` of agent questions) stays.
*   `server.ts:9`: drop `ownerEmail`, `machineSecret`, `nativeHostIds`, `integrationHostId`, `publicationHostId`; add `peopleFile`.

#### 3. `packages/coordinator/db/schema.sql` (Modify) — slices 1-6

*   **Migration 13 (slice 1):** `person(email text pk, name text, roles text[], owner boolean, loaded_at timestamptz)`, `person_grant(email text, object_kind text, object_id text, pk(email, object_kind, object_id))`. RLS: readable when `fleet.project_ids = '*'` or `email = current_setting('fleet.person')`.
*   **Migration 14 (slice 2):** `host_setting(host_id, key, value, updated_by, updated_at, applied_at, apply_error)` as data foundation §11; RLS like `store`.
*   **Migration 15 (slice 3):** `document` policy (`schema.sql:176`) gains `OR (kind IN ('host','managed-account','api-connection') AND data->>'holder' = current_setting('fleet.person', true))`; `store` gains column `holder text` and its policy (`schema.sql:336`) gains `OR holder = current_setting('fleet.person', true)`.
*   **Migration 16 (slice 4):** `execution.directed_by text`, `task.directed_by text`, `task.requested_by text`, `task.taken_from text`; backfill all three with the owner's e-mail, then `SET NOT NULL` on `execution.directed_by` and `task.directed_by`.
*   **Migration 17 (slice 6):** `execution_state` gains `queued`; `execution.wait_reason text`; index `execution(state) WHERE host_id IS NULL`; `question.cause text` with a unique index on `cause` where the question is open; `route_decision(execution_id, at, host_id, candidates jsonb, winner jsonb)`; `budget(id, scope, scope_id, unit, limit numeric, period, action, set_by, set_at)`. RLS: `route_decision` by the execution's project; `budget` by `scope_id` project or person, `*` for the rest.
*   `share` (§1 "live share") is not added here; it stays in the data foundation plan.

#### 4. `packages/coordinator/hosts.ts` (Create); `app.ts`, `stores.ts`, `accounts.ts`, `delivery.ts`, `recovery.ts` (Modify); `scripts/hosts-bootstrap.ts` (Create) — slice 2

*   `host` kind `{id, name, holder, platform, roles, secretHash, intake, harnesses}`; `hosts.register`, `hosts.rotate_secret` (owner only); `hostFromSecret(bearer)`; `hostMaySee(host, projectId)`.
*   `app.ts`: `owned()` (`:73`) uses the derived host; `hostMayRead` (`:195`) and command delivery in `/api/hosts/poll` (`:169`) call `hostMaySee`; the startup check `:25` compares only `browserSecret`.
*   `accounts.ts`: `hosts()`, `inspect`, `report` (`/api/hosts/accounts`, which never called `owned()`) and `catalogue` use `host` rows and the derived host instead of the config list (`accounts.ts:7,8,19,21`); the inspector is the host with role `inspector-for:<id>`.
*   `delivery.ts:49,57`: the publication host is the host with role `publication`.
*   `recovery.ts`: receives the new `owned` unchanged in signature.
*   `scripts/hosts-bootstrap.ts`: the cut-over in §3.

#### 5. `crates/executor/src/main.rs`, `worktree.rs`, `harness.rs` (test at `:549`), `packages/coordinator/delivery_worker.py`, `docs/configuration.md` (Modify) — slice 2

*   `FLEET_HOST_TOKEN` (`main.rs:1044`) is replaced by `FLEET_HOST_SECRET_FILE` (0600, read at start); `FLEET_HOST_ID` stays as a label but the coordinator ignores it. `worktree.rs:23` and the harness test drop the old name.
*   `delivery_worker.py:51-53` reads the publication host's secret file instead of `FLEET_MACHINE_SECRET`.

#### 6. `packages/coordinator/store.ts`, `accounts.ts`, `pipelines.ts` (Modify) — slice 3

*   `store.tx/read/atomic(scope, fn)` with `SET LOCAL` per transaction (§2); remove the pool-level `-c fleet.project_ids` (`store.ts:20,30`).
*   Fill `project_id` on `pipeline-run` and `project-account` rows; the global kinds listed in §2 stay `project_id` null and are visible to architects and to their holder.
*   Each operation in `app.ts` and each route gets a role tag; `can()` enforces it.

#### 7. `accounts.ts`, `tasks.ts`, `sessions.ts`, `agent-tools.ts`, `delegation.ts`, `executions.ts`, `lab/experiments.ts` (Modify) — slice 4

*   `managed-account` fields `holder`, `kind`, `plan`, `intake`, `take: {projects}`, `switchPct`, `comment`; `enabled` migrated to `intake` and removed.
*   Remove the Claude 403s in `accounts.ts` (`hosts().eligible`, `inspect`, `account_login_handoff`, `report`, `resolve`); read `host.harnesses`.
*   `account-action` becomes `setting-change`.
*   `directed_by`, `requested_by` set in `task_create`, `session_create`, `agent-tools.ts` task creation and `delegation.ts`; inherited by child executions in `executions.ts`; `task_take` operation.
*   `account_preference` and `project-account` are replaced by project setting `subscriptions` (ordered IDs); `managed.preferred` returns the first entry for delivery (`delivery.ts:10-29`) until slice 6.

#### 8. `packages/coordinator/dispatch.ts` (Create); `app.ts`, `agent-tools.ts`, `delivery.ts` (Modify) — slice 6

*   `dispatch(pollingHost)` as §6, called from `/api/hosts/poll` inside its transaction.
*   `enqueue` (`app.ts:129-143`) writes a queued execution with constraints, no host and no start command. `pickAccount` (`app.ts:145`, used at `agent-tools.ts:32,190`) and the host loop are deleted; agent tools name a subscription only as an explicit choice.
*   `delivery.ts`: `managed.resolve`/`preferred` calls (`app.ts:290`) become an explicit-subscription constraint on the queued execution.
*   Stall question upsert and close (§7).

#### 9. `crates/executor/src/harness.rs`, `codex.rs`, `outbox.rs`, `antigravity.rs`, `profile.rs` (Modify) — slices 4 and 7

*   Slice 4: harness kind `acp` with binding `{executable, args, environment}`; `harness.rs:38` accepts it; Antigravity is the first `acp` binding with its current args (`harness.rs:292-297`). Keep `claude_placement` (`harness.rs:309-321`) as a readiness report.
*   Slice 7: forward Codex `rate_limits` and Claude `rate_limit_event` as `limit_reading` outbox events.

#### 10. `packages/coordinator/executions.ts` (Modify) — slice 7

*   Ingest `limit_reading` into `rate_limit_last` and the binding's latest reading.

#### 11. `packages/coordinator/budgets.ts` (Create), `lab/experiments.ts` (Modify) — slice 6 (shape), C3 (money)

*   Budget create/update (owner), spend query, reached-budget set used by dispatch and the gateway snapshot, budget request question and answer handler. Experiment `budgets` JSON moves to `budget` rows.

#### 12. `packages/coordinator/placement.ts`, `projects.ts` (Modify) — slice 5

*   Location change preview and confirm; default `pds18` copy; wait instead of expire for offline stores; `delivery.access` per repository in `ENUMS` (`projects.ts:14`).

#### 13. UI — slices 3-7

*   `app/src/main.tsx`, `app/src/api.ts`, `app/src/lists.ts` (Modify): routes `/subscriptions`, host holder and intake on `/hosts`, director and route decision on task and execution views, project order list, locations and budgets. Components from `packages/ui` only (`data-list`, `setting-field`, `state-badge`, `fill-bar`, `decision-block`, `problem-row`); new screens registered in `packages/ui/src/registry.ts`. `pnpm run check:design` clean.

#### 14. Docs (Modify/Create)

*   `docs/designs/fleet-data-foundation.md`: Terms (hosts may be held by others); §9 per-host secrets and held-host trust; §10 owner flag, holder, `directed_by`, `requested_by`, take, identity header; §11 `host` record with intake and harness placement fields; Non-Goals: replace "Concurrency or admission caps of any kind" with "No built-in caps; every limit is an owner setting"; the routing non-goal points here.
*   `docs/designs/fleet-operator-app.md`: `me`, holder views, the needs-you stall question; web push stays later.
*   `docs/designs/fleet-lab.md`: budgets point to §9; account choice is experiment default with variant override, recorded per trial as a confounder.
*   `docs/designs/README.md`: list Lab docs and both new designs.
*   `docs/onboarding-host.md` (Create): executor, store, tunnel, deploy key, host secret, for Mac and WSL2.
*   `docs/configuration.md`: new and removed variables.

#### 15. Tests (Create/Modify)

*   Create `tests/coordinator/people.test.ts` (CUJ 1, header and auth mixing), `host-secrets.test.ts` (CUJ 2, derived host, rotation, `hostMaySee`, revocation), `dispatch.test.ts` (CUJ 3-6, claim race, take, intake, switch point, warnings, one question per cause), `budgets.test.ts` (CUJ 7).
*   Modify `tests/coordinator/project-fixture.ts` and the tests that pass `machineSecret`, `nativeHostIds`, `integrationHostId` or `publicationHostId`: register hosts through `hosts.register` in the fixture.
*   Executor unit tests for `limit_reading` from saved Codex and Claude samples, and for the `acp` binding.

### Verification Plan

*   **Build:** `pnpm run build`, `cargo build -p executor`.
*   **Tests:** `pnpm test`, `cargo test -p executor`, `python3 -m pytest deploy/infrastructure tests/executor`.
*   **Design check:** `pnpm run check:design`.
*   **Live:** the slice checks in the table above, ending with: Alina's Mac runs one task on her Codex; Simon's PC completes one Claude run under WSL2; pausing the only subscription for a ready task creates exactly one question, and resuming closes it.

--------------------------------------------------------------------------------

*Drafted with EGM*
