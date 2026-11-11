# Fleet language trial (L1)

The trial that chose Go: the slice, its requirements and the measurement protocol. Split from [fleet-stack.md](fleet-stack.md) on 2026-11-11.

## Trial (L1)

The language was chosen by porting one coordinator slice to Go and to Rust and measuring both against the TypeScript coordinator with the same outside tests and script. Go and TypeScript tied on edit-to-green; Rust's one-line rebuild was about 14 times slower; Go has 11 packages in `go.sum` against 156 (TypeScript) and 172 (Rust) in the whole lockfile; both ports reached green inside the time box with at most one build-fix cycle. Numbers: [evidence](../../evidence/fleet-stack.md#language-trial). The Go port is `go/coordinator`; the Rust port was not merged.

The slice and the protocol are the template for the coordinator's outside tests.

### Trial slice (L1)

Requirements SL1-SL11 are the slice's own. Each has one outside test in `tests/trial/slice.test.ts` whose name starts with its identifier.

| ID | Requirement | Risk the test guards |
|---|---|---|
| SL1 | A browser request carries `Authorization: Bearer <browser secret>` (exact secret, `Bearer` scheme only) and a known `X-Fleet-Person`; otherwise 401, or 403 for an unknown person. | Authorization header parsed loosely |
| SL2 | A host request is authorized by its own host secret only. An unknown or missing secret is answered 401, never a redirect or another status; a host request carrying `X-Fleet-Person`, or a body `hostId` of another host, is 403. | The executor outbox keeps a row only on 3xx and 401 (`go/executor/outbox.go`): any other answer drops or completes work it should retry; host impersonation |
| SL3 | `POST /api/sessions` creates a session directed by the caller; `GET /api/sessions/:id` returns it with `sessionId`, `directedBy`, `messages` and `executions`; an unknown id is 404. | Session row not committed or wrong shape |
| SL4 | `POST /api/sessions/:id/messages` with exactly `requestId` and `text` (optional `delivery`) queues one execution with no host yet; the same `requestId` returns the same execution; any other body is 400 and writes nothing; an unknown session is 404. | Duplicate work on a client retry |
| SL5 | `POST /api/hosts/poll` records the host observation; a poll of the default host (`FLEET_HOST_ID`) claims a queued execution and creates its `start` command; a poll returns the pending commands of the polling host only. | Work sent to the wrong machine |
| SL6 | `POST /api/hosts/receipts` (status receipt) moves the command and execution forward only: a regression is 409, an invalid status 400, a repeated terminal receipt returns `{ok: true}` and changes nothing, another terminal status is 409, a command of another host is 403. | Outbox retries and reordering corrupt state |
| SL7 | `POST /api/hosts/events` stores each event once with its `data` unchanged; `GET /api/executions/:id/events` returns them in `seq` order. A redelivered batch changes nothing; the same id with other content, a taken `seq`, or an invalid identity refuses the whole batch (409, 409, 400); another host is 403. | The `item`/`data` rename; a partial batch |
| SL8 | `GET /api/stream` (SSE, `text/event-stream`) sends `ready` with a cursor, then every committed change after it in order with `id:` equal to its `seq`; `?cursor=<seq>` replays exactly the changes after that seq; an invalid cursor is 400. Live changes are woken by `LISTEN fleet_change`. | Lost or repeated live updates |
| SL9 | A person without grants sees no row outside their scope, by read (404) or by stream (no change frames): row-level security with `fleet.project_ids` per transaction. | Scope not applied |
| SL10 | Messages written at once on many sessions each get their execution; one `requestId` sent at once yields one execution; none fails. | Transaction and scope-lock layer under concurrency |
| SL11 | Event batches of one execution sent at once all land, with no `seq` lost. | Execution-scope lock and serialization retry |

**Routes.** Only the parts named here are in the slice; a port may answer anything else, and no outside test reaches it.

| Route | In the slice | Out of the slice |
|---|---|---|
| every route | the `onRequest` credential check (SL1, SL2), the host `preHandler` body `hostId` check, the per-route transaction (read snapshot for GET, one GLOBAL write otherwise, execution scope for `/api/hosts/events`), RLS scope per person, 42501 as 403 | origin checks (`FLEET_BROWSER_PROXY_AUTH`, `Origin`), public app routes, architect-role routes |
| `POST /api/ops/hosts_register` | owner registers a host with a minted secret (test setup) | every other operation |
| `POST /api/sessions`, `GET /api/sessions/:id` | SL3 | `queued`, `questions`, `handoffs`, `transitions`, `forks`, `delegations` may be empty or absent |
| `POST /api/sessions/:id/messages` | SL4: no profile, no account, no project; harness `codex`; the execution waits for dispatch | queueing a second message while an execution is active; profiles, accounts, projects, delivery |
| `POST /api/hosts/poll` | SL5, the `host-observation` write, and the dispatch claim (`go/coordinator/dispatch.go`) in its simplest case: no subscriptions, the default host polling | ranking over subscriptions and hosts, budgets, wait reasons and stall questions, delegation reconcile, problem detection, promotion, store observations, project trust |
| `POST /api/hosts/receipts` | the status receipt (SL6) | the artifact gate on `completed`; native session capture; pipeline and delivery follow-ups |
| `POST /api/hosts/events` | SL7, SL11 | transport-conflict records and recovery; harness version, child agents and totals |
| `GET /api/executions/:id/events` | SL7 | |
| `GET /api/stream` | SL8, SL9 | ping, pruning, the `close` frame |

