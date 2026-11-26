# Tab bar

**Code.** `TabBar` in `packages/ui` (`@ui-sdk/solid` `Tabs`); tabs that do not fit fold into the More menu, measured with a `ResizeObserver`, and the current tab stays in the bar. Demo: [book](../book/index.html#detail-head). Board: [blocks.bl](../board/blocks.bl), masters `Tab bar` and `Tab`, board *Sections*.

**Purpose.** Switches between facets of one opened entity inside the work area, for example a project's facets ([OR-46](../patterns/navigation-and-columns.md#rules-kept-here) names them; [OR-49](../owner-rules.md)). The board draws a project's five as an example.

**Anatomy.** One line under the [detail head](../patterns/list-detail.md#board) with a `line-soft` rule under it. Each tab is its name in `font-size-md`; the current tab is in `fg` with a 2 px `accent` underline, the rest in `fg-muted`. A tab may carry a count in mono when its facet has items that need a look ([OR-18](../owner-rules.md)); a zero count is hidden ([OR-29](state-counters.md#rules-kept-here)).

**States.** Tab: `default`, `current`, hover (`hover` fill), focus ring.

**Behaviour.**
* A tab changes only the work area; the control column stays as it is ([OR-47](../patterns/navigation-and-columns.md#rules-kept-here)).
* Each tab has its own URL ([OR-47](../patterns/navigation-and-columns.md#rules-kept-here), [OR-57](../owner-rules.md)); browser Back returns to the previous tab.
* At phone width the tabs that do not fit fold into a last More tab; the bar never wraps and never scrolls sideways ([OR-25](../patterns/app-shell.md#rules-kept-here)).

**Keyboard.** Arrow keys move between tabs, `Home` and `End` jump to the ends, `Enter` or `Space` opens.

**Accessibility.** `role="tablist"` from `TabBar` only, never hand-made ([OR-49](../owner-rules.md)); each tab is `role="tab"` with `aria-selected`.

**Do.** Use it for facets of one entity. **Don't.** Use it as section navigation or above the columns ([OR-46](../patterns/navigation-and-columns.md#rules-kept-here)).

**Used in.** [Projects](../pages/projects.md) and other opened entities with facets.
