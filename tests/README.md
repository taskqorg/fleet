# tests

* `trial/*.test.ts`, `go/*.test.ts`: outside tests of the Go coordinator over HTTP, run by `tests/go/run.sh` (part of `pnpm test`). `harness.ts` starts the binary on a fresh Postgres database; `s3-fake.ts` is the fake store.
* `ui/*.test.ts`: unit tests for `packages/ui` and the app, run by `pnpm test`. `*.check.ts` and `*.check.mjs` are browser checks against the Go binary (`seed.ts` writes their fixtures).
* `executor/*.test.ts`: MCP adapter tests (captured stdio replay, artifact path confinement) against the Go executor binary (`FLEET_EXECUTOR_BIN`, else `go build`). The executor's own tests are `go test ./executor` in `go/`.

CI (`.github/workflows/ci.yml`): job `go` runs `go vet`, `go test -race ./...`, the executor MCP tests and `tests/go/run.sh` against a `-race` coordinator; job `app` the UI unit tests; job `browser` the `*.check.ts` browser checks and the design report.

* `../scripts/test-rule-report.ts`: static requirement/risk inventory for test declarations (TypeScript and Go); warnings, except that with `--strict` (CI) a case in `go/` without both fails; `--self-check` checks the inventory logic.

* `ui/test-selection.test.ts`: deterministic shadow-selector fixtures (event identities, full group union, renames/deletions, unknown impact and exact-candidate evidence refusal), run with `node --test tests/ui/test-selection.test.ts`. No database/browser setup.

CI keeps the `test-selection` and `test-qualification` artifacts for 7 days (the release gate downloads them). The narrow qualified rollout may omit unrelated families only for prose and the inspected Lab-create boundary; the latter artifact binds the plan to actual executed or skipped results. Unknown paths/history and nonempty `FLEET_TEST_FULL` retain full execution. See [selected-gate qualification](../docs/designs/fleet-delivery-pipeline.md#selected-gate-qualification) before enabling any omissions. Warning reports run separately from required behavioral completion; release checks qualification rather than waiting for warning jobs.
