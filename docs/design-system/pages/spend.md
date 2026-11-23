# Spend (architect)

**App routes:** `/spend` in the operator app (`app/src/views/spend.tsx`). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Requirements: [spend view](../../designs/fleet-analytics.md#spend-view). There is no static mockup.

**Purpose.** Answer "where did the agents' money go, and what did it buy" for the owner; agents read the same figures through `spend_summary`.

**Layout.** [App shell](../patterns/app-shell.md) and one scrolling page, no control column (OR-24: no catalogue).
* **Period and total first.** Offer `24h`, `7d`, `30d`, and `90d`, show the UTC dates included in the selected period, and keep Refresh and project focus available. Show total spend (`CostFigures`), runs, failed runs, tokens, and any leaves outside access scope. Explain the estimate marker `≈`, including subscription list-price equivalents.
* **Unpriced usage.** Show the reported count when present. A usage amount or price entry is missing, so this usage contributes no dollar figure. Use the [measurement basis](../principles.md#2-measurement-basis) unavailable display where a dollar figure is missing.
* **Breakdown.** Use the shared grouping control for Purpose, Project, Category, Agent, Model, and Host. Lead the sortable `DataTable` with cost (`CostFigures`) and **Share of spend**, then show runs, failures, tokens, and unpriced usage. Share is the fraction of available USD spend in the selected period and scope. Use neutral percentages, including `100%` and a known `0%`. Show unavailable shares with a dash and distinguish small nonzero shares from zero. In Category, label the unclassified row **Unknown category**, explain that no category is recorded, and keep its available spend and share visible.
* **Spend by purpose:** a [share bar](../controls/share-bar.md) across task, lab, conversation, system, and other work. Label the basis when the bar shows run counts.
* **What it bought:** completed tasks and task spend, conversation runs and spend, plus per-project completed tasks, task spend, estimated cost per task ([number](../controls/number.md)), and unpriced count.

Monetary figures keep their currency and basis. At narrow widths, the shared table frames scroll sideways and the period, total, and breakdown controls stay within the work area.

| Inventory item | Shown by |
|---|---|
| Spend by purpose | Share bar |
| What it bought; cost per completed task | What it bought |
| Breakdown by purpose, project, category, agent, model, host | Breakdown table |

**Open.** Links from each figure to `analytics_query` rows on Projects.
