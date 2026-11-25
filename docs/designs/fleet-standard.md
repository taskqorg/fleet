# Fleet standard: one way per layer

Status: stages 0–11 and closing S1–S10 cleanups merged, 2026-11-21; see [What remains](#what-remains). Owner decision:
refactor every layer onto these standards first, with no new tests; tests come after, only where a check earns its
place.

## Why

A read-only review of origin/main (go/coordinator 49.7k lines, go/executor 15.2k, gateway 2.8k, update 1.9k,
app 6k, packages/ui 5k) found the same jobs done many ways:

| Concern | Variants today |
|---|---|
| Data storage | ~45 record kinds in one JSONB `document` table next to ~45 typed tables |
| Database access | 15 helper patterns, 737 raw SQL statements, 4147 `map[string]any` accessor uses |
| Transactions / locks | 7 entry points / 5 lock mechanisms; every write takes GLOBAL |
| Soft delete | 7 schemes |
| Change feed / wake | 6 mechanisms |
| Row-level security | one expression copy-pasted 23 times, 26 SECURITY DEFINER escapes |
| Operations | 179 ops via 9 registration helpers; ~96 typed, ~70 re-parsed maps, 13 raw |
| Authorization | ~10 owner/role variants, 404 vs 403 inconsistent, agent tools skip the guard on one path |
| Retries / claims | coordinator 7 + 7, executor 6 delivery schemes |
| State writers | execution.state written by 13 files, no forward-only check on 2 paths |
| Problems for the owner | 6 open/close mechanisms with opposite dedupe rules |
| MCP servers | written by hand 8 times |
| Host protocol | generated types, ~970 untyped lookups in the executor |
| App data fetching | 7 ways, ~40 hand-written invalidation predicates |

Each variant is a choice an agent must make, and each pair of variants is a place where two rules disagree.
The goal is one way per concern, written once, checked mechanically, so an agent has no choice to make.

Language and platform stay: Go, PostgreSQL, TypeScript/React. Experiment 2 (2026-11-20) showed a durable
workflow framework (DBOS) made agent work worse on this code, sqlc was neutral for agents and cut hand SQL
in `connections.go` from 56 to 4, and executable invariants caught a real defect before commit.

## The standards

**S1 Schema.** Every entity is a typed table with real columns and foreign keys with `ON DELETE` rules.
JSONB only for genuinely open payloads, in a column named `payload`. The `document` table and the `data`
mirror columns of `execution`/`task`/`question` go away. Delete = `deleted_at` column plus one generated
`live` policy; `archived_at` and similar are their own columns. Visibility = one SQL function
`fleet_visible(project_id)` used by every policy; scope changes only through one Store helper that
restores it. Migrations (`go/coordinator/migrations/`, one file each) only change tables; functions, triggers and policies live in idempotent
files under `go/coordinator/schema/` re-applied on every start, so each has exactly one current definition.

**S2 Queries.** Only sqlc (pgx/v5) from `go/coordinator/queries/<area>.sql` into package `fleet/coordinator/db`.
No SQL string literals in Go, no `map[string]any` read from the database, no `doc{}`/`astr`/`num` accessors.

**S3 Transactions.** Two entry points: `store.Read(ctx, q, fn)` and `store.Write(ctx, q, locks, fn)`.
Locks are entity keys declared by the caller (GLOBAL only by name, where truly global). Effects after
commit or rollback only through `afterCommit(q, fn)` and `afterRollback(q, fn)` (`onEnd` stays internal to
`store.go` and `tx.go`). The auth cache is fenced and cleared only by writes holding `GLOBAL` (which all
person, grant, initial-member and host writes hold).

**S4 Operations.** The contract (`contracts/ops-*.schema.json`) fully declares an operation: request,
reply, `read`, `long`, `errors`, `auth` (`person` | `project:<path>` | `holder:<kind>` | `architect` |
`owner` | `system`), `agent` (`no` | `tool` | `bound:<binder>`), `description`, optional `audit`.
Go registers each with one line `op("name", handler)` where the handler is
`func(q *Request, r *contract.XRequest) (contract.XReply, error)`. One dispatcher `runOp` serves
`/api/ops`, every MCP surface and nothing else; REST aliases are removed. Fixed rules: object not visible
→ 404; visible but role missing → 403; no person → refused unless `auth: system`. Audit through one
`audit(q, target, field, prev, next)` helper.

**S5 State machines.** Each machine (execution, task, question, experiment, command, artifact copy,
store, pipeline step, resource build/repair) has one transition table `map[from][]to` and one
`<x>Transition(q, id, event)` function, the only writer of its state column, which also runs the side
effects (totals, ended events, command stop).

**S6 Background work.** Item work (anything retried) only through the work kit: durable `attempts`,
`next_attempt_at`, `last_error` columns, registered queues with their own period, one wake channel.
Periodic passes only through `every()` in `background.go`. No `go func`, `time.Sleep`, tickers or
mutex maps elsewhere except HTTP/stream plumbing. Problems for the owner only through `problems.go`
(condition rules via `problemsSync`, one dedupe rule). Wakes only through `wake_outbox` or `afterCommit`.

**S7 Host protocol and executor.** `contracts/host-protocol.schema.json` gets a route map
(route → request, reply, long) and a typed `Command` union and `Capability` enum; one capability gate in
`go/contract`. Executor: one durable outbox for everything leaving the host, one HTTP client factory,
one outcome classifier, one `supervise.Spawn` for child processes, one goroutine helper that recovers
and reports. Files between processes (gateway snapshot, spool, update files) come from a schema.

**S8 MCP.** One `go/mcpserve` package (JSON-RPC loop, initialize, tools from the contract) used by all
eight servers.

**S2a Query cost.** Measure a query as the app runs it. For a screen, call `db_explain {view, args}` (owner or full
access): it runs the view as the caller under RLS, records its statements and answers each one's EXPLAIN (ANALYZE,
BUFFERS), with no shell on the database host. For any other query, as role `fleet_app` with RLS
(`SET ROLE fleet_app; SELECT set_config('fleet.project_ids','*',false), set_config('fleet.person','<email>',false);
SET statement_timeout='20s'; EXPLAIN (ANALYZE) …`). As postgres, RLS is bypassed and the plan can be 3-5 times faster
than the app's (runners, 2026-11-26). Under RLS an index serves a user condition only when its functions are
leakproof. Plain comparisons of timestamps, numbers and text, and `IS NULL`, are leakproof. Regular expressions, most
casts and enum comparisons are not. So a screen query chooses its rows by such columns (or reads an index-only
summary such as `execution_spend`), and only then joins and builds what it returns. Sums that a screen rereads are
kept by a pass or at ingest (`runner_call_hour`), not recomputed per read ([data layer](fleet-data-layer.md)).

