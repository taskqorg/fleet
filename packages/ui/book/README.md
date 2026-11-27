# UI design book source

## Overview

`book/` is the interactive SolidJS 2.0 demonstration of shared controls, patterns, token values, and screen compositions. `scripts/build-book.mjs` bundles it with `solidEsbuildPlugin` into standalone design-book HTML files.

## Interface

- `book.tsx:Book` mounts the control and pattern demonstrations.
- `pages.tsx:mountScenes` mounts the generated scene catalog or an isolated scene; `open-questions.tsx:Page` renders recorded visual alternatives.
- `fixtures.ts` exports example tasks, hosts, transcript blocks, and timing constants; `nav.ts:NAV` and `NAV_FOCUS` supply demo navigation with counters without and with a focus.
- `entity-cards.tsx` holds a fixture card per entity kind and the book's `EntityCards` source (`CardsProvider`); `EntityCardDemo` is its section.
- `home-blocks.tsx:HOME_BLOCKS` and `session-blocks.tsx:SESSION_BLOCKS` are the book sections of the Home and session-view blocks, appended to `book.tsx` `SECTIONS`.
- `blocks.tsx:BLOCK_SECTIONS` holds the demos of the Live and catalogue-section blocks, appended to the book's sections.
- `focus-demo.tsx:FocusDemo` is the focus chip with local state, shared by the book and the mockups' shell header.

## Invariants

- Book and mockup components import shared parts from `../src`; fixtures are presentation data, not coordinator state.
- The book's source does not call Fleet coordinator operations.

## Side Effects

- Browser-only interactions update component state and theme/demo controls.

## Verification

- Build: `pnpm run book`
- Design warnings: `pnpm run check:design`

## Scenes

A manifest under `scenes/<area>/<surface>/<scenario>/` owns its stable scene key. It names its specification, section route, unique hash routes for the `default` and other named states, and a visible readiness landmark. The specification links the scene or its directory. Add a local `scene.tsx` default export and synthetic typed `fixture.ts`. The generated catalog discovers entries in path order. Use the optional `details` field for the one scene that owns arbitrary single-segment details on a legacy mockup route.

Compose with the public UI exports. Reuse the shared preview clock and shell. Keep option work in a private copy of the pinned source tree, with its eventual canonical scene paths. Each scene has its own source and fixture. The whole-book link keeps its existing hash routes. Shared controls and styles can affect many scene recipes.

## Capture

Run this in the private source tree with the project dependencies installed.

```sh
pnpm run preview:scene -- pages/tasks/list --state waiting --out .egm/task-preview
```

Use a fresh output directory per run. The server claims a granted loopback port atomically and serves only its random preview path. Run the generated `capture.js` in the managed tool runner with `project-browser` and Fleet artifact tools. It opens a fresh browser, verifies the recipe, landmark, fonts, viewport, theme, clock and reduced motion, captures the four viewport PNGs, checks stored PNG receipts and closes the browser. Stop the owned server when it returns. Use `--build-only` to regenerate the recipe and standalone HTML without serving.

Read the originals back through Fleet to critique them. Retain the recipe, frame hashes and critique with the task. The capture summary identifies stored originals. Fleet's sealed decision and approval check are separate delivery steps described in [Design first](../../../docs/design-system/README.md#design-first).

For the local report under managed browser execution, run `FLEET_DESIGN_MANAGED_BROWSER=1 pnpm run check:design` and use the managed tool for real page checks. The report identifies advisory shell browser checks skipped in that environment. Set `FLEET_DESIGN_REPORT_DIR` to a private directory when sharing a checkout.
