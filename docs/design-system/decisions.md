# Design decisions

Append only. Each entry: date, question, options, choice, reason.

## 2026-11-09 D1: Owner priorities for the interface

* **Given by the owner:** controls with ideal usability; smooth scrolling that never jumps while rows arrive; fast, remembered filtering and search; dense but calm screens; decisions answered inline; measured, estimated and unavailable numbers look different; light and dark; desktop first, phone usable; spec-driven work.
* **Recorded in:** [principles.md](principles.md).

## 2026-11-09 D2: Visual direction (open)

* **Options:** A Quiet ledger, B Console, C Soft paper (archived in git history).
* **Choice:** resolved in D3.

## 2026-11-09 D3: Visual direction chosen

* **Question:** which visual direction becomes the base for tokens and controls (D2).
* **Options:** A Quiet ledger, B Console, C Soft paper.
* **Choice:** A Quiet ledger, with B's monospace figures for numbers and IDs.
* **Reason:** A is the densest calm baseline; monospace figures keep numbers and IDs aligned and easy to scan. No changes to principles.
* **Delivery note:** the owner views specimens inline in the Orca chat. Every specimen and the book page is one self-contained HTML file under 512 KB, with no `100vh` sizing.

## 2026-11-09 D4: Front-end stack (methodology decision 1)

