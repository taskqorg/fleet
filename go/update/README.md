# Updater

## Overview

`fleet-update` installs signed Fleet releases on executor hosts and PDS18. It verifies artifacts before activation, tracks update state, health-checks restarts, and rolls back unhealthy native releases.

## Interface

- `fleet-update executor` updates one executor host.
- `fleet-update pds18` updates the coordinator side, PDS18 executor, and UI independently.
- `fleet-update selftest` verifies the compiled-in Ed25519 release-key setup.
- Linux and macOS trigger helpers write a path watcher for the executor's `update.json` target signal when the matching existing service definition is present.

## Invariants

- Exactly one updater run holds the local lock ([update.go: `lock`](update.go)).
- A release requires a signed `SHA256SUMS`, a matching artifact digest, the requested version in `release.json`, and successful binary selftests before installation ([update.go: `fetch`](update.go)).
- Archive extraction permits only local regular-file and directory entries. Installed files become read-only, and release links are changed through temporary symlinks ([update.go: `unpack`, `install`, `relink`](update.go)).
- A release must become healthy after restart; after three unhealthy starts the updater restores `previous`, records the refusal, and retains the pre-deploy database dump where applicable ([update.go: `settle`, `pruneDumps`](update.go)).
- Cached release metadata is returned only after validation. UI state remains independent from native rollout state ([update.go: `getCached`](update.go), [ui.go: `cycleUI`](ui.go)).

## Side effects

- Downloads release metadata and archives, writes cached metadata and update state, installs release trees and symlinks, restarts services, and prunes obsolete releases ([update.go: `fetch`, `install`, `pruneReleases`](update.go)).
- Executor mode may write the target-file trigger unit through `runExecutor` and `ensureTrigger` ([main.go](main.go), [trigger.go: `triggerFor`](trigger.go)).
- On PDS18 it may run `pg_dump`, copy the verified updater to its root-owned path, and update UI release files ([main.go: `runPDS18`, `installUpdater`](main.go), [ui.go: `cycleUI`](ui.go)).

## Verification

Run from `go/`:

```sh
go test ./update
go run ./update selftest
```
