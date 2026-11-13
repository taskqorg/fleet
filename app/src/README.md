# Operator application source

## Overview

`src/` is the browser client: it routes registry-backed screens, calls coordinator operations, keeps list and detail reads current from the change feed, and restores read-only cached state when offline.

## Interface

- [`main.tsx`](main.tsx) defines `SCREENS` and `App`, connects `AppShell`, identity/grant checks, preferences, feed/store startup, service worker registration, and routes.
- [`api.ts`](api.ts) exports typed `op`, `writeOnce`, `addRequestedKey`, `sendingId`, `useAlive`, `online`, and `signedOut`.
- [`feed.ts`](feed.ts) exports `startFeed`, `subscribe`, `currentCursor`, and `feedState`; [`store.ts`](store.ts) exports `useList`, `useDoc`, list loading, and live-change application.
- [`lists.ts`](lists.ts), [`threads.ts`](threads.ts), [`selection.ts`](selection.ts), and [`url.ts`](url.ts) hold pure list/thread/selection logic and URL hooks.
- [`offline.ts`](offline.ts) owns the IndexedDB read cache; [`prefs.ts`](prefs.ts) reads and patches per-person settings.
- [`views`](views/README.md) exports the routed screen components.

## Invariants

- `op` treats an aborted request separately from offline state, and a successful response whose JSON body cannot be read is an `OpError` rather than an empty success (`api.ts:op`).
- `writeOnce` retries only unknown write outcomes with the same request body and stops on a definite error or aborted signal (`api.ts:writeOnce`).
- The server decides list membership and ordering; `applyMembership` only merges returned rows, and `store.ts` bounds `list_membership` calls to 1,000 IDs and two lists.
- Feed delivery de-duplicates sequence numbers and batches subscribers per animation frame (`feed.ts:deliver`).
- Stored app data is cleared when the grants version changes or the browser is signed out (`offline.ts:checkGrants`, `main.tsx:App`).

## Side Effects

- `api.ts` sends same-origin `POST /api/ops/:name` requests; `feed.ts` opens `EventSource('/api/stream…')` and polls `changes_since` after repeated failures.
- `offline.ts` reads, writes, and deletes IndexedDB cache entries; `prefs.ts` writes person settings through coordinator operations.
- `main.tsx` registers `/sw.js`, changes browser history, and can redirect to Cloudflare Access logout.

## Verification

- Run: `pnpm run typecheck`
- Build: `pnpm run app:build`
- Design warnings: `pnpm run check:design`
