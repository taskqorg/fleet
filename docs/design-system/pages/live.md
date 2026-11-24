# Live (screen 2)

**Status.** Target. Until it is built, the same facts are spread over the [Overview](overview.md), [Hosts](hosts.md), [Runners](runners.md) and [Delivery](delivery.md).

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
     * A busy slot shows the task title, code, project colour on the left edge, harness, elapsed time and tokens.
     * A free slot is a dashed "idle" tile.
     * A failing slot is outlined in the danger tone and says what fails and when it retries.
   * A 24-hour strip of runs, one tick per run window, with failures in the danger tone; marks at −24 h, −18 h, −12 h, −6 h and now.
4. **Side column.**
   * Projects now: running, queued and done counts per project, with a sparkline.
   * Delivery: pull requests moving through checks, merge, release and install, each with its stage chip.
   * CI runners: jobs running and minutes today; self-hosted runner state.
   * Just now: the latest five events, each with age and a state dot.

Every element opens its detail in the app: a host in System, a slot or event in its session, a pull request in its project's delivery facet, a project in Projects. In wall mode nothing opens; the screen only shows.

## Behaviour

* **Updates in place.** Values update in place with fixed-width figures (OR-6). A running slot's dot pulses, and its elapsed time ticks every second.
* **Colour.** Nothing moves except what changed (OR-23). Colour carries meaning only: the running tone for work, the success tone for done, the danger tone only for trouble (OR-56), and attention for "waiting for you".
* **When something breaks.**
  * The status pill turns to the danger tone with the cause in words.
  * The host band glows in the danger tone, and a line under its head says what fails, since when, and what still works.
  * The failing slot names the error and the next retry.
  * Ticks in the danger tone appear on that host's strip, and the failed-starts figure rises.
  * Repeats aggregate into one problem with `×N` (OR-58).
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

## Data

| Part | Read |
|---|---|
| Host bands | `hosts_list`: `slots`, `running`, `runningByHarness`, `queued`, `problems`, `pollError`, `lastSeenAt` |
| Gauges | `telemetry_summary`: memory, disk and CPU count per host |
| Slot tiles and strips | `executions_runners` (the newest runs, per host, with time strips) and `executions_list` for the running ones |
| Figures | `execution_totals` (via `executions_runners`), `task_page` counts, `spend_summary` |
| Delivery | `delivery_status` |
| CI | `executions_ci` |
| Events and live updates | `changes_since` |

## Open

* **CPU load:** no read gives current CPU load per host; the gauge needs it from host telemetry.
* **Slot identity:** `slots` is a count, not a list. Placing a run in a slot tile needs the run-to-slot assignment, or tiles drawn from the running executions in start order.
* **Wall mode:** read-only access for the wall mode needs a viewer grant without other rights.

## Design board

The rendered states are on the [design board](../README.md#design-board), canvas [screens/live.bl](../board/screens/live.bl): normal, and "something breaks". Totals were taken from the live Runners screen on 24 Nov; slot contents are illustrative. An approved state is rebuilt from `packages/ui` and the book (OR-13).
