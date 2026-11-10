# Fleet language: Go for every service

*   **Status:** Accepted by the owner on 2026-11-10. Closes L1 of
    [`fleet-stack.md`](./fleet-stack.md).
*   **Scope:** the language of Fleet's services (coordinator, executor,
    gateway) and the rules for Go code. The browser app stays TypeScript
    ([`fleet-stack.md`](./fleet-stack.md#non-goals)). The binding rules are in
    [`fleet.md`](../../fleet.md#stack); this file records the evidence, the
    module table and the migration order.

## Decision

Fleet moves to Go, one language for all services. The order is:

1.  The coordinator (`packages/coordinator`), after the feature coordinator
    reports that the Conversations, Connections and people-access work is
    merged.
2.  The executor (`crates/executor`) and the gateway (`crates/gateway`),
    together, only after a separate probe confirms that the executor builds
    without a C compiler (SQLite outbox, macOS kernel calls in `native.rs`).
    Until then both stay Rust; Connections keeps writing the gateway in Rust.

This replaces the order in
[Migration if the language changes](./fleet-stack.md#migration-if-the-language-changes)
(executor first): the coordinator holds most of the code and gains most from
one cross-build, and the executor waits for the probe.

The owner's reasons: one build from any machine for every platform; little is
lost against Rust; speed is equal for Fleet's workload (the trial below), so
developer and deploy convenience decides.

## Problem and goals

The problem and the goals in the owner's order are those of
[`fleet-stack.md`](./fleet-stack.md#the-problem). How each goal came out:

1.  **Fast agent development in parallel worktrees.** Go and TypeScript tie on
    edit-to-green; Rust's one-line rebuild is about 14 times slower.
2.  **Fast build and delivery.** Go cross-builds every target from one Linux
    job without a C toolchain; Rust needs a macOS runner or `cargo-zigbuild`.
3.  **Small supply chain.** Go 11 packages in `go.sum`, against 146 (TypeScript)
    and 105 (Rust) in the coordinator closure.
4.  **Low resource use.** The Go server uses 21 MB against 146 MB for Node; the
    Go binary is 15.5 MB against 3.6 MB for Rust. This goal does not decide.
5.  **One source per seam.** Equal: every arm generates from
    `contracts/host-protocol.schema.json`.

## Candidates and trial protocol

Candidates B (Go) and D (Rust) from
[Alternatives Considered](./fleet-stack.md#language) against baseline A
(TypeScript and Rust after L2-L4). The slice, the outside tests SL1-SL11, the
porting task and the measurement script are in
[Trial (L1)](./fleet-stack.md#trial-l1) and the three sections after it.
Ports: Go in pull request #105 (merged), Rust in pull request #106 (closed
unmerged after this document).

## Results

Owner's Mac, Apple M2, 8 CPUs; commit `8423216` (main with both ports);
`node tests/trial/measure.ts <arm> --runs 5 --worktrees 4`; no foreign heavy
process. Medians with min-max spread; all 15 runs green.

**Only the TypeScript runs are comparable.** The arms ran back to back and
the load of the previous arm had not decayed: the 1-minute load average was
3.4 when TypeScript started, 4.9 when Go started and 12.9 when Rust started,
on 8 CPUs. The script marked every Go and Rust run `comparable=false`. The Go
and Rust numbers are therefore upper bounds measured under load, not a fair
race. Run 1 of each compiled arm also paid cold caches in all four worktrees at
once (edit-to-green 18.3 s for Go, 50.3 s for Rust).

| Measure | TypeScript | Go | Rust |
| --- | --- | --- | --- |
| Cold build, ms | 542 (520-1689) | 6700 (6632-7483) | 39115 (28686-49003) |
| Warm build, ms | 531 (522-550) | 445 (302-543) | 186 (171-205) |
| Rebuild after a one-line edit, ms | 541 (522-563) | 539 (531-590) | 7744 (7294-8208) |
| Compiler peak RSS, KB | 207328 | 395040 | 593856 |
| Outside tests incl. setup, ms | 1271 (1258-1338) | 1413 (1297-1661) | 1425 (1378-1517) |
| Outside tests only, ms | 719 | 546 | 604 |
| Server peak RSS, KB | 145776 | 20784 | 11936 |
| Edit-to-green, 4 worktrees at once, ms | 2775 (2608-3767) | 2611 (2421-18378) | 3228 (3041-50261) |
| Artifact, bytes | 2522405 + Node 110635104 | 15504866 | 3564224 |
| Disk per worktree, KB | 129580 | 191444 | 464792 |
| Third-party packages, whole lockfile | 156 | 11 | 172 |
| Third-party packages, coordinator closure | 146 | not computed | 105 |

TypeScript's cold build reuses the installed `node_modules`; Go's cold build
deletes a per-worktree `GOCACHE` and recompiles the standard library.

Build-fix cycles to the first all-green run: Go 1 (a missing `go.sum` entry,
fixed with `go mod tidy`; 0 compiler errors, 0 test failures), Rust 0. Agent
time: Go about 12 minutes, Rust about 18 minutes, against a time box of 8
hours.

Against the [rejection rows](./fleet-stack.md#trial-l1): both ports pass the
concurrency tests (SL10, SL11); Go's edit-to-green ties the baseline (the
difference is inside the spread), which the strict row would count as "not
faster"; the module count goes down for both; both reach green inside the
time box; the cgo row is the executor probe. Open question 1 (how the rows
combine) is answered by the owner's decision: a tie on speed lets convenience
decide.

## L1-L4 decisions

| Decision | Outcome |
| --- | --- |
| L1 language | Go for every service, in the order above. |
| L2 executor delivery | Stays as built (Rust, `executor-release` job) until the executor is ported; then the Go cross-build job publishes it. |
| L3 contract source | Unchanged: JSON Schema in `contracts/`. Go types come from `go/gen`; generated files are not committed. |
| L4 MCP subcommands | Unchanged; ported with the executor. |

## Platforms

| Component | linux/amd64 | darwin/arm64 | windows/amd64 |
| --- | --- | --- | --- |
| Coordinator (Go) | CI cross-build | CI cross-build | CI cross-build |
| Executor (Rust today) | `executor-release`, Linux runner | `executor-release`, macOS runner | not built (non-goal) |
| Executor (Go, after the probe) | CI cross-build | CI cross-build | not built (non-goal) |
| Gateway (Rust today) | `gateway-release` | not built | not built |

All Go targets build in one Linux job with `CGO_ENABLED=0`. A downloaded
darwin binary that is not signed may be quarantined by macOS; this stays
open question 5 of [`fleet-stack.md`](./fleet-stack.md#open-questions).

## `fleet-toolchain.md` decisions

| Decision | Effect |
| --- | --- |
| D1 Node 22 | Holds for the app, the browser checks and the outside tests; cancelled for the coordinator when its last slice moves. |
| D2 shared Rust `CARGO_TARGET_DIR` | Holds while Rust code exists; cancelled when the executor and gateway are ported. |
| D3 own `s3.ts` | Holds; its Go successor follows the same rule (own SigV4 signer, see the module table). |
| D4 `node:sqlite` and the bundled server | Cancelled when the coordinator's last slice moves. |

## Handoff questions

Answers to the questions in the
[handoff](../handoffs/toolchain-choice-2026-11-10.md#questions-for-the-session):

1.  **One language or fewer?** One: Go for services, TypeScript for the browser
    app only.
2.  **Type safety for agents.** Go's compiler plus types generated from one
    JSON Schema. The Go port needed 0 compiler-error cycles.
3.  **Test speed.** Outside tests over HTTP against one real Postgres server,
    one database per worktree: 11 tests in about 1.4 s including setup. RLS is
    tested on the real server. The full-suite figure needs the rewrite.
4.  **Executor distribution.** One Linux CI job cross-builds every target with
    SHA-256 files; the executor joins it after the probe.
5.  **Python glue.** Removed by L4 and Connections C1; nothing new in Python.
6.  **Security posture.** Memory-safe language, `CGO_ENABLED=0` (no native
    code), 7 third-party modules compiled in, the checksum database on,
    `govulncheck` in CI. Signing stays open question 5.
7.  **Cost against gain.** Measured above; the port took about 12 agent
    minutes for the slice.

## Third-party Go modules

Every module that Go code imports needs a row here. The standard library is
the default ([`fleet.md`](../../fleet.md#stack)).

| Module | Replaces | Why the standard library does not cover it |
| --- | --- | --- |
| `github.com/jackc/pgx/v5` (closure: `pgpassfile`, `pgservicefile`, `puddle/v2`, `x/crypto`, `x/sync`, `x/text`) | `pg` | The standard library has no Postgres driver. `database/sql` would still need a driver and hides LISTEN, connection startup options and the simple protocol for multi-statement migrations. |

Not added:

*   **ClickHouse:** its HTTP interface through `net/http`; no client library.
*   **S3 (Garage):** a hand-written SigV4 signer on `crypto/hmac` and
    `crypto/sha256`, as `s3.ts` does today in 125 lines. Counted on
    2026-11-10 for `github.com/minio/minio-go/v7` v7.4.1 in an empty module:
    19 third-party modules compiled in, 26 in `go.sum`. The signer adds 0.
*   **Tools:** `govulncheck` runs in CI at a pinned version; it is a tool, not
    a module of Fleet's code, and has no row.

## Coordinator migration order

The Go binary becomes the front door and serves the routes it owns; it passes
every other request to the TypeScript coordinator with
`net/http/httputil.ReverseProxy`. Both processes share one Postgres. The
advisory scope locks (`pg_advisory_xact_lock`) serialize writers across the two
processes. `schema.sql` stays the single schema source and is frozen during a
slice; the Go binary applies it at start.

Each slice:

*   first adds outside tests for its routes in `tests/<area>/`, run against
    the TypeScript coordinator and then against Go, as `tests/trial/` does;
*   ends with those tests green against Go, and the full gate green in CI;
*   deletes the TypeScript code it replaced and the old
    `tests/coordinator/*.test.ts` files whose subject moved. Each slice pull
    request lists the deleted files. This brings forward the review decision
    to delete old tests at the end of the effort: an old test cannot run
    against a Go route.

Rollback point for every slice: the previous release symlink, while the
schema is unchanged. Within a release, a route moves back by removing it from
the Go route table, which hands it to the proxy again.

| Slice | Routes and behaviour | Deleted at the end |
| --- | --- | --- |
| 1. Front door | Go serves credentials, sessions, messages without profiles, host poll, receipts and events, execution events and the stream (the trial slice, SL1-SL11), and proxies the rest; release packaging and the deploy start the Go binary | `tests/trial/arms/typescript.json`; TypeScript stream and session read code; old tests of those routes |
| 2. Dispatch and execution | Full dispatch ranking, subscriptions, budgets, wait reasons, stall questions; profiles, accounts and projects on message submit; receipt envelopes and the artifact gate; transport-conflict records | `dispatch.ts`, related store code, their old tests |
| 3. Operations | `/api/ops/*`: projects, tasks, people access, connections, catalogue, architect routes | the matching TypeScript modules and old tests |
| 4. Pipelines and results | Pipelines, delivery, artifacts and S3, lab, threads, agent tools | the matching modules, `s3.ts`, old tests |
| 5. Remove TypeScript | Go serves every route; the proxy is removed | `packages/coordinator`, PGlite, the esbuild server bundle, `release-smoke.ts` in its Node form, the remaining old tests and helpers (`pg-test.ts`, `project-fixture.ts`); `fleet.md` verification command updated |

The executor and gateway follow as one later effort after the probe, behind
the unchanged `/api/hosts/*`, one host at a time, with a symlink as rollback
([`fleet-stack.md`](./fleet-stack.md#migration-if-the-language-changes)).

## Risks

| Risk | Effect | Mitigation |
| --- | --- | --- |
| The compiled arms were measured under load | Go and Rust speed numbers are pessimistic | Load only slows the compiled arms, so an idle rerun can only improve Go's numbers; rerun `measure.ts go` on an idle machine when the rewrite starts |
| Two coordinators share Postgres during the migration | A write ordering differs between processes | Advisory scope locks in both; SL10 and SL11 run against the mixed setup |
| The executor needs C (SQLite or macOS calls) | One language is not reached | Probe before porting; a need for C is a stop-and-ask |
| `encoding/json` matches keys case-insensitively | A validated field differs from the stored one | Found and fixed in the trial port; validated values are stored, case variants of identity keys are refused |
| An unsigned darwin binary is quarantined | Mac hosts cannot run the CI build | Open question 5 of `fleet-stack.md` |

## Open questions

1.  **Executor probe.** Whether the executor builds without a C compiler; a
    separate worker runs it now.
2.  **Signing** of downloaded binaries on macOS and Windows (open question 5
    of `fleet-stack.md`).
3.  **Host protocol versioning** while hosts update one at a time (open
    question 9 of `fleet-stack.md`).
