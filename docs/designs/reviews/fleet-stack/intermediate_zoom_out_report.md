# Stage 8: Intermediate Zoom Out (fleet-stack.md)

Date: 2026-11-10. Reviewer: `egm-zoom-out` (fresh subagent). Focus: additions since Stages 5-7.

Verdict: no blockers. Most Stage 7 fixes cut scope.

| # | Sev | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | Major | Rust arm measured twice; "Rust only" row duplicates the "Both" edit-to-green row; reflink risk row | Partly: duplicate row removed; double measurement kept because the coordinator approved it (Stage 5 item D); reflink risk row kept as a measurement note |
| 2 | Major | Open question 11 (protocol versioning) invites unneeded work; hosts already update by hand | Not applied: proposed default "additive-only" is an architecture choice with more than one answer; stays an owner question |
| 3 | Nit | Planted rename measured after "expected equal" | Fixed: one check per arm |
| 4 | Nit | Other Python files have no owning phase | Fixed: `origin_guard.py` in L4, others out of scope |
| 5 | Nit | Contract-source alternatives over-detailed | Not applied: coordinator asked to keep the trade-offs of protobuf and OpenAPI |
| 6 | Nit | Freeze row in risk table is premature | Fixed: moved into migration section |
