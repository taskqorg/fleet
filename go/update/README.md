# Updater

## Overview

`fleet-update` installs signed Fleet releases on executor hosts and PDS18. It verifies artifacts before activation, tracks update state, health-checks restarts, and rolls back unhealthy native releases.

## Interface

- `fleet-update executor` updates one executor host.
- `fleet-update pds18` updates the coordinator side, PDS18 executor, and UI independently. It installs the `fleet-postgres-backup` system timer and service from the verified updater.
- `fleet-update backup-postgres` is the pds18 service entry point. A first run starts after installation, then the timer runs nightly. See the [backup contract](../../docs/designs/fleet-system-management.md#backups).
- `fleet-update install-local <verified-stage> <releases>` prepares the managed CLI cache and switches the local release under the same lock as auto-update.
- `fleet-update selftest` verifies the compiled-in Ed25519 release-key setup.
- The files it shares with the executor are declared in [`contracts/host-files.schema.json`](../../contracts/host-files.schema.json) and generated into `contract`: `update.json` (`contract.UpdateSignal`, written by the executor), `update-state.json` (`contract.UpdateStateFile`, whose `last` the executor sends in its poll), and on PDS18 `pds18-state.json` (`contract.Pds18StateFile`, read by the coordinator). Hosts run mixed versions during an update, so these shapes change only compatibly.
- Linux and macOS trigger helpers write a path watcher for the executor's `update.json` target signal when the matching existing service definition is present.

## Invariants

- Exactly one updater run holds the local lock ([update.go: `lock`](update.go)).
- A release requires a signed `SHA256SUMS`, a matching artifact digest, the requested version in `release.json`, and successful binary selftests before installation ([update.go: `fetch`](update.go)).
- Before switching executors, [`harnesses.go`](harnesses.go) installs the pinned payloads of every managed kind (agent CLIs, host tools; `harnessrelease.Kinds`) after digest verification, unpacking vendor archives with their local links ([`payload.go`](payload.go)), runs each new component's `--version` (one that does not start or names another version refuses the release), and keeps payloads for `current`, `previous`, and any release pinned by `<data>/active-releases/<ver>.lock` (via `flock`).
- Archive extraction permits only local regular-file and directory entries. Installed files become read-only, and release links are changed through temporary symlinks ([update.go: `unpack`, `install`, `relink`](update.go)).
- A release must become healthy after restart (or in-place executor `selfExec` before falling back to unit restart); after three unhealthy starts the updater restores `previous`, records the refusal, and retains the pre-deploy database dump where applicable ([update.go: `settle`, `pruneDumps`, `pruneReleases`](update.go)).
- After installing a coordinator or UI release and before its restart, the app's source maps go to Bugsink when `/etc/fleet/bugsink/` holds `ci-token` and `ingest-url` (PDS18); a failed upload is only a warning ([sourcemaps.go: `uploadSourcemaps`](sourcemaps.go), [error tracking](../../docs/designs/fleet-error-tracking.md#6-source-maps)).
- Cached release metadata is returned only after validation. UI state remains independent from native rollout state, and both UI and native coordinator releases pool hashed `assets/*` into the UI root with 7-day mtime pruning ([update.go: `getCached`](update.go), [ui.go: `cycleUI`, `poolUIAssets`](ui.go)).
- HTTP clients come from [`httpClient`](httpclient.go). [`design_rules_test.go`](design_rules_test.go) (`TestDesignRules`) enforces S10 AST rules across non-test, non-generated `go/update/*.go` files against `designUpdateOwned`: `U-MAP` forbids `map[string]any`/`map[string]interface{}`, `U-GO` forbids bare `go` statements outside `errtrack.Go`, and `U-HTTP` keeps `http.Client` construction in its documented owner.

## Side effects

- Unexpected errors and panics go to Bugsink through [`errtrack`](../errtrack/README.md) with `errtrack.DSN()`, tagged `service` fleet-update.

- Downloads release metadata and archives, writes cached metadata and update state, installs release trees and symlinks, restarts services, and prunes obsolete releases ([update.go: `fetch`, `install`, `pruneReleases`](update.go)).
- Executor mode may write the target-file trigger unit through `runExecutor` and `ensureTrigger` ([main.go](main.go), [trigger.go: `triggerFor`](trigger.go)).
- On PDS18 it may upload source maps to Bugsink's loopback address, run `pg_dump` before a release that carries a new migration (`stagedMigrates`: a migration id the staged `fleet-coordinator migrations` lists and `schema_migration` lacks), copy the verified updater to its root-owned path, and update UI release files ([main.go: `runPDS18`, `installUpdater`](main.go), [ui.go: `cycleUI`](ui.go)).

## Verification

Run from `go/`:

```sh
go test ./update
go test ./update -run TestDesignRules
go run ./update selftest
```
