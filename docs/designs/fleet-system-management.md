# Fleet system management: hosts, cleanup, backups, problems and the audit journal

*   **Author:** Alex Kirs
*   **Status:** Proposed
*   **Date:** 2026-11-11
*   **Linked design:** [telemetry](fleet-telemetry.md). This document owns the audit journal, problems as needs-you questions, and the System screen layout and actions. Telemetry owns spans, the rules pass and the screen's read panels.

--------------------------------------------------------------------------------

## Overview

The owner runs Fleet from the app without a shell: add, pause and remove hosts; free disk; archive and delete test data; see backups and releases; see and answer problems. Every action is an operation over the shared core ([fleet.md R2](../../fleet.md#principles)), so the PM agent can do what its director can. Every system change is written to one audit journal. Nothing is blocked by built-in rules: destructive actions show a preview, limits are settings with permissive defaults.

### Terms

*   **Archive:** hide an object from default lists; keep all data; reversible with `*_unarchive`.
*   **Delete:** mark an object `deleted` (tombstone): it leaves every view and operation, its references stay valid, its secrets are dropped at once. Owner only.
*   **Purge:** remove tombstoned rows that no other row references, and their stored files. On demand, with a preview.
*   **Reference map:** for each object kind, the tables and columns that point at it. Purge and the delete preview read it.
*   **Problem:** a needs-you question with a `cause` key, opened by Fleet from a rule or a direct signal, closed by Fleet when the cause clears.
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
4.  Problems from the [stage 1 telemetry rules](fleet-telemetry.md#6-rules) and direct signals as self-closing needs-you questions.
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

### Problems: needs-you questions from rules

**Copy what exists.** Dispatch already opens one `operator_decision` question per `cause`, withdraws it when the cause is gone, and asks once; the ask-once and withdraw logic sits in the dispatch caller, and `openCauseQuestion` only asks. `problems.sync` takes that logic over, and the dispatch sweep moves into `problems.sync(source=dispatch)` in M4, with the source stored on each question, before any rule writes a key. New keys start with `problem:` so the current sweep regex never matches them. Problems reuse this exact mechanism; the second asking path `cAsk` stays for its current callers and is not used here.

*   **Writers:** `problems.sync(source, causes)` for sources that compute a full set each run (`dispatch`, `rules`): given the full set of current causes from one source, open questions for new causes and withdraw open questions of that source whose cause is not in the set. A cause stays open until 2 consecutive runs without it (setting `problems.cleanRuns`, avoids flapping). One-off senders (span writer, backup pass, drill, update result in a poll) use `problems.open(cause, ...)` and `problems.close(cause)` and never withdraw each other. The question's `data` holds `cause`, `source`, `rule`, `exemplars`, `hostId`, `storeId`.
*   **Changes to the existing helper (M4):** `openCauseQuestion` today hard-codes `options: ["ok"]`; it gains an options parameter, the extra `data` fields, and `dismissed_at`: a dismissed question stays withdrawn while its cause is present and the mark clears when the cause goes.
*   **Recipient:** owner for system causes (no project); the project director for project causes; the cause's holder is named in the text, as the stall question does.
*   **Answers:** `Investigate` (stage 2: the PM opens a task with exemplar links and its analysis; stage 1: a link to `telemetry_trace`) or `Dismiss` (withdraw; it reopens only after the cause clears and returns).
*   **Grouping (Alertmanager-style inhibition):** before sync, findings whose subject depends on a firing absence cause fold into it: `problem:host:<id>:silent` absorbs every finding whose rule row carries that `host_id`; `problem:store:<id>:offline` absorbs store errors; `problem:telemetry:clickhouse` absorbs every rule (rules cannot run). Folded findings stay in `telemetry_findings` with `groupCause`.

| Cause | Source | Fires | Recipient | Built |
|---|---|---|---|---|
| `host:<id>:{offline,trust,paused,holder}`, `subscription:<id>:{paused,not-taken,not-ready,refused}`, `placement:<host>:<harness>`, `budget:<id>:reached` | dispatch | queued work has no candidate | director | built |
| `update:<host>:<version>` | direct: refused or failed update in a poll | at once | holder | built |
| `problem:host:<id>:silent` | rule `absence-host` | no heartbeat 3 min (dispatch's `host:<id>:offline` is about placement, this is about the heartbeat) | owner | stage 1 |
| `problem:host:<id>:saturated` | rule `host-saturation` | see telemetry | owner | stage 1 |
| `problem:store:<id>:fill` | rule `store-fill` | free below `free_space_warning` | owner | stage 1 |
| `problem:execution:<id>:stuck` | rule `stuck` | no activity 30 min | director | stage 1 |
| `problem:execution:<id>:orphans` | rule `orphan` | processes alive after end | owner | stage 1 |
| `problem:backup:missing` | direct: backup pass | no good dump for 48 h | owner | stage 1 (M6) |
| `problem:backup:drill-failed` | direct: drill | last drill failed | owner | stage 1 (M6) |
| `problem:telemetry:clickhouse` | direct: span writer | 3 failed flushes | owner | stage 1 |
| `problem:<rule>:<subject>` | other rules | see telemetry | owner or director | stage 1 and 2 |

All numbers are settings with these defaults, tuned from the first week: `problems.cleanRuns` 2, `backup.missingHours` 48, `backup.at` 03:30, `backup.daily` 7, `backup.weekly` 4, `forge.prPollMinutes` 5, `system.confirmDelete` false. Web push later reads the same questions.

### Hosts

*   **`hosts_remove {id, preview}`** (owner, or the holder for their own host). Preview reply: running executions, open sessions, stores on the host with their bytes and whether another replica exists. Confirm: running executions get `execution_stop` with reason `host-removed` (state `cancelled`, not refused); sessions are marked `host-removed`; the host document gets fields `state: removed` and `removedAt` (hosts are `document` rows of kind `host`); host secret, settings and harness placement are deleted; one journal entry. `hosts_list` hides removed hosts unless `includeRemoved`.
*   **Executor:** a poll with a removed host id gets HTTP 410 `host-removed`; the executor logs it, stops polling and exits 0; the unit uses `Restart=on-failure`, so systemd does not restart it.
*   **Store on a removed host:** artifacts with another replica stay readable; artifacts with no other replica are listed in the preview and become `lost` after confirm (data foundation tombstone rules).
*   **Deep purge of a removed host** runs through `system_purge` like every other kind: the host row goes when no execution, session or artifact references it, which for a host with history is never. That keeps history attributable.

### Archive, delete, purge

**Reference map first.** One Go table in the coordinator: for each kind (`host`, `project`, `task`, `execution`, `session`, `experiment`, `connection`, `artifact`), the list of `(table, column)` pairs that reference it, including JSON paths in `document.data`. A test compares it with the schema and fails when a new foreign key or `*_id` column is not listed.

*   **`<kind>_archive {id}` / `<kind>_unarchive {id}`** for project, task, execution, session, experiment, connection: sets `archived_at`; default lists filter it; anyone with write rights on the object. Journal entry.
*   **`<kind>_delete {id, preview}`** (owner): preview returns the counts of objects that go with it (a project takes its tasks, executions, sessions, experiments' trials of that project) and stored bytes. Confirm sets `deleted_at` on all of them, drops secrets (connection keys, run tokens), sends `teardown` for live worktrees, stops running executions (reason `deleted`). The setting `system.confirmDelete` (default false) makes the app ask once more; the operation is the same.
*   **`system_purge {preview, kinds?}`** (owner): finds tombstoned rows that no live or tombstoned row references (via the reference map), in dependency order (artifact, execution, task, session, experiment, project, connection, host). Preview returns counts and bytes per kind. Confirm deletes rows, then their store objects through the artifact purge path (data foundation §3), then ClickHouse rows with one lightweight `DELETE FROM <table> WHERE execution_id IN (...)` per table per purge (`event`, `measurement`, `span`); coordinator spans without `execution_id` stay (telemetry, no TTL). A store object that fails to delete stays as a purge task retried by the next purge. Journal: one entry per kind.
*   **Lab:** experiment delete checks scores and recommendations; a recommendation that cites a trial keeps the trial (it stays referenced).
*   **Releases:** current and previous per host, as [self-update prune](fleet-self-update.md) already does. Shown on screen, no new action.

### Worktree on delivery

*   **`cleanup.keepWhen`** (existing project setting, default today `["uncommitted","teardown-failed"]`) gains `unpushed` in its default. The executor reads it from the project payload it already receives; today it ignores the setting and hard-codes two checks.
    *   `uncommitted`: `git status --porcelain` non-empty.
    *   `unpushed`: `git rev-list @{u}..HEAD` non-empty, or no upstream.
    *   `teardown-failed`: an earlier teardown of this worktree failed.
*   **Trigger:** the coordinator sends `teardown` when a session's task is accepted (exists) or its PR is merged. **PR merge detection** (new): the coordinator's forge pass polls open PRs of sessions with a PR link every 5 minutes through the project's GitHub connection (`GET /repos/{o}/{r}/pulls/{n}`, `merged_at`). Webhooks later.
*   **Local branch:** after a merged PR the executor runs `git branch -D <branch>` (today it uses `-d`, which refuses squash-merged branches) (squash merges leave it unmerged locally); the remote branch stays.
*   **Kept:** abandoned or failed sessions keep their worktree until a person archives or closes the session. A teardown refused by `keepWhen` reports the reason; the session shows "worktree kept: unpushed".
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
| Problems | open cause questions | answer, dismiss |
| Services | [telemetry §8](fleet-telemetry.md#8-system-screen-read-half) | none |
| Hosts | `hosts_list`, heartbeat readings | pause, resume, remove (preview), rotate secret |
| Stores | `stores_list`, `store_free` | set free-space warning |
| Releases | `hosts_list` versions, current and previous | none (self-update owns it) |
| Backups | `backups_list` | run dump now, run drill |
| Tokens | `connections_token_list` | revoke |
| Cleanup | `system_purge {preview: true}`, archived and deleted counts | purge (preview first) |
| Journal | `journal_list` | filter |

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
| M4 Problems | `problems.sync`, grouping, direct signals; telemetry stage 1 rules feed it | an offline host raises one question that closes itself |
| M5 System screen | panels of the table above | owner answers "is the system healthy" on one screen |
| M6 Backups | `backup` table, pass, rotation, off-host copy, drill, `backups_list`, `backup_run` | drill passes and shows on screen |

Order: M0 before M1 and M2. M3 independent. M4 needs telemetry stage 1 rules for rule causes (direct causes work without). M5 needs M1, M2, M4 and telemetry §8.

Files and areas:

1.  **Coordinator schema** (Postgres migrations area): tables `journal`, `backup`; columns `archived_at`, `deleted_at` on project, task, execution, session, experiment, connection; host documents gain fields `state`, `removedAt` (no migration).
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
*   Exact cascade list for project delete is generated from the reference map in M2 and listed in that PR.
