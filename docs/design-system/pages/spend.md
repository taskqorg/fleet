# Spend (architect)

**App routes:** `/spend` in the operator app (`app/src/views/spend.tsx`). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Requirements: [spend view](../../designs/fleet-analytics.md#spend-view). There is no static mockup.

**Purpose.** Answer "where did the agents' money go, and what did it buy" for the owner; agents read the same figures through `spend_summary`.

**Layout.** [App shell](../patterns/app-shell.md) and one scrolling page, no control column (OR-24: no catalogue).
* **Period and total first.** The **Period** control offers **UTC day**, **7d**, **30d**, and **90d**, with **30d** selected by default. **UTC day** selects the current UTC calendar date. Show the included UTC dates and keep Refresh and project focus available. When results load, show **Total spend**, **Runs**, **Failed**, and **Tokens**. Explain `≈`, including subscription list-price equivalents. Show **Outside access scope** when the hidden count is positive.
* **Unpriced usage.** When the count is positive, show it and explain that a missing usage amount or price entry excludes that usage from dollar figures. For unavailable cost, share, and cost per task cells, use the [measurement basis](../principles.md#2-measurement-basis) dash, accessible name and tooltip. Include the reason when known.
* **Breakdown.** The shared **Group by** control offers Purpose, Project, Category, Agent, Model, and Host. The sortable `DataTable` leads with **Cost**, **Billed USD share**, and **Estimated subscription value share**, followed by **Runs**, **Failed**, **Tokens**, and **Unpriced**. Use the row's known amount divided by the corresponding known USD total for the selected period and scope. The billed basis uses recorded metered figures, including tariff estimates. The subscription basis uses USD subscription list-price equivalents. Unpriced usage contributes to neither total. Show a share as unavailable when its row amount is missing or its basis has no positive total. Use neutral percentages, including `100%` and a known `0%`, and distinguish small nonzero shares from zero. In Category, show **Unknown category** and explain it when the unclassified row is present.
* **By purpose.** When purpose results are present, show a separate [share bar](../controls/share-bar.md) for **Billed USD** and **Estimated subscription value**. Each shows known amounts and rounded shares of its own basis. Show an unavailable message in place of a bar when that basis has no positive total. Omit purpose amounts that have no figure for the basis.
* **What it bought:** completed tasks and task spend, conversation runs and spend, plus per-project completed tasks, task spend, estimated cost per task ([number](../controls/number.md)), and unpriced count.

Monetary figures keep their currency and basis. At narrow widths, the shared table frames scroll sideways and the period, total, and breakdown controls stay within the work area.

| Inventory item | Shown by |
|---|---|
| Spend by purpose | Share bar |
| What it bought; cost per completed task | What it bought |
| Breakdown by purpose, project, category, agent, model, host | Breakdown table |

**Open.** Links from each figure to `analytics_query` rows on Projects.
