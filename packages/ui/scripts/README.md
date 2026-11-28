# UI build and design-check scripts

## Overview

These Node ESM scripts derive token CSS, build the static design book, and report design-rule warnings.

## Interface

- `tokens.mjs` exports `tokens`, `tokensCss`, `PAIRS`, `UI_PAIRS`, and `contrastProblems`.
- `solid-esbuild.mjs` exports `solidEsbuildPlugin` to compile Solid 2 JSX/TSX modules via `@solidjs/compiler`.
- `build-book.mjs` bundles the book entries with esbuild + `solidEsbuildPlugin` and writes the generated book pages.
- `check-design.mjs` scans UI/app source, bundles the public UI entry, and prints the warning report used by `pnpm run check:design`.

- `scenes.ts` discovers and validates scene manifests with Node's standard library.
- `build-scene.mjs` writes one scene, its recipe and the [managed capture script](../book/README.md#capture). A build under `FLEET_SCENE_DIR` passes the bundle to the managed browser without binding a port. `scene-build.mjs` builds the scene and hashes its actual inputs.
- `design-sources.mjs` enforces hard source rules in both the local design report and the app candidate check.

- `design-impact.mjs` maps changed scene and shared files and preserves unmapped impact. `design-candidate.mjs` produces unprivileged recipe inputs for exact PR and merge-group heads. Unmapped impact stays unknown.
- `design-protected.mjs` checks provider candidate identity and merge-group member receipts, recomputes Git changes, and requires protected findings for unmapped paths and app parity. It runs from the protected verifier revision and takes the provider, Fleet and isolated-render adapters from its caller. It performs no check publication.
- `design-github-checks.mjs` is the issuer entry point for a separate protected service. It verifies before publishing and reads back the completed check on the exact head from the configured App. It uses `FLEET_DESIGN_GITHUB_APP_ID`, `FLEET_DESIGN_GITHUB_INSTALLATION_ID` and `FLEET_DESIGN_GITHUB_APP_KEY_FILE`. For a service named `fleet-design-verifier.service`, use the service's `github-app.pem` credential at `/run/credentials/fleet-design-verifier.service/github-app.pem`. The code does not provision the App, store the installation token or run in candidate CI.
- `design-evidence.mjs` binds stored four-frame originals to one selected recipe. Its decision and artifact inputs belong to the trusted Fleet verifier described in the [delivery contract](../../../docs/designs/fleet-delivery-pipeline.md#design-approval).

## Invariants

- `tokensCss` derives CSS custom properties from `../tokens.json`; `contrastProblems` checks the declared pairs: text at 4.5:1, UI parts at 3:1 (UR-42).
- `check-design.mjs` is a warning report: its script text labels the design-check result separately from command execution failures.

## Side Effects

- `build-book.mjs` writes generated book files. `check-design.mjs` creates or updates `design-report/report.txt`.

## Verification

- Run: `pnpm run book`
- Run: `pnpm run check:design`
