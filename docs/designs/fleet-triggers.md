# Fleet triggers: one mechanism for timed and change wakes

A trigger puts a message into a conversation, or wakes a bot, when its time comes. It can also require that something in its focus has changed since it last fired. It replaces three hard-coded mechanisms: the session watch (`watch.go`: change kinds plus a timer), the bot's own daily wake and Needs you switch (bot settings `scheduleTime`, `scheduleZone`, `wakeOnNeedsYou`), and bot automations (`bot_automations.go`). Presets are ordinary trigger values kept as files.

## Problem

Measured on prod on 2026-11-25: the conversation timer in C-FLT-592 sends a fixed text every 5 minutes, and each such wake read about 400k tokens only to answer "no changes". Two causes were fixed separately: instructions re-injected each turn (#890) and a handoff that grew without bound after host moves (#891). The third cause is the mechanism itself: a timer wakes the model even when nothing changed.

The three mechanisms do the same thing: at a time or on a change, they put a message into a session. Each has its own table or settings fields, its own scheduler, its own fixed message text in code, and its own request-id prefix (`watch:`, `timer:`, `limits:`, `automation:`, `schedule:`). Queries parse these prefixes with `LIKE` to find the source of a message.

## Decisions (owner, 2026-11-25)

1. **One entity.** A trigger has a target: a session (the message arrives in that conversation, with its context) or a bot (the bot wakes in its home session through `admitBotWake`: a bot run, pause and active-run rules). Schedule, condition, text and state are shared. Only delivery depends on the target.
2. **Events now.** Change wakes (watch kinds, Needs you) move into triggers in the same change as timers and daily wakes. An event trigger is not a separate kind: it is `every` at the pass interval (`FLEET_TRIGGER_TICK_MS`, 15 s by default) with a change condition. A trigger that checks each pass counts no quiet checks. `trigger_list` answers the pass interval as `passMs`, so a client that creates an event trigger (the bot page's Needs you) sets `everyMs` to it, and a view tells an event trigger from a timer by it.
3. **Quiet checks show as one line.** A check that finds no change sends no message and makes no model call. The trigger counts quiet checks since its last fire. The conversation shows one collapsed line, updated in place, for example "Timer: 12 checks without changes since 14:05".
4. **Presets are files.** `config/triggers/*.json`, reviewed by pull request like agent profiles. Project presets come later through definitions, if needed.
5. **The condition is on by default.** Migrated watch timers (the HAL0 5-minute timer included) and the conversation-check preset get the condition "only if something changed". A timer without the condition stays available as an edit.
6. **Old storage goes now.** The `watch`, `bot_automation` and `bot_wake_cursor` tables and the bot settings fields `scheduleTime`, `scheduleZone` and `wakeOnNeedsYou` are migrated; their storage is dropped by a later migration, once no release a rollback may start still reads it. The operations `watch_get`, `watch_set`, `watch_timer_set`, `watch_stop`, `bot_automations` and `bot_automation_save`, and the `timers` and `automations` of `session_context`, were thin wrappers over triggers until the app moved to the trigger operations; they are deleted. The session view and the bot page read `trigger_list` (a bot trigger's last run is its `lastRun`), the composer edits a trigger with `/trigger` commands ([composer](../design-system/controls/composer.md#commands)), and agents call `trigger_list` and `trigger_save`.

Unchanged defaults: no limits (no minimum interval, no trigger count cap); the same rights as today; the "child session finished" notice (`wake:<execution>`, `agent_turn.go`) and board wakes stay built-in notices, not triggers.

## Trigger

| Field | Meaning |
| --- | --- |
| `id` | UUID chosen by the caller (idempotent create), as bot automations. |
| `sessionId` or `botId` | The target. Exactly one. |
| `projectId` | The project of the target (row-level security); null for a session without a project. |
| `name` | Shown in lists and in the history line. |
| `presetId` | The preset the trigger was made from; a label only. |
| `text` | The message text. It replaces the fixed texts in code. |
| `when` | `every` (`everyMs`), `daily` (`time` `HH:MM`, IANA `zone`) or `manual` (only `trigger_run`). |
| `ifChanged` | Change kinds: `task`, `question`, `execution`, `host`, `limits`. Empty: fire on time without a check. |
| `enabled` | Paused when false. A paused trigger keeps every value. |
| `revision` | Optimistic concurrency for saves and deletes. |
| state | `cursor` (change log position), `nextAt`, `lastSlot` (daily date), `fires`, `lastFireAt`, `quietChecks` and `quietSince` (checks without change since the last fire), `lastError`, `limitsSignaled`. |

Focus for the condition: a session target uses the session's focus (`focus_set` projects, else every project its director may see), as watch does today. A bot target uses the bot's project.

## Pass

One single-flight pass runs every `FLEET_TRIGGER_TICK_MS` (default 15000; a setting, not a limit) and also when the change stream wakes, which the bot pass does today. For each enabled trigger whose time has come:

1. **Busy target.** If a trigger message is still queued for the session, or the bot has an active run, skip the trigger and keep its state. The next pass covers everything.
2. **Condition.** If `ifChanged` is not empty, read the change log after `cursor` with the watch filter: kind in `ifChanged`, project in focus, director scope, own executions and child sessions excluded, each change classified by its current row (the [watch table](fleet-pm-agent.md#5-watch-mode)). `limits` adds subscriptions projected empty before their reset, once per reset. A pruned history adds "change history was pruned; re-read your focus".
3. **Quiet.** If nothing remains: increase `quietChecks`, set `quietSince` on the first quiet check, move `cursor` to the log head, schedule the next time. Send no message.
4. **Fire.** Otherwise send `text`, followed by the change list when there is one (`Fleet: 3 changes in your focus: task t1 now failed; …`, IDs and states only). The list covers every change kind (limits aside, each trigger signals those itself) that the target's enabled change triggers watch, read from the earliest of their cursors, and afterwards all of them start after it. A change trigger and a 5-minute check on one conversation then never report a change twice and never drop one. A session target gets an ordinary queued message with `sentBy: Fleet`, `source: trigger`, `payload.triggerId` and the idempotency key `trigger:<id>:<slot>`. A bot target goes through `admitBotWake` with the bot run's `triggerId`. Then, when the wake was sent or admitted (a paused bot's skipped wake shows in `bot_runs` only), increase `fires`, set `lastFireAt`, reset `quietChecks`; move `cursor`, schedule the next time.
5. **Error.** Keep the state and write `lastError`; lists show it.

`every` schedules `nextAt = now + everyMs` and is due from half a pass before it, so a trigger every pass checks on each pass. `daily` fires once per local date at or after its time, as bot automations do today (a time that a clock change removes is skipped that day). `manual` fires only through `trigger_run`.

The source of a message is `source: trigger` plus `payload.triggerId`, not a request-id prefix. Queries that ask "is a trigger wake still queued" read `payload->>'triggerId'`.

## Presets

A preset is a JSON file `config/triggers/<id>.json` with the trigger fields except target, state and revision: `{id, name, description, text, when, ifChanged, target: session|bot}`. `trigger_save` copies the preset's values into the trigger when the request names `presetId` and omits a field. Changing a preset does not change triggers made from it.

| Preset | When | Condition | Text (summary) |
| --- | --- | --- | --- |
| `conversation-check` | every 5 min | task, question, execution | Review your focus with `task_page` and `questions_list`. |
| `focus-changes` | every 15 s (each pass) | task, question, execution | none beyond the change list |
| `daily-reconciliation` | daily 09:00 UTC | none | Reconcile your project: open tasks, Needs you, runs since the last reconciliation. |
| `needs-you` | every 15 s (each pass), bot target | question, execution | Handle new Needs you entries in your project. |

## Operations

Agents first ([R7](../../fleet.md#principles)); the UI moves later (the session context panel and composer editing belong to the UI redesign).

| Operation | Request | Reply |
| --- | --- | --- |
| `trigger_list` | `sessionId` or `botId` | triggers with state, `canManage`, presets |
| `trigger_save` | `id`, `baseRevision` (0 creates), target, `presetId?`, fields | the saved trigger |
| `trigger_delete` | `id`, `baseRevision` | `{deleted}` |
| `trigger_run` | `id` | fires now, whatever `when` and the condition say |

Pause and resume are a save with `enabled`. Rights are today's rights: a person who directs the session or the bot (or has full access), and an agent for its own session or for a session or bot its director directs. A wake is a message, not a call: what the woken agent does is checked under its own director, as watch wakes are today.

## Migration

One schema migration:

- Each `watch` row becomes up to two session triggers: its `kinds` become `focus-changes` (every 15 s, `ifChanged = kinds`), and its timer becomes `conversation-check` (`everyMs` from `timerMs` or `pausedTimerMs`, enabled unless paused, `ifChanged = task, question, execution`, text from today's fixed timer text). Both keep the watch's `cursor`, and the timer keeps `nextWakeAt`.
- Each `bot_automation` row becomes a bot trigger with the same id: `schedule` becomes `daily`, `needs_you` becomes every 15 s with `ifChanged` from its event (`all`: question and execution), and `manual` stays `manual`. `instructions` become `text`. The trigger keeps `event_cursor` and `last_schedule_date`.
- A bot whose current settings have `scheduleTime` or `wakeOnNeedsYou` gets the matching bot triggers. Its `bot_wake_cursor` gives the cursor and the last daily slot.
- `bot_run.automation_id` becomes `bot_run.trigger_id`.
- The `watch`, `bot_automation` and `bot_wake_cursor` tables, `bot_run.automation_id` and the three bot settings columns stay unused (expand only), so a rollback to a release up to five back still starts (`deploy/migrate-check.sh`); a later migration drops them. The settings fields leave the contract now, and the bot editor stops showing them.

The wrappers read and write triggers: `watch_*` uses the session's `focus-changes` and `conversation-check` triggers, and `bot_automation*` uses the bot's triggers with the same ids.

## Not in this change

- UI: a trigger list in the session context panel, the collapsed quiet-check line in the thread, and editing through composer commands. The UI redesign session takes these from the operations above.
- Project presets through definitions.
- Fresh context for a conversation trigger (run the check in a short child session that reports back). A bot target already gives a fresh small context.
