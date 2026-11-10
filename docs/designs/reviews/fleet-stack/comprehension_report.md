# Stage 6: Comprehension Test (fleet-stack.md)

Date: 2026-11-10. Reviewer: `egm-comprehension` (fresh subagent, document only).

## What this proposes

The document splits an earlier "move everything to Go" concept into separate decisions. First comes Step 0: get Fleet's rules (`fleet.md` and a pinned "specq core") to coding agents through `AGENTS.md`, a new `config/projects/fleet/` project, and a CI lint and report that only warns. Then three changes are made on the current stack (TypeScript coordinator, Rust executor): CI builds and publishes executor files for linux and darwin (L2), one JSON Schema in `contracts/` generates types for the coordinator, app and executor (L3), and the executor replaces its Python MCP servers with its own subcommands (L4). After that, one slice is ported to Go and to Rust. Both ports are measured against the improved current stack, and some results reject a candidate. The owner then writes the language decision into `fleet.md` before any code changes. If no port wins, Fleet stays on TypeScript and Rust.

## How the existing system works, per this document

A TypeScript coordinator keeps its data in Postgres; most data sits in one table as `jsonb` documents. `store.ts` handles implicit transactions, savepoints, advisory locks and retries. A Rust executor runs on Unix hosts and talks to the coordinator over a host protocol (poll, events, receipts) with no table. The executor starts Python MCP servers with `python3`, and the coordinator also runs some Python. CI builds the executor only in debug mode for tests; hosts get it by hand. `contracts/` holds example JSON and one JSON Schema that is copied but never validated. The known seam bugs are in event payloads. `AGENTS.md` does not point agents to `fleet.md` or specq.

## Findings and resolution

| # | Sev | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | Major | specq undefined; `engineering/*.md` location unclear | Fixed: Terms list defines specq with link |
| 2 | Major | Rust arm scope unclear for the executor | Fixed: Go arm ports executor parts; Rust arm ports coordinator slice only |
| 3 | Major | "event shape from `jsonb`" reads as sqlc leftover | Fixed: generated from contract JSON Schema |
| 4 | Major | Handoff not linked | Fixed: linked in header |
| 5 | Major | D1-D4 cited without gloss | Fixed: glossed in Terms |
| 6 | Major | Seam bugs only named | Fixed: described in Terms |
| 7 | Major | "Old tests" undefined; step 8 deletion contested | Fixed: defined; step 8 marked pending open question 9 |
| 8 | Nit | Brief is Russian; decisions delegated to it | Partly: step 18 keeps pointer; brief is the owner's record |
| 9 | Nit | L numbering and order | Fixed: Terms states origin and independence |
| 10 | Nit | agentq, lasting test, PGlite undefined | PGlite fixed; agentq cited with survey path; "lasting test" is specq wording, kept |
| 11 | Nit | N not fixed | Fixed: owner sets N before the trial |
| 12 | Nit | `tests/executor/*.py` fate | Fixed in step 16 |
