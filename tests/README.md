# tests

* `coordinator/*.test.ts`: coordinator tests on PGlite, run by `npm test`. Files ending in `-browser.ts`, `-packaged.ts` or `-ops.ts` are manual or browser scripts, not part of `npm test`; `*-fixture.ts`, `*-helpers.ts`, `s3-fake.ts` and `fixtures/` are shared helpers.
* `ui/*.test.ts`: unit tests for `packages/ui` and the app, run by `npm test`. `*.check.ts` and `*.check.mjs` are browser checks run by `npm run check:design` (warnings only).
* `executor/*.py`: protocol and native probes for `crates/executor`; Rust unit tests live in the crate and run with `cargo test`.

CI (`.github/workflows`) runs build, `cargo test`, `npm test`, then the design report.
