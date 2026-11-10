# Consolidated EGM review: fleet-lab.md (2026-11-10)

Reviewers run in parallel on revision 1: zoom-out, comprehension, design critic, codebase details, implementation readiness. Stage order of `egm-review` was compressed into one pass (worker run, no human checkpoints); structural, style and inventive catalyst were not run on the design (they ran twice on the brief).

## Applied in revision 2

* `execution.request_id` with partial unique index, insert on conflict, per-experiment advisory lock (zoom-out 1, critic 1, codebase 2, readiness 1).
* `attempt` defined; owner cancel cell and retry cell; cancelled counts as final (critic 2, 3; readiness 2).
* One accounting path: `connection_call.py` CLI and MCP entry points both write `.connection-calls.jsonl`, uploaded as `tool_run`; pull-back from Pipe removed (zoom-out 4, critic 5, readiness 4).
* All Lab executions carry `experiment_id` and count toward budgets (critic 4).
* Budgets narrowed to experiment scope and measurable units (zoom-out 5).
* Rating queue opened by the owner; automatic sampling deferred (zoom-out 6).
* Examples stored as artifacts; use cases stay repo files (zoom-out 7).
* Promote produces a catalogue patch and reuses `recommendation_set` (zoom-out 3, critic 7).
* Infra failures on `execution.problem` (zoom-out 8); `priority`, `iteration` columns dropped (zoom-out 9, critic 9).
* `no_gain` formula and primary evaluator (critic 6, readiness 2).
* Pipeline v2 reference grammar, `output.json`, job ID storage and resume (readiness 3).
* Full migration 11 DDL, named triggers, RLS (readiness 6, codebase 8).
* Removal list completed: Rust importers, extra tests, `LEGACY_ROUTES`, README, second `cfg!` site, old `/api/lab/*` routes and their replacements (codebase 1, 3-7).
* `clientSha256` pin (codebase 9); `direct_search` cited as the old lane (codebase 10).
* Stage ordering fixed: MCP wiring moved to Stage 2, routes to Stage 3, S0 exit uses an SQL fixture (readiness 7).
* Missing files and tests listed (readiness 8, 9); skills install per harness (readiness 10).
* Glossary, round-3 decision table, Q9 explained, N = 2 typo, generator/allocator defined (comprehension 1-7, 10).

## Not applied

* Split loop and WSL into separate docs (zoom-out 2): kept as Q4 for the owner; Stage 4 gated on Stage 3 results.
* Line-level detail inside the Technical Plan (zoom-out 10): partly moved; some kept for traceability.
* Route schemas are summary-level, not full JSON Schema (readiness 5): judged enough for a prototype.
