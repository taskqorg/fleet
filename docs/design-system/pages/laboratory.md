# Laboratory (architect)

**Mockup routes:** `#/laboratory` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** Compare variants, inspect trials, assess and recommend.

**Layout.**
* Page head: the experiment name and size, and a [split button](../controls/split-button.md) "Repeat trials" with "Add variant" and "Run on new task set".
* Tabs: Compare, Trials, Assessments.
* The recommendation as a [decision block](../controls/decision-block.md) addressed to the architect.

| Inventory item | Shown by |
|---|---|
| Experiments: variants, configuration differences, evaluators, task set | Page head; a configuration diff per variant is still to be added |
| Trials per variant with scores, tokens, time and cost, each with its basis | Trials tab: the [list view](../patterns/list-view.md) |
| Comparison of variants, with drill-down to runs | Compare table: score, judge, tokens, time, cost with basis, failed/trials chip (red past a threshold). A row opens its trials |
| Assessments and recommendations | Assessments tab (empty state); recommendation decision block |

**Open.** The variant configuration diff, and how drill-down from a cell filters the trials.
