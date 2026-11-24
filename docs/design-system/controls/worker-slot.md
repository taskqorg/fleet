# Worker slot

**Code.** *Target*: `WorkerSlot` in `packages/ui`, not built yet. Board: [blocks.bl](../board/blocks.bl), master `Worker slot`.

**Purpose.** One worker slot of a host on [Live](../pages/live.md): what runs in it, or that it is free or failing.

**Anatomy.** A tile in the [host band](host-band.md)'s slot row, height `size-row-2` plus one line:
* a 3 px bar on the left edge in the project's colour;
* first line: a running dot, the task title in `font-weight-medium`, the elapsed time in mono on the right ([OR-2](../owner-rules.md));
* second line in `font-size-xs`, `fg-muted`: the [entity code](entity-card.md), project and harness, then tokens on the right.

**States.**
* *Busy:* as above; the dot pulses and the time ticks every second ([OR-6](../owner-rules.md)).
* *Idle:* a dashed `line` border and the word "idle" centred in `fg-faint`.
* *Failing:* a `danger-fg` border; the first line says what fails (`Codex start failed ×22`, [OR-58](../owner-rules.md)) and when it retries; the second line the cause ([OR-56](../owner-rules.md)).

**Behaviour.**
* A busy or failing slot opens its session view ([OR-26](../owner-rules.md)); an idle slot opens nothing. In wall mode nothing opens.
* Slots keep their order; a slot that frees up becomes idle in place ([OR-23](../owner-rules.md)).

**Accessibility.** Named "Slot 2: Fix receipt factory, T-FLT-51, running 12 minutes"; an idle slot "Slot 4, idle".

**Do.** Keep the title the widest part ([OR-21](../owner-rules.md)). **Don't.** Show model or cost ([OR-54](../owner-rules.md)).

**Used in.** [Host band](host-band.md) on [Live](../pages/live.md).
