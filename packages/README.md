# Shared browser packages

## Overview

`packages/` groups the shared UI contract and canonical URL codec used by the operator application.

## Interface

- [`ui`](ui/README.md) provides shared Preact controls, patterns, registry values, formatting, visual tokens, the design book, and its build/check scripts.
- [`view-url`](view-url/README.md) parses and formats supported view URLs.

## Invariants

- `packages/ui/src/index.ts` re-exports `packages/view-url/view-url.ts`, so app consumers use one URL contract.

## Side Effects

- Package code is browser-facing; build and check side effects are isolated to the UI scripts.

## Verification

- Run: `pnpm run typecheck`
