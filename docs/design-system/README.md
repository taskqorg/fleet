# Fleet design system

This folder is the single knowledge base for Fleet's interface design. It plays the same role for design that `fleet.md` and `docs/designs/` play for architecture. Every design change starts here, and every page is built from what is recorded here.

## Start here

1. [owner-rules.md](owner-rules.md): the owner's rules. Read its [principles card](owner-rules.md#principles-card) first in every design or UI session.
2. `packages/ui`: the shared parts (tokens, icons, formatter, virtual list, controls). Use them; do not write local variants.
3. [book/index.html](book/index.html): the live demo of `packages/ui`. [pages/mockups.html](pages/mockups.html): page mockups built from it. Both are built by `pnpm run book` and not committed (parallel UI changes never conflict on them); CI uploads them in the `design-report` artifact; `pnpm run check:design` prints the warning report; its checks are listed in `packages/ui/scripts/check-design.mjs`. CI posts the report summary on every pull request.

## Design board

The board is the visual source of the interface: a [Brilliant](https://brilliant.design) project kept in [board/](board/) as plain-text canvases (`.bl`). The board's style file is generated from `packages/ui/tokens.json` ([tokens](tokens.md#design-board)); Brilliant's caches and thumbnails stay out of git (root `.gitignore`).

The board has one home, like a shared design file: the persistent board worktree `~/Projects/fleet-board`, which the owner keeps open in the Brilliant app. Every Brilliant MCP `init` on a new path registers a new project in that app and shows a notification, so:

* **Reading the board** (building, reviewing, comparing a page with its canvas): read the `.bl` files as plain text with file tools, in any checkout. Never call the Brilliant MCP tools from a task worktree.
* **Drawing on the board** (design first, [OR-62](owner-rules.md)): work in `~/Projects/fleet-board`, which stays at `origin/main` and is never committed in. Bring it up to date first (`git -C ~/Projects/fleet-board pull --ff-only origin main`), then `init` with its `docs/design-system/board` and use `read`, `edit` and `export`. Several design sessions may draw at once, each on its own canvases (each `init` has its own cursor), and the owner watches all of them live in the one open project. Options go on canvases under `drafts/` (git ignores them), named after the task. When the owner approves, copy the changed approved canvases into your task branch and commit them there with the page spec; when the pull request merges, delete your drafts; if `pull --ff-only` then stops on a canvas you changed, `git -C ~/Projects/fleet-board checkout -- <that canvas>` and pull again (the merged version is the same drawing).
* **A rendered picture** for a pull request or a comparison comes from `export` in that worktree; build sessions ask the design session (or the orchestrator) for it and never render the board themselves.

| Canvas | Holds | Changes when |
|---|---|---|
| [styles](board/styles.bl) | Foundations: every Fleet colour in both themes, the type scale, spacing, radius and sizes, drawn from the tokens; the board draws Inter for the product's system-ui, so a board-versus-prod check compares layout and colour, not glyph widths | `tokens.json` changes (`pnpm run board:tokens`) |
| [components](board/components.bl) | One master per book section (the controls of `packages/ui`), generated from the built book with every colour bound to a token by its dark and light pair, plus a light-theme row of the core masters. [code-map.json](board/code-map.json) names each master's id, exports, spec and book anchor | The book changes: `pnpm run book`, then `node packages/ui/scripts/board-extract.mjs <json>` and `python3 packages/ui/scripts/board-components.py <json> <dir>`, and send the batches from the board home |
| [patterns](board/patterns.bl) | Each [pattern](patterns/README.md) and the master that shows it | A pattern spec changes |
| [ia](board/ia.bl) | The sections, the question each answers, the journeys | The [information architecture](../designs/fleet-ia-and-journeys.md) changes |
| [blocks](board/blocks.bl) | Every block of Home, Live and the session view as a component with all its states ([block list](controls/README.md#blocks-of-home-live-and-the-session-view)) | A block's spec changes; screens use its instances, so the change reaches them |
| `screens/<page>` ([home](board/screens/home.bl), [people](board/screens/people.bl), [live](board/screens/live.bl), [session](board/screens/session.bl), [conversations](board/screens/conversations.bl), [projects](board/screens/projects.bl), [lab](board/screens/lab.bl), [library](board/screens/library.bl), [system](board/screens/system.bl)) | The approved states of one page: a row at 1440 px, then phone 390 px, then the parts it adds | The owner approves a state; the [page spec](pages/README.md) links its canvas |
| [infinite-lists](board/infinite-lists.bl) | The one list pattern (OR-65): loading row, end of list, error with Retry, card preview, a list that scrolls inside, at 1440 and 390 px | The [infinite list](patterns/infinite-list.md) pattern changes |
| [screens/host-updates](board/screens/host-updates.bl) | The approved frames of the host updates design: 1A Changes grouped by kind, 2A a host's update history, at 1440 and 390 px | The [Changes](pages/changes.md) component groups or the [Hosts](pages/hosts.md) Updates change |
| [screens/shell](board/screens/shell.bl) | The rail's user block: update strip, avatar, version and its overview in every state, expanded, collapsed and phone | The [nav rail](controls/nav-rail.md) user block or version changes |
| [screens/flow](board/screens/flow.bl) | The journeys J1–J5, answering a question from Home and waking a bot as click paths over instances of the screens | A screen or a journey changes; screens are components, so their changes reach it |

[index.json](board/index.json) lists every committed frame (id, canvas, page, width, theme, status); `pnpm run board:index` regenerates it and the code map. A task names its frame by id; an agent finds it there without opening a canvas. In [code-map.json](board/code-map.json), `blocks` lists each master the screens are drawn with: the `packages/ui` export it stands for, the generated masters that render it, and every screen frame that uses it (`usedIn`), so a build agent goes from a frame's `inst(...)` rows straight to the import.

How a screen moves through the board:

1. **Audit.** Capture what runs and mark the problems. Screenshots are working papers: they go into the pull request, not the tree.
2. **Options.** Draw the change on the board before any code ([OR-62](owner-rules.md)). When a question has more than one reasonable answer, draw up to three options. Options go on `drafts/` canvases in the board home; they and audits are working papers. Before drawing, walk the live pages you change and their neighbours on real data (dark, 1440 and 390 px: long names, many items, empty states), note where the live app differs from the approved canvases, and draw from what real data looks like (owner, 2026-11-26).
3. **Decision.** The owner picks on the board. Record the choice in [decisions.md](decisions.md), the rules in [owner-rules.md](owner-rules.md) and the page in `pages/<page>.md`.
4. **Screens.** Draw the approved states on `screens/<page>.bl` (and blocks on `blocks.bl`), and commit them in the same pull request as the page spec, before or with the code.
5. **Build.** Build from `packages/ui` and the book (OR-13). Compare the app at 1440 and 390 px with the canvas, and run `pnpm run check:design`. After each deploy, walk the same live pages and their neighbours again on real data and compare them with the canvas; the comparison goes into the pull request description (owner, 2026-11-26).

Board drawings use Fleet's tokens through the board's style file ([tokens](tokens.md#design-board)): `$color.*` roles and `$fleet.*` colours, never bare colours. The canvas drawn before the tokens were imported (`ia`) still holds bare colours copied from `tokens.json`; it moves to tokens when next redrawn. A control the board needs and `packages/ui` lacks is a change to the book first (process step 3 below).

## Files

| File | Holds | Changes when |
|---|---|---|
| [inventory.md](inventory.md) | What the interface must let people see, configure, choose and decide. Grouped by workspace, with the data each item comes from. | The architecture adds, removes or changes something a person sees or controls. |
| [owner-rules.md](owner-rules.md) | The owner's principles card, the owner rules that need judgment, and where every other owner rule lives now (a control or pattern spec, or a check), by id. | The owner changes a rule. |
| [formats.md](formats.md) | One look per kind of value, the copy format, and what each screen answers. | A value kind or a screen's purpose is added or changed. |
| [principles.md](principles.md) | Usability rules that need judgment, each with its check: density, scrolling, filtering, keyboard, states, errors, motion, accessibility, glance, conversation, details on demand, wayfinding, feedback, recovery, consistency, phone; and where every other one lives now. | The owner approves a new rule. |
| [product-map.md](product-map.md) | Every capability and app route: its rail section, screen, built state, whether it works on prod, and the operations it uses. | A route or capability is added, moved or fixed, or a prod check is run. |
| `tokens.md` + `packages/ui/tokens.json` | Colour, type, spacing, radius, elevation, motion, in light and dark. | The owner approves a visual change. |
| `controls/<control>.md` | One file per control: purpose, anatomy, states, behaviour, keyboard, accessibility, do and don't, where it is used, and under "Rules kept here" the owner and usability rules it carries in full. | A control is added or changed. |
| `patterns/<pattern>.md` | Compositions of controls for recurring jobs, including [navigation and columns](patterns/navigation-and-columns.md), the decision block, the result viewer or the problem row. | A pattern is added or changed. |
| `pages/<page>.md` | Each page: purpose, the inventory items it covers, the layout, and which controls and patterns it uses. | A page is added or changed. |
| `decisions.md` | Dated design decisions with the options considered and the reason. Append only; `.gitattributes` merges it with `union`, so two pull requests that each append an entry rebase without a conflict (the blank line between the two new entries can drop; the page still renders, and the next edit adds it back). | Every owner decision. |

## Context for a UI task

The process helps, recommends and shows; it never blocks. The design and docs reports are warnings (the design report fails only on raw controls in app views); no new check is added that fails a build or gates a merge. Consistency comes from what each agent is given: exactly what its task needs, and no more.

* **One command per page.** `node scripts/ui-context.ts <page>` prints what to read before designing or building that page: first the owner's [principles card](owner-rules.md#principles-card), then its spec, its board canvas, the blocks, controls and patterns it links, the text of every judgment rule they cite by ID, and one line per spec or check that keeps the other rules they cite. A task brief for a page names this command instead of the whole rule list. Without an argument it lists the pages.
* **A page spec cites, never restates.** It links its blocks in a `## Blocks` section (a link with an anchor, such as `decision-block.md#card`, names one section of a control spec) and its controls and patterns where it uses them, and names rules by ID: `OR-<n>` in [owner-rules.md](owner-rules.md), `UR-<n>` in `principles.md`. What the spec does not link or cite, the agent is not told.
* **Rules review.** Each week the orchestrating session lists, for the owner, the rules that are still *Target*, rules with no check, rules that conflict, and rules no page cites; the owner keeps, rewrites or removes each one.

## Process (spec-driven design)

1. **Change the spec first.** Before a page or control changes, update the inventory, the principle, the control or the pattern it depends on, and add a `decisions.md` entry. Pages never hold rules of their own.
2. **Choices go to the owner.** When a design question has more than one reasonable answer, present up to three options with a rendered example of each, and let the owner choose. Record the choice in `decisions.md`.
3. **Build from the book.** Pages use only recorded tokens, controls and patterns. A need the book does not cover is a change to the book first (step 1).
4. **One source per rule.** A rule lives in one file; other files link to it. A rule that one control or pattern carries lives in that spec's "Rules kept here"; a rule a script enforces is named with its check; only rules that need judgment stay in [owner-rules.md](owner-rules.md) and [principles.md](principles.md), whose tables say where every moved id lives.
5. **Check against the book.** A page review checks it against the principles and the control specs it uses, not against taste.
6. **A rule comes with its check.** A new owner rule names how it is checked ([owner rules](owner-rules.md#how-each-rule-is-checked)): a check on the rendered page where one is possible, otherwise the reviewer. Layout rules are checked on the rendered page, never by searching source text.
7. **A target looks at what exists.** Before drawing a Target page, read the current page on the app and its rows in the [product map](product-map.md). Decide for each capability: keep it, change it, or drop it; much of today's build is clutter and should go, while working tools people rely on (grouping, sorting and saved views chosen by each person) are not lost by accident. The pull request lists what it drops and why, so the owner sees it.

## Inputs

* Architecture: [fleet.md](../../fleet.md), [interface workflows](../designs/fleet-operator-app.md#interface-workflows), [use cases](../use-cases.md), [data foundation design](../designs/fleet-data-foundation.md).
* Removed prototype screens, which are evidence of what was tried, not approved design: the legacy pages listed in [retirement coverage](../designs/fleet-operator-app.md#8-coexistence-and-retirement) (in git history before Lab Stage 0), and screenshots in `evidence/*-desktop.png` and `*-mobile.png`. The CSGO storyboard (in git history) was an unapproved navigation study.

[Projects management](pages/projects.md) uses the shared project definition operations.

