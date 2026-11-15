# Row pin

**Code.** `RowPin` in `packages/ui`, with the row class `pin-row`. Demo: [book](../book/index.html#data-list).

**Purpose.** Show that a row is pinned (OR-12) and unpin it in one click.

**Anatomy.** A pin icon button before the state icon, only on pinned rows. The row class `pin-row` adds that one leading column, so other rows do not move. The two always go together.

**Behaviour.** A click unpins; the row leaves the Pinned group and focus follows it. In selection mode the pin toggles the row's checkbox like the rest of the row. On touch devices the column is 44 px wide.

**Accessibility.** Named "Unpin <title>" with the tooltip Unpin.

**Used in.** Tasks.
