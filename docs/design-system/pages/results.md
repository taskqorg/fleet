# Results (operator)

**Mockup routes:** `#/results` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** Find results by project, task, type and time, and open them in the right viewer.

**Layout.** [List and detail](../patterns/list-detail.md): a [virtual list](../controls/data-list.md) of results on the left: copy [state icon](../controls/state-badge.md) first (OR-22), then name with task ID, project, size and age. The type is read from the file name, so there is no Type column (OR-17) and the [result viewer shell](../controls/result-viewer.md) on the right.

| Inventory item | Shown by |
|---|---|
| Find results by project, task, type and time | List; the [list view](../patterns/list-view.md) [filter bar](../controls/filter-bar.md) applies (type, project, time) |
| Open in the right viewer, compare versions, annotate | Viewer shell: tabs View, Checks, Versions; Compare icon |
| Copy state per location | Copy state icon in the list; copy badges in the viewer head; unavailable explains the last copy and offers "Notify when online" |
| Share with an email | Share icon in the viewer head |

**Stub.** The viewer body in the mockup is a placeholder; real viewers are specified in [result viewer](../controls/result-viewer.md).

**Open.** None.
