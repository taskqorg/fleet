# Operator application source

## Overview

`src/` is the browser client: it routes registry-backed screens, calls coordinator operations, and keeps in-memory list and detail reads current from the change feed.

## Interface

- [`views/bots.tsx`](views/bots.tsx) and [`bot-view.ts`](bot-view.ts) render the Bots workspace with saved settings, project memory, triggers, runtime actions (`Run now`, `Pause`/`Resume`, `Stop`, `Home` conversation link), managed tasks (`active`/`waiting`/`historical`), wake runs (`quiet`/`skipped`/`queued`/`failed`/`unconfirmed`), open and historical questions with errors, and bot spend/usage analytics by recorded execution project and source executions. The Work group of the sidebar and Spotlight link to the workspace. The view uses the project focus and the shared operation client.

- [`errors.ts`](errors.ts) starts the Sentry browser SDK for Bugsink before any other module (DSN from `config/bugsink.json`, none: off): errors only, no personal data, imports no app module; `main.tsx` passes `configureErrors` the ignored errors (`OpError`, `AccessChanged`) and the release (the version this tab runs); `captureRender` reports a render crash. `main.tsx:RenderBoundary` shows a crashed page as an error with Reload ([error tracking](../../docs/designs/fleet-error-tracking.md#2-browser-tunnel)).
- [`version.ts`](version.ts) reads `system_versions`, clears private version snapshots on access invalidation, keeps unknown/stale observations explicit, and detects and activates app updates (busy until the page unloads). Rows are attention or danger only for a confirmed current problem ([nav rail](../../docs/design-system/controls/nav-rail.md), Version). `hostState` puts a host with `pollError` first: "polls failing", the reason code and status as its note and the first failure as a "failing since" age; danger once its polls have failed long enough to open the poll problem (`pollProblem`).
- [`main.tsx`](main.tsx) defines `SCREENS` and `App`, connects `AppShell` (wall mode for `/live?wall`; a viewer-only person gets Live alone), identity/grant checks, preferences, feed/store startup, `@ui-sdk/core/agent` Agent Bridge (`window.__fleetAgent` and `registerTools`), service worker registration, and routes.
- [`router.ts`](router.ts) provides the lightweight History API router (`useLocation`, `useSearch`, `navigate`, `syncLocation`); [`routes.ts`](routes.ts) translates address-bar routes and the paths views speak both ways, path and query: a project facet `/projects/FLT/tasks/T-FLT-42` is the Tasks view filtered to the project (`/tasks/T-FLT-42?project=<id>`), with project codes from `project_list` (`setProjectCodes`); until that first read, a screen whose address names a project shows Loading (`projectCodesPending`), so no read sends a code as a project id. [`codes.ts`](codes.ts) resolves a URL item given as a human code to its id (`useItemId`).
- [`signal.ts`](signal.ts) exports `cell` (synchronous `.value`/`.peek()` over Solid 2 `createSignal`) and `MaybeAccessor` helpers.
- [`api.ts`](api.ts) exports typed `op`, `writeOnce`, `addRequestedKey`, `sendingId`, `useAlive`, `online`, and `signedOut`. [`answers.ts`](answers.ts) `useAnswers` connects `packages/ui` `useAnswer` to `questions_answer` and `connections_key_add` for every view that answers questions.
- [`feed.ts`](feed.ts) exports `startFeed`, `subscribe`, `currentCursor`, and `feedState`; [`store.ts`](store.ts) exports `useList`, list loading and live-change application, and `useOp(name, request, {every, quiet})`, the one way views read an operation: the cache key is the operation and its canonical request (shared by every view asking the same), and it rereads when `touchedBy` says a change touches it (the server's `touches` on every change, from each operation's contract `changes`, narrowed to the entities its request names), on resync and every `every` ms while the tab is visible (a hidden tab polls nothing). `usePreparedOp` is the same for prepared reads.
- [`entity-cards.ts`](entity-cards.ts) is the `EntityCards` source `main.tsx` provides: `useOp('entity_card', …)`, every 5 s for kinds whose state comes from runs; Open navigates in the app.
- [`lists.ts`](lists.ts), [`threads.ts`](threads.ts), [`selection.ts`](selection.ts), and [`url.ts`](url.ts) hold pure list/thread/selection logic and URL hooks.
- [`list-page.tsx`](list-page.tsx) exports `useListPage` and `ListPagePanel`, the list-view pattern Tasks, Conversations and Delivery share: saved views and system presets, pins read by id, grouping with collapse remembered per person (`listGroups`), selection mode with bulk actions, and the row menu. A page that reads its whole list itself (Delivery) passes `local` in place of a server list kind.
- [`uploads.ts`](uploads.ts) exports `uploadFile` (`artifact_upload_begin`, then `PUT` parts with `Content-Range`, resuming at the server's `received`) and `useAttachments`, the composer's files for one target: upload on add with progress, remove, `ready()` before a send, clear after it ([Attachments](../../docs/designs/fleet-conversations.md#attachments)).
- [`persist.ts`](persist.ts) is the read mirror ([operator app D3](../../docs/designs/fleet-operator-app.md#5-offline-sign-in-state-and-the-pwa-shell-d2-d3)): `restore` and `persist` keep the last answer of each read in IndexedDB under the validated person and grants (`adopt`), so a reload renders at once; `purge` and `forget` drop it (another person, changed grants, sign-out). Without IndexedDB every call is a no-op.
- `store.ts` `useView(name, request, {quiet})` is the read of a screen: a view operation (`ViewName`, contract `view: true`), no `every` ([data layer](../../docs/designs/fleet-data-layer.md)). A hidden tab reads nothing: reads the change stream asks for while it is hidden run once when it shows again.
- [`offline.ts`](offline.ts) owns the synchronous in-memory store reset bus (`onClear`, `clearAll`); [`prefs.ts`](prefs.ts) reads and patches per-person settings and turns the focus and ignored projects into the scope of lists that name no project (`scopeIds`, `withFocus`): the focus as the project filter, otherwise the ignored projects as the server-side exclusion; focus-dependent loads wait for `prefsRead`.
- [`system-views.ts`](system-views.ts) turns the system presets and navigation counter defaults of [`contracts/vocabulary.json`](../../contracts/vocabulary.json) into views; the coordinator's `nav_counts` reads the same file. [`nav-counts.ts`](nav-counts.ts) `useNavCounts` reads `nav_counts` for the rail counters ([nav rail](../../docs/design-system/controls/nav-rail.md)) through `useOp`: again when a change touches it (this person's settings included) at most once per 5 s, and every 30 s; `navCounted` names the counted view as the server resolved it, and `prefs.ts` `setNavCounter` chooses it.
- [`views`](views/README.md) exports the routed screen components.

## Invariants

- `op` treats an aborted request separately from offline state, and a successful response whose JSON body cannot be read is an `OpError` rather than an empty success (`api.ts:op`).
- `writeOnce` retries only unknown write outcomes with the same request body and stops on a definite error or aborted signal (`api.ts:writeOnce`).
- The server decides list membership and ordering (`store.ts` coalesces affected row IDs per mounted list, reconciles via `list_membership`, and falls back to quiet `loadList` when membership or the paging boundary cannot be proved; `list-page.tsx` reads pinned rows separately via `useOp('list_membership', ...)`).
- Feed delivery de-duplicates sequence numbers and batches subscribers per animation frame (`feed.ts:deliver`).
- A doc name identifies its read: every request for one name within one task (a feed batch reaching all its readers, mounts in one render) sends one request. Reads never wait for a running one, and an answer is shown only when it started after the answer on screen, so a stalled request neither blocks nor overwrites newer data; clearing memory discards running reads (`store.ts:readDoc`).
- Invalidation has one rule, `store.ts:touchedBy`: the change's `touches` names the operation, and the change names no other task, session, host, artifact, execution, experiment, question or project than the request (a definition other than `project/<id>` touches every project); a setting-change of another known person touches nothing. Views write no predicates.
- Stored in-memory app state is cleared on identity/grant invalidation or sign-out (`offline.ts:clearAll`, `main.tsx:App`).
- A read's mirrored answer shows (`restored`, freshness `updating`) only until the first read of this page answers, and never after a clear that began after its restore started (`store.ts:docEntry`, `entry`). A newly mounted reader joins a read already on its way; a resync rereads only what has answered before (`store.ts:holdDoc`, `watch`).
- Live updates keep what is on screen: a doc reread or list reload keeps every object equal to the one shown (`share.ts`; array items matched by `id`), so `<For>` and keyed `<Show>` rebuild only what changed. Lists of cards with inputs key `<For>` by id as well (`session.tsx` and `conversations.tsx` open questions).
- Typed text lives in the draft store, never only in a component: `Composer` (app persistence or `draftKey`), `QuestionCard` (`question:<id>`) and `createDraft` fields (packages/ui `drafts.ts`; backend `api.ts`, localStorage `fleet-draft:[identity, key]`). A remount or reload restores it; text typed before the person is known is stored once they are. Design rule `UI-INPUT`: no raw `<input>`/`<textarea>` in `app/src`.
- A focus or resync that cannot confirm the person keeps the page and asks again; only another person or changed grants clear it (`main.tsx:recheck`, `feed.ts:rebaseFeed`).

## Side Effects

- `errors.ts` sends Sentry envelopes to the same-origin tunnel `POST /api/errors` (the SDK's own fetch, outside `op()`).
- `uploads.ts` sends same-origin `PUT /api/uploads/:id` parts with `XMLHttpRequest` (for upload progress).
- `api.ts` sends same-origin `POST /api/ops/:name` requests; `feed.ts` opens `EventSource('/api/stream…')` and polls `changes_since` after repeated failures.
- `persist.ts` reads and writes the IndexedDB database `fleet-data` and the `localStorage` key `fleet-data-owner`.
- `offline.ts` clears in-memory stores on access invalidation; `prefs.ts` writes person settings through coordinator operations.
- `main.tsx` updates any legacy `/sw.js` registration to its self-unregistering cleanup stub, changes browser history, and can redirect to Cloudflare Access logout.

## Verification

- Run: `pnpm run typecheck`
- Build: `pnpm run app:build`
- S9 AST design rules (`UI-ANY`, `UI-INPUT`, `UI-OP`, `UI-VOCAB`): `node --test tests/ui/design-rules.test.ts` ([`design-rules.test.ts`](../../tests/ui/design-rules.test.ts), part of `node --test tests/ui/*.test.ts`)
- Design warnings: `pnpm run check:design`
- Drafts through live updates and reload: `pnpm run app:bundle && node tests/ui/drafts.check.ts` ([`drafts.check.ts`](../../tests/ui/drafts.check.ts))
- Read mirror, person check and idle update: `pnpm run app:bundle && node tests/ui/mirror.check.ts` ([`mirror.check.ts`](../../tests/ui/mirror.check.ts); every API answer held back 400 ms)

## Authorized prepared reads

`access.ts` owns the single generation gate and injected identity/baseline controllers. `api.ts` fences every ordinary read/write and provides only the fixed identity/no-cursor control allowlist. `main.tsx` unmounts private route generations before validation and preserves shell geometry. `feed.ts` establishes a new authorized baseline without replaying old cursors; `offline.ts` fences and clears in-memory state on access invalidation.

`store.ts:usePreparedOp` shares one active read plus one pending bit per canonical prepared key, checks bigint versions, retains figures on unchanged progress, cancels on last subscriber and checks foreground progress at most every two seconds. Scoped authored drafts remain in `api.ts` under their original actor/target; they are not server cache. Activation and rollback: [prepared resources](../../docs/designs/fleet-analytics.md#browser-access-and-activation).

`prepareSending` retains complete message/start bodies and their SHA-256 under the authorized draft scope; explicit retries keep the original delivery arguments. Project editor/create targets share that adapter, preserve the original optimistic base and frozen pending save body, and never automatically resubmit after validation.
