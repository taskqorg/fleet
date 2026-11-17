# Operator app

## Overview

`app/` builds Fleet's Preact operator application into `dist/app`. The coordinator serves its routes; this package does not define a separate API.

## Interface

- [`build.mjs`](build.mjs) bundles [`src/main.tsx`](src/main.tsx), emits hashed assets with debug IDs, external source maps (`dist/app/sourcemaps`), per-workspace web manifests, `screens.json`, `sw.js`, and a size report.
- [`src`](src/README.md) contains the application shell, typed operation client, live-state store, and route views.
- `tsconfig.json` type-checks the app, UI package, URL package, and UI tests with Preact JSX.

## Invariants

- `build.mjs` removes and recreates only `dist/app`; its service worker never caches `/api/` requests.
- Source maps are written to `dist/app/sourcemaps`, which the coordinator never serves (it serves `assets/` and named entry files) and the service worker never caches; every script carries a debug ID derived from its content, so a rebuild of one commit gives the same IDs ([error tracking](../docs/designs/fleet-error-tracking.md#6-source-maps)).
- Installable manifests come from registry workspaces whose `app` flag is true (`build.mjs`, `packages/ui/src/registry.ts`).

## Side Effects

- `build.mjs` deletes and writes generated files under `dist/app`.

## Verification

- Run: `pnpm run app:build`
- Design warnings: `pnpm run check:design`
