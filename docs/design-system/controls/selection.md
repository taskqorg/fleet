# Selection mode

**Code.** `SelectToggle`, `SelectBar` and `RowCheck` in `packages/ui`; `FilterBar` `selection`. Demo: [book](../book/index.html#group-sort).

**Purpose.** Act on several rows at once without checkboxes on every row all the time (OR-37).

**Anatomy.**
* `SelectToggle`: a square at the start of the list tools line, above the checkbox column it opens.
* In selection mode, a checkbox column appears left of the state icon (the state stays visible).
* `SelectBar` takes its own line under the tools: select all or none (in the checkbox column), the cross to leave, the count, and the actions on the right. Group and sort stay where they are; nothing is covered.

**Behaviour.**
* On: the square, Shift-click on a row, `x` on a focused row, or Select in the row menu. In the mode, a click on a row toggles it.
* Off: the cross, the square again, or Esc.
* Select all takes the loaded rows. Actions apply to the selected rows they fit (Pause skips paused tasks) and say why when none fit. Cancel asks first, inline.
* Checked rows are the view URL's `sel`, so a link reproduces them.

**Accessibility.** The bar is a `toolbar` named Selection; the count is a polite live region; the select-all box is indeterminate when some rows are checked.

**Used in.** Tasks; lists with bulk actions.
