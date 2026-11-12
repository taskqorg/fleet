# Runners (architect)

**Purpose.** See everything that executes work, per host, with the time and resources each run took, and what was spent today and this month.

**Layout.** [App shell](../patterns/app-shell.md) and one scrolling page, no control column (OR-24: no catalogue).
* **Spend:** a table of host and execution kind (agent runs, child agents, host jobs, tool calls) with runs, wall time, tokens and USD for today and the month (UTC). USD is an estimate from the price table; without a price entry it is unavailable, never 0.
* **One panel per host** (hostless gateway calls under "coordinator"): the head names the host (opens it on Hosts) and its running count; each row is one run active now or ended in the last 24 hours: [state icon](../controls/state-badge.md), task title (or kind) and execution ID, [harness mark](../controls/harness-counts.md), a problem chip, tokens, CPU time, peak memory and USD when recorded, a [time strip](../controls/time-strip.md) over the last 24 hours, and the duration, or the [age chip](../controls/number.md) while it runs (attention past 2h). The row opens its task, else its conversation.

**Data.** `executions_runners` (newest 500 runs; pipeline runs and steps left out, their agent and tool runs are listed; totals from `execution_totals`). It reloads on execution changes and every 15 s.

* **CI** (`executions_ci`, GitHub Actions of taskqorg/fleet read on the coordinator through `githubRead`): one row per runner type with jobs and wall minutes since 00:00 UTC (a lower bound while some of today's runs are not read yet). A self-hosted runner shows its online and busy state, or "state unknown" without a GitHub key. Below these rows are the jobs in progress and queued, each with a time strip and an age chip, and each opens on GitHub. A "public" chip marks keyless reads (refreshed every 5 minutes); a problem chip lists failed reads.

**Open.** CI minutes for the month need the billing API or a stored job history.
