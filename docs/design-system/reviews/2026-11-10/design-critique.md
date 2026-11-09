# Design critique: book and page mockups

Date: 2026-11-10. Method: `design:design-critique`, judged against `owner-rules.md` (OR-1..22) first, then common practice.
Rendered with Playwright Chromium from `docs/design-system/book/index.html` and `docs/design-system/pages/mockups.html` (built files on main at 959fdba), light and dark, 1440 px and 600 px wide, 900 px tall. Screenshots: [`shots/`](shots/) (`<page>-<theme>-<width>.png`; `mock-tasks-9989-light-600-touch.png` uses a touch device profile).
`npm run check:design` at the same commit reports **no warnings**; every item below is outside what the check sees.

## Overall impression

The system is coherent: one shell, one row grammar (state icon, title + ID, right-side service zone), consistent chips, and the two themes are equivalent. The pages read as a dense professional tool, as OR-5 intends. The main gaps are (1) the narrow layout, which hides the detail pane and keeps desktop-size targets on touch, and (2) many small OR-17 repetitions that the check cannot see.

## Owner-rule breaches

| # | Sev | Rule | Where | What |
|---|---|---|---|---|
| C1 | major | OR-19, OR-14 spirit | `#/tasks/9989`, `#/tasks/9978` at ≤1100 px (`.split-view.has-detail` collapses to one column) | The selected task's detail renders below the whole list (top at 948-964 px in a 900 px viewport). Opening a task shows no visible change; the detail is hidden off-screen. Shots: `mock-tasks-9978-light-600.png`, `mock-tasks-9989-light-600-touch.png`. |
| C2 | major | Owner exception: 44 px touch targets | mockups, `pointer:coarse` | The coarse-pointer rule covers only `.btn, .chip button, .tw, .pill`. Row pin buttons stay 24×24, nav rail links 24×28, row title links 18 px tall in 32 px rows, `.nav-pin` only gets `opacity:1`. |
| C3 | major | OR-17 no repeated words | `#/hosts` table and detail | "windows windows", "mac macos", "pds18 linux": host name plus OS word; detail heading repeats "windows windows". Use the OS icon (OR-1) instead of the word. Shot: `mock-hosts-dark-1440.png`. |
| C4 | major | OR-17 | `#/overview` | "3 need you" appears three times in one view: header chip, stat strip, nav badge "Overview 3"; the section is titled "Needs you" and every card says "for you". |
| C5 | major | OR-19 never hide what matters | `#/overview` Accounts | "window 97% · 3% left, warnin…" is truncated at 1440 px exactly where the warning is; "claude-max" wraps to two lines while the bar takes fixed width. Shot: `mock-overview-light-1440.png`. |
| C6 | nit | OR-17 | `#/tasks` filter bar | Σ 3,000 counter and "3,000 of 3,000" show the same number side by side. |
| C7 | nit | OR-17 | `#/laboratory` | Section "Recommendation", question "Recommend B…?", button "Recommend". |
| C8 | nit | OR-6 fixed-width figures | `#/laboratory` Variants | Judge column shows "0.8" next to "0.68"; decimals are not aligned. |
| C9 | nit | OR-1 icon first | `#/results` | Type column is words (diff, image, report, video, glb) where registry file-type icons exist; Project column words also repeat the row's project scope. |
| C10 | nit | layout | `#/overview` low-space banner, `#/hosts` | "18  GB free, warning at 25  GB": double gap between number and unit (number chip padding). |
| C11 | nit | OR-3 / layout | nav rail at 600 px | Count badges ("2", "1", "3") overlap the section icons; the icon is partly covered. Shot: `mock-overview-dark-600.png`. |

## Usability and hierarchy (common practice)

| # | Sev | Finding | Recommendation |
|---|---|---|---|
| C12 | nit | `#/tasks` at 1440×900: list card stops ~100 px above the viewport bottom; same empty band on `#/results`, `#/laboratory`. | Let the list card fill to the viewport bottom (`dvh`-based shell, not `100vh`). |
| C13 | nit | `#/results` detail pane is a 100 px placeholder with large empty area. | Fine for a mockup; note in `pages/results.md` that the viewer shell is a stub. |
| C14 | nit | Filter bar "Group" label sits above the baseline of the group tabs (1440 px) and wraps oddly at 600 px. | Align label baseline with tabs. |
| C15 | nit | Book (1440): the light/dark side-by-side halves force each specimen to ~640 px, so wide controls (task list) are shown narrower than in real use. | Optional full-width specimen per control. |

## What works

- OR-22 state-first rows, ID after title, right-side service zone are consistent in every list (tasks, results, hosts, variants, executions).
- OR-14: decision blocks, answer fields and the task detail are inline / in a side pane; nothing interactive floats.
- OR-7: no direction arrows next to values. OR-8: all icons from the registry.
- Both themes keep the same hierarchy; attention, danger and estimate colours stay distinct in dark.
