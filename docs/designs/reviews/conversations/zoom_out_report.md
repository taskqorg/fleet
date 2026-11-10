# Stage 5: Initial Zoom Out Review

Target: `docs/designs/fleet-conversations.md` (full file). Reviewers: `egm-zoom-out`, `egm-inventive-catalyst`. Date: 2026-11-10.

## Zoom Out findings

| # | Severity | Finding | Resolution |
|---|---|---|---|
| Z1 | Blocker | `GET /api/results/:id` (`app.ts:213`) already serves artifact bytes with a sandbox CSP that allows inline scripts. The design added a second route and contradicted the CSP. | Fixed: extend `/api/results/:id`; drop `allow-scripts` from its CSP. |
| Z2 | Blocker | The cost chip pointed to a Tasks threshold that does not exist. | Fixed: cost chip has no attention colour; no threshold is invented. |
| Z3 | Major | `effort` is already stored in `execution.config_ref` (`executions.ts:47`). | Fixed: read `config_ref->>'effort'`; column and backfill removed. |
| Z4 | Major | `tasks.answer` (`tasks.ts:79`) already returns the stored question on a repeated same answer and 409 on a different one. | Fixed: no `requestId`; the existing behaviour is used and the 409 is shown on the card. |
| Z5 | Major | Delivery stages already exist (`registry.ts` `stages`, `StageChip`); the design invented a second enum. | Fixed: map delivery states to the existing stage keys in a table. |
| Z6 | Major | `ResultViewer` (`controls.tsx:269`) already exists. | Fixed: extend `ResultViewer`; no new viewer control. |
| Z7 | Major | Re-projection pipeline is heavy; ask whether old history must survive. | Kept, default noted: owner preference "no data moves to save time, fix forward thoroughly" favours keeping history. Listed as an open question. |
| Z8 | Major | Cross-window copy fetched blocks to build markdown on the client while `thread_export` builds it on the server. | Fixed: `thread_export` takes an optional range; one builder. |
| Z9 | Nit | `total` on every page is a full count. | Fixed: returned on open and `around` only. |
| Z10 | Nit | Helper-level detail and speculative model registry. | Fixed: registry future item removed. |
| Z11 | Nit | `READ_OPERATIONS` listed twice (items 5 and 6). | Fixed. |
| Z12 | Nit | Result card "a link" unspecified. | Fixed: links to the Results page entry. |

## Inventive Catalyst findings

| # | Severity | Finding | Resolution |
|---|---|---|---|
| I1 | Major | `seq` is assigned at insert, not at commit, so concurrent transactions can commit out of `seq` order; `after` paging and live append could skip or reorder rows. The `change` table already solves this with `(xid, seq)` and the `pg_snapshot_xmin` horizon. | Fixed: thread reads stop at the change-feed horizon; live entries come only through the feed; wording corrected; test case added. |
| I2 | Major | Joining stream deltas at read time leaves page edges and search undefined. | Fixed: the executor writes one final block when a stream ends; reads skip superseded delta rows; `limit` counts blocks. |
| I3 | Nit | Two markdown builders for copy. | Fixed with Z8. |
| I4 | Nit | `total` cost. | Fixed with Z9. |
