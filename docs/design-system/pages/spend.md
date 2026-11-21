# Spend (architect)

**App routes:** `/spend` in the operator app (`app/src/views/spend.tsx`). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Requirements: [spend view](../../designs/fleet-analytics.md#spend-view). There is no static mockup.

**Purpose.** Answer "where did the agents' money go, and what did it buy" for the owner; agents read the same figures through `spend_summary`.

**Layout.** [App shell](../patterns/app-shell.md) and one scrolling page, no control column (OR-24: no catalogue).
* **Head:** window (`24h`, `7d`, `30d`, `90d`), breakdown (`purpose`, `project`, `category`, `profile`, `model`, `host`), optional project filter, and Refresh.
* **Window total:** total spend (`CostFigures`), runs, failed runs, tokens, unpriced leaves, and hidden outside-access-scope leaves when `> 0`.
* **Spend by purpose:** a [share bar](../controls/share-bar.md) across task, lab, conversation, system, and other work.
* **Breakdown:** sortable table (`DataTable`) of runs, failed, tokens, cost (`CostFigures`), share, and unpriced runs for the chosen breakdown dimension.
* **What it bought:** completed tasks and task spend, conversation runs and spend, plus per-project completed tasks, task spend, estimated cost per task ([number](../controls/number.md)), and unpriced count.

Subscription list-price figures and metered money never sum; unpriced is never 0 ([measurement basis](../principles.md#2-measurement-basis)).

| Inventory item | Shown by |
|---|---|
| Spend by purpose | Share bar |
| What it bought; cost per completed task | What it bought |
| Breakdown by purpose, project, category, agent, model, host | Breakdown table |

**Open.** Links from each figure to `analytics_query` rows on Projects.
