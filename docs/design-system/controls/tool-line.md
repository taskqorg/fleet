# Tool line

**Code.** `ToolLine` in `packages/ui`: the folded form of the thread's tool rows ([transcript viewer](transcript-viewer.md)); the session view wires it into `Thread`. Demo: [book](../book/index.html#tool-line). Board: [blocks.bl](../board/blocks.bl), master `Tool line`.

**Purpose.** Keeps a conversation light: consecutive tool calls and system events between two messages become one quiet line ([OR-52](../owner-rules.md), principle 3).

**Anatomy.** One line in `font-size-sm`, `fg-muted`, no fill: a disclosure caret, what was done in words ("Looked at the Home canvas and 3 session states"), and the total time in mono (`13s`, [OR-2](../owner-rules.md)).

**States.**
* *Folded:* one line.
* *Open:* the calls below it, one mono row each (tool, target, time), on `surface-sunken`; it opens downward ([OR-23](../owner-rules.md)).
* *Running:* the caret is a running dot and the time ticks; the words name the current call.
* *Failed:* a call failed and the agent did not recover: the line turns `danger-fg` and names the failed call ([OR-56](../owner-rules.md)). A failure the agent recovered from stays quiet.

**Behaviour.**
* A click anywhere on the line opens or folds it ([OR-26](../owner-rules.md)).
* Repeated attachments, heartbeats and internal notices are never shown, folded or open ([OR-52](../owner-rules.md)); repeats inside an open line aggregate with `×N` ([OR-58](../owner-rules.md)).
* Model, tokens and cost of the calls are in the session's Details, not on the line ([OR-54](../owner-rules.md)).

**Keyboard.** One focus stop; `Enter` or `Space` toggles; arrow keys move between the open calls.

**Accessibility.** A button with `aria-expanded`, named by its words and time ("Looked at the Home canvas, 13 seconds").

**Do.** Write the words from the calls' purpose. **Don't.** List tool names on the folded line.

**Used in.** The one session view ([OR-51](../owner-rules.md)): Conversations, sessions opened from Home and Projects.
