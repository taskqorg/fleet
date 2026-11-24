# Figure tile

**Code.** `FigureTile` and its `Sparkline` in `packages/ui` (`src/controls/figure-tile.tsx`); the number is [`Num`](number.md), an unknown value is `?`. Demo: [book](../book/index.html#figure-tile). Board: [blocks.bl](../board/blocks.bl), master `Figure tile`.

**Purpose.** One headline figure on [Live](../pages/live.md) with its recent trend, readable from across a room (principle 1).

**Anatomy.** A `surface` tile with a `line` border and `radius-lg`:
* the label in `font-size-sm`, `fg-muted` ("Agents working");
* the value in mono, `font-size-2xl`, `font-weight-bold`, rounded ([OR-59](../owner-rules.md), [formats](../formats.md));
* one sub-line in `font-size-xs`, `fg-muted` ("of 8 slots", "month $812");
* on the right a sparkline of the same figure, bars in `data-1`; the latest bar is the current value.

**States.**
* *Normal:* value in `fg`.
* *Alert:* the value and the latest bars turn `danger-fg` when the figure means trouble (failed starts), and the sub-line names the cause ([OR-18](../owner-rules.md), [OR-56](../owner-rules.md)).
* *Unknown:* the value is `?`, never `0` ([principles §2](../principles.md#2-measurement-basis)).
* *Stale:* dimmed, nothing pulses ([offline marker](offline-marker.md)).

**Behaviour.**
* Values update in place with fixed-width figures and the change tint ([OR-6](../owner-rules.md)).
* Qualifiers (`≈`, subscription, partial) are in the tooltip ([OR-59](../owner-rules.md)).
* A click opens the list behind the figure; in wall mode nothing opens.

**Accessibility.** Named "Agents working: 4 of 8"; the sparkline is `role="img"` named by its range ("last 24 hours, 2 to 6").

**Do.** Keep six tiles at most in one row. **Don't.** Add arrows ([OR-7](../owner-rules.md)) or colour a normal value.

**Used in.** [Live](../pages/live.md) figures row.
