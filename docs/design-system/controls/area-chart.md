# Area chart

**Code.** `AreaChart` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Shows how a value moves over a time range: what was recorded up to now and what is projected after it. Used for subscription limits: left of capacity, or burn per hour ([people access §8](../../designs/fleet-people-access.md#limit-history-pool-and-projection)).

**Anatomy.** A plot frame (`line` border, `bg` fill, `radius-md`, four control heights tall). Each series is a filled area in `data-1..4`: recorded at 55 % opacity, projected at 25 %. A `step` series holds its value until the next point; a reset is two points at one time, so the projected area steps up there. Day ticks are 1 px `line`; the now line is 1 px `accent`. The top-left label is the scale maximum; the axis under the plot shows the range ends with `stamp`.

**Stacked.** With `stacked`, the series are layers on top of each other, the first at the bottom, drawn opaque on one scale (the maximum is the largest total); a legend under the axis names each layer with its last value. More than four layers reuse `data-1..4` with stripes (`pattern`), never new hues ([tokens](../tokens.md)). Used where the question is which part of a total grows.

**Drawing.** CSS only: each area is a block clipped by a `clip-path` polygon in percent of the plot. No inline SVG (OR-8), no chart library.

**States.** Data; no points in range (an empty plot with the axis); the now line outside the range is not drawn.

**Behaviour.** The range is the page's (Today and arrows move it). Hover the now line for the exact time. The chart is read only.

**Accessibility.** `role="img"` with a name that reads each series and its last value ("codex weekly left: recorded: 45%; projected: 20%").

**Do.** Show the numbers the chart summarises next to it (left, next reset, first empty). **Don't.** Use a chart as the only place a value appears (OR-19), or add arrows (OR-7).

**Used in.** [Hosts](../pages/hosts.md) Limits cards; [Delivery](../pages/delivery.md) (stacked, and one per stage card).
