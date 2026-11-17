# UI source package

## Overview

`src/` supplies Fleet's reusable Preact components and browser helpers. `index.ts` is the only intended import barrel for app code.

## Interface

- `controls.tsx` exports controls for navigation, lists, details, threads, forms, artifacts, metrics, and charts.
- `charts.tsx` exports the day charts for pace pages: `DayChart`, `ShareBar`, `NormBar` and the scale helper `capOf`.
- `patterns.tsx` exports assembled layouts including `AppShell`, `ListDetail`, `ListFrame`, `TaskListView`, and `DefinitionEditor`.
- `virtual-list.tsx:VirtualList` exports keyed virtual rendering and `VirtualListHandle`.
- `registry.ts` exports icons, state families, workspaces, actions, harnesses, and stages; `format.ts` exports compact value formatting (including calendar days in the viewer's zone: `day`, `dayStart`, `hourLabel`) and actor/file helpers.
- `markdown.ts`/`markdown.tsx` export parsing and Preact markdown rendering; `clip.ts` exports clipboard message/HTML helpers; `search.ts:matchSearch` applies supported search fields.

## Invariants

- `Icon` resolves names through `registry.ts:iconSvg`; state-bearing components use `registry.ts:states` families.
- `markdown.ts:SAFE_LINK` permits only `http:`, `https:`, and `mailto:` links in rendered markdown.
- `virtual-list.tsx:VirtualList` keeps item identity through its required key callback and exposes scrolling through `VirtualListHandle`.
- Formatting exposes measured, estimated, subscription, and unavailable values distinctly (`format.ts:Measured`, `format.ts:measured`, `format.ts:splitCostFigures`). `CostFigures` and `Totals` keep metered spend separate from subscription list-price equivalents across currencies.
- Interactive `AreaChart` bucket buttons sit outside `.ac-plot[role="img"]` without `<svg>`, `ExecTree` supports recursive hierarchies (`onSelect`, `costFigures`), and `BoundaryRow` accepts per-execution `resources`.

## Side Effects

- Components update browser DOM, focus, history-adjacent UI callbacks, and local component state.
- Clipboard helpers call `navigator.clipboard`; `Composer` uses injected persistence only; the app owns its storage and authorization.

## Verification

- Run: `pnpm run typecheck`
- Design warnings: `pnpm run check:design`

`VirtualListHandle` exposes `getAnchor()` and `restoreAnchor(key, offsetPx)` in addition to key jumps; `fill` makes it the owning column's only scroller. `Composer` accepts an injected `draftPersistence` adapter and is memory-only without one; completion clears only unchanged original text. `StatusLine` reserves two lines for visible asynchronous feedback without moving adjacent controls.

`AppShell.privateReady` and `privateGeneration` fence its Spotlight state independently of column geometry. `Composer` remounts its private input by adapter key, resetting busy state before another target becomes usable. VirtualList applies requested restoration after its own layout work and uses current viewport edges for continuous loading.
