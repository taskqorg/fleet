# tests

* `trial/*.test.ts`, `go/*.test.ts`: outside tests of the Go coordinator over HTTP, run by `tests/go/run.sh` (part of `pnpm test`). `harness.ts` starts the binary on a fresh Postgres database; `s3-fake.ts` is the fake store.
* `ui/*.test.ts`: unit tests for `packages/ui` and the app, run by `pnpm test`. `*.check.ts` and `*.check.mjs` are browser checks against the Go binary (`seed.ts` writes their fixtures).
* `executor/*.py`, `executor/*.test.ts`: protocol and native probes and MCP adapter tests for `crates/executor`; Rust unit tests live in the crate and run with `cargo test`.

CI (`.github/workflows`) runs build, `cargo test`, `pnpm test`, then the design report.

* `../scripts/test-rule-report.ts`: warning-only static requirement/risk inventory for test declarations and Python assertions; `--self-check` checks the inventory logic.
