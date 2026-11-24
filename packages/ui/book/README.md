# UI design book source

## Overview

`book/` is the interactive SolidJS 2.0 demonstration of shared controls, patterns, token values, and screen compositions. `scripts/build-book.mjs` bundles it with `solidEsbuildPlugin` into standalone design-book HTML files.

## Interface

- `book.tsx:Book` mounts the control and pattern demonstrations.
- `pages.tsx:App` renders page mockups; `open-questions.tsx:Page` renders recorded visual alternatives.
- `fixtures.ts` exports example tasks, hosts, transcript blocks, and timing constants; `nav.ts:NAV` and `NAV_FOCUS` supply demo navigation with counters without and with a focus.
- `entity-cards.tsx` holds a fixture card per entity kind and the book's `EntityCards` source (`CardsProvider`); `EntityCardDemo` is its section.
- `home-blocks.tsx:HOME_BLOCKS` and `session-blocks.tsx:SESSION_BLOCKS` are the book sections of the Home and session-view blocks, appended to `book.tsx` `SECTIONS`.
- `focus-demo.tsx:FocusDemo` is the focus chip with local state, shared by the book and the mockups' shell header.

## Invariants

- Book and mockup components import shared parts from `../src`; fixtures are presentation data, not coordinator state.
- The book's source does not call Fleet coordinator operations.

## Side Effects

- Browser-only interactions update component state and theme/demo controls.

## Verification

- Build: `pnpm run book`
- Design warnings: `pnpm run check:design`
