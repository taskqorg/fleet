# Repository tools

## Overview

Build, CI, documentation, Lab import and delivery tools run from the repository root. These are operator programs, not coordinator operations.

## Interface

| Source | Entry or exported boundary |
|---|---|
| [ci-job.sh](ci-job.sh), [ci-part.sh](ci-part.sh) | Shared workflow commands: app/browser/release jobs and Go build/test parts; workflow setup remains in `.github/workflows/`. |
| [test-selection.ts](test-selection.ts) | `comparison`, `changedPaths`, `select`, `execution`, `makePlan`, `run`, `previousResult`, `queueQualified`, `verifyEvidence`: exact-candidate family selection and evidence validation; CLI emits a plan or accepts `--verify`. |
| [pds18-route.ts](pds18-route.ts) | `FAMILIES`, `plan`, `dead`; CLI emits runner matrices, `--watch` cancels stalled pool runs, `--self-check` checks routing fixtures. |
| [docs-report.ts](docs-report.ts) | `kind`, `linkFixOnly`, `anchors`, `links`; CLI `--base <ref>` or `--self-check`. |
| [test-rule-report.ts](test-rule-report.ts) | Static test metadata inventory; `--strict` fails missing requirement/risk annotations in outside tests; `--self-check`. |
| [lab-drift.ts](lab-drift.ts) | `Source`, `Status`, `slug`, `section`, `hash`, `repoDir`, `current`, `check`, `registryDir`, `loadRegistry`, `run`; CLI can report, `--write`, or `--pin` registry sources. |
| [lab-example-format.ts](lab-example-format.ts) | `ExampleFile`, `Example`, `useCaseIds`, `parseExample`: validated canonical Lab inputs. |
| [lab-export.ts](lab-export.ts) | `parseExport`, `examplesFromExport`: version-1 project exports to inline examples. |
| [lab-examples.ts](lab-examples.ts) | `readExamples`, `fileBytes`, `runChecks`, `checkExample`, `importExamples`; CLI `check`, `import`, `from-export`. |
| [connections-import.sh](connections-import.sh) | Import a key file into the coordinator host's connection secret directories, printing fingerprints. |
| [host-secret.sh](host-secret.sh) | Create a host-secret file without overwriting it; print only its SHA-256. |
| [key-add.sh](key-add.sh) | Read one provider key from stdin and call `connections_key_add`; print the projected fingerprint/state reply. |
| [delivery-latency.sh](delivery-latency.sh) | Report merged PR stage times from GitHub, current host updates and the PDS18 journal. |
| [watch-landing.sh](watch-landing.sh), [watch-landings.sh](watch-landings.sh) | Observe one PR, or concurrent open/queued/recently merged PRs, through release and host/site installation. |
| [lab/](lab/README.md) | Shipped pipeline scripts and Blender artifact processing. |

## Invariants

- `test-selection.ts:makePlan` falls back to all families on unknown comparison/history; `verifyEvidence` checks the candidate, policy and executed jobs. Pull-request heavy work is deferred to the merge queue by `run`.
- `pds18-route.ts:plan` gives no pool slots to untrusted events or an unavailable pool. Workflow admission remains a separate boundary; routing is not a security grant.
- `lab-example-format.ts:parseExample` rejects unknown use cases, duplicate paths/check IDs and invalid relative paths. `lab-examples.ts:fileBytes` checks binary seed hashes.
- `lab-examples.ts:checkExample` checks the reference and untouched input separately; examples with no script checks remain owner/judge-rated.
- `docs-report.ts` reports warnings without failing the build. Its scope and classification follow [docs governance](../docs/designs/fleet-docs-governance.md).

## Side Effects

CI commands install dependencies, build binaries, start test databases/containers and run checks. Lab example checks execute shell commands in temporary directories; import sends approved examples to the coordinator. Registry `--write` and `--pin` edit configuration. Key tools write secret files or call the deployed app. Delivery tools read GitHub/site/journal data and fetch Git refs; the pool watchdog can cancel its workflow run. Do not run mutating commands merely to inspect their interface.

## Verification

Documentation changes: `node scripts/docs-report.ts --base origin/main` and `git diff --check`.
Existing focused checks: `node scripts/docs-report.ts --self-check`, `node scripts/pds18-route.ts --self-check`, `node scripts/test-rule-report.ts --self-check`, `node --test tests/ui/test-selection.test.ts`.
The full CI gate belongs on CI, not the owner's Mac; see [fleet.md](../fleet.md).
