# Operator views

## Overview

Each file exports one routed SolidJS 2.0 screen or a part of one. `main.tsx` loads the section views (Home, Live, Conversations, Projects, Lab, Library, System) via `lazy`; Projects, Library and System each render every screen of their section.

## Interface

- [`projects.tsx`](projects.tsx) (with [`projects-editor.tsx`](projects-editor.tsx) for project configuration and readiness editing), [`home.tsx`](home.tsx) (Home, `/home`, and the session it opens at `/home/<code>`), [`tasks.tsx`](tasks.tsx), [`conversations.tsx`](conversations.tsx) (with [`conversation-new.tsx`](conversation-new.tsx), the `/conversations/new` page), [`results.tsx`](results.tsx), [`delivery.tsx`](delivery.tsx) (with [`delivery-model.tsx`](delivery-model.tsx) for stage/history/bucket helpers and [`delivery-panels.tsx`](delivery-panels.tsx) for overview, pace, and pull-request detail panes), [`runners.tsx`](runners.tsx), [`lab.tsx`](lab.tsx), and [`keys.tsx`](keys.tsx) export their like-named screens.
- [`session.tsx`](session.tsx) exports `SessionView {id, frame, onClose?, onTitle?}`, the one session view ([session](../../../docs/design-system/pages/session.md)): `id` is a `C-…` code (resolved with `entity_resolve`) or a session id; `frame="home"` is `/home/C-…` (Home mounts it, full width; `onTitle` gives the session's name for the shell title), `frame="conversations"` the Conversations work area. It reads `session_get`, `thread_composer`, the thread (`threads.ts`, shown by `Thread quiet`) and `session_context`, and keeps Fork, Stop, steer or queue, rename, switch or hand off, start fresh, archive, search and Details.
- [`hosts.tsx`](hosts.tsx) exports `Hosts`, the System section ([system](../../../docs/design-system/pages/system.md)): one `CatalogueTree` with the roots Hosts, Accounts & limits, Connections & keys, Runners, Spend and People (owner only), and the work area for every System route (`/hosts…`, `/hosts/limits[/<account>]`, `/keys…`, `/runners`, `/spend`, `/humans…`), so the tree stays mounted between them; a host opens as its detail head, problem rows, host band, agent CLIs, readings and Remove behind a confirmation card. [`system-idle.tsx`](system-idle.tsx) renders the idle summary: readiness problem rows (hosts, failure causes ×N, findings, accounts near their limit or with no host to run on, failing keys), spend figures and services with errors in the last hour. [`accounts.tsx`](accounts.tsx) renders one account (left per period, the hosts it runs on, J5) and shares the light limits read. [`../causes.ts`](../causes.ts) maps a stored run cause code to its plain sentence (`contracts/vocabulary.json` causes).
- [`humans.tsx`](humans.tsx) renders the owner-only people summary and one person's access editor in the System work area, including Cloudflare email rule status and retry for removed roster entries. [`personal-lab.tsx`](personal-lab.tsx) renders personal Lab drafts and architect review.
- [`resources.tsx`](resources.tsx) provides shared `Totals`, `CostFigures`, scoped rollup (`own` / `delegated` / `spawned`), and recursive execution tree builders used by Tasks, Conversations, Projects, Definitions, and Runners.
- [`projects.tsx`](projects.tsx) is the Projects section: the project catalogue, the cross-project summary and an opened project's facets (Overview, Tasks, Pipelines, Results, Delivery, Setup, History); Setup reads the project's settings (`project_context`) as plain values and opens the project editor ([`projects-editor.tsx`](projects-editor.tsx)) in their place; [`project-overview.tsx`](project-overview.tsx) holds Overview and Pipelines (one `project_view` read), a pipeline page (`project_pipeline_view`) and History (`HistoryFeed`). `Tasks`, `Results` and `Delivery` take `project` (the facet's project, kept through Clear filters and saved views) and `frame` (one column where the opened item replaces the list); their URL item is a code (`T-FLT-42`, `R-FLT-3`) or an id, resolved by [`../codes.ts`](../codes.ts).
- [`live.tsx`](live.tsx) exports `Live`, the [Live](../../../docs/design-system/pages/live.md) page: status pill, six figures, host bands with worker slots in start order and 24-hour run strips, and the side column; `wall` (`/live?wall`) renders it with nothing to open.
- [`history.tsx`](history.tsx) exports `History`, the [History](../../../docs/design-system/pages/history.md) page (one `history_view` list with scope, kind and Failed filters in the address), `HistoryFeed` for a scoped history that loads on scroll, and `HistoryPreview`, the newest entries a section's empty pane shows from its own view (Lab: `lab_view.history`).
- [`agents.tsx`](agents.tsx) exports `Library`, the Library section (`/library`, `/library/<methods|pipelines|agents|bots|skills|changes>/<id>`): one tree over every root, what waits when idle, and the opened item. It opens [`definitions.tsx`](definitions.tsx) `DefinitionDetail` (differences from the base default as diff rows), [`bots.tsx`](bots.tsx) `BotDetail` and `BotCreateForm` (a bot by its `B-` code, `entity_resolve` when the list does not hold it), and [`changes.tsx`](changes.tsx) `Changes`.
- [`lab-create.tsx`](lab-create.tsx) exports `LabCreate` for experiment-draft creation; [`lab.tsx`](lab.tsx) opens it.
- [`limits.tsx`](limits.tsx) exports `Limits`, rendered as the Accounts & limits root of System.

## Invariants

- Views read through `store.ts:useOp` (prepared reads `usePreparedOp`, lists `useListPage`) and write through `api.ts:op` or `api.ts:writeOnce`; they hold no invalidation predicates and no `any`: replies are the generated contract types. `pnpm run check:design` fails on a raw HTML control beyond a file's legacy count.
- List screens use server rows through `store.ts:useList`; `Tasks` and `Conversations` share `list-page.tsx:useListPage`, which retains server ordering and membership for the filtered page, reads accessible pinned rows separately by ID in server-sized batches (Tasks unfiltered, Conversations filtered by the archived list the view shows), and keeps those rows visible and selectable across filters. Pin-read failures remain visible and retryable. `Delivery` uses the same pattern over the rows `delivery_status` returns, filtered, grouped and sorted in the browser (`LocalList`); its pins come from those rows.
- Writes with uncertain outcomes preserve the existing request identity or retain an uncertain receipt for user review (`session.tsx:Session`, `lab-create.tsx:LabCreate`).
- The session view starts a fresh child through `session_fork`. It reads lineage, packet status and recipient execution from `session_get`, and reads the authorized packet text through `session_handoffs`.
- The session view reads its screen through one `session_view` (head, composer destination, open questions, threads, triggers, context, last message, newest turns), independently of transcript pagination; `session_get` is read only while Details is open. Drafts remain scoped to the open session and each submitted request retains its displayed destination.
- `LabCreate` does not launch an experiment: it creates a draft and validates a readback before navigating to it.

## Side Effects

- Views mutate browser-local draft, selection, or receipt state and invoke coordinator operations such as task, session, definition, lab, and delivery operations.
- The session view can copy a server-exported thread to the clipboard; `LabCreate` uses `sessionStorage` for an in-progress draft receipt.

## Verification

- Run: `pnpm run typecheck`
- Design warnings: `pnpm run check:design`

Projects uses independent configuration/save/readback and prepared resource headers when explicitly selected and advertised. Dirty fields survive resource updates; definition changes show conflicts. `resources.tsx:PreparedProjectResources` owns bounded continuous tools/costs/prices windows using opaque server positions and shared anchor methods. Legacy rendering remains available while capability is off. Composer receives an identity/authorized-target persistence adapter; Lab separately quarantines authored input and clears server-derived receipts on access changes.

Project saves fence older configuration reads and coalesce definition refreshes until the write/readback settles. Reloading the same content and repository hash retains the current readiness observation; applying a changed source clears it.
