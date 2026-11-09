# Stat strip

**Code.** `StatStrip`. Demo: [book](../book/index.html#stat-strip).

**Purpose.** Shows a few operational counts at a glance and links each one to its source: things that need you, failures, workers running against slots per host, queued work and spend.

**Anatomy.** A row of compact tiles. Each tile has an icon, a [number with basis](number.md), an optional `/slots` part, a short label, and an optional link.

**Behaviour.**
* A tile takes a tone only when it needs attention, for example when all slots are full or something failed (OR-18).
* Values update in place with the change tint (OR-6).
* For an offline host, the last known value shows as a lower bound (`≥ 1/2`) with its age in the tooltip.
* Each tile links to the filtered list behind its number.

**Do.** Show at most one strip per page, near the top (W1). **Don't.** Use big cards or gauges (W2).
