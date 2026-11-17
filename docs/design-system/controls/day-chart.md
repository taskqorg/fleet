# Day chart

**Code.** `DayChart` and `capOf` in `packages/ui` (`src/charts.tsx`). Demo: the Delivery mockup variants (`pages/mockups.html#/delivery/a`, `/b`).

**Purpose.** Shows one figure per calendar day (or per shorter bucket) on a column grid that other day charts share, so charts stacked under each other line up day by day. Used for pace: open to installed P50 and P90 per day, pull requests merged per day.

**Anatomy.** A head line (title, then a key for the textures in use); a left gutter with the scale top in `font-mono` at the top gridline, never over the plot; one column per day; under the plot optional rows of values (`P50` in ink, `P90` muted) and the day axis (`16 Nov`, `day` from `format.ts`, in the viewer's zone). Marks take `data-1..4`.

* `bar`: a bar per day (at most 24 px, 4 px rounded top), its value on the cap.
* `range`: a bar to the value (P50) and a lighter extension to the upper mark (P90).
* `line`: the value as a 2 px line with 8 px dots, the upper mark as a band; a day that is not measured breaks the line.

**Scale.** `capOf` takes about P95 of the figures shown, rounded up to a clean step in the display unit. With a week of daily figures P95 is the maximum, so a figure over four times their median is left out first (`fence`; counts pass `Infinity`, since a busy day is no outlier). A figure above the scale is drawn to the top with a break and its true value above it.

**States.** Measured; *part of the day* (some pull requests measured, or today so far): the mark is striped and its value muted; *not measured* (`v: null`, with the reason): a hatched column and `—`, never zero or a gap that reads as zero; selected day: `selected` fill and an `accent` base line in every chart of the stack. The head shows a key only for the states present.

**Behaviour.** Each column is a button: hover and focus show the day's figures (`data-tip`, OR-20); a click picks the day and a second click clears it (the caller filters the list). Past 16 columns the cap labels and value rows give way to tooltips, and the axis labels every few days.

**Accessibility.** The plot is a `group` named by `label`; each column button's name reads its day and figures, including "not measured" and the reason, so colour and texture are never the only key.

**Do.** Put every day chart of a section on the same columns; show the axis on the last one only. **Don't.** Sum medians into a stack, use two scales in one plot, or add `<svg>` (OR-8).

**Used in.** [Delivery](../pages/delivery.md) Pace (variants A and B, under review).
