# App shell

**Code.** `AppShell`.

**Job.** Gives every page the same three columns (OR-24), so the person always knows where they are and where the attention is.

**Composition, left to right.**
* *Navigation:* the [navigation rail](../controls/nav-rail.md). Its head holds the product name and the "N need you" chip, which links to [needs-you](needs-you.md). Pinned sections come first; the current user block ends it.
* *Control column,* `size-ctl` wide: the catalogue of the current section. See [list and detail](list-detail.md).
* *Work area:* the opened item, or the whole page for a section with no catalogue (Overview).
* *[TooltipLayer](../owner-rules.md):* mounted once.

There is no top bar. Global search goes into the navigation.

**Collapse toggle (OR-42).** The shell places it once, at the top left of the column right after the navigation: the control column, or the work area on a page without one (Overview). The first heading or filter bar there leaves room for it.

**Dividers (OR-41).** The navigation and the control column each have a drag divider on their right edge (`role=separator`, Left and Right arrows by 16 px, double-click resets). Widths are the `--size-rail` and `--size-ctl` variables on the shell. Limits: navigation 160 to 400 px, control column 280 to 720 px. Until the first settings read answers (at most 1.5 s) the shell is laid out but hidden, so a stored width or a collapsed rail never jumps in (OR-23). The divider's hit zone is 8 px with a pointer and 44 px on touch, over the column it resizes; the line stays 4 px. Widths are person settings (`widths.nav`, `widths.control`; `0` resets), clamped to these limits when read; a drag saves once, at its end, and an arrow key on release; the upper limit also follows the window (a quarter for the navigation, half the split for the list).

**Rules.**
* A "Skip to content" link comes first. The "N need you" chip is hidden on the Overview, where the needs-you panel is already shown (OR-17, D14).
* The shell fills the viewport and does not scroll. Each column has at most one vertical scroller, never nested (OR-25): the rail, the catalogue's virtual list (a short hand-built catalogue scrolls with its column instead), and the work area (or the transcript inside it).
* The shell is the only rule that uses the viewport height. Nothing else uses `100vh` or `100dvh`.
* The navigation collapses to icons by its button; the choice is the person setting `navCollapsed`, so it follows the person to every device. Below 760 px it is always collapsed. It never disappears.
* A click anywhere on a list row opens it (OR-26). The shell handles this once for every row that has a row link, in virtual lists and in key-value lists.
* Routes are stable URLs (`#/tasks/9989` in mockups, real paths in the app; specq F7). Back restores the list position and selection.
* Every page renders `AppShell`. The traceability check in `pnpm run check:design` reports any page that does not.
