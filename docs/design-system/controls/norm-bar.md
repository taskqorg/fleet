# Norm bar

**Code.** `NormBar` in `packages/ui` (`src/charts.tsx`). Demo: the Delivery mockup, Moving now.

**Purpose.** Shows how long an item has been in its current stage against that stage's norm, so a column of items in different stages compares at a glance.

**Anatomy.** An 8 px track two norms long (0 to 2 × P90 of the stage), so the P90 tick (2 px, `fg`) sits in the middle of every row; a 1 px P50 tick in `fg-muted`; the fill is the time so far in `fg-muted`, and `danger-fg` past P90. Past the end of the track the fill stops with a break. A stage without a norm (Open, where people work; Checks failed and Queue failed, which need a look at once) shows an empty hatched track.

**Behaviour.** None of its own. The row shows the time in stage as an age chip (red past P90, OR-18) and the norm in text (`P90 8m`), with the baseline in its tooltip.

**Accessibility.** `role="img"` named with the stage, the time, P50 and P90, and "past the norm" when it is.

**Used in.** [Delivery](../pages/delivery.md) Moving now.
