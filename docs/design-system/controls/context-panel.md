# Context panel

**Code.** `ContextPanel`, `PlanItem`, `TimerRow`, `TimerChip`, `SwarmMember` and `TaskLink` in `packages/ui` ([FLT-30](https://youtrack.ufobe.com/issue/FLT-30)). It is the contents of a [side panel](side-panel.md), which gives it the column, the toggle, the width and the phone sheet. Demo: [book](../book/index.html#context-panel). Board: [blocks.bl](../board/blocks.bl), masters `Context panel`, `Plan item`, `Timer row` and `Swarm member`.

**Purpose.** Beside a conversation, shows what the session plans and keeps running: its plan, timers, automations, swarm and related tasks ([OR-51](../owner-rules.md)).

**Anatomy.** A column right of the conversation, on `surface` with a `line` border on its left, at the person's width (`size-search` by default). Under a head with the word Context (in the sheet also a Hide icon, [OR-15](../patterns/list-detail.md#rules-kept-here)), sections in this order, each hidden when empty ([OR-29](state-counters.md#rules-kept-here)):

1. **Plan** (`Plan · 3 of 5`): one **plan item** per step, a check square and the step's words; done steps are checked in the success tone.
2. **Timers:** one **timer row** per timer of the session (a [trigger](../../designs/fleet-triggers.md) that puts a message into this conversation): its schedule (`Every 5m`, `Paused · Every 5m`) with the live time to the next check (`in 3m`, [OR-2](number.md#rules-kept-here)), the message it sends (up to three lines), Pause or Resume, Edit and Cancel as icons, and the quiet checks since it last fired (`12 quiet checks since 14:05`). A timer the person may not change shows no icons. While its command is in the composer the row is selected (running tone).
3. **Automations:** one line each, the rule and its schedule.
4. **Swarm** (`2 of 3 done`): one **swarm member** per agent: avatar, role and target, the plan steps assigned to it that are done (`1 of 2 steps`), and the [state badge](state-badge.md#dot-and-word) dot and word.
5. **Tasks:** one **task link** per related task (the session's own task, the plan's tasks, the tasks it spawned and its bot's tasks): code, title, and the state dot and word.
6. A last quiet line: "Model, tokens, cost and ids: Details" ([OR-54](../owner-rules.md)).

**States.** *Open*, *closed* (the conversation takes the width; the Context toggle in the shell header reopens it), *loading* (section skeletons), and on a phone a *sheet* opened from the same toggle ([OR-60](../patterns/app-shell.md#rules-kept-here)).

**Behaviour.**
* A swarm member opens its own session view and a task link its task; a plan item opens nothing.
* Pause and Cancel act in place and change only that timer. Cancel confirms in place.
* Edit never opens a form: it fills the [composer](composer.md#commands) with the timer's command (`/trigger conversation-check every 5m if changed: <text>`) and focuses it, so the interval and the message are edited as text and applied with Enter.
* Showing or hiding the column keeps the transcript row being read in place and the composer's draft.
* Open or closed and the width are stored per person on the server by the side panel (`session-context`), like the navigation ([OR-41](side-panel.md#rules-kept-here)).
* It is part of the work area, never a floating layer ([OR-27](../patterns/list-detail.md#rules-kept-here)).

**Keyboard.** Sections are headings in order; timer icons are buttons in reading order.

**Accessibility.** `role="complementary"` named "Session context"; a plan item is a read-only checkbox; each timer icon has its name ("Pause timer", "Edit timer in the composer"); the live time is not a live region, its long form is the accessible name.

**Do.** Keep the sections in this order. **Don't.** Put metrics or cost in the panel's body.

**Used in.** The one session view ([OR-51](../owner-rules.md)).

## Timer chip

`TimerChip` (owner pick A, 2026-11-25; board [screens/session.bl](../board/screens/session.bl), *Session timers (approved A)*). In the [session head](../pages/session.md#layout), after the state, while the session has a timer: a clock and `5m · in 3m`, or a pause icon and `paused · 5m`; `+1` when there are more timers. It shows the first active timer, else the first paused one. The figures keep their width ([OR-6](../owner-rules.md)). A click opens the context panel (the sheet on a phone). Its accessible name says the whole state ("Timer every 5m, next check in 3 minutes: open in Context").

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **UR-91.** The context panel and long details open as a sheet, never as a floating panel over the conversation ([OR-51](../owner-rules.md), [OR-27](../patterns/list-detail.md#rules-kept-here)). *Check:* Review.
