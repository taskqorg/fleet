# State counters

**Code.** `StateCounters`. Demo: [book](../book/index.html#data-list).

**Purpose.** Shows how many items are in each state and filters by state with one click. This is the quick state filter in every list, and the per-project summary on the Overview ([D9](../decisions.md)).

**Anatomy.** First a Σ capsule with the bold total count (OR-29), then one capsule per state, in the fixed registry order. Each capsule has the state's icon and its count in monospace, tinted by tone. The state name is in a tooltip only (B1, OR-20).

**States.**
* *Count zero:* neutral, no fill, icon only with no `0` (OR-29). A counter block (not a quick filter, for example an Overview project row) passes `hideZero`: zero capsules are hidden and the caller hides a block whose values are all zero.
* *Count unknown:* shows `?`, never `0` ([principles §2](../principles.md#2-measurement-basis)).
* *Active:* outlined in the tone colour, `aria-pressed`.

**Behaviour.**
* Σ shows all items: it clears the state filter and is pressed while no state is selected ([D12](../decisions.md)).
* A state capsule toggles that state in the filter. Several states can be on together. Clicking an active state turns it off; when the last one turns off, Σ is pressed again.
* The Σ total is the sum of the shown states. If any count is unknown, the total is `?`.
* Counts reflect every other active filter (project, agent, search), so they show what one click will give.
* Counts update in place and never reorder (W13).
* Callers may omit states that are not relevant, as the Overview does with completed and cancelled.

**Accessibility.** `group` named for its scope ("Tasks by state"). Each capsule is named "Waiting: 230".

**Do.** Keep one order everywhere. **Don't.** Add the state word next to the icon (B1).
