# Fleet design system

This folder is the single knowledge base for Fleet's interface design. It plays the same role for design that `fleet.md` and `docs/designs/` play for architecture. Every design change starts here, and every page is built from what is recorded here.

## Start here

1. [owner-rules.md](owner-rules.md): the owner's rules. Read its [principles card](owner-rules.md#principles-card) first in every design or UI session.
2. `packages/ui`: the shared parts (tokens, icons, formatter, virtual list, controls). Use them; do not write local variants.
3. [book/index.html](book/index.html): the live demo of `packages/ui`. [pages/mockups.html](pages/mockups.html): page mockups built from it. Both are built by `pnpm run book` and not committed (parallel UI changes never conflict on them); CI uploads them in the `design-report` artifact; `pnpm run check:design` prints the warning report; its checks are listed in `packages/ui/scripts/check-design.mjs`. CI posts the report summary on every pull request.

## Design first

[OR-62 and OR-69](owner-rules.md#rules) define the approved target. The active procedure changes when Fleet qualifies the owner decision and production admission path, the trusted merge gate, and the host and decision channels. See the [delivery contract](../designs/fleet-delivery-pipeline.md#design-approval). Until then use the [prior board procedure](https://github.com/taskqorg/fleet/blob/912acb1992aa93324943e866a9f5a37216efa49d/docs/design-system/README.md#design-board) for production UI work.

1. **Walk.** Run `node scripts/ui-context.ts <page>`. Use the managed browser to walk the live page and its neighbours in both widths and themes. Record the release, routes, observed data, and each code, data or missing-field difference. Label a synthetic baseline as recreated and cite the live observations.
2. **Design.** Build one to three comparable options in task-private scratch from the exported controls, tokens and patterns. Use the same shell and synthetic fixture states across options. Include the states, entry paths and neighbours affected by the change. Keep production changes outside the design execution.
3. **Capture and critique.** For standalone scenes, run `pnpm run preview:scene -- <scene-key> --out <private-dir>` and follow the managed [capture instructions](../../packages/ui/book/README.md#capture). Inspect each original PNG. Record hierarchy, alignment, spacing, state visibility, truncation and touch or keyboard access by frame. Fix findings and recapture. Run `pnpm run check:design`. Retain original images, recipe and critique with the task.
4. **Decide.** With qualifying originals, Fleet seals the packet and presents an addressed, typed question covering the compared options. Only the owner's explicit selection of one sealed recipe permits production implementation. Rework makes a new packet. Link the selected packet and decision in the PR.
5. **Implement and verify.** Transfer the selected scene to its canonical path and update its spec. Compare normal app entry points with the approved references at both sizes and themes. Send new visible differences through design. Verify the actual PR and queue trees, then repeat the live walk after deployment and attach the comparisons.

Scene discovery uses `packages/ui/book/scenes/<area>/<surface>/<scenario>/scene.json`. The owning specification links the scene or its directory. Each scene owns its fixture, composition and states. Catalogs, draft options and PNGs stay in run-private output or Fleet retained storage. Shared controls, styles and tokens still require coordinated ownership when multiple scenes use them.

For whole-viewport approval, the originals must reproduce the actual application shell on the pinned normal route and state with deterministic coordinator operation fixtures and capture settings bound to the selected source by a sealable recipe. The book builder emits an isolated-scene v1 recipe. The [protected verifier](../../deploy/design-verifier/README.md#register-a-selected-route-and-impact-mapping) supplies separate route fixtures for a selected candidate and compares its frames after selection. Fleet currently has no supported path to capture and seal whole-viewport owner originals. Retain the proposed scope and the missing capture or sealing prerequisite on the task and leave the choice pending. A demo shell or a separate screenshot cannot qualify as a whole-viewport original. Take any proposed change to the comparison contract to the owner.

## Design board

[Legacy canvases](board/) and their [frame index](board/index.json) preserve the Brilliant design history. Existing links still identify their original frames. The board freezes at cutover. New approval references then point to the immutable Fleet packet and corresponding scene sources.

### Board layout

The archived geometry checks and frame conventions remain in the [prior board layout rules](https://github.com/taskqorg/fleet/blob/912acb1992aa93324943e866a9f5a37216efa49d/docs/design-system/README.md#board-layout). They describe the Brilliant canvases and their existing lint script.

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

The design and docs reports remain advisory in CI. The Design-First delivery gate uses its own trusted check. Local `check:design` fails on invalid scene sources, invalid raw controls or a failed build.

* **One command per page.** `node scripts/ui-context.ts <page>` prints what to read before designing or building that page: first the owner's [principles card](owner-rules.md#principles-card), then its spec, its scenes and historical board references, the blocks, controls and patterns it links, the text of every judgment rule they cite by ID, and one line per spec or check that keeps the other rules they cite. A task brief for a page names this command instead of the whole rule list. Without an argument it lists the pages.
* **A page spec cites, never restates.** It links its blocks in a `## Blocks` section (a link with an anchor, such as `decision-block.md#card`, names one section of a control spec) and its controls and patterns where it uses them, and names rules by ID: `OR-<n>` in [owner-rules.md](owner-rules.md), `UR-<n>` in `principles.md`. What the spec does not link or cite, the agent is not told.
* **Rules review.** Each week the orchestrating session lists, for the owner, the rules that are still *Target*, rules with no check, rules that conflict, and rules no page cites; the owner keeps, rewrites or removes each one.

## Process (spec-driven design)

1. **Change the spec first.** Before a page or control changes, update the inventory, the principle, the control or the pattern it depends on, and add a `decisions.md` entry. Pages never hold rules of their own.
2. **Choices go to the owner.** When a design question has more than one reasonable answer, follow [Design first](#design-first) and let the owner choose a sealed preview. Record the choice in `decisions.md`.
3. **Build from the book.** Pages use only recorded tokens, controls and patterns. A need the book does not cover is a change to the book first (step 1).
4. **One source per rule.** A rule lives in one file; other files link to it. A rule that one control or pattern carries lives in that spec's "Rules kept here"; a rule a script enforces is named with its check; only rules that need judgment stay in [owner-rules.md](owner-rules.md) and [principles.md](principles.md), whose tables say where every moved id lives.
5. **Check against the book.** A page review checks it against the principles and the control specs it uses, not against taste.
6. **A rule comes with its check.** A new owner rule names how it is checked ([owner rules](owner-rules.md#how-each-rule-is-checked)): a check on the rendered page where one is possible, otherwise the reviewer. Layout rules are checked on the rendered page, never by searching source text.
7. **A target looks at what exists.** Before drawing a Target page, read the current page on the app and its rows in the [product map](product-map.md). Decide for each capability: keep it, change it, or drop it; much of today's build is clutter and should go, while working tools people rely on (grouping, sorting and saved views chosen by each person) are not lost by accident. The pull request lists what it drops and why, so the owner sees it.

## Inputs

* Architecture: [fleet.md](../../fleet.md), [interface workflows](../designs/fleet-operator-app.md#interface-workflows), [use cases](../use-cases.md), [data foundation design](../designs/fleet-data-foundation.md).
* Removed prototype screens, which are evidence of what was tried, not approved design: the legacy pages listed in [retirement coverage](../designs/fleet-operator-app.md#8-coexistence-and-retirement) (in git history before Lab Stage 0), and screenshots in `evidence/*-desktop.png` and `*-mobile.png`. The CSGO storyboard (in git history) was an unapproved navigation study.

[Projects management](pages/projects.md) uses the shared project definition operations.

