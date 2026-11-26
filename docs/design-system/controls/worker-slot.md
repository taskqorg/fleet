# Worker slot

**Code.** `WorkerSlot` in `packages/ui` (`src/controls/worker-slot.tsx`); the project colour is one of the data tones. Demo: [book](../book/index.html#worker-slot). Board: [blocks.bl](../board/blocks.bl), master `Worker slot`.

**Purpose.** One worker slot of a host on [Live](../pages/live.md): what runs in it, or that it is free or failing.

**Anatomy.** A tile in the [host band](host-band.md)'s slot row, height `size-row-2` plus one line:
* a 3 px bar on the left edge in the project's colour;
* first line: a running dot, the task title in `font-weight-medium`, the elapsed time in mono on the right ([OR-2](number.md#rules-kept-here));
* second line in `font-size-xs`, `fg-muted`: the [entity code](entity-card.md) and project, then the [work facts](work-facts.md) on the right: `×N` attempts of the task, its time in work since the first attempt (when that is earlier than this run), and the agent mark. Tokens are left out: the slot answers how the work goes, not what it costs ([OR-54](../owner-rules.md)). Without work facts (the book's older demo) the second line keeps the harness name and tokens.

**States.**
* *Busy:* as above; the dot pulses and the time ticks every second ([OR-6](../owner-rules.md)).
* *Idle:* a dashed `line` border and the word "idle" centred in `fg-faint`. A host with more free slots than Live draws one by one (three) shows them as one idle tile with their count, `23 idle` (`free`), so a 24-slot host stays one glance.
* *Failing:* a `danger-fg` border; the first line says what fails and when it retries; the second line the cause ([OR-56](../owner-rules.md)), then `×N` failed starts ([OR-58](../owner-rules.md)) and the time since the latest one (the slot has no time in work).

**Behaviour.**
* A busy or failing slot opens its session view ([OR-26](../patterns/app-shell.md#rules-kept-here)); an idle slot opens nothing. In wall mode nothing opens.
* Slots keep their order; a slot that frees up becomes idle in place ([OR-23](../owner-rules.md)).

**Accessibility.** Named "Slot 2: Fix receipt factory, T-FLT-51, running 12 minutes"; an idle slot "Slot 4, idle"; a counted one "23 slots idle".

**Do.** Keep the title the widest part ([OR-21](data-list.md#rules-kept-here)). **Don't.** Show model or cost ([OR-54](../owner-rules.md)).

**Used in.** [Host band](host-band.md) on [Live](../pages/live.md).
