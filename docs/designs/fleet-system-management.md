# Fleet system management: hosts, cleanup, backups, problems and the audit journal

*   **Author:** Alex Kirs
*   **Status:** Proposed
*   **Date:** 2026-11-11
*   **Linked design:** [telemetry](fleet-telemetry.md). This document owns the audit journal, problems and their routing, and the System screen layout and actions. Telemetry owns spans, the rules pass and the screen's read panels.

--------------------------------------------------------------------------------

## Overview

The owner runs Fleet from the app without a shell: add, pause and remove hosts; free disk; archive and delete test data; see backups and releases; see problems and the bot working on each. Every action is an operation over the shared core ([fleet.md R2](../../fleet.md#principles)), so the PM agent can do what its director can. Every system change is written to one audit journal. Nothing is blocked by built-in rules: destructive actions show a preview, limits are settings with permissive defaults.

### Terms

*   **Archive:** hide an object from default lists; keep all data; reversible with `*_unarchive`.
*   **Delete:** mark an object `deleted` (tombstone): it leaves every view and operation, its references stay valid, its secrets are dropped at once. Owner only.
*   **Purge:** remove tombstoned rows that no other row references, and their stored files. On demand, with a preview.
*   **Reference map:** for each object kind, the tables and columns that point at it. Purge and the delete preview read it.
*   **Problem:** an anomaly with a `cause` key, opened by Fleet from a rule or a direct signal, owned by a bot, closed by Fleet when the cause clears. Not a question.
*   **Audit journal:** the durable record of system changes: who, what, previous and new value, trace link.
*   **Delivered:** a session's result reached its destination: its task was accepted, or its pull request was merged.

## The Problem

*   Hosts cannot be removed. `pds18-program` and `owner-mac-program` sit paused forever.
*   A worktree goes only at `session_archive`. Delivered work keeps its checkout on disk until someone archives the session.
*   Tasks, executions, projects, Lab experiments and connections have no archive or delete. Acceptance runs leave data nobody can clean from the app.
*   Problems (host silent, disk full, backup missing, stuck run) are designed in parts and not built. Built today: dispatch stall questions and the update question (`update:<host>:<version>`).
*   Backups are the pre-deploy dumps of self-update only: no schedule, no rotation shown, no restore ever tested.
*   Changes are recorded in `setting-change` documents for some settings and `transport-audit` for transport; there is no single journal of who did what.
*   There is no System screen.

## Goals

1.  `hosts_remove` with a preview; a removed executor gets 410 and stops.
2.  Archive for every object kind; owner hard delete with preview; `system_purge` with preview.
3.  A worktree is removed as soon as its session is delivered and no `keepWhen` condition holds.
4.  Problems from the [stage 1 telemetry rules](fleet-telemetry.md#6-rules) and direct signals as self-closing rows with a bot owner; Needs you only for what a person alone can give.
5.  Scheduled Postgres dumps with rotation, a restore drill, both shown on screen.
6.  One audit journal for every system operation, by person, agent or Fleet.
7.  One System screen where the owner answers "is the system healthy" and acts.

## Non-Goals

*   Revocation of running work on a grant change ([people access §4](fleet-people-access.md), separate work).
*   Web push, Telegram, email alerts. Web push is later.
*   Time-based worktree expiry.
*   Built-in bans, quotas or confirmation walls. Delete confirmation is an opt-in setting.
*   ClickHouse and Garage backup in this design: spans are telemetry (loss accepted), artifacts already replicate under data foundation §3. Postgres is the state that cannot be rebuilt.

## Critical User Journeys (CUJs)

### CUJ 1: Remove a stuck host

*   **Given:** `pds18-program` is paused with no running work.
*   **When:** the owner opens System, Hosts, Remove; the preview shows 0 executions and 0 sessions; the owner confirms.
*   **Then:** the host is `removed`, its secret and settings are gone, it leaves the host list; its history still names it. A journal entry records the change. An executor still running with that id gets 410 and exits.

### CUJ 2: Clean acceptance data

*   **Given:** project `acc-test` with 40 tasks and 300 executions.
*   **When:** the owner deletes the project; the preview lists counts per kind and stored bytes.
*   **Then:** the project and its tasks and executions are tombstoned. `system_purge {preview: true}` lists them as unreferenced; `system_purge {preview: false}` removes rows and files. Journal entries record both.

### CUJ 3: Delivered worktree goes

*   **Given:** a session's PR is merged and the worktree is clean and pushed.
*   **When:** the coordinator's PR poll sees the merge.
*   **Then:** it sends `teardown`; the executor checks `keepWhen`, removes the worktree and the local branch. The branch stays on the remote; transcript and artifacts stay in the store.

### CUJ 4: Backup missing

*   **Given:** the daily dump failed for two days (disk full).
*   **When:** the problems pass sees no successful backup for 48 hours.
*   **Then:** the owner gets one question `problem:backup:missing` with the last error. It closes after the next good dump.

### CUJ 5: The PM cleans up for its director

*   **Given:** the owner's PM with `fleetOperations`.
*   **When:** the owner asks it to archive finished Lab experiments older than a month.
*   **Then:** the PM calls `experiment_archive` per experiment; each call writes a journal entry with `actor.kind = agent` and `actor.directedBy = owner`.

## Technical Plan (Architecture)

### Audit journal

**Copy what exists.** The coordinator already writes `setting-change` documents (`target`, `field`, `previous`, `value`, `actor`, `at`; budgets have no `previous` and add `askedBy`; some add `holder` or `projectId`) for hosts, accounts, budgets, people, connections and task director changes, and reads them with `setting_changes`. The journal generalises this: one table, one writer, every system operation.

*   **Table** `journal` in Postgres: `seq bigserial`, `id uuid`, `at timestamptz`, `actor jsonb` (the `Actor` of [definition editors](fleet-definition-editors.md#contract-defs): kind `person|agent|system`, person, `executionId`, `directedBy`), `operation text` (operation name, `hosts_remove`), `target text` (`host:<id>`, `project:<id>`), `project_id text NULL`, `field text NULL`, `previous jsonb NULL`, `value jsonb NULL`, `trace_id text NULL`, `note text NULL`. Index on `(target, seq)` and `(project_id, seq)`.
*   **Writer:** `journal(q, entry)` inside the operation's transaction. If the insert fails, the operation fails (never lost, unlike spans). One entry per changed field for settings; one entry per object for archive, delete, remove, purge (purge: one entry per kind with the count, plus the id list in `value`).
*   **What writes:** every operation whose handler changes state outside task work: hosts, stores, connections, budgets, people, settings, archive, delete, purge, backup, restore drill, worktree teardown by Fleet. Task and execution state changes stay in their own history (data foundation §2), not the journal.
*   **Migration:** existing `setting-change` documents are copied into `journal` once at start (operation `setting-change`; missing `previous` stays NULL; extra keys go into `note` as JSON), the writers switch to `journal`, `setting_changes` becomes `journal_list`. `transport-audit` stays: it is transport evidence, not a change by an actor.
*   **Read:** `journal_list {target?, projectId?, actor?, before?, limit}`, newest first. Owner sees all; others see entries of their granted projects; the `people` target is owner-only (as today).
*   **Spans vs journal:** see [telemetry §7](fleet-telemetry.md#7-spans-and-the-audit-journal). The entry stores `trace_id`.

### Problems: anomalies with a bot owner

An **anomaly** is any signal of a fault or a deviation: a telemetry rule finding, a dispatch stall, a Bugsink issue, a delivery regression, an agent gap class, a refused key, a failed update. Every anomaly is a **problem**: one row per cause, repeats counted as `×N`, closed by itself when the cause goes. A problem is not a question. A person sees every problem passively; a bot owns it and reacts; Needs you receives only what a person alone can give (owner, 2026-11-26: "I will never press these buttons").

**Routing rule.**

1. Every problem is visible passively: the Home pulse line counts them, System › Problems lists them. No automation hides one.
2. Every problem has one owner: a bot, chosen by duty (below). With no matching duty the owner is the system duty bot (HAL0). A person is never the default owner.
3. Needs you receives a problem only as a question an agent writes with `problem_escalate`, and only of one of three kinds: `only-you` (a step needs something only a person holds: a key, an account, money, a vendor or legal act), `product` (several acceptable outcomes and no approved requirement picks one), `authority` (the fix is irreversible, deletes data or overrides the owner's own setting).
4. The test for an engineer adding a signal: "Could an agent with every Fleet tool finish this without asking anyone?" Yes: it is only a problem. No: the escalation names what only the person can give and what waits until they do. "Should I look?" never qualifies.
5. A signal source never creates a question. Sources call `problemRaise`, `problemsSync`, `problemsSet` and `problemClose` only; `problem_escalate` (an agent tool) is the one path from a problem to a question. The only automatic question is the watchdog's (rule 6).
6. **Watchdog.** When a duty bot stops working (its last 3 wakes failed, or a problem it owns stays `new` for 30 minutes with no run of the bot since it opened), the coordinator raises one problem `duty:<bot>:down` with no owner and asks the owner one `only-you` question naming the bot, the reason, the count of problems waiting and that a PM can fix or take the duty. It closes when the bot runs again. One item, never one per waiting problem.

**Table** `problem`: `id`, `seq` (the code `P-<seq>`), `cause`, `source`, `rule`, `project_id NULL`, `text`, `data jsonb` (cause, source, rule, exemplars, hostId, storeId, executionId as today), `state` (`open`, `closed`), `phase` (`new`, `working`, `review`, `watching`, `stuck`, `escalated`), `owner_bot_id NULL`, `note` (the owner's latest one-line note), `work` (`T-…` task code or pull request URL), `question_id NULL` (the open escalation), `count`, `misses`, `opened_at`, `seen_at`, `phase_at`, `closed_at`, `close_reason` (`cleared`, `fixed`, `duplicate`), `reopened`. One open row per cause (unique index on `cause` where `state = 'open'`). A cause that returns within 24 h of closing reopens its row (`reopened + 1`) and keeps its owner, work and conversation; later it opens a new row.

*   **Writers** keep their names and callers: `problemsSync(source, causes)` for sources that compute a full set each run (`rules`, `delivery`, `grants`): open new causes, refresh present ones, close one absent for `problems.cleanRuns` runs (default 2, avoids flapping). `problemsSet(source, pattern, causes)` for a pass that sees all of one pattern at once (dispatch, pins): absent causes close at once. One-off senders use `problemRaise(cause, ...)` and `problemClose(cause)` and never close each other's causes. A raise of an open cause counts `+1` and refreshes `seen_at`, except a source with its own tally (agent gaps), whose count and last occurrence the problem takes as they are; a Bugsink regression or unmute reopens its row whatever happened before.
*   **Owner bot actions** (`problem_update {id, phase?, note?, work?, close?}`): set the phase, write the note, link the task or pull request, close as `fixed` with evidence or as `duplicate` of another problem (audited in the journal). The owner may do the same. `problem_escalate {id, kind, text, options?, recommendation}` asks one `operator_decision` question from the owner bot, linked to the problem (`question_id`), phase `escalated`; one open escalation per problem; its answer goes to the bot's problem conversation. Reads: `problems_list {state?, phase?, owner?, projectId?, before?, limit}` (newest first; open by default) and `problem_get {id | cause}` with the occurrences and the linked work.
*   **Progress signal, not a limit.** A problem whose owner bot ran on it 3 times without a change of phase, note or work, or that stays `working` for 48 h, turns `stuck` and shows loud. No attempt cap, budget or cooldown stops the bot.
*   **Conversation.** Each problem has one conversation of its owner bot (subject `problem:<id>`), created on the bot's first wake for it or on the person's first message; System › Problems shows it beside the row. The person acts by writing there ("look at the github grants", "not this class"). There is no Investigate or Dismiss button.
*   **Grouping (Alertmanager-style inhibition)** stays: before sync, findings whose subject depends on a firing absence cause fold into it (`problem:host:<id>:silent` absorbs every finding whose rule row carries that `host_id`; `problem:store:<id>:offline` absorbs store errors; `problem:telemetry:clickhouse` absorbs every rule). Folded findings stay in `telemetry_findings` with `groupCause`.
*   **Migration.** Every open or answered question that holds a cause becomes an open problem with the same cause, text, data, project and age; open ones are withdrawn. The cause column of `question` and its indexes are dropped once no writer uses them. Agent gap findings keep their table and link `problem_id` instead of `question_id`; their Investigate step goes (the owner bot investigates).

**Duty.** A bot's settings carry `duty.problems`: `system` (true: owns every problem without a project and those of projects no other bot claims) and `projects` (ids it owns). HAL0 holds `system: true` and `projects: ["fleet"]` ([HAL0](../../fleet.md#hal0-operations-duty)). A bot trigger with `ifChanged: ["problem"]` wakes on a new problem it owns or a change of one (a raise of an open cause wakes nobody; the bot's own updates wake nobody); the `needs-you` preset includes `problem`.

*   **One batch, one conversation.** A pass's problem changes for one bot go as one message to the bot's problems conversation (subject `problems`): every problem in it with its code, phase and why it is in the wake (new, back, changed by someone else, not started). The bot works the batch there and may open a problem's own conversation (subject `problem:<id>`) for a long investigation; `problem_talk` (a person's words) always goes to the problem's own conversation. A burst of 48 problems is one turn, not 48 runs (2026-11-26: HAL0 started 104 runs an hour).
*   **Idempotent wakes.** A wake starts nothing unless the batch has something the bot has not seen: a problem's line enters a wake only when it opened, reopened or changed since the bot's last wake about it (`problem.woken_at`), or when the bot never saw it. A wake that would repeat what the bot has (same problems, same phases) records a quiet pass and sends nothing; a batch the bot's conversation is still working on waits for that turn to end and joins the next one.
*   **Quiet wakes.** Each wake of a problem counts one quiet wake; `problem_update` resets the count; three turn it stuck.

**Run-rate breaker.** No bot has a concurrency cap. When a bot starts more than `breaker.factor` (10) times its usual runs in `breaker.window` (1 h) and at least `breaker.floor` (20) runs, the usual being the median of its runs per hour over the last 7 days, the coordinator pauses the bot (`bot_pause`, reason `burst`) and raises one problem `burst:<bot>` without an owner: its words name the bot, the rate against the usual and the subjects with the most runs. It is never a Needs you item per run, and it asks no question. Running turns finish. The pause lifts by the owner's word (a person's `bot_resume`, or telling a PM) or by itself after `breaker.coolMinutes` (30): the bot resumes on probation, and if it trips again within the window it pauses again with twice the cool time. The problem closes when the bot has run a full window under the threshold.

**A bot's failed sessions.** A session of a bot whose latest run failed (no question to a person in it) is a problem of that bot, cause `run-failed:<bot>:<plain cause>`, repeats `×N`: Home's Failed group lists only people's own sessions, and an aggregated failure (`…check the host ×N`) shows only as the pulse line's blocker chip.

| Cause | Source | Fires | Built |
|---|---|---|---|
| `host:<id>:{offline,trust,paused,holder}`, `subscription:<id>:{paused,not-taken,not-ready,refused}`, `placement:<host>:<harness>`, `budget:<id>:reached` | dispatch | queued work has no candidate | built |
| `update:<host>:<version>` | direct: refused or failed update in a poll | at once | built |
| `key:<fingerprint>:disabled` | direct: a provider refused a pooled key | at once | built |
| `pins:<kind>:<sha>`, `pins:cli:revert:<harness>@<version>` | pins pass | a pin proposal failed qualification or is reverted | built |
| `problem:host:<id>:silent` | rule `absence-host` | no heartbeat 3 min (dispatch's `host:<id>:offline` is about placement, this is about the heartbeat) | stage 1 |
| `problem:host:<id>:saturated` | rule `host-saturation` | see telemetry | stage 1 |
| `problem:store:<id>:fill` | rule `store-fill` | free below `free_space_warning` | stage 1 |
| `problem:execution:<id>:stuck` | rule `stuck` | no activity 30 min | stage 1 |
| `problem:execution:<id>:orphans` | rule `orphan` | processes alive after end | stage 1 |
| `problem:resource:failing` | rule `resource-failing` | a resource processor item paused at its maximum retry pause | built |
| `problem:work:stalled` | rule `work-stalled` | a work kit queue whose selection or failure writes keep failing, at the maximum pause | built |
| `problem:backup:missing` | direct: backup pass | no good dump for 48 h | stage 1 (M6) |
| `problem:backup:drill-failed` | direct: drill | last drill failed | stage 1 (M6) |
| `problem:telemetry:clickhouse` | direct: span writer | 3 failed flushes | built |
| `problem:bugsink:<issue>` | direct: the Bugsink alert webhook ([error tracking](fleet-error-tracking.md#5-alerts-become-problems)) | a new Bugsink issue, or one that regressed or passed its mute; closes when Bugsink reports it resolved | built |
| `problem:delivery:<key>:<metric>` | delivery | a stage or job got slower than its baseline | built |
| `gap:<class>` | agent gaps | a class above the finding threshold | built |
| `grants:<connection>` | grants | a connection lacks catalogue tools its users call | built |
| `duty:<bot>:down` | watchdog | a duty bot stopped working (rule 6) | built |
| `burst:<bot>` | run-rate breaker | a bot started about 10× its usual runs in an hour; the bot is paused | not built |
| `run-failed:<bot>:<cause>` | sessions | a bot's session failed for this plain cause | not built |
| `problem:<rule>:<subject>` | other rules | see telemetry | stage 1 and 2 |

Not built: the batch conversation, `woken_at`, the run-rate breaker and bot failures as problems (today one conversation per problem and Home lists a bot's failed sessions); System › Problems reads at most 200 open problems.

All numbers are settings with these defaults: `breaker.factor` 10, `breaker.window` 1 h, `breaker.floor` 20, `breaker.coolMinutes` 30, `problems.cleanRuns` 2, `problems.reopenHours` 24, `problems.stuckWakes` 3, `problems.stuckHours` 48, `duty.downWakes` 3, `duty.newMinutes` 30, `backup.missingHours` 48, `backup.at` 03:30, `backup.daily` 7, `backup.weekly` 4, `forge.prPollMinutes` 5, `system.confirmDelete` false.

### Hosts

*   **`hosts_remove {id, preview}`** (owner, or the holder for their own host). Preview reply: running executions, open sessions, stores on the host with their bytes and whether another replica exists. Confirm: running executions get `execution_stop` with reason `host-removed` (state `cancelled`, not refused); sessions are marked `host-removed`; the host document gets fields `state: removed` and `removedAt` (hosts are `document` rows of kind `host`); host secret, settings and harness placement are deleted; one journal entry. `hosts_list` hides removed hosts unless `includeRemoved`.
*   **Executor:** a poll with a removed host id gets HTTP 410 `host-removed`; the executor logs it, stops polling and exits 0; the unit uses `Restart=on-failure`, so systemd does not restart it.
*   **`host_diagnose {host, wait?}`** (agent tool and System › Hosts, owner or the host's holder; read). What a person used to read on the host itself, so nobody needs SSH or the host's files: the host's last poll, version and update state; its executor log tail (the last 200 lines of the log file below, each with its UTC time, credentials redacted); its outbox (queued rows per route) and its `outbox_failed` rows of the last day (at most 20: route, status, reason, time); failed transfers of the last day; commands of work still open that the host has not confirmed (a command of an execution the coordinator settled meanwhile is history and stays out); and every queued turn or step with this host among its candidates, with the reason dispatch skipped this host. The coordinator asks the host on its next poll (`PollReply.diagnose`); the executor answers in its following poll (`PollRequest.diagnosis`); the reply waits up to `wait` seconds (default 30) for it. An offline host answers with the coordinator's facts and its latest report with its age. `host_diagnosis {host}` (read) answers the latest report without asking; the host detail reads it, asks with `host_diagnose` wait 0 when it opens, and rereads when a report lands (`host_diagnosis` rows feed the change stream as kind `host`); the coordinator asks every online host every 5 minutes (`FLEET_HOST_DIAGNOSIS_TICK_MS`). There is no Diagnose button (owner, 2026-11-26).
*   **Executor log.** The executor writes every line with its UTC time, a start line (version, host, pid) and one status line an hour (polls, outbox rows, failures), so a quiet log means a stopped executor, never a healthy one. It writes the lines to `<FLEET_DATA_DIR>/logs/executor.log` on every platform (rotated at 10 MB, one previous file kept), the file `host_diagnose` reads, and to stderr as before (the unit's log: `~/Library/Logs/fleet-executor.log` from `deploy/hosts/macos/fleet-executor.plist`, the journal of `deploy/hosts/linux/fleet-executor.service`), so nobody opens either by hand.
*   **Store on a removed host:** artifacts with another replica stay readable; artifacts with no other replica are listed in the preview and become `lost` after confirm (data foundation tombstone rules).
*   **Deep purge of a removed host** runs through `system_purge` like every other kind: the host row goes when no execution, session or artifact references it, which for a host with history is never. That keeps history attributable.

### Archive, delete, purge

**Reference map first.** One Go table in the coordinator: for each kind (`host`, `project`, `task`, `execution`, `session`, `experiment`, `connection`, `artifact`), the list of `(table, column)` pairs that reference it, including JSON paths in `document.data`. A test compares it with the schema and fails when a new foreign key or `*_id` column is not listed.

*   **`<kind>_archive {id}` / `<kind>_unarchive {id}`** for project, task, execution, session, experiment, connection: sets `archived_at`; default lists filter it; anyone with write rights on the object. Journal entry.
*   **`<kind>_delete {id, preview}`** (owner): preview returns the counts of objects that go with it (a project takes its tasks, executions, sessions, experiments' trials of that project) and stored bytes. Confirm tombstones all of them (`tombstoned_at`, since `artifact.deleted_at` already records bytes removed by retention; a project's definition row gets `deleted_at` and stays as the reservation of its id), drops secrets (connection keys, run tokens), sends `teardown` for live worktrees, stops running executions (reason `deleted`). A restrictive row-level policy hides tombstoned rows and every row of a deleted project from every read; host calls still see them, so stops and teardowns land. The setting `system.confirmDelete` (default false) makes the app ask once more; the operation is the same.
*   **`system_purge {preview, kinds?}`** (owner): finds tombstoned rows that no live or tombstoned row references (via the reference map), in dependency order (artifact, execution, task, session, experiment, project, connection, host). Preview returns counts and bytes per kind. Confirm deletes rows, then their store objects through the artifact purge path (data foundation §3), then ClickHouse rows with one lightweight `DELETE FROM <table> WHERE execution_id IN (...)` per table per purge (`event`, `measurement`, `span`); coordinator spans without `execution_id` stay (telemetry, no TTL). A store object that fails to delete stays as a purge task retried by the next purge. Journal: one entry per kind.
*   **Built (M2, first part):** `object_delete` for `project`, `experiment`, `task` and `session`, and `system_purge` for `host`, `project`, `experiment`, `task` and `session` (shapes `ObjectDeleteRequest`, `ObjectDeleteReply`, `SystemPurgeRequest`, `SystemPurgeReply` in `contracts/ops-executions.schema.json`; the cascade is `referenceMap` in `go/coordinator/cleanup.go`). Delete refuses (409) while a live row outside the object keeps one of its objects. Prepared project resource summaries (startup flag, off by default) still count a deleted experiment's trials until purge. A task takes its executions (and child runs), questions and artifacts; it stays while a live task depends on it (`task_dependency`), accepted its artifacts, or names it as creator. A session takes its executions, messages and questions; it stays while a live session was forked or created from it, a live task names it as creator, or one of its executions ran for a live task (delete the task first). An open session working for a task keeps it (archive the session first); an archived one outlives the task (the reference goes stale). Definitions: [definition editors, Delete](fleet-definition-editors.md#delete). Not built: `object_archive`, delete of a single execution, connection or artifact, the System screen's Cleanup panel.
*   **Lab:** experiment delete checks scores and recommendations; a recommendation that cites a trial keeps the trial (it stays referenced).
*   **Releases:** current and previous per host, as [self-update prune](fleet-self-update.md) already does. Shown on screen, no new action.

### Worktree on delivery

*   **`cleanup.keepWhen`** (existing project setting, default today `["uncommitted","teardown-failed"]`) gains `unpushed` in its default. The executor reads it from the project payload it already receives; today it ignores the setting and hard-codes two checks.
    *   `uncommitted`: `git status --porcelain` non-empty.
    *   `unpushed`: `git rev-list @{u}..HEAD` non-empty, or no upstream.
    *   `teardown-failed`: an earlier teardown of this worktree failed.
*   **Trigger:** the coordinator sends `teardown` when a session's task is accepted or its PR is merged. **PR merge detection:** the delivery pass already polls the project's merged pull requests; a merged PR whose head branch is `<branchPrefix>/<session>` names its session. Other run kinds (pipeline steps, Idle chats, sessions without a row) follow [host cleanup, sections 10 and 11](fleet-host-cleanup.md#10-worktree-lifecycle-for-every-run-kind).
*   **Local branch:** after a merged PR the executor runs `git branch -D <branch>` (today it uses `-d`, which refuses squash-merged branches) (squash merges leave it unmerged locally); the remote branch stays.
*   **Kept:** abandoned or failed sessions keep their worktree until they are archived, by a person or by the Idle archive ([host cleanup, section 10](fleet-host-cleanup.md#10-worktree-lifecycle-for-every-run-kind)). A teardown refused by `keepWhen` reports the reason; the session shows "worktree kept: unpushed".
*   Journal entry per Fleet teardown (`actor.kind = system`).

### Backups

Copy the pattern of `pg_dump` with daily and weekly rotation (as in common `pg_dump` cron scripts and Postgres docs), plus a restore test, which is what makes a backup real.

*   **Backup pass** in the coordinator (`pass.backup`), daily at the setting `backup.at` (default 03:30 host time): `pg_dump -Fc -d fleet -f /backup/fleet/daily/fleet-<YYYYMMDD>.dump` run as `postgres` through the same `runuser` path self-update uses. Writes to `.tmp`, renames on success, records size and SHA-256 in table `backup` (`id`, `kind daily|weekly|pre-deploy|drill`, `path`, `bytes`, `sha256`, `started_at`, `ended_at`, `outcome`, `error`).
*   **Rotation:** keep 7 daily, 4 weekly (Sunday's daily is hard-linked into `weekly/`), and the pre-deploy dumps of current and previous (self-update keeps that rule). Settings `backup.daily`, `backup.weekly`. Prune after a good dump only, never when the new one failed.
*   **Off-host copy:** after a good dump, put it into the artifact store as a system artifact with a replica on a second host (data foundation §3 placement); rotation removes the store copy too. This uses the existing store; no new service.
*   **Restore drill** `backup_drill {id?}` (owner, also weekly by the pass after the Sunday dump): `createdb fleet_drill`, `pg_restore -d fleet_drill <dump>`, run the coordinator's migration check and three counts (`task`, `execution`, `journal`) against the live database's counts at dump time stored in the `backup` row, `dropdb fleet_drill`. Outcome in a `backup` row of kind `drill`. Failure opens `problem:backup:drill-failed`.
*   **Restore** stays a runbook step (stop coordinator, `pg_restore` into `fleet`), linked from the screen. An in-app restore would replace the database that runs the app.
*   **`backups_list`** returns rows of `backup` with the last drill.

### System screen

One screen (owner decision), from `packages/ui` only, dark theme first. Per-screen buttons stay only where the object already shows (session archive, execution stop).

| Panel | Read from | Actions |
|---|---|---|
| Problems | `problems_list`: owner, phase and note per row, the owner's problem conversation beside it | none: the person writes in the conversation |
| Services | [telemetry §8](fleet-telemetry.md#8-system-screen-read-half) | none |
| Hosts | `hosts_list`, heartbeat readings | pause, resume, remove (preview), rotate secret |
| Stores | `stores_list`, `store_free` | set free-space warning |
| Releases | `hosts_list` versions, current and previous | none (self-update owns it) |
| Backups | `backups_list` | run dump now, run drill |
| Tokens | `connections_token_list` | revoke |
| Cleanup | `system_purge {preview: true}`, archived and deleted counts | purge (preview first) |
| Journal | `journal_list` | filter |

### Changes screen

Requirements for one answer to "is every host on the same setup, and what has not reached it" ([changes](../design-system/pages/changes.md)). Agents and skills travel with each task from the coordinator, so a host needs only the release and the pinned harnesses; a definition change is waiting until its new revision is active, not until a host picks it up.

* **One row per change**, with columns repository, active (the coordinator's store) and one per host:
  * a shipped definition change that is not active yet (`repository-changed`, [definition editors](fleet-definition-editors.md#library)), grouped by the commit or pull request that changed the files ("Skill modern-web-guidance for 9 agents · #672": merged, 0 of 9 active); action: take the repository version;
  * a definition whose repository version differs for another reason (one row each); action: see the change;
  * the release: published, coordinator version, and the version each host runs; action: open a host that is behind;
  * one row per pinned component ([host updates](fleet-host-updates.md#2-one-shape-for-every-kind): agent CLIs in `config/harness-pins.json`, host tools in `config/tool-pins.json`), grouped by kind under the kind's open proposal (`automation/<kind>-pins`: number, the components it changes, stage offline, on hosts, merging, landed, reverting or failed, and its Lab trials passed of total): pinned, the proposal's version with its trial result, and per host on the pin, behind `<version>`, installing, refused `<reason>` or `N of M runs failed` (the pin watch); a kind whose pins are off shows `own` dimmed ("pins off · hosts keep their own"). A host cell opens that host's update history.
* A cell is done, waiting, not reached (with the value it has, such as `1.261119.0`) or not needed. An offline host shows its last heartbeat.
* **Made in Fleet, not in the repository:** counts of store-only agents and pipelines with the ones that ran this month named; action: save to the repository ([CUJ 8](fleet-definition-editors.md#cuj-8-save-what-was-made-in-fleet-to-the-repository)).
* Data: `definition_list` states, `hosts_list` versions and harness reports, the release manifest; `system_changes` `components` joins the release's pin files with the proposals and the pin watch the delivery pass read last (at most once a minute; `go/coordinator/pins_view.go`). Grouping by commit needs the release to record, per shipped definition file, the commit that last changed it; until then a row names the files.
* The System screen's Releases panel keeps its one line per host and links here.
* **Host update history** (System › Hosts › host, Updates): every change of the update result the host reported, newest first, from `host_update_history` (one page per request, loaded on scroll): state, release, the components it brought against the host's last switched release, reason and time on the right; consecutive switches without a component change fold into one `×N` row. A switch to a new release is also an audit journal entry (`host:<id>`, field `release`).

### Operations contract

`$defs` for `contracts/ops.schema.json` (wave 2). `Actor` is the type in [definition editors](fleet-definition-editors.md#contract-defs).

```json
{
  "ObjectKind": {"type": "string", "enum": ["host", "project", "task", "execution", "session", "experiment", "connection", "artifact"]},
  "KindCount": {"type": "object", "required": ["kind", "count"], "properties": {"kind": {"$ref": "#/$defs/ObjectKind"}, "count": {"type": "integer"}, "bytes": {"type": "integer"}}},
  "JournalEntry": {"type": "object", "required": ["seq", "id", "at", "actor", "operation", "target"], "properties": {
    "seq": {"type": "integer"}, "id": {"type": "string"}, "at": {"type": "string", "format": "date-time"}, "actor": {"$ref": "#/$defs/Actor"},
    "operation": {"type": "string"}, "target": {"type": "string"}, "projectId": {"type": "string"}, "field": {"type": "string"},
    "previous": {}, "value": {}, "traceId": {"type": "string"}, "note": {"type": "string"}}},
  "JournalListRequest": {"type": "object", "properties": {"target": {"type": "string"}, "projectId": {"type": "string"}, "actor": {"type": "string"}, "before": {"type": "integer"}, "limit": {"type": "integer", "minimum": 1}}},
  "JournalListReply": {"type": "object", "required": ["entries"], "properties": {"entries": {"type": "array", "items": {"$ref": "#/$defs/JournalEntry"}}}},
  "HostsRemoveRequest": {"type": "object", "required": ["id", "preview"], "properties": {"id": {"type": "string"}, "preview": {"type": "boolean"}}},
  "HostsRemoveReply": {"type": "object", "required": ["id", "runningExecutions", "openSessions", "unreplicatedArtifacts", "removed"], "properties": {
    "id": {"type": "string"}, "runningExecutions": {"type": "integer"}, "openSessions": {"type": "integer"},
    "unreplicatedArtifacts": {"type": "integer"}, "removed": {"type": "boolean"}}},
  "ArchiveRequest": {"type": "object", "required": ["kind", "id"], "properties": {"kind": {"$ref": "#/$defs/ObjectKind"}, "id": {"type": "string"}, "unarchive": {"type": "boolean"}}},
  "ArchiveReply": {"type": "object", "required": ["kind", "id", "archived"], "properties": {"kind": {"$ref": "#/$defs/ObjectKind"}, "id": {"type": "string"}, "archived": {"type": "boolean"}}},
  "DeleteRequest": {"type": "object", "required": ["kind", "id", "preview"], "properties": {"kind": {"$ref": "#/$defs/ObjectKind"}, "id": {"type": "string"}, "preview": {"type": "boolean"}}},
  "DeleteReply": {"type": "object", "required": ["affected", "deleted"], "properties": {"affected": {"type": "array", "items": {"$ref": "#/$defs/KindCount"}}, "deleted": {"type": "boolean"}}},
  "SystemPurgeRequest": {"type": "object", "required": ["preview"], "properties": {"preview": {"type": "boolean"}, "kinds": {"type": "array", "items": {"$ref": "#/$defs/ObjectKind"}}}},
  "SystemPurgeReply": {"type": "object", "required": ["removed", "purged"], "properties": {"removed": {"type": "array", "items": {"$ref": "#/$defs/KindCount"}}, "purged": {"type": "boolean"}, "retryLater": {"type": "integer"}}},
  "BackupKind": {"type": "string", "enum": ["daily", "weekly", "pre-deploy", "drill"]},
  "Backup": {"type": "object", "required": ["id", "kind", "startedAt", "outcome"], "properties": {
    "id": {"type": "string"}, "kind": {"$ref": "#/$defs/BackupKind"}, "path": {"type": "string"}, "bytes": {"type": "integer"}, "sha256": {"type": "string"},
    "startedAt": {"type": "string", "format": "date-time"}, "endedAt": {"type": "string", "format": "date-time"},
    "outcome": {"type": "string", "enum": ["running", "ok", "error"]}, "error": {"type": "string"}, "offHost": {"type": "boolean"}}},
  "BackupsListRequest": {"type": "object", "properties": {"limit": {"type": "integer", "minimum": 1}}},
  "BackupsListReply": {"type": "object", "required": ["backups"], "properties": {"backups": {"type": "array", "items": {"$ref": "#/$defs/Backup"}}, "lastDrill": {"$ref": "#/$defs/Backup"}}},
  "BackupRunRequest": {"type": "object", "properties": {"drill": {"type": "boolean"}, "id": {"type": "string"}}},
  "BackupRunReply": {"type": "object", "required": ["backup"], "properties": {"backup": {"$ref": "#/$defs/Backup"}}}
}
```

Archive and delete are one operation each with a `kind` (`object_archive`, `object_delete`) rather than one per kind: the handler is one function over the reference map. `backup_run` covers both "dump now" and the drill.

### Rights

*   Owner: everything. Hard delete, purge, backup and drill are owner-only.
*   Holder: pause, resume, remove of hosts they hold; rotate their secret.
*   Director or project writer: archive in their projects.
*   PM agent with `fleetOperations`: everything its director may, through the same operations; journal `actor.kind = agent`, `directedBy` set. No extra confirmation unless `system.confirmDelete` is on.

## Alternatives Considered

### Hard delete at once instead of tombstone and purge

*   **Pros:** one step, no tombstones.
*   **Cons:** breaks history: executions, spans and journal entries would point at nothing.
*   **Reason for rejection:** Rejected by author (owner decision): tombstone keeps history attributable; purge removes what nothing references.

### Time-based worktree expiry

*   **Reason for rejection:** Rejected by author (owner decision): delivery is the signal; a time limit would remove work in progress.

### Problems as a separate alert service (Alertmanager)

*   **Reason for rejection:** Rejected by author: the cause-question mechanism exists and gives grouping, single ask and self-close; a second service adds a process and a store.

### Problems as questions addressed to the duty bot

*   **Pros:** the smallest change: dedupe, withdraw and the PM hand-off stay as they are.
*   **Cons:** a fact with occurrences, phases and a watched fix lives as a question with answer buttons; "is this a decision?" becomes "who is it addressed to"; the buttons come back the first time a bot is missing.
*   **Reason for rejection:** Rejected by author (consilium of three models, owner approved 2026-11-26): questions hold decisions only.

### Attempt caps, wake budgets and cooldowns for the duty bot

*   **Pros:** bound cost when a noisy class flaps.
*   **Cons:** an invented limit that stops work the owner wants done.
*   **Reason for rejection:** Rejected by author (owner rule: no invented limits): no progress turns a problem `stuck` and shows it loud; work continues.

### Backups by a host cron script outside Fleet

*   **Pros:** proven, small.
*   **Cons:** invisible to the app; no problem when it fails; no drill.
*   **Reason for rejection:** Rejected by author: the owner runs Fleet from the app; the pass records every dump and drill.

### Physical backups with WAL archiving (pgBackRest)

*   **Pros:** point-in-time recovery.
*   **Cons:** a new tool and a WAL store for a database of a few GB.
*   **Reason for rejection:** Rejected by author: 20/80; daily logical dumps plus pre-deploy dumps cover the loss window the owner accepts. Revisit when the database or the loss window outgrows it.

### Keep `setting-change` documents as the journal

*   **Reason for rejection:** Rejected by author: they cover settings only, live in the generic `document` table without an actor type, and cannot hold archive, delete or purge without stretching `field`.

--------------------------------------------------------------------------------

## Non-Functional Requirements

*   **Never lose the journal.** Entry and change in one transaction.
*   **Preview equals effect.** Delete and purge compute the preview and the effect with the same query; confirm recomputes and returns the actual counts.
*   **Backup safety.** Prune only after a good dump; dumps written to `.tmp` then renamed; checksum stored.
*   **Secrets.** Removed hosts and deleted connections drop secrets at confirm, before purge; the journal never stores a secret value (`value` is `"<dropped>"`).

## Risks & Mitigations

*   **Reference map misses a column.** The schema test fails the build; purge skips rows whose kind the map does not cover.
*   **PR poll rate.** One request per open PR per 5 minutes; GitHub's 5,000 per hour per token covers far more than Fleet's open PRs.
*   **Drill uses disk.** The drill database is dropped at once; a disk-full drill fails, opens `problem:backup:drill-failed`, and the `store-fill` rule shows why.

## Detailed Implementation

Names packages and areas; wave 1 renames Go files.

| Stage | Content | Done when |
|---|---|---|
| M0 Audit journal | `journal` table, writer, copy of `setting-change`, `journal_list`; every existing writer switched | journal rows for `hosts_update` and budget changes |
| M1 Host remove | `hosts_remove` with preview, 410 for removed hosts, list filter | two paused hosts removed from the app |
| M2 Archive, delete, purge | reference map with schema test, `object_archive`, `object_delete`, `system_purge` | owner clears acceptance data in the app |
| M3 Worktree on delivery | executor reads `keepWhen`, `unpushed`, PR merge poll, local branch delete | merged session's worktree gone, branch on remote |
| M4 Problems | `problem` table, writers moved off questions, `problems_list`, `problem_get`, `problem_update`, `problem_escalate`, duty, `problem` trigger kind, watchdog, System › Problems | Needs you on prod holds only owner-level questions; HAL0 takes a new problem, links its fix and closes it |
| M5 System screen | panels of the table above | owner answers "is the system healthy" on one screen |
| M6 Backups | `backup` table, pass, rotation, off-host copy, drill, `backups_list`, `backup_run` | drill passes and shows on screen |

Order: M0 before M1 and M2. M3 independent. M4 needs telemetry stage 1 rules for rule causes (direct causes work without). M5 needs M1, M2, M4 and telemetry §8.

Files and areas:

1.  **Coordinator schema** (Postgres migrations area): tables `journal`, `backup`; columns `archived_at` and `tombstoned_at` on task, execution, session, experiment, connection, `deleted_at` and `purged_at` on the project definition; host documents gain fields `state`, `removedAt` (no migration).
2.  **Coordinator journal area:** writer, migration copy, `journal_list`; replace `setting-change` writes in hosts, accounts, budgets, people, connections and task director code, and the agent-actor path in the router.
3.  **Coordinator hosts area:** `hosts_remove`; 410 in poll handling; list filter.
4.  **Coordinator cleanup area (new):** reference map, its schema test, `object_archive`, `object_delete`, `system_purge`.
5.  **Coordinator problems area (new):** `problems.sync` over `openCauseQuestion`; dispatch stall moves to call it with source `dispatch`; grouping.
6.  **Coordinator forge pass:** PR merge poll; teardown on merge or acceptance.
7.  **Coordinator backup area (new):** pass, rotation, off-host copy, drill, `backups_list`, `backup_run`.
8.  **Executor worktree area:** `keepWhen` from the command, `unpushed`, local branch delete, 410 handling.
9.  **Contracts:** `contracts/host-protocol.schema.json` teardown `Command` gains `deleteBranch` (bool; `keepWhen` comes from the project payload); the `ops` `$defs` above go to `contracts/ops.schema.json` in wave 2.
10. **App (`packages/ui`):** System screen.
11. **Docs:** [people access §7](fleet-people-access.md) stall alert points at Problems here when next edited; `docs/designs/README.md` rows for this design and telemetry.

Tests (`tests/go` and package tests):

1.  Journal: a failing journal insert rolls back the change.
2.  Host remove: preview counts; a poll after remove gets 410; history still names the host.
3.  Reference map: a test schema column `foo_project_id` not in the map fails the test.
4.  Purge: a tombstoned project with a referenced trial keeps the trial and the project; an unreferenced one goes with its files.
5.  Worktree: unpushed worktree kept with reason; clean merged worktree removed, remote branch untouched.
6.  Problems: two runs without a cause close it; a host offline absorbs its executions' error findings.
7.  Backup: rotation keeps 7 and 4 and never prunes after a failed dump; a drill on a corrupted dump fails and opens `problem:backup:drill-failed`.

## Open Questions

*   Default `backup.at` and rotation counts are judgment defaults (settings).
