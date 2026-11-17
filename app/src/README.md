# Operator application source

## Overview

`src/` is the browser client: it routes registry-backed screens, calls coordinator operations, keeps list and detail reads current from the change feed, and restores read-only cached state when offline.

## Interface

- [`errors.ts`](errors.ts) starts the Sentry browser SDK for Bugsink before any other module (DSN from `config/bugsink.json`, none: off): errors only, no personal data, imports no app module; `main.tsx` passes `configureErrors` the ignored errors (`OpError`, `AccessChanged`) and the release (the version this tab runs); `captureRender` reports a render crash. `main.tsx:RenderBoundary` shows a crashed page as an error with Reload ([error tracking](../../docs/designs/fleet-error-tracking.md#2-browser-tunnel)).
- [`version.ts`](version.ts) reads `system_versions`, clears private version snapshots on access invalidation, keeps unknown/stale observations explicit, and detects and activates app updates. `hostState` puts a host with `pollError` first: danger, "polls failing", the reason code and status as its note and the first failure as a "failing since" age.
- [`main.tsx`](main.tsx) defines `SCREENS` and `App`, connects `AppShell`, identity/grant checks, preferences, feed/store startup, service worker registration, and routes.
- [`api.ts`](api.ts) exports typed `op`, `writeOnce`, `addRequestedKey`, `sendingId`, `useAlive`, `online`, and `signedOut`.
- [`feed.ts`](feed.ts) exports `startFeed`, `subscribe`, `currentCursor`, and `feedState`; [`store.ts`](store.ts) exports `useList`, `useDoc`, list loading, and live-change application.
- [`invalidation.ts`](invalidation.ts) supplies the personal-preference and mounted-project definition predicates over authoritative change metadata, retaining conservative unknown/legacy fallbacks.
- [`lists.ts`](lists.ts), [`threads.ts`](threads.ts), [`selection.ts`](selection.ts), and [`url.ts`](url.ts) hold pure list/thread/selection logic and URL hooks.
- [`list-page.tsx`](list-page.tsx) exports `useListPage` and `ListPagePanel`, the list-view pattern Tasks, Conversations and Delivery share: saved views and system presets, pins read by id, grouping with collapse remembered per person (`listGroups`), selection mode with bulk actions, and the row menu. A page that reads its whole list itself (Delivery) passes `local` in place of a server list kind.
- [`uploads.ts`](uploads.ts) exports `uploadFile` (`artifact_upload_begin`, then `PUT` parts with `Content-Range`, resuming at the server's `received`) and `useAttachments`, the composer's files for one target: upload on add with progress, remove, `ready()` before a send, clear after it ([Attachments](../../docs/designs/fleet-conversations.md#attachments)).
- [`offline.ts`](offline.ts) owns the IndexedDB read cache; [`prefs.ts`](prefs.ts) reads and patches per-person settings and turns the focus and ignored projects into the scope of lists that name no project (`scopeIds`, `withFocus`): the focus as the project filter, otherwise the ignored projects as the server-side exclusion; focus-dependent loads wait for `prefsRead`.
- [`views`](views/README.md) exports the routed screen components.

## Invariants

- `op` treats an aborted request separately from offline state, and a successful response whose JSON body cannot be read is an `OpError` rather than an empty success (`api.ts:op`).
- `writeOnce` retries only unknown write outcomes with the same request body and stops on a definite error or aborted signal (`api.ts:writeOnce`).
- The server decides list membership and ordering; `applyMembership` only merges returned rows, and `store.ts` bounds `list_membership` calls to 1,000 IDs and two lists.
- Feed delivery de-duplicates sequence numbers and batches subscribers per animation frame (`feed.ts:deliver`).
- A doc name identifies its read: every request for one name within one task (a feed batch reaching all its readers, mounts in one render) sends one request. Reads never wait for a running one, and an answer is shown only when it started after the answer on screen, so a stalled request neither blocks nor overwrites newer data; clearing memory discards running reads and pending restores (`store.ts:readDoc`).
- Preference invalidation uses the server's audit `settingTarget`, not the actor or an inferred project: current-person changes reread, other known targets do not, and legacy missing/null targets plus resync remain conservative. Mounted project detail skips only a known other `project/<id>` definition; own, legacy, unknown and non-project dependency changes still reread.
- Stored app data is cleared when the grants version changes or the browser is signed out (`offline.ts:checkGrants`, `main.tsx:App`).

## Side Effects

- `errors.ts` sends Sentry envelopes to the same-origin tunnel `POST /api/errors` (the SDK's own fetch, outside `op()`).
- `uploads.ts` sends same-origin `PUT /api/uploads/:id` parts with `XMLHttpRequest` (for upload progress).
- `api.ts` sends same-origin `POST /api/ops/:name` requests; `feed.ts` opens `EventSource('/api/stream…')` and polls `changes_since` after repeated failures.
- `offline.ts` reads, writes, and deletes IndexedDB cache entries; `prefs.ts` writes person settings through coordinator operations.
- `main.tsx` registers `/sw.js`, changes browser history, and can redirect to Cloudflare Access logout.

## Verification

- Run: `pnpm run typecheck`
- Build: `pnpm run app:build`
- Design warnings: `pnpm run check:design`

## Authorized prepared reads

`access.ts` owns the single generation gate and injected identity/baseline controllers. `api.ts` fences every ordinary read/write and provides only the fixed identity/no-cursor control allowlist. `main.tsx` unmounts private route generations before validation and preserves shell geometry. `feed.ts` establishes a new authorized baseline without replaying old cursors; `offline.ts` fences memory and async persistence independently of blocked deletion.

`store.ts:usePreparedDoc` shares one active read plus one pending bit per canonical prepared key, checks bigint versions, retains figures on unchanged progress, cancels on last subscriber and checks foreground progress at most every two seconds. Scoped authored drafts remain in `api.ts` under their original actor/target; they are not server cache. Activation and rollback: [prepared resources](../../docs/designs/fleet-analytics.md#browser-access-and-activation).

`prepareSending` retains complete message/start bodies and their SHA-256 under the authorized draft scope; explicit retries keep the original delivery arguments. Project editor/create targets share that adapter, preserve the original optimistic base and frozen pending save body, and never automatically resubmit after validation.
