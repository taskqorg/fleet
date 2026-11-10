# Handoff: choosing Fleet's technology stack (2026-11-10)

For a separate session. The owner has not settled the stack yet. Goal: pick the technologies that are the **safest** and **fastest**: fast to compile, fast to run, and fast for agent-driven development, where coding agents (Codex, Claude, Antigravity) write most of the code in parallel worktrees.

## How to run the session

Use EGM: brainstorm with the owner first, then Goldfish review, then `/egm:egm-draft` (the owner invokes it). Measure on the real repo before deciding. The measurements in `docs/designs/fleet-toolchain.md` show how to do that. Owner preferences are binding:

- The system helps and never blocks.
- Use the 20/80 rule.
- Invent no limits or caps.
- Copy proven designs.
- Fleet is an early prototype with no users yet. Legacy and existing data do not need support, so a rewrite is allowed when the numbers justify it.

## Current stack (main, 2026-11-10)

| Part | Technology | Where |
|---|---|---|
| Coordinator (API, scheduler, change feed, stores) | TypeScript on Node 22.18, Fastify 5, `pg`, `@clickhouse/client`. A single esbuild bundle, with no `node_modules` in the release. | `packages/coordinator` (about 23 files) |
| Operator app (PWA) | Preact 11, Signals, Wouter, `@tanstack/virtual-core`, esbuild | `app/`, `packages/ui`, `packages/view-url` |
| Executor (runs harnesses on each host: Mac, Windows/WSL, Linux PDS18) | Rust (`crates/executor`, about 16 files) | builds on each host |
| Glue and tests | Python scripts (`delivery_worker.py`, `connection_call.py`, origin guard, executor protocol tests) | about 20 files |
| Data | Postgres 18 with FORCE RLS and roles `fleet`/`fleet_app`; ClickHouse in rootless Podman; Garage S3 on each host | `deploy/infrastructure` |
| Tooling | pnpm 10, TypeScript 7 (`tsc`), `node --test`, Playwright, PGlite in tests, GitHub Actions; the CI release artifact is the only deployable | `.github/workflows/ci.yml`, `docs/infrastructure-build.md` |

## What was already decided (docs/designs/fleet-toolchain.md)

- **D1: stay on Node 22 and slim it.** Bun and Deno were measured. Bun had 7 failing tests, missing `better-sqlite3` and missing nested `t.test`. Deno needs `--sloppy-imports` and failed its type check. Neither beat pnpm on install time or disk.
- **D2: one shared Rust target directory per machine.** sccache was rejected.
- **D3: our own `s3.ts` replaced `@aws-sdk`.**
- **D4: `node:sqlite` replaced `better-sqlite3`.** No native dependencies are left.
- **Measured on the Mac:**

  | Step | Time |
  |---|---|
  | build | 2.3 s (tsc 2.1 s, esbuild 0.07 s) |
  | `tsgo --noEmit` | about 0.15 s, vs 2.9 s for `tsc` |
  | test suite | about 52 s |
  | pnpm warm install | 1.2 s, 5 MB per extra worktree |

Web stack reasoning: `docs/designs/web-stack-brief.md`, drift `docs/designs/reviews/drift-2026-11-10-web-stack.md`.

## Pain points seen in practice

- **Three languages** (TS, Rust, Python), so agents switch context and some contracts are duplicated: outbox and transfer classification, transcript normalisation, event shapes. Bugs found today sat on these seams:
  - the app read `ev.item` while the executor wrote `ev.data`;
  - the 3xx and 401 outbox rules lived in two places.
- **Typing gaps in the coordinator.** It is TypeScript with a lot of `any` and very long one-line functions (`app.ts`, `accounts.ts`). They are hard for agents to edit safely, and `/code-review` keeps catching shape bugs.
- **The test suite takes about 52 s**, and the Postgres-roles tests run only in CI.
- **CI timing assertions turned out to be flaky.** Timing is now reported, not asserted.
- **The executor is built on each host.** Mac and WSL need local Rust builds, and release artifacts exist only for the coordinator.
- **Python glue runs on hosts without pinned versions.**

## Questions for the session

1. **One language or fewer languages?** For example: the coordinator in Rust next to the executor (shared types, one toolchain), TS everywhere, or keep the split but generate the shared contracts from one schema.
2. **Type safety for agents.** What gives agents the fastest feedback? Candidates: strict TS with `tsgo`, Rust's compiler, generated types from one schema (JSON Schema, protobuf, TypeSpec).
3. **Test speed.** How do we get the full suite under about 10 s locally and keep the real Postgres RLS tests?
4. **Executor distribution.** Build per platform in CI (macOS, Linux, Windows/WSL) and download, instead of building on each host.
5. **Python glue.** Replace it, pin it (uv), or move it into the main languages.
6. **Security posture.** Memory safety, supply-chain size, native dependencies (none today), signing and checking release artifacts.
7. **Migration cost vs gain.** Measure each option on the real repo, as `fleet-toolchain.md` did. Without numbers, no switch.

## Rules for the session

Never print secrets. Never read `~/.config/ufobe-agents/*` or vault `Keys/`. Measure in worktrees, never on the live stand (PDS18). Other workers are active on main (people-access slices, Conversations stages 1-2), so avoid touching their files.
