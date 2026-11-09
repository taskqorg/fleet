# Fleet design system

This folder is the single knowledge base for Fleet's interface design. It plays the same role for design that `fleet.md` and `docs/designs/` play for architecture. Every design change starts here, and every page is built from what is recorded here.

## Files

| File | Holds | Changes when |
|---|---|---|
| [inventory.md](inventory.md) | What the interface must let people see, configure, choose and decide. Grouped by workspace, with the data each item comes from. | The architecture adds, removes or changes something a person sees or controls. |
| `principles.md` | Usability rules that every screen follows: density, scrolling, filtering, keyboard, states, errors, empty states, motion, accessibility. | The owner approves a new rule. |
| `tokens.md` (+ machine-readable tokens file) | Colour, type, spacing, radius, elevation, motion, in light and dark. | The owner approves a visual change. |
| `controls/<control>.md` | One file per control: purpose, anatomy, states, behaviour, keyboard, accessibility, do and don't, and where it is used. | A control is added or changed. |
| `patterns/<pattern>.md` | Compositions of controls for recurring jobs, for example the decision block, the result viewer or the problem row. | A pattern is added or changed. |
| `pages/<page>.md` | Each page: purpose, the inventory items it covers, the layout, and which controls and patterns it uses. | A page is added or changed. |
| `decisions.md` | Dated design decisions with the options considered and the reason. Append only. | Every owner decision. |

## Process (spec-driven design)

1. **Change the spec first.** Before a page or control changes, update the inventory, the principle, the control or the pattern it depends on, and add a `decisions.md` entry. Pages never hold rules of their own.
2. **Choices go to the owner.** When a design question has more than one reasonable answer, present up to three options with a rendered example of each, and let the owner choose. Record the choice in `decisions.md`.
3. **Build from the book.** Pages use only recorded tokens, controls and patterns. A need the book does not cover is a change to the book first (step 1).
4. **One source per rule.** A rule lives in one file; other files link to it.
5. **Check against the book.** A page review checks it against the principles and the control specs it uses, not against taste.

## Inputs

* Architecture: [fleet.md](../../fleet.md), [interfaces](../interfaces.md), [use cases](../use-cases.md), [data foundation design](../designs/fleet-data-foundation.md).
* Existing prototype screens, which are evidence of what was tried, not approved design: `packages/coordinator/ui.ts`, `project-ui.ts`, `lab-ui.ts`, `account-ui.ts`, and screenshots in `evidence/*-desktop.png` and `*-mobile.png`. The [CSGO storyboard](../prototypes/csgo-workflow.html) is an unapproved navigation study.
