# Pulse line

**Code.** `PulseLine` and `BlockerChip` in `packages/ui`. Demo: [book](../book/index.html#pulse-line). Board: [blocks.bl](../board/blocks.bl), masters `Pulse line` and `Blocker chip`.

**Purpose.** One line at the top of [Home](../pages/home.md) that answers "how much is in flight and what is stuck?" at a glance (principle 1, [OR-50](../owner-rules.md)).

**Anatomy.** One line in `font-size-sm`, read left to right:

1. **Counts.** Working, queued, done today: a dot in the state's tone, the count in mono and `font-weight-medium`, the word in `fg-muted`. A zero count is hidden ([OR-29](state-counters.md#rules-kept-here)).
2. **Problems.** One quiet count of open problems with who owns them (`33 problems · HAL0 on them`), a `fg-muted` dot ([routing rule](../../designs/fleet-system-management.md#problems-anomalies-with-a-bot-owner)). Then loud only what no bot is fixing: one `attention-bg` pill for stuck problems (`1 stuck: Provider out of quota`, the first one named) and one `danger-bg` pill for problems without an owner, among them a duty bot's down problem (`1 problem without an owner`).
3. **Blocker chips.** Failed runs that need a look, one per cause: a `danger-bg` pill with a danger dot, the cause in words and `×N` ([OR-56](../owner-rules.md), [OR-58](../owner-rules.md)).
4. **`Problems ›`** when there are problems, else **`Live ›`**, at the end.

**States.**
* *Normal:* counts, no chips.
* *Blocked:* one chip per cause, newest cause first; chips that do not fit the line fold into `+K` ([OR-30](../patterns/infinite-list.md#rules-kept-here)).
* *Stale:* the counts keep their last value with the [offline marker](offline-marker.md) age.

**Behaviour.**
* A count opens [Conversations](../pages/conversations.md) with that state as the filter.
* A blocker chip opens [Live](../pages/live.md) at that host or cause; the problems count and its pills open System › Problems ([system](../pages/system.md)), a pill at its first problem.
* Values update in place with fixed-width figures ([OR-6](../owner-rules.md)); a new chip never moves the counts ([OR-23](../owner-rules.md)).
* At phone width the line wraps; chips keep the cause and fold `×N` into the tooltip.
* The line never runs past its column: a cause longer than the line ends in an ellipsis inside its chip, and the chip's tooltip holds the full cause ([OR-20](../owner-rules.md)).

**Keyboard.** Each count and chip is a link in reading order.

**Accessibility.** `role="group"` named "Pulse"; each count is named "4 working"; a chip is named "Blocker: Codex cannot start on pds18-native, 22 times".

**Do.** Keep the line one line at desktop width. **Don't.** Add arrows to show trends ([OR-7](../owner-rules.md)) or a chip per repeat.

**Used in.** [Home](../pages/home.md).
