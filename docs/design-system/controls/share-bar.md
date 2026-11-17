# Share bar

**Code.** `ShareBar` in `packages/ui` (`src/charts.tsx`). Demo: the Delivery mockup variants.

**Purpose.** Shows one whole split into parts with the size of each part: where the time of a pull request goes (PR checks, Queue, Release, Install) over the chosen range or the picked day.

**Anatomy.** An optional title; a bar `size-badge` tall with segments in `data-1..4` separated by a 2 px gap; the value of a segment inside it in `data-N-ink` when the segment holds at least 9 % of the whole; under the bar a legend with every part, its value and its share, and an optional note saying what the figures are ("mean per pull request, 76 of 389 measured").

**Figures.** Parts must add up to the whole they split. For durations use the mean per pull request over the pull requests with every part measured, so the parts add up to their mean open-to-installed. Never stack medians.

**Behaviour.** Hover a segment for its value, share and any detail the caller adds (for example the stage's P50).

**Accessibility.** `role="img"` with a name that reads every part, value and share; the legend is always visible, so colour is never the only key.

**Do.** Keep the part order and tones fixed (pipeline order). **Don't.** Use it for a fill against capacity; that is the [fill bar](fill-bar.md).

**Used in.** [Delivery](../pages/delivery.md) Pace (variants under review).
