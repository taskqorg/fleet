# Design decisions

Append only. Each entry: date, question, options, choice, reason.

## 2026-11-09 D1: Owner priorities for the interface

* **Given by the owner:** controls with ideal usability; smooth scrolling that never jumps while rows arrive; fast, remembered filtering and search; dense but calm screens; decisions answered inline; measured, estimated and unavailable numbers look different; light and dark; desktop first, phone usable; spec-driven work.
* **Recorded in:** [principles.md](principles.md).

## 2026-11-09 D2: Visual direction (open)

* **Options:** [A Quiet ledger](explorations/archive/a-quiet-ledger.html), [B Console](explorations/archive/b-console.html), [C Soft paper](explorations/archive/c-soft-paper.html).
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
* **Reason:** the type and project are visible outside the app (messengers, branches, prompts); hyphens and a leading letter keep codes apart from short SHAs and GitHub `#N`; no dots or slashes, so messengers do not auto-link them. Brief: [entity-ids-brief.md](../designs/entity-ids-brief.md).

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
