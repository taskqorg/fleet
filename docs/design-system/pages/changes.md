# Changes (architect)

**Target home.** [Library](library.md) › Changes for definitions (`/library/changes`), and the drift line of each host in [System](system.md) › Hosts for release and harness pins. `/changes` redirects to Library.

**App routes:** `/changes` in the operator app (`app/src/views/changes.tsx`). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Requirements: [changes screen](../../designs/fleet-system-management.md#changes-screen). Canvas: [screens/host-updates](../board/screens/host-updates.bl), frame 1A (the component groups); the definitions: board `drafts/definitions-home`.

**Purpose.** Answer "is every host on the same setup, and what has not reached it": configuration changes, the release and the harness pins, from the repository to the active store and to each host.

**Layout.** [App shell](../patterns/app-shell.md) and one scrolling page, no control column (OR-24: no catalogue).
* **Head:** one summary line: the definitions on their way to the repository (quiet), the rows that need a person (attention), the Lab experiments (quiet), then the hosts: installing (running), behind (attention), revert in flight (attention), qualification failed (danger), or "hosts aligned"; and Refresh.
* **Definitions** (board `drafts/definitions-home`, approved 2026-11-26): one [change row](../controls/change-row.md) per definition that is not the repository's, from `system_changes`. A Fleet save sends itself to the repository ([one home per kind](../../designs/fleet-definition-editors.md#one-home-per-kind)), so a row needs a person only by exception:
  * *on its way*: the chip is the publish task's pull request and its stage (`PR #1102 · CI running`, `PR #1095 · merged · waits for release`), or the task while it has none (`T-FLT-67 · worker on it`); no buttons; it leaves when a release ships it.
  * *conflict* (repository-changed, attention): the repository changed the file under a Fleet edit. Compare (opens the definition), Keep mine (`definition_publish` with `keep`), Take repository (`definition_take_repository`).
  * *failed* (danger): the pull request failed its checks or the merge queue, or its task ended without one. Open task.
  * *made in Fleet* (store-only, not a Lab variant): `New in Fleet`, Publish (`definition_publish`); an ahead head from before publishing existed: `Edited in Fleet`, Publish.
  Rows on their way come first, then those that need a person, then those made in Fleet, newest first.
* **Lab variants:** one change row per live experiment whose variants are store-only definitions: its goal, its state, the variants by id, its age. A click opens the experiment; the variants leave when it is decided.
* **Release:** one [live row](../controls/live-row.md): `Release <version>` (the version hosts are offered), then where it stands: the hosts that refused it with the reason (danger), the hosts installing with the reason (running), the hosts behind (attention), or "every host runs it"; time on the right. A click opens the first host it waits on.
* **Agent CLIs, Host tools** (after the Release row): one group per kind of release-managed component (`system_changes` `components`). Above the table, the kind's open proposal as a [live row](../controls/live-row.md): its state icon, `#<number>` and the versions it proposes, the stage (offline checks, on hosts, merging, landed, reverting, failed) with the Lab trials (`5 of 8 trials · 1 failed`), its age on the right; a click opens the pull request. A `DataTable` row per component: Pinned, Proposal (the version and its trial result, the failure in danger) and one cell per host: a check on the pin, `behind <version>` (attention), `installing <version>` (running; a pending update to the coordinator's own release), `refused <reason>` and `N of M runs failed` (danger), `own <version>` when the kind's pins are off (the version the host reports: its CLIs, and its host tools on the executor's PATH). A host cell opens that host's Updates. A kind whose pins are off is dimmed with "pins off · hosts keep their own". The table stacks at narrow widths (OR-66).

| Inventory item | Shown by |
|---|---|
| Where each change has reached | Definitions rows (pull request stage), Release row, component groups |
| What a pin proposal is doing and which host fails it | Proposal row and the Proposal and host cells of the component groups |
| A conflict between a Fleet edit and the repository | Conflict row: Compare, Keep mine, Take repository |
| Definitions made in Fleet and not in the repository | New in Fleet rows with Publish; Lab variants by experiment |
