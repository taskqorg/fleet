# Overview (operator)

**Mockup routes:** `#/overview` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** Answers "what needs me and is the fleet healthy?" in the first screen.

**Layout.** [App shell](../patterns/app-shell.md). The page head shows "updated 20s". Then:
* the [ops summary](../patterns/ops-summary.md) [stat strip](../controls/stat-strip.md);
* two columns: [needs you](../patterns/needs-you.md) on the left (wider), and workers per host, accounts and projects on the right.

Counters follow OR-29: the stat strip, a host row and a project row show only non-zero values, a project row starts with its bold total, and a block with nothing above zero is hidden. A host row shows its running workers only while the host is live; an offline host shows its state badge and last-seen age, never a stale count (it used to read `≥ 0`).

| Inventory item | Shown by |
|---|---|
| Tasks across projects by state, priority and progress | Projects panel: [state counters](../controls/state-counters.md) per project; click opens Tasks filtered |
| Everything waiting for a person | Needs-you panel: [decision blocks](../controls/decision-block.md) and [problem rows](../controls/problem-row.md) |
| Load and capacity by host and account, with placement reasons | Workers per host (running/slots, [harness counts](../controls/harness-counts.md), queued); placement reasons in the host detail (Hosts page) |
| Account and quota state per subscription | Accounts panel: [fill bar](../controls/fill-bar.md) in percent, reset time in the tooltip, unavailable when unknown |
| Change priority; pause or continue work | Actions in the task detail; priority is an open item for the Tasks list (sort by priority) |

**Open.** Changing priority from the Overview: priority is a task field shown by grouping (D11); the edit control is not yet designed.
