# Coordinator design rules

The checked form of [Fleet standard](../../docs/designs/fleet-standard.md) S1–S10. [`design_rules_test.go`](design_rules_test.go) (`TestDesignRules`) parses every non-test, non-generated Go file of this package. A file listed in its `designRulesLegacy` allowlist may still break the named rules; any other file fails. An allowlisted file that no longer breaks its rule also fails until its entry is removed, so the list only shrinks.

| ID | Rule | Check |
|---|---|---|
| S1 | Every entity is a typed table with real columns and foreign keys; JSONB only in a `payload` column; `deleted_at` plus one `live` policy; visibility through `fleet_visible(project_id)`; functions, triggers and policies in idempotent files under `schema/`. | Review. Not built: a schema check. |
| S2 | Queries only through sqlc from `queries/<area>.sql` into package `fleet/coordinator/db`; no SQL in Go, no `map[string]any` read from the database. | `R-SQL`: a string literal whose first word is `SELECT`, `INSERT`, `UPDATE`, `DELETE` or `WITH` (any case) followed by more text. `R-MAP`: calls to `astr`, `anys`, `mapOf`, a `doc{...}` literal, the type `map[string]any`. |
| S3 | Two transaction entry points, `store.Read` and `store.Write` with caller-declared entity locks; effects after commit only through `afterCommit`. | Review. Not built: a check for other entry points. |
| S4 | Operations fully declared in `contracts/ops-*.schema.json` (including `auth` and `agent`) and registered with one line `op("name", handler)`; one dispatcher `runOp`; 404 for invisible, 403 for a missing role. | `R-OP`: calls to `gatedOp`, `gatedObjOp`, `shaped`, `operation`, `agentTool`. |
| S5 | Each state machine has one transition table and one `<x>Transition` function, the only writer of its state column. | Review. Not built: a state-column write check. |
| S6 | Retried item work only through the work kit; periodic passes only through `every()`; no goroutines, sleeps or tickers elsewhere except HTTP and stream plumbing; owner problems only through `problems.go`; wakes only through `wake_outbox` or `afterCommit`. | `R-GO`: `go` statements, `time.NewTicker`, `time.Sleep`. Not built: the `cause=` write check. |
| S7 | Host protocol has a route map, a typed `Command` union and `Capability` enum; the executor has one outbox, HTTP client factory, outcome classifier, `supervise.Spawn` and goroutine helper. | Review (outside this package). |
| S8 | One `go/mcpserve` package serves every MCP server. | Review (outside this package). |
| S9 | App data only through `useOp(name, request)` and `useListPage`; closed contract row types; one JSON source for presets, view grammar and harness lists. | Review (outside this package). |
| S10 | These rules are checked by a source scan with a shrinking legacy allowlist. | `TestDesignRules` in `go test ./coordinator`. |
