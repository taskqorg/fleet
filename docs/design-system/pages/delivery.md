# Delivery (operator)

App only. Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** Answers "where does delivery wait right now, and since when?" for all connected projects together: pull requests from open to installed on hosts.

**Layout.** [App shell](../patterns/app-shell.md) without a control column (no catalogue, like Overview). The page head shows the age of the last GitHub read (red after 15 minutes) and a `public GitHub` chip while no key is on the `github` connection. Then:
* a [stat strip](../controls/stat-strip.md): pull requests per stage and the workers waiting to land; a stage whose oldest pull request is past its limit is red;
* one stacked [area chart](../controls/area-chart.md) "In flight, 24 hours": a layer per stage from Open to Installing, bottom up, on one scale, so a growing layer shows where delivery backs up; waiting stages in amber, CI in light grey, the release tail in green, the second stage of each pair striped;
* one line naming the first such stage ("Waiting starts at Queue wait");
* one card per stage in pipeline order (Open, PR CI, Queue wait, Queue CI, Releasing, Installing, Installed 24h; Merged 24h for repositories without releases): count and oldest age in the head, an [area chart](../controls/area-chart.md) of the count over 24 hours on one scale for all cards, and the six oldest pull requests (state icon, title with `#number`, release, age in stage; the row opens the pull request on GitHub).

| Inventory item | Shown by |
|---|---|
| Delivery stage of each change (review, merge, deployment) | Stage cards and their rows |
| Where delivery waits | Red stat and age chip, the "waiting starts" line, a rising area |

**Data.** `delivery_status` only. The coordinator reads GitHub every 6 minutes (pull requests, active workflow runs, releases) through the `github` connection, or unauthenticated for the fleet repository while the connection has no key, and records the per-stage counts at each read. Installed: every host seen in the last hour runs that release or newer (`hosts_list` versions).

**Limits per stage.** PR CI 30 min, Queue wait 15 min, Queue CI 30 min, Releasing 20 min, Installing 20 min; Open has none (people work there).

**Open.** A project focus control filters items and samples by `projectId`. Workers waiting to land is the count of pull requests with auto-merge on or in the merge queue: Fleet keeps no record of the waiting worker itself.
