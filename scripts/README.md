# Repository tools

## Overview

Build, CI, documentation, Lab import and delivery tools run from the repository root. These are operator programs, not coordinator operations.

## Interface

| Source | Entry or exported boundary |
|---|---|
| [check.sh](check.sh) | Every check of a change, the same in CI (jobs `go` and `app`) and locally: `scripts/check.sh [go|app]` ([checks per change](../docs/designs/fleet-delivery-pipeline.md#checks-per-change)). |
| [design-report-comment.ts](design-report-comment.ts) | `diffReport`, `appNotRun`, `comment`: the PR design report comment against the latest `main` artifact (new, fixed, changed figures; standing report folded); `--self-check`. |
| [docs-report.ts](docs-report.ts) | `kind`, `linkFixOnly`, `anchors`, `links`; CLI `--base <ref>` or `--self-check`. |
| [ui-context.ts](ui-context.ts) | `rules`, `principles`, `cited`, `refs`, `links`, `section`, `context`; CLI `<page>` prints the minimal reading for one page (spec, canvas, blocks, controls, patterns, cited rule texts, the data-layer rules). |
| [page-budget.ts](page-budget.ts) | CLI `<path>...`: reads, bytes and settle time of a deployed screen on open, reload, Alt-Tab and Back, as site-agents ([data layer](../docs/designs/fleet-data-layer.md)); a tool, not a gate. |
| [lab-drift.ts](lab-drift.ts) | `Source`, `Status`, `slug`, `section`, `hash`, `repoDir`, `current`, `check`, `registryDir`, `loadRegistry`, `run`; CLI can report, `--write`, or `--pin` registry sources. |
| [lab-example-format.ts](lab-example-format.ts) | `ExampleFile`, `Example`, `useCaseIds`, `parseExample`: validated canonical Lab inputs. |
| [lab-export.ts](lab-export.ts) | `parseExport`, `examplesFromExport`: version-1 project exports to inline examples. |
| [lab-examples.ts](lab-examples.ts) | `readExamples`, `fileBytes`, `runChecks`, `checkExample`, `importExamples`; CLI `check`, `import`, `from-export`. |
| [connections-import.sh](connections-import.sh) | Import a key file into the coordinator host's connection secret directories, printing fingerprints. |
| [host-secret.sh](host-secret.sh) | Create a host-secret file without overwriting it; print only its SHA-256. |
| [key-add.sh](key-add.sh) | Read one provider key from stdin and call `connections_key_add`; print the projected fingerprint/state reply. |
| [delivery-latency.sh](delivery-latency.sh) | Report merged PR stage times from GitHub, current host updates and the PDS18 journal. |
| [watch-landing.sh](watch-landing.sh), [watch-landings.sh](watch-landings.sh) | Observe one PR, or concurrent open/queued/recently merged PRs, through release and host/site installation; after a landing, `watch-landings.sh` prints the J1–J5 walk report ([tests/journeys/walk.ts](../tests/journeys/walk.ts)) without changing its exit. |
| [lab/](lab/README.md) | Shipped pipeline scripts and Blender artifact processing. |

## Invariants

- `lab-example-format.ts:parseExample` rejects unknown use cases, duplicate paths/check IDs and invalid relative paths. `lab-examples.ts:fileBytes` checks binary seed hashes.
- `lab-examples.ts:checkExample` checks the reference and untouched input separately; examples with no script checks remain owner/judge-rated.
- `design-report-comment.ts` and the J1–J5 walk report information only: a missing baseline, artifact or token becomes a line of text, never a failure.
- `docs-report.ts` reports warnings without failing the build. Its scope and classification follow [docs governance](../docs/designs/fleet-docs-governance.md).

## Side Effects

`check.sh` installs dependencies, builds binaries, starts a test database and runs checks. Lab example checks execute shell commands in temporary directories; import sends approved examples to the coordinator. Registry `--write` and `--pin` edit configuration. Key tools write secret files or call the deployed app. Delivery tools read GitHub/site/journal data and fetch Git refs. Do not run mutating commands merely to inspect their interface.

## Verification

Documentation changes: `node scripts/docs-report.ts --base origin/main` and `git diff --check`.
Every change: `scripts/check.sh` (about a minute; [fleet.md](../fleet.md#verification)).
