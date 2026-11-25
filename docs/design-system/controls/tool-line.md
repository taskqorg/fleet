# Tool line

**Code.** `ToolLine` in `packages/ui`: the folded form of the thread's tool rows ([transcript viewer](transcript-viewer.md)); the session view wires it into `Thread`. Demo: [book](../book/index.html#tool-line). Board: [blocks.bl](../board/blocks.bl), master `Tool line` (the chip form is drawn in the Conversations v2 canvas).

**Purpose.** Keeps a conversation light: consecutive tool calls and system events between two messages become one quiet line ([OR-52](../owner-rules.md), principle 3).

**Anatomy.** One line in `font-size-sm`: one chip per kind of call in the order the kinds first appear, then the total time in mono (`20s`, [OR-2](../owner-rules.md)). A chip (`size-chip`, `surface-sunken`, `line` border) has the kind's icon and name, the count of calls, failures in `attention-fg` ("2 failed") and the kind's time when it is half a second or more. Kinds (`toolKind` in `tool-fold.ts`): Shell, Files, Edits, Search, Web, Results (`artifact_*`), Context (compaction), Fleet (Fleet's MCP tools), Notice (Fleet's notices to the agent) and Tools (any other). Approved: board `drafts/conversations-v2.bl` (Conversations v2, owner pick c2, 2026-11-25).

**States.**
* *Folded:* the chips and the time.
* *Open:* a chip opens its calls below the line on a `surface` block with a head ("Results · 4 calls · 2 failed · 850 ms"), one mono row each: tool, a readable target (a code or a name rather than a UUID, the last two parts of a path), the error line when it failed, time. A call opens its input and output (the harness's own values) below it, loaded when it opens (`tool_output {callId}`): the thread reads tool calls as summaries (`thread_entries full: false`: kind, target, error line, times), so a long session's transcript does not carry every output. The block opens downward ([OR-23](../owner-rules.md)); one chip is open at a time.
* *Running:* a running dot leads, the running chip has a `running-fg` border, and the time ticks.
* *Failed:* a call failed and the agent did not recover (no later call of the same kind worked): the failed chip's border and count turn `danger-fg` and the line starts with that chip open ([OR-56](../owner-rules.md)). A failure the agent recovered from only shows in its chip's count.

**Behaviour.**
* A click on a chip opens or folds its calls ([OR-26](../owner-rules.md)); a click on a call opens or folds its input and output.
* Repeated attachments, heartbeats and internal notices are never shown, folded or open ([OR-52](../owner-rules.md)); repeats inside an open chip aggregate with `×N` ([OR-58](../owner-rules.md)).
* Model, tokens and cost of the calls are in the session's Details, not on the line ([OR-54](../owner-rules.md)).

**Keyboard.** Each chip is a focus stop; `Enter` or `Space` toggles it; arrow keys move between the open calls; `Enter` on a call opens its input and output.

**Accessibility.** A group named by the line's words and time ("Ran a command, saved 3 results, 20 seconds"); each chip a button with `aria-expanded`, named by kind, count, failures and time ("Results: 4 calls, 2 failed, 850 milliseconds").

**Do.** Group by what the calls did. **Don't.** List tool names on the folded line or show bare UUIDs and host paths as targets.

**Used in.** The one session view ([OR-51](../owner-rules.md)): Conversations, sessions opened from Home and Projects.
