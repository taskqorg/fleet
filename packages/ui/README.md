# packages/ui

Fleet's shared interface parts. Every app screen and the design book are built only from here (OR-13). Rules: [docs/design-system](../../docs/design-system/README.md).

## Key files

| File | Purpose |
|---|---|
| `tokens.json` | Source of truth for colour, type, space, size, radius, elevation and motion, light and dark. `scripts/tokens.mjs` turns it into CSS custom properties. |
| `icons.json` | The icon registry (TaskQ Lucide set, OR-8). |
| `src/registry.ts` | State families, workspaces, actions, harnesses and delivery stages, each with its icon. |
| `src/format.ts` | The compact time and number formatter (OR-2). |
| `src/controls.tsx` | Controls: buttons, badges, numbers, filter bar, decision block, transcript, composer, offline marker and others. |
| `src/patterns.tsx` | Compositions: `TaskListView`, `AppShell`, `ListDetail` and others. |
| `src/virtual-list.tsx` | `VirtualList` with anchor-keeping inserts and `onEnd` (OR-9, OR-11). |
| `src/styles.css` | Shared styles, tokens only. |
| `book/` | The live design book; `pnpm run book` builds it into `docs/design-system/book`. |
| `scripts/check-design.mjs` | `pnpm run check:design`: the warning-only design report. |

## Connections

Used by `app/` and the book. Specs for each control and pattern are in `docs/design-system/controls` and `patterns`. Tests: `tests/ui`.
