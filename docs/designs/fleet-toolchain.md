# Fleet toolchain

Status: draft for owner approval. Decisions D1–D4 below were approved by the coordinator during brainstorm.

## Problem

Each worktree carries 150 MB of `node_modules` and 300–620 MB of Rust `target`. The dependency tree has 163 production packages, a large share of them from a fast-moving SDK. Builds run `tsc` (2.1 s) on every build, and tests run through `tsx`. The owner asked for the best toolchain from evidence, including a runtime switch.

## Measurements

Owner Mac, main `74e0186`, Node 22.18.0, npm 10.9.3, pnpm 9.12.0, Bun 1.3.12, Deno 2.9.5, cargo 1.98.1. Warm means the package manager's global cache is filled. Real disk is the `df` free-space delta, because `du` cannot see APFS clones.

| Option | Install, warm | Real disk per extra worktree | Test suite (coordinator + ui) | Breaks found |
|---|---|---|---|---|
| npm (today) | 2.1 s | 150 MB | 52–62 s, all pass | none |
| pnpm | 1.2 s | 5 MB (APFS clone from the global store) | 52 s, all pass | none |
| Bun | 1.1 s | 144 MB | 7 fail, 3 errors | `better-sqlite3` unsupported; nested `t.test()` not implemented (oven-sh/bun#5090); 5 s default timeout |
| Deno | 3.3 s | 143 MB (`node_modules` still created for npm packages) | 130 s, 129 pass, 1 fail | needs `--sloppy-imports` (`.js` specifiers); `better-sqlite3` bindings missing; type check fails |

| Step | Time |
|---|---|
| `npm run build` | 2.3 s (tsc 2.1 s, esbuild 0.07 s) |
| `tsc --noEmit` | 2.9 s |
| `tsgo --noEmit` (TypeScript native preview) | about 0.15 s |
| esbuild bundle of `packages/coordinator/server.ts` | 0.11 s, 4.3 MB |

| Rust (`fleet-executor`) | Time | Disk |
|---|---|---|
| Per-worktree cold build | 14.7 s | 304 MB (355 MB with test binaries) |
| Per-worktree warm rebuild | 0.6 s | — |
| Shared `CARGO_TARGET_DIR`, first / next worktree | 15.4 s / 0.1 s | one copy for all worktrees |
| sccache, cold / fresh target with warm cache | 16.4 s / 7.3 s | still 300 MB per worktree |
| `[profile.dev] debug = 0` / `"line-tables-only"` | ~14 s | 207 MB / 264 MB |
| `cp -c -R` clone of `target` / `node_modules` | 0.35 s / 2.6 s | ~0 (df delta 0.8 MB) |

Supply chain today: lifecycle scripts run for `better-sqlite3` and `esbuild` only. `@aws-sdk/*` plus `@smithy/*` are 24 of 163 production packages and 18 MB. `playwright` is the only unpinned version (`^1.56.1`).

## Decisions

**D1. Stay on Node 22 and slim it.** Bun and Deno break tests and do not beat pnpm on install time or disk. Their remaining gains (built-in S3 and SQL clients, single binary) touch our riskiest paths (LISTEN, advisory locks, RLS settings, Access headers after S3 signing) for no measured win. Inside Node:
- pnpm 10 (`packageManager` pinned in `package.json`), with `minimumReleaseAge` and lifecycle scripts allowed only through `onlyBuiltDependencies: [esbuild]`. All versions pinned, `playwright` included. Orca and t3code use the same setup.
- Imports use `.ts` specifiers. `tsconfig.json` adds `noEmit`, `allowImportingTsExtensions` and `erasableSyntaxOnly`, which bans enums, namespaces and parameter properties because Node cannot strip them. Tests and scripts run with Node's native type stripping, so `tsx` goes away. Phase 2 confirms that `tsgo` accepts these flags.
- `tsgo --noEmit` is the type check, a separate CI step. The build is esbuild only.
- The pnpm numbers above are from 9.12. Phase 1 pins a pnpm 10 release that has `minimumReleaseAge` and measures again.

**D2. Shared Rust target per machine.** Each machine sets `CARGO_TARGET_DIR` in the environment or the `orca.yaml` setup. There is no absolute path in a committed `.cargo/config.toml`, so CI and PDS18 keep the default `target`. `Cargo.toml` sets `[profile.dev] debug = "line-tables-only"`. The `orca.yaml` setup is `pnpm install`. Cargo locks the shared dir, so parallel worktree builds run one after another. Each takes under 15 s cold and 0.1 s warm, so the queue is short. sccache is rejected: it is one more tool and still leaves 300 MB per worktree.

Code that finds the executor binary through `target/debug/fleet-executor` must resolve the directory as `CARGO_TARGET_DIR` or `cargo metadata` `target_directory`. These places: `tests/coordinator/executor-artifacts.test.ts`, `tests/coordinator/recovery-browser.ts`, `tests/executor/{fake_integration,codex_wire,claude_mcp,antigravity_acp}.py`.

**D3. Our own `s3.ts` replaces `@aws-sdk`.** It is one file using `node:crypto` SigV4: header auth, query presign for GET and UploadPart, `UNSIGNED-PAYLOAD`, and no checksum headers. That gives today's `requestChecksumCalculation: WHEN_REQUIRED` behaviour by construction. It sends requests through `fetch` and streams bodies (`duplex: 'half'`, nothing buffered in memory). Access headers are added after signing, as the middleware does now. It keeps everything the SDK client is set up with today (`artifacts.ts:21-29`):
- `region: 'garage'` and path-style URLs.
- Neither request nor response checksums.
- One client per store and route.
- The loopback route never gets Access headers. A tunnel route gets them only when it passes `allowedTunnel`.
- Two presign lifetimes: `URL_SECONDS` and the 6 h copy source in `placement.ts:50`.
- `ContentType` on CreateMultipartUpload, and `ContentLength` read from HEAD.

The SDK's standard retry also stays explicit in `s3.ts`: 3 attempts with jittered backoff on 5xx, 429/503 throttling and network errors, and no request timeout by default. Presigned URLs are not retried, because the executor fetches them.

It covers 10 operations (Put, Head, Get, Delete, Copy, Create/Complete/Abort MultipartUpload, UploadPart, UploadPartCopy). It extracts UploadId, ETag, CopyPartResult ETag and the error Code from the XML. Errors expose `$metadata.httpStatusCode` and `Code` in the same shape as today. Callers read only the status (404 on HEAD, `artifacts.ts:106`) or swallow errors. NoSuchKey, NoSuchUpload, 412 and 503 SlowDown map to typed codes for the retry above and for tests. Tests cover the AWS SigV4 vectors including presign, plus the existing artifact tests. Before deploy there is one live check through all three store tunnels (pds18, mac, windows) with Access headers: put, get, head, delete, a multipart upload of a few hundred MB, UploadPartCopy and a presigned GET. The live check needs the coordinator's approval first.

**D4. `node:sqlite` replaces `better-sqlite3`, and the server ships as a bundle.** `node:sqlite` comes in three places: `migrate-sqlite.ts`, `tests/coordinator/migrate.test.ts` and `tests/coordinator/recovery-browser.ts`. `db.transaction(fn)()` becomes explicit `BEGIN`/`COMMIT`/`ROLLBACK`. `{readonly, fileMustExist}` becomes `{readOnly: true}` with an `existsSync` check first. After D3 and D4 there are no native dependencies.

The bundle stays where the compiled file is today: `dist/packages/coordinator/server.js` with `server.js.map`. A second entry, `dist/packages/coordinator/migrate-sqlite.js`, keeps the one-off migration runnable. Every relative path then stays the same. Module-relative paths keep working: `../../config/` and `../../var/stores/` in `server.ts:9`, `./db/schema.sql` in `store.ts:120`, and `./pipe_search.py` in `projects.ts:14`. So do working-directory paths: `dist/app` in `app.ts:72`, plus `config` in `delivery.ts:22` and `projects.ts:45`, with the unit's WorkingDirectory unchanged. `package-assets.mjs` keeps copying the same assets: `config/{agents,projects,pipelines,catalogue.json}`, `contracts/lab-config.json`, `db/`, `pipe_search.py` and `delivery_worker.py`. The release is today's `dist` tree, with all other compiled files and `node_modules` left out. `package.json` stays in the release so `.js` loads as ESM. The release keeps today's `var/stores` handling. The same pinned Node runtime (`/home/fleet/runtime/node-v22.18.0-linux-x64`) runs it with `--enable-source-maps`. The bundle gets a `createRequire` banner if a dependency needs `require` or `__dirname`. A CI job copies the release dir without `node_modules`, starts the bundle against the Postgres service, and calls `/overview` and one op. `docs/infrastructure-build.md` records the new layout. Rejected: `bun`/`deno compile` (runtime switch declined), and Node SEA (assets need the SEA blob API, for no gain over one file and the existing runtime).

## Phases (one PR each, CI green, coordinator merges)

1. **Package manager and Rust.** pnpm 10 with the lockfile, pins and supply-chain settings in `pnpm-workspace.yaml`. `onlyBuiltDependencies` is `[esbuild, better-sqlite3]` until phase 3. In CI, every `setup-node cache: npm`, `npm ci` and `npx` across the three jobs moves to `pnpm/action-setup` and `pnpm`. Add `orca.yaml` setup, set the dev debug profile, and resolve executor paths.
2. **TypeScript without tsc or tsx.** Switch to `.ts` specifiers and add the `tsconfig` flags. Use native type stripping in the `test` and `check:filter` scripts and in the `postgres-roles` CI job's `--test-concurrency=1` run. Add a `tsgo` check step and make the build esbuild only, with `start` unchanged.
3. **`node:sqlite`.** Port the three files, then remove `better-sqlite3`, `@types/better-sqlite3` and their entry in `onlyBuiltDependencies`.
4. **`s3.ts`.** Remove `@aws-sdk/*` and add SigV4 vector tests. The live tunnel check runs after approval.
5. **Bundled release.** Bundle with a sourcemap, add the CI job that runs from a release copy, update the infrastructure doc. Deploy only when the coordinator authorizes it.

Expected end state: about 135 production packages instead of 163 (24 SDK, `better-sqlite3` with its install helpers). Dev packages also drop `tsx` and `@types/better-sqlite3`., 5 MB `node_modules` per extra worktree, one shared `target` per machine, a build under 0.3 s and no `node_modules` on PDS18. Each phase's PR records the exact numbers it measured.

## Risks

- `node:sqlite` is stability 1.1 in Node 22. It is used only by the one-off SQLite migration and two tests, and `migrate.test.ts` covers it.
- Our own S3 client owns SigV4 correctness. The vector tests and the live tunnel check cover it.
- `tsgo` is a preview. It only checks types, so falling back to `tsc --noEmit` is a one-line change.
- pnpm's strict `node_modules` can expose undeclared transitive imports. The full suite runs in phase 1.

## Implementation notes

- PRs: #41 (phase 1), #43 (phase 2), #44 (phase 3), #45 (phase 4), #46 (phase 5). Each one is stacked on the one before.
- `minimumReleaseAge` is 1440 minutes (one day), not seven days. The dependencies pinned when we switched were 4 to 8 days old, so a seven-day limit rejected them.
- The type check is TypeScript 7.0.2, the stable release of the native compiler, not the `tsgo` preview. Warm, it takes 0.25 s; `tsc` 5.9 took 2.9 s.
- Because of `node:sqlite`, Node 22 prints an ExperimentalWarning when the migration CLI and its tests run. That is expected. `engines.node` is `>=22.18`.
- Live S3 check, 2026-11-10, from PDS18 through the pds18, mac and windows tunnels with Access headers. It covered put, get, head, delete, a 256 MiB multipart upload through presigned parts, UploadPartCopy, CopyObject, and a presigned GET whose SHA-256 matched. It passed on all three stores, and the test prefix was empty afterwards.
- Cloudflare finding (infrastructure, not the client): on a store hostname, the first HEAD of a key with a cacheable extension (for example `.bin`) returned 403. Cloudflare served that HEAD as a GET to the origin, so the SigV4 signature did not match, and it may have cached the object at the edge. Fleet keys are content hashes and `.pending/<id>.<store>`, so they have no such extension. A cache-bypass rule on the `fleet-store-*` hostnames would remove the risk. That is a Cloudflare configuration change and needs the owner's decision.
