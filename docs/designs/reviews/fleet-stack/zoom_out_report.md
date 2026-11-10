# Stage 5: Initial Zoom Out (fleet-stack.md)

Date: 2026-11-10. Reviewers: `egm-zoom-out`, `egm-inventive-catalyst` (fresh subagents, Read/Grep/Glob only). Standard: `zoom-out-standard.md`. Context: `fleet-stack-brief.md`, `fleet.md`, `fleet-toolchain.md`.

Verdict: no blockers; revisions needed.

| # | Sev | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | Major | L3 generators are new tooling; `lab-config.json` is only copied as an asset (`packages/coordinator/package-assets.mjs`), no generator or validator exists; generator modules not counted | Fixed: wording corrected; generator modules and generation time counted |
| 2 | Major | Old tests deleted in trial prep, before L2-L4 refactors and before the language decision | Held for owner: added as open question 9; step 8 keeps the brief's wording |
| 3 | Major | Only Go had rejection criteria; Rust arm had none | Fixed (approved): "Results that reject a candidate" applied to both |
| 4 | Major | Any single row rejects; seam-bug row true by construction | Fixed (approved): seam-bug row is an expected-equal check; combination stays open question 1 |
| 5 | Major | L3 scope is whole API at once | Fixed (approved): staged by seam, events and host protocol first |
| 6 | Major | Shared `target` lock scored as language property | Fixed (approved): Rust arm measured with shared target and with APFS clone |
| 7 | Major | Baseline and ports test on different databases | Fixed: all arms on same Postgres; PGlite reported separately (brief Б5) |
| 8 | Nit | `contract_lint.py` lives in specq | Fixed: pinned specq checkout, as agentq does |
| 9 | Nit | L4 mixes non-MCP Python | Fixed: listed separately, incl. `tests/executor/*.py` |
| 10 | Nit | Step 18 template detail | Fixed: points to brief structure |
| 11 | Nit | Trial prep missing from Overview order | Fixed |
| 12 | Nit | Trial state after L4 (single SQLite writer) unstated | Fixed |
| 13 | Nit | Reversal check skips Go | Fixed: event type generated for TS, Rust, Go in trial |
| 14 | Nit | Step 0 measurement on one task is noisy | Fixed: same prompt/model; result is a direction |
| 15 | Nit | Freeze of `schema.sql`/`app.ts` unscheduled | Fixed: noted, decided at migration planning |
| 16 | Nit | No gate after L2-L4 if baseline already meets budget | Not applied: would add a decision point the owner did not set; recorded here |