**S9 App.** A screen reads one view operation (`view: true` in its contract) through `useView(name, request)`.
Other reads use `useOp(name, request)`: the cache key is the operation and its canonical request, and invalidation
comes from server-sent `touches`. Lists stay on `useListPage`. Screens never reread on focus or visibility, and a
hidden tab reads nothing. View replies carry no stored blobs (`go/contract/view_reply_test.go`). No raw `op()` reads
in views, no `any`. Contract list rows are closed types. One JSON source for presets, view grammar and harness
lists. See [data layer](fleet-data-layer.md).

**S10 Enforcement.** `go/coordinator/DESIGN_RULES.md` lists S1–S10; `design_rules_test.go` scans the
coordinator source (go/parser) and fails on SQL literals (`R-SQL`), map accessors and `map[string]any`
(`R-MAP`), goroutines, sleeps and tickers (`R-GO`), non-S3 transaction and `onEnd` calls (`R-TX`) and raw op registration (`R-OP`) in every file (the legacy
allowlist emptied with the `doc` map type and `docOf`, stage 8f). A short owner list names what one file owns (the work kit: its
loop and the retry-column writes; `background.go`: periodic passes and background helpers in its named functions, verified
alongside startup-only pass registration and kit-area bans by `TestDesignScheduling`). State-column writes outside the owner
and transition tables are checked by `TestDesignStateWrites` and `TestDesignTransitions` across all declared state machines
(`resource_build`, `resource_project_summary`, `resource_metadata_repair`, `command`, `execution`, `pipeline step`,
`task`, `question`, `experiment`, `host`, `connection_key`, `artifact_copy`, `store`, `question.cause` and `audit_log`).

## Engineering principles

