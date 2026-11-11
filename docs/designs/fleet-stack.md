# Fleet stack and language

Fleet's languages, toolchain, contract source, rule delivery and the order in which the services move to Go. The binding rules are in [`fleet.md`](../../fleet.md#stack); this file holds their detail. Executor release files are a separate topic: [executor delivery](fleet-executor-delivery.md). Measured numbers are in the [stack evidence](../../evidence/fleet-stack.md).

## Decision

Every Fleet service is written in Go. The browser app stays TypeScript (Preact, esbuild). Reasons and compared options: [why this stack](#why-this-stack).

Order:

1. The coordinator (`go/coordinator`), rewritten in one pass ([coordinator rewrite](fleet-go-code.md#coordinator-rewrite)).
2. The executor (`go/executor`) and the gateway (`go/gateway`) together, after the coordinator ([executor and gateway](fleet-go-code.md#executor-and-gateway)).

Never port two components at once.

Today: the coordinator, the executor and the gateway are Go (`go/coordinator`, `go/executor`, `go/gateway`); there is no Rust and no Python in the repository.

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
| Typed seams after the port (quality round, 2026-11-11) | hand checks on raw JSON | After the port the generated types were used in 0 places, and every live regression came through a seam (host protocol, `jsonb` payloads, app operations). |

## Delivery order and change cycles

- Build the operator and architect experiences on the web first. Desktop packaging for Windows, macOS and Linux follows later and reuses the web UI and the shared operations; it adds no second business-logic implementation.
- Web interface, coordinator and execution hosts change on their own cycles. A UI-only edit rebuilds no executor. Updating the web interface cancels no remote work and restarts no executor. A coordinator update follows the recovery contract ([execution continuity](../execution-continuity.md)).
- Interfaces between independently updated components are versioned enough to expose incompatibility. Not built: host protocol versioning ([open questions](#open-questions)).
- A stack choice is made from measured edit-to-green and edit-to-deploy time, supply chain, maintainability and deploy effort, and is recorded in `fleet.md` before code changes.

## Rule delivery

- `AGENTS.md` sends every agent attempt to `fleet.md` and the specq shared core at a pinned commit.
- `fleet.md` names one merge-gate command and a diagnostic time budget of 180 s on an idle Mac ([verification](../../fleet.md#verification)). The full gate runs only in CI ([why](#why-this-stack)).
- CI runs specq `contract_lint.py` on `fleet.md` and `scripts/test-rule-report.ts --strict`: an outside test in `tests/go/` that names no requirement or risk fails CI; any other test is reported as a warning.
- Not built: a `fleet` project in `config/projects/`, so that specq rule delivery also applies to agents Fleet itself dispatches to work on Fleet.

## Toolchain

The labels D1 and D3 are cited by code comments. Each stays while the code it governs exists.

### D1. Node 22 and pnpm

- Node 22 (`engines.node` `>=22.18`). Bun and Deno were rejected: they broke tests and did not beat pnpm on install time or disk ([evidence](../../evidence/fleet-stack.md#toolchain-measurements)).
- pnpm 10, pinned in `packageManager`. `pnpm-workspace.yaml` sets `minimumReleaseAge: 1440` (one day; seven days rejected the pins of the switch) and allows install scripts only for `esbuild`. Every version is pinned.
- Imports use `.ts` specifiers. `tsconfig.json` sets `noEmit`, `allowImportingTsExtensions` and `erasableSyntaxOnly`; tests and scripts run with Node's native type stripping, without `tsx`.
- The type check is TypeScript 7 (`typescript` 7.0.2, the native compiler) in `pnpm run typecheck`. The app build is esbuild (`app/build.mjs`).
- Holds for the app, the browser checks and the outside tests.

The label D2 (shared Rust target per machine) ended with the Rust executor and gateway.

### D3. Own S3 client

`go/coordinator/d_s3.go` signs with SigV4 on `crypto/hmac` and `crypto/sha256`; no S3 module ([third-party Go modules](fleet-go-code.md#third-party-go-modules)). It keeps the Garage setup: region `garage`, path-style URLs, no request or response checksums, one client per store and route, Access headers and `Accept-Encoding` added after signing (Access headers only on an allowed tunnel route), no redirects. It covers Put, Head, Get with Range, Delete, Copy, the multipart operations and UploadPartCopy, presigns GET and UploadPart, and retries 3 times with jittered backoff on 429, 5xx, throttling codes and network errors. An idle timeout (`FLEET_S3_IDLE_TIMEOUT_MS`, default 60 s) replaces a total request cap. `go test` checks the AWS GET Object vector.

The TypeScript-era label D4 (`node:sqlite` and the bundled Node server) ended with the TypeScript coordinator.

## Contracts (L3)

Moved to [fleet-contracts.md](fleet-contracts.md).

## MCP servers as executor subcommands (L4)

- The executor starts delegation, design-rules and connection servers as `fleet-executor mcp <name>` (`go/executor/mcp_*.go`); no Python process serves them. `tests/executor/mcp-replay.test.ts` replays stdio exchanges (`tests/executor/fixtures/mcp.json`, first captured from the former Python servers) against the Go binary; a malformed line answers the JSON-RPC parse error `-32700 "Parse error"`.
- The Access origin guard is the gateway's `guard` command (`go/gateway/guard.go`).

## Go code

Moved to [fleet-go-code.md](fleet-go-code.md).

## Coordinator rewrite

Moved to [fleet-go-code.md](fleet-go-code.md#coordinator-rewrite).

## Executor and gateway

Moved to [fleet-go-code.md](fleet-go-code.md#executor-and-gateway).

## Trial (L1)

Moved to [fleet-stack-trial.md](fleet-stack-trial.md).

## Risks

| Risk | Effect | Mitigation |
| --- | --- | --- |
| The compiled trial arms were measured under load | Go and Rust speed numbers are pessimistic | Rerun `measure.ts go` on an idle machine |
| The rewrite drops a behaviour the TypeScript coordinator had | A screen or host call stops working | Each area lists ported routes and dropped behaviour in its pull request; the app checks in `tests/ui` run against the binary |
| `encoding/json` matches keys case-insensitively | A validated field differs from the stored one | Validated values are stored; case variants of identity keys are refused (`go/coordinator`) |
| The executor's private libproc symbol disappears | Identity-bound signals on macOS stop working | The executor refuses to signal when the symbol is missing |
| An unsigned darwin binary is quarantined | Mac hosts cannot run the CI build | [Executor delivery](fleet-executor-delivery.md); signing is an open question |

## Open questions

1. **Signing** of downloaded binaries on macOS and Windows.
2. **Host protocol versioning** while hosts update one at a time: a protocol version in the poll request, or additive-only schema changes with the coordinator accepting the previous version.
3. **Lasting executor publication**: where hosts download from, which version, who starts an update ([executor delivery](fleet-executor-delivery.md)).
4. **Requirement files**: which files hold requirements for the whole system; SL1-SL11 are the only identified set today.
