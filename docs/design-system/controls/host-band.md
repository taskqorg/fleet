# Host band

**Code.** *Target*: `HostBand` in `packages/ui`, not built yet; gauges are the [fill bar](fill-bar.md#gauge), the strip is the [time strip](time-strip.md#run-strip). Board: [blocks.bl](../board/blocks.bl), masters `Host band` and `Gauge`.

**Purpose.** One host on [Live](../pages/live.md): its health, what runs in each slot, and its last 24 hours of runs.

**Anatomy.** A `surface` band with a `line` border and `radius-lg`, full width of the main column. Top to bottom:

1. **Head.** The host's state dot, its name in `font-size-lg`, `font-weight-bold` (600), the platform and free disk in `fg-muted`; on the right three gauges (CPU, MEM, DISK) and the day's runs and active time in mono.
2. **Problem line.** Only while the host has a problem: what fails, since when and what still works, in `danger-fg` on `danger-bg` ([OR-56](../owner-rules.md)).
3. **Slots.** One [worker slot](worker-slot.md) per slot, in a row that wraps.
4. **Run strip.** The 24-hour [run strip](time-strip.md#run-strip).

**States.**
* *Normal:* as above.
* *Problem:* the border turns `danger-fg` with a soft danger glow, the problem line shows, and the failing slot says why. Repeats aggregate ([OR-58](../owner-rules.md)).
* *Offline:* the state dot is danger, gauges and slots keep their last values dimmed with the age ([offline marker](offline-marker.md)).
* *Loading:* a band skeleton of the same height.

**Behaviour.**
* Bands are ordered: hosts with a problem first, then by busy slots ([Live](../pages/live.md)).
* The head opens the host in System; slots open their sessions ([OR-26](../owner-rules.md)). In wall mode nothing opens.
* Values update in place ([OR-6](../owner-rules.md)); a band that changes order moves after a short highlight, never while the person reads it ([OR-23](../owner-rules.md)).
* At phone width slots stack and the gauges move under the name.

**Accessibility.** `role="region"` named by the host; the problem line is `role="status"`.

**Do.** Keep the problem line in plain words. **Don't.** Put more than one problem line on a band: aggregate.

**Used in.** [Live](../pages/live.md) hosts column.