The owner's standing engineering principles. They bind every attempt, beside the standards above; briefs do not
restate them. UI principles: [the principles card](../design-system/owner-rules.md#principles-card).

1. **Agentic first.** Agents get every capability through operations and MCP first (S4, S8); the web UI comes second.
2. **Help, not block.** No new CI gate for its own sake and no invented limits. A limit is a setting with a
   permissive owner default; agents get precise information, instructions and tools instead.
3. **Fix as a class.** Fix the cause for every case of its kind, never field by field. Requirements come first, then
   entities and storage, with the fewest that serve (Occam's razor). Ops mechanics (updates, host preparation,
   access) follow one written procedure, designed and approved before code.
4. **Verify at the right level.** Query cost is measured with `EXPLAIN (ANALYZE)` as the app role under RLS (S2a),
   and the change is checked live on production; no large inherited test matrices.
5. **Fleet onboards accounts.** Keys and subscriptions are connected through Fleet onboarding and its status view,
   never through a manual checklist for the owner.
6. **No low-stakes questions.** Decide numbers and mechanical options yourself and report the choice in one line;
   ask the owner only for real product decisions.
7. **Legacy data may go.** Data the system no longer supports may be deleted in a migration: count the rows and
   check the backup first; no compatibility layer for it.
8. **One home per definition.** Agents, pipelines and the catalogue change by a pull request to the repository, even
   after a hotfix save in Fleet; projects, bots and settings change in Fleet. Never copy a change by hand between the
   repository and the store ([one home per kind](fleet-definition-editors.md#one-home-per-kind)).

## How the work runs

- One stage = one pull request to `main`, merged within the stage. A stage that cannot finish is reverted,
  never left half-done.
- One worker per stage, a second only on files the first does not touch.
- Workers do not add tests. They run: build, `go vet`, the design-rules scan, and the one or two outside
  test files named in the stage. The full suite runs in CI only.
- Each worker brief has a stop condition: the stage's "done" list below.
- Owner gets one line per merged stage.

## Stages

| # | Stage | Done when | Merged |
|---|---|---|---|
| 0 | Ground: merge the silent-skip test fix (55 PG tests no longer skip), this document, DESIGN_RULES.md with S10 scan + legacy allowlist | merged; scan passes with allowlist | #675 |
| 1 | Foundations: sqlc toolchain + `db` package, `store.Read/Write`, `afterCommit`, `fleet_visible()`, schema functions split, `op[Req,Rep]` + `auth`/`agent` in the contract generator, single `runOp`, `mcpserve`, generalized work kit | new primitives used by at least one area each; old ones untouched | #676, #677 |
| 2 | Operations: every op on `op[Req,Rep]` with schema `auth`/`agent`; delete `gatedOp`, `gatedObjOp`, `shaped`, `agentOps`, `architectOps`, REST aliases | no raw/shaped ops; agent tools guarded | #679 |
| 3 | Executions, tasks, questions, artifacts, definitions, experiments on sqlc; transition owners; drop `data` mirrors | these files off the allowlist | #681, #691, #693 |
| 4 | Conversations: session, message, event, command, handoff into tables | `document` no longer holds them | #682, #692 |
| 5 | Hosts, accounts, connections, tokens into tables (reuse exp2/C); typed host protocol on the coordinator side | allowlist shrinks by these files | #687 |
| 6 | Pipelines and Lab onto the kit and transitions; delete advance goroutines and lock maps | no `go func` in these files | #689 |
| 7 | Resource projection onto the kit (reuse exp2/B), analytics on sqlc | resource files off the allowlist | #678 |
| 8 | Delivery, audit, uploads and small kinds; delete `document`; cleanup reference map from foreign keys | `document` table dropped | #694, #697, #698, #699 |
| 9 | Executor: typed commands, single outbox, `supervise.Spawn`, `mcpserve`; gateway/update error tracking | executor `jStr` count 0 | #680, #688, #696 |
| 10 | App: `useOp`, closed row types, single preset/harness source | no raw `op()` reads or `any` in views | #686 |
| 11 | Tests: delete tests of removed internals, keep invariant checks; full suite green | CI green on main | this stage's pull request |

## What remains

None: every S1–S10 standard and enforcement gap is closed.

The remaining items from Stage 11 and post-Stage 11 cleanups are all resolved:
- **Executor:** `jStr`, `jObj` and `jBoolOr` are gone from non-test code and forbidden by `E-MAP` in `go/executor/design_rules_test.go`; all five stdio MCP servers serve through `go/mcpserve`.
- **Schema:** `command.execution_id` (migration 85) and `person_setting.email` (migration 86) both carry their foreign keys again; people reload upserts and prunes removed people instead of deleting every row.
- **Coordinator S3/S5/S6/S10:** `task`, `artifact_copy` (`artifact_states.go`) and `store` (`artifact_states.go`) transition tables and write ownership are enforced by `TestDesignStateWrites` and `TestDesignTransitions`; `background.go` scheduling helpers are verified directly by `TestDesignScheduling`; `onEnd` is internal to `store.go` and `tx.go` (`R-TX`) with callers on `afterCommit` / `afterRollback`; and `Store.txLocks` fences and invalidates the auth cache only on writes holding `GLOBAL`.
- **Stage 11 fixes:** a coordinator read token is revoked when the last read of an ended token releases it (the 10 s pass stays as the backstop), a start with `"harness": null` fails as an unknown harness instead of starting codex, and a host poll answers a pending command whose stored row is unchanged from the reply it prepared before (`pollPrepared`, `host_poll.go`) instead of decoding it again.
