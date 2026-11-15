# Delivery (operator)

App only. Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** Answers, in this order: what in delivery needs a look, what is moving now, and what the pace is. Covers the pull requests of all connected projects, from open to installed on hosts.

**Layout.** [List and detail](../patterns/list-detail.md), like Tasks ([D: owner, 2026-11-15](../decisions.md)).

*Control column.* The page head shows the age of the last GitHub read (red after 15 minutes) and a `public GitHub` chip while no key is on the `github` connection. The [filter bar](../controls/filter-bar.md) has the search (`state:` and `project:` terms), the [state counters](../controls/state-counters.md) of the `delivery` family as the stage quick filter, then Group (Stage, Project, None) and Sort (in stage since, opened). Rows are grouped by stage in attention order: Checks failed, Queue checks, Waiting in queue, PR checks, Open, Releasing, Installing, Installed (and Merged for repositories without releases). A row: stage icon, title, `#number` and the release; the time in the stage on the right, red past the stage's limit. A row opens the pull request in the work area.

*Work area, nothing open:*
1. **Needs a look**, only when something does: the open regressions (`delivery_regressions`), pull requests whose checks failed, and pull requests past their stage's limit, as [problem rows](../controls/problem-row.md); the latest 3 and `+K` (OR-30).
2. **Moving now, 6 hours:** one lane per pull request not yet installed, in list order, on a shared 6-hour axis ([time strip](../controls/time-strip.md) with parts): the span before the current stage (one part: the current read knows only when the pull request opened and when it entered its stage), then the current stage to now, red past its limit; the time in stage on the right.
3. **Pace** (the fleet repository's recorded history; hidden for a person focused away from fleet): a range choice (24 h, 7 days, 30 days); [totals](../controls/number.md) of open-to-installed P50 and P90 against the rolling baseline, pull requests merged (and per day), queue removals (and how many were flaky); two [area charts](../controls/area-chart.md): "Where the time goes" (P50 per stage per bucket, stacked: PR checks, queue, release, install) and "Merged" per bucket. A bucket (a day, or 2 hours in the 24 h range) filters the list to the pull requests merged in it, shown as a removable chip.

*Work area, a pull request open:* the title, stage, author and age; totals (open to installed or lead so far, queue runs, removals and flaky ones); the stages on one time axis, each a time strip with its duration (a stage without its own end ends where the next starts); the merge-queue runs with their conclusion and failed jobs; the hosts it is installed on. Other repositories, and fleet pull requests not merged yet, show what the current read knows.

| Inventory item | Shown by |
|---|---|
| Delivery stage of each change (review, merge, deployment) | The list, grouped by stage; the opened pull request's stages |
| Where delivery waits | Needs a look; red time in stage; the red part of a lane |
| Delivery pace and regressions | Pace totals against the baseline; Where the time goes; regressions in Needs a look |

**Live.** Delivery reaches the page through the change stream: each pass that changes a project's pull requests writes `delivery-state:<project>`, and each history refresh writes `delivery-history`, both change kind `delivery`; the page re-reads on that change, and every 5 minutes as a fallback. With a key the coordinator reads GitHub every minute (pull requests, active and the latest finished workflow runs, releases); without one, the fleet repository every 6 minutes. Installed: every host seen in the last hour runs that release or newer.

**Data.** `delivery_status`, and for Pace and the opened pull request `delivery_history`, `delivery_baseline` and `delivery_regressions` ([delivery health](../../designs/fleet-delivery-pipeline.md#delivery-health)). The view (stages, search, group, sort, range, picked bucket, opened pull request) is in the URL.

**Limits per stage.** PR checks 30 min, Waiting in queue 15 min, Queue checks 30 min, Releasing 20 min, Installing 20 min; Open has none (people work there); Checks failed always needs a look.

**Unknown is not zero.** When some GitHub reads failed, the stage counters are unknown (`?`), an alert names the failure, and the rows are the last ones read, said so.

**Open.** Workers waiting to land is no longer shown; Fleet keeps no record of the waiting worker itself.
