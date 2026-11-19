# Changes (architect)

**Route:** `/changes` in the operator app (not built). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Requirements: [changes screen](../../designs/fleet-system-management.md#changes-screen).

**Purpose.** Answer "is every host on the same setup, and what has not reached it": configuration changes, the release and the harness pins, from the repository to the active store and to each host.

**Layout.** [App shell](../patterns/app-shell.md) and one scrolling page, no control column (OR-24: no catalogue).
* **Head:** a [stat strip](../controls/stat-strip.md): changes waiting, hosts behind, definitions only in Fleet.
* **Where each change has reached:** a table. A row is one change: kind icon, title and a muted line with its cause (the pull request, or the files). Columns: repository, active, then one per host. A cell is a [state icon](../controls/state-badge.md) with a short value: done, waiting, not reached (with the value it has) or not needed. The last column holds the row's one action: take the repository version (primary), see the change, open the host.
* Below the table, one muted line says that agents and skills travel with each task, and names an offline host with its last heartbeat.
* **Made in Fleet, not in the repository:** one [key-value row](../controls/detail-section.md) for agents and one for pipelines, with the ones that ran this month named and "Save to the repository".

| Inventory item | Shown by |
|---|---|
| Where each change has reached | The table |
| Agents waiting for a repository version; take it for many at once | Definition rows and their action |
| Definitions made in Fleet and not in the repository | Made in Fleet section |

**Open.** Naming the pull request needs the release to record the commit per shipped definition file.
