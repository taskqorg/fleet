# View URL codec

## Overview

`view-url.ts` is the canonical parser and formatter for Fleet browser view paths and query state. It is re-exported by `packages/ui/src/index.ts`.

## Interface

- Exports view/state constants including `VIEWS`, `TASK_STATES`, `SESSION_STATES`, `TASK_GROUPS`, `TASK_SORTS`, and `DEFAULT_TASK_SORT`, read from [`contracts/vocabulary.json`](../../contracts/vocabulary.json), the file the coordinator's `view_link` reads too.
- `parseView(input, opts)` validates a supported path and query into `View` or returns `null`.
- `formatView(view)` produces the canonical path/query representation; `listKey(view)` and `toPageParams(view)` adapt it for list reads; both carry `view.exclude` (the projects a list leaves out, never in the URL) as `#exclude=` and `filter.excludeProjectId`.
- `splitTerms` parses field-qualified text, and `viewProblem` reports an unsupported view value.

## Invariants

- `parseView` returns `null` for invalid input instead of accepting an unknown route (`view-url.ts:parseView`).
- Formatting sorts and de-duplicates supported list values through the local `sorted`/`join` helpers, producing a stable list key and URL.
- URL decoding failure leaves that raw value intact rather than throwing (`view-url.ts:dec`).

## Side Effects

- None. All exported codec functions are pure.

## Verification

- Run: `pnpm run typecheck`
