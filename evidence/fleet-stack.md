# Fleet stack measurements

Measured numbers that [`docs/designs/fleet-stack.md`](../docs/designs/fleet-stack.md), [`fleet-go-code.md`](../docs/designs/fleet-go-code.md) and [`fleet-stack-trial.md`](../docs/designs/fleet-stack-trial.md) cite. Each section names its machine and commit. Records of what was observed; the rules are in the specification.

## Toolchain measurements

Owner Mac, main `74e0186`, Node 22.18.0, npm 10.9.3, pnpm 9.12.0, Bun 1.3.12, Deno 2.9.5, cargo 1.98.1. Warm means the package manager's global cache is filled. Real disk is the `df` free-space delta, because `du` cannot see APFS clones.

| Option | Install, warm | Real disk per extra worktree | Test suite (coordinator + ui) | Breaks found |
|---|---|---|---|---|
| npm | 2.1 s | 150 MB | 52–62 s, all pass | none |
| pnpm | 1.2 s | 5 MB (APFS clone from the global store) | 52 s, all pass | none |
| Bun | 1.1 s | 144 MB | 7 fail, 3 errors | `better-sqlite3` unsupported; nested `t.test()` not implemented (oven-sh/bun#5090); 5 s default timeout |
| Deno | 3.3 s | 143 MB (`node_modules` still created for npm packages) | 130 s, 129 pass, 1 fail | needs `--sloppy-imports` (`.js` specifiers); `better-sqlite3` bindings missing; type check fails |

| Step | Time |
|---|---|
| `npm run build` | 2.3 s (tsc 2.1 s, esbuild 0.07 s) |
| `tsc --noEmit` | 2.9 s |
| `tsgo --noEmit` (preview) | about 0.15 s; TypeScript 7.0.2 later measured 0.25 s warm |
| esbuild bundle of `packages/coordinator/server.ts` | 0.11 s, 4.3 MB |

| Rust (`fleet-executor`) | Time | Disk |
|---|---|---|
| Per-worktree cold build | 14.7 s | 304 MB (355 MB with test binaries) |
| Per-worktree warm rebuild | 0.6 s | — |
| Shared `CARGO_TARGET_DIR`, first / next worktree | 15.4 s / 0.1 s | one copy for all worktrees |
| sccache, cold / fresh target with warm cache | 16.4 s / 7.3 s | still 300 MB per worktree |
| `[profile.dev] debug = 0` / `"line-tables-only"` | ~14 s | 207 MB / 264 MB |
| `cp -c -R` clone of `target` / `node_modules` | 0.35 s / 2.6 s | ~0 (df delta 0.8 MB) |

Supply chain then: 163 production packages; `@aws-sdk/*` plus `@smithy/*` were 24 of them and 18 MB.

Live S3 check of `s3.ts`, 2026-11-10, from PDS18 through the pds18, mac and windows store tunnels with Access headers: put, get, head, delete, a 256 MiB multipart upload through presigned parts, UploadPartCopy, CopyObject and a presigned GET with matching SHA-256 passed on all three stores.

## Language trial

Owner's Mac, Apple M2, 8 CPUs; commit `8423216` (main with both ports); `node tests/trial/measure.ts <arm> --runs 5 --worktrees 4`. Medians with min-max spread; all 15 runs green.

Only the TypeScript runs are comparable. The arms ran back to back: the 1-minute load average was 3.4 when TypeScript started, 4.9 for Go and 12.9 for Rust, and the script marked every Go and Rust run `comparable=false`. Run 1 of each compiled arm also paid cold caches in all four worktrees at once (edit-to-green 18.3 s for Go, 50.3 s for Rust).

| Measure | TypeScript | Go | Rust |
| --- | --- | --- | --- |
| Cold build, ms | 542 (520-1689) | 6700 (6632-7483) | 39115 (28686-49003) |
| Warm build, ms | 531 (522-550) | 445 (302-543) | 186 (171-205) |
| Rebuild after a one-line edit, ms | 541 (522-563) | 539 (531-590) | 7744 (7294-8208) |
| Compiler peak RSS, KB | 207328 | 395040 | 593856 |
| Outside tests incl. setup, ms | 1271 (1258-1338) | 1413 (1297-1661) | 1425 (1378-1517) |
| Outside tests only, ms | 719 | 546 | 604 |
| Server peak RSS, KB | 145776 | 20784 | 11936 |
| Edit-to-green, 4 worktrees at once, ms | 2775 (2608-3767) | 2611 (2421-18378) | 3228 (3041-50261) |
| Artifact, bytes | 2522405 + Node 110635104 | 15504866 | 3564224 |
| Disk per worktree, KB | 129580 | 191444 | 464792 |
| Third-party packages, whole lockfile | 156 | 11 | 172 |
| Third-party packages, coordinator closure | 146 | not computed | 105 |

TypeScript's cold build reuses the installed `node_modules`; Go's cold build deletes a per-worktree `GOCACHE` and recompiles the standard library.

Build-fix cycles to the first all-green run: Go 1 (a missing `go.sum` entry), Rust 0. Agent time: Go about 12 minutes, Rust about 18 minutes, against a time box of 8 hours. Go port: pull request #105; Rust port: #106, closed unmerged.

Before the trial, on an Apple M2: `node tests/trial/run.ts` against the TypeScript coordinator took about 1.4 s for 11 tests including setup, 0.8 s of it the tests; all 17 planted defects turned their test red. With 4 parallel worktrees on one server, SL8 was red in 1 of 4 before the `held` re-read.

## Executor probe

Owner's Mac, Go 1.27.3, arm64; spikes were in `go/probe/` (pull request #115; deleted after the port). Rust executor for reference: 68 s cold build, 5.9 MB release binary.

| | journal (stdlib) | `modernc.org/sqlite` v1.62.0 | `ncruces/go-sqlite3` v0.37.0 |
|---|---|---|---|
| Crash test (20 rounds, 2 concurrent writer processes, kill -9, reopen) | 3 runs, ~1,050 acks each, 0 lost | 5 runs, 60k–72k acks each, 0 lost | 5 runs, 38k–45k acks each, 0 lost |
| Commit rate | ~200/s (`F_FULLFSYNC`) | ~14k/s | ~9k/s |
| Modules in `go.sum` / linked | 0 / 0 | 25 / 10 | 5 / 4 |
| Binary darwin / linux / windows | 4.1 / 4.4 / — MB | 10.1 / 10.6 / 10.4 MB | 14.0 / 14.6 / 14.8 MB |
| Cold build darwin / linux / windows | 2.8 / 2.9 s / fails | 8.5 / 9.1 / 8.0 s | 10.3 / 10.3 / 11.8 s |
| Reads a Rust-written `journal.sqlite` | no | yes | yes |

ncruces: two early runs with two separate autocommit counts after reopen saw commands and outbox differ by one or two rows; the `sqlite3` CLI found the file consistent, and 10 later single-snapshot runs showed no difference. Unexplained.

Signal spike (`go/probe/signal`, darwin, `CGO_ENABLED=0`): the audit-token signal through purego was refused for an old token after exec and after exit, delivered with the current token, and refused after a real pid reuse (pid reused after 93557 starts; the new process stayed alive). All spikes cross-built for darwin/arm64, linux/amd64 and windows/amd64 except the stdlib journal on Windows (`syscall.Flock`); only the darwin binaries were run.
