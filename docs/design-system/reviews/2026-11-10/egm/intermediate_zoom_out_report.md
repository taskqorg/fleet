# Stage 8 Intermediate Zoom Out: methodology-brief.md

Reviewer: `egm-zoom-out` with `zoom-out-standard.md`, building on Stages 5-7. Nothing edited.

**Summary:** 0 Blocker, 3 Major, 3 Nit. The brief still addresses the root problem: shared parts make the right way the easy way (lines 16-18, 80). Several Stage 6/7 fixes would enlarge the brief against the 20% principle (line 11); the findings below scope them and add two facts.

## Major

1. **Stale "OR-1 to OR-19" is also in `README.md:7` and `README.md:17`.** Brief line 15 is not the only copy, and the README is the entry point (`AGENTS.md:3`). Root fix: write "the numbered rules in owner-rules.md" everywhere instead of a range, and point the check list (lines 47-55) at the `RULES` table in `packages/ui/scripts/check-design.mjs` with two or three examples. Closes Stage 5 #1 and Stage 7 #1, #2, #5.
2. **The Stage 5 CI-comment fix means new infrastructure: the repo has no `.github/workflows/`.** Cheapest fix: add to the work loop (line 23) "run check:design and paste the summary into the PR, noting any warning kept on purpose". Covers Stage 5's last nit and Stage 7's "nobody owns the screenshots". A CI job stays "later".
3. **Stage 7's Risks section and Stage 6's glossary would overgrow the brief.** Keep Risks to 3-4 lines (warnings ignored, regex false positives, silent "not run"); mitigations: report prints "not run" at top, gaps are fixed in `packages/ui`. Replace inline glosses with one "Terms" line pointing to owner-rules.md, decisions.md and specq P1-P16; delete "Stage-1 specimens" (line 97) and "seven review lenses" (line 5). Skip a success metric for now.

## Nit

4. Stage 7 "Problem and Goals out of order" is cosmetic (lines 7-11, 62-75); a heading is enough.
5. Performance targets need labels, not tools (lines 58, 94): "200 KB = `packages/ui` bundle as reported by check:design"; "100 ms: not measured yet".
6. Delete the Decisions table (lines 87-98) and link `decisions.md`, per line 59 "recorded once".

None recommends a gate, a cap or a stack change.
