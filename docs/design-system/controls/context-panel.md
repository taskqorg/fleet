# Context panel

**Code.** `ContextPanel`, `PlanItem`, `TimerRow` and `SwarmMember` in `packages/ui` ([FLT-30](https://youtrack.ufobe.com/issue/FLT-30)); the phone sheet is the session view's. Demo: [book](../book/index.html#context-panel). Board: [blocks.bl](../board/blocks.bl), masters `Context panel`, `Plan item`, `Timer row` and `Swarm member`.

**Purpose.** Beside a conversation, shows what the session plans and keeps running: its plan, timers, automations and swarm ([OR-51](../owner-rules.md)).

**Anatomy.** A column right of the conversation, on `surface` with a `line` border on its left, width `size-search`. Under a head with the word Context and a Hide icon ([OR-15](../owner-rules.md)), sections in this order, each hidden when empty ([OR-29](../owner-rules.md)):

1. **Plan** (`Plan · 3 of 5`): one **plan item** per step, a check square and the step's words; done steps are checked in the success tone.
2. **Timers:** one **timer row** per timer: what it does, when it fires (`in 42m · once`), and Pause, Edit and Cancel as icons.
3. **Automations:** one line each, the rule and its schedule.
4. **Swarm** (`2 of 3 done`): one **swarm member** per agent: avatar, role and target, and the [state badge](state-badge.md#dot-and-word) dot and word.
5. A last quiet line: "Model, tokens, cost and ids: Details" ([OR-54](../owner-rules.md)).

**States.** *Open*, *hidden* (the conversation takes the width; a Context icon in the session head reopens it), *loading* (section skeletons), and on a phone a *sheet* opened from Context ([OR-60](../owner-rules.md)).

**Behaviour.**
* A swarm member opens its own session view; a plan item opens nothing.
* Pause, Edit and Cancel act in place; Cancel confirms in place.
* Open or hidden is stored per person on the server, like the column widths ([OR-41](../owner-rules.md)).
* It is part of the work area, never a floating layer ([OR-27](../owner-rules.md)).

**Keyboard.** Sections are headings in order; timer icons are buttons in reading order.

**Accessibility.** `role="complementary"` named "Session context"; a plan item is a read-only checkbox; each timer icon has its name ("Pause timer").

**Do.** Keep the sections in this order. **Don't.** Put metrics or cost in the panel's body.

**Used in.** The one session view ([OR-51](../owner-rules.md)).
