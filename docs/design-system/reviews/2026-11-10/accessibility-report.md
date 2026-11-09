# Accessibility review (WCAG 2.1 AA)

Date: 2026-11-10. Method: `design:accessibility-review`. Automated probes in Playwright Chromium (text contrast against the nearest opaque background, accessible names of interactive elements, target sizes, headings and landmarks, Tab order, horizontal scroll at 600 px) on the book and all seven mockup routes, light and dark, 1440 and 600 px; plus code reading of `packages/ui/src`. Screenshots: [`shots/`](shots/). The probe is not a full axe run; items marked "manual" need a screen-reader pass.

## Summary

| Check | Result |
|---|---|
| Text contrast 1.4.3 | pass: no text under 4.5:1 (3:1 large) found in either theme on any route |
| Names 4.1.2 | pass: 0 interactive elements without a name across all routes |
| Language 3.1.1, title 2.4.2 | pass (`lang="en"`, titles set) |
| Reflow 1.4.10 | pass: no horizontal scroll at 600 px |
| Focus visible 2.4.7 | pass: `:focus-visible` ring on links/buttons, `.search:focus-within` ring on inputs |
| Live region 4.1.3 | partial: only the list count is `aria-live` |
| Owner touch-target exception (44 px) | fail, see A2 |

## Findings

| # | Sev | WCAG | Where | Issue | Fix |
|---|---|---|---|---|---|
| A1 | major | 1.4.1 Use of colour, 1.3.1 | `#/tasks` overrun age chip (`span.chip-age.t-danger.alert`, e.g. "13h") | "Running too long" is shown by red only; its accessible name is "13 hours ago", so screen readers and colour-blind users lose the alert (OR-18 chip). | Add the meaning to the name ("13 hours, running too long") and a non-colour cue (weight/outline already on `.alert` should be checked for ≥3:1 non-text contrast). |
| A2 | major | owner exception (44 px on touch); 2.5.5 is AAA, noted for context | mockups with `pointer:coarse` | Row pin buttons 24×24, nav rail links 24×28, row links 18 px tall. Only `.btn,.chip button,.tw,.pill` are enlarged. | Extend the coarse-pointer rule to `.nav a`, row pin, row link hit area (row height 44 on coarse). |
| A3 | major | 1.3.2 Meaningful sequence / 2.4.3 | `#/tasks/:id` at ≤1100 px | Detail pane is placed after the full list in DOM and layout; selecting a row gives no visible or announced change. | Move focus to the detail heading on open, or render detail as its own route at narrow width. |
| A4 | nit | 1.4.13 Content on hover or focus | `TooltipLayer` (`packages/ui/src/controls.tsx:40-60`) | Tooltip hides on `pointerout` of the trigger, so it cannot be hovered; on touch it never shows. Where the tip carries unique data (exact time, OR-2) it is reachable only by mouse hover or focus. | Keep the tip open while the pointer is over it; make sure the same data is in the accessible name (most already are). |
| A5 | nit | 2.4.1 Bypass blocks | all mockup routes | No skip link; Tab goes through every nav item and its pin button (12+ stops) before the page. Landmarks exist (`header`, `nav`, `main`), which technically satisfies 2.4.1. | Add a "Skip to content" link; drop pin buttons from the Tab order until row focus (roving tabindex). |
| A6 | nit | 4.1.3 Status messages | live updates (OR-6) | Value tints and new-row pill are not announced; only the "N of M" count is `aria-live`. | Make the "12 new" pill a polite live region. |
| A7 | nit | 1.3.1 | `#/tasks` | Single `h1` and no section headings; group headers ("Priority 0") are not headings or row-group labels. | Give group headers `role="rowgroup"` + label, or `h2`. |
| A8 | nit | 1.4.11 Non-text contrast (manual) | fill bars, `--line` borders, estimate dotted underline | Not machine-checked; dark `--line #2b3137` on `--surface #171b1f` is ~1.4:1 — fine for decoration, but the fill-bar track and the dashed "stale" state ring on `#/hosts` mac must reach 3:1 where they carry meaning. | Verify with tokens. |
