# History: one feed of what happened, for every scope

Status: accepted (owner picked option A with the panel, 2026-11-26). Step 1 (Telegram row security, grant moves with their project) and step 2 (the activity table and `history_view`) are built; the screens follow.

## Problem

Fleet records what happens in many places: runs in `execution`, changes in `audit_log`, questions in `question`, experiments in `experiment`, trigger fires in `bot_run`, Telegram in its journal. On 2026-11-26:

- The only record across kinds, the `change` log, is kept for 24 hours. It serves stream reconnects, not history.
- No read covers more than one kind, and scope filters and cursors differ per operation.
- Live "Just now" is a client fold of the last 24 hours of runs.
- The audit has no screen, and 46 of its last 100 rows were a person's own preferences (pins, widths, panels).
- Some actions leave no lasting record: task state moves, experiment launches and stops.

The owner asked for one mechanism: a feed of everything, and the same history for a section (Lab: its recent runs and experiments), a subsection or one object.

## Model

**An activity entry** (table `activity`, migration 122) records one thing that happened. It holds:

- `kind`: `run`, `task`, `question`, `lab`, `settings` or `access`;
- `verb`, such as started, finished, failed, created, answered or changed;
- `outcome`: `ok`, `failed`, `stopped`, `waiting`, `running` or `info`;
- `section`: `work`, `lab` or `system`;
- `subject` (`task:<id>`, `execution:<id>`, an audit target) and its title at the time;
- the actor;
- the project and the objects it belongs to: task, session, host, bot, experiment;
- a `group_key` for folding repeats;
- small `detail` facts.

**One writer.** `fleet_activity()` (`schema/functions.sql`) runs from triggers on `execution`, `task`, `question`, `experiment` and `audit_log`, in the same transaction as the write it records. It records:

- a run's start (agent and pipeline runs) and its end (also host jobs);
- a task created or moving state;
- a question asked, answered or withdrawn;
- an experiment created, moving state or decided;
- every audit row except a person's own preferences (target `person:<actor>`).

The `audit()` writer stays as it is; its rows reach history through the trigger. The function writes in the coordinator's scope, so recording never fails a write, and a reader sees an entry through the table's policy:

- the reader sees its project;
- or, for an entry without a project, the reader sees its session or holds the entry.

Migration 122 backfills from what the source tables still hold. Entries have no default expiry ([R5](../../fleet.md)).

**One read.** `history_view` (`view: true`, `changes: ["activity"]`) returns entries newest first:

- Narrow with `section`, `projectIds` (`-`: outside projects) or one object (`taskId`, `sessionId`, `hostId`, `botId`, `experimentId`).
- Filter with `kinds`, `actor` and `outcome`.
- Page older entries with `before` set to the reply's `nextCursor`.

Every condition compares a column that an index of migration 122 leads with, so under RLS a scoped page starts from its index ([data layer](fleet-data-layer.md)). Consecutive entries of one group with the same verb and outcome come back as one entry with `n` and the oldest `firstAt` (OR-58).

**Live tail.** Each entry is a change of kind `activity`. A mounted history rereads its first page when one arrives. The first page is small, and entries are far fewer than execution changes.

**The same list for every scope:**

| Where | Request |
|---|---|
| History (rail) | `{}` |
| Lab › History, Lab's empty detail pane | `{section: "lab"}` |
| An experiment | `{experimentId}` |
| A task's History tab | `{taskId}` |
| A conversation, a host, a bot | `{sessionId}`, `{hostId}`, `{botId}` |

## Screens (option A with the panel)

The board is `docs/design-system/board/drafts/history.bl`.

- **History** is a rail item under Live. It is a list-detail page:
  - kind chips, actor and outcome filters, scope chips (`All › fleet › Lab › …`, × widens);
  - rows with the state icon, title, kind, actor, ×N and the time rightmost (OR-64), grouped by day, loading on scroll;
  - a row click opens its details on the right.
- **Each section** gets a History view chip (Lab: All · Mine · Waiting · History). With nothing selected, its detail pane shows the newest 5 to 10 entries of its history. The heading opens the full scoped History.
- **Objects** (task, conversation, host, bot, experiment) get a History tab. The task's History tab (`task_get.history`, #1006) becomes `history_view {taskId}`, so state moves, delivery and questions interleave with its conversations.
- **Phone:** History takes a bottom-bar slot. A section's History is a chip, and an object's is a tab.
- **Live "Just now"** becomes the newest entries of this feed, and its heading opens History.

## Not yet recorded

The following have no entry yet:

- delivery stages (`delivery_pr` is a GitHub mirror without an actor);
- trigger quiet checks;
- Telegram turns;
- publishing actions.

Each joins through `fleet_activity()` when its source table gets a trigger.
