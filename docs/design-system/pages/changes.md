# Changes (architect)

**Target home.** [Library](library.md) › Changes for definitions (`/library/changes`), and the drift line of each host in [System](system.md) › Hosts for release and harness pins. `/changes` redirects to Library.

**App routes:** `/changes` in the operator app (`app/src/views/changes.tsx`). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Requirements: [changes screen](../../designs/fleet-system-management.md#changes-screen). Canvas: [screens/host-updates](../board/screens/host-updates.bl), frame 1A (the component groups).

**Purpose.** Answer "is every host on the same setup, and what has not reached it": configuration changes, the release and the harness pins, from the repository to the active store and to each host.

**Layout.** [App shell](../patterns/app-shell.md) and one scrolling page, no control column (OR-24: no catalogue).
* **Head (frame 1A):** one summary line, each part in its tone: hosts installing (running), hosts behind, revert in flight (attention), qualification failed (danger), then quiet: definitions that wait for the repository, made in Fleet; and Refresh.
* **Shipped definitions changed in repository:** a table (`DataTable`) of definitions whose repository version differs from the active store, with store revision, 30-day runs, Open, and Preview/Apply take repository version (`definition_take_repository`) via [problem row](../controls/problem-row.md).
* **Release:** one [live row](../controls/live-row.md): `Release <version>` (the version hosts are offered), then where it stands: the hosts that refused it with the reason (danger), the hosts installing with the reason (running), the hosts behind (attention), or "every host runs it"; time on the right. A click opens the first host it waits on.
* **Agent CLIs, Host tools** (after the Release row; the definitions sections follow them): one group per kind of release-managed component (`system_changes` `components`). Above the table, the kind's open proposal as a [live row](../controls/live-row.md): its state icon, `#<number>` and the versions it proposes, the stage (offline checks, on hosts, merging, landed, reverting, failed) with the Lab trials (`5 of 8 trials · 1 failed`), its age on the right; a click opens the pull request. A `DataTable` row per component: Pinned, Proposal (the version and its trial result, the failure in danger) and one cell per host: a check on the pin, `behind <version>` (attention), `installing <version>` (running; a pending update to the coordinator's own release), `refused <reason>` and `N of M runs failed` (danger), `own <version>` when the kind's pins are off (the version the host reports: its CLIs, and its host tools on the executor's PATH). A host cell opens that host's Updates. A kind whose pins are off is dimmed with "pins off · hosts keep their own". The table stacks at narrow widths (OR-66).
* **Made in Fleet, not in repository:** a table of store-only and ahead definitions with 30-day runs and repository path, plus Export patch (`definition_export`) shown as a diff.

| Inventory item | Shown by |
|---|---|
| Where each change has reached | Shipped definitions table, Release row, component groups |
| What a pin proposal is doing and which host fails it | Proposal row and the Proposal and host cells of the component groups |
| Agents waiting for a repository version; take it for many at once | Preview take all / Take repo actions |
| Definitions made in Fleet and not in the repository | Made in Fleet section with Export patch |

**Open.** Naming the pull request needs the release to record the commit per shipped definition file.
