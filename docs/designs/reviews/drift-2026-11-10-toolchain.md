# Drift report: toolchain slice, 2026-11-10

Scope: `docs/designs/fleet-toolchain.md` and the docs it touches (`docs/infrastructure-build.md`, `docs/coordinator-build.md`, `docs/executor-build.md`, the artifact and verification sections of `docs/designs/fleet-data-foundation.md`, READMEs with build or test commands), checked against `origin/main` at `8cedc62`. The toolchain merged at `422dcf9` (PR #46). Later commits on main: Lab Stage 2 (#51, #52, #53).

## Matches the code

- pnpm 10.34.9 pinned in `packageManager`; `pnpm-workspace.yaml` has `minimumReleaseAge: 1440` and `onlyBuiltDependencies: [esbuild]`. All versions pinned, `playwright` included.
- `tsconfig.json` has `noEmit`, `allowImportingTsExtensions`, `erasableSyntaxOnly`. Imports use `.ts`. Tests run with Node's type stripping; no `tsx`.
- `typecheck` runs TypeScript 7.0.2; the build is esbuild only after the type check.
- `Cargo.toml` sets `[profile.dev] debug = "line-tables-only"`. `orca.yaml` setup is `pnpm install --frozen-lockfile`. No committed `.cargo/config.toml`.
- No `better-sqlite3` and no `@aws-sdk/*` in `package.json`. `packages/coordinator/s3.ts` exists.
- CI: all three jobs use `pnpm/action-setup` and `pnpm`; `postgres-roles` runs `tests/coordinator/release-smoke.ts` and `node --test --test-concurrency=1`.
- `docs/executor-build.md` uses `fleet-executor` and `cargo metadata` `target_directory`. `docs/coordinator-build.md` setup line uses pnpm, the bundle and `migrate-sqlite.js` with `node:sqlite`.

## Doc-side drift, fixed in this PR

1. `fleet-toolchain.md` status still said "draft for owner approval". All five phases are merged. Now: implemented, deploy waits for the coordinator.
2. `fleet-toolchain.md` D2 lists six files that resolve the executor path. The code resolves it in two helpers, `tests/coordinator/executor-binary.ts` and `tests/executor/executor_binary.py`. Recorded in the implementation notes.
3. `fleet-toolchain.md` D4 says the bundle is `server.js` with `server.js.map`. The code has a two-line `server.js` that loads `server-main.js` (with `server-main.js.map`). Recorded in the implementation notes.
4. `fleet-toolchain.md` D4 lists `pipe_search.py` as an asset. Lab Stage 2 replaced it with `connection_call.py` and added `config/lab` and `config/skills`. Recorded in the implementation notes.
5. `fleet-toolchain.md` "Expected end state" had a broken sentence (`@types/better-sqlite3`., 5 MB). Fixed.
6. `docs/infrastructure-build.md` release layout listed `dist/config/{agents,projects,pipelines,catalogue.json}`. `package-assets.mjs` also copies `lab` and `skills`. Fixed.
7. `fleet-data-foundation.md` items 9, 10 and 50 still planned to remove `better-sqlite3` at Phase 7, and item 50 named `tests/coordinator/lab-packaged.ts`, which no longer exists. Items 9 and 10 are marked done; item 50 now removes `migrate-sqlite.ts` and `FLEET_DATABASE`.
8. `fleet-data-foundation.md` Verification Plan used `npm`, `tsc --noEmit` and the crate name `executor`. Now `pnpm run build`, `pnpm test`, `pnpm run typecheck` and `fleet-executor`.
9. `fleet-view-urls-and-list-path.md` listed the Node toolchain slice as planned. Now marked done, with a link and the choice made.
10. `docs/coordinator-build.md` evidence lines named `npx tsx tests/coordinator/browser.ts` and `proxy-browser.ts`. Both files are gone. The lines now say so. The dated `npm test` evidence is left as history.

Left as is: the briefs (`web-stack-brief.md`, `fleet-data-foundation-brief.md`) and review reports describe the state at their date.

## Migrations 11 and 12

- Migration 11 (Lab groundwork: `experiment` and `score` tables with RLS and change triggers, `execution.request_id`, question kind `rating`) came in PR #47 (`a063386`, Lab Stage 0).
- Migration 12 (artifact role `example`, `artifact.approved_at`, unique example index) came in PR #49 (`1cc572c`, Lab Stage 1).
- Design docs: both are in `docs/designs/fleet-lab.md` (Stage 0 and Stage 1) and in the migration log of `docs/designs/fleet-data-foundation.md` (§ table list). Covered.
- `postgres-roles` CI: every Postgres-backed test there applies the whole `schema.sql`, migrations 11 and 12 included, as the owner `fleet` on roles from `deploy/infrastructure/fleet-postgres.sql`, so the DDL is checked without superuser rights. New tables get `fleet_app` grants through default privileges. But the tests that read and write `experiment`, `score` and example artifacts (`lab-trial.test.ts`, `lab-registry.test.ts`) use `createApp` with PGlite, also in that job. So row-level security and `fleet_app` grants on these tables are not exercised on real Postgres. See follow-up 1.

## Code-side drift, follow-ups

1. Run the Lab store tests (`lab-trial.test.ts`, `lab-registry.test.ts`) through `freshDb` / `appUrl` when `FLEET_TEST_PG` is set, so `postgres-roles` covers `experiment`, `score` and example artifacts as `fleet_app` under RLS.
2. Data foundation Phase 7: remove `migrate-sqlite.ts` and `FLEET_DATABASE` after the rollback window (item 50 as now written). Its `node:sqlite` ExperimentalWarning goes with it.
3. Cloudflare cache-bypass rule on `fleet-store-*` hostnames: done 2026-11-10 (owner approved; zone rule `set_cache_settings cache:false`, verified with a `.bin` HEAD on all three tunnels, recorded in docs/infrastructure-build.md). No action left.
