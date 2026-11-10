# Consolidated review: fleet-pm-agent.md

*   **Target:** [../../fleet-pm-agent.md](../../fleet-pm-agent.md)
*   **Date:** 2026-11-10
*   **Round 1 reviewers (fresh context, read-only):** zoom-out, design critic, implementation readiness. Round 2: implementation readiness on the revised draft.

## Round 1 findings and how the draft resolves them

Duplicates across reviewers are merged. Reviewer tags: Z = zoom-out, D = design critic, R = implementation readiness.

| # | Severity | Finding | Raised by | Resolution in draft |
| --- | --- | --- | --- | --- |
| 1 | Blocker | `reconcile()` runs only at startup (`app.ts:298`), not on every poll; watch would never fire | Z1, D-B1, R-B2 | New `watchPass()` on the existing placement timer tick (`app.ts:300-302`); `reconcile()` untouched (§5) |
| 2 | Blocker | `change` rows have no actor; "skip own changes" cannot be built | Z2, D-B2, R-B1 | `change.actor` column filled by `fleet_change()` from `SET LOCAL fleet.actor`, set by the agent-tools wrapper (§1, §5) |
| 3 | Blocker | App operations (tasks, settings) are not reachable from `bound()`; tool list publishes only agent ops | Z3, D-M5, R-B3 | `deps.ops` route plus role-based `tools/list`; exact deny list (§1) |
| 4 | Blocker | `task_update` supports only four fields and accepts `action` | R-B4 | Whitelist = existing four fields; `action` is 400 (§3) |
| 5 | Blocker | Director scope mechanism undefined; `bound()` already runs inside `store.tx` | R-B5 | Scope and actor set in the `/api/hosts/agent-tools` wrapper; audit via `setting-change` with `on_behalf_of` (§1) |
| 6 | Blocker | Held invisible in list query, `where()`, counts, group order, view-URL codec | R-B6 | All specified in §4 "Views"; `view-url.ts` added to files |
| 7 | Major | Change-log resync and pruning unhandled; new watch has no start cursor | Z5, D-M2, R-M5 | Cursor initialised at head; resync gives one message; paging by 500 (§5 steps 1-2) |
| 8 | Major | Watch filter kinds do not map to change kinds; rows carry no state | D-M1, R-M5 | Kind table; classification reads the current row, race accepted (§5) |
| 9 | Major | Row-level security on `change` hides project-less rows except under `*` | D-M3 | Each watch runs with its director's scope; host rows owner-only (§5 step 3) |
| 10 | Major | Child finish wakes the PM twice (`wake()` and watch) | D-M4 | Watch drops changes from child sessions (§5 step 4) |
| 11 | Major | No Risks section; prompt injection into an owner-rights PM | D-M6 | Risks section added; Q7 opened for owner |
| 12 | Major | `createdBy` shape clashes with `session:<id>` | D-M7 | `execution:<id>` (§3) |
| 13 | Major | Held transition rules incomplete | D-N1, R-M1 | Rule table with 409 messages (§4) |
| 14 | Major | Two-step held create | Z6, R-M2 | `create({held})` inserts `paused` directly (§4) |
| 15 | Major | Focus over-specified (`taskFilter`, `defaultProjectId`) | Z4, R-M4 | Focus reduced to `projectIds` (§2) |
| 16 | Major | `session_create` is in `app.ts:88`, not `sessions.ts`; child director not set | R-M3, R-M4 | Files corrected; `start()` copies `directed_by` (§2) |
| 17 | Major | Busy PM gets N queued messages | D-N4 | Skip while a `watch:` message is queued (§5 step 8) |
| 18 | Major | Tests and UI files not concrete | R-M6, R-M7 | Three named test files with cases; UI files named |
| 19 | Nit | Alternatives D-G lack trade-offs | D-N2 | Pros and cons added |
| 20 | Nit | `wake()` error change out of scope | Z8 | Removed |
| 21 | Nit | "Register document kind" not needed; view line numbers off by one | R-N1, R-N2 | Removed; line refs dropped from file list |

## Round 2

See [readiness_round2.md](readiness_round2.md).

## Open for the owner

Q1-Q7 in the design carry defaults. Q7 (confirmation for people and grant changes made by an agent) is the one with security weight.
