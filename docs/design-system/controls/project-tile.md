# Project tile

**Code.** `ProjectTile`, `identityColor`, `setProjectIdentities`, `IdentityPicker` in `packages/ui` (`controls/identity.tsx`, styles `controls.css`). Colours: tokens `id-<colour>` ([tokens](../tokens.md)); the colour and pattern names: `contracts/vocabulary.json` `identity`. Board: [screens/projects.bl](../board/screens/projects.bl), frame "B · chosen (owner 2026-11-27)".

**Purpose.** A project is recognised at a glance by its code in its own colour, in every place that names it (owner pick B, 27 Nov 2026).

**Anatomy.** A square with a hairline border on the page background: the project's code in mono, bold, in its identity colour, over a bar in the same colour along the bottom edge, solid or hatched. Sizes: `xs` 16 (the project reference, [entity card](entity-card.md)), `sm` 20 (Summary lines, the colour picker, Setup), `md` 32 (`size-row`, a Projects list row), `lg` 40 (`size-avatar-lg`, the project head).

**Colour.** Settings `color` names one of eight identity colours (rose, violet, sky, teal, lime, sand, coral, slate) and `pattern` the bar (`solid`, the default, or `hatch`). Absent means automatic: when a project (or a bot) is created it is given the least-used colour of its kind, stored once (`identity_colour`) and never recomputed, so no two share a colour while one is free (owner, 27 Nov 2026); `project_list` returns it as `autoColor`. Before the first read the tile falls back to a colour picked from the code. The owner or an agent changes them in Setup (About, pencil, Colour) or with `project_update`; a value outside the vocabulary is refused (400).

**States.** None: the tile is identity, never state ([OR-72](#rules-kept-here)). Readiness is a separate fact on the row ([fact count](fact-count.md), `setup`).

**Accessibility.** Decorative next to the project's name; with `label` it is `role="img"` named by it. Every identity colour passes 4.5:1 on `bg` and `surface` in both themes (`tokens.mjs` contrast pairs).

**Do.** Use the tile wherever a project is shown with its name or as a reference. **Don't.** Colour the tile by state, use identity colours for anything but a project's or a bot's identity, or draw a hand-made code chip.

**Used in.** Projects list rows, the Projects Summary, the project head, Setup › About, the project reference (`EntityCode`, kind project). *Target:* bots take the same rule (their letter avatar in the bot's identity colour, a picture avatar with a ring in it, a project PM in its project's colour), and the rail's pinned projects, when the rail pins projects.

## Rules kept here

* **OR-72 Identity colour is not state colour.** A project (and, *Target*, a bot) has an identity colour: one of eight muted hues, chosen in its settings, else derived from its code, with a solid or hatched bar. It sits only on the project's tile (and the bot's avatar), so a trained eye finds a project by colour. It never says anything about state: state keeps its own glyph and tone beside it. This is the one exception to D19's "no hue of its own" (owner, 27 Nov 2026).
