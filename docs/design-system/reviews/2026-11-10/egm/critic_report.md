# Goldfish Review Report (Stage 7, Critic Review)

**Target:** `docs/design-system/methodology-brief.md` (`1-98`, whole file)

## Reviewer Status

| Reviewer | Status |
| --- | --- |
| `egm-design` | completed |
| `egm-structural` | completed |
| `egm-style` | completed |

Severity breakdown: 0 Blocker, 6 Major, 14 Nit.

None of the recommendations below add a gate, a cap or a stack change.

## Prioritized Findings (Blockers -> Major -> Nits)

### Owner-rule range is out of date - Severity: Major
* **Reviewer Lens:** `egm-design`, `egm-structural`, `egm-style`
* **Affected Scope:** `Line 15`
* **Discrepancy Description**: The brief says "OR-1 to OR-19". `owner-rules.md` now goes up to OR-22 (OR-20 and OR-21 from D8, OR-22 from D11/D12). A fresh session would assume the newer rules are not part of the method.
* **Rationale & Recommendation**: Drop the fixed range and write "every numbered OR rule in owner-rules.md". If you keep a range, update it.

### Check list does not match `check-design.mjs` - Severity: Major
* **Reviewer Lens:** `egm-design`
* **Affected Scope:** `Lines 47-58, 94`
* **Discrepancy Description**:
  * The brief does not list these script checks:
    * OR-9 `<select>`
    * OR-15 "Back to" links
    * icon `currentColor` (D12)
    * formatter tests
    * scroll anchor
    * page-to-mockup trace (D10)
  * It also claims things the script does not do:
    * "Pixel values outside tokens" is only checked in inline `style={{}}` in `.tsx` files, not in CSS.
    * "Initial JS ≤ 200 KB" is really the size of the `packages/ui` bundle.
* **Rationale & Recommendation**: Either make the list match the script or replace it with a pointer to the script's `RULES` table. Keep the checks as warnings only.

### Performance targets do not say how they are measured - Severity: Major
* **Reviewer Lens:** `egm-design`, `egm-style`
* **Affected Scope:** `Lines 58, 94`
* **Discrepancy Description**: The brief calls them "measured targets", but:
  * nothing measures the 100 ms interaction target;
  * the 200 KB target does not say gzipped or raw;
  * there is no metric (for example INP) and no device.
* **Rationale & Recommendation**: For each target, name where it is measured, or label it as unmeasured. Do not add new limits.

### No Risks & Mitigations section - Severity: Major
* **Reviewer Lens:** `egm-design`, `egm-structural`
* **Affected Scope:** Whole file (after "Alternatives weighed")
* **Discrepancy Description**: The standard requires this section. These risks are already present:
  * Warnings get ignored. This is the same way the AGENTS.md-only approach failed (line 80).
  * The regex checks give noisy results:
    * the OR-14 check flags information-only dialogs;
    * the arrow check misses arrows drawn as icons.
  * The trace check quietly prints "not run" when the book has not been built.
  * Gaps in `packages/ui` push pages into writing local variants.
* **Rationale & Recommendation**: Add a short section with soft mitigations that leave everything warn-only:
  * show the warning count per PR next to the previous count;
  * keep a list of known false positives;
  * put "not run" at the top of the report;
  * say how gaps in `packages/ui` get reported.

### Status and Mechanisms don't include later decisions (D7-D13, D10 trace) - Severity: Major
* **Reviewer Lens:** `egm-design`, `egm-structural`, `egm-style`
* **Affected Scope:** `Lines 5, 23, 36-60`
* **Discrepancy Description**:
  * The Status line cites only D4-D6.
  * D10 added the page-spec "Mockup routes" link and the trace check to fix drift, but the Mechanisms section doesn't mention them.
  * D13 owner approval is not referenced.
* **Rationale & Recommendation**: Add a Mechanisms bullet for the trace check. Add "amended by D10, D12; last checked against D13" to the Status line.

### Problem and Goals are out of order - Severity: Major
* **Reviewer Lens:** `egm-structural`
* **Affected Scope:** `Lines 7-11, 62`
* **Discrepancy Description**: The problem statement is folded into Overview, and Goals come after Mechanisms, so the reader sees the solution before the scope.
* **Rationale & Recommendation**: Split out a "## Problem" section from lines 9-11. Move "Goals and non-goals" right after it.

### Rejection credited to the owner has no link - Severity: Nit
* **Reviewer Lens:** `egm-design`, `egm-structural`
* **Affected Scope:** `Line 79`
* **Rationale & Recommendation**: Link it to [D6](decisions.md).

### Reason for rejecting the LLM reviewer is weak - Severity: Nit
* **Reviewer Lens:** `egm-design`
* **Affected Scope:** `Line 81`
* **Discrepancy Description**: "Not repeatable" is a reason against an LLM reviewer that acts as a gate. It is not a reason against one that only advises.
* **Rationale & Recommendation**: Give the reason the advisory version was rejected (cost, noise), or mark it as deferred.

### Decisions table repeats the body - Severity: Nit
* **Reviewer Lens:** `egm-design`, `egm-structural`
* **Affected Scope:** `Lines 87-98` vs `36-60`
* **Discrepancy Description**: The table repeats the stack, the performance targets and the role of the book. The repetition is how the stale facts above got into two places, and it goes against the brief's own "record each decision once" rule (line 59).
* **Rationale & Recommendation**: Keep only the choices and their D-links in the table.

### No signal that the method is working - Severity: Nit
* **Reviewer Lens:** `egm-design`
* **Affected Scope:** `Lines 9, 62-69`
* **Rationale & Recommendation**: Add one light signal that does not block work, for example "settled questions reopened" or "owner corrections per round".

### Nobody owns the screenshots - Severity: Nit
* **Reviewer Lens:** `egm-design`, `egm-style`
* **Affected Scope:** `Lines 21, 82, 93, 96`
* **Discrepancy Description**: The brief does not say who attaches the screenshots and the warning report to the PR, or which views the screenshots should show.
* **Rationale & Recommendation**: Name the step that does it and the views (for example, the book in both themes).

### Links to decisions.md have no anchors; brainstorm record is not linked - Severity: Nit
* **Reviewer Lens:** `egm-structural`, `egm-style`
* **Affected Scope:** `Lines 5, 40, 91, 92`
* **Rationale & Recommendation**:
  * Link each decision to its heading anchor in decisions.md.
  * Link the "seven review lenses" brainstorm record, or remove the mention.

### List formatting is uneven - Severity: Nit
* **Reviewer Lens:** `egm-structural`
* **Affected Scope:** `Lines 47-57`
* **Rationale & Recommendation**: Make list punctuation consistent. Move the stray paragraph on line 57 out of the list.

### Wording fixes - Severity: Nit
* **Reviewer Lens:** `egm-style`
* **Affected Scope:** Lines 9, 17, 18, 21, 23/34, 27, 72/82/96, 80, 85
* **Discrepancy Description and Recommendation**:
  * Line 9: write "touch targets", not "targets".
  * Line 17: write `VirtualList`, not "a ready virtual list".
  * Line 18: the sentence repeats line 16; keep one of the two.
  * Line 21: the book is used here before it is defined; add a forward reference.
  * Lines 23 and 34: "at every step" conflicts with "until the owner says ok"; state the loop once.
  * Line 27: the sentence repeats lines 11 and 15; remove it.
  * Lines 72, 82, 96: visual regression is described three different ways; use one wording.
  * Line 80: write "shared parts in `packages/ui`".
  * Line 85: define P1-P16 (specq owner preferences).
