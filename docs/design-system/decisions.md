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
* **Extended (25 Nov, [FLT-47](https://youtrack.ufobe.com/issue/FLT-47)):** results (`R`, original artifacts) and bots (`B`) get codes too, so OR-38 holds on their pages ([FLT-39](https://youtrack.ufobe.com/issue/FLT-39), [FLT-40](https://youtrack.ufobe.com/issue/FLT-40)); regex `[TQECRB]`.

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
* **Recorded as:** [principles](owner-rules.md#principles-card), OR-50 to OR-61 and new wording of OR-19, OR-24, OR-27, OR-40, OR-43 and OR-46 in [owner rules](owner-rules.md); the seven sections in [information architecture](../designs/fleet-ia-and-journeys.md#1-closed-information-architecture); [navigation and columns](patterns/navigation-and-columns.md).
* **Not decided:** the session context panel ([FLT-30](https://youtrack.ufobe.com/issue/FLT-30)); the Telegram channel's rooms and approval cards ([FLT-31](https://youtrack.ufobe.com/issue/FLT-31) and its subtasks); page specs for Home and Live; each new rule's automated check.

## 2026-11-25: Screens built from blocks

* **Question (owner):** start composing interface rules and blocks, and build the interface from those blocks.
* **Choice:** Home, Live and the session view are composed of named blocks. Each block has one spec in `controls/` (a new file, or a section of the control it extends), and one component on the design board, [blocks.bl](board/blocks.bl), with every state as a variant. Screen canvases place instances of those components, so a block change reaches every screen. The block list maps each block to its `packages/ui` control or marks it new: [blocks of Home, Live and the session view](controls/README.md#blocks-of-home-live-and-the-session-view).
* **Reason:** the same element looked different on different screens; one master per block keeps one look, and one spec per block keeps one source per rule.

## 2026-11-25: Usability rules v2 and the product map

* **Question (owner, through the orchestrator session):** the usability rules were written for the list-and-detail app; the conversation-first direction needs rules every screen and every block can be checked against, and the later end-to-end variant needs a map of what exists today.
* **Choice:** every rule in [principles](principles.md) is one testable sentence with an id (`UR-<n>`) and its check, a browser step or a review. New sections cover glance, conversation as the main mode, details on demand, wayfinding, feedback, error prevention and recovery, consistency and phone. The rules link the owner rules and never restate them; owner rules win. The [product map](product-map.md) lists every capability and route with its target section, built state, prod state and operations.
* **Reason:** a rule without a check is kept only by reading it, and the rebuild needs one list of what works and what does not.
## 2026-11-25: The design process helps, never blocks

* **Question (owner):** what to add to the design and development process so that screens stop drifting apart and agents know how the interface is structured.
* **Options:** blocking checks (a ratchet on the design report, no page change without its canvas, a required design-review status); or precise context for each agent plus reports people read.
* **Choice (owner):** no new gates; CI is already slow and its failures say little, so a gate only adds a place where work stalls. Agents get exactly the information, instructions and tools their task needs. Five changes: a per-page context command; rules cited by ID with a weekly rules review; page specs that name their blocks; a product map with a page-by-rule view; a design report that leads with what a pull request changed, and a walk through journeys J1 to J5 on production after a release, as reports.
* **Recorded as:** [Context for a UI task](README.md#context-for-a-ui-task), `scripts/ui-context.ts`, the design line in `AGENTS.md`.

## 2026-11-25: One end-to-end variant of the interface

* **Question (owner):** draw one variant of the whole web interface, assembled only from the board's blocks, so the product can be rebuilt on it.
* **Choice:** every rail section has a target spec with `## Blocks` and a screen canvas built from block instances: [session view](pages/session.md), [Conversations](pages/conversations.md#target), [Projects](pages/projects.md#target), [Lab](pages/laboratory.md#target), [Library](pages/library.md), [System](pages/system.md), with Home and Live as before. [screens/flow.bl](board/screens/flow.bl) draws J1–J5, answering a question from Home and waking a bot as click paths across the screens. Every earlier route has a target route in the [route consolidation map](../designs/fleet-ia-and-journeys.md#route-consolidation-map); URLs are section, kind and human code (`/projects/FLT/tasks/T-FLT-42`, `/library/agents/<id>`, `/system/hosts/<name>`).
* **Earlier specs:** Tasks, Results and Delivery stay as the content of the Projects facets; Catalogue, Agents, Pipelines and Changes as Library roots; Hosts, Runners and Spend as System roots. Each names its target home; none is duplicated.
* **Owner choices, answered by the owner the same day:** a session opened from Home is `/home/C-…`, full width, Home highlighted; the context panel is a right column and a sheet on a phone; Conversations grouping, sorting and saved views stay configurable per person, as built (the owner asked to keep them user-configurable), with Waiting for you first, then by project as the default view; a project opens on Sessions; Lab's idle screen is the Model × Pipeline matrix. The other options were removed from the canvases.
* **Review of the current build:** each section spec checks its [product map](product-map.md) rows and says what it keeps, changes and drops; clutter goes, useful tools such as per-person grouping, sorting and saved views stay. Dropped: the conversation metrics header and per-boundary resource rows (into Details); the harness mark in conversation rows; resource analytics on Projects (cost lives on System › Spend); Lab's mode switch, summary strip and undecided banner; Library's harness and category breakdowns and the bot Analytics tab; the spend table on Runners.
* **Not decided:** result codes for result URLs; a menu master for the Group menu (drawn as a marked stand-in).
## 2026-11-25: A target keeps what works
## 2026-11-25: A target looks at what exists

* **Question:** the end-to-end variant proposed a fixed grouping for Conversations, while the app already lets each person choose grouping, sorting and saved views.
* **Options:** a target keeps every current capability unless a decision removes it; or a target reviews each current capability and keeps, changes or drops it, listing the drops.
* **Choice (owner):** review, not keep-all: much of the current build is clutter to clean out; the point is that useful tools are not lost by accident. Recorded as process step 7 in [README](README.md#process-spec-driven-design).

## 2026-11-25: The board has one home

* **Question:** every task worktree called the Brilliant MCP `init` on its own copy of the board (as `scripts/ui-context.ts` told it to), so the owner's Brilliant app registered a new project per worktree and filled with "Agent working in board" notifications.
* **Options:** keep a board copy per worktree; or one persistent board worktree for drawing, with every other session reading the `.bl` files as text.
* **Choice (owner):** one home. Drawing happens only in `~/Projects/fleet-board`, one design session at a time, delivered by pull request; build and review sessions read canvases as text and never call Brilliant. Recorded in [README](README.md#design-board).

## 2026-11-25: Brilliant first

* **Question:** UI work (most recently the Live list rows) went straight to code, with options as HTML mockups or none at all, so the owner first saw a design in the built app.
* **Options:** design in code and review the result; or draw every visible change on the Brilliant board first and build only what the owner approved there.
* **Choice (owner):** Brilliant first, as a rule ([OR-62](owner-rules.md)). Design sessions draw in the board home `~/Projects/fleet-board`, options on git-ignored `drafts/` canvases, several sessions at once on their own canvases; approved canvases are copied into the task branch and committed with the spec. Recorded in [README](README.md#design-board).

## 2026-11-25: The user block and the app update

* **Question (owner):** the badge by the person changed colour or vanished on dark; the versions overview closed by itself; Update reloaded, came back, then went; a version warning appeared after navigation and was gone when opened.
* **Causes:** the rail avatar took the Home avatar style (transparent with a dark letter on dark, an amber disc when toned); the user block was re-created on every versions read, closing its overview; Update cleared its busy state before the reload landed; warnings came from one momentary read (the tab "unknown" for 3 s on every load, a gateway probe older than a minute, one failed read).
* **Options (board):** A, an icon button after the version, as built; B, an update strip above the person naming the new version, with Reload.
* **Choice (owner):** B and the rules: the avatar never changes; the version is toned only by a confirmed problem that is true now; an offline host does not tone it. Recorded in [nav rail](controls/nav-rail.md) (Version, Update) and drawn on [screens/shell](board/screens/shell.bl).
## 2026-11-25: A tab the person is away from waits for them before it updates

* **Question (owner):** with many releases a day, a hidden tab reloaded itself in the background, so every return after Alt-Tab found a cold page that loaded in pieces.
* **Choice (owner, "A ok", relayed by the orchestrator):** a hidden tab, or one whose window has no focus, never reloads; when the person comes back it updates at once if their last action was 30 s or more ago, otherwise after the rest of 30 s without activity. The read mirror (operator app D3) makes the reloaded page show its data at once. Supersedes "a hidden tab updates in the background" below. Recorded in [nav rail](controls/nav-rail.md) (Update).

## 2026-11-25: The app update installs itself when idle

* **Question (owner):** Fleet ships many releases a day, so the "New app ready" strip needs a click many times a day; an update should install itself when the person is not using the app, with a visible countdown that resets on activity, and never lose work.
* **Options:** hold the countdown while unsent input, an open dialog or a save in flight would be lost; or reload anyway and restore drafts afterwards.
* **Choice (owner, "ok"):** as drawn: the strip counts down 30 s without activity ("Updating in 24s" and a bar that empties), any activity puts it back to 30, a hidden tab updates in the background, Update still reloads at once; the count holds ("Waits for your draft", "Waits for the open dialog", "Waits for a save"), chosen over restoring drafts because holding cannot lose what a page did not save. Recorded in [nav rail](controls/nav-rail.md) (Update) and drawn on [screens/shell](board/screens/shell.bl), frame "Shell · app update installs itself when idle".
## 2026-11-25: Bot avatars

* **Question:** bots showed only a letter; the owner wanted a configurable avatar, animated ones included, made through Fleet's own pipelines, and the minimal way to show a bot's activity.
* **Options:** activity as light (the avatar dims, glows while working, one dot for waiting or failed) or as a 2 px state ring; Lottie/TGS players or native formats only.
* **Choice (owner, "Yes, all as drawn"):** light (A); native formats (still and animated images, animated SVG, muted WebM/MP4 loops; Lottie and TGS not shown); sizes sm 16, md 24, lg 40, xl 96; a still frame in lists, animation at lg/xl, on hover and while working; the picker in Settings & Memory; the `avatar-make` pipeline (image, check, optional animated SVG). Spec: [avatar](controls/avatar.md); board: `blocks.bl` (Blocks · Bot avatar) and `screens/library.bl` section 4.
## 2026-11-25: Bots in the navigation

* **Question (owner):** a button in the left menu shows bots, off by default; on, a zone shows the pinned bots and the recently active ones, with a configurable count and a link to all bots.
* **Options:** the zone at the bottom of the rail, above the user block, growing into free space (A); or at the top under the head, with a fixed number of rows so the sections do not move with bot activity (B).
* **Choice (owner):** A, bottom. Placement is therefore not a setting. The switch and the recent count are the person setting `railBots`; pins stay the Library pins. Recorded in [nav-rail](controls/nav-rail.md), board [shell](board/screens/shell.bl), frame "Shell · rail bots zone". Replaced on 2026-11-26 by the Pinned zone.

## 2026-11-25: Balancing lives on Live as items

* **Question (owner):** one place that decides where and on what work runs: take a host out, pin a project to hosts and subscriptions, keep personal subscriptions personal, see quota that will go unused, and say "Antigravity wherever possible" instead of one fixed model per profile; shown one item at a time and changed by voice through HAL0.
* **Options** (drawn in Fleet tokens on the board home's `drafts/balancing.bl`): A, a band of rules as sentences on top of Live; B, a Balancing root in System with Live showing one summary line; C, a Balance block on Live with one line per host, subscription, model class and pinned project, each with its own state and why, and no rule list.
* **Choice (owner):** C. A profile names a shared model class, not one model. Quota that will go unused is shown and HAL0 offers the preference; Fleet never switches on its own.
* **Reason:** the owner reads the balance per item on the technical screen; fields on items keep one fact in one place, with no rule engine.
* **Added by the owner the same day:** accounts become understandable on the same block (one real account with its logins per host, the placement order in plain words, the latest picks), and a conversation stays on its host and login unless forced, every move shown with its reason.
* **Recorded in:** [work allocation and balancing](../work-allocation.md), [Live](pages/live.md#balance), [inventory](inventory.md#balancing).

## 2026-11-25: Conversation timers are visible and edited in the composer

* **Question (owner, about HAL0's 5-minute timer):** it should be visible that a conversation has a timer and that Fleet supports it; today only the interval is editable, not the message; the inline edit form and the context panel look temporary. Edit should drop a prefilled command into the composer, which the operator edits and sends.
* **Options** (drawn on the board home's `drafts/session-timers.bl`): A, a chip in the session head plus the timer row in the context panel; B, a strip above the composer plus the row; C, the row in the context panel only.
* **Choice (owner):** A. Edit fills the composer with `/trigger <name> every 5m if changed: <message>`; one server parser previews (`session_command_preview`) and applies (`session_command`) it, agents call the same operation; a 409 shows what the timer is now with Load current. Timers are [triggers](../designs/fleet-triggers.md).
* **Recorded in:** [context panel](controls/context-panel.md#timer-chip), [composer commands](controls/composer.md#commands), [session view](pages/session.md), board [screens/session.bl](board/screens/session.bl) (*Session timers (approved A)*).
## 2026-11-25: One row anatomy for work

* **Question (owner, looking at Live):** the lists do not show all the data; it is unclear how long ago a state changed and how long an item has been in it; rows do not reuse the best parts of other lists. Show the time and the agent, and mark a row that looks fine but has made 500 attempts.
* **Options for the row (drawn on the board, `drafts/live-rows`):** A every fact on one line (attempts, latest attempt, time in work, time in state, agent); B quiet, only attempts, time in work and agent on the row, the rest in the tooltip and the detail; C the agent before the title and the facts on a second line.
* **Choice (owner):** B.
* **Options for what stands out:** (a) a fixed limit (rejected: no invented limits); (b) a spread statistic over the list's rows (Tukey fence, MAD); (c) an order of magnitude over the peers' median; (d) nothing stands out, the number alone.
* **Choice:** one shared part, [work facts](controls/work-facts.md) (`WorkFacts`), form B: `×N` attempts, time in work, agent mark; the latest attempt and the time in state in the tooltip of the time in work. Parts show by meaning: `×N` from two attempts, time in work always when known. Outliers by (c), `peerFence`: tried on prod data, (b) marked every `×2` and `×3`, because most tasks have one attempt and the spread is zero; (c) marks `×207` and leaves `×2` and `×3` plain.
* **Built into Live:** worker slots (attempts, time in work, agent mark in place of the harness name and tokens), a new In work list of every open task, Delivery rows (time since opened and in the stage), Just now (repeats as one event with `×N`, named by the task or the conversation), and `+K more` that opens the rest in place. Reads: `task_page` adds `lastStartedAt`, `executions_runners` adds `sessionTitle`; agents read both through ops and MCP.
* **Next adopters:** Tasks (its row facts become `WorkFacts`), Conversations (agent mark and time in state), Results and Runners (time and agent), Home cards (time in state).
* **Rule:** [OR-63](controls/work-facts.md#rules-kept-here).
* **Board (OR-62):** options A, B and C drawn on `drafts/live-rows`; the owner picked B, now in [screens/live.bl](board/screens/live.bl).

## 2026-11-25: Tasks rows use the work facts

* **Question (owner):** move the Tasks list to the shared work facts too.
* **Choice:** the Tasks row facts keep open questions, the delivery stage, run evidence that needs a look (stale, stalled, unconfirmed) and spend; attempts, task age, current run age, time in state and the agent become [work facts](controls/work-facts.md) in the approved form B. The fixed two-hour "running too long" alert is removed (no invented limits): a long task stands out against the page's open tasks (`peerFence`). A finished task shows how long ago it reached its state instead of a time in work. The row is the approved `Work facts` block; no new drawing (OR-62).

## 2026-11-25: Work facts on four more lists, time last

* **Question (owner):** bring the [work facts](controls/work-facts.md) to the Conversations, Results, Runners and Home lists, drawn first.
* **Options** (board home, `drafts/workfacts-rest.bl`): Conversations A, the waiting time as its own fact, or B, inside the question chip; Home A, the facts on the meta line, or B, in the card head.
* **Owner's answer:** "the time always on the far right, so it lines up and reads well". The rule is [OR-64](controls/work-facts.md#rules-kept-here): in every row and card the time is the rightmost fact. Conversations take A and Home takes A, each with the time last; the B options are dropped.
* **Choice per list:** Conversations: the now line and the question chip, then the agent mark, then the wait for you (the oldest open question), else the current run's time in work, else for a failing start its failed starts (`×N`) and the latest one, else the last activity; a conversation is not one task, so its runs are no `×N`. Results: the codes, project and size, then the attempts of the result's task and the agent of the run that made it, then the Created age. Runners: the resources, then which attempt of its task the run is, the agent mark (moved out of the title), then the time in work against the page's runs (the fixed two-hour alert is removed) or the duration. Home: the work facts at the right end of the meta line, no agent mark. Live and Tasks change to the same order: the agent mark before the time, the delivery stage chip before the facts, the Just now age after them, the Tasks spend before the work facts.
* **Reads:** `sessions_list` adds `waitingSince`, `runStartedAt`, `lastStartedAt` and `failedStarts`; `executions_runners` adds `attempt`; `artifact_list` adds `taskAttempts` and `harness`. Agents read them through ops and MCP.
* **Rule:** [OR-64](controls/work-facts.md#rules-kept-here), next to [OR-63](controls/work-facts.md#rules-kept-here).
* **Board (OR-62):** the approved frames are on [screens/conversations.bl](board/screens/conversations.bl), [screens/projects.bl](board/screens/projects.bl) (Results), [screens/system.bl](board/screens/system.bl) (Runners) and [screens/home.bl](board/screens/home.bl); the `Work facts` block and the Live rows frame are reordered to the time-last order.

## 2026-11-25: Lists load on scroll

* **Owner's answer:** pagination that shows some rows, then `+K` and Show less, is out; every list loads on scroll and wastes nothing. Loading row: A, three faint skeleton rows.
* **Rule:** [OR-65](patterns/infinite-list.md#rules-kept-here); [OR-30](patterns/infinite-list.md#rules-kept-here) now means a fixed preview whose title opens the full list.
* **Pattern:** [infinite list](patterns/infinite-list.md): one sentinel per list, cursor pages, one request in flight, an abort on close, a danger row with Retry, nothing after the last row.
* **Board (OR-62):** [infinite-lists.bl](https://github.com/taskqorg/fleet/blob/d899f56c/docs/design-system/board/infinite-lists.bl), 1440 and 390.
## 2026-11-25: A person's Telegram is set on the person card

* **Owner's ask:** "Телеграм id его добавь - добавь такую возможность": set a person's Telegram id in Fleet, not only through an op.
* **Choice:** one option drawn, approved as drawn ("ok"). System › People › person gets a Telegram section: a pick among the Telegram users who wrote to the bot without a link, or a typed id, with the language; once linked, the state, the user, the language, when it was linked and Unlink. The People list shows the @username. Spec: [system.md](pages/system.md#states); canvas [screens/people.bl](board/screens/people.bl).
* **Reads and writes:** `telegram_people` and `telegram_person_set`, unchanged; agents use the same ops.

## 2026-11-25: Conversations v2: results, bot threads, tool chips

* **Owner's ask:** every HAL0 turn attached `mcp-lifecycle.jsonl` files, a bot's messages all ran in one stream, and tool calls said too little in short form. "Результаты — это картинки, видео"; technical JSON hidden or in a very short form.
* **Choice:** "2 2 2" from the audit's options, drawn and approved as drawn ("ok").
  * **a2.** MCP receipts get role `diagnostic`: hidden from the thread and Results, deleted after 7 days. Pictures and videos show inline; files show as chips; no preview duplicates.
  * **b2.** One Home session per bot, with a thread per work item. Each thread continues its own native session and is idle between events; empty ticks fold into a timer row.
  * **c2.** Tool chips per kind of call.
* **Where:** spec in [session.md](pages/session.md#layout), [tool-line.md](controls/tool-line.md), [transcript-viewer.md](controls/transcript-viewer.md) and [fleet-conversations.md](../designs/fleet-conversations.md#work-item-threads); canvas [screens/session.bl](board/screens/session.bl), frame "Conversations v2".
* **Reads and writes:** `session_view` (threads, triggers), `thread_entries {session, thread}`, `message_send {thread}`, `tool_output`; agents use the same ops.

## 2026-11-26: Bot threads under the bot in the Conversations list; the phone row; Idle, not Parked

* **Owner's ask:** the thread-list column of the session view (#967) was rejected: no mockup first, the term "Parked" for threads that did nothing, the bot's name cut to "H…", the transcript squeezed beside the Conversations list. "Park" in Fleet means host stickiness only.
* **Choice:** "B, строка ок". Two options were drawn from a prod walk on real data: A, a thread picker in the session head; B, threads nested under the bot in the Conversations list. B was picked, together with the phone list row.
  * A bot's threads sit under its row: Open (with an empty-state line), Idle, and "Archived · N" (idle 7+ days, folded). The timer's quiet checks are at the foot. A thread opens with `?thread=<key>` and "HAL0 › <thread>" in the head. Without a pick, the newest Open thread shows, else Main. A bot with only its main thread lists none.
  * Phone row: the title wraps to two lines and the time stays rightmost on line 1. Line 2 holds the agent mark and the now line. The code is hidden.
* **Where:** [conversations.md](pages/conversations.md#layout), [session.md](pages/session.md#layout), [fleet-conversations.md](../designs/fleet-conversations.md#work-item-threads); canvas [screens/conversations.bl](board/screens/conversations.bl), frames "Thread list v2 under the bot" and "Phone list row".
* **Process:** walk the live pages on real data before drawing and after each deploy ([design board](README.md#design-board), steps 2 and 5).

## 2026-11-26: Pinned in the navigation

* **Question (owner):** the bots zone allowed only bots to be pinned; a PM conversation could not be. Replace it with a zone that shows or hides pinned objects, a bot or a conversation alike.
* **Options:** the zone as drawn on the board ([board](board/screens/shell.bl), frame "Shell · rail Pinned zone (approved)"): one pin per object, pinned from the object itself, rows in the person's order with Move up and Move down; or a simpler order (bots, then conversations) with no reordering.
* **Choice (owner, "как есть"):** as drawn, with Move up and Move down. The switch is the person setting `railPinned` (the `railBots` switch carried over, migration 119); the pins stay the Conversations and Library pins, so existing bot pins show at once; the order is `rowPins.nav`. Active and recent bots no longer show on their own. Recorded in [nav-rail](controls/nav-rail.md) (**Pinned**).
* **Amendment (owner, 26 Nov, "без стрелок ап-даун, порядок можно менять драгон дропом"):** no Move up / Move down anywhere pins are ordered. The rail's Pinned zone, its pinned sections and the Pinned group of lists reorder by drag, by keyboard (Space, arrows, Space) and by long press on touch, through one primitive (`useReorder`). The Pinned group of a list keeps the person's order (its `rowPins` list) instead of the list's sort. Board: `drafts/rail-pinned.bl`, frame "drag to reorder".

## 2026-11-26: Each rule has one fate

* **Question (owner):** the rule lists grew to 70 owner rules and 93 usability rules, many of them repeated in the control and pattern specs or enforced by a script, so an agent reads the same rule two or three times.
* **Choice (owner, one pass, not waves):** every rule gets one fate. *Component:* a control or pattern spec carries it in full; its text moves into that spec's "Rules kept here" (an owner rule keeps the owner's words). *Check:* an existing script enforces it; the check is named in the rule table. *Judgment:* it stays in [owner-rules.md](owner-rules.md#rules) or [principles.md](principles.md), merged with its duplicates. No rule is new, no check is added, and every id stays: [owner rules](owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](principles.md#rules-kept-elsewhere) say where each moved id lives. `node scripts/ui-context.ts <page>` prints the judgment rules a page cites in full and one line per spec or check for the rest.

## 2026-11-26: Phone bottom bar and pins without hover

* **Question:** the rule cut found two specs that disagreed with an owner rule: app-shell.md put History in the phone bottom bar, which OR-60 does not; nav-rail.md showed a pin button on hover, which OR-37 forbids in list rows.
* **Choice (owner, "Как в OR-60 (Projects)"):** the phone bottom bar is Home, Live, Conversations, Projects and More; History goes into More ([app shell](patterns/app-shell.md), [nav rail](controls/nav-rail.md)). The built bar changes later.
* **Choice (owner, "Без исключений"):** no button appears on hover in a list row or a rail item, the pin included. A pinned item shows its pin always; pinning is in the row menu and on `p` ([row pin](controls/row-pin.md), [nav rail](controls/nav-rail.md)). The built rail changes later.

## 2026-11-26: Host diagnosis without a button

* **Question:** the host detail needed what an agent reads with `host_diagnose` (queued work this host skipped, refused uploads, unconfirmed commands, the executor log); the draft had a Diagnose button.
* **Choice (owner, "Ок, без кнопки"):** a Diagnosis section between the versions and Updates, as drawn in [screens/host-diagnosis](board/screens/host-diagnosis.bl), with no button: the report refreshes by itself when the host opens and in the background; an operator never presses anything for infrastructure, and agents use `host_diagnose`. With nothing to report the section is hidden ([hosts](pages/hosts.md)).

## 2026-11-26: References everywhere, hover cards, live state

* **Question (owner):** objects are still named in many places as words or bare codes; a code says nothing until clicked, and its state is the one it had when the page loaded.
* **Choice (owner):** wherever the interface shows an object it shows a reference: kind icon, state and human code. Hovering shows that object's card, GitHub style: a short delay (about half a second) shows it, leaving hides it, a click pins it; a tap opens it on touch. This replaces the click-only choice of 2026-11-18. Each kind has its own card (a pull request card differs from a result card; an image result shows its picture in the card). Several forms: a very short one (the code, with quick copy and the card) and longer ones. From the card the person goes to the detail view. When an object's state changes, every reference already shown (conversations, lists, cards) updates, through one efficient mechanism that stays fast under load. OR-44 rewritten.
* **Decided here:** three forms, `code`, `chip` (default) and `full`, with the rule of where each goes; a 500 ms peek, 300 ms grace on leaving, no peek on keyboard focus; one batched read `entity_refs` for the live state of every rendered reference, matched against the change feed by entity key, at most one reread per second, no polling; result previews from the thumbnails and posters results already carry. Details in [entity card](controls/entity-card.md).

## 2026-11-27: Board home is a mirror; agents draw on draft copies

* **Question:** every design session drew in the shared board home through the Brilliant MCP while its pull request went from another checkout; edits stayed uncommitted there, so the home never fast-forwarded (81 commits behind main) and the owner saw stale and overlapping frames. Binding Brilliant to each worktree instead floods the app with projects (2026-11-25).
* **Options:** A. one Brilliant project (the home), tracked canvases read-only, every change drawn on a `drafts/<task>/` copy and copied into the task worktree; B. the home as a mirror plus a second drafting checkout as a second project; C. one project per session's worktree.
* **Choice (owner, "Да, вариант A"):** A. Fleet fast-forwards the home after every merge that touches the board; a dirty tracked file equal to main (or only Brilliant's reordering of the style file) is restored, anything else becomes a problem for the owner bot and drift under Changes › Agent environment ([drawing on the board](README.md#design-board)).
* **Reason:** one project the owner keeps open, no per-session projects, and a home that always shows main.

## 2026-11-27: Projects list: code tile in the project's colour, facts as glyphs

* **Question (owner):** the Projects list looked uneven: the code floated after names of different widths, and "nothing in flight" repeated on most rows. A tracker's list shows a coloured code tile, the bold name and a muted description.
* **Options:** A neutral code tile with readiness in its bar; B a tile in the project's own colour; C a plain code column. For the facts: F1 toned glyphs in fixed columns; F2 capsules on the tone background.
* **Choice (owner, "вариант B ... это общее правило", then "F2"):** B, as a general rule for projects and bots: an identity colour set in settings, else derived from the code, with an optional hatched bar ([OR-72](controls/project-tile.md#rules-kept-here)); readiness is a separate fact on the row, not inside the tile. Facts are glyphs with counts, never words ([OR-73](controls/fact-count.md#rules-kept-here)), as F2 capsules. Identity colour is the one exception to D19's hue-less palette: it never shows state.
* **Decided here:** eight colours (rose, violet, sky, teal, lime, sand, coral, slate) as tokens `id-<colour>` passing 4.5:1 on `bg` and `surface`; settings `color`, `pattern`; the tile sizes 16/20/32/40; the glyph table in `registry.ts` `factKinds`. Bots, the rail and the other screens with word facts follow in their own changes.

## 2026-11-27: Board canvases tidied

* **Question:** the board had frames on top of each other, finished drafts, a duplicate Brilliant project and canvases outside the structure the design method approved (foundations, components, patterns, screens, flows, drafts).
* **Options:** fix the layout only; or also close the extra Brilliant projects, delete finished drafts and move canvases into the structure.
* **Choice (owner, "B. Лишние проекты Brilliant, C. Старые черновики, D. Структура"):** all three. The extra Brilliant projects close; the drafts sm-a, sm-b, masters-test, design-method, host-diagnose and local-agents are deleted (archived first); `local-agents.bl` moves to [screens/local-agents](board/screens/local-agents.bl), the infinite list frame moves into [patterns](board/patterns.bl) (the decision of 2026-11-25 above linked the old `infinite-lists.bl`), and `no-hscroll.bl` goes, its rule kept in [no horizontal scroll](patterns/no-horizontal-scroll.md). Flows (ia, flow, conversation-model) follow as a separate task.
* **Reason:** one place per kind of frame, so an agent finds a frame by its canvas and nothing on the board contradicts the specs.
* **Amendment (owner, 27 Nov, via MASTER):** automatic colours must not collide (ABT and CBZ were both sand): the default is the least-used colour of its kind when a project or bot is created, stored and stable; the owner can still override it.

## 2026-11-27: Host-independent Design-First UI

The owner selected Option A in [T-FLT-144](https://fleet.ufobe.com/projects/FLT/tasks/T-FLT-144). The approved [architecture](https://fleet.ufobe.com/projects/FLT/tasks/T-FLT-147) and [adoption request](https://fleet.ufobe.com/projects/FLT/tasks/T-FLT-146) define [OR-62 and OR-69](owner-rules.md#rules). Render task-private scene options with Fleet’s actual UI library and retain the exact selected recipe and four-frame evidence in Fleet. Use the same procedure for restorations and other visible changes on Linux, Windows and Mac. Preserve the Brilliant canvases as historical references at cutover.