* **Options:** (a) Preact + Signals + Wouter; (b) plain TypeScript templates with hand-written DOM patching; (c) server-rendered pages sharing only CSS and the registry.
* **Choice:** (a) Preact + Signals + Wouter, built with esbuild. The coordinator serves JSON and the static bundle.
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
* **D3 checks:** `pnpm run check:design` prints a warning report. It is not in `pnpm test` and never blocks a pull request.
* **D4 performance:** measured targets, not stops: initial JavaScript ≤ 200 KB, interaction response ≤ 100 ms.
* **D5 approval:** the owner says "ok" in chat, and the worker records it in `decisions.md`.
* **D6 visual regression:** not now. Book screenshots are attached to the pull request.
* **D7 old specimens:** archived in `explorations/archive/`.
* **D8 book role:** the book is the live demo page of `packages/ui`. There is no separate copy.
* **Rejected:** blocking checks in `pnpm test` or CI (the reviewers' recommendation), and gates on the order of work.

## 2026-11-09 D7: Stage 2 built as packages/ui (pending owner review)

* **Built:**
  * `packages/ui`: tokens, the icon registry (TaskQ Lucide set), the formatter, `VirtualList` on TanStack Virtual core, and 14 controls.
  * `pnpm run book` builds [book/index.html](book/index.html) as one self-contained file.
  * `pnpm run check:design` prints the warning report.
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
  * The traceability check `tests/ui/trace.check.mjs` is part of the `pnpm run check:design` report. For each `pages/<page>.md`, it opens the routes listed on the spec's "Mockup routes" line and clicks every tab. It then reports any linked control or pattern that did not render, and requires `app-shell` and `nav-rail` on every page. It is a warning, not a gate (D6).
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

Source: the 2026-11-10 independent review (in git history) found 0 blockers, 11 majors and 21 nits. `check:design` caught none of them.

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

## 2026-11-10 D16: Priority is a queue class

* **Choice (owner):** `urgent`, `normal` (the default) or `background`, with `p0`, `p1` and `p2` accepted as synonyms. Recorded in [data foundation §2](../designs/fleet-data-foundation.md).
* **In the app:**
  * Urgent has a red chip and background a grey chip. Normal has no chip (OR-17).
  * Grouping by priority orders urgent, normal, background, and the rows inside a group carry no chip.
* **Planned:** the scheduler behaviour and the per-class queue view, in [fleet-operator-app.md](../designs/fleet-operator-app.md#future-improvements).
* **Review fixes to PR #24 also in this change:**
  * Hosts and Overview re-read hosts every 5 s and use the server clock.
  * The draft is kept when a send fails.
  * The IndexedDB clear closes connections and cancels pending writes.
  * List updates are ordered by request sequence and sent in chunks of at most 1000 ids; a failed update falls back to a reload.
  * The stream drains again after joining.
  * Tasks without a project have their own filter.
  * Durations use `endedAt`.
  * Totals, artifacts and commands carry parent ids.
  * The service worker caches only app routes, each under its own path.

## 2026-11-10 D17: Human entity codes

* **Question:** how operators read and recognise entities without UUIDs.
* **Options:** `T-FLT-A-42` (optional section), `T-FLT-42`, `FLT-42`, `T-4217`, host plus base32.
* **Choice:** `T-FLT-42` for tasks (`T`), questions (`Q`), conversations (`C`, owner 2026-11-13) and experiments (`E`); spec rules carry a mandatory section letter, `S-FLT-D3`. Scope `ALL` for general rules. Regex: `\b(?:([TQEC])-([A-Z][A-Z0-9]{1,4})-([1-9][0-9]*)|S-([A-Z][A-Z0-9]{1,4})-([A-DF-PRU-Z])([1-9][0-9]*))\b`. Grammar and allocation: [data foundation §2](../designs/fleet-data-foundation.md#2-task-production-work-item).
* **Display:** one formatter in `packages/ui` renders the code. The code sits directly after the title (OR-22), in monospace. The UUID is shown only in a tooltip and a "copy UUID" action, never as a second visible ID (OR-17). An entity without a code shows no ID (OR-38: a UUID is never shown). The formatter is `EntityCode`; a click or Enter copies the UUID without opening the row. A section or area of a task is a chip, not part of the code. `packages/ui/book/open-questions.tsx` renders `T-{t.id}` today and moves to the formatter.
* **Reason:** the type and project are visible outside the app (messengers, branches, prompts); hyphens and a leading letter keep codes apart from short SHAs and GitHub `#N`; no dots or slashes, so messengers do not auto-link them.

## 2026-11-10 D18: Lab screen, first build (Lab Stage 3)

* **Question:** how the `/lab` workspace shows experiments, the matrix, ratings and the registry with the existing book.
* **Choice:** the registry workspace `laboratory` becomes `lab` (route `/lab`, `app: true`), so nav, manifests and app routes follow the registry. `/lab` lists experiments and the use-case registry as data lists (state first: experiment state, source drift) and a "Waiting for you" panel with example approvals. `/lab/<id>` is one page: state and actions in the head, spend as totals with each budget's limit and action, the matrix as static rows (rows examples, columns variants; a cell holds a summary per evaluator and one line per repeat: state, select, repeat, time, credits, scores, run link, cancel or retry), variants with spread and Promote, the decision gate (judge-to-owner agreement), and ratings as decision blocks answered from the keyboard (`p`/`f`, `1`–`5`, `a`, `b`, …).
* **New in the book:** state families `experiment`, `trial` and `source`; matrix row styles `lab-row`, `lab-cell`, `lab-trial`, `lab-patch` in `styles.css`. No new control: the matrix reuses static data-list rows.
* **Reason:** inventory items of the Laboratory page with no new control; mockup tabs (Compare, Trials, Assessments) give way to one page because a cell already carries its trials.

## 2026-11-11 D19: Three columns, direction B, and a check for every rule

* **Problem (owner):** the app had an empty top bar, the conversation opened as a tall sticky panel that read as a popup, lists scrolled inside the scrolling page, and a row opened only from its title. The look was generic.
* **Why the rules let it through:** the top bar and the inner list scroller were written into the shell spec; no rule covered the row target; OR-14 was checked by searching source for `role="dialog"`, which a sticky panel does not match. Layout cannot be checked from source text.
* **Options shown (one live mockup, three styles):** A "Cut" (zero radius, heavy capitals, inverted selection), B "Instrument" (mono labels, hairlines, a state tick, teal accent), C "Tonal" (no lines, soft corners, raised sheet).
* **Choice (owner):** B, on the three-column layout.
* **Rules:** OR-24 three columns and no top bar, OR-25 one scroll per column, OR-26 the whole row opens, OR-27 open in the work area. OR-14 now points to OR-27.
* **Method:** every owner rule names its check ([owner rules](owner-rules.md#how-each-rule-is-checked)). The four new rules are checked on the rendered mockups and the built app by `tests/ui/layout.check.ts`.
* **Changed specs:** [app shell](patterns/app-shell.md), [list and detail](patterns/list-detail.md), [data list](controls/data-list.md), [navigation rail](controls/nav-rail.md), [principles §1 and §11](principles.md), [tokens](tokens.md). Catalogue rows are two lines (`size-row-2`), which replaces "one line per row" for the control column only.
* **Direction B in the controls:** state shows as a tick or a plain coloured icon, never a filled box in a list; the current item (row, section, tab) carries a 3 px accent tick; totals are a ruled strip, not boxed cells; state counters are outlined mono chips, filled only when active; the decision block has a left bar; a conversation sits on the plain surface and the operator's messages carry the accent tick; scrollbars are the browser's thin ones.
* **Colour (owner):** the first build used a teal accent on green-grey neutrals, picked without the owner; the owner rejected it. Three palettes were then shown on the dark tasks mockup: "Mono" (untinted neutrals, ink as the accent), "Graphite and blue", "Warm charcoal and cream". Choice: Mono. Running keeps its blue, because the accent no longer carries it. A palette is an owner choice (process step 2) and is shown before it reaches `tokens.json`.
* **Dark first (owner works in it):** the columns step up in lightness from navigation to work area in both themes (`rail`, `bg`, `surface`); the checks save a dark screenshot of every route next to the light one.
* **Type (owner, replaced by [D22](#2026-11-11-d22-system-fonts)):** IBM Plex Sans and IBM Plex Mono, weights 400, 500 and 600, Latin and Cyrillic. The app serves the files from its own origin, so \"no web fonts\" becomes \"no font from another origin\" ([tokens](tokens.md#type)). The book and the mockups are single files and do not embed the fonts: they show Plex where it is installed and the system stack elsewhere.
* **Lab, Hosts, API keys:** moved to the same columns ([laboratory](pages/laboratory.md), [hosts](pages/hosts.md)); their catalogue rows use the two-line shape.
* **Not done:** the result viewer's text frame scrolls inside the work area; the layout check reports it once the text is long enough.

## 2026-11-11 D20: Editing agents, pipelines and the catalogue

* **Question (owner):** how to edit agents and pipelines so that different scenarios stay clear and simple to set up.
* **Evidence:** the architect objects and use cases UC4, UC5, UC7 and UC8; the files in `config/`; and a published walkthrough of a "software factory" on Claude Code (labelled issue, intake with a risk rating, spec, build, a gate of plain checks, reviewers chosen by the rating and run in parallel, an approver that merges or escalates, at most three rounds). Its author edits everything as files and through chat and watches a separate dashboard.
* **Shown to the owner:** one mockup with four scenarios: an agent draft, that factory as a Fleet pipeline, a run with a failed step, a capability with its methods.
* **Options for the pipeline:** a step list with a read-only flow picture, or a canvas where the graph itself is edited.
* **Choice (owner):** the step list.
* **Reason:** one place to edit and no second source; rows carry more text than nodes; the same list shows a run.
* **Recorded as:** the [definition editor](patterns/definition-editor.md) pattern and the page specs [agents](pages/agents.md), [pipelines](pages/pipelines.md), [catalogue](pages/catalogue.md).
* **Built:** the controls [step list](controls/step-list.md), [draft bar](controls/draft-bar.md) and [choice](controls/choice.md), the `DefinitionEditor` pattern, the `definition` state family and the step kinds in the registry, and the mockup routes `#/agents`, `#/pipelines`, `#/pipelines/render-props`, `#/catalogue`.
* **Not done:** the app screens and the operations they need. The screen for skills is not designed.


## 2026-11-11 D21: One format per value, and messages that copy well

* **Problem (owner):** the screens mixed formats for the same kind of value, so the eye jumped: sentences in mono, times boxed in one place and plain in another, words and counts in the same column, scores formatted as counts, headers that wrapped, file names cut into stubs. Conversations showed neither time nor author on a message, and a copied message lost both.
* **Rule:** OR-28 One format per value, with [formats](formats.md) as the one table: type use, every value kind and its helper, rows and columns, the copy format, and what each screen answers.
* **Applied in `packages/ui`, not per screen:** a neutral age is plain and only an alert age is boxed; setting values are sans and left-aligned; step bands and step errors are sans; row facts use `.facts`, not `.id`; column headers never wrap. Mockups: Catalogue quality is a score, run files show their stem, headers shortened; catalogue rows hold one kind of value on the right.
* **Conversations:** each message has a head line with author and age. Thread text blocks now carry `at` and the sender (`sentBy` on new messages). Copy writes `text/plain` (a `**who · 11 Nov 14:03**` line per message) and `text/html` with only `b`, `i`, `code`, `pre`, `a`, `br`; `thread_export` takes `tz`.
* **Checks:** `layout.check.ts` warns on prose in mono and wrapped headers; `layout-break.check.mjs` injects a breach of each layout rule and fails if a check stays quiet.
* **Not done:** person names (only emails exist, so the email's local part is shown); the Lab app screen does not yet show score against the baseline per variant.

## 2026-11-11 D22: System fonts

* **Problem (owner):** IBM Plex looked no different from the system fonts, and on load the text visibly switched from the fallback to Plex. That jump breaks OR-23.
* **Choice (owner):** no web fonts. This replaces the type part of [D19](#2026-11-11-d19-three-columns-direction-b-and-a-check-for-every-rule).
* **Stacks:** `font-sans` is `system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI Variable Text", "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif` plus colour emoji; `font-mono` is `ui-monospace, "SF Mono", SFMono-Regular, "Cascadia Mono", "JetBrains Mono", Menlo, Consolas, "Liberation Mono", monospace`. Numbers keep `tabular-nums`.
* **Cyrillic:** San Francisco and SF Mono (macOS), Segoe UI and Cascadia Mono or Consolas (Windows), and the fontconfig defaults behind `system-ui` and `monospace` on Linux (Noto, Cantarell, DejaVu, Liberation) all carry Cyrillic.
* **Removed:** the `@fontsource` packages, the font copying and `@font-face` rules in `app/build.mjs`, the font files in the service worker precache, and the font check in `tests/ui/app.check.ts`.
* **Layout:** sizes, line height and row heights are unchanged; `check:design` and the app check report no layout warnings with the new metrics.

## 2026-11-11 D23: Name and mark

* **Question (owner):** what the product is called, Fleet or TaskQ, and what its logo is.
* **Name (owner):** Fleet. TaskQ was a prototype that Fleet replaces, and it named only the task queue. The `taskqorg` GitHub organisation keeps its name; people do not see it in the product.
* **Options for the mark,** shown in direction B ink on the dark and light rail at 88, 32, 20 and 16 px: A "Grid", six cells of a 3x3 grid that form an F and a formation; B "Slab", a solid ink square with a geometric f cut out; C "Cursor", a hairline square with a block cursor and a mono wordmark.
* **Choice (owner):** A.
* **Recorded as:** the `.brand-mark` element in the navigation head ([navigation rail](controls/nav-rail.md)), drawn by a CSS mask in `packages/ui/src/styles.css` and filled with `fg`, so it follows the theme. It is a brand asset, not an icon, so OR-8 does not apply to it. The name beside it is set in capitals at the section-title weight.
* **Not done:** the per-view install icons and favicons (`app/build.mjs`) still show each view's registry icon, so installed views stay distinguishable.

## 2026-11-11 D24: Limits on the Hosts page

* **Source:** the owner request of 2026-11-11 recorded in [people access §8 and §12](../designs/fleet-people-access.md#12-screens) (merged in #185): one Limits screen, dark theme first, no combined score.
* **Placement:** the Hosts work area at `/hosts/limits`, opened from the page head (OR-24, OR-27); the idle work area shows the system (telemetry), and the control column keeps the hosts catalogue only.
* **New control:** the [area chart](controls/area-chart.md), drawn with CSS `clip-path` polygons, because OR-8 rules out inline SVG and no chart library is used.
* **Not shown to the owner as options:** the layout follows §12 as written; the chart drawing is the only new choice and has one reasonable answer under OR-8.
* **Not done:** a per-member line in the chart (the pool line only), pin on the cards.

## 2026-11-12 D25: Owner review of lists, navigation and codes

* **Source:** owner review decisions P1 to P14 of 2026-11-12, given to the UI workers.
* **Rules:** OR-29 to OR-42 in [owner rules](owner-rules.md). OR-32 makes the Cmd+K palette the one exception to OR-14. OR-38 extends [D17](#2026-11-10-d17-human-entity-codes): a UUID is never shown, only copied.
* **Stored per person on the server:** saved views (OR-33), focus and its presets (OR-34), column widths (OR-41). Agents get saved views through MCP first (fleet.md R7, [agentic first](../../fleet.md)).
* **Checks:** none yet; the rules are in "Reading only" until the workers who build them add checks.

## 2026-11-12 Lab draft creation

Coordinator-approved bounded plan: New experiment opens a work-area form over the existing create operation, with searchable choices in the control column and explicit draft readback. A minimal shared TextField handles unsaved values; SettingField retains its applied-setting semantics. Create never launches. Lost replies retain uncertainty and cannot automatically repost; grants/actor changes clear local drafts. All existing experiment kinds and optional budget/stop settings remain available without new policy. CI owns browser checks and captured references; owner/native visual acceptance remains pending rather than inferred from source approval. Variant configuration diff and a new experiment engine are outside this slice.

## 2026-11-13: Fleet browser and installation icons

* **Question:** which mark identifies browser tabs and installed web apps?
* **Choice:** reuse the existing block F from the navigation for every workspace, on the dark background with light foreground. Keep workspace-specific launch URLs.
* **Reason:** the owner requested the existing application mark as both favicon and installed web app icon.

## 2026-11-14: Unified resource accounting and analytics UI primitives

* **Question:** how should metered vs subscription cost, multi-currency totals, scoped execution rollups, interactive CSS charts, and host vs execution telemetry be surfaced across `packages/ui` and the operator app without violating `OR-8`, `OR-17`, `OR-20`, `OR-25`, or `OR-39`?
* **Choice:** extend `Basis` with `'subscription'` (shown with `≈` plus a subscription mark when list-price equivalent exists, or `—` when unpriced), add `CostFigures` and `splitCostFigures()` so metered and subscription amounts are never summed across bases or currencies, make `ExecTree` recursively render `parentExecutionId` hierarchies with collapsed-by-default `tool_run` summaries per parent, extend CSS-only `AreaChart` with bucket selector `<button>` controls placed outside `.ac-plot` (`role="img"`), and add optional `BoundaryRow` resource summaries inside `Thread`.
* **Applied across screens:** Projects integrates `analytics_query` trend chart (`AreaChart`), `Totals`/`CostFigures`, dimension breakdowns, `unknown`/`hidden` indicators, and drill-down links; Tasks and Conversations show `own`/`delegated`/`spawned` rollups (`ScopedResourceRollup`) and distinct `elapsedMs` vs `executionWallMs`; Agents and Pipelines show 30-day usage and per-revision windows; Runners renders all currencies cleanly; Hosts keeps daemon health visually separate from active execution runs.
* **Reason:** preserves owner typography/minimalism rules (`no <svg>` in `OR-8`, known totals in `OR-39`, non-nested scroll in `OR-25`) while faithfully reflecting server resource accounting semantics.

## 2026-11-15: Independent Projects and access lifecycle

* **Choice:** configuration and save readback use independent detail reads; prepared confirmed figures have their own progress, failure and continuous virtual windows. The approved interface-performance plan selects prepared summaries, notifications and cheap checks; the five-second maximum requires later rendered-value load qualification.
* **Access:** one generation gate fences requests and unmounts private views before identity validation, persistent clearing and a new authorized feed baseline. Authored drafts are quarantined by identity/target rather than deleted; receipts remain server-derived cache.
* **Controls:** shared list anchor capture/restore and injected Composer persistence preserve position and newer input without adding page-local scrollers or storage authorities.

## 2026-11-15: Row actions without hover, selection mode, Pinned group

* **Question:** row pin and checkbox appeared on hover over the row facts and shifted them when set, so the facts could not be hovered; checking rows led nowhere (no select all, no actions).
* **Options:** A. checkbox replaces the state icon on hover; B. reserved empty slots for checkbox and pin; C. checkboxes only in selection mode, as their own column. Pin marks: Pinned group, pin before the title, pin in the facts.
* **Choice (owner):** C, with the checkbox column left of the state icon so the state stays visible; pin through the row menu (right click), `p`, the selection bar and the task header; pinned rows as a Pinned group at the top with no per-row mark. The state quick filter has a line of its own between the search line and group and sort; the title takes all width the facts leave.
* **Controls:** `ContextMenu`, `SelectBar`, `VirtualList` `onRowMenu`, `FilterBar` `counters` and `tools`. `RowActs` is removed. OR-37 updated.

## 2026-11-15: Group and sort as chips with menus, selection from the left

* **Question:** Group opened its choices inline (the line changed), with no quick way to remove it; Sort put the direction before the field and stepped through fields on click; the Select button sat at the far right and the selection bar replaced group and sort.
* **Options:** group as a chip with a cross, a plain button with None inside, or one View popover; sort as one chip with the direction after the field, a chain of sort levels, or presets; selection bar on its own line, as the list header, or floating at the bottom.
* **Choice (owner):** the first of each. Multi-level order is Group plus Sort (groups in attention order), so no sort chain for now. The select square starts the tools line above the checkbox column; the selection bar is its own line (select all, cross, count, actions on the right).

## 2026-11-15: Pin mark on pinned rows, plain group and sort chips

* **Choice (owner):** a pinned row shows a pin before its state icon, and a click on it unpins (the row leaves the Pinned group). Only pinned rows get this column; other rows do not move. This replaces "rows carry no pin mark".
* **Choice (owner):** the group and sort chips have no fill of their own and use the same box as the saved-view buttons (height, padding, centred content); a set value shows in foreground text.

## 2026-11-15: Delivery as list and detail, one overview

* **Question:** Delivery was one long page of stage cards and charts with no way to open a pull request, and it updated only by polling.
* **Options:** the work area as an overview, a timeline, a metrics board, or tabs of the three.
* **Choice (owner):** D, one overview in the order of the operator's questions: Needs a look, Moving now, Pace. No tabs: each tab answered only part of the question (OR-19). The control column lists pull requests by stage like Tasks; a pull request opens in the work area. The stage tiles (repeating the counters, OR-17) and the 24-hour in-flight stack are dropped.
* **With it:** the `delivery` state family with Checks failed (`pr_failed`, from the latest finished pull_request run, read with a key); delivery changes in the change stream (kind `delivery`); a GitHub read every minute with a key; `TimeStrip` parts.

## 2026-11-16: Conditional plan facts in the existing step list

* **Context:** the owner approved the reviewed conditional-pipeline implementation.
* **Choice:** keep activation separate from execution state in the shared StepList; show pending, not selected and blocked without fabricated execution measurements. Lab opens an authorized trial plan in its existing detail and links retained project assessments. Definition rows expose finite conditions and the result source.
* **Evidence boundary:** a completed run or passing assessment contract does not imply model-quality acceptance or adoption.

## 2026-11-15: Version beside the person, one update control

* **Question:** where does the person see what Fleet runs, and why did "Update available" come and go and return after an update?
* **Cause found:** the banner listened for the service worker's `updatefound` event. The browser often runs its update check during navigation, before the page listens, so the event was missed (no banner) or arrived later (banner). Reload did not activate the waiting worker (no `skipWaiting`), so the old worker kept control and the next check could raise the banner again.
* **Choice (owner):** one control: the version after the person's name, toned by the parts' states, a click shows every part's version and state; when the site serves a newer app, an Update button that updates on the first click. The banner is removed.
* **How:** `system_versions` (coordinator, served UI, gateway, hosts with update state); the app compares its own hashed assets with the site's current page instead of waiting for worker events; Update tells a waiting worker to `skipWaiting`, then reloads.

## 2026-11-16: Tasks group by project; system presets set states only

* **Question:** which grouping does Tasks open with, and what does a system preset (Needs me, Running) change?
* **Options:** keep priority as the default grouping (D11) or group by project; let a preset carry its own grouping and sort, or only its states.
* **Choice (owner):** group by project and sort by most recently changed first by default; a system preset sets only the state filter and keeps the person's grouping, sort, projects and search. Personal views still restore everything they saved; views saved before this kept no group and open grouped by priority as when they were saved.
* **Reason:** the owner works across projects and switches presets often; a preset that reset the grouping undid the person's choice each time.

## 2026-11-17: One shell header; focus chip with ignore and the outside-focus count

* **Question (owner):** search and the project focus are key controls but sat in the navigation, small and easy to miss; a set focus looked like any other row, and projects the person never wants to see could only be left out by focusing all others.
* **Choice (owner):** one shell header over the area right of the navigation: the collapse toggle (moved from the column corner) and the section title (moved from the list column) on the left; the focus chip and a search icon on the right; a slot for a right-panel toggle. The Search item and the focus leave the rail (OR-31, OR-34, OR-42, new OR-43; OR-24 loses "no top bar").
* **Choice (owner):** the focus chip names the projects and is toned while set; its panel floats under it. A third project state, ignored: hidden everywhere for the person, also with All projects, including search; restored from "Ignored (n)" in the panel. Items that need the person outside the focus show as "N need you outside focus"; after review the owner chose that it never counts ignored projects (ignore is full silence) and shows nothing with All projects.
* **Decided here:** the outside count opens `/overview?all` (the Overview without the focus; ignored projects stay hidden); ignoring a project removes it from the focus; a query that names an ignored project exactly (`project:<id>`) or is a code jump finds it, as a view whose URL names it shows it; an agent's search sees every project; the set chip is filled with the accent, which reads in dark and light alike.

## 2026-11-17: Conversation titles, hover copy, result names inside the column

* **Question (owner, seen live):** the conversation detail showed only `C-FLT-65 claude-worker`; titles were the first message (often the standing-rules preamble) or "New conversation"; long result names ran past the right edge; every transcript block showed a copy button.
* **Choice (owner):** the detail header shows the title, then the code and profile muted. Titles are generated by a cheap model after the first reply, as the Claude and Codex apps do, stored on the session and never written over a title a person or agent set; Rename in the detail menu and `session_rename` for agents; older conversations are backfilled by the same pass. Copy icons in the transcript show on hover or focus and always on touch, the way the navigation pin does: an exception to OR-37's "no buttons that appear on hover" for transcript rows, which are read, not picked. Result names wrap and end in an ellipsis, the full name in the tooltip.
* **Decided here:** the model is `gemini-3.5-flash-lite` on the `google-ai` connection, the cheapest text model granted to every project (the only Anthropic connection is granted to one project); its calls are Fleet's own system activity with their token counts. The empty "New conversation" rows were host job sessions of pipeline script steps; they leave the Conversations list and are kept, since each holds its step's job.

## 2026-11-18: Entity card on every reference

* **Question (owner):** a code in a detail (`T-FLT-42`, a pull request, a host, a run) says nothing until the person opens it, and the code only copied its UUID. GitHub's hover card on `taskqorg/fleet #643` shows the repository and number, the state, the title, the author and the time.
* **Choice (owner):** one standard control for references, `EntityCode`, with a card of the object's useful details: kind icon and code, state badge, title, project, agent, compact time, one to four facts per kind, and the actions Open (first), copy code, copy UUID. Copy moves into the card; a UUID is still never shown (OR-38). New OR-44.
* **Choice (owner, after the first build):** the card opens on click, tap, Enter or Space, not on hover; clicking the code never navigates, Open does. Escape, a click outside or the code again close it; focus moves into the card when a key opened it and returns to the code on close. A non-modal `role="dialog"`.
* **Decided here:** one read `entity_card` by code or kind and id for every kind (task, question, conversation, run, result, experiment, project, host, agent, pipeline, pull request, release), under the same grants as each kind's own reads, and the same read for agents as an MCP tool (fleet.md R7); the app keeps cards in its store and rereads them on the change feed. D17 codes in agent messages become references. The per-kind fields, and the kinds left out, are in [entity card](controls/entity-card.md).

## 2026-11-17: Delivery pace by calendar day; red past the stage's P90; Queue failed

* **Question:** the Pace charts misled: one slow day squashed the scale, a stack of P50s per stage summed medians, days without install times looked like data, the two charts used different axes (a rolling 24 h), and Moving now bars were all full width. The list was mostly installed rows, and "needs me" had no stage for a pull request the merge queue removed.
* **Options:** three Pace variants rendered from `packages/ui` in the book: A, small charts stacked on one calendar-day axis; B, one combined chart (P50 line with a P90 band over the merged bars) beside the stage share; C, a table of figures per day.
* **Choice (owner):** A, with C's over-norm tint on the P50 and P90 rows under the Open-to-installed chart. Days are calendar days in the viewer's zone (24 h is twelve local two-hour buckets, 30 days thins its labels); a day without a measured stage is "not measured", never zero; a day measured in part is striped; the scale tops at about P95 and a figure above it shows its true value. Where the time goes is one share bar for the range or the picked day: the mean per pull request over those measured through install, so the parts add up.
* **Choice (owner):** red means past the stage's P90 over the last 14 days (`delivery_baseline`), for the Moving now bars, the list's time chips and the P50/P90 rows. This replaces the fixed limits per stage.
* **Choice (owner):** a new stage `mq_failed`, Queue failed: the coordinator derives it from the latest finished merge-group runs (an open pull request whose last queue run of some workflow failed, with no new commit since and not set to enter the queue again). Needs me is Checks failed and Queue failed.
* **With it:** the Delivery list is the shared list view (`useListPage` over the rows `delivery_status` holds) with the system presets Needs me, In flight and Installed (states only), grouped by stage with Installed collapsed; `DayChart`, `ShareBar`, `NormBar` and `capOf` in `packages/ui`.

## 2026-11-17: New conversation in the work area, with presets

* **Question (owner):** the new-conversation form sat in the list column above the list, showed raw role and profile ids, kept "Projectless conversation" as a separate button, and nothing could start in a project without roles (cs).
* **Choice (owner):** a new conversation opens in the work area at its own URL `/conversations/new` (linkable, Back works), like the new-session screens of Claude Code, Codex and Orca: the project (recent and focused first, ignored left out), then the presets of what we are doing now, then the composer, with chips under it for project, agent, access and branch. "No project" is one project choice. The New conversation button moves into the shell header next to the section title.
* **Choice (owner):** presets set the role or profile, read or write access, a new branch or none, and a starter instruction. A project keeps its own in its definition (`settings.presets`, edited with `project_update`); without them Fleet's system presets apply: Discuss, Design, Fix / build, Review, Investigate a problem. A preset whose role the project does not map falls back on a system profile, so a conversation can always start. A card says what the preset sets in words, not raw ids.
* **Decided here:** the button is an icon (`plus`, tooltip "New conversation", OR-1) in a new header slot after the title (`ShellActions`); access and branch chips flip in one click for this start; "no branch" is a worktree detached at the base, not the host checkout; a preset sets no model or effort of its own, since every profile fixes its model (a different model is another profile); `?in=<project>` preselects a project, and the old `?new=<project>` link opens the page.
* **Choice (owner, 2026-11-18):** read access keeps Fleet operations allowed: the code does not change, but tasks and pipelines can be started. Cards and chips say "Code read only" and "The code does not change", not "Nothing changes".

## 2026-11-17: Composer as one box; message attachments

* **Question (owner):** the composer was a bare text area with a Send button and a separate Stop above it; files could not be given to an agent at all.
* **Choice (owner):** the composer of Orca, Claude Code and Codex: one bordered box with the text inside and one bottom row: `+` (attach), the agent profile with model and effort (opens the existing profile switch), the permissions (shown only), one Send button that is Stop while the agent runs. Dictation is left out.
* **Choice (owner):** files by the `+` picker, by paste (screenshots, images, files; very long text as a .txt file) and by drop anywhere over the conversation with a highlight; chips above the text with thumbnail, name, size, progress and ×; Send waits for the uploads. The sent message shows its files in the transcript and a click opens the result viewer.
* **Decided here:** pasted text longer than 4,000 characters becomes `pasted-<n>.txt`; a message thumbnail is a fixed 72 px square (token `size.thumb`) so a loading image moves nothing; the permissions chip says `Workspace` or the profile's mode, plus `network` with git network access, and lists the tools in its tooltip; a message with files is queued, never steered.
* **Spec:** [composer](controls/composer.md); server and agent delivery in [Fleet Conversations, Attachments](../designs/fleet-conversations.md#attachments).

## 2026-11-17: Navigation counters with a focus split

* **Question (owner):** the rail had a counter slot that nothing filled; which number does each section show, and how does it read with a project focus?
* **Choice (owner):** each section counts a counter view, a person setting chosen from the section's system or saved views; defaults Tasks and Conversations Running, Delivery In flight, Results the last 24 hours, Hosts offline hosts (attention). Needs you stays the head chip, not a counter. With a focus the in-focus count is bright, then a separator and the outside remainder dim (`12·30`); ignored projects are never counted; collapsed shows only the bright number as a badge, with no icon moving. One server read (`nav_counts`, also an agent tool) computes every counter with the lists' own filters; the app rereads it on changes, debounced.
* **Decided here:** zero in focus with some outside reads `·30` on no fill, so a bare number always means in focus, and collapsed shows no badge for it; other sections get no counter; the choice is a menu item on a view chip ("Count in navigation", "Stop counting in navigation"); a removed saved view takes its counter choice with it and the default counts again; a saved view naming its projects counts all of it as in focus, as its list ignores the focus; Results and Hosts counters can only be turned off.

## 2026-11-18: Direct detail facts, aligned transcript controls and live ages

* **Choice (owner):** show resource figures and task requirements directly. Keep technical names and full paths behind the relevant object or its metadata.
* **Choice (owner):** group transcript time, model and actions at the left edge below the block. Distinguish operator and agent message content by alignment. Keep tooltips at their readable width near viewport edges.
* **Choice (owner):** use the shared compact time control for relative timestamps and update them live. Give entity codes a copy icon on hover and focus, in a reserved slot. Show a check after copy succeeds. Keep the code itself as the card trigger.

* **Refinement (owner):** label the run and its duration, size code to its content, list results vertically, and distinguish questions and answers on a neutral transcript surface.

## 2026-11-18: Narrow layouts and touch targets

* **Choice (owner):** use the available width, share one edge gutter, retain consistent button geometry and keep popups readable as the window narrows.
* **Application:** follow [Narrow work areas](principles.md#narrow-work-areas). Apply the compact spacing by work-area width and the larger hit targets by input mode.

## 2026-11-20: Pages reworked from the live site; one-line catalogue rows; useful work areas

* **Question (owner):** rework every page in the Instrument direction from the live site's data. The live pages showed two-line rows that cut titles to half, empty work areas ("Pick a row to open it here."), raw URLs running past the Needs-you column, near-duplicate alerts piled one per card, and row figures that ran together ("cyprus.bzcs", "3.52 9 GB").
* **Options:** rendered on a Design canvas from the real `packages/ui` components and live data, each page beside a screenshot of the live one: Overview A (one grouped block per cause beside side panels) and B (the Needs-you inbox in the control column, the answer or the fleet in the work area); Tasks A (one-line rows, the task open) and B (a full-width table until a task opens).
* **Choice (owner):** Overview B, with the fleet's state in the work area while nothing is picked; Tasks A. The same patterns carry to the other pages.
* **Decided here:** catalogue rows are one line, the title then its code, service facts on the right, 32 px (44 px on touch); the control column is 520 px by default (`size-ctl`). A task row counts runs from two. Every work area that used to wait for a pick says what needs a look: Hosts lists only the operations with errors, refusals or a P95 over a second (the healthy rest folds), Delivery folds several slow metrics into one block named by the pull requests they first went over with, Lab heads with the finished experiments that wait for a decision, API keys with provider usage Fleet did not record, retired keys and connections limited to some projects, Runners with GitHub-hosted CI minutes on a day self-hosted runners stood free. Gateway calls are named by their connection, not "Tool calls"; a host row labels its load and its free memory and hides a stale reading; Results names files without their hashes; API keys name the common holder once.
* **Not in this change:** folding a Lab series into one row in Conversations, a summary in the Conversations and Tasks work areas, real state counts in Results, and the share of runs without a project in Projects analytics.


## 2026-11-20: Library, Lab questions, Changes and Spend

* **Question (owner):** a usability review against what Fleet is for: keep agent settings the same on every host, add a skill once and have it everywhere, state a theory about a pipeline, run it on models and variants and read the conclusion, and see tokens, resources and spend, for the operator and for agents. Also an agent catalogue with tags and marks, and a clearer Lab.
* **Evidence (live site, 20 Nov):** 41 agents, 21 of them Lab variants in the main list; 10 waiting for a repository version (9 from the modern-web-guidance skill of #672, none active); 23 agents and 7 pipelines only in the store, and only store-only pipelines ran in 30 days; skills had no screen; 36 experiments in 14 series, read as pipeline and variant names, not as questions; one agent took 65% of the month's list price; nothing showed which host runs which release and harness pins.
* **Options:** rendered on the design canvas from `packages/ui` and live data: Library A (agents by category, active first, variants folded, summary when nothing is open), Skills A, Pipelines A, Lab B (questions with answers; an experiment as question, design, runs, answer), Changes A, Spend A.
* **Choice (owner):** all of them, as specifications first.
* **Recorded as:** requirements in [definition editors: library](../designs/fleet-definition-editors.md#library), [Lab screen: questions view](../designs/fleet-lab.md#lab-screen), [system management: changes screen](../designs/fleet-system-management.md#changes-screen), [analytics: spend view](../designs/fleet-analytics.md#spend-view); page specs [agents](pages/agents.md), [pipelines](pages/pipelines.md), [laboratory](pages/laboratory.md), [changes](pages/changes.md), [spend](pages/spend.md); [inventory](inventory.md) rows. A work area with nothing open shows the section's summary ([list and detail](patterns/list-detail.md)).
* **Not decided:** where the new definition fields and skill rows are stored, the category list's home, and how a release names the pull request behind a changed file.

## 2026-11-21: Navigation coherence (OR-46–OR-49), OR-45 clarification, and unified CatalogueTree

* **Question:** after consolidating the 15-section rail into 8 sections (`fleet-ia-and-journeys.md`), screens drifted into a horizontal `<nav class="sub-nav">` bar above columns, mode tabs in the Control Column, Work Area tabs that rebuilt the Control Column, `override` attention badges on configuration rows, raw `role="tablist"` markup, and early-return loading states that unmounted `ListDetail`.
* **Choice:**
  * **OR-45 clarification:** Diff-from-default views show identity plus **only** fields that differ from their base default, collapsing unchanged defaults into `Inherited defaults (N)`. No `override`, `overridden`, or `custom` badges or words, and no `t-attention` tone on normal configuration values; show the base default only in a tooltip if useful (`OR-20`).
  * **OR-46 (One nav level, no second shell bar):** One `NavRail` (8 sections) and one `ShellHeader`. Horizontal `<nav class="sub-nav">` above columns is forbidden. Section object navigation lives in the Control Column; `TabBar` is allowed only inside the Work Area for facets of a single opened entity.
  * **OR-47 (One-way causality & URL truth):** Strict left-to-right flow (`NavRail` → Control Column → Work Area). Work Area tabs never rebuild the Control Column. Every opened item (including Lab use cases, entity creation, `Limits`) has a URL and renders in `detail` with an icon `Close` button in `.detail-h` (`OR-15`). `ListDetail` stays mounted while loading (skeleton inside the column, never an early return replacing `ListDetail`).
  * **OR-48 (Two catalogue engines only):** Control Column uses strictly `ListPagePanel`, `CatalogueTree`, or none (`Spend`, `Runners`). Raw `<div class="vl-wrap">` catalogues are forbidden; section actions (`New …`) sit in `<ShellActions>` (`OR-43`), not in `<header class="page-h">`.
  * **OR-49 (One control per job):** `TabBar` (`packages/ui`) is the only tab control (no raw `role="tablist"`). `Choice` is only for 2–5 fixed options (never project/entity lists or single-option checkboxes). `EntityCode` (`OR-44`) is used for every entity link.
  * **Unified `CatalogueTree` for `Lab`, `Library`, and `Infrastructure`:** each of those three System sections uses one `CatalogueTree` in the Control Column (`Use Cases` + `Runs` in `Lab`; `Methods` + `Pipelines` + `Agents` + `Skills` in `Library`; `Hosts` + `Accounts` + `Connections & API keys` + `Runners` in `Infrastructure`) instead of sub-nav bars or Control Column mode switchers.
* **Reason:** enforces a single predictable left-to-right navigation model, eliminates redundant chrome and state loss on load/tab switches, and provides machine-checkable rules in `packages/ui/scripts/check-design.mjs`.

## 2026-11-23: Quiet live list updates

* **Question:** when should the list show loading progress?
* **Options:** show progress for every read, or for initial and operator-initiated reads.
* **Choice:** show progress for initial and operator-initiated reads. Keep background updates quiet and surface failures with retry. Repeated feed changes with identical results had been flashing progress and dimming the list ([T-FLT-22](https://fleet.ufobe.com/tasks/486357fa-2fe2-4d31-95d9-14a90b005574)).

## 2026-11-24: Cloudflare Access in Humans

* **Choice:** show the current email rule alongside Fleet admission. Keep a revoked identity with unresolved edge removal available in the existing list and detail workspace. Use the shared error and action controls for retry.
* **Reason:** the owner can identify and repair a failed edge change after Fleet access is revoked.

## 2026-11-25: Conversation-first Fleet, rules v2

* **Question (owner):** navigation is incoherent, elements differ between screens, data is shown without logic and transitions are unclear. Asked to fix it as a whole, with a rule list that every screen follows.
* **Evidence (live site, 24 Nov, 1440 and 390 px, dark):** 18 screens and 8 rail items, so 10 screens are reachable only by URL or Spotlight; `/agents`, `/pipelines` and `/catalogue` render the same page, and so do `/hosts`, `/keys` and `/runners`; 22 of 34 Needs-you rows are the same failure; one state is called Done, Completed and Succeeded; a conversation shows a metrics header, one row per tool call, repeated attachments and raw UUIDs; on a phone, task titles shrink to 5–8 characters.
* **Options:** a host-centric Overview (fleet map, flow board, pulse timeline); three navigation maps (keep 8 sections, 6 sections with one Work list, project first); a conversation-first product. All were rendered on a design board.
* **Choice (owner):** conversation first. Three principles: glance, work through conversation, details on demand. Screen 1 is Home with sessions; screen 2 is Live, a technical wall of everything that runs. The rail has seven sections: Home, Live, Conversations, Projects, Lab, Library, System. The host-centric Overview options were not taken.
* **Recorded as:** [principles](owner-rules.md#principles), OR-50 to OR-61 and new wording of OR-19, OR-24, OR-27, OR-40, OR-43 and OR-46 in [owner rules](owner-rules.md); the seven sections in [information architecture](../designs/fleet-ia-and-journeys.md#1-closed-information-architecture); [navigation and columns](patterns/navigation-and-columns.md).
* **Not decided:** the session context panel ([FLT-30](https://youtrack.ufobe.com/issue/FLT-30)); the Telegram channel's rooms and approval cards ([FLT-31](https://youtrack.ufobe.com/issue/FLT-31) and its subtasks); page specs for Home and Live; each new rule's automated check.

## 2026-11-25: Screens built from blocks

* **Question (owner):** start composing interface rules and blocks, and build the interface from those blocks.
* **Choice:** Home, Live and the session view are composed of named blocks. Each block has one spec in `controls/` (a new file, or a section of the control it extends), and one component on the design board, [blocks.bl](board/blocks.bl), with every state as a variant. Screen canvases place instances of those components, so a block change reaches every screen. The block list maps each block to its `packages/ui` control or marks it new: [blocks of Home, Live and the session view](controls/README.md#blocks-of-home-live-and-the-session-view).
* **Reason:** the same element looked different on different screens; one master per block keeps one look, and one spec per block keeps one source per rule.

## 2026-11-25: Usability rules v2 and the product map

* **Question (owner, through the orchestrator session):** the usability rules were written for the list-and-detail app; the conversation-first direction needs rules every screen and every block can be checked against, and the later end-to-end variant needs a map of what exists today.
* **Choice:** every rule in [principles](principles.md) is one testable sentence with an id (`UR-<n>`) and its check, a browser step or a review. New sections cover glance, conversation as the main mode, details on demand, wayfinding, feedback, error prevention and recovery, consistency and phone. The rules link the owner rules and never restate them; owner rules win. The [product map](product-map.md) lists every capability and route with its target section, built state, prod state and operations.
* **Reason:** a rule without a check is kept only by reading it, and the rebuild needs one list of what works and what does not.
