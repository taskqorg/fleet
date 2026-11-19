# Spend (architect)

**Route:** `/spend` in the operator app (not built). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Requirements: [spend view](../../designs/fleet-analytics.md#spend-view).

**Purpose.** Answer "where did the agents' money go, and what did it buy" for the owner; agents read the same figures through `spend_summary`.

**Layout.** [App shell](../patterns/app-shell.md) and one scrolling page, no control column (OR-24: no catalogue).
* **Totals:** agent runs, list price, today, unpriced runs, each [number](../controls/number.md) with its basis.
* **What the money went to:** a [share bar](../controls/share-bar.md) by purpose (project work, Lab, conversations, research and reviews) with the agents of each part on its tooltip and a note on what is left out unpriced.
* **What it bought:** per project, tasks done, agent spend and cost per completed task; a muted line names the spend that buys answers, not tasks (conversations, Lab).
* **By model:** harness mark with model, runs, cost, unpriced runs.
* **By host:** runs, wall time and cost.

Subscription list-price figures and metered money never sum; unpriced is never 0 ([measurement basis](../principles.md#2-measurement-basis)).

| Inventory item | Shown by |
|---|---|
| Spend by purpose | Share bar |
| What it bought; cost per completed task | What it bought |
| By model and host | The two sections |

**Open.** The window choice (30 days now) and links from each figure to `analytics_query` rows on Projects.
