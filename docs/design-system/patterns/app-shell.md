# App shell

**Code.** `AppShell`.

**Job.** Gives every page the same three columns (OR-24), so the person always knows where they are and where the attention is.

**Composition, left to right.**
* *Navigation:* the [navigation rail](../controls/nav-rail.md). Its head holds the product name, the "N need you" chip, which links to [needs-you](needs-you.md), and the collapse button. Pinned sections come first.
* *Control column,* `size-ctl` wide: the catalogue of the current section. See [list and detail](list-detail.md).
* *Work area:* the opened item, or the whole page for a section with no catalogue (Overview).
* *[TooltipLayer](../owner-rules.md):* mounted once.

There is no top bar. Global search and the account, when built, go into the navigation.

**Rules.**
* A "Skip to content" link comes first. The "N need you" chip is hidden on the Overview, where the needs-you panel is already shown (OR-17, D14).
* The shell fills the viewport and does not scroll. Each column has at most one vertical scroller, never nested (OR-25): the rail, the catalogue's virtual list, and the work area (or the transcript inside it).
* The shell is the only rule that uses the viewport height. Nothing else uses `100vh` or `100dvh`.
* The navigation collapses to icons by its button; the choice is remembered on the device. Below 760 px it is always collapsed. It never disappears.
* A click anywhere on a list row opens it (OR-26). The shell handles this once for every list that has a row link.
* Routes are stable URLs (`#/tasks/9989` in mockups, real paths in the app; specq F7). Back restores the list position and selection.
* Every page renders `AppShell`. The traceability check in `pnpm run check:design` reports any page that does not.
