# Stage 7: Critic Review (fleet-stack.md, Overview to Alternatives)

Date: 2026-11-10. Reviewers: `egm-design`, `egm-structural` (standard `feature-spec-standard.md`), `egm-style`. Fresh subagents, read-only.

Verdict before fixes: 2 Blockers (same gap: no Risks section), 18 Majors, many Nits. After fixes: no open Blocker.

| # | Reviewer | Sev | Finding | Resolution |
| --- | --- | --- | --- | --- |
| 1 | design, structural | Blocker | No Risks & Mitigations / NFR section; executor integrity, generator supply chain, version skew, trial overrun unowned | Fixed: NFR section with risk table, rollout/rollback; SHA-256 per executor copied from coordinator pattern (`ci.yml:110-112`) |
| 2 | design | Major | CUJ 2 overpromises where code reads through `any` | Fixed: CUJ narrowed; step 11 moves seam reads to generated types; risk row |
| 3 | design | Major | No host protocol versioning across one-by-one updates | Open question 11 (architecture choice, owner) |
| 4 | design | Major | Generator location and toolchain unstated | Open question 12, chosen at L3 start |
| 5 | design | Major | Go and Rust arms measure different work | Fixed: metrics split by coordinator and executor part |
| 6 | design | Major | No sample size or variance plan for agent metrics | Fixed: median and spread, ties; run count is open question 10 |
| 7 | design | Major | No time box or stop rule for the trial | Fixed: rejection row; box set by owner (open question 10) |
| 8 | design | Major | Step order allows deleting old tests before L3/L4 | Held: open question 9 awaits owner; risk row added |
| 9 | design | Major | Numbering note contradicts trial baseline | Fixed |
| 10 | design | Major | `@clickhouse/client` question answerable | Evidence added; kept open per coordinator (8 brief questions stay open) |
| 11 | structural | Major | Alternatives miss reasons for C, E, TypeSpec; mixed format | Fixed: tables with status and reason |
| 12 | structural | Major | No Step 0 subsection in Technical Plan | Fixed |
| 13 | structural | Major | L4 subsection thin | Fixed: three MCP servers named (`delegation.py`, `connection_call.py` mcp mode, `design_rules.py`), done-condition |
| 14 | structural | Major | No CUJ for L4; CUJ 3 not verifiable | Fixed: CUJ 4 added; CUJ 3 names files and SHA-256 |
| 15 | structural | Major | Handoff questions 3 and 6 not stated | Fixed |
| 16 | style | Major | "Strong", "proven" for protobuf; vague JSON mapping | Fixed |
| 17 | style | Major | "Best for" column states unmeasured advantages | Fixed: "Expected advantage (unmeasured)" |
| 18 | style | Major | "beats the baseline on the cycle", "measured win" undefined | Fixed: tied to rejection conditions |
| 19 | all | Nit | `text` fence, wrap, duplicate seam definition, open-question format, "(Rust)" placement, brief divergence on seam-bug row, baseline row placement, separators, judgement words ("fair", "hard part", "invented", "fate"), "nothing to drift" overclaim, OpenAPI vagueness, mixed status words, long sentence in Q9 | Fixed |
| 20 | design | Nit | Linux C library target | Fixed: risk row and step 12 |
| 21 | design | Nit | Linux hosts for clone measurement | Fixed: risk row, measure per host |
