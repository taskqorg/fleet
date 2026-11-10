# Laboratory (architect)

**Routes:** app `/lab` and `/lab/<experiment id>` (workspace `lab`); mockup `#/lab` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Built in Lab Stage 3 ([D18](../decisions.md), [fleet-lab.md](../../designs/fleet-lab.md#lab-screen)).

**Purpose.** Compare variants of a pipeline over examples, inspect trials, rate outputs, decide and promote.

**Layout of `/lab`.**
* "Waiting for you": open ratings count; drafted examples awaiting approval as [decision blocks](../controls/decision-block.md) (pass or fail).
* Experiments: [data list](../controls/data-list.md), state first, title = pipeline · kind · variants, short ID, age.
* Use-case registry: data list, drift state first ([state badge](../controls/state-badge.md) family `source`), goal with ID, fit, maturity.

**Layout of `/lab/<id>`.**
* Head: experiment state, title, ID, Launch / Resume / Stop.
* Spend: [totals](../controls/number.md) for trials, wall time, Pipe calls and credits, each with its basis and the budget limit and action.
* Matrix: rows are examples, columns variants. A cell shows a summary per evaluator (never combined across forms) and one line per repeat: trial state (reason when left out of comparison), select for rating, repeat, time, credits, scores, run link, cancel or retry. Confounders (host, harness and version, model, web route) on the repeat's tooltip.
* Variants: spread across repeats per evaluator; Promote shows the catalogue patch inline or the recommendation it set.
* Decision gate: judge-to-owner agreement; Lab priority is background (no effect until the scheduler).
* Ratings: decision blocks with outputs labelled A, B, … in the seeded order, answered from the keyboard; "rated k of n".

| Inventory item | Shown by |
|---|---|
| Experiments: variants, evaluators, examples | Experiments list; experiment head and matrix |
| Trials per variant with scores, time and spend, each with its basis | Matrix cells |
| Comparison of variants, with drill-down to runs | Matrix summaries, variant spread, run links |
| Assessments and recommendations | Ratings, decision gate, Promote |

**Open.** The variant configuration diff; experiment creation from the screen (today `POST /api/lab/experiments`).
