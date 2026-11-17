# Operator views

## Overview

Each file exports one routed Preact screen. `main.tsx:SCREENS` maps the registry's application workspaces to these components.

## Interface

- [`projects.tsx`](projects.tsx), [`overview.tsx`](overview.tsx), [`tasks.tsx`](tasks.tsx), [`conversations.tsx`](conversations.tsx), [`results.tsx`](results.tsx), [`delivery.tsx`](delivery.tsx), [`hosts.tsx`](hosts.tsx), [`runners.tsx`](runners.tsx), [`lab.tsx`](lab.tsx), and [`keys.tsx`](keys.tsx) export their like-named screens.
- [`resources.tsx`](resources.tsx) provides shared `Totals`, `CostFigures`, scoped rollup (`own` / `delegated` / `spawned`), and recursive execution tree builders used by Tasks, Conversations, Projects, Definitions, and Runners.
- [`projects.tsx`](projects.tsx) integrates `analytics_query` (`window`, `dimensions`, `measures`, `filters`) with interactive `AreaChart` time-bucket filtering and dimension breakdown beside project settings and PM chat.
- [`definitions.tsx`](definitions.tsx) exports `Definitions` for the `agent`, `pipeline`, and `catalogue` routes.
- [`lab-create.tsx`](lab-create.tsx) exports `LabCreate` for experiment-draft creation; [`lab.tsx`](lab.tsx) opens it.
- [`limits.tsx`](limits.tsx) exports `Limits`, rendered from the Hosts screen.

## Invariants

- The pipeline definition step list includes publication artifact input paths from `files` values, preserving nested paths and deduplicating references. Only relative paths bound to example inputs or an earlier step are shown; destination keys and malformed/internal paths do not become read facts.
- Views call coordinator operations through `api.ts:op` or `api.ts:writeOnce`; they do not construct a second backend protocol.
- List screens use server rows through `store.ts:useList`; `Tasks` and `Conversations` share `list-page.tsx:useListPage`, which retains server ordering and membership for the filtered page, reads accessible pinned rows separately by ID in server-sized batches (Tasks unfiltered, Conversations filtered by the archived list the view shows), and keeps those rows visible and selectable across filters. Pin-read failures remain visible and retryable. `Delivery` uses the same pattern over the rows `delivery_status` returns, filtered, grouped and sorted in the browser (`LocalList`); its pins come from those rows.
- Writes with uncertain outcomes preserve the existing request identity or retain an uncertain receipt for user review (`conversations.tsx:Conversation`, `lab-create.tsx:LabCreate`).
- `LabCreate` does not launch an experiment: it creates a draft and validates a readback before navigating to it.

## Side Effects

- Views mutate browser-local draft, selection, or receipt state and invoke coordinator operations such as task, session, definition, lab, and delivery operations.
- `Conversations` can copy a server-exported thread to the clipboard; `LabCreate` uses `sessionStorage` for an in-progress draft receipt.

## Verification

- Run: `pnpm run typecheck`
- Design warnings: `pnpm run check:design`

Projects uses independent configuration/save/readback and prepared resource headers when explicitly selected and advertised. Dirty fields survive resource updates; definition changes show conflicts. `resources.tsx:PreparedProjectResources` owns bounded continuous tools/costs/prices windows using opaque server positions and shared anchor methods. Legacy rendering remains available while capability is off. Composer receives an identity/authorized-target persistence adapter; Lab separately quarantines authored input and clears server-derived receipts on access changes.

Project saves fence older configuration reads and coalesce definition refreshes until the write/readback settles. Reloading the same content and repository hash retains the current readiness observation; applying a changed source clears it.
