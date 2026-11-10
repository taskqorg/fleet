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
   generate both sides of the contracts from one schema (L3), move the Python
   MCP servers into executor subcommands (L4).
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
*   **L4 ports all three executor MCP servers**, including the `mcp` mode of
    `connection_call.py`. Connections C1 later changes the Rust subcommand
    instead of the Python `mcp` mode (owner). Follow-up:
    [`fleet-connections.md`](./fleet-connections.md) C1 (items 5 and 7) must be
    updated to match.
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
    process is started for any executor MCP server.

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

The executor gains subcommands that replace its three Python MCP servers:
`delegation.py`, `connection_call.py` in `mcp` mode, and `design_rules.py`
(for example `fleet-executor mcp delegation`). Done means the executor starts
no `python3` process. Connections C1 later changes the Rust connection
subcommand instead of the Python `mcp` mode. The coordinator still runs
`connection_call.py` and `delivery_worker.py` on its own host, so Python is
not removed from the coordinator host. Whether `deploy/infrastructure/origin_guard.py` is
ported, kept or deleted is decided in L4, as the brief says; other Python
outside the executor is out of scope (Detailed Implementation step 16).

### Trial (L1)

One slice, ported to Go and to Rust, on one machine, with one prompt and one
model for the agent, repeated as the trial run plan sets. The candidate slice:

*   Coordinator (both arms): SSE in `packages/coordinator/stream.ts`, the
    `listen`, `tx` and `changes` parts of `store.ts`, `store.all` with a
    filter, `POST /api/sessions`, `POST /api/sessions/:id/messages`,
    `POST /api/hosts/events`, and the poll and receipt routes. This covers
    Postgres, RLS, LISTEN, HTTP, SSE, and half of the host protocol.
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

6.  **Blocked on open question 2.** Assign identifiers to the requirements of the trial slice only, in the file
    chosen for open question 2, and count them.
7.  Write external tests for the slice that use only HTTP and the host
    protocol, and make them green on the current stack. Choose the test
    language before the port.
8.  Plant at least 10 bugs, including the two known seam bugs and a violation
    of the outbox rule for 3xx/401 responses; each must turn a test red. Old tests
    stay; they are deleted at the end of the whole effort. Give the owner a
    list of behaviour that only old tests cover.

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

The behaviour spec for L4 is the current behaviour of the three Python MCP
servers, captured by outside tests before the port.

14. `crates/executor/src/main.rs`, `crates/executor/src/profile.rs`: start the
    delegation, connection and design-rules MCP servers as
    `fleet-executor mcp <name>` instead of `python3`.
15. Remove `crates/executor/src/delegation.py` and
    `crates/executor/src/design_rules.py`, their `include_str!`/`include_bytes!`
    uses (`main.rs:405`, `profile.rs:167,208,227`) and the `CONNECTION_CLIENT`
    constant (`profile.rs:172`, used at `main.rs:442`) once the subcommands
    replace them. Update the readers of these files in the same change:
    *   `profile.rs:227` hashes `design_rules.py` into `mcpServerSha256`; after
        the port it hashes the executor binary (author decision).
    *   `profile.rs:262` (test `scoped_claude_and_antigravity_settings`)
        asserts the design-rules command is `python3`; it asserts
        `fleet-executor` with args `mcp design-rules` instead.
    *   `tests/coordinator/artifact-tools.test.ts:9` runs `delegation.py`
        through `python3` and is run by CI; it calls
        `target/debug/fleet-executor mcp delegation` instead (CI builds it
        first, `ci.yml:29-30`). Its `python3` SQLite setup calls stay.
    *   `crates/executor/README.md:15` documents both files.
    *   `evidence/delegation-approval-candidate-sha256.txt:3` pins a hash of
        `delegation.py`; it stays as a historical record.
    *   `tests/executor/delegation_protocol.py`, `project_protocol.py` and
        `lab_protocol.py` call the files and will break (open question 6).
    `packages/coordinator/connection_call.py` keeps its `call` mode for the
    coordinator (`pipelines.ts:69`); its `mcp` mode is removed.
16. `deploy/infrastructure/origin_guard.py`: the owner decides whether it is
    ported, kept or deleted (brief, item С6).

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
*   **Trial preparation and L1:** test language, runner and directory for the
    external slice tests; the planted-bug list with file, change and expected
    red test; worktree naming and per-worktree Postgres database creation;
    results table format.

