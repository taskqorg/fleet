# Row pin

**Code.** `RowPin` in `packages/ui`, with the row class `pin-row`. Demo: [book](../book/index.html#data-list).

**Purpose.** Show that a row is pinned (OR-12) and unpin it in one click.

**Anatomy.** A pin icon button before the state icon, only on pinned rows. The row class `pin-row` adds that one leading column, so other rows do not move. The two always go together.

**Behaviour.** A click unpins; the row leaves the Pinned group and focus follows it. In selection mode the pin toggles the row's checkbox like the rest of the row. On touch devices the column is 44 px wide.

**Accessibility.** Named "Unpin <title>" with the tooltip Unpin.

**Used in.** Tasks.

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **OR-12 Pin anywhere.** Every catalogue, list and menu lets the person pin items. Pinned items show first in that same place. There is no separate Favourites section, and the pin is an icon.
