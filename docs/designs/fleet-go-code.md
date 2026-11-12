# Fleet Go code

The Go code target state, the coordinator rewrite and the executor and gateway port. Split from [fleet-stack.md](fleet-stack.md) on 2026-11-11 ([delivery pipeline, section 8](fleet-delivery-pipeline.md#8-workers-merge-and-watch-their-own-pull-requests)).

## Go code

The rules are in [`fleet.md`](../../fleet.md#stack): standard library first, `CGO_ENABLED=0`, one module `go/` with one `go.sum`, `-mod=readonly`, the checksum database on, `govulncheck` as a warning, contract types from `go/gen`, `-trimpath` release binaries from one Linux job. The schema `go/coordinator/schema.sql` is committed and embedded in the binary (`store.go`). Generated contract types live in `go/contract` and are committed; the `go` job fails when `go generate` changes them. CI gates on three families, reported as the required checks `go`, `app` and `browser` (`go` and `browser` are statuses the `app` job posts after validating the families' jobs). `go`: vet (and a windows/amd64 vet of `go/coordinator`), `go test -race`, the executor MCP adapter tests and the outside tests against a coordinator built with `-race` (`GORACE=halt_on_error=1`); Node runs the tests, no `pnpm install`. `app`: contract lint, typecheck and bundle, UI unit tests, and in a merge group it fails unless `release-build` passed: the release tarballs from `deploy/build-release.sh` for linux/amd64 and darwin/arm64, each binary checked for `CGO_ENABLED=0`, the linux `selftest`s and the migration check ([delivery pipeline, section 4](fleet-delivery-pipeline.md#4-automatic-release)). `browser`: one build, Chromium cached by Playwright version, the filter and Conversations checks, the design book and the warning-only reports.

### Target state

The goal of the quality round (2026-11-11). Each line stays `Not built:` until the code lands.

1. Built in part: one shared generated package `go/contract`, built from `contracts/*.schema.json`; generated Go code and `schema.sql` are committed next to their users; a fresh clone runs `go build ./... && go vet ./...` with no prior step; CI fails on `go generate` drift. Not built: imports from the gateway and typed executor senders (item 2).
2. Built in part: `host-protocol` (the coordinator decodes `/api/hosts/poll` and `/api/hosts/events` into `contract.PollRequest` and `contract.HostEvents`; a field of the wrong type is 400; unknown fields are still accepted while hosts update one at a time, [open question 2](fleet-stack.md#open-questions); `Platform` is an enum), `agent-tools` (`go/executor/mcp-tools.json` is the one source of MCP input schemas, one closed schema per tool; before forwarding a project tool call the executor checks its property names, string lengths and required arguments against it; the project tool names are the contract's `ProjectOperation`, `contract.ProjectOperationValues`), `profile` (`contracts/profile.schema.json`, one validator `contract.ParseProfile` for both sides) and `gateway` (`contracts/gateway.schema.json`: snapshot and call line, one `contract.GatewayCall`). Not built: the rest of `host-protocol` (every `/api/hosts/*` route, one envelope per route, per-kind command payloads, Fleet event types). Handlers decode into structs and refuse unknown fields on closed types. Vendor-owned payloads (native harness records, provider bodies, usage) stay raw.
3. Built in part: the app calls `op('name', request)` typed by the generated `Ops` map, and `Change`, `ChangeKind` and the stream's `ready`/`resync` body come from `ops` (Go embeds `contract.Change`); thread blocks are `Block`, one closed `$def` per kind, and the coordinator builds its blocks from the generated structs. Not built: the executor's `text`, `tool` and `reasoning` builders (`go/executor/harness_blocks.go`) still write maps.
4. Built in part: executions (agent runs, host jobs, pipeline runs and steps) have one state vocabulary (the typed column), their real kind and harness, and no `legacy_kind`; an agent run or host job has one writer. Not built: the same for commands, sessions, questions and tasks; typed tables scanned into structs.
5. Built in part: the coordinator stays one `package main` with files named by domain, no area prefixes, no hook variables or glue files (direct calls), one router, one JSON helper set per binary, one request transaction wrapper (`runTx` over `store.Tx`; `txCtx` joins a transaction already in the context), one number reader (`num`/`numOK`), one millisecond-time reader (`msTime`), one canonical JSON (`canonical`, no HTML escaping) and one body reader (`body`; a non-object body is 400 for every route). Not built: one auth path.
6. Built: binaries built with `-race` for tests; CI families `go` (generate diff, vet, race, outside tests), `app` (the release tarballs from the release script) and `browser` (cached Chromium). Not built: outside tests named by domain and requirement, one non-owner scope file.
7. Not built: every correctness finding of the round fixed with a failing-then-passing test.
8. Not built: docs and code comments name Go paths; nothing points at deleted TypeScript, Rust or Python.

Not doing: many internal packages by domain; typing all untyped references; protobuf, OpenAPI, sqlc, an ORM or a runtime schema validator; rewriting outside tests in Go; replacing Preact; closed types for vendor records; protocol versioning now; replacing SQLite in the executor (the store needs queries and cross-process waits).

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
- The cutover deleted `packages/coordinator` (except `db/schema.sql`), PGlite, `pg`, Fastify, the esbuild server bundle and `tests/coordinator`. The release is the Go binaries plus `dist/app`.

Not built: delivery (`delivery_create`, `delivery_generate`, `delivery_step`, `delivery_get`, `delivery_history`, `session_deliver` and the delivery worker). The TypeScript coordinator never completed a delivery: no executor handled `delivery-step` commands, and nothing launched the worker. It was meant to do the following:

- The operator confirms a static-site task with a project question.
- A research pipeline run, or a retained run, feeds a Codex generation run (gpt-6.1-sol, low effort). That run writes `output/index.html` and CSS with a Sources section.
- A program worker integrates the bounded HTML/CSS artifacts into the project repo (pull request, then merge after checks) and publishes them.
- After a verified merge, `base-updated` asks every environment host to fast-forward its clean checkout.
- `session_deliver` opens and merges the pull request of an agent session's pushed branch `<branchPrefix>/<sessionId>`.

## Executor and gateway

Both were ported to Go in one pass after the coordinator (owner direction 2026-11-11), with no compatibility layer and no fallback to the Rust binaries, behind the unchanged `/api/hosts/*` and `contracts/*.schema.json`. The executor is `go/executor` (binary `fleet-executor`), the gateway `go/gateway` (`fleet-gateway`, with its `guard` command).

- Journal: `modernc.org/sqlite`, WAL, `synchronous=FULL`; one schema source, `go/executor/journal.sql`, applied in sections by `PRAGMA user_version`. The probe showed 0 lost acknowledged commits in the crash test at about 14k commits/s ([evidence](../../evidence/fleet-stack.md#executor-probe)). A standard-library journal was rejected ([why](fleet-stack.md#why-this-stack)).
- macOS kernel calls: `github.com/ebitengine/purego` for `proc_pidinfo` and `proc_signal_with_audittoken`; the signal is refused when the private symbol is missing. Linux identity-bound signals use `os.Process` with pidfd. `golang.org/x/sys/unix` for `Openat`, `Statfs`, `Flock` and the terminal check.
- Preview memory cap: `prlimit --as=<bytes>` in the ffmpeg command line on Linux (Go has no `pre_exec`); on macOS `RLIMIT_AS` is not enforced, as before.
- The executor writes OSC 7501 program status to stderr when stderr is a terminal (working, blocked with kind `auth` on an edge answer to the poll, done, error).
- Tests: `go test ./executor` (fake harnesses, a fake coordinator over HTTP) and the Node MCP replay in `tests/executor`.

Not built: Windows executor and gateway (process control and file safety need their own port: job objects, `LockFileEx`, `os.Root`, ACL checks); a Linux run of the pidfd signal and crash checks in CI.
