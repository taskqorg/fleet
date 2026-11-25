# Spend (architect)

**Target home.** [System](system.md) › Spend (`/system/spend`), the one screen where cost is the question ([OR-54](../owner-rules.md)). `/spend` redirects.

**App routes:** `/spend` in the operator app (`app/src/views/spend.tsx`). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Requirements: [spend view](../../designs/fleet-analytics.md#spend-view). There is no static mockup.

**Purpose.** Answer "where did the agents' money go, and what did it buy" for the owner; agents read the same figures through `spend_summary`.

**Layout.** [App shell](../patterns/app-shell.md) and one scrolling page, no control column (OR-24: no catalogue).
* **Period and total first.** The **Period** control offers **UTC day**, **7d**, **30d**, and **90d**, with **30d** selected by default. **UTC day** selects the current UTC calendar date. Show the included UTC dates and keep Refresh and project focus available. When results load, show **Total spend**, **Runs**, **Failed**, and **Tokens**; Total, Runs and Unpriced usage carry a sparkline of one bar per UTC day of the period (`spend_summary` with `by: day`). Explain `≈`, including subscription list-price equivalents. Show **Outside access scope** when the hidden count is positive.
* **Unpriced usage.** When the count is positive, show it and explain that a missing usage amount or price entry excludes that usage from dollar figures. For unavailable cost, share, and cost per task cells, use the [measurement basis](../principles.md#2-measurement-basis) dash, accessible name and tooltip. Include the reason when known. The server’s unavailable cost figure does not identify a billing basis. Show its reason for cost and describe the missing USD amount for each affected share.
* **Breakdown.** The shared **Group by** control offers Purpose, Project, Category, Agent, Model, and Host. The sortable `DataTable` leads with **Cost**, **Billed USD share**, and **Estimated subscription value share**, followed by **Runs**, **Failed**, **Tokens**, and **Unpriced**. Use the row's known amount divided by the corresponding known USD total for the selected period and scope. The billed basis uses recorded metered figures, including tariff estimates. The subscription basis uses USD subscription list-price equivalents. Unpriced usage contributes to neither total. Show an unavailable share with the reason when its row amount is missing, its basis has no positive total, its amount is negative, or its amount exceeds the total. Keep the recorded amount visible. Allow floating-point summation error at the upper boundary when it rounds to 100%. Use neutral percentages, including `100%` and a known `0%`, and distinguish small nonzero shares from zero. In Category, show **Unknown category** and explain it when the unclassified row is present.
* **Negative adjustments.** When a share basis has a positive total and a negative amount in the breakdown or purpose results, explain that refunds or other negative adjustments reduce that basis total. Positive shares can then add up to more than 100%.
* **By purpose.** When purpose results are present, show the known amounts and shares separately for **Billed USD** and **Estimated subscription value**, using the same totals as the breakdown. Use a [share bar](../controls/share-bar.md) when the nonnegative parts add up to the basis total. Otherwise list the amounts and shares in a table and explain why there is no bar. When purpose amounts are available and the basis has no positive total, keep the amounts in the table with unavailable shares. When no purpose amounts are known, show an unavailable message. Omit purpose amounts that have no figure for the basis.
* **What it bought:** completed tasks and task spend, conversation runs and spend, plus per-project completed tasks, task spend, estimated cost per task ([number](../controls/number.md)), and unpriced count.

Monetary figures keep their currency and basis. At narrow widths, the shared table frames scroll sideways and the period, total, and breakdown controls stay within the work area.

| Inventory item | Shown by |
|---|---|
| Spend by purpose | Share bar or table |
| What it bought; cost per completed task | What it bought |
| Breakdown by purpose, project, category, agent, model, host | Breakdown table |

**Open.** Links from each figure to `analytics_query` rows on Projects.
