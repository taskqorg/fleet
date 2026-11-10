# Codebase details review: fleet-lab.md (2026-11-10)

Reviewer: `egm-codebase-details`.

1. Blocker: `tests/coordinator/delegation.test.ts:12` and `delivery.test.ts:31` use `/api/experiments*` and `/api/lab/executions`. Fix: say where each endpoint moves.
2. Blocker: `execution` has no `request_id` (`schema.sql:72-100`). Fix: add column + unique index.
3. Major: missing importers: `harness.rs:59,264` (`crate::lab::validate*`), `tests/coordinator/multiharness.test.ts:1`; `main.rs` also calls `lab::prepare`, `lab::apply`, `lab::mcp_servers`.
4. Major: unlisted: `tests/coordinator/lab-packaged.ts`, `stream.test.ts:77`, `tests/ui/sw.test.ts:11`, `app.ts:27` `LEGACY_ROUTES`, `packages/coordinator/README.md:18`.
5. Major: second `cfg!(target_os = "macos")` at `harness.rs:513` (test).
6. Major: `/api/lab/*` already served by `lab.ts:18,20,25`.
7. Major: `createExecution` already accepts `configRef`; missing are `experimentId`, `variant`, `accountId`.
8. Major: name triggers `experiment_change`, `score_change`; state `seq` sequence.
9. Major: `pipe_search.py:28` hard-codes `web_search`; connection record lacks a hash field. Fix: `clientSha256`.
10. Nit: `harness.rs:307` is the `mcp__fleet__` allowlist; cite the actual search lane.

Verified correct: `artifacts.ts:45`, `execution_kind` values, `question_kind` lacks `rating`, `delivery_worker.py:98`, both v1 pipelines, listed delete targets exist.
