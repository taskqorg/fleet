# Stage 9: Implementation readiness (fleet-operator-app.md, DetailedRange)

Reviewers: egm-implementation-readiness, egm-codebase-details and egm-style. Verdict on the first draft: not ready. The review found 7 blockers, 14 majors and 12 nits.

## Blockers

| # | Finding |
|---|---|
| B1 | Migration 7 already exists in the schema. |
| B2 | The trigger used `NEW.seq` (the source row's sequence, not the change row's), was NULL on DELETE, and sent a non-text payload. |
| B3 | Two keys were undefined: `task_dependency` has no `id`, and tables holding several kinds need `legacy_kind`. |
| B4 | The migration splitter cuts a multi-line plpgsql function apart (`store.ts:117`). |
| B5 | The GET read-snapshot wrapper would wrap the stream (`app.ts:31-36`). |
| B6 | There is no settings path for retention. |
| B7 | The `task_list` and `list_membership` contracts were undefined, and `started_at` does not exist on tasks. |

## Majors

| # | Finding |
|---|---|
| M1 | The RLS scope is per store, not per request. |
| M2 | The `listen` backend details were missing. |
| M3 | `/api/me` had no source for the email, because the guard forwards no identity. |
| M4 | Outside proxy mode the app has no credential, and `EventSource` cannot send headers. |
| M5 | The legacy move was underspecified: missing route files, the bypass predicate, and old-path behaviour. |
| M6 | Inline legacy links were not listed. |
| M7 | Existing tests and browser scripts that use the old paths were not listed. |
| M8 | The `tests/ui/*.test.ts` files never run in `npm test`. |
| M9 | The overlap window could not be turned into a query, and the resync rule was stated three different ways. |
| M10 | `?cursor=` and `Last-Event-ID` contradicted each other. |
| M11 | No kind map existed for `event` and `host-observation`. |
| M12 | The `hosts_list` contract was missing. |
| M13 | Build wiring was open: copy or no-op, the tsconfig, `FLEET_APP_DIR`, and the `@fastify/static` version. |
| M14 | Session and host membership, the host detail view and the retry op were missing; the UX check had two owners. |

## Nits

`NAV` versus `workspaces`; no change needed to `ci.yml`; only `@fastify/static` is a new dependency; spec order; list the ops explicitly; how the cursor advances; which coordinator prunes; function names; one term per concept; Sessions versus Conversations; the D15 prefix; wording of the verification plan.
