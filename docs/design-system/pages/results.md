# Results (operator)

**Mockup routes:** `#/results` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** Find results by project, task, type and time, and open them in the right viewer.

**Layout.** [List and detail](../patterns/list-detail.md): a [virtual list](../controls/data-list.md) of results on the left (title with task ID, type, project, size, copy [state icon](../controls/state-badge.md), age) and the [result viewer shell](../controls/result-viewer.md) on the right.

| Inventory item | Shown by |
|---|---|
| Find results by project, task, type and time | List; the [list view](../patterns/list-view.md) [filter bar](../controls/filter-bar.md) applies (type, project, time) |
| Open in the right viewer, compare versions, annotate | Viewer shell: tabs View, Checks, Versions; Compare icon |
| Copy state per location | Copy state icon in the list; copy badges in the viewer head; unavailable explains the last copy and offers "Notify when online" |
| Share with an email | Share icon in the viewer head |

**Open.** The results list should reuse the full list view pattern (views, counters by copy state). The mockup uses a plain virtual list.
