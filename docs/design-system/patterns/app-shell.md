# App shell

**Code.** `AppShell`.

**Job.** Gives every page the same frame, so the person always knows where they are and where the attention is.

**Composition.**
* *Top bar,* `size-topbar` high: product name and the "N need you" chip, which links to [needs-you](needs-you.md). Later it also holds global search and the account.
* *Left column:* the [navigation rail](../controls/nav-rail.md), with pinned sections first.
* *Main area:* page head (title, then page actions on the right with stale data or the split button), then panels.
* *[TooltipLayer](../owner-rules.md):* mounted once.

**Rules.**
* The main area has one scroll region (specq F3). Lists scroll inside their own virtual list.
* Routes are stable URLs (`#/tasks/9989` in mockups, real paths in the app; specq F7). Back restores the list position and selection.
* Pages size to their content; there is no `100vh`.
* Phone (below 760 px): the rail shrinks to icons (interim, [D10](../decisions.md)). List and detail stack ([list-detail](list-detail.md)).
* Every page renders `AppShell`. The traceability check in `npm run check:design` reports any page that does not.
