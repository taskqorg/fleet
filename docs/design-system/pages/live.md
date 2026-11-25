# Live (screen 2)

**Status.** Built (`app/src/views/live.tsx`, FLT-50). [Hosts](hosts.md), [Runners](runners.md) and [Delivery](delivery.md) keep their detail; Live is the glance over them.

**App route:** `/live`. A wall mode, `/live?wall`, hides the navigation and the shell header for a TV.

**Purpose.** Answers "what runs where right now, and what is broken?" ([principles](../owner-rules.md#principles) 1). It is the technical picture of the whole fleet: hosts, worker slots, projects, delivery and CI. It is built to watch from across a room: it moves while work runs, and when something breaks that is the loudest thing on the screen.

## Layout

A full-width page right of the navigation ([OR-24](../owner-rules.md)); in wall mode the page fills the screen. From top to bottom:

1. **Status bar.**
   * The product mark and the fleet status pill: "All systems working" in the success tone, or the first problem in words in the danger tone, with a count when there are more ([OR-56](../owner-rules.md)).
   * The focus (OR-34) and the clock.
2. **Figures.** Six tiles, each with a sparkline of the same figure's recent trend:
   * Agents working (busy slots of all slots)
   * Agent runs today (plus host jobs and tool calls)
   * Tasks done today (with the lead-time median)
   * Tokens today (with the month)
   * Spend today (list price, with the month)
   * Failed starts in the last 6 hours (with the cause)

   Figures follow [formats](../formats.md) and OR-59: rounded value in the tile, qualifiers in the tooltip.
3. **Hosts** (main column), one band per host, in this order: hosts with a problem, then by busy slots. Each band:
   * Head: state dot, name, platform and free disk.
   * Gauges for CPU, memory and disk.
   * The day's runs and active time.
   * One tile per worker slot:
     * A busy slot shows the task title, code, project colour on the left edge and this run's elapsed time, then the [work facts](../controls/work-facts.md): the task's attempts (`×N`), its time in work and the agent mark.
     * A free slot is a dashed "idle" tile; past three free slots, one tile counts them (`23 idle`).
     * A failing slot is outlined in the danger tone and says what fails, how many times (`×N`), how long ago the latest failed start was, and when it retries.
   * A 24-hour strip of runs, one tick per run window, with failures in the danger tone; marks at −24 h, −18 h, −12 h, −6 h and now.
4. **Side column.**
   * Projects now: running, queued and done counts per project, with a sparkline.
   * In work: every open task (working, then waiting, then queued; longest in work first) with its work facts: attempts, time in work and the agent; the latest attempt and the time in its state on hover. A task stuck in retries shows here between its attempts, when no slot shows it.
   * Delivery: pull requests moving through checks, merge, release and install, each with its time since opened (time in the stage on hover) and its stage chip.
   * CI runners: jobs running and minutes today; self-hosted runner state.
   * Just now: the latest events, each with age, a state dot, the task or conversation it belongs to and the agent; repeats are one event with `×N` (OR-58).
   * Each side list shows its latest five, then `+K more`, which opens the rest in place (OR-30).

Every element opens its detail in the app: a host in System, a slot or event in its session, a pull request in its project's delivery facet, a project in Projects. In wall mode nothing opens; the screen only shows.

## Balance

*Target* ([decision](../decisions.md#2026-11-25-balancing-lives-on-live-as-items), [work allocation and balancing](../../work-allocation.md#interface)). A Balance block answers "where may new work go, and why". One line per item, state first (OR-22):

* host: In, Out until a time, or cap;
* account: Preferred, Default, Avoid or Out, with what is left and when it resets; Only its holder for a personal one;
* model class: its effective candidate order ("Antigravity first, then Codex, then Claude");
* pinned project: the hosts and subscriptions it may use.

Account lines list their logins (host and state: ready, not logged in, expired). Under the lines: the placement order in plain words, and the latest picks and moves, each with its reason.

Each line says why in words: who set it, when, until, what it moved. A subscription with quota that will go unused carries the attention tone and the offered preference. The row menu (OR-37) offers In or Out, Prefer, Default or Avoid, Until and Undo; HAL0 makes the same changes by voice. An out host's band is dimmed with its state word. In wall mode the block only shows. Data: `balance_view`; writes: `balance_set`, `balance_undo`. Canvas: the frame "Live · Balance block (target)" on [screens/live.bl](../board/screens/live.bl). Not built.

## Behaviour

* **Updates in place.** Values update in place with fixed-width figures (OR-6). A running slot's dot pulses, and its elapsed time ticks every second.
* **Colour.** Nothing moves except what changed (OR-23). Colour carries meaning only: the running tone for work, the success tone for done, the danger tone only for trouble (OR-56), and attention for "waiting for you".
* **When something breaks.**
  * The status pill turns to the danger tone with the cause in words.
  * The host band glows in the danger tone, and a line under its head says what fails, since when, and what still works.
  * The failing slot names the error and the next retry.
  * Ticks in the danger tone appear on that host's strip, and the failed-starts figure rises.
  * Repeats aggregate into one problem with `×N` (OR-58).
  * A red host band and the status pill open System (`/system`), whose problem row names the cause and the fix and opens the host (J5). A healthy band opens its host; a failing slot opens its host.
* **Recovery.** When the cause clears, the danger tone fades within one update.

## States

* **Loading:** band and tile skeletons; the status pill says "Connecting…".
* **Stale or offline:** the last known picture stays, dimmed, with "Last update 2m ago" in the status bar; nothing pulses ([offline marker](../controls/offline-marker.md)).
* **No hosts:** one tile that explains how to register a host, linking to System › Hosts.
* **Phone (390 px):**
  * Figures in two columns, then one host band per screen width with slots stacked.
  * The side column moves below the hosts.

## Inventory

| Inventory item | Shown by |
|---|---|
| Load and capacity by host and account | Host bands: gauges, worker slots, busy of all |
| Everything that executes work, per host, with time and resources | Slot tiles and the 24-hour run strip |
| Spend today and this month | Spend figure tile |
| Delivery stages in flight | Delivery list in the side column |
| CI jobs and runners | CI runners list in the side column |
| Problems that need a look | Status pill, host danger tone, failing slot, failed-starts figure |
| Balance now, take out and put back, preferences, unused quota (target) | Balance block |

## Data

| Part | Read |
|---|---|
| Host bands | `hosts_list`: `slotLimit`, `running`, `runningByHarness`, `queued`, `problems`, `pollError`, `lastSeenAt` |
| Gauges | `hosts_list` `load`: CPU (`100 * load1 / cpus`), memory (`memFree` of `memTotal`) and free disk (`diskFree`), while the host polls and the reading is under 2 minutes old |
| Slot tiles | `hosts_list` `runs` in slot order: tile *i* shows `runs[i]` (title and harness from `executions_runners`; code, project, `executions` and `startedAt` from `task_page`); a band covers `max(slotLimit, running)` slots |
| In work | `task_page` open tasks: `executions` (attempts), `lastStartedAt` (latest attempt), `startedAt` (first attempt), `state` and `stateAt`, `agent` else `lastAgent`; outliers against the same rows (`peerFence`) |
| Failures | `failure` on `executions_list` rows and `bot_runs` records: `text` in plain words, `code` under Details, `cause` to group by |
| Strips | `executions_runners` (the newest runs, per host, with time strips) |
| Figures | `execution_totals` (via `executions_runners`: runs, tokens and list-price spend today and this month), `task_page` (tasks done today and their lead time) |
| Projects now | `task_page` grouped by project with state counts; done today from the completed tasks |
| Delivery | `delivery_status`: `stage`, `since` (time in the stage), `opened` |
| CI | `executions_ci` |
| Events | the runs of `executions_runners` (started, finished, failed), grouped by task, else session, and state; named by `taskTitle`, else `sessionTitle` |
| Live updates | `changes_since` and timers: `hosts_list` every 5 s, `executions_runners` every 15 s |

## Open

* **Failing slot retry:** no read names when a failed start retries, so the failing tile shows the cause without a countdown.
* **Disk gauge fill:** hosts report free disk only; a fill needs the disk's size in the heartbeat (a host-protocol change).
* **Delivery attempts:** `delivery_status` has no rerun or queue-removal count per pull request (`delivery_history` has `queueRuns` and `queueRemovals`), so a Delivery row shows no `×N` yet.
* **Canvas:** the work facts and the side-column rows are a group of their own on [screens/live.bl](../board/screens/live.bl) (owner pick B, 25 Nov); the 1440 and 390 frames above it still show the earlier side column.

Settled on 25 Nov ([FLT-47](https://youtrack.ufobe.com/issue/FLT-47)): CPU load and slot identity come from `hosts_list`, and wall mode uses a viewer grant ([people access, Wall viewer](../../designs/fleet-people-access.md#wall-viewer)). Slot identity is the start order, not a stored slot number: executors run a host's work as one pool with a count limit and number no slots, so a stored number would be a second fact that could disagree with the pool.

## Blocks

* [Status bar](../controls/status-pill.md#status-bar) with the [status pill](../controls/status-pill.md): the top line.
* [Navigation rail](../controls/nav-rail.md#board) and [shell header](../patterns/app-shell.md#board): the frame outside wall mode.
* [Figure tile](../controls/figure-tile.md): the six figures with sparklines.
* [Host band](../controls/host-band.md): one band per host.
* [Gauge](../controls/fill-bar.md#gauge): CPU, memory and disk in a band head.
* [Worker slot](../controls/worker-slot.md): one tile per slot in a band.
* [Run strip](../controls/time-strip.md#run-strip): the 24-hour strip of a band.
* [Live row](../controls/live-row.md): projects, tasks in work, delivery, CI runners and events in the side column.
* [Work facts](../controls/work-facts.md): attempts, latest attempt, time in work and in state, agent, in slots and work rows.
* [State dot and word](../controls/state-badge.md#dot-and-word): states in slots and rows.

## Design board

The rendered states are on the [design board](../README.md#design-board), canvas [screens/live.bl](../board/screens/live.bl): normal and "something breaks" in wall mode at 1920 px, then Live inside the app shell at 1440 px. Every part is an instance of a block on [blocks.bl](../board/blocks.bl) (see Blocks above). Totals were taken from the live Runners screen on 24 Nov; slot contents are illustrative. An approved state is rebuilt from `packages/ui` and the book (OR-13).
