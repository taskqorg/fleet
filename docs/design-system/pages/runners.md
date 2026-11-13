# Runners (architect)

**App routes:** `/runners` in the operator app (`app/src/views/runners.tsx`). The app check (`tests/ui/app.check.ts`) verifies it. There is no static mockup.

**Purpose.** See everything that executes work, per host, with the time and resources each run took, and what was spent today and this month.

**Layout.** [App shell](../patterns/app-shell.md) and one scrolling page, no control column (OR-24: no catalogue).
* **Spend:** a table of host and execution kind (agent runs, child agents, host jobs, tool calls) plus a `Totals` summary showing runs, wall time, CPU time, tokens, and separated multi-currency metered and subscription list-price `CostFigures` for today and the month (UTC). Subscription spend is shown with its subscription indicator alongside metered spend and is never summed with metered spend or across currencies. Unpriced runs without a rate card are `unavailable`, never `0`.
* **One panel per host** (hostless gateway calls under "coordinator"): the head names the host (opens it on Hosts) and its running count; each row is one run active now or ended in the last 24 hours: [state icon](../controls/state-badge.md), task title (or kind) and execution ID (with caller `parentExecutionId` link when nested), [harness mark](../controls/harness-counts.md), a problem chip, tokens, CPU time, peak memory and multi-currency/`subscription` `CostFigures` when recorded, a [time strip](../controls/time-strip.md) over the last 24 hours, and the duration, or the [age chip](../controls/number.md) while it runs (attention past 2h). The row opens its task, else its conversation.

**Data.** `executions_runners`: the newest 500 runs. Pipeline runs and steps are left out, because their agent runs are listed. Tool calls appear as one row per caller (per host: parent execution, else task, else connection) with the number of calls and failed calls. Totals come from `execution_totals`; agent tokens come from the harness's last usage report. It reloads every 15 s (execution changes arrive many times a second).

* **CI** (`executions_ci`: the last background pass over GitHub Actions of taskqorg/fleet, run every minute and once at start through `githubRead`): one row per runner type with jobs and wall minutes since 00:00 UTC (a lower bound while some of today's runs are not read yet). A self-hosted runner shows its online and busy state, or "state unknown" without a GitHub key. Below these rows are the jobs in progress and queued, each with a time strip and an age chip, and each opens on GitHub. A "public" chip marks keyless reads (refreshed every 5 minutes); a problem chip lists failed reads.

**Open.** CI minutes for the month need the billing API or a stored job history.
