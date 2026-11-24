# Live row

**Code.** `LiveRow` in `packages/ui` (`src/controls/live-row.tsx`), a list item; the caller gives the `<ul>`. Demo: [book](../book/index.html#live-row). Board: [blocks.bl](../board/blocks.bl), master `Live row`.

**Purpose.** One compact row of the side column on [Live](../pages/live.md): a project now, a pull request in delivery, a CI runner, or a recent event.

**Anatomy.** A row on `surface` with a `line-soft` separator, height `size-row`, state first ([OR-22](../owner-rules.md)). One variant per kind:

| Kind | Left | Right |
|---|---|---|
| Project | name; under it running, queued and done counts in their tones ([OR-29](../owner-rules.md)) | a sparkline of runs |
| Delivery | state dot, pull request [entity code](entity-card.md) and title | the [stage chip](stage-chip.md) |
| Runner | state dot, runner name | jobs running and minutes today, in mono |
| Event | age in mono ([OR-2](../owner-rules.md)), state dot, the event in words | nothing |

**States.** *Normal*; *trouble* (the dot and the right-hand fact in `danger-fg`, [OR-56](../owner-rules.md)); *stale* (dimmed with the [offline marker](offline-marker.md)).

**Behaviour.**
* The whole row opens its item ([OR-26](../owner-rules.md)): a project in Projects, a pull request in its project's delivery facet, a runner in System, an event in its session. In wall mode nothing opens.
* The event list shows the latest five ([OR-30](../owner-rules.md)); a new event enters at the top without moving a row being hovered ([OR-6](../owner-rules.md)). Repeats aggregate ([OR-58](../owner-rules.md)).

**Accessibility.** Rows are list items named by their full text; a sparkline is `role="img"` named by its range.

**Do.** Keep one line per row at desktop width. **Don't.** Add row buttons ([OR-37](../owner-rules.md)).

**Used in.** [Live](../pages/live.md) side column.
