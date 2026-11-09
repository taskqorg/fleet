# Consolidated EGM review: fleet-operator-app.md

* **Mode:** consolidated report, Stages 5 to 9.
* **Reviewers:** zoom-out, inventive-catalyst, comprehension, design, structural, style, implementation-readiness, codebase-details.
* **Reports:** [Stage 5](zoom_out_report.md), [Stage 6](comprehension_report.md), [Stage 7](critic_report.md), [Stage 8](intermediate_zoom_out_report.md), [Stage 9](implementation_readiness_report.md).

## Totals after deduplication (first draft)

There were 10 Blockers, about 30 Majors and about 35 Nits. All Blockers and Majors were applied to the doc. On the revised doc, Stage 8 found 0 Blockers and 1 Major, and all of its findings were fixed afterwards. No finding reversed owner decisions D1–D5.

## Blockers and how they were fixed

| # | Blocker | Fix |
|---|---|---|
| 1 | The GET read-snapshot wrapper would freeze `/api/stream`. | The stream is in `longRoutes` and uses one short read per wake. A test checks it. |
| 2 | Migration 7 already exists. | The new migration is number 8. |
| 3 | Conversations could not update live. | `event` and `message` changes carry `session_id`, and the open conversation fetches after its cursor. |
| 4 | The trigger used `NEW.seq`, broke on DELETE, and sent a non-text payload. | It uses `OLD`/`NEW` by `TG_OP` and a constant notify payload. |
| 5 | `task_dependency` and `legacy_kind` keys were undefined. | Defined in the kind map. |
| 6 | The migration splitter breaks multi-line functions. | The trigger function is written on one line. |
| 7 | No settings path for retention. | Config `changeLogRetentionDays` (default 7), reported by the `me` op. |
| 8 | The list contracts and `started_at` were undefined. | Exact request and response schemas, sorted by priority, created or updated time. |
| 9 | No Risks section. | Risks and Mitigations table added. |

## Main Majors applied

* **Watermark.** An xid watermark replaces the overlap window, so the cursor never passes an unfinished transaction.
* **Notify load.** Constant payload, 50 ms debounce, only mapped kinds, and `host-observation` polls skipped when only `lastSeenAt` changes.
* **Scope.** Process scope `*` in this slice; Phase 6 adds `SET LOCAL` per read and per stream.
* **Prune.** Batched, in its own lock scope, with a floor; resync when the cursor is below the floor or above the maximum.
* **Signed-out state.** Kept separate from offline.
* **SSE.** Headers set, a smoke script, and the polling fallback.
* **One resume mechanism:** `?cursor=` only.
* **Offline marker.** Three states, and restore from the lowest cursor among stored lists.
* **Kind map.** It covers the parent-row effects: harness mark, host counts and session questions.
* **Guard.** One predicate decides which requests skip the Bearer check.
* **Legacy.** Routes move to `/legacy/*`; inline links and existing tests are listed.
* **Tests.** The `npm test` glob now includes `tests/ui`.
* **Checks.** `app.check.mjs` runs behind a local proxy that injects the Bearer.
* **Build.** `app/build.mjs` writes `dist/app`; `app/tsconfig.json`; exact `@fastify/static` 8.x.
* **Scope cut.** Retry moves out of the slice, because no op exists.
* **Overview.** Its ops are listed.
* **Host detail.** A view is added.

## Accepted trade-offs

* **Long transactions.** One long write transaction delays the feed by its own duration. This is the cost of the watermark; feed delay is reported.
* **Safari.** It installs one PWA per origin, so G4 holds in Chromium-based browsers only.
* **IndexedDB at rest.** It is not encrypted; device encryption is assumed.
