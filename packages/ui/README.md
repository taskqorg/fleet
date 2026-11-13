# Shared UI

## Overview

`packages/ui` is Fleet's shared Preact UI layer. App screens and the design book consume its exported controls, patterns, registry, formatting, URL helpers, and tokens.

## Interface

- [`src`](src/README.md) is the public source package; `src/index.ts` is its export barrel.
- [`book`](book/README.md) is the interactive design-book source.
- [`scripts`](scripts/README.md) builds the book, derives token CSS, and emits design warnings.
- `tokens.json` is the token input for `scripts/tokens.mjs`; `icons.json` supplies the icon registry used by `src/registry.ts`.

## Invariants

- `src/index.ts` is the shared import surface and re-exports the canonical view URL package.
- `build-book.mjs` and `check-design.mjs` bundle with the project-installed dependencies; neither adds a runtime UI dependency.

## Side Effects

- The scripts write generated design-book/report outputs outside this directory as configured by their source.

## Verification

- Run: `pnpm run check:design`
- Build the book: `pnpm run book`
- Type-check: `pnpm run typecheck`
