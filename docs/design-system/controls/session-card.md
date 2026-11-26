# Session card

**Code.** `SessionCard` and `SessionCardSkeleton` in `packages/ui`, with `Avatar` and `ProjectChip`. Demo: [book](../book/index.html#session-card). Board: [blocks.bl](../board/blocks.bl), master `Session card`.

**Purpose.** One active session on [Home](../pages/home.md): who it is, what it does now, and what it needs from the person, answered in place ([OR-50](../owner-rules.md), [OR-53](decision-block.md#rules-kept-here)).

**Anatomy.** A `surface` card with a `line` border and `radius-lg`, padding `space-5`, fixed width in its grid. Top to bottom:

1. **Head.** The avatar (the bot's mark, or `P` for a project PM in the project's colour; [agent mark](transcript-viewer.md)), the name in `font-weight-medium`, and the project chip.
2. **State.** The dot and word of the [state badge](state-badge.md#dot-and-word), from the one vocabulary ([OR-55](state-badge.md#rules-kept-here)).
3. **Now.** One line, at most two, saying what the session does now. It never shows a raw id or a tool call.
4. **Progress.** A 2 px bar in the running tone, only when the session has a plan with steps.
5. **Meta line.** `font-size-sm` in `fg-muted`: step, host, the next timer, then at its right end the [work facts](work-facts.md) (`facts`), the time last ([OR-2](number.md#rules-kept-here), [OR-64](work-facts.md#rules-kept-here)). A missing fact is left out, never shown as a dash.
6. **Question.** At most one, the oldest open one, as the card form of the [decision block](decision-block.md#card).

**States.** One variant per state of the vocabulary; the border and the question carry the tone ([OR-56](../owner-rules.md)):

| State | Border | Question |
|---|---|---|
| Working | `line` | none |
| Waiting for you | `attention-line` | the open question |
| Queued | `line` | none; the meta line gives the queue position |
| Failed | `danger-fg` | the recovery question when the agent asked one (Retry, Open) |
| Done | `line` | none; shown only in the board's anatomy row, since a finished session leaves Home |

Plus *stale* (the whole card at reduced contrast with the [offline marker](offline-marker.md) age) and *loading* (a skeleton card of the same size).

**Behaviour.**
* The whole card opens the session in the [one session view](../owner-rules.md) ([OR-26](../patterns/app-shell.md#rules-kept-here), [OR-51](../owner-rules.md)); the question's buttons act in place and do not open it.
* Answering edits the card in place; the next open question takes its place.
* Values update in place ([OR-6](../owner-rules.md)); the card being read never moves ([OR-23](../owner-rules.md)). A session that changes group moves after a short highlight.
* The card never shows model, tokens, cost or ids ([OR-54](../owner-rules.md)).

**Keyboard.** The card is one focus stop; `Enter` opens the session. `Tab` moves into the question's buttons and its answer field.

**Accessibility.** `role="article"` named by the session name; the state word is text, so colour is never the only signal. The progress bar is `role="progressbar"` with the step count as its value.

**Do.** Keep the "now" line in the person's words. **Don't.** Add hover buttons ([OR-37](../patterns/list-view.md#rules-kept-here)), a metrics header or a second question.

**Used in.** [Home](../pages/home.md) groups; the phone Home at full width.