**Tables and kinds.** `document` (kinds `session`, `message`, `command`, `host`, `host-observation`, `event`, `event-delta`), `execution`, `change` (written by the `fleet_change` triggers, which also send `NOTIFY fleet_change`), `person` and `person_grant` (mirror of the people file), `schema_migration`. The schema is `go/coordinator/schema.sql`; RLS policies and triggers come from it.

**Port layout.** Go: module `go/`, binary package `go/coordinator`, built to `go/bin/fleet-coordinator` (ignored by git), host protocol types in `go/contract` (generated by `go/gen`). The binary serves on `127.0.0.1:$PORT` with this environment: `FLEET_DATABASE_URL` (role `fleet_app`), `FLEET_MIGRATION_DATABASE_URL` (role `fleet`, which applies `schema.sql` at start in the same `-- migration N` steps and records them in `schema_migration`; migration 6's JavaScript document move is a no-op on an empty database), `FLEET_PEOPLE_FILE`, `FLEET_BROWSER_SECRET`, `FLEET_HOST_ID`. The measured arm is `tests/trial/arms/go.json`.

**Runner.** `node tests/trial/run.ts` is the one command. It starts, once per machine, a local PostgreSQL in `~/.cache/fleet-trial/pg` (trust authentication, Unix socket only, port 54329, left running for other worktrees), creates a fresh database for this worktree with `deploy/infrastructure/fleet-postgres.sql`, starts the coordinator under test on a free port (`tests/harness.ts`; `FLEET_TRIAL_SERVER`, default `go/bin/fleet-coordinator`), runs the tests with the base URL and secret in the environment, stops the server and drops the database. Tests are TypeScript on Node's test runner, standard library only, and reach the server only over HTTP and SSE.

**Shared-server rule.** Change visibility uses `pg_snapshot_xmin(pg_current_snapshot())`, which spans every database on the server, so a transaction open in another database holds committed changes back and no second `NOTIFY` arrives when it ends. `changes.since` reports `held`, and the stream looks again after 250 ms (`heldWait` in `go/coordinator/stream.go`). The same stall applies in production behind any long transaction that writes no change row.

**Planted defects.** The trial planted these defects in the TypeScript coordinator, since deleted; each turned its test red.

| ID | Defect | Red test |
|---|---|---|
| P1 | Event `data` stored as `item` (the `item`/`data` seam rename) | SL7 |
| P2 | Unknown host secret answered 403, not 401 (outbox rule) | SL2 |
| P3 | Unknown host secret answered 200 with an empty poll (outbox rule) | SL2 |
| P4 | Events written one by one while the batch is still checked | SL7 |
| P5 | Write routes without their transaction, message route without its own | SL10 |
| P6 | Write transactions take no scope lock | SL10 |
| P7 | Events returned in insertion order, not `seq` order | SL7, SL11 |
| P8 | Stream resume replays the change at the cursor (`>=`) | SL8 |
| P9 | Any authorization scheme accepted before the secret | SL1 |
| P10 | A host request may carry a person | SL2 |
| P11 | A body `hostId` of another host silently replaced | SL2 |
| P12 | A poll returns pending commands of every host | SL5 |
| P13 | A person without grants works in the operator scope | SL9 |
| P14 | Receipt status regression accepted | SL6 |
| P15 | SSE change frames without an `id:` line | SL8 |
| P16 | SSE served as `text/plain` | SL8 |
| P17 | LISTEN on the wrong channel | SL8 |

Not covered by an outside test: the advisory lock alone (it matters only for a second process; the slice runs one), the `held` re-read (it needs a transaction open in another database), and removing only the message route's own `store.tx` (the per-route transaction already covers it).

Covered only by old tests: everything out of the slice above; PGlite as a store; nested transactions and savepoints; serialization and deadlock retry with backoff; data moves of migrations 1-17; change-log pruning; profiles, accounts, projects, tasks, delegation, pipelines, artifacts, lab, threads and the agent tools.

### Trial measurement protocol

`node tests/trial/measure.ts <arm> [--runs 5] [--worktrees 4]` measures one arm the same way for every language. The arm file names `setup` (per fresh worktree), `clean`, `check` (the command run before the outside tests), `build` (check plus the release artifact), `edit` (file for the one-line edit), `server`, `artifact`, `runtime`, `lockfiles`, `roots` and `disk`. The script creates N fresh git worktrees of `HEAD` under `$TMPDIR`, runs `setup` in each, and per run records:

- cold build (`clean`, then `build`), warm build (`build` again), rebuild after appending one comment line to `edit`;
- peak resident size of the largest compiler process (`/usr/bin/time -l`) and of the server's process group during the outside tests (sampled every 100 ms by `run.ts`);
- outside-test time of `run.ts`, including setup and teardown;
- edit-to-green with N worktrees at once: the same edit in each, then `check` and `run.ts`, timed per worktree;

and once: artifact and runtime size, disk per worktree (`du -sk` of the `disk` paths), third-party packages in each lockfile, whole and as the closure of `roots`.

Results are medians and min-max spreads over green runs; a difference inside the spread is a tie. Each run records the 1-, 5- and 15-minute load averages at start and end, and is marked `comparable=false` when the 1-minute load before the script exceeded half the logical CPUs or a process outside the script's tree used more than one core. The script refuses nothing. Build-fix cycles are counted by the porting agent: one cycle is a failed build or `run.ts` run followed by an edit.
