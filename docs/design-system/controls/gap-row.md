# Gap row, step trail, before/after

**Code.** `GapRow`, `StepTrail` and `GapCompare` in `packages/ui` (`src/controls/gap-row.tsx`); `PickMenu` (`src/controls/lists.tsx`) for the filter chips; `Sparkline` takes `changeAt`. Board: `drafts/agent-garden` in the board repository, frames 1, 2 (1440) and 4, 5 (390).

**Purpose.** [System › Agents](../pages/system.md): what agents could not do and what fixes it. A gap row is one class of failure from `agent_gaps`; the step trail says how far its finding got; before/after shows the class's rate on each side of the change that should cut it.

**Anatomy.**
* **Gap row** (a list item; the caller gives `<ul class="gap-rows">`): the class in words (`python missing`, `Sandbox blocks writes`), a native link; under it the count and where (`17 runs · pds18, windows · codex`, calls for a gateway class); the class's days as a [sparkline](figure-tile.md), the bars after the change in the success tone with a marker before the first; a [state badge](state-badge.md) with words: Open (attention), Investigating, Waiting · #n, Watching · #n on hosts (running), Fixed (success), Still failing after #n (danger), Below threshold, Add a rule (neutral); the last failure last ([OR-64](work-facts.md#rules-kept-here)): `09:40` today, else the day.
* **Step trail:** Found · Investigating · Change · On hosts · Watching · Fixed; done steps carry ✓ in the success tone, the current step is `fg`; Still failing is the danger step; a dismissed finding shows Dismissed.
* **Before/after:** the failures a day before the change and after it (success), the class's days with the change marked, and one line on what closes it. After the verdict it shows the verdict's counts and runs.

**Layout.** The gap list is the size container. Wide: the name over the detail, then sparkline, badge and time on the right. Narrower than 26 rem: name, detail, then sparkline, badge and time on one line; nothing scrolls sideways ([OR-66](../patterns/no-horizontal-scroll.md#rules-kept-here)).

**Behaviour.** The whole row opens the class ([OR-26](../patterns/app-shell.md#rules-kept-here)): the name's link is stretched over the row; the badge's tooltip stays above it. The open class is marked (`aria-current`).

**Accessibility.** A list item named by its class and state; the sparkline is an image whose name lists the days; the step trail is an ordered list with the current step marked `aria-current="step"` and done steps named.

**Do.** Say the state in words. **Don't.** Put Investigate on the row: the decision is in the opened class, on its finding.

**Used in.** [System](../pages/system.md).
