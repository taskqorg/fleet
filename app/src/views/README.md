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

- Views call coordinator operations through `api.ts:op` or `api.ts:writeOnce`; they do not construct a second backend protocol.
- List screens use server rows through `store.ts:useList`; `Tasks` retains server ordering and membership while applying pins only to the displayed rows.
- Writes with uncertain outcomes preserve the existing request identity or retain an uncertain receipt for user review (`conversations.tsx:Conversation`, `lab-create.tsx:LabCreate`).
- `LabCreate` does not launch an experiment: it creates a draft and validates a readback before navigating to it.

## Side Effects

- Views mutate browser-local draft, selection, or receipt state and invoke coordinator operations such as task, session, definition, lab, and delivery operations.
- `Conversations` can copy a server-exported thread to the clipboard; `LabCreate` uses `sessionStorage` for an in-progress draft receipt.

## Verification

- Run: `pnpm run typecheck`
- Design warnings: `pnpm run check:design`
