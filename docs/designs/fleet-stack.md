# Fleet stack: rule delivery, current-stack fixes, then a measured language trial

*   **Author:** Alex Kirs
*   **Status:** Proposed
*   **Date:** 2026-11-10
*   **Brief:** [`fleet-stack-brief.md`](./fleet-stack-brief.md) (Russian; owner decisions of 2026-11-10)
*   **Review:** [`reviews/fleet-stack/`](./reviews/fleet-stack/)
*   **Handoff:** [`../handoffs/toolchain-choice-2026-11-10.md`](../handoffs/toolchain-choice-2026-11-10.md)
    (pain points and the 7 handoff questions)

**Terms.** This list defines the words that the rest of the document uses
without explanation.

*   **specq**: the shared rule set for taskqorg projects
    (<https://github.com/taskqorg/specq>). Paths such as
    `engineering/testing.md`, `engineering/rule-delivery.md` and
    `engineering/spec-first.md` are files in specq, not in Fleet. The "specq
    core" is its shared engineering rules, pinned to one commit.
*   **Seam**: a place where two programs exchange data.
*   **Known seam bugs** (from the handoff): the app read `ev.item` while the
    executor wrote `ev.data` in transcript events; and the outbox rules for
    3xx and 401 responses (how the executor classifies a failed delivery from
    its local outbox) lived in two places that could disagree.
*   **Old tests**: every `tests/coordinator/*.test.ts` file that imports
    `packages/coordinator` (33 of 37 on 2026-11-10). They cannot run against a
    coordinator in another language. Helpers such as `pg-test.ts` and
    `project-fixture.ts` go with them.
*   **PGlite**: Postgres compiled for Node, which the coordinator tests use
    today instead of a Postgres server.
*   **Edit-to-green**: time from an edit to green tests for the changed part.
*   **`fleet-toolchain.md` decisions** ([link](./fleet-toolchain.md)): D1 stay
    on Node 22; D2 one shared Rust `CARGO_TARGET_DIR` per machine; D3 Fleet's
    own `s3.ts` instead of `@aws-sdk`; D4 `node:sqlite` and a bundled server.
*   **Numbering**: L1-L4 come from the brief; L1 is the language and runs
    last. The brief runs the others in the order L3, L2, L4. Each of L2, L3
    and L4 can be built on its own, but the trial baseline needs all three.

--------------------------------------------------------------------------------

## Overview

An earlier concept proposed moving Fleet's coordinator, executor and glue
scripts to Go, generating types from `schema.sql` through sqlc, and removing
Python and Rust. Seven reviewers read the repository and found that the concept
joins four separate decisions: the language, delivery of built files to hosts,
the source of contract types, and the test rule. Only the language depends on a
trial port. The other three can be done on the current stack (TypeScript
coordinator, Rust executor).

The owner accepted this order on 2026-11-10:

1. **Step 0.** Deliver the shared specq rules to the agents that write Fleet.
2. **L2-L4 on the current stack.** Build and publish the executor in CI (L2),
   generate both sides of the contracts from one schema (L3), move delegation and design-rules MCP servers into executor subcommands (L4).
3. **L1 trial.** Port one slice to Go and to Rust, and measure both against the
   current stack after L2-L4. Trial preparation (requirement identifiers and
   external tests for the slice, Detailed Implementation steps 6-8) runs after
   Step 0 and before L3.
4. **Decision.** Write the language decision into `fleet.md` before any code
   changes. The language changes only if a candidate passes the rejection
   conditions in [Trial (L1)](#trial-l1); how they combine is open question 1.

This document is that plan. It does not choose a language.

**Nothing about Go, sqlc, cgo, or macOS signing was built or measured.** Every
statement below about how those behave is unverified until the trial runs.
Repository facts were checked against the code on 2026-11-10; line numbers can
drift.

## The Problem

The handoff that started this work lists slow agent work in parallel worktrees,
no executor delivery, seam bugs between programs, a mixed supply chain, and
untyped `any` values in the coordinator (count: open question 4). The reviewers
found that the Go concept does not address most of these:

*   **Seam bugs live in JSON, not in table rows.** `schema.sql` stores sessions,
    events, messages and commands in `document(kind, id, data jsonb)`
    (`packages/coordinator/db/schema.sql:6-13`). The host protocol
    (`/api/hosts/poll`, `/events`, `/receipts`) has no table at all. Both known
    seam bugs are in event payloads. Types generated from `schema.sql` would
    give raw bytes for these fields, and a drift check would pass while the
    drift remains.
*   **`contracts/` holds examples, not a schema.** `contracts/prototype.json` is
    sample data (`contracts/README.md`), and no code, test or CI job reads it.
*   **There is no executor delivery.** CI builds a debug executor only for tests
    (`.github/workflows/ci.yml:29,123`) and publishes only the coordinator
    archive. Hosts build or copy the executor by hand. This is not a language
    problem.
*   **Python on hosts is more than scripts.** The executor starts three Python
    MCP servers with `python3`: `delegation.py`
    (`crates/executor/src/main.rs:405-409`), `connection_call.py` in `mcp` mode
    (`main.rs:442-447`, embedded through `profile.rs:172`), and
    `design_rules.py` (`profile.rs:167,208`). The coordinator also runs
    `python3 connection_call.py` (`packages/coordinator/pipelines.ts:69`).
*   **The rules exist but do not reach agents.** specq already defines the test
    method (`engineering/testing.md`). Fleet's `AGENTS.md` has two lines about
    design and does not link `fleet.md` or specq. `config/projects/` has no
    `fleet` project, so rule delivery (`engineering/rule-delivery.md`) does not
    apply to agents that write Fleet. `fleet.md` names no verification command
    and no time budget.
*   **A coordinator port must rewrite the transaction layer.** `store.ts` uses
    `AsyncLocalStorage` for implicit transactions, savepoints, advisory locks
    and retry on 40001/40P01 (`packages/coordinator/store.ts:1,38,55-87`).

If Go is compared with today's state, it gets credit for delivery, contracts
and Python removal that do not depend on the language. The baseline is the
current stack after Step 0 and L2-L4.

## Goals

In the owner's priority order:

1.  **Fast agent development in parallel worktrees.** Measured as
    edit-to-green and as build-fix cycles per agent, in N worktrees at once
    (N = 4).
2.  **Fast build and delivery.** CI publishes an executor for each target; a
    host can get a named version.
3.  **Small supply chain.** Measured as the count of third-party modules,
    including transitive ones and code generators.
4.  **Low resource use.** Compiler and process memory, file size, disk per
    worktree. This goal does not decide the outcome.
5.  **One source per seam.** Each seam has one source file, and both sides are
    generated from it.

Step 0 has its own goal: agents that write Fleet receive `fleet.md` and the
pinned specq core, and a before/after measurement on one task shows whether
they follow them.

## Non-Goals

*   An executor for Windows without WSL (ConPTY, job objects). The Rust code is
    Unix-only today; there are two build targets.
*   A new test method. specq `engineering/testing.md` applies.
*   Support for old code, old data, or old tests.
*   Changing the browser app stack (TypeScript, Preact, esbuild).
*   New CI limits or bans. Reports are warnings; nothing fails CI because a
    test lacks a requirement reference.
*   A language decision before the trial measurements exist.
*   Using sqlc or `schema.sql` as the contract source for API, events or the
    host protocol.

## Open Questions

Questions 1-8 come from the brief and stay open. The owner answers them.

1.  **Decision rule.** Who decides after the measurements, and which goal wins
    when metrics disagree.
2.  **Requirements.** What counts as a specification requirement, and which
    files hold requirements.
3.  **`@clickhouse/client`.** Whether it is used. On 2026-11-10 it is pinned in
    `package.json` and no file under `packages/coordinator` imports it (one
    comment mentions ClickHouse, `executions.ts:128`). Reviewers disagreed, so
    it stays open; the trial counts it the same way for every arm.
4.  **`any` count.** The exact count in `packages/coordinator` and the counting
    method. Reviewers counted 470-501 matches; the concept said 123.
5.  **macOS signing.** Signing and verification of a downloaded executor on
    macOS.
6.  **Executor Python tests.** Whether `tests/executor/*.py` pass today. They do
    not run in CI.
7.  **Executor download.** Where hosts download the executor from, which
    version, and who starts an update.
8.  **Handoff questions 3 and 6.** Question 3: how to get the full suite under
    about 10 s locally and keep the real Postgres RLS tests. Question 6:
    security posture (memory safety, supply-chain size, native dependencies,
    signing and checking release artifacts). Neither is closed.
9.  **Host protocol versioning.** Once hosts update one at a time (L2), an old
    executor can talk to a coordinator built from a newer schema. Options
    include a protocol version in the poll request, or additive-only schema
    changes with the coordinator accepting the previous version. Not decided.
10. **Code generators.** Which generator each language uses, where it runs, and
    whether `cargo build` alone still works (a Node generator would make the
    Rust build need Node; a `build.rs` crate adds to `Cargo.lock`). Chosen at
    L3 start together with the reversal check.

### Decisions recorded during review (2026-11-10)

*   **Old tests** stay while they help the work on the current stack (L3, L4,
    the trial) and are deleted at the end of the whole effort, not kept after
    it (owner).
*   **L4 ports delegation and design-rules only.** `connection_call.py`, its
    loaded Python client and `delivery_worker.py` remain until Connections C1.
    Fleet builds its own connections and then deletes Pipe; a Rust bridge to
    today's Pipe protocol is out of scope.
*   **Step 0 time budget:** 180 s for the full merge gate on the owner's Mac,
    diagnostic only (owner).
*   **Trial run plan** (numeric choices, decided by the author of this
    document on the owner's instruction): N = 4 worktrees; 5 repeated runs per
    arm and task; a time box of 8 agent-hours per arm.

## Critical User Journeys (CUJs)

### CUJ 1: An agent starts work on Fleet

*   **Given**: Step 0 is done.
*   **When**: An agent opens a Fleet worktree.
*   **Then**: `AGENTS.md` points it to `fleet.md` and the pinned specq core;
    `fleet.md` gives one verification command and a time budget; CI prints a
    report of tests that name no requirement.

### CUJ 2: A seam changes

*   **Given**: L3 stage 1 is done.
*   **When**: A developer renames a field in an event payload in the contract
    schema.
*   **Then**: Code generation updates the coordinator, app and executor types.
    Every place that reads the field through the generated types and still uses
    the old name fails to compile or type-check. Code that reads the payload
    through `any` is not caught.

### CUJ 3: A host gets a new executor

*   **Given**: L2 is done.
*   **When**: A commit lands on main.
*   **Then**: CI publishes executor files for linux and darwin, named with the
    commit, each with a SHA-256 file. Who downloads and starts the update is
    open question 7.

### CUJ 4: An executor starts an MCP server

*   **Given**: L4 is done.
*   **When**: A session needs the delegation MCP server.
*   **Then**: The executor starts `fleet-executor mcp delegation`; no `python3`
    process is started for delegation or design-rules. Connection MCP remains
    Python until C1.

### CUJ 5: The owner decides the language

*   **Given**: L2-L4 are done and the trial ran on Go and Rust.
*   **When**: The owner reads the measurements against the rejection conditions.
*   **Then**: The decision is written into `fleet.md` before code changes. If no
    candidate passes, Fleet keeps TypeScript and Rust.

## Technical Plan (Architecture)

### Separate decisions

| Decision | Status | Depends on the trial |
| --- | --- | --- |
| Step 0: rule delivery | Accepted, runs first | No |
| L2: executor built and published by CI | Accepted, runs second | No |
| L3: one contract schema per seam | Accepted, runs second | No |
| L4: Python MCP servers as executor subcommands | Accepted, runs second | No |
| L1: language | Proposed; owner decides after measurements | Yes |
| Test principle | Already in specq `engineering/testing.md` | No |

### Rule delivery (Step 0)

Fleet's `AGENTS.md` links `fleet.md` and the specq core at a pinned commit. A
`fleet` project in `config/projects/` makes specq rule delivery apply to agents
that write Fleet. `fleet.md` gains a verification section: one command, a time
budget on a named machine, and the rule that a lasting test names its
requirement and risk. CI runs specq's `contract_lint.py` and prints a report of
tests that name no requirement, as a warning. A before/after run on one task
shows whether agents follow the rules; Step 0 is not done without it.

### Seams and their sources (L3)

```text
schema.sql ───────────────► database rows (coordinator store)

contracts/<schema> ──┬────► coordinator types (TypeScript)
  API, events,       ├────► app types (TypeScript)
  host protocol      └────► executor types (Rust)
```

Two sources, each owning one seam: `schema.sql` owns database rows; one
protocol schema in `contracts/` owns the HTTP API, event payloads, and the host
protocol (poll, events, receipts). The format is JSON Schema (owner decision,
2026-11-10). A JSON Schema file already exists (`contracts/lab-config.json`),
but it is only copied as an asset (`packages/coordinator/package-assets.mjs`);
the repo has no generator and no validator for it, so L3 adds both (open
question 10). Generated files are produced at build time, not stored in git, so
they cannot drift from the schema; code that uses them fails to compile when it
drifts.

L3 is staged by seam. Stage 1 covers event payloads and the host protocol,
which hold the known seam bugs and the trial's planted bugs; the trial
baseline needs only stage 1. Other HTTP routes get schema types when they are
next changed.

**Reversal condition.** At L3 start, generate types for one union event shape
(the transcript event with `item`/`data`) for TypeScript and Rust. If either
side gets a weak type for it (untyped, `any`, or a raw JSON value), the format
choice goes back to the owner with protobuf and OpenAPI as the alternatives.

### Executor delivery (L2)

CI builds the executor for `linux/amd64` and `darwin/arm64` (architectures
and the Linux C library target unverified) and publishes each file with a
SHA-256 file next to the coordinator archive, as CI already does for the
coordinator (`.github/workflows/ci.yml:110-112`). Today that archive is a
workflow artifact uploaded on push to main (`ci.yml:107-121`), which expires;
whether the executor needs a lasting release is part of open question 7. The brief names two Rust
paths for darwin: a native macOS runner in the CI matrix, or `cargo-zigbuild`
from Linux. Signing and quarantine on macOS are unverified (open question 5).

### Python removal (L4)

The executor starts `fleet-executor mcp delegation` and
`fleet-executor mcp design-rules` instead of Python for those two servers.
Their outside stdio captures define the behavior to preserve, including errors
and concurrent journal access. Delegation and account receipt types come from
`contracts/host-protocol.schema.json`.

`connection_call.py` (both CLI and MCP), its loaded Python client and
`delivery_worker.py` remain until Connections C1 replaces this path with Fleet's
own connections. L4 builds no Rust bridge to Pipe. Hosts serving connection MCP
still need Python. `deploy/infrastructure/origin_guard.py` stays: it protects the
origin independently of MCP. Other Python outside these two MCP servers is out
of scope. The remaining file inventory is in the [executor README](../../crates/executor/README.md#remaining-python).

### Trial (L1)

One slice, ported to Go and to Rust, on one machine, with one prompt and one
model for the agent, repeated as the trial run plan sets. The candidate slice:

*   Coordinator (both arms): Postgres, RLS, LISTEN, HTTP, SSE, and half of
    the host protocol. The exact routes, tables and behaviours are fixed in
    [Trial slice (L1)](#trial-slice-l1).
*   Executor (Go arm only, because the executor is already Rust): port
    `native.rs` and build it with `CGO_ENABLED=0` from Linux, the SQLite outbox
    without cgo, one MCP server as a subcommand.

Every metric is reported separately for the coordinator part and the executor
part. Go and Rust are compared on the coordinator part; the Go executor part is
judged on its own rows below.

Tests for the slice run against real Postgres with one database per worktree
on one local server, because PGlite is a Node build and has no known Go
equivalent (unverified). The baseline runs the same external slice tests on the
same Postgres server, so the arms differ only in language; the PGlite time is
reported as a separate number. The trial runs on the state after L4, when the
Python writer of `journal.sqlite` (`delegation.py`) is gone and the executor is
its only writer.

**What to measure:** cold and warm build; rebuild after a one-line edit,
including code generation; slice test time with Postgres per worktree,
including setup; edit-to-green for N agents in N worktrees on the same change;
build-fix cycles per agent; peak compiler and process memory, file size, disk
per worktree; third-party module count including generators (`go.sum`,
`Cargo.lock`, pnpm lockfile); darwin-arm64 build on each path and a run of the
downloaded file on a Mac; the type generated from the contract JSON Schema for
one transcript event (today stored in a `jsonb` column), for TypeScript, Rust
and Go. Results are reported as median and spread over the repeated runs; a
difference smaller than the spread counts as a tie.

**Results that reject a candidate.** Each row is compared with the measured
baseline (current stack after L2-L4). How the rows combine, and which goal wins
when they disagree, is open question 1. A row can reject both candidates.

*   Both: the ported transaction layer fails concurrency and serialization
    tests.
*   Both: edit-to-green on the slice is not faster than the baseline's
    type-check plus the same slice tests on the same machine.
*   Both: the other candidate reaches green in fewer agent cycles with equal or
    better cycle time.
*   Both: the module count does not go down.
*   Both: the arm does not reach green within its time box (trial run plan in [Decisions recorded during review](#decisions-recorded-during-review-2026-11-10)).
*   Go only: a build without cgo does not cover SQLite or the macOS kernel
    calls in `native.rs`.

The Rust arm's edit-to-green is measured twice: with one shared
`CARGO_TARGET_DIR` (D2) and with a per-worktree APFS clone of a warm `target`
(`fleet-toolchain.md` measured `cp -c -R` at 0.35 s), so the shared-lock wait
is not scored as a property of the language.

**Expected equal, not a rejection:** planted seam bugs caught (including the
`item`/`data` rename), run once per arm as a check. The brief listed
this as a rejection condition for Go; it changed because after L3 every
language generates from the same schema, so both candidates and the baseline
should catch the same planted bugs. If that holds, a port buys only delivery,
which L2 already gives.

If no candidate passes the rejection conditions, keep TypeScript and Rust and
stop after L2-L4.

### Trial slice (L1)

Fixed on 2026-11-10 against `main` after L2-L4 and the Conversations merges.
Requirements SL1-SL11 below are the slice's own; they live here until open
question 2 names the requirement file for the whole system. Each has one
outside test in `tests/trial/slice.test.ts` whose name starts with its
identifier.

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

**Routes.** Only the parts named here are in the slice; the port may answer
anything else, and no outside test reaches it.

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

**Tables and kinds.** `document` (kinds `session`, `message`, `command`,
`host`, `host-observation`, `event`, `event-delta`), `execution` (typed
table), `change` (written by the `fleet_change` triggers, which also send
`NOTIFY fleet_change`), `person` and `person_grant` (mirror of the people
file), `schema_migration`. The schema is
`packages/coordinator/db/schema.sql`, unchanged; RLS policies and triggers
come from it.

**Port target layout.** Both arms start from the same commit and the same
rules:

*   Rust: crate `crates/coordinator` (binary `fleet-coordinator`), a member
    of the existing Cargo workspace, so it shares `Cargo.lock` and `target`
    with the executor. Go: module in `go/` (`go/go.mod`), binary package
    `go/coordinator`, built to `go/bin/fleet-coordinator` (ignored by git).
*   The binary serves the slice routes on `127.0.0.1:$PORT` with the same
    environment as `packages/coordinator/server.ts`: `FLEET_DATABASE_URL`
    (role `fleet_app`), `FLEET_MIGRATION_DATABASE_URL` (role `fleet`, which
    applies `packages/coordinator/db/schema.sql` at start in the same
    `-- migration N` steps and records them in `schema_migration`),
    `FLEET_PEOPLE_FILE`, `FLEET_BROWSER_SECRET`, `FLEET_HOST_ID`. Migration
    6's JavaScript document move is a no-op on an empty database; the port
    records the version without it.
*   Host protocol types are generated at build time from
    `contracts/host-protocol.schema.json` and not committed. Rust may reuse
    the executor's generator (`crates/executor/build.rs`); the Go arm writes
    its generator in Go as part of its work and the build runs it, so
    `go build` needs no Node (open question 10).
*   The arm adds `tests/trial/arms/<language>.json` (keys as in
    `typescript.json`) and changes nothing under `packages/`, `app/`,
    `tests/coordinator/` or `tests/trial/` except that file.

**Outside tests and runner.** `node tests/trial/run.ts` is the one command:
it starts (once per machine) a local PostgreSQL in `~/.cache/fleet-trial/pg`
(initdb with trust authentication, Unix socket only, port 54329, left running
for other worktrees), creates a fresh database for this worktree with
`deploy/infrastructure/fleet-postgres.sql` (only the database name changed),
starts the coordinator under test on a free port
(`FLEET_TRIAL_SERVER`, default `node packages/coordinator/server.ts`), runs
the tests with the base URL and secret in the environment, stops the server
and drops the database. Tests are TypeScript on Node's test runner with the
standard library only; they reach the server only over HTTP and SSE.
11 tests; on an Apple M2 the whole command takes about 1.4 s including
setup and teardown, 0.8 s of it the tests.

**Shared-server finding.** Change visibility uses
`pg_snapshot_xmin(pg_current_snapshot())`, and the snapshot xmin spans every
database on the server. A transaction still open in another worktree's
database therefore held committed changes back, and no second `NOTIFY`
arrived when it ended, so the stream stalled until the next write (SL8 red
in 1 of 4 parallel worktrees). `changes.since` now reports `held` and the
stream looks again after 250 ms; a production coordinator had the same stall
behind any long transaction that writes no change row.

**Planted defects.** `node tests/trial/defects.ts` applies each defect to
the current coordinator alone, runs the outside tests and restores the file.
All 17 turn their test red.

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

Not covered by an outside test, with the reason:

*   The advisory lock alone (`pg_advisory_xact_lock`): the in-process queue
    orders writers of one server; the advisory lock matters only for a second
    process, and the trial runs one.
*   The `held` re-read: it needs a transaction open in another database
    during the test, which the tests cannot open over HTTP. It shows under
    N parallel worktrees (SL8) and in the measurement runs.
*   Removing only the message route's own `store.tx`: not a defect, because
    the per-route transaction already covers the handler.

**Behaviour only old tests cover** (for the owner; old tests stay until the
whole effort ends): everything marked out of the slice above; PGlite as a
store; nested transactions and savepoints; serialization and deadlock retry
with backoff; migrations 1-17 data moves; change-log pruning; profiles,
accounts, projects, tasks, delegation, pipelines, artifacts, lab, threads and
the agent tools.

### Trial measurement protocol

`node tests/trial/measure.ts <arm> [--runs 5] [--worktrees 4]` measures one
arm the same way for the baseline and both ports. The arm file
`tests/trial/arms/<arm>.json` names `setup` (per fresh worktree), `clean`,
`check` (the command an agent runs before the outside tests), `build` (check
plus the release artifact), `edit` (file for the one-line edit), `server`,
`artifact`, `runtime`, `lockfiles`, `roots` and `disk`. The script creates N
fresh git worktrees of `HEAD` (commit the arm first) under `$TMPDIR`, runs
`setup` in each, and per run records:

*   cold build (`clean`, then `build`), warm build (`build` again), rebuild
    after appending one comment line to `edit` (including code generation);
*   peak resident size of the largest compiler process (`/usr/bin/time -l`)
    and of the server's process group during the outside tests (sampled every
    100 ms by `run.ts`);
*   outside-test time of `run.ts`, including setup and teardown;
*   edit-to-green with N worktrees at once: the same one-line edit in each,
    then `check` and `run.ts`, timed per worktree;

and once: artifact size and runtime size, disk per worktree (`du -sk` of the
`disk` paths), third-party packages in each lockfile, whole and as the
closure of `roots`.

Results are medians and min-max spreads over green runs. Every run records
the 1-minute, 5-minute and 15-minute load averages at its start and end. A
run is marked `comparable=false` when the 1-minute load average before the
script started exceeded half the logical CPUs, or a process outside the
script's own process tree used more than one core at the start or end of the
run (file-event daemons that react to the script's own writes excluded). The
script refuses nothing.

Not measured by the script: build-fix cycles (counted by the porting worker,
below), the darwin-arm64 path and a downloaded-file run (L2 rows), the
generated transcript-event type (read from each arm's generator output).

**Baseline.** The `typescript` arm runs again next to the ports, on the same
machine and commit, so each comparison uses numbers from the same
conditions. The preparation run is in the pull request that added this
protocol.

### Porting task

Both porting workers get this text; only `<LANGUAGE>` differs (`Go` or
`Rust`).

> Port the Fleet coordinator trial slice to <LANGUAGE>. Read `AGENTS.md`,
> `fleet.md`, and in `docs/designs/fleet-stack.md` the sections Trial (L1),
> Trial slice (L1) and Trial measurement protocol; they fix the routes,
> tables, behaviours, target layout and environment. Work in your own
> worktree from the commit you were given; the time box is 8 agent-hours.
>
> Done means: `FLEET_TRIAL_SERVER=<your binary> node tests/trial/run.ts` is
> green for all 11 tests SL1-SL11, unchanged; the binary builds from a clean
> checkout with the documented commands; host protocol types are generated
> at build time from `contracts/host-protocol.schema.json`; you add
> `tests/trial/arms/<language>.json` and run
> `node tests/trial/measure.ts <language>` once. Do not change the outside
> tests, the schema, the contracts or the TypeScript coordinator. Do not read
> the TypeScript coordinator's old tests for the slice behaviour; the
> specification and the outside tests are the requirement.
>
> Count build-fix cycles: one cycle is one run of your build command or of
> `run.ts` that fails, followed by an edit. Keep a log with one line per run:
> time, command, result (`build-red`, `test-red: SL…`, `green`) and the
> wall time. Report: total cycles until the first all-green run, cycles that
> were compiler errors and cycles that were test failures, total agent time,
> and the log. Then report the measurement table, the third-party packages
> you added and why, and anything in the specification you found ambiguous.

### Migration if the language changes

Executor first, behind the unchanged `/api/hosts/*`, one host at a time, with a
symlink as rollback. Coordinator last, route by route behind the HTTP seam;
`schema.sql` and `app.ts` are frozen during each switch; rollback is the
release symlink while the schema is unchanged. Never port both components at
once.

## Non-Functional Requirements

### Risks and mitigations

| Risk | Effect | Mitigation |
| --- | --- | --- |
| A downloaded executor is altered or the wrong version | A host runs code that holds harness credentials | SHA-256 file per executor in L2, as for the coordinator; signing and download source are open questions 5 and 7 |
| macOS blocks or quarantines the downloaded file | Mac hosts cannot run the CI build | Measure a run of the downloaded file on a Mac in L2 and the trial; open question 5 |
| Linux build needs a newer C library than hosts have | Executor fails to start on PDS18 or WSL | Name the target triple (glibc version or musl) in L2 and run the file on each host type |
| Old executor talks to a newer coordinator | Payloads are misread during a one-host-at-a-time update | Open question 9 |
| Code generators add modules to the build path | Supply chain grows; Goal 3 regresses | Count generator modules in the trial; open question 10 |
| JSON Schema generators produce weak union types | L3 does not close the known seam bug | Reversal condition in L3 |
| Seam code reads payloads through `any` | A rename is not caught (CUJ 2) | Step 11 moves seam reads to generated types; `any` count is open question 4 |
| Old tests deleted before L3/L4 refactors | Regressions in the refactors go unseen | Old tests stay until the end of the whole effort (owner decision) |
| Trial agent results vary between runs | Noise read as a language effect | Repeated runs, median and spread, ties (trial run plan in [Decisions recorded during review](#decisions-recorded-during-review-2026-11-10)) |
| Trial runs long | Owner time spent on two ports | Time box per arm (trial run plan in [Decisions recorded during review](#decisions-recorded-during-review-2026-11-10)) |
| Per-worktree `target` clones | Full disk cost on Linux without reflink | Measure on each host that runs parallel worktrees |

### Rollout and rollback

L2-L4 run on the current stack and are reverted by ordinary commits. A
language change follows [Migration if the language changes](#migration-if-the-language-changes):
symlink rollback per host for the executor, and the release symlink for the
coordinator while the schema is unchanged.

## Alternatives Considered

### Baseline

Rejected by author: comparing Go with today's state, because Go would get
credit for delivery, contracts and Python removal that L2-L4 give on any
language.

### Language

| Option | Expected advantage (unmeasured) | Weak point | Status and reason |
| --- | --- | --- | --- |
| A. Keep TypeScript and Rust, add L2-L4 | No port | Two server languages remain | Baseline for the trial |
| B. Go for coordinator and executor | One language, cross-builds | cgo for SQLite and macOS calls; transaction rewrite; `map[string]any` over `jsonb` | In the trial |
| C. Go coordinator, Rust executor | Delivery without porting the executor | Two languages; transaction port remains | Not a separate arm: its coordinator half is the Go arm's coordinator part, its executor half is the baseline |
| D. Rust everywhere | One language, no runtime on hosts | Shared `target` lock across worktrees; coordinator cold build unknown | In the trial |
| E. TypeScript everywhere | One language, no compile step | Executor memory and size on hosts | Not in the trial: a Node executor on every host adds the Node runtime's memory and size there (unmeasured); `fleet-toolchain.md` rejected Node SEA only for the coordinator bundle |

### Contract source

| Option | For | Against | Status |
| --- | --- | --- | --- |
| JSON Schema in `contracts/` | Works on the current stack; `prototype.json` gives start examples; generators exist for TypeScript, Rust and Go (unverified for unions) | Second source next to `schema.sql`; repo has no generator yet | Chosen by owner, with the L3 reversal condition |
| protobuf with buf | Generates typed code for TypeScript, Rust and Go | Adds a contract language and the `buf` tool; the protobuf JSON mapping changes field names and number encoding (unverified for current payloads) | Fallback if the reversal condition triggers |
| OpenAPI | Describes HTTP routes and bodies together | Adds a tool (for example oapi-codegen for Go); no standard way to describe SSE event streams or host-side event payloads | Fallback with protobuf |
| `schema.sql` -> sqlc -> Go -> TypeScript | One file | Does not describe events, the API, or the host protocol; `schema.sql` is a migration log with plpgsql triggers, `SECURITY DEFINER` functions and `ALTER TYPE ... ADD VALUE`; `store.ts` builds queries at run time, which sqlc cannot read (sqlc behaviour unverified); Go -> TypeScript needs a second generator | Rejected by author |
| Go structs with tygo | Lightest if Go wins | Tied to Go; unusable for the baseline | Rejected by author |
| TypeSpec | — | Named in the handoff's candidate list; no reviewer evaluated it | Not evaluated |

### Test rule

Rejected by author: failing CI when a test names no requirement. specq does not
require it, and agents would satisfy it with arbitrary references. CI prints a
report instead, as it already does for use-case drift
(`.github/workflows/ci.yml:33`, "Use-case drift (warning only)").

### Separate test-method research

Rejected by author: the method is already in specq `engineering/testing.md`;
only its delivery is missing (Step 0).

--------------------------------------------------------------------------------

## Detailed Implementation

Each step lists the files the implementing work changes. Phases run in the
order listed: Step 0, trial preparation, L3, L2, L4, L1. L-numbers name the
decisions in [Technical Plan](#technical-plan-architecture), not the run order.
This is the phase-level plan. Each of L3, L2, L4 and L1 gets a short phase
design before its code, covering only the choices that have more than one
defensible answer. Mechanical choices (for example a crate or hand-written
JSON-RPC, target triples, CI job layout) are the implementer's, taken by the
smallest-thing-that-works rule in specq `engineering/ponytail.md` and recorded
in the phase PR. A step marked **Blocked** cannot start until the named open question is
answered. Each phase ends with the full suite green (`pnpm test` and
`cargo test -p fleet-executor`, as CI runs them, `ci.yml:29-30`, and the
real-Postgres roles job, `ci.yml:102,125`).

### Step 0: deliver rules to agents (runs first)

Step 0 needs no separate phase design. It is implemented in draft PR
[taskqorg/fleet#75](https://github.com/taskqorg/fleet/pull/75), which pins
specq at `d178c809b607d37696fe93b7572d85ed62f883a9` and sets the verification
command. The time budget is 180 s for the full merge gate on the owner's Mac,
diagnostic only (owner decision). The
steps below describe what that PR covers.

1.  `AGENTS.md`: link `fleet.md` and the specq shared core at a pinned commit.
2.  `fleet.md`: add a verification section with one command, a time budget on a
    named machine, and the rule "a lasting test names its requirement and
    risk". Add the rule that a stack decision is recorded in this file before
    code changes (`engineering/spec-first.md`).
3.  `config/projects/fleet/project.json` and `config/projects/fleet/rules.md`:
    add a `fleet` project, following the existing projects in
    `config/projects/`, so rule delivery applies to agents that write Fleet.
4.  `.github/workflows/ci.yml`: check out specq at the pinned commit and run
    its `contract_lint.py` on `fleet.md`, as agentq does; print a report of tests that name no
    requirement. Both checks warn and do not fail the build.
5.  Measure one task before and after, with the same prompt and model: number
    of new tests and whether they name requirements. One task gives a
    direction, not a proof. Step 0 is not done without this measurement.

### Trial preparation

6.  Done: the slice's requirements are SL1-SL11 in
    [Trial slice (L1)](#trial-slice-l1) (11 requirements); open question 2
    stays open for the rest of the system.
7.  Done: `tests/trial/slice.test.ts` (TypeScript, Node's test runner,
    standard library only) and its runner `tests/trial/run.ts`, green on the
    current stack.
8.  Done: `tests/trial/defects.ts` plants 17 defects, including both known
    seam bugs; each turns a test red. The list of behaviour that only old
    tests cover is in [Trial slice (L1)](#trial-slice-l1).

### L3: contracts

**Blocked on open question 10.** Before step 9, check the
[reversal condition](#seams-and-their-sources-l3) and choose the generators.

9.  `contracts/`: add one JSON Schema. Stage 1 covers event payloads and the
    host protocol, starting from the examples in `contracts/prototype.json`;
    `contracts/README.md` describes it. Other HTTP routes follow when changed.
10. Generators for the coordinator and app (TypeScript) and the executor
    (Rust), run at build time; generated files are not committed.
11. `packages/coordinator/**`, `app/src/**`, `crates/executor/src/**`: read
    seam payloads through the generated types (for example
    `app/src/transcript.ts`, which reads `ev.data` without a type today).

### L2: executor delivery

12. `.github/workflows/ci.yml`: build release executors for linux and darwin
    with a named target triple, write a SHA-256 file for each, and publish them
    with the coordinator archive. The darwin path (native macOS runner or
    `cargo-zigbuild`) is chosen by measuring both (brief, "Доставка исполнителя
    на Mac"). Lasting publication is **blocked on open question 7**.
13. `deploy/build-release.sh` (packs only the coordinator today, lines 17-22),
    `deploy/README.md`: add the executor file and its SHA-256 next to the
    coordinator tarball, using the same `shasum -a 256` pattern.

### L4: Python MCP servers into executor subcommands

The behaviour spec for L4 is the current behavior of delegation and design-rules,
captured through stdio before porting. `tests/executor/fixtures/mcp.json` retains
those exchanges and journal effects; `tests/coordinator/mcp-replay.test.ts` replays them
against the Rust subcommands.

14. `crates/executor/src/main.rs`, `profile.rs` and `mcp.rs`: start delegation
    and design-rules as `fleet-executor mcp <name>`. Retain connection MCP's
    Python command and `CONNECTION_CLIENT` until C1.
15. Remove `delegation.py` and `design_rules.py` and their embedded source uses.
    Update the profile assertions, artifact-tools check and executor protocol
    checks to call the subcommands. `mcpServerSha256` hashes the executor binary.
    Preserve the historical delegation approval hash record. Type delegation
    and account receipt readers through generated contracts; keep existing
    ownership and input validation.
16. Keep `origin_guard.py` and its test: origin admission is independent of MCP.
    List remaining Python and its removal conditions in the executor README.

Out of scope for this plan: the coordinator's use of
`packages/coordinator/connection_call.py` (`call` mode),
`packages/coordinator/delivery_worker.py`, and the `tests/executor/*.py` suite
(open question 6).

### L1: trial and decision

17. Port the slice to Go and to Rust in scratch worktrees; record every
    measurement in [Trial (L1)](#trial-l1).
18. Write a new decision document in `docs/designs/` with these sections:
    status and scope; problem and goals in order; measured baseline;
    candidates; trial protocol; results; L1-L4 decisions; per-platform table;
    which of the `fleet-toolchain.md` decisions D1-D4 it cancels; answers to
    the 7 questions in the [handoff](../handoffs/toolchain-choice-2026-11-10.md);
    migration order, deletions and rollback point; risks; open questions. Add
    only a pointer to `docs/designs/fleet-toolchain.md`.
19. `fleet.md`: record the language decision under the step 2 rule.

### Carried to phase designs

Stage 9 review findings that need phase detail. Each phase design answers the
ones with more than one defensible answer; the implementer settles the rest in
the phase PR.

*   **L3:** schema file names and layout; the full list of stage 1 payload
    types; union representation (`oneOf` or a discriminator field); generator
    per language, output paths, build hook and `.gitignore` entries (open
    question 10); the list of reader files and functions for `ev.data`/`ev.item`
    and the poll, events and receipts bodies.
*   **L2:** target triples (glibc or musl); file names; which `ci.yml` job is
    extended; how the darwin path is measured and chosen; workflow artifact or
    release (open question 7); the exact `build-release.sh` change.
*   **L4:** Rust MCP stdio approach (crate or hand-written JSON-RPC, counted
    against Goal 3); CLI parsing of `fleet-executor mcp <name>`; new module
    files; the outside tests that capture current server behaviour.
*   **L1:** answered in [Trial slice (L1)](#trial-slice-l1),
    [Trial measurement protocol](#trial-measurement-protocol) and
    [Porting task](#porting-task).

