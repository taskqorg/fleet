# Pulse line

**Code.** `PulseLine` and `BlockerChip` in `packages/ui`. Demo: [book](../book/index.html#pulse-line). Board: [blocks.bl](../board/blocks.bl), masters `Pulse line` and `Blocker chip`.

**Purpose.** One line at the top of [Home](../pages/home.md) that answers "how much is in flight and what is stuck?" at a glance (principle 1, [OR-50](../owner-rules.md)).

**Anatomy.** One line in `font-size-sm`, read left to right:

1. **Counts.** Working, queued, done today: a dot in the state's tone, the count in mono and `font-weight-medium`, the word in `fg-muted`. A zero count is hidden ([OR-29](../owner-rules.md)).
2. **Blocker chips.** One per cause: a `danger-bg` pill with a danger dot, the cause in words and `×N` ([OR-56](../owner-rules.md), [OR-58](../owner-rules.md)).
3. **`Live ›`** at the end, in `fg-muted`.

**States.**
* *Normal:* counts, no chips.
* *Blocked:* one chip per cause, newest cause first; chips that do not fit the line fold into `+K` ([OR-30](../owner-rules.md)).
* *Stale:* the counts keep their last value with the [offline marker](offline-marker.md) age.

**Behaviour.**
* A count opens [Conversations](../pages/conversations.md) with that state as the filter.
* A blocker chip opens [Live](../pages/live.md) at that host or cause.
* Values update in place with fixed-width figures ([OR-6](../owner-rules.md)); a new chip never moves the counts ([OR-23](../owner-rules.md)).
* At phone width the line wraps; chips keep the cause and fold `×N` into the tooltip.
* The line never runs past its column: a cause longer than the line ends in an ellipsis inside its chip, and the chip's tooltip holds the full cause ([OR-20](../owner-rules.md)).

**Keyboard.** Each count and chip is a link in reading order.

**Accessibility.** `role="group"` named "Pulse"; each count is named "4 working"; a chip is named "Blocker: Codex cannot start on pds18-native, 22 times".

**Do.** Keep the line one line at desktop width. **Don't.** Add arrows to show trends ([OR-7](../owner-rules.md)) or a chip per repeat.

**Used in.** [Home](../pages/home.md).
