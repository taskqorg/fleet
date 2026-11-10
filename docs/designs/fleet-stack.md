# Fleet stack and language

Fleet's languages, toolchain, contract source, rule delivery and the order in which the services move to Go. The binding rules are in [`fleet.md`](../../fleet.md#stack); this file holds their detail. Executor release files are a separate topic: [executor delivery](fleet-executor-delivery.md). Measured numbers are in the [stack evidence](../../evidence/fleet-stack.md).

## Decision

Every Fleet service is written in Go. The browser app stays TypeScript (Preact, esbuild). Reasons and compared options: [why this stack](#why-this-stack).

Order:

1. The coordinator (`go/coordinator`), rewritten in one pass ([coordinator rewrite](#coordinator-rewrite)).
2. The executor (`crates/executor`) and the gateway (`crates/gateway`) together, after the coordinator ([executor and gateway](#executor-and-gateway)). Until then both stay Rust.

Never port two components at once.

Today: the coordinator is Go (`go/coordinator`, one binary); the executor and the gateway are Rust; the probe spikes are in `go/probe/`.

## Why this stack

Reference for reconsidering a choice. Numbers: [evidence](../../evidence/fleet-stack.md); trial on an Apple M2 with 8 CPUs, 4 worktrees at once.

The owner's first goal is fast agent work in parallel worktrees: the Mac must stay usable with several agents at once. Speed is equal for Fleet's workload (outside tests about 1.4 s in every arm), so developer and deploy convenience decided.

| Choice | Compared with | Deciding facts |
| --- | --- | --- |
| Go for services | Rust | Per fresh worktree Rust costs a 39 s cold build, 465 MB disk, 594 MB compiler memory; a one-line rebuild 7.7 s against 0.54 s. Go: 6.7 s, 191 MB, 395 MB. |
| Go for services | staying on TypeScript | Equal edit-to-green (2.6 s against 2.8 s, inside the spread); 11 third-party packages against 146 in the coordinator closure; one build from any machine for every platform, no C toolchain; server 21 MB against 146 MB. |
| One language, executor included | Rust executor kept | One toolchain, build job and dependency policy. Lost: Rust's stricter compiler and a smaller binary (executor 5.9 MB against about 10 MB). The Rust slice port needed 0 build-fix cycles, Go 1. |
| Full gate only in CI | local full gate | Local gates by several agents drove the Mac to load average 223 on 8 cores; one gate took 339 s against the 180 s budget (#103). |
| JSON Schema contracts | `schema.sql` with sqlc; protobuf; OpenAPI | Seam bugs (`ev.item`/`ev.data`, outbox 3xx/401) live in `jsonb` payloads and the table-less host protocol, where `schema.sql` types are raw bytes. protobuf adds `buf` and changes JSON names; OpenAPI cannot describe SSE or host-side events. |
| Standard library first; pgx; own SigV4 | `database/sql`; `minio-go` | No stdlib Postgres driver, and `database/sql` hides LISTEN. `minio-go` compiles in 19 modules, the signer 0. |
| `modernc.org/sqlite`, purego (executor) | stdlib journal; cgo | Journal: hand-written queries and cross-process waits, cannot read existing files (breaks Python tests, in-place upgrade), 200 commits/s against 14k/s, no Windows build. modernc lost 0 acknowledged commits under kill -9; purego reaches libproc without C. |
| One-pass rewrite by four parallel areas | Go front door proxying to TypeScript | No live users and no data to keep: the speed of development decides (owner, 2026-11-11). A proxy would keep two coordinators and their shared-lock ordering alive. |

## Delivery order and change cycles

- Build the operator and architect experiences on the web first. Desktop packaging for Windows, macOS and Linux follows later and reuses the web UI and the shared operations; it adds no second business-logic implementation.
- Web interface, coordinator and execution hosts change on their own cycles. A UI-only edit rebuilds no executor. Updating the web interface cancels no remote work and restarts no executor. A coordinator update follows the recovery contract ([execution continuity](../execution-continuity.md)).
- Interfaces between independently updated components are versioned enough to expose incompatibility. Not built: host protocol versioning ([open questions](#open-questions)).
- A stack choice is made from measured edit-to-green and edit-to-deploy time, supply chain, maintainability and deploy effort, and is recorded in `fleet.md` before code changes.

## Rule delivery

- `AGENTS.md` sends every agent attempt to `fleet.md` and the specq shared core at a pinned commit.
- `fleet.md` names one merge-gate command and a diagnostic time budget of 180 s on an idle Mac ([verification](../../fleet.md#verification)). The full gate runs only in CI ([why](#why-this-stack)).
- CI runs specq `contract_lint.py` on `fleet.md` and `scripts/test-rule-report.ts`, which reports tests that name no requirement and risk as a warning. Nothing fails because a test lacks a reference.
- Not built: a `fleet` project in `config/projects/`, so that specq rule delivery also applies to agents Fleet itself dispatches to work on Fleet.

## Toolchain

The labels D1-D4 are cited by code comments. Each stays while the TypeScript or Rust code it governs exists.

### D1. Node 22 and pnpm

- Node 22 (`engines.node` `>=22.18`). Bun and Deno were rejected: they broke tests and did not beat pnpm on install time or disk ([evidence](../../evidence/fleet-stack.md#toolchain-measurements)).
- pnpm 10, pinned in `packageManager`. `pnpm-workspace.yaml` sets `minimumReleaseAge: 1440` (one day; seven days rejected the pins of the switch) and allows install scripts only for `esbuild`. Every version is pinned.
- Imports use `.ts` specifiers. `tsconfig.json` sets `noEmit`, `allowImportingTsExtensions` and `erasableSyntaxOnly`; tests and scripts run with Node's native type stripping, without `tsx`.
- The type check is TypeScript 7 (`typescript` 7.0.2, the native compiler) in `pnpm run typecheck`. The app build is esbuild (`app/build.mjs`).
- Holds for the app, the browser checks and the outside tests.

### D2. Shared Rust target per machine

- Each machine sets `CARGO_TARGET_DIR` in its environment; nothing absolute is committed, so CI keeps the default `target`. `orca.yaml` runs only `pnpm install --frozen-lockfile`.
- `Cargo.toml` sets `[profile.dev] debug = "line-tables-only"`.
- Code that finds the executor binary resolves the target directory through `cargo metadata` `target_directory`: `tests/executor/executor-binary.ts` and `tests/executor/executor_binary.py`.
- Cargo locks the shared directory, so parallel worktree builds queue. sccache was rejected: one more tool, still 300 MB per worktree.
- Ends when the executor and the gateway are Go.

### D3. Own S3 client

`go/coordinator/d_s3.go` signs with SigV4 on `crypto/hmac` and `crypto/sha256`; no S3 module ([third-party Go modules](#third-party-go-modules)). It keeps the Garage setup: region `garage`, path-style URLs, no request or response checksums, one client per store and route, Access headers and `Accept-Encoding` added after signing (Access headers only on an allowed tunnel route), no redirects. It covers Put, Head, Get with Range, Delete, Copy, the multipart operations and UploadPartCopy, presigns GET and UploadPart, and retries 3 times with jittered backoff on 429, 5xx, throttling codes and network errors. An idle timeout (`FLEET_S3_IDLE_TIMEOUT_MS`, default 60 s) replaces a total request cap. `go test` checks the AWS GET Object vector.

The TypeScript-era label D4 (`node:sqlite` and the bundled Node server) ended with the TypeScript coordinator.

## Contracts (L3)

Two sources, each owning one seam ([why](#why-this-stack)):

- `packages/coordinator/db/schema.sql` owns table rows.
- `contracts/host-protocol.schema.json` (JSON Schema) owns the host protocol (`/api/hosts/poll` request and reply, status receipts, event batches) and event payloads.

Rules:

- Types are generated at build time and never committed: TypeScript by `contracts/gen-ts.mjs` (run by `pnpm run typecheck` and `pnpm run build`), Rust by `crates/executor/build.rs`, Go by `go/gen` (`go generate`). `cargo build` and `go build` need no Node.
- In-repo generators, not `json-schema-to-typescript` and `typify`, which add 15 npm modules and 30 crates (and typify is 0.x); the off-the-shelf output was checked first and typed the union correctly, so only supply chain decided. All generators accept only the keyword subset in [`contracts/README.md`](../../contracts/README.md) and fail on any other keyword, so a schema never yields a silently weaker type.
- `EventData` is an open object with named fields (`item`, `fleetNormalized`, `harnessVersion`, `children`); unnamed fields pass through (Rust `rest`). A closed union by `type` cannot cover native harness records, whose `type` is their own.
- Types only, no generated runtime validator: the coordinator's hand checks at the three host routes stay the trust-boundary validation.
- Command `payload` stays an open object; a command kind gets a schema when it is next changed. Other HTTP routes get schema types when they are next changed.
- The outbox and store answer share one status table, `outbox::class(status, host_token)` (`crates/executor/src/outbox.rs`): a redirect is an edge condition; 401 is an edge condition for the coordinator and a rejection for a store; 429 and 5xx retry; other 4xx are rejected.

The generators were checked against off-the-shelf output for the transcript event union before the choice ([`evidence/l3/`](../../evidence/l3/)).

## MCP servers as executor subcommands (L4)

- The executor starts delegation and design-rules as `fleet-executor mcp <name>` (`crates/executor/src/mcp.rs`, `profile.rs`); no Python process serves them. `tests/executor/mcp-replay.test.ts` replays the stdio exchanges captured from the former Python servers (`tests/executor/fixtures/mcp.json`).
- Connection MCP stays `python3 connection_call.py` until Connections replaces it ([connections](fleet-connections.md)); `delivery_worker.py` likewise. `deploy/infrastructure/origin_guard.py` stays: it guards the origin independently of MCP. The remaining Python and its removal conditions are listed in the [executor README](../../crates/executor/README.md#remaining-python).

## Go code

The rules are in [`fleet.md`](../../fleet.md#stack): standard library first, `CGO_ENABLED=0`, one module `go/` with one `go.sum`, `-mod=readonly`, the checksum database on, `govulncheck` as a warning, contract types from `go/gen`, `-trimpath` release binaries from one Linux job. The schema is embedded in the binary: `go generate` copies `packages/coordinator/db/schema.sql` (the schema source, unchanged) next to `d_schema.go`; the copy is not committed. The CI job `go` vets, runs `go test` and cross-builds `go/coordinator` for linux/amd64, darwin/arm64 and windows/amd64, checks each binary for `CGO_ENABLED=0`, writes a SHA-256 file per binary and uploads the workflow artifact `fleet-go-<sha>`. Nested modules under `go/probe/` are outside that module and outside CI.

### Third-party Go modules

Every module that Go code imports has a row here.

| Module | Replaces | Why the standard library does not cover it |
| --- | --- | --- |
| `github.com/jackc/pgx/v5` (closure: `pgpassfile`, `pgservicefile`, `puddle/v2`, `x/crypto`, `x/sync`, `x/text`) | `pg` | The standard library has no Postgres driver. `database/sql` would still need a driver and hides LISTEN, connection startup options and the simple protocol for multi-statement migrations. |

Not added:

- ClickHouse: its HTTP interface through `net/http` when Fleet needs it; the coordinator has no ClickHouse code.
- S3 (Garage): a hand-written SigV4 signer on `crypto/hmac` and `crypto/sha256`. `minio-go` v7.4.1 would compile in 19 third-party modules (26 in `go.sum`); the signer adds 0.
- `govulncheck` is a tool run at a pinned version, not a module of Fleet's code.

Allowed for the executor port (Not built: no Go code imports them yet):

| Module | Replaces | Why the standard library does not cover it |
| --- | --- | --- |
| `modernc.org/sqlite` | `rusqlite` with bundled SQLite | SQLite without cgo; keeps `journal.sqlite`, its SQL and WAL across processes. 25 modules in `go.sum`, 10 linked. |
| `github.com/ebitengine/purego` | `libc` `dlsym` | Calls macOS libproc (`proc_pidinfo`, `proc_signal_with_audittoken`) without cgo; no dependencies of its own. |
| `golang.org/x/sys` | `libc` | `Openat`, `Statfs`, `SysctlKinfoProc`. |

## Coordinator rewrite

The coordinator was rewritten in Go in one pass (owner direction 2026-11-11: no live users and no data to keep). There was no reverse proxy, no mixed TypeScript and Go setup, no fallback switch and no data migration. Behaviour that looked like a bug was fixed and noted in the pull request of its area.

- Four areas worked in parallel in `go/coordinator`. Each area's files are named `<area>_*.go`. Routes register from `init()` through `registry.go`: `route`, `longRoute` (no surrounding transaction), `publicRoute` (before the credential check), `operation` (`/api/ops/{name}`) and `onStart` (background tickers).
- The seams were `packages/coordinator/db/schema.sql` and `contracts/*.schema.json`; both stayed unchanged.
- Tests are outside tests over HTTP: `tests/trial`, `tests/go` and the app checks in `tests/ui`. All of them start the binary through `tests/harness.ts` on a fresh database with the PDS18 roles.
- The cutover deleted `packages/coordinator` (except `db/schema.sql`), PGlite, `pg`, Fastify, the esbuild server bundle and `tests/coordinator`. `connection_call.py` moved to `crates/executor/`, which embeds it for connection MCP servers. The release is the Go binary plus `dist/app`.

Not built: delivery (`delivery_create`, `delivery_generate`, `delivery_step`, `delivery_get`, `delivery_history`, `session_deliver`, `delivery_worker.py`). The TypeScript code never completed a delivery: no executor handled `delivery-step` commands, and nothing launched the worker. It was meant to do the following:

- The operator confirms a static-site task with a project question.
- A research pipeline run, or a retained run, feeds a Codex generation run (gpt-6.1-sol, low effort). That run writes `output/index.html` and CSS with a Sources section.
- A program worker integrates the bounded HTML/CSS artifacts into the project repo (pull request, then merge after checks) and publishes them.
- After a verified merge, `base-updated` asks every environment host to fast-forward its clean checkout.
- `session_deliver` opens and merges the pull request of an agent session's pushed branch `<branchPrefix>/<sessionId>`.

Not built: artifact placement (`placement.ts`: copies across project `artifact_locations`, artifact rules, `artifact_copy_retry`). `stores_list` reports full stores only.

## Executor and gateway

The executor and the gateway move after the coordinator, together, behind the unchanged `/api/hosts/*`, one host at a time, with the previous binary behind a symlink as rollback.

The probe in `go/probe/` showed that the executor needs no C compiler ([evidence](../../evidence/fleet-stack.md#executor-probe)):

- Journal: `modernc.org/sqlite` keeps `journal.sqlite`, its SQL, WAL across processes, the Python protocol tests and in-place upgrade (queued receipts and non-terminal commands survive). It lost 0 acknowledged commits in the crash test at about 14k commits/s. Fallback: `ncruces/go-sqlite3` (fewer modules, 4 MB larger binary, one unexplained read anomaly).
- A standard-library journal was rejected ([why](#why-this-stack)).
- macOS kernel calls: `github.com/ebitengine/purego` for `proc_pidinfo` and `proc_signal_with_audittoken`; `golang.org/x/sys/unix` for `Openat`, `Statfs` and `SysctlKinfoProc`. Linux identity-bound signals need only the standard library (`os.Process` with pidfd). The Rust refusal when the private libproc symbol is missing is kept.
- Losses against Rust: about 4 MB more binary; no `pre_exec`, so the preview `RLIMIT_AS` cap needs a re-exec wrapper or a racy `prlimit`; macOS `getpwuid` needs purego or a `$HOME` fallback.

Before the port: choose the `RLIMIT_AS` mechanism. Not built: a Linux run of the signal (pidfd) and crash checks in CI. Windows builds, but process control and file safety need their own port (job objects, `LockFileEx`, `os.Root`, ACL checks); Windows without WSL is not a target.

## Trial (L1)

The language was chosen by porting one coordinator slice to Go and to Rust and measuring both against the TypeScript coordinator with the same outside tests and script. Go and TypeScript tied on edit-to-green; Rust's one-line rebuild was about 14 times slower; Go has 11 packages in `go.sum` against 156 (TypeScript) and 172 (Rust) in the whole lockfile; both ports reached green inside the time box with at most one build-fix cycle. Numbers: [evidence](../../evidence/fleet-stack.md#language-trial). The Go port is `go/coordinator`; the Rust port was not merged.

The slice and the protocol are the template for the coordinator's outside tests.

### Trial slice (L1)

Requirements SL1-SL11 are the slice's own. Each has one outside test in `tests/trial/slice.test.ts` whose name starts with its identifier.

| ID | Requirement | Risk the test guards |
|---|---|---|
| SL1 | A browser request carries `Authorization: Bearer <browser secret>` (exact secret, `Bearer` scheme only) and a known `X-Fleet-Person`; otherwise 401, or 403 for an unknown person. | Authorization header parsed loosely |
| SL2 | A host request is authorized by its own host secret only. An unknown or missing secret is answered 401, never a redirect or another status; a host request carrying `X-Fleet-Person`, or a body `hostId` of another host, is 403. | The executor outbox keeps a row only on 3xx and 401 (`crates/executor/src/outbox.rs`): any other answer drops or completes work it should retry; host impersonation |
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
| `POST /api/hosts/poll` | SL5, the `host-observation` write, and the dispatch claim (`dispatch.ts`) in its simplest case: no subscriptions, the default host polling | ranking over subscriptions and hosts, budgets, wait reasons and stall questions, delegation reconcile, problem detection, promotion, store observations, project trust |
| `POST /api/hosts/receipts` | the status receipt (SL6) | account, delegation and project envelopes; the artifact gate on `completed`; native session capture; pipeline and delivery follow-ups |
| `POST /api/hosts/events` | SL7, SL11 | transport-conflict records and recovery; harness version, child agents and totals |
| `GET /api/executions/:id/events` | SL7 | |
| `GET /api/stream` | SL8, SL9 | ping, pruning, the `close` frame |

**Tables and kinds.** `document` (kinds `session`, `message`, `command`, `host`, `host-observation`, `event`, `event-delta`), `execution`, `change` (written by the `fleet_change` triggers, which also send `NOTIFY fleet_change`), `person` and `person_grant` (mirror of the people file), `schema_migration`. The schema is `packages/coordinator/db/schema.sql`, unchanged; RLS policies and triggers come from it.

**Port layout.** Go: module `go/`, binary package `go/coordinator`, built to `go/bin/fleet-coordinator` (ignored by git), host protocol types from `go/gen`. The binary serves on `127.0.0.1:$PORT` with this environment: `FLEET_DATABASE_URL` (role `fleet_app`), `FLEET_MIGRATION_DATABASE_URL` (role `fleet`, which applies `schema.sql` at start in the same `-- migration N` steps and records them in `schema_migration`; migration 6's JavaScript document move is a no-op on an empty database), `FLEET_PEOPLE_FILE`, `FLEET_BROWSER_SECRET`, `FLEET_HOST_ID`. The measured arm is `tests/trial/arms/go.json`.

**Runner.** `node tests/trial/run.ts` is the one command. It starts, once per machine, a local PostgreSQL in `~/.cache/fleet-trial/pg` (trust authentication, Unix socket only, port 54329, left running for other worktrees), creates a fresh database for this worktree with `deploy/infrastructure/fleet-postgres.sql`, starts the coordinator under test on a free port (`tests/harness.ts`; `FLEET_TRIAL_SERVER`, default `go/bin/fleet-coordinator`), runs the tests with the base URL and secret in the environment, stops the server and drops the database. Tests are TypeScript on Node's test runner, standard library only, and reach the server only over HTTP and SSE.

**Shared-server rule.** Change visibility uses `pg_snapshot_xmin(pg_current_snapshot())`, which spans every database on the server, so a transaction open in another database holds committed changes back and no second `NOTIFY` arrives when it ends. `changes.since` reports `held`, and the stream looks again after 250 ms (`HELD_MS` in `packages/coordinator/stream.ts`). The same stall applies in production behind any long transaction that writes no change row.

**Planted defects.** The trial planted its defects in the TypeScript coordinator (`tests/trial/defects.ts`, deleted with it); each turned its test red.

| ID | Defect | File | Red test |
|---|---|---|---|
| P1 | Event `data` stored as `item` (the `item`/`data` seam rename) | `recovery.ts` | SL7 |
| P2 | Unknown host secret answered 403, not 401 (outbox rule) | `app.ts` | SL2 |
| P3 | Unknown host secret answered 200 with an empty poll (outbox rule) | `app.ts` | SL2 |
| P4 | Events written one by one while the batch is still checked | `recovery.ts` | SL7 |
| P5 | Write routes without their transaction, message route without its own | `app.ts` | SL10 |
| P6 | Write transactions take no scope lock | `store.ts` | SL10 |
| P7 | Events returned in insertion order, not `seq` order | `app.ts` | SL7, SL11 |
| P8 | Stream resume replays the change at the cursor (`>=`) | `store.ts` | SL8 |
| P9 | Any authorization scheme accepted before the secret | `app.ts` | SL1 |
| P10 | A host request may carry a person | `app.ts` | SL2 |
| P11 | A body `hostId` of another host silently replaced | `app.ts` | SL2 |
| P12 | A poll returns pending commands of every host | `app.ts` | SL5 |
| P13 | A person without grants works in the operator scope | `app.ts` | SL9 |
| P14 | Receipt status regression accepted | `app.ts` | SL6 |
| P15 | SSE change frames without an `id:` line | `stream.ts` | SL8 |
| P16 | SSE served as `text/plain` | `stream.ts` | SL8 |
| P17 | LISTEN on the wrong channel | `stream.ts` | SL8 |

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

## Risks

| Risk | Effect | Mitigation |
| --- | --- | --- |
| The compiled trial arms were measured under load | Go and Rust speed numbers are pessimistic | Rerun `measure.ts go` on an idle machine |
| The rewrite drops a behaviour the TypeScript coordinator had | A screen or host call stops working | Each area lists ported routes and dropped behaviour in its pull request; the app checks in `tests/ui` run against the binary |
| `encoding/json` matches keys case-insensitively | A validated field differs from the stored one | Validated values are stored; case variants of identity keys are refused (`go/coordinator`) |
| The executor's private libproc symbol disappears | Identity-bound signals on macOS stop working | Refuse to signal when the symbol is missing, as Rust does today |
| An unsigned darwin binary is quarantined | Mac hosts cannot run the CI build | [Executor delivery](fleet-executor-delivery.md); signing is an open question |

## Open questions

1. **Signing** of downloaded binaries on macOS and Windows.
2. **Host protocol versioning** while hosts update one at a time: a protocol version in the poll request, or additive-only schema changes with the coordinator accepting the previous version.
3. **Lasting executor publication**: where hosts download from, which version, who starts an update ([executor delivery](fleet-executor-delivery.md)).
4. **Requirement files**: which files hold requirements for the whole system; SL1-SL11 are the only identified set today.
