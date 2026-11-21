# Changes (architect)

**App routes:** `/changes` in the operator app (`app/src/views/changes.tsx`). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Requirements: [changes screen](../../designs/fleet-system-management.md#changes-screen). There is no static mockup.

**Purpose.** Answer "is every host on the same setup, and what has not reached it": configuration changes, the release and the harness pins, from the repository to the active store and to each host.

**Layout.** [App shell](../patterns/app-shell.md) and one scrolling page, no control column (OR-24: no catalogue).
* **Head:** drift summary (repository changed, store-only, ahead, hosts behind, harness pins drifting) and Refresh.
* **Shipped definitions changed in repository:** a table (`DataTable`) of definitions whose repository version differs from the active store, with store revision, 30-day runs, Open, and Preview/Apply take repository version (`definition_take_repository`) via [problem row](../controls/problem-row.md).
* **Release versions:** [key-value rows](../controls/detail-section.md) for Coordinator, Web UI, Gateway, and each host with its [state icon](../controls/state-badge.md).
* **Harness CLI pins vs hosts:** a table of pinned vs observed harness versions per host.
* **Made in Fleet, not in repository:** a table of store-only and ahead definitions with 30-day runs and repository path, plus Export patch (`definition_export`) shown as a diff.

| Inventory item | Shown by |
|---|---|
| Where each change has reached | Shipped definitions table, Release versions, Harness CLI pins table |
| Agents waiting for a repository version; take it for many at once | Preview take all / Take repo actions |
| Definitions made in Fleet and not in the repository | Made in Fleet section with Export patch |

**Open.** Naming the pull request needs the release to record the commit per shipped definition file.
