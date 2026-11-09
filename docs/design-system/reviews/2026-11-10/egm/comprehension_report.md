# Stage 6: Comprehension Test

Target: `docs/design-system/methodology-brief.md`, FeatureRange 1-98, DetailedRange N/A. Reviewer: `egm-comprehension`, document only, no standard.

**What this proposes**

This is a lightweight method that keeps Fleet's agent-built interface consistent from one session to the next, without blocking anyone's work. It has three parts:
- **Owner rules:** a one-page list, OR-1 to OR-19, that every session reads first.
- **Shared parts in `packages/ui`:** formatters, an icon registry, tokens, a virtual list and controls. They are meant to be easier to use than writing a local version.
- **A warn-only check:** `npm run check:design` prints a report, and screenshots are attached to pull requests.

Work goes through a loop: update the spec, the owner says "ok" in chat, the worker logs it in `decisions.md`, and a fresh session builds from the spec. There is a suggested order of work and a list of rejected alternatives.

**How the existing system works, per this document**

Agents build Fleet's interface across many separate sessions. In earlier rounds, new sessions ignored past decisions, the owner's preferences and the shared guidance in `specq/design`, even though their instructions said to follow the spec. Work was not grounded in real data, and basics such as spacing and scrolling were decided again each time. A prototype UI already exists in `packages/coordinator/*ui.ts`. New PWA routes will replace it page by page.

## Findings

1. **Major.** Lines 9, 74, 85: "specq", `specq/design` and "P1–P16" are never explained. A new reader cannot tell what is deliberately not copied.
2. **Major.** Lines 15, 30, 32, 53-54, 83: OR rules cited by number only (OR-4, OR-5, OR-7, OR-10, OR-14). OR-5 and OR-10 cannot be guessed. A one-line gloss per cited rule would make the brief self-contained.
3. **Major.** Lines 3, 5, 9, 11, 23, 79: "the owner" and "the worker" are not defined; the approval loop depends on them.
4. **Major.** Lines 21, 46, 96, 98: "book screenshots" used before the book is defined; nobody is named to take them, of what, or how.
5. **Major.** Line 97: "Stage-1 specimens" appear only in the decisions table, unexplained.
6. **Nit.** Line 5: "seven review lenses" has no explanation or link.
7. **Nit.** Line 17: "TaskQ Lucide set" — TaskQ not explained.
8. **Nit.** Line 29: "system sections" not defined.
9. **Nit.** Line 44: "anchor keeping" not defined (likely scroll-anchor preservation).
10. **Nit.** Lines 43-45: "money with basis" unclear.
11. **Nit.** Line 58: performance targets do not say how or where they are measured.
12. **Nit.** Line 60: "PWA routes" assumes the reader knows Fleet is becoming a PWA.
13. **Nit.** Lines 5, 40, 92: D4-D6 cited by number only; the decisions table summarises them, which is enough.

No Blockers: every section could be paraphrased and the parts connect.
