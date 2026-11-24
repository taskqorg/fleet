# Tab bar

**Code.** `TabBar` in `packages/ui` (`@ui-sdk/solid` `Tabs`). Board: [blocks.bl](../board/blocks.bl), masters `Tab bar` and `Tab`, board *Sections*.

**Purpose.** Switches between facets of one opened entity inside the work area, for example a project's Sessions, Tasks, Delivery, Results, Rules & methods ([OR-46](../owner-rules.md), [OR-49](../owner-rules.md)).

**Anatomy.** One line under the [detail head](../patterns/list-detail.md#board) with a `line-soft` rule under it. Each tab is its name in `font-size-md`; the current tab is in `fg` with a 2 px `accent` underline, the rest in `fg-muted`. A tab may carry a count in mono when its facet has items that need a look ([OR-18](../owner-rules.md)); a zero count is hidden ([OR-29](../owner-rules.md)).

**States.** Tab: `default`, `current`, hover (`hover` fill), focus ring.

**Behaviour.**
* A tab changes only the work area; the control column stays as it is ([OR-47](../owner-rules.md)).
* Each tab has its own URL ([OR-47](../owner-rules.md), [OR-57](../owner-rules.md)); browser Back returns to the previous tab.
* At phone width the bar scrolls sideways inside its own frame ([OR-25](../owner-rules.md)); it never wraps.

**Keyboard.** Arrow keys move between tabs, `Home` and `End` jump to the ends, `Enter` or `Space` opens.

**Accessibility.** `role="tablist"` from `TabBar` only, never hand-made ([OR-49](../owner-rules.md)); each tab is `role="tab"` with `aria-selected`.

**Do.** Use it for facets of one entity. **Don't.** Use it as section navigation or above the columns ([OR-46](../owner-rules.md)).

**Used in.** [Projects](../pages/projects.md) and other opened entities with facets.
