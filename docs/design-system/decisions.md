# Design decisions

Append only. Each entry: date, question, options, choice, reason.

## 2026-11-09 D1: Owner priorities for the interface

* **Given by the owner:** controls with ideal usability; smooth scrolling that never jumps while rows arrive; fast, remembered filtering and search; dense but calm screens; decisions answered inline; measured, estimated and unavailable numbers look different; light and dark; desktop first, phone usable; spec-driven work.
* **Recorded in:** [principles.md](principles.md).

## 2026-11-09 D2: Visual direction (open)

* **Options:** [A Quiet ledger](explorations/a-quiet-ledger.html), [B Console](explorations/b-console.html), [C Soft paper](explorations/c-soft-paper.html).
* **Choice:** resolved in D3.

## 2026-11-09 D3: Visual direction chosen

* **Question:** which visual direction becomes the base for tokens and controls (D2).
* **Options:** A Quiet ledger, B Console, C Soft paper.
* **Choice:** A Quiet ledger, with B's monospace figures for numbers and IDs.
* **Reason:** A is the densest calm baseline; monospace figures keep numbers and IDs aligned and easy to scan. No changes to principles.
* **Delivery note:** the owner views specimens inline in the Orca chat. Every specimen and the book page is one self-contained HTML file under 512 KB, with no `100vh` sizing.

## 2026-11-09 D4: Front-end stack (methodology decision 1)

* **Options:** (a) Preact + Signals + Wouter; (b) plain TypeScript templates with hand-written DOM patching; (c) server-rendered Fastify pages sharing only CSS and the registry.
* **Choice:** (a) Preact + Signals + Wouter, built with esbuild. Fastify serves JSON and the static bundle.
* **Reason:** keyed components and signals give in-place live updates without jumps; proven in TaskQ. (c) cannot meet the no-jump update requirement; (b) grows into hand-made list diffing.
* **Also decided:** virtual scroll with anchor keeping uses an existing library rather than hand-written code (candidate: TanStack Virtual via `preact/compat`), accepted only if it passes the scroll behaviour tests.

## 2026-11-09 D5: Form of owner rules (methodology decision 2)

* **Options:** (a) every P-n becomes OR-n with a check or blocking review item; (b) only automatable rules become OR-n; (c) the owner decides per rule in one table.
* **Choice:** (c). Table: [owner-rules-table.md](owner-rules-table.md). Conflicts with common practice are answered per row. Decisions 3–8 wait for the table.

## 2026-11-09 D6: Method, owner rules table and decisions 3–8

* **Principle (owner):** the method guides agents and never becomes a gate that stops work. Aim for 95% of the result with 20% of the effort; start simple and learn.
* **Owner rules:** [owner-rules.md](owner-rules.md), OR-1 to OR-19, read first in every design or UI session. They are instructions, not gates.
  * Accepted as instructions: P1, P2, P3, P5, P6, P8, P12, P13 (the rule part), P15, P16, plus OR-17 to OR-19.
  * P7: the arrow is dropped from the new-rows pill.
  * P11: infinite scroll, with a count and jump to start or end.
  * P14: right-click and short context menus are allowed. Forms and working tools never float.
  * P4, P10 and the gate part of P13: kept as the order of work, not as gates.
  * P9: any collection longer than a screen uses the virtual list component. No 100k-item test.
  * Precedence: the owner rule wins, except touch targets of 44 px on touch devices and the contrast minimums, which always hold.
