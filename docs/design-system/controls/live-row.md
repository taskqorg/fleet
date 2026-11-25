# Live row

**Code.** `LiveRow` in `packages/ui` (`src/controls/live-row.tsx`), a list item; the caller gives the `<ul>`. Demo: [book](../book/index.html#live-row). Board: [blocks.bl](../board/blocks.bl), master `Live row`.

**Purpose.** One compact row of the side column on [Live](../pages/live.md): a project now, a task in work, a pull request in delivery, a CI runner, or a recent event.

**Anatomy.** A row on `surface` with a `line-soft` separator, height `size-row`, state first ([OR-22](../owner-rules.md)). One variant per kind:

| Kind | Left | Right |
|---|---|---|
| Project | name; under it running, queued and done counts in their tones ([OR-29](../owner-rules.md)) | a sparkline of runs |
| Task | the shared [state dot](state-badge.md#dot-and-word) of the task's state (OR-55), task [entity code](entity-card.md) and title | [work facts](work-facts.md): `×N`, agent, time in work last; latest attempt and time in state on hover |
| Delivery | state dot, pull request [entity code](entity-card.md) and title | the [stage chip](stage-chip.md), then the [work facts](work-facts.md) (time since opened, last; time in the stage on hover) |
| Runner | state dot, runner name | jobs running and minutes today, in mono |
| Event | the shared [state dot](state-badge.md#dot-and-word) of the event's state (OR-55; the word for screen readers only), the event in words: the task's code and title, else the conversation's title | [work facts](work-facts.md): `×N` repeats and the agent, then the age in mono, last ([OR-2](../owner-rules.md), [OR-64](../owner-rules.md)) |

**States.** *Normal*; *trouble* (the dot and the right-hand fact in `danger-fg`, [OR-56](../owner-rules.md)); *stale* (dimmed with the [offline marker](offline-marker.md)).

**Behaviour.**
* The whole row opens its item ([OR-26](../owner-rules.md)): a project in Projects, a pull request in its project's delivery facet, a runner in System, an event in its session. In wall mode nothing opens.
* Each list shows its latest five; its heading, with the count, opens the full list ([OR-30](../owner-rules.md), [OR-65](../owner-rules.md)); in wall mode the heading is plain text. A new event enters at the top without moving a row being hovered ([OR-6](../owner-rules.md)).
* Repeats aggregate ([OR-58](../owner-rules.md)): runs of one subject (a task, else a conversation) in one state are one event with `×N` and the newest time; failed and started runs also by host.
* With work facts the title keeps at least 60% of the row; facts that do not fit wrap under it ([OR-60](../owner-rules.md)).

**Board.** The task row and work facts are drawn in [screens/live.bl](../board/screens/live.bl), frame `Work facts · Live rows`; the master `Live row` in blocks.bl keeps its four earlier kinds.

**Accessibility.** Rows are list items named by their full text; a sparkline is `role="img"` named by its range.

**Do.** Keep one line per row at desktop width. **Don't.** Add row buttons ([OR-37](../owner-rules.md)).

**Used in.** [Live](../pages/live.md) side column.
