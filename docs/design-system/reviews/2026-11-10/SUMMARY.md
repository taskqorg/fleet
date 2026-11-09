# Design system review, 2026-11-10: consolidated summary

Independent review of the approved Fleet design system on main (959fdba). Review only: nothing in the design system, code or specs was changed.

Reports in this folder:
- EGM review of `methodology-brief.md` (uninterrupted mode, stages in order): [Stage 5 zoom out](egm/zoom_out_report.md), [Stage 6 comprehension](egm/comprehension_report.md), [Stage 7 critic](egm/critic_report.md), [Stage 8 intermediate zoom out](egm/intermediate_zoom_out_report.md), [Stage 9 not run](egm/implementation_readiness_report.md) (the brief has no implementation-plan section, so DetailedRange = N/A).
- [Design critique](design-critique.md) of the book and the seven mockup routes, light/dark, 1440/600 px.
- [Accessibility report](accessibility-report.md), WCAG 2.1 AA.
- [Screenshots](shots/).

`npm run check:design` reports **no warnings** at this commit. None of the findings below is caught by it. All recommendations respect the owner constraints: no gates, no caps, owner rules first (except touch targets and contrast), stack unchanged.

**Counts: 0 blocker, 11 major, 21 nit.**

## Major

| # | Area | File / element | Finding | Source |
|---|---|---|---|---|
| M1 | Pages | `pages/mockups.html` `#/tasks/9989`, `#/tasks/9978` at ≤1100 px (`.split-view.has-detail` → one column) | Selected task's detail renders below the whole list (top ≈ 950 px in a 900 px viewport); opening a task shows no change. Breaks OR-19; no focus move (WCAG 1.3.2/2.4.3). | C1, A3 |
| M2 | Pages | mockups under `pointer:coarse` | Owner exception (44 px on touch) not met: row pin 24×24, nav rail links 24×28, row links 18 px in 32 px rows. Coarse rule covers only `.btn,.chip button,.tw,.pill`. | C2, A2 |
| M3 | Pages | `#/tasks` overrun chip `span.chip-age.t-danger.alert` | "Running too long" conveyed by red only; accessible name "13 hours ago" drops it (WCAG 1.4.1, OR-18). | A1 |
| M4 | Pages | `#/hosts` table rows and detail heading | OR-17: "windows windows", "mac macos", "pds18 linux". Use OS icon. | C3 |
| M5 | Pages | `#/overview` | OR-17: "3 need you" three times (header chip, stat strip, nav badge) plus "Needs you" heading and "for you" on every card. | C4 |
| M6 | Pages | `#/overview` Accounts row | OR-19: "window 97% · 3% left, warnin…" truncated exactly at the warning; "claude-max" wraps. | C5 |
| M7 | Method | `methodology-brief.md:15`, `README.md:7,17`, brief `:47-55` | Stale "OR-1 to OR-19" (rules go to OR-22) in brief and README entry point; check list misses six real checks (OR-9 select, OR-15, icon `currentColor`, formatter tests, scroll anchor, page trace) and over-claims pixel checks. Replace ranges/lists with links to `owner-rules.md` and the `RULES` table in `check-design.mjs`. | EGM 5, 7, 8 |
| M8 | Method | brief `:19-23, 93` | Nothing puts the warning report or screenshots into the PR; there is no CI in the repo. Cheapest fix: one step in the work loop, "paste the check:design summary into the PR". | EGM 5, 8 |
| M9 | Method | brief `:58, 94` | Performance targets unlabeled: 200 KB is the `packages/ui` bundle (not first-load JS, raw vs gzip unstated); 100 ms is measured nowhere. Label honestly. | EGM 5, 7, 8 |
| M10 | Method | brief `:5, 9, 17, 21, 74, 85, 97` | Undefined terms for a fresh reader: specq / P1-P16, "owner" and "worker", "book screenshots", "Stage-1 specimens", "seven review lenses", TaskQ. One "Terms" line, delete the dead references. | EGM 6, 8 |
| M11 | Method | brief (whole) | No Risks section; keep it 3-4 lines (warnings ignored, regex false positives, silent "not run"). Also Status/Mechanisms omit D7-D13. | EGM 7, 8 |

## Nit

| # | File / element | Finding |
|---|---|---|
| N1 | `#/tasks` filter bar | OR-17: Σ 3,000 counter next to "3,000 of 3,000". |
| N2 | `#/laboratory` | OR-17: "Recommendation" / "Recommend B…?" / "Recommend". |
| N3 | `#/laboratory` Judge column | OR-6: "0.8" vs "0.68", decimals not fixed. |
| N4 | `#/results` Type column | OR-1: file types as words, not registry icons. |
| N5 | `#/overview`, `#/hosts` | Double gap "18  GB". |
| N6 | nav rail at 600 px | Count badges overlap section icons. |
| N7 | `#/tasks`, `#/results`, `#/laboratory` at 1440×900 | List card stops ~100 px above viewport bottom. |
| N8 | `#/results` detail | Viewer is a 100 px stub; mark as stub in `pages/results.md`. |
| N9 | `#/tasks` filter bar | "Group" label off the tab baseline. |
| N10 | book | Side-by-side themes show wide controls at ~640 px. |
| N11 | `packages/ui/src/controls.tsx:40-60` `TooltipLayer` | Tooltip not hoverable (hides on trigger `pointerout`); none on touch (WCAG 1.4.13). |
| N12 | all routes | No skip link; nav pins add 12+ Tab stops (2.4.1 met by landmarks). |
| N13 | live updates | Only "N of M" is `aria-live`; "12 new" pill not announced (4.1.3). |
| N14 | `#/tasks` | Group headers are not headings or labelled row groups (1.3.1). |
| N15 | fill bars, stale ring | Non-text contrast (1.4.11) not machine-checked; verify meaningful parts reach 3:1. |
| N16 | brief `:87-98` | Decisions table repeats body and `decisions.md`; delete and link. |
| N17 | brief `:7-11, 62` | Problem/Goals order; a heading is enough. |
| N18 | brief `:81` | LLM-reviewer rejection rules out only a blocking one, not an advisory one. |
| N19 | brief links | `decisions.md` links have no anchors. |
| N20 | brief `:39-46` | Module detail belongs in specs; line 46 names the generated `book/index.html` instead of `packages/ui/book` source. |
| N21 | brief | Uneven list formatting and small wording fixes (see critic report). |

## Traceability

Design critique C1-C15 and accessibility A1-A8 map into M1-M6 and N1-N15 as noted. EGM findings: Stage 5 (3 major, 4 nit), Stage 6 (5 major, 8 nit), Stage 7 (6 major, 14 nit), Stage 8 (3 major, 3 nit) deduplicate into M7-M11 and N16-N21; full text in each stage report.
