# Choice

**Code.** `Choice` in `packages/ui`. Demo: [book](../book/index.html#draft-bar).

**Purpose.** Pick one, or several, of a few fixed options that all fit on a line: a role, a harness, a step kind, allowed tools. A longer or open set uses a searchable control instead (OR-9).

**Anatomy.** A row of small buttons, each with an optional registry icon. The picked ones have the `selected` background.

**Behaviour.** With a single value a click replaces the pick; with a list a click adds or removes the option.

**Keyboard.** `Tab` to each option, `Enter` or `Space` toggles.

**Accessibility.** A named group; each option exposes `aria-pressed`.

**Used in.** Agents, Pipelines.
