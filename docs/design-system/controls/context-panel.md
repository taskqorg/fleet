# Context panel

**Code.** `ContextPanel`, `PlanItem`, `TimerRow`, `SwarmMember` and `TaskLink` in `packages/ui` ([FLT-30](https://youtrack.ufobe.com/issue/FLT-30)). It is the contents of a [side panel](side-panel.md), which gives it the column, the toggle, the width and the phone sheet. Demo: [book](../book/index.html#context-panel). Board: [blocks.bl](../board/blocks.bl), masters `Context panel`, `Plan item`, `Timer row` and `Swarm member`.

**Purpose.** Beside a conversation, shows what the session plans and keeps running: its plan, timers, automations, swarm and related tasks ([OR-51](../owner-rules.md)).

**Anatomy.** A column right of the conversation, on `surface` with a `line` border on its left, at the person's width (`size-search` by default). Under a head with the word Context (in the sheet also a Hide icon, [OR-15](../owner-rules.md)), sections in this order, each hidden when empty ([OR-29](../owner-rules.md)):

1. **Plan** (`Plan · 3 of 5`): one **plan item** per step, a check square and the step's words; done steps are checked in the success tone.
2. **Timers:** one **timer row** per timer: what it does, when it fires (`in 42m · once`, `paused · every 5m`), and Pause or Resume, Edit and Cancel as icons; a timer the person may not change shows no icons.
3. **Automations:** one line each, the rule and its schedule.
4. **Swarm** (`2 of 3 done`): one **swarm member** per agent: avatar, role and target, the plan steps assigned to it that are done (`1 of 2 steps`), and the [state badge](state-badge.md#dot-and-word) dot and word.
5. **Tasks:** one **task link** per related task (the session's own task, the plan's tasks, the tasks it spawned and its bot's tasks): code, title, and the state dot and word.
6. A last quiet line: "Model, tokens, cost and ids: Details" ([OR-54](../owner-rules.md)).

**States.** *Open*, *closed* (the conversation takes the width; the Context toggle in the shell header reopens it), *loading* (section skeletons), and on a phone a *sheet* opened from the same toggle ([OR-60](../owner-rules.md)).

**Behaviour.**
* A swarm member opens its own session view and a task link its task; a plan item opens nothing.
* Pause, Edit and Cancel act in place and change only the timer: the watch's event subscriptions stay. Cancel confirms in place.
* Showing or hiding the column keeps the transcript row being read in place and the composer's draft.
* Open or closed and the width are stored per person on the server by the side panel (`session-context`), like the navigation ([OR-41](../owner-rules.md)).
* It is part of the work area, never a floating layer ([OR-27](../owner-rules.md)).

**Keyboard.** Sections are headings in order; timer icons are buttons in reading order.

**Accessibility.** `role="complementary"` named "Session context"; a plan item is a read-only checkbox; each timer icon has its name ("Pause timer").

**Do.** Keep the sections in this order. **Don't.** Put metrics or cost in the panel's body.

**Used in.** The one session view ([OR-51](../owner-rules.md)).
