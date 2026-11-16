# Operator application source

## Overview

`src/` is the browser client: it routes registry-backed screens, calls coordinator operations, keeps list and detail reads current from the change feed, and restores read-only cached state when offline.

## Interface

- [`client-errors.ts`](client-errors.ts) reports uncaught errors, unhandled rejections and render crashes to `client_error_report`: once per error per 5 minutes with a repeat count, at most 30 reports per tab, keepalive sends, only app-produced URL parts; it never throws. `main.tsx:RenderBoundary` shows a crashed page as an error with Reload ([telemetry §9](../../docs/designs/fleet-telemetry.md#9-browser-errors)).
- [`version.ts`](version.ts) reads `system_versions`, clears private version snapshots on access invalidation, keeps unknown/stale observations explicit, and detects and activates app updates.
- [`main.tsx`](main.tsx) defines `SCREENS` and `App`, connects `AppShell`, identity/grant checks, preferences, feed/store startup, service worker registration, and routes.
- [`api.ts`](api.ts) exports typed `op`, `writeOnce`, `addRequestedKey`, `sendingId`, `useAlive`, `online`, and `signedOut`.
- [`feed.ts`](feed.ts) exports `startFeed`, `subscribe`, `currentCursor`, and `feedState`; [`store.ts`](store.ts) exports `useList`, `useDoc`, list loading, and live-change application.
- [`invalidation.ts`](invalidation.ts) supplies the personal-preference and mounted-project definition predicates over authoritative change metadata, retaining conservative unknown/legacy fallbacks.
- [`lists.ts`](lists.ts), [`threads.ts`](threads.ts), [`selection.ts`](selection.ts), and [`url.ts`](url.ts) hold pure list/thread/selection logic and URL hooks.
- [`offline.ts`](offline.ts) owns the IndexedDB read cache; [`prefs.ts`](prefs.ts) reads and patches per-person settings.
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

- `client-errors.ts` sends same-origin keepalive `POST /api/ops/client_error_report` requests outside `op()` (no access fence, no offline or signed-out marking).
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
