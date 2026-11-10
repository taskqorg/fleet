# Implementation readiness, round 2: fleet-pm-agent.md

Fresh-context implementation-readiness reviewer on the revised draft. People-access slices 1-4 accepted as prerequisites.

| # | Severity | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | Blocker | A non-owner PM session with no `projectId` has `project_id = NULL`; row-level security hides its session, watch and execution rows under the director's scope, so CUJ 1 fails at the first call | Trusted coordinator code reads and writes session-own rows (session, focus, watch, child list) under `*`; only the requested operation runs under the director's scope (§1). Test in `pm-focus.test.ts` |
| 2 | Major | Own-change filter needs an execution lookup hidden by RLS | `fleet.actor = session:<sid>/execution:<eid>`; prefix match, no lookup (§5 step 4) |
| 3 | Major | `deps.ops` is circular: `ops` built after `agentTools` (`app.ts:148` vs `:338`) | Getter `deps.ops: () => ops`; agent names win |
| 4 | Major | Agent could forge or skip `createdBy`/`held` | `bound()` strips them and passes a trusted `ctx`; test added |
| 5 | Major | No P3 or CUJ 3 tests; migrations are sections in `schema.sql`, not files | `pm-focus.test.ts`; setting-change case; appended `-- migration N` section |

All round-2 findings are applied in the design. No further review round was run.
