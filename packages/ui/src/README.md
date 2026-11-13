# UI source package

## Overview

`src/` supplies Fleet's reusable Preact components and browser helpers. `index.ts` is the only intended import barrel for app code.

## Interface

- `controls.tsx` exports controls for navigation, lists, details, threads, forms, artifacts, metrics, and charts.
- `patterns.tsx` exports assembled layouts including `AppShell`, `ListDetail`, `ListFrame`, `TaskListView`, and `DefinitionEditor`.
- `virtual-list.tsx:VirtualList` exports keyed virtual rendering and `VirtualListHandle`.
- `registry.ts` exports icons, state families, workspaces, actions, harnesses, and stages; `format.ts` exports compact value formatting and actor/file helpers.
- `markdown.ts`/`markdown.tsx` export parsing and Preact markdown rendering; `clip.ts` exports clipboard message/HTML helpers; `search.ts:matchSearch` applies supported search fields.

## Invariants

- `Icon` resolves names through `registry.ts:iconSvg`; state-bearing components use `registry.ts:states` families.
- `markdown.ts:SAFE_LINK` permits only `http:`, `https:`, and `mailto:` links in rendered markdown.
- `virtual-list.tsx:VirtualList` keeps item identity through its required key callback and exposes scrolling through `VirtualListHandle`.
- Formatting exposes measured, estimated, subscription, and unavailable values distinctly (`format.ts:Measured`, `format.ts:measured`, `format.ts:splitCostFigures`). `CostFigures` and `Totals` keep metered spend separate from subscription list-price equivalents across currencies.
- Interactive `AreaChart` bucket buttons sit outside `.ac-plot[role="img"]` without `<svg>`, `ExecTree` supports recursive hierarchies (`onSelect`, `costFigures`), and `BoundaryRow` accepts per-execution `resources`.

## Side Effects

- Components update browser DOM, focus, history-adjacent UI callbacks, and local component state.
- Clipboard helpers call `navigator.clipboard`; `Composer` persists drafts in browser storage when a `draftKey` is supplied.

## Verification

- Run: `pnpm run typecheck`
- Design warnings: `pnpm run check:design`
