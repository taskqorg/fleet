# Fleet design system

This folder is the single knowledge base for Fleet's interface design. It plays the same role for design that `fleet.md` and `docs/designs/` play for architecture. Every design change starts here, and every page is built from what is recorded here.

## Start here

1. [owner-rules.md](owner-rules.md): the owner's rules. Read first in every design or UI session.
2. `packages/ui`: the shared parts (tokens, icons, formatter, virtual list, controls). Use them; do not write local variants.
3. [book/index.html](book/index.html): the live demo of `packages/ui`. [pages/mockups.html](pages/mockups.html): page mockups built from it. Both are built by `pnpm run book` and not committed (parallel UI changes never conflict on them); CI uploads them in the `design-report` artifact; `pnpm run check:design` prints the warning report; its checks are listed in `packages/ui/scripts/check-design.mjs`. CI posts the report summary on every pull request.

## Design board

The board is the visual source of the interface: a [Brilliant](https://brilliant.design) project kept in [board/](board/) as plain-text canvases (`.bl`). Agents read and write it through the Brilliant MCP tools (`init` with the absolute path of `docs/design-system/board` in the checkout, then `read`, `edit` and `export`); the owner opens the same folder in the Brilliant app. The board's style file is generated from `packages/ui/tokens.json` ([tokens](tokens.md#design-board)); Brilliant's caches and thumbnails stay out of git (root `.gitignore`).

| Canvas | Holds | Changes when |
|---|---|---|
| [styles](board/styles.bl) | Every Fleet colour in both themes and the type scale, drawn from the tokens | `tokens.json` changes (`pnpm run board:tokens`) |
| [rules-v2](board/rules-v2.bl) | The principles and rules as one picture; the text of record is [owner-rules.md](owner-rules.md) | An owner rule changes |
| [ia](board/ia.bl) | The sections, the question each answers, the journeys | The [information architecture](../designs/fleet-ia-and-journeys.md) changes |
| [blocks](board/blocks.bl) | Every block of Home, Live and the session view as a component with all its states ([block list](controls/README.md#blocks-of-home-live-and-the-session-view)) | A block's spec changes; screens use its instances, so the change reaches them |
| `screens/<page>` ([home](board/screens/home.bl), [live](board/screens/live.bl), [session](board/screens/session.bl), [conversations](board/screens/conversations.bl), [projects](board/screens/projects.bl), [lab](board/screens/lab.bl), [library](board/screens/library.bl), [system](board/screens/system.bl)) | The approved states of one page: a row at 1440 px, then phone 390 px, then the parts it adds | The owner approves a state; the [page spec](pages/README.md) links its canvas |
| [screens/flow](board/screens/flow.bl) | The journeys J1–J5, answering a question from Home and waking a bot as click paths over instances of the screens | A screen or a journey changes; screens are components, so their changes reach it |

How a screen moves through the board:

1. **Audit.** Capture what runs and mark the problems. Screenshots are working papers: they go into the pull request, not the tree.
2. **Options.** When a question has more than one reasonable answer, draw up to three options on a local Brilliant board outside the repository. Options and audits are working papers.
3. **Decision.** The owner picks. Record the choice in [decisions.md](decisions.md), the rules in [owner-rules.md](owner-rules.md) and the page in `pages/<page>.md`.
4. **Screens.** Draw the approved states on `screens/<page>.bl` in this board, in the same pull request as the page spec.
5. **Build.** Build from `packages/ui` and the book (OR-13). Compare the app at 1440 and 390 px with the canvas, and run `pnpm run check:design`.

Board drawings use Fleet's tokens through the board's style file ([tokens](tokens.md#design-board)): `$color.*` roles and `$fleet.*` colours, never bare colours. The canvases drawn before the tokens were imported (`rules-v2`, `ia`) still hold bare colours copied from `tokens.json`; they move to tokens when next redrawn. A control the board needs and `packages/ui` lacks is a change to the book first (process step 3 below).

## Files

| File | Holds | Changes when |
|---|---|---|
| [inventory.md](inventory.md) | What the interface must let people see, configure, choose and decide. Grouped by workspace, with the data each item comes from. | The architecture adds, removes or changes something a person sees or controls. |
| [owner-rules.md](owner-rules.md) | The owner's rules (the only list of them). | The owner changes a rule. |
| [formats.md](formats.md) | One look per kind of value, the copy format, and what each screen answers. | A value kind or a screen's purpose is added or changed. |
| [principles.md](principles.md) | Usability rules that every screen follows, each with its check: density, scrolling, filtering, keyboard, states, errors, motion, accessibility, glance, conversation, details on demand, wayfinding, feedback, recovery, consistency, phone. | The owner approves a new rule. |
| [product-map.md](product-map.md) | Every capability and app route: its rail section, screen, built state, whether it works on prod, and the operations it uses. | A route or capability is added, moved or fixed, or a prod check is run. |
| `tokens.md` + `packages/ui/tokens.json` | Colour, type, spacing, radius, elevation, motion, in light and dark. | The owner approves a visual change. |
| `controls/<control>.md` | One file per control: purpose, anatomy, states, behaviour, keyboard, accessibility, do and don't, and where it is used. | A control is added or changed. |
| `patterns/<pattern>.md` | Compositions of controls for recurring jobs, including [navigation and columns](patterns/navigation-and-columns.md), the decision block, the result viewer or the problem row. | A pattern is added or changed. |
| `pages/<page>.md` | Each page: purpose, the inventory items it covers, the layout, and which controls and patterns it uses. | A page is added or changed. |
| `decisions.md` | Dated design decisions with the options considered and the reason. Append only; `.gitattributes` merges it with `union`, so two pull requests that each append an entry rebase without a conflict (the blank line between the two new entries can drop; the page still renders, and the next edit adds it back). | Every owner decision. |

## Context for a UI task

The process helps, recommends and shows; it never blocks. The design and docs reports are warnings (the design report fails only on raw controls in app views); no new check is added that fails a build or gates a merge. Consistency comes from what each agent is given: exactly what its task needs, and no more.

* **One command per page.** `node scripts/ui-context.ts <page>` prints what to read before designing or building that page: its spec, its board canvas, the blocks, controls and patterns it links, the three principles and the text of every rule they cite by ID. A task brief for a page names this command instead of the whole rule list. Without an argument it lists the pages.
* **A page spec cites, never restates.** It links its blocks in a `## Blocks` section (a link with an anchor, such as `decision-block.md#card`, names one section of a control spec) and its controls and patterns where it uses them, and names rules by ID: `OR-<n>` in [owner-rules.md](owner-rules.md), `UR-<n>` in `principles.md`. What the spec does not link or cite, the agent is not told.
* **Rules review.** Each week the orchestrating session lists, for the owner, the rules that are still *Target*, rules with no check, rules that conflict, and rules no page cites; the owner keeps, rewrites or removes each one.

## Process (spec-driven design)

1. **Change the spec first.** Before a page or control changes, update the inventory, the principle, the control or the pattern it depends on, and add a `decisions.md` entry. Pages never hold rules of their own.
2. **Choices go to the owner.** When a design question has more than one reasonable answer, present up to three options with a rendered example of each, and let the owner choose. Record the choice in `decisions.md`.
3. **Build from the book.** Pages use only recorded tokens, controls and patterns. A need the book does not cover is a change to the book first (step 1).
4. **One source per rule.** A rule lives in one file; other files link to it.
5. **Check against the book.** A page review checks it against the principles and the control specs it uses, not against taste.
6. **A rule comes with its check.** A new owner rule names how it is checked ([owner rules](owner-rules.md#how-each-rule-is-checked)): a check on the rendered page where one is possible, otherwise the reviewer. Layout rules are checked on the rendered page, never by searching source text.
7. **A target keeps what works.** A Target page spec or canvas keeps every capability the page has today: grouping, sorting, saved views, filters, pins, selection, row menus and the rest of its rows in the [product map](product-map.md). Read the current page on the app and its map rows before drawing. A capability leaves only by an owner decision in `decisions.md`; the pull request names each one it drops.

## Inputs

* Architecture: [fleet.md](../../fleet.md), [interface workflows](../designs/fleet-operator-app.md#interface-workflows), [use cases](../use-cases.md), [data foundation design](../designs/fleet-data-foundation.md).
* Removed prototype screens, which are evidence of what was tried, not approved design: the legacy pages listed in [retirement coverage](../designs/fleet-operator-app.md#8-coexistence-and-retirement) (in git history before Lab Stage 0), and screenshots in `evidence/*-desktop.png` and `*-mobile.png`. The CSGO storyboard (in git history) was an unapproved navigation study.

[Projects management](pages/projects.md) uses the shared project definition operations.

