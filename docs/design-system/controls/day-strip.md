# Day strip

**Code.** `DayStrip` in `packages/ui` (`src/charts.tsx`). Demo: the Delivery mockup, variant C (`pages/mockups.html#/delivery/c`).

**Purpose.** Shows several figures per calendar day as a compact table: one column per day, one row per figure, the exact value with a small bar under it.

**Anatomy.** A `<table>`: day buttons in the head (`day` from `format.ts`), the figure names in the first column, values right-aligned in `font-mono`, and a 3 px bar under each value scaled to its row's `capOf` (a value above it fills the cell and shows a break). A rule (`sep`) starts a new kind of figure; `strong` marks the main row.

**States.** Measured; part of the day (value muted, striped bar); not measured (`—` on a hatched cell, with the reason in the tooltip); above the row's alert level (a baseline P90): the value in the attention tone and bold, and the tooltip says so (OR-18); selected day: the column in `selected`.

**Behaviour.** A click on a day or any of its cells picks the day; a second click clears it.

**Accessibility.** A real table with row and column headers; each cell's name reads the day, the figure, its value and its state.

**Used in.** [Delivery](../pages/delivery.md) Pace (variant C, under review).
