# Change row

**Code.** `ChangeRow` in `packages/ui` (`src/controls/change-row.tsx`), a list item; the caller gives the `<ul class="change-rows">`. Board: `drafts/definitions-home` in the board repository, frame `Changes · definitions with one home per kind` (1440 and 390).

**Purpose.** One definition change, or one Lab experiment, on [Library › Changes](../pages/changes.md): where the change stands, and the person's action only when it needs one.

**Anatomy.** A card on `surface` with a `line-soft` border and `radius-md`, in source order:
* the name (the row's link) with the kind quiet after it;
* the summary in `fg-muted`: what changed, or why it needs a person;
* the chip, a [state badge](state-badge.md) with words in its tone: the pull request and its stage, `Conflict`, `Failed`, `New in Fleet`, the experiment's state;
* the actions, default buttons, the recommended one primary;
* the time, last ([OR-64](work-facts.md#rules-kept-here)).

**States.** *Normal*; *needs a person* (`attention-fg` border on `attention-soft`: a conflict); *failed* (`danger-fg` border on `danger-bg`).

**Layout.** Each row is its own size container. At 40 rem and wider, one line: name and kind (two lines), summary, chip, actions, time. Narrower, it stacks: name, kind and time on the first line, then the chip, the summary and the actions; nothing scrolls sideways ([OR-66](../patterns/no-horizontal-scroll.md#rules-kept-here)).

**Behaviour.** The whole row opens its item ([OR-26](../patterns/app-shell.md#rules-kept-here)): the name is the one link, stretched over the row; buttons and tooltips stay above it and act on their own.

**Accessibility.** An `article` named by its name, kind and chip; the name is a native link.

**Do.** Give actions only to a row a person must decide. **Don't.** Add a button to a row that is on its way by itself.

**Used in.** [Changes](../pages/changes.md).
