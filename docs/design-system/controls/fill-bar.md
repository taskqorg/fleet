# Fill bar

**Code.** `FillBar` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Shows how full a store or quota window is, split by project or series.

**Anatomy.** 8 px bar on `neutral-bg` with `radius-md`; segments in `data-1..4` per project in fixed order; legend text after the bar ("game 78% · design 13%"); optional warning mark at the threshold (a 1 px `attention-fg` tick).

**States.** Normal; warning (below the free-space threshold: outline in `attention-fg`, legend says "18 GB free, warning at 25 GB"); full (outline in `danger-fg`, legend "Full: new uploads stop"); stale (segments at 50% opacity, legend "as of 2h ago"); unavailable (empty bar, legend "—, unavailable: no report").

**Behaviour.** Hover a segment to see its project, bytes and share. Clicking a segment opens the project's results on that store.

**Accessibility.** `role="img"` with an accessible name that reads the legend in full; legend text is always visible, so colour is never the only key.

**Do.** Keep project colours stable across pages. **Don't.** Use a gauge or donut for fill.

**Used in.** Hosts and stores, Overview capacity, account quota windows.
