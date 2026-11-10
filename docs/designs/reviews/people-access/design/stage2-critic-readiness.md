# Stage 2 review: design critic, structural and style, implementation readiness and codebase details

Targets: `docs/designs/fleet-people-access.md`, `docs/designs/fleet-connections.md` after the stage 1 fixes.
Reviewers (Goldfish, fresh context): `egm-design` (feature spec), `egm-structural` with `egm-style`, `egm-implementation-readiness` with `egm-codebase-details` (detailed plans against `origin/main` `f69b5dc` and assetq `lane/`).
EGM stages 7 and 9 were run together; stage 8 was folded in.

## Findings and disposition

| # | Sev | Finding (reviewers) | Disposition |
| --- | --- | --- | --- |
| 1 | Blocker | Two polls can claim one execution (design) | Fixed: `FOR UPDATE SKIP LOCKED` plus null-guarded `host_id` update |
| 2 | Blocker | Forged `X-Fleet-Person` on machine routes; credential kinds can mix (design, readiness) | Fixed: site-level strip, `@machine` matcher `path /api/hosts/*`, host-secret requests with the header are 403, coordinator on loopback |
| 3 | Blocker | C1 needs `dispatch.ts`, which slice 6 creates (structural, readiness) | Fixed: C1 mints tokens in `enqueue`; slice 6 moves the call |
| 4 | Blocker | `rg -i pipe` matches "pipeline" (readiness) | Fixed: specific pattern |
| 5 | Blocker | Renaming `connection_call.py` breaks `include_str!` and tests; `fleet call` has no executable (readiness) | Fixed: file keeps its name; exact command lines; dependants listed |
| 6 | Blocker | Gateway copy omits coreq telemetry, Tavily-only provider, snapshot schema, Cargo workspace (readiness) | Fixed: coreq removed, new spool writer with path, catalogue in snapshot, schema written, root `Cargo.toml` listed |
| 7 | Blocker | Scope is per pool, not per request (readiness) | Fixed: `store.tx/read/atomic(scope, fn)` with `SET LOCAL`; machine and timer scope stated |
| 8 | Blocker | Host-secret change misses `onRequest`, `/api/hosts/accounts`, delivery worker, inspector mapping, `FLEET_HOST_TOKEN`, fixtures (readiness) | Fixed: files and variables listed; host `roles`; bootstrap cut-over script |
| 9 | Major | Neither doc has Risks & Mitigations (structural, design) | Fixed: added to both |
| 10 | Major | One host may win the whole queue; proposed optional capacity setting (design) | Rejected: owner rule forbids concurrency caps; recorded as a risk, intake and order list are the tools |
| 11 | Major | Run token readable by same-user runs; no expiry (design, structural) | Fixed: threat model stated; token removed on end, lease loss or revocation |
| 12 | Major | Grant revocation does not reach delivered work (design) | Fixed: stop runs, drop tokens, question to rotate delivered keys |
| 13 | Major | Owner login on someone else's host exposes it (design) | Fixed: explicit warning "Simon can read this login", not a ban |
| 14 | Major | Automatic take is A2 renamed; knock-on effects (design) | Fixed: `requested_by`, budget and question effects stated, terms note with section cite |
| 15 | Major | Key failover can replay charged calls (design) | Fixed: failover only for `idempotent` tools |
| 16 | Major | Budget overshoot unstated (design) | Fixed: bound stated in §6 and Risks |
| 17 | Major | Problem vs question for disabled keys (structural, design) | Fixed: question with cause `key:<id>:disabled` |
| 18 | Major | Decisions table incomplete (structural) | Fixed: rows 6, 7, 8 |
| 19 | Major | ACP work and harness placement unscheduled or spread over slices (structural) | Fixed: slice table rows 2, 4, 8 |
| 20 | Major | "Rejected by author" without link; A2, A4 reasons (structural, design) | Fixed: brief links and technical reasons |
| 21 | Major | Connection `access` semantics (structural) | Fixed: use rule, visibility rule, Tavily seed |
| 22 | Major | Callers of `pickAccount`, `resolve`, `preferred` missed (readiness) | Fixed: `agent-tools.ts`, `delivery.ts` listed |
| 23 | Major | Queued host-less executions vs schema and `detectProblems` (readiness) | Fixed: `queued` state, partial index, `execution.wait_reason`, `detectProblems` skip |
| 24 | Major | Schema underspecified: migrations, backfill, RLS (readiness) | Fixed: migrations 13-17 with policies and backfill |
| 25 | Major | Actor rewrite list incomplete (readiness) | Fixed: every line listed |
| 26 | Major | Owner identity undefined (readiness) | Fixed: `owner: true` in `people.json` |
| 27 | Major | `pipe-read` names in pipelines and profiles (readiness) | Fixed: renamed `tavily` in C1 |
| 28 | Major | UI items name no files (readiness) | Fixed: `app/src` files, routes, components |
| 29 | Major | Missing modules `recovery.ts`, `executions.ts`, `sessions.ts` (readiness) | Fixed |
| 30 | Nit | npm vs pnpm, pytest paths, line ranges, wording of slice 1 check, cross-references (structural, readiness) | Fixed |
| 31 | Nit | Host identity alternatives (design) | Fixed: A7 per-host service token or mTLS |
| 32 | Nit | Snapshot reread storm (design) | Fixed: one reread in flight |
