# Area chart

**Code.** `AreaChart` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Shows how a value moves over a time range: what was recorded up to now and what is projected after it. Used for subscription limits: left of capacity, or burn per hour ([people access §8](../../designs/fleet-people-access.md#limit-history-pool-and-projection)).

**Anatomy.** A plot frame (`line` border, `bg` fill, `radius-md`, four control heights tall). Each series is a filled area in `data-1..4`: recorded at 55 % opacity, projected at 25 %. A `step` series holds its value until the next point; a reset is two points at one time, so the projected area steps up there. Day ticks are 1 px `line`; the now line is 1 px `accent`. The top-left label is the scale maximum; the axis under the plot shows the range ends with `stamp`.

**Stacked.** With `stacked`, the series are layers on top of each other, the first at the bottom, drawn opaque on one scale (the maximum is the largest total); a legend under the axis names each layer with its last value. Layers past the four data hues, or a second layer of one hue, take stripes (`pattern`), never new hues ([tokens](../tokens.md)). Used where the question is which part of a total grows.

**Drawing.** CSS only: each area is a block clipped by a `clip-path` polygon inside `.ac-plot` in percent of the plot. Zero `<svg>` (`OR-8`), no chart library. Interactive bucket selectors (`<button>` controls in `.ac-buckets`) live outside `role="img"` so buttons are never nested inside an image role.

**States.** Data; selected bucket highlight; no points in range (an empty plot with the axis); the now line outside the range is not drawn.

**Behaviour.** Supports both read-only quota windows (Host Limits: Today and arrows move the range, hover the now line for exact time) and interactive analytics time buckets (`buckets`, `selectedBucket`, `onSelectBucket`) used in Projects analytics. Hovering or focusing a bucket or marker shows details via `data-tip` (`OR-20`), never browser `title`; clicking a bucket toggles filtering in place. Touch viewports expand bucket targets to at least 44 px.

**Accessibility.** `role="img"` sits on `.ac-plot` only (never wrapping `<button>` elements) with an `aria-label` that reads each series and its latest value ("codex weekly left: recorded: 45%; projected: 20%"). Bucket `<button>` selectors sit in sibling `.ac-buckets` with `aria-pressed` and `data-tip`.

**Do.** Show the numbers the chart summarises next to it (`Totals`, `CostFigures`, or summary metrics). **Don't.** Use a chart as the only place a value appears (`OR-19`), add `<svg>` elements (`OR-8`), add directional arrows (`OR-7`), or put `<button>` elements inside `role="img"`.

**Used in.** [Hosts](../pages/hosts.md) Limits cards; [Projects](../pages/projects.md) resource analytics; [Delivery](../pages/delivery.md) (stacked, and one per stage card).
