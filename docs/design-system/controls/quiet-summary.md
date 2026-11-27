# Quiet summary

**Code.** `QuietSummary` in `packages/ui`. Demo: [book](../book/index.html#quiet-summary). Board: [blocks.bl](../board/blocks.bl), master `Quiet summary`.

**Purpose.** A book example of a summary that says what happened while the person was away and who keeps watch.

**Anatomy.** A `surface` card with a `line` border and `radius-lg`, with the title "Nothing needs you" in `font-size-lg`, one line of numbers since the last visit (tasks done, pull requests merged, failures) naming the watching bot, then two [buttons](button.md): the primary one asks the watching bot "what happened?", the quiet one opens Live.

**States.** *Summary* as above; *first visit* (no last visit yet: the numbers cover today); *loading* (a skeleton of the same height).

**Behaviour.** It leaves in place when a question or a failure arrives; the group that arrives takes its space ([OR-23](../owner-rules.md)). The first button sends the question in the composer and opens that bot's session.

**Accessibility.** `role="status"`; the buttons are named by their text.

**Do.** Give numbers, not adjectives. **Don't.** Show an empty grid or an illustration.

**Used in.** The component book.
