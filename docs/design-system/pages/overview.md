# Overview (operator)

**Purpose.** Answers "what needs me and is the fleet healthy?" in the first screen.

**Layout.** [App shell](../patterns/app-shell.md). The page head shows "updated 20s". Then:
* the [ops summary](../patterns/ops-summary.md) stat strip;
* two columns: [needs you](../patterns/needs-you.md) on the left (wider), and workers per host, accounts and projects on the right.

| Inventory item | Shown by |
|---|---|
| Tasks across projects by state, priority and progress | Projects panel: state counters per project; click opens Tasks filtered |
| Everything waiting for a person | Needs-you panel: decision blocks and problem rows |
| Load and capacity by host and account, with placement reasons | Workers per host (running/slots, queued); placement reasons in the host detail (Hosts page) |
| Account and quota state per subscription | Accounts panel: fill bar in percent, reset time in the tooltip, unavailable when unknown |
| Change priority; pause or continue work | Actions in the task detail; priority is an open item for the Tasks list (sort by priority) |

**Open.** The priority control (a column or a sort) is not yet designed. The inventory needs it.
