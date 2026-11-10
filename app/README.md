# app

The Fleet operator app: a Preact single-page app served by the coordinator at `/overview`, `/tasks`, `/conversations` and `/hosts`. Design: [docs/designs/fleet-operator-app.md](../docs/designs/fleet-operator-app.md); design-system decision D15.

## Key files

| File | Purpose |
|---|---|
| `build.mjs` | esbuild build into `dist/app`: hashed chunks per view, `index.html`, `sw.js`, per-view manifests and icons, size report. Run by `pnpm run build` and `pnpm run app:build`. |
| `tsconfig.json` | Type-checks `app/` and `packages/ui` with Preact JSX. |
| `src/main.tsx` | Entry: `AppShell`, Wouter routes, identity check (`me`), feed start, service worker and the "Update available" and "Session expired" notices. |
| `src/api.ts` | `op(name, args)` against `/api/ops/:name`; signed-out detection. |
| `src/feed.ts` | SSE client for `/api/stream?cursor=`, with backoff, `changes_since` polling fallback and `resync`. |
| `src/live.ts`, `src/lists.ts` | Entity rows and server-decided list membership (`list_membership`), applied in batches. |
| `src/offline.ts` | IndexedDB mirror of open views, cleared on sign-out or a grants change. |
| `src/prefs.ts` | Per-browser pins and saved views (localStorage) until people exist. |
| `src/views/*.tsx` | The four views. List and detail share one route component (`Tasks` with an `id`, and so on). |

## Connections

Builds only from `packages/ui` (OR-13). Reads and writes only through coordinator ops; the coordinator (`go/coordinator/d_routes.go`, `stream.go`) serves the built files. Checked by `tests/ui/app.check.ts` through `pnpm run check:design`.
