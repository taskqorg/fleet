# State counters

**Code.** `StateCounters`. Demo: [book](../book/index.html#data-list).

**Purpose.** Shows how many items are in each state and filters by state with one click. This is the quick state filter in every list, and the per-project summary on the Overview ([D9](../decisions.md)).

**Anatomy.** One capsule per state, in the fixed registry order. Each capsule has the state's icon and its count in monospace, tinted by tone. The state name is in a tooltip only (B1, OR-20).

**States.**
* *Count zero:* neutral, no fill.
* *Count unknown:* shows `?`, never `0` ([principles §2](../principles.md#2-measurement-basis)).
* *Active:* outlined in the tone colour, `aria-pressed`.

**Behaviour.**
* Clicking toggles that state in the view's state filter. Several states can be active together.
* Counts reflect every other active filter (project, agent, search), so they show what one click will give.
* Counts update in place and never reorder (W13).
* Callers may omit states that are not relevant, as the Overview does with completed and cancelled.

**Accessibility.** `group` named for its scope ("Tasks by state"). Each capsule is named "Waiting: 230".

**Do.** Keep one order everywhere. **Don't.** Add the state word next to the icon (B1).
