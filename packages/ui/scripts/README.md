# UI build and design-check scripts

## Overview

These Node ESM scripts derive token CSS, build the static design book, and report design-rule warnings.

## Interface

- `tokens.mjs` exports `tokens`, `tokensCss`, `PAIRS`, `UI_PAIRS`, and `contrastProblems`.
- `solid-esbuild.mjs` exports `solidEsbuildPlugin` to compile Solid 2 JSX/TSX modules via `@solidjs/compiler`.
- `build-book.mjs` bundles the book entries with esbuild + `solidEsbuildPlugin` and writes the generated book pages.
- `check-design.mjs` scans UI/app source, bundles the public UI entry, and prints the warning report used by `pnpm run check:design`.

## Invariants

- `tokensCss` derives CSS custom properties from `../tokens.json`; `contrastProblems` checks the declared pairs: text at 4.5:1, UI parts at 3:1 (UR-42).
- `check-design.mjs` is a warning report: its script text labels the design-check result separately from command execution failures.

## Side Effects

- `build-book.mjs` writes generated book files. `check-design.mjs` creates or updates `design-report/report.txt`.

## Verification

- Run: `pnpm run book`
- Run: `pnpm run check:design`
