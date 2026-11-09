# Harness marks and counts

**Code.** `HarnessMark`, `HarnessCounts`. Demo: [book](../book/index.html#lifecycle).

**Purpose.** Shows which harness runs a task, and how many workers of each harness run on a host ([D11](../decisions.md)).

**Anatomy.**
* *Mark:* the harness icon from the registry (Codex, Claude, Antigravity). The name is in the tooltip and the accessible name.
* *Counts:* mark plus a monospace number for each harness, in registry order.

**Behaviour.**
* A count of zero is omitted, because the missing mark says the same thing.
* An unknown count is not zero: when the host is unconfirmed, show the last known values next to a lower-bound total.
* In task rows the mark appears only while the task is in progress (OR-22).

**Used in.** Task list rows, stat strip (workers per host), Hosts table, Overview.