* **Consistency:** shared parts in `packages/ui` make the right way the easy way: formatter, icon registry (TaskQ icons), tokens, virtual list.
* **D3 checks:** `npm run check:design` prints a warning report. It is not in `npm test` and never blocks a pull request.
* **D4 performance:** measured targets, not stops: initial JavaScript ≤ 200 KB, interaction response ≤ 100 ms.
* **D5 approval:** the owner says "ok" in chat, and the worker records it in `decisions.md`.
* **D6 visual regression:** not now. Book screenshots are attached to the pull request.
* **D7 old specimens:** archived in `explorations/archive/`.
* **D8 book role:** the book is the live demo page of `packages/ui`. There is no separate copy.
* **Rejected:** blocking checks in `npm test` or CI (the reviewers' recommendation), and gates on the order of work.

## 2026-11-09 D7: Stage 2 built as packages/ui (pending owner review)

* **Built:**
  * `packages/ui`: tokens, the icon registry (TaskQ Lucide set), the formatter, `VirtualList` on TanStack Virtual core, and 14 controls.
  * `npm run book` builds [book/index.html](book/index.html) as one self-contained file.
  * `npm run check:design` prints the warning report.
* **Applied:**
  * OR-7: the pill reads "12 new".
  * OR-14: filters expand inline.
  * OR-12: saved filters and rows can be pinned.
  * OR-2: compact age chips.
  * OR-18: the age chip turns red past a caller-supplied threshold.
  * OR-6: changed values tint for 2 s.
* **Open choices for the owner:**
  * B1: state in task lists: icon and word, or icon only with the word in the tooltip.
  * B2: row pin button: shown on hover and focus (always on touch), or always visible.

## 2026-11-10 D8: Stage 2 review: B1, B2, OR-20, OR-21

* **B1 (state in lists):** icon only. Writing the word next to an icon that already shows the state is redundant. The name and reason appear in a tooltip only where they help.
* **B2 (row pin):** always visible.
* **New owner rules:**
  * OR-20: tooltip discipline.
  * OR-21: visual hierarchy by importance.

  Both are in [owner-rules.md](owner-rules.md).
* **Applied:**
  * Task list: the title gets the most space; ID, state icon, agent, age, cost and pin sit in one zone on the right.
  * Tooltips: one `TooltipLayer` that is small, delayed and hidden while typing. Elements opt in with `data-tip`, and `title` tooltips were removed where they added nothing (age chips, measured numbers, clock times).
  * The scroll-anchor bug found by the new behaviour check is fixed.

## 2026-11-10 D9: Navigation pinning, split button, relative transcript times, views and counters, dashboards

* **Owner requests:**
  1. Pin any section in a left rail.
  2. A split action button.
  3. Relative transcript times, with the exact time on hover.
  4. Deep filter design: per-person view presets, grouping, quick state filters, counters with totals, and quick navigation with counters (reference: TaskQ state counters).
  5. Operational dashboards such as workers per host.
* **Applied:**
  * Controls: [nav-rail](controls/nav-rail.md), [split-button](controls/split-button.md), [state-counters](controls/state-counters.md), [stat-strip](controls/stat-strip.md). Views, grouping and counters are in the [filter bar](controls/filter-bar.md) and the [list view](patterns/list-view.md).
  * The transcript uses `age()`.
  * The fill bar supports percent for quota windows.
* **Stage 3:** patterns in [patterns/](patterns/), page specs in [pages/](pages/), and mockups in [pages/mockups.html](pages/mockups.html), built from `packages/ui` only.

## 2026-11-10 D10: Drift: mockups shown without the navigation rail (root cause and fix)

* **Finding (owner):** the final mockups showed no left rail or pins, although the book and `patterns/app-shell.md` define them.
* **What actually happened:** the mockups did use `AppShell` with `NavRail`. The owner's search found 0 references because the bundle is minified. But `styles.css` had a rule, added by the agent while building stage 3, that hid the rail below 760 px and offered nothing in its place. The inline chat viewer is narrower than 760 px, so the owner saw pages with no navigation at all.
* **Root causes:**
  1. *An undecided choice shipped silently.* Phone navigation was still an open question (d). Instead of asking, the agent picked "hide the rail" in CSS. That broke rule 2 of the [README process](README.md) (choices go to the owner).
  2. *No check tied specs to what renders.* The only check was "does the code exist", run by the agent at desktop width. Nothing checked what a page actually renders at the width the owner views it.
* **Fixes:**
  * The rail never disappears. Below 760 px it shrinks to icons, with pins first, counters on the icons and names in tooltips. This is interim until the owner picks a variant for (d).
  * Every control and pattern now marks its root with `data-ui="<spec name>"`.
  * The traceability check `tests/ui/trace.check.mjs` is part of the `npm run check:design` report. For each `pages/<page>.md`, it opens the routes listed on the spec's "Mockup routes" line and clicks every tab. It then reports any linked control or pattern that did not render, and requires `app-shell` and `nav-rail` on every page. It is a warning, not a gate (D6).
  * Page specs now link every control they name.
  * The layout patterns list-detail, needs-you and ops-summary are components (`ListDetail`, `NeedsYou`, `OpsSummary`, `ListFrame`), so pages compose them instead of copying markup.
* **Open questions (a)–(d):** rendered as variants in [explorations/open-questions-01.html](explorations/open-questions-01.html) for the owner to pick.

## 2026-11-10 D11: Owner picks on open questions (a)–(c), task row order, harness counts

* **(a) Priority:** A2, generalised. The operator chooses the grouping: priority, state, project, agent, host or none, alongside quick state filters and saved views ([list view](patterns/list-view.md)). Default grouping is priority. The `p0` marker appears in rows only when the list is not grouped by priority (OR-17).
* **New rule OR-22:**
  * The state icon is the leftmost column.
  * The ID sits right after the title.
  * The agent's harness mark shows while the task runs.
  * The delivery stage shows as a chip.
* **(b) Outcome and inputs:** B2. The [outcome block](controls/outcome-block.md) sits above the task tabs.
* **(c) Host detail:** C3. Stacked [detail sections](controls/detail-section.md), no tabs.
* **(d) Phone navigation:** still open. The interim icon rail (D1) stays.
* **Workers per host:** counts per harness, shown as harness mark plus number and omitted when zero ([harness counts](controls/harness-counts.md)). They appear in the stat strip, the Hosts table and the Overview.
* **Registry:** adds the harness marks (Codex, Claude, Antigravity) from TaskQ's icon set, and the delivery stages.

## 2026-11-10 D12: Final review fixes (icon colour, Σ counter, state first in every list)

* **Approved** except for three fixes.
* **1. Icon colour:** the harness marks (Codex, Claude) had no `fill` and rendered black in the dark theme.
  * Fix: every registry icon now takes the theme colour (`currentColor`).
  * `check:design` warns about any icon with a fixed or default colour.
* **2. Σ counter:** state counters start with a Σ button that shows the total and resets the state filter to all. The state buttons toggle on and off, and several can be on at once. Spec: [state-counters](controls/state-counters.md).
* **3. State first in every list:** OR-22 now covers every list, not only task rows.
  * Results drifted: its copy state was in a middle column. It now starts with the copy state, and the Laboratory variants start with their run state.
  * The traceability check warns when a list row's first cell is not a state badge.
  * Verified: moving the Results state column back makes the report show `state not first column in list "Results"`.

## 2026-11-10 D13: Owner approval of the book and mockups

* **Approved:** the design book (`book/index.html`), the page mockups (`pages/mockups.html`), and the specs behind them as of D12.
* **Still open:**
  * (d) phone navigation; the interim icon rail stays.
  * The control for editing priority from the Overview.

## 2026-11-10 D14: Review fixes and automatic checks

Source: the [independent review](reviews/2026-11-10/SUMMARY.md) found 0 blockers, 11 majors and 21 nits. `check:design` caught none of them.

**Majors fixed**

| ID | Problem | Fix |
|---|---|---|
| M1 | At 1100 px and below, the opened task's detail was off-screen. | The detail now comes first and takes focus ([list-detail](patterns/list-detail.md)). |
| M2 | Touch targets were under 44 px. | 44 px with a coarse pointer for row pins, row links, nav links, chips, tabs, stat links and inputs; 44 px list rows on touch ([principles §10](principles.md#10-accessibility)). |
| M3 | The overrun age chip used colour only. | It now has a warning icon, and its name and tooltip say "running too long". |
| M4 | Hosts repeated words ("windows windows"). | The platform is shown only when the host name does not already say it; the detail heading is the name only. |
| M5 | Overview said "need you" several times. | The chip is hidden on the Overview, the "need you" stat tile is gone, and "for you" is dropped when the addressee is you. |
| M6 | The Accounts warning was truncated. | The fill-bar legend wraps and puts the warning first; it is marked `data-important`. |
| M7 | The brief and README cited stale rule ranges and a wrong check list. | Both now link [owner-rules.md](owner-rules.md) and `check-design.mjs` and copy neither. |
| M8 | The report never reached the pull request. | CI ([ci.yml](../../.github/workflows/ci.yml)) posts the summary and uploads the report and screenshots. The work loop has a manual fallback. |
| M9 | The 200 KB and 100 ms targets were not measured. | Both are now measured and labelled: bundle and page JS, raw and gzip; task row to detail heading latency in headless Chromium. |
| M10 | The brief used undefined terms. | A Terms paragraph was added and dead references removed. |
| M11 | The brief had no risks. | A Risks section was added; Status links D7–D14. |

**Nits fixed**
* N1: the "N of M" count shows only while filtered.
* N2: the Laboratory recommendation is worded once.
* N3: scores show two fixed decimals.
* N4: the Results Type column is removed, because the type is in the file name.
* N5: no double gap in "18 GB".
* N6: the narrow-rail badge no longer overlaps the icon.
* N8: the Results viewer is marked as a stub.
* N9: the "Group" label is aligned.
* N12: a skip link was added.
* N13: the "N new" pill is in a polite live region.
* N16–N21: the brief was rewritten.

**Nits left**
* N7: the list card height at 1440×900 (a fixed token, no `100vh`).
* N10: the book's side-by-side theme frames at about 640 px.
* N11: the tooltip cannot be hovered and does not show on touch (WCAG 1.4.13); this needs a tap-to-show design choice.
* N14: group headers are not headings or labelled row groups.
* N15: non-text contrast of fill bars is not machine-checked.

**New checks**

`tests/ui/ux.check.mjs` is part of `check:design` and only warns. It covers:
* repeated words in one region (OR-17);
* touch targets under 44 px with an emulated coarse pointer;
* status shown by colour only;
* truncated `data-important` text;
* `[data-ui=detail]` outside the viewport at 600 and 1100 px;
* page JS size and the latency from click to detail.

Each check was proven once with `UX_INJECT=<defect>`, which produced its warning; the defect was then removed. The first real run also found further problems, which were fixed:
* nav and chip targets under 44 px;
* repeated "glb" in Results rows;
* repeated "trials" and "log";
* the Laboratory failed-trials chip, which used colour only;
* the compact badge's hidden reason text.

## 2026-11-10 D15: Operator app first slice

* **Design:** [fleet-operator-app.md](../designs/fleet-operator-app.md), owner-approved, decisions D1–D5 there.
* **Built:** the operator app in `app/`: Overview, Tasks (list and detail), Conversations, Hosts.
* **New controls:**
  * [Composer](controls/composer.md).
  * [Offline marker](controls/offline-marker.md).
  * The `session` state family ([state badge](controls/state-badge.md)).
  * `VirtualList` gains `onEnd` for infinite scroll (OR-11).
* **Conversations** is an app-only page spec ([pages/conversations.md](pages/conversations.md)). It is checked by the app check, not by a mockup.
* **Differences from the design doc,** kept deliberately simpler:
  * The paged task list is a new read op, `task_page`. `task_list` keeps its existing shape for agents.
  * Resync is detected by looking up the cursor's own change row: a missing row means it was pruned or is unknown. This replaces a separate floor table.
  * Change rows use the client kinds (`task`, `execution`, `question`, `session`, `event`, `host`), not legacy kinds.
  * The list order is group key, then newest first, then id. There is no separate sort option.
  * Priority grouping shows higher numbers first. Which way priority should run is an open question for the owner.
