# Drift row

**Code.** `DriftRow` in `packages/ui` (`src/controls/drift-row.tsx`), a list item; the caller gives the `<ul class="drift-rows">`. Board: `drafts/agent-garden` in the board repository, frames 3 (1440) and 6 (390).

**Purpose.** One setting that every future agent gets, on [Library › Changes](../pages/changes.md), Agent environment: a connection's tools, a key pool, a host tool, a sandbox group, profiles. It answers "is it in sync, and if not, what drifts".

**Anatomy.** Flat rows in a panel, separated by `line-soft`, in source order:
* the name, a native link when the row opens something (a host, a profile);
* the detail in `fg-muted`: what the setting gives (`19 granted · all 19 that code and profiles use`);
* the state, a [state badge](state-badge.md) with words: `In sync`, `Ready`, `Applied on 3 hosts` (success), or the drift (`3 missing`, `No failover`, `Missing on pds18`, attention; `No active key`, danger). The badge tooltip names what drifts and the fix;
* the fact, last ([OR-64](work-facts.md#rules-kept-here)): the last change (`tools changed 26 Nov · alex`, exact time and actor on hover) or the release that applies it.

**Layout.** The list is the size container. At 40 rem and wider, one line: name, detail, state, fact. Narrower, each row stacks: name and state on the first line, then the detail, then the fact; long values wrap and nothing scrolls sideways ([OR-66](../patterns/no-horizontal-scroll.md#rules-kept-here)).

**Behaviour.** A row with a link opens it from anywhere on the row ([OR-26](../patterns/app-shell.md#rules-kept-here)): the name's link is stretched over the row, and the state's tooltip stays above it.

**Accessibility.** A list item named by its name and state; the name is a native link when the row opens.

**Do.** Say the drift in words on the badge. **Don't.** Add buttons: a fix is an operation an agent or a person runs (the badge tooltip names it).

**Used in.** [Changes](../pages/changes.md).
