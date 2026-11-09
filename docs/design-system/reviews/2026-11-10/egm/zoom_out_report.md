# Goldfish Review Report: Initial Zoom Out (Stage 5)

**Target:** `docs/design-system/methodology-brief.md` (`1-98`)
**Standard:** `resources/document_standards/zoom-out-standard.md`

## Reviewer Status

| Reviewer | Status |
| --- | --- |
| `egm-zoom-out` | completed |
| `egm-inventive-catalyst` | completed |

Summary: 0 Blocker, 3 Major, 4 Nit. Both reviewers found the overall design sound. It goes after the root cause (line 80: shared parts make the right way the easy way), it reuses existing tools, and its non-goals are well chosen. No finding asks for a gate or a cap.

## Prioritized Findings (Blockers -> Major -> Nits)

### Brief is out of date with owner-rules.md and decisions.md - Severity: Major
* **Reviewer Lens:** `egm-zoom-out`, `egm-inventive-catalyst`
* **Affected Scope:** Lines 5, 15, 47-57
* **Discrepancy Description**:
  * Line 15 says "OR-1 to OR-19", but `owner-rules.md` now runs to OR-22 (OR-20 tooltips, OR-21 hierarchy, OR-22 state first).
  * Line 5 cites only D4-D6 as approval; the approval of the design system itself is recorded later, as D13.
  * The `check:design` list leaves out three warnings added later:
    * the trace check `tests/ui/trace.check.mjs` (D10, `decisions.md:110`);
    * the fixed/default icon colour warning (D12, `decisions.md:134`);
    * the state-first row check (D12, `controls/data-list.md:12`).
* **Rationale & Recommendation**: The brief exists so that fresh sessions stop re-deciding settled questions, and an out-of-date brief causes exactly that. Stop listing items that change: refer to "the OR-n rules in owner-rules.md", and say the current warning catalogue lives in `check-design.mjs` / `decisions.md`, with at most a few examples.

### The warning report only helps if someone runs it - Severity: Major
* **Reviewer Lens:** `egm-inventive-catalyst`
* **Affected Scope:** Lines 19-21, 79, 93
* **Discrepancy Description**: Line 93 says there is a "Warning report in the pull request", but nothing puts it there. `check:design` is a manual script (`package.json:10`) and is not part of `npm test`, so the report depends on a worker remembering to run it. The brief's own motivation, and the D10 nav-rail drift, show that this kind of step gets skipped.
* **Rationale & Recommendation**: Being non-blocking and being reliably seen do not conflict. Add one always-passing CI job that runs `check:design` and posts or updates a single PR comment. This fits D3/D6 and the 20% budget. Downsides: one job to maintain, and the trace check needs a headless browser in CI.

### Performance targets have no stated measuring point - Severity: Major
* **Reviewer Lens:** `egm-inventive-catalyst`
* **Affected Scope:** Lines 55, 58, 73, 94
* **Discrepancy Description**: Line 73 rules out timing gates, and bundle size appears in the report (line 55). But no mechanism measures the interaction ≤ 100 ms target, so in practice that target goes unchecked.
* **Rationale & Recommendation**: Report both numbers in the same non-blocking report. Bundle size comes from the esbuild metafile. Interaction time comes from one scripted interaction in the existing `tests/ui/trace.check.mjs`. Treat CI timing as a trend only, never pass or fail.

### Decisions table repeats the body and decisions.md - Severity: Nit
* **Reviewer Lens:** `egm-zoom-out`, `egm-inventive-catalyst`
* **Affected Scope:** Lines 87-98 (compare 19-21/47-57 vs 93, 58 vs 94, 46 vs 98, 40 vs 91, and line 59)
* **Discrepancy Description**: Line 59 says each decision is recorded once in `decisions.md`, yet the table restates D3-D6 and repeats statements from the Overview and Mechanisms sections. This repetition is what causes the drift in the first finding.
* **Rationale & Recommendation**: Shrink the table to D-number links, or cut the repeated Mechanisms bullets.

### Implementation detail in a method brief - Severity: Nit
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** Lines 39-45, 58
* **Discrepancy Description**: The module-by-module inventory of `packages/ui` (format "money with basis", VirtualList "anchor keeping", tokens.json producing CSS variables) belongs in `tokens.md` / `controls/*.md`.
* **Rationale & Recommendation**: Use one line plus links to the specs. Keep the stack line, since D4 is a fixed decision.

### Book path points at a generated file - Severity: Nit
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** Line 46
* **Discrepancy Description**: The brief names `book/index.html`, but the source lives at `packages/ui/book/*.tsx` and is built by `packages/ui/scripts/build-book.mjs`.
* **Rationale & Recommendation**: Give the source path and the build command, so agents edit the source and not the generated output.

### Loop does not say which spec or what to do with warnings - Severity: Nit
* **Reviewer Lens:** `egm-zoom-out`, `egm-inventive-catalyst`
* **Affected Scope:** Lines 23, 57
* **Discrepancy Description**: "Update the spec" (line 23) does not name the file (`inventory.md`, `controls/*.md` or `pages/*.md`). Line 57 does not say what happens after a warning appears.
* **Rationale & Recommendation**: Name the spec locations. Add one line saying the worker either fixes each warning or notes in the PR why it stays. The warning still does not block anything.
