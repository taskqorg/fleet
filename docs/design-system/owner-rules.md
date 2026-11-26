# Owner rules

Read this first in every design or UI session. These rules are what the owner likes and wants to see, and they apply everywhere. They are instructions, not gates. `pnpm run check:design` warns where it can see a breach.

Each rule has one home. The [rules](#rules) here need judgment. A rule that a control or pattern spec carries in full lives in that spec, and a rule a script enforces is named with its check; [rules kept by a component or a check](#rules-kept-by-a-component-or-a-check) says where each of them lives, by its id.

Source wording: [specq owner preferences P1–P16](https://github.com/taskqorg/specq/blob/main/design/owner-preferences.md). Decisions: [D6](decisions.md).

If an owner rule conflicts with common practice, the owner rule wins. Two exceptions always hold: touch targets are at least 44 px on touch devices, and contrast meets the WCAG minimums.

## Principles card

The owner's standing principles on one screen. Every design or UI session, and every Fleet agent doing design work, reads this card first; `node scripts/ui-context.ts <page>` prints it. Each line names its detailed rule by id; the id leads to the one source. Engineering principles: [Fleet standard](../designs/fleet-standard.md#engineering-principles).

Every screen serves these three, in this order. A rule below that conflicts with them is changed, not worked around.

1. **Glance.** Open Fleet and see at once who works, how much work is in flight and what is stuck. Detail is one click away, never in the way.
2. **Work through conversation.** Every job, operator or architect, is done by talking to a session: a bot or a project PM. Sessions are the home of the product; Home lists them, and the same session is reached from the web app and from Telegram.
3. **Details on demand.** Show what a person needs to understand and to talk. Ids, metrics, tool calls, cost and qualifiers fold away and open on click.

Standing principles:

- **Useful empty pane.** An idle detail pane shows the page summary, findings or recent history, never "Pick a row" (UR-35).
- **Infinite lists.** No Show more or less, no `+N` toggles, no page buttons; one request per page; a card preview shows a fixed count and its title opens the full list (OR-65, OR-30).
- **Row facts.** Time is the rightmost fact (OR-64); work facts in the quiet form (OR-63); repeats fold into `×N` (OR-58).
- **Dark first.** Review dark before light, at 1440 and 390 px (UR-48).
- **Plain words.** No UUIDs, raw keys, host paths or JSON; only real results show as results (OR-38, OR-67).
- **No filler.** Every element answers a question the owner has; no repeated labels, no empty sections (OR-68, OR-17).
- **Brilliant first.** Every visible change is drawn on the board and approved by the owner before code; options are board frames, never text (OR-62).
- **Walk the live site.** Before drawing and after each deploy, walk the section and its neighbours on real data and compare with the canvas (OR-69).
- **No sideways scroll.** Nothing scrolls horizontally, at any width: rows wrap, tables stack at narrow widths, long values wrap or truncate with the full text on click (OR-66).
- **Anomalies to agents.** An anomaly shows passively with the bot that owns it; no Investigate or Dismiss button; Needs you holds only what only the person can give (OR-71).
- **Terms.** Inactive things are Idle, then Archived; "park" means host stickiness only; the product word is conversation (разговор) (OR-70).

A rule marked *Target* is approved and not built yet; until it is built, the current screens keep the behaviour the rule replaces.

## Rules

| Rule | What to do |
|---|---|
| **OR-1 Icon first** | If a registry icon shows the meaning, show the icon with a tooltip, not a word. Every icon has an accessible name. |
| **OR-3 Navigation anchors** | Every section, menu item and workspace has one stable registry icon next to its name. |
| **OR-4 Everyday controls first** | Work in broad strokes: map the whole system before polishing one page (OR-10). Then design lists, tables, filters and forms before pages. This is the order of work. |
| **OR-5 Apple baseline** | Apple HIG numbers fill any gap no owner rule covers. The owner rules make Fleet denser, because it is a tool for professionals. |
| **OR-6 Stable live updates** | Values update in place with fixed-width figures. A changed value tints briefly: green or red only where up or down has a known meaning, otherwise neutral. Inserted rows never move the row you are reading. |
| **OR-13 Book before pages** | Pages use only controls from the book. Menus are left-aligned with action icons, and a page is not a pile of text buttons. |
| **OR-14 Informational popups only** | Tooltips, previews, right-click menus and short context menus may float. Forms and working tools never float: they open in the work area (OR-27) or inline. Every floating layer has its own surface colour, border and shadow in both themes (OR-16). |
| **OR-18 Visual salience** | A small chip changes colour when something needs a look. Example: `2h` turns red when a task has run too long. Hover gives details, following the same rules. |
| **OR-19 Never hide what matters** | What a person needs to understand the state and to act stays visible: the state, what runs, what is stuck, what waits for them. Technical detail (ids, metrics, tool calls, cost, qualifiers) folds and opens on click (principle 3, OR-54, OR-59). |
| **OR-20 Tooltip discipline** | Add a tooltip only where it gives the operator real value, and decide this per element. Clear notation such as `2h` needs none. A tooltip describes that one element. It appears after a delay, stays small, and never covers a field being typed in. At the edge, move the hint within the visible viewport at its standard width. When the viewport itself is narrower, wrap at the available width. Essential facts on touch are reachable without hover. No large combined tooltips. Use `data-tip` with the shared `TooltipLayer`. |
| **OR-23 Nothing jumps** | An element that appears or disappears (a loading bar, a status line, a badge) never moves the content around it. Reserve its space and change only its look, or overlay it. Example: the list loading bar keeps its height and is transparent when idle. |
| **OR-28 One format per value** | Each kind of value (code, time, duration, count, money, score, state, person, agent, path, hash) has one look everywhere, listed in [formats](formats.md). Mono is for codes and numbers, never for sentences. Each screen shows first what the owner comes to it for ([what each screen answers](formats.md#what-each-screen-answers)). |
| **OR-38 Human codes, never UUIDs** | Entities show their human code (`T-FLT-42`, [D17](decisions.md#2026-11-10-d17-human-entity-codes)) everywhere. A UUID or hash is never shown; it is a tooltip with click-to-copy only. A project abbreviation can change at any time and old codes still resolve. Generated titles never contain a UUID. |
| **OR-49 One control per job** | `TabBar` (`packages/ui`) is the only tab control (no raw `role="tablist"`). `Choice` is only for 2–5 fixed options, never for project or entity lists (OR-9) and never as a single-option checkbox. `EntityCode` (OR-44) is used for every reference to an object. |
| **OR-50 Home is sessions** | *Target.* The first screen is Home: active sessions as cards (what each does now in one line, its question with answer buttons), then asleep bots with Wake, a composer that picks who to talk to (project PMs, bots, a new session from a preset), and one pulse line (working, queued, done today, blockers). Order: Waiting for you, Failed, Working, Queued, Asleep; a finished session leaves Home and stays in Conversations. A quiet Home says what happened while the person was away. |
| **OR-51 One session view** | *Target.* Every bot, PM and task run opens in the same conversation view, from Home, Conversations, Projects or a link. Beside it, a collapsible context panel shows the session's plan with done items checked, its timers (pause, edit, cancel), automations and swarm members; on a phone the panel is a sheet. |
| **OR-54 Quiet by default** | *Target.* A conversation and a session card show no metrics header, no model badge per message and no cost. Model, tokens, cost, CPU, memory and ids are under Details. Lists show cost only where cost is the question (Spend). |
| **OR-56 Red means trouble** | Red is only for failures, dead hosts and blocked work, so the eye finds trouble first. Waiting for a person is attention (amber); running is the running tone. |
| **OR-57 Every screen has one home** | Every screen belongs to one rail section, which stays highlighted while the screen is open. There are no orphan routes. *Target:* a URL is section, kind and human code (OR-38), never a UUID. |
| **OR-58 Repeats aggregate** | *Target.* Identical events or problems collapse into one item with `×N`, the cause and the newest time, never one row each. |
| **OR-59 Qualifiers in details** | *Target.* Marks such as `≈`, `≥`, "partial" and the subscription mark move to the tooltip and Details; the main view shows the rounded value. |
| **OR-61 Risky actions confirm** | *Target.* A radical action (release to production, delete or ban accounts, message real users, change keys or money, drop data) starts only from a confirmation card that names its exact scope and the way back. The card is a question (OR-53), so it shows on Home, in the session and in Telegram, and only a person allowed to approve that scope can confirm it. |
| **OR-62 Brilliant first** | Every visible change to the interface (a screen, a block, a row, a state, a layout) is drawn on the design board in Brilliant first and approved by the owner there, before code is written. The pull request that builds it links the approved canvas and commits it ([board](README.md#design-board)). A fix that restores what a canvas already shows needs no new drawing. |
| **OR-67 Only real results show** | The interface speaks in plain words: it never shows a UUID, a raw key, a host path or raw JSON. Technical artifacts (logs, tool output, payloads, internal files) are hidden or folded under Details; only real results a person asked for (images, video, documents, files) show as results. |
| **OR-68 Every element answers a question** | Each element on a screen answers a question the owner has. A word appears once per region: position, headings and icons carry repeated meaning, so a label that repeats what the position or a heading already says goes (OR-17), and a section with nothing to show is hidden, not drawn empty. |
| **OR-69 Walk the live site** | Before drawing a change and after each deploy of it, walk the live section and its neighbours on real data (dark, 1440 and 390 px: long names, many items, empty states) and compare them with the approved canvas; the comparison goes into the pull request description ([design board](README.md#design-board)). |
| **OR-70 One word per idea** | Inactive conversations, sessions and items are Idle, then Archived. "Park" means host stickiness only, never inactivity; pinning is OR-12. The product word for a session a person talks to is conversation (разговор). |

## Rules kept by a component or a check

These rules left the list above on 2026-11-26 (owner, one pass). Their ids stay; the spec named is the one source, and a check row keeps the owner's words.

| Rules | Fate | Where it lives now |
|---|---|---|
| OR-2 Compact time | component | [controls/number.md](controls/number.md#rules-kept-here) |
| OR-9 Scale-proof collections, OR-21 Hierarchy by importance, OR-22 State first in every list | component | [controls/data-list.md](controls/data-list.md#rules-kept-here) |
| OR-11 Infinite scroll, OR-33 Saved views, OR-35 Group and sort are shared, OR-37 No hover buttons, facts in the space | component | [patterns/list-view.md](patterns/list-view.md#rules-kept-here) |
| OR-12 Pin anywhere | component | [controls/row-pin.md](controls/row-pin.md#rules-kept-here) |
| OR-15 Return by icon, OR-27 Open in the work area, OR-39 Detail shows the story | component | [patterns/list-detail.md](patterns/list-detail.md#rules-kept-here) |
| OR-24 Three columns, OR-25 One scroll per column, OR-26 The whole row opens, OR-43 One shell header, OR-60 Phone first-class | component | [patterns/app-shell.md](patterns/app-shell.md#rules-kept-here) |
| OR-29 Sum first, no zeros | component | [controls/state-counters.md](controls/state-counters.md#rules-kept-here) |
| OR-30 Latest N, then the full list, OR-65 Lists load on scroll | component | [patterns/infinite-list.md](patterns/infinite-list.md#rules-kept-here) |
| OR-31 Search is an icon | component | [controls/search.md](controls/search.md#rules-kept-here) |
| OR-32 One palette | component | [controls/spotlight.md](controls/spotlight.md#rules-kept-here) |
| OR-34 Focus | component | [controls/focus-control.md](controls/focus-control.md#rules-kept-here) |
| OR-40 Needs you first, OR-71 Anomalies to agents | component | [patterns/needs-you.md](patterns/needs-you.md#rules-kept-here) |
| OR-41 Resizable columns | component | [controls/side-panel.md](controls/side-panel.md#rules-kept-here) |
| OR-42 Collapse without a jump | component | [controls/nav-rail.md](controls/nav-rail.md#rules-kept-here) |
| OR-44 Entity reference and card | component | [controls/entity-card.md](controls/entity-card.md#rules-kept-here) |
| OR-45 Diff from default | component | [patterns/definition-editor.md](patterns/definition-editor.md#rules-kept-here) |
| OR-46 One nav level, no second shell bar, OR-47 One-way causality & URL truth, OR-48 Two catalogue engines only | component | [patterns/navigation-and-columns.md](patterns/navigation-and-columns.md#rules-kept-here) |
| OR-52 Tools fold into one line | component | [controls/tool-line.md](controls/tool-line.md#rules-kept-here) |
| OR-53 Questions are actions | component | [controls/decision-block.md](controls/decision-block.md#rules-kept-here) |
| OR-55 One state vocabulary | component | [controls/state-badge.md](controls/state-badge.md#rules-kept-here) |
| OR-63 Work facts, OR-64 Time last | component | [controls/work-facts.md](controls/work-facts.md#rules-kept-here) |
| OR-66 No horizontal scroll | component | [patterns/no-horizontal-scroll.md](patterns/no-horizontal-scroll.md#rules-kept-here) |
| OR-7 No added arrows | check | `check-design.mjs` OR-7: the glyphs ↑↓▲▼⬆⬇ in markup, and an `arrow-up`, `arrow-down`, `trending-*`, `move-*` or `triangle` icon on any component but a button or menu trigger (a jump or previous/next action is not direction). The owner's words: "Never add an arrow next to a value or in a pill to show direction. Example: the new-rows pill says `12 new`." |
| OR-8 One icon set | check | `check-design.mjs` OR-8: inline SVG, emoji (pictographs other than © ® ™ and the arrows of OR-7), icon font classes and `font-family` (Material, Font Awesome, Bootstrap Icons, Codicons), and its icon colour check on `icons.json`. The owner's words: "Icons come only from the registry, which holds the TaskQ Lucide set. No inline SVG, icon fonts or emoji." |
| OR-36 Filters live in the query | check | `check-design.mjs` OR-36: a button or menu trigger labelled Filter, Filters or "… filter" (Clear filters and Remove filter are allowed); `tests/ui/ux.check.mjs`: the same label on a rendered button; `tests/ui/search-syntax.test.ts`: one query syntax in `search` and every list. The owner's words: "There is no separate Filter button. Filters are typed in search with the query syntax (OR-32)." |
| OR-10 System first | merged | into OR-4 in [rules](#rules) |
| OR-16 Distinct floating surface | merged | into OR-14 in [rules](#rules) |
| OR-17 No repeated words | merged | into OR-68 in [rules](#rules) |

## How each rule is checked

A rule without a check is kept only by reading it, so each rule names its check. `pnpm run check:design` prints all of them as warnings, and fails on one: a raw HTML control (`<button>`, `<input>`, `<table>` and the like) in `app/src/views` beyond that file's legacy count (OR-13: screens use packages/ui controls).

| Check | Rules | Where |
|---|---|---|
| Rendered page (mockups and the built app) | OR-24 (no bar over the whole window, name in the navigation, all columns present), OR-25, OR-26, OR-27, OR-28 (prose in mono, wrapped column headers), OR-41 (both dividers present), OR-42 (toggle first in the shell header, user block last, no row or icon moves on collapse, the toggle icon follows the state), OR-43 (one header right of the navigation and above the page, a section title, the search icon, one top heading, nothing pushed out sideways) | `tests/ui/layout.check.ts`. It reads each route as it loads and does not open tabs. `tests/ui/layout-break.check.mjs` injects a breach of each layout rule and fails if its check stays quiet. |
| Rendered page (mockups) | OR-17, OR-18, OR-19, OR-22, OR-29 (zero in a state chip), OR-31 (unfocused empty search field), OR-35 (Group as a word), OR-36 (Filter button), OR-37 (pin button in a list row), UR-42 (text and icon contrast in light and dark) | `tests/ui/ux.check.mjs`, `tests/ui/trace.check.mjs` |
| Source text | OR-2, OR-7 (arrow glyphs and arrow icons), OR-8 (inline SVG, emoji, icon fonts), OR-9, OR-14 (dialog markup; the entity card is the one informational dialog, OR-44), OR-15, OR-25 (viewport height outside the shell), OR-32 (the palette is the one dialog allowed), OR-38 (a short UUID or a hand-made code in markup), OR-44 (an entity code in a hand-made `id`, `mono` or `<code>` span or link), OR-35 (Group as a word), OR-36 (Filter button or menu), OR-45 (`override` attention badge), OR-46 (`<nav class="sub-nav">`), OR-48 (Control Column `vl-wrap` and `"New …"` in `page-h`), OR-49 (raw `role="tablist"` outside `tables.tsx`), OR-65 (a Show more, Show less, Load more, Older, Newer or `+K more` button label) | `RULES` in `packages/ui/scripts/check-design.mjs` |
| Rendered page (built app) | OR-29 (Overview counters: no zero chip, total first, no host lower bound), OR-30 and OR-40 (Needs you shows 3), OR-39 (no empty totals in the task detail), OR-38 (entity codes never wrap; no hash or UUID as a visible name in the task detail) | `tests/ui/app.check.ts` |
| Built app in a browser | OR-44 (a 500 ms hover shows the card and leaving hides it, a click pins it and never opens the row, a tap opens it on touch; the reference shows the live state and a pull request merging updates it in an open conversation without a reload; state and title shown, no UUID; Copy code and Copy UUID; Escape, the code again and a click outside close it; Enter moves focus to Open and Escape returns it to the code; Open goes to the task; a code in an agent message opens the same card); the card's read and grants per kind | A live check on the deployed site; the reviewer; `go/coordinator/entity_card_test.go` |
| Built app in a browser | OR-32 (Cmd+K opens the palette with focus in the field; the empty palette shows the focus presets and terms; sections, tasks and a conversation through `search`; Enter opens, Escape closes; query, results and highlighted row kept after closing and after navigation; "All N" opens the list with the query) | A live check on the deployed site; the reviewer |
| Built app in a browser | OR-43 (the section action sits right after the title: New conversation opens `/conversations/new` in the work area, OR-27), OR-17 and OR-19 on the presets (what a preset sets in words, no raw profile ids), nothing pushed out sideways at 390 px | A live check on the deployed site; the reviewer |
| Built app in a browser | OR-34, OR-42 (navigation counters: the server's count, one number without a focus, `in·outside` with one and its words, collapsed only the in-focus badge with no icon moving, the counter view chosen and stopped from the saved views, live) | A live check on the deployed site; the reviewer |
| Built app in a browser | OR-41 and OR-42 (side panels: the context toggle after search and the navigation toggle first stay in place; closing and reopening the context panel at 1440 px shows the column, never a sheet; open or closed and the width of the context panel and the navigation are stored on the server and kept after a reload, another session and Home; a double-click resets; at 390 px the toggle opens the sheet and stores nothing) | A live check on the deployed site; the reviewer |
| Built app in a browser | OR-31, OR-34, OR-43 (the header shows the section title; its search icon opens the palette; a set focus is toned unlike All projects; an ignored project leaves the list and the picker, stays hidden with All projects and is restored; the outside-focus indicator opens the Overview without the focus) | A live check on the deployed site; the reviewer |
| Built app in a browser | OR-20 (hint width at either edge), OR-23 (a transcript copy icon shows on hover or focus without moving the row; always on touch), OR-25 (the conversation header and long result names fit at 1440 and 390 px), OR-27 and OR-43 (shared narrow gutter and hit targets inside the shell header), OR-38 (a conversation title never holds a UUID: `session_rename` refuses one and the title model's answer loses it) | A live check on the deployed site; the reviewer |
| Unit test and live check (search) | OR-32 (code jump with old codes, questions, conversations, results, agents and pipelines; the focus first in each kind; "All N" counts equal to the list for the same query; ignored projects left out of every kind and count; definitions only for architects), the query syntax of OR-36 (one syntax in `search`, `task_page`, `sessions_list`, `artifact_list` and lists filtered in the browser) | `tests/ui/search-syntax.test.ts`; a live check on the deployed site; the reviewer |
| Live check | OR-33, OR-34, OR-41 and OR-42 (widths, the collapsed navigation and the collapsed list groups stored on the server per person, never in the browser; agents write them; focus and ignored projects filter lists that name no project, ignored ones as the server-side exclusion of each list operation; an ignored project leaves the focus and the person's `search`; system presets fixed), OR-35 (sort, recently changed first), OR-36 (query terms), OR-37 (row facts in the list page; hovering a row moves no fact; Shift-click enters selection mode) | A live check on the deployed site; the reviewer |
| Rendered page (built app and deployed site) | OR-66 (no element overflows sideways at 1440, 1100, 768 and 390 px: the page, a scroller whose content is wider, content past the viewport; each case is a design report line) | `tests/ui/hscroll.ts` from `tests/ui/app.check.ts`; `scripts/hscroll.ts` walks the deployed site |
| Unit tests | OR-63 (the outlier fence: `×500` among ordinary rows stands out, `×2` and `×3` do not; no fence below three peers) | `tests/ui/work-facts.test.ts`; the rest by reading |
| Browser (written, no script yet) | OR-6 (from UR-11: note the first visible row's `top`, insert rows through the feed, compare), OR-51 (from UR-56: open the same session from Home, Conversations, a project and a task; expect the same view and URL), OR-61 (from UR-76: review against the OR-61 list; the action's first click shows the card, not the result), OR-28 (from UR-85: review) | The reviewer |
| Reading only | Every rule in [Rules](#rules) and the parts of component rules no check above covers (until each gets its check); OR-62 (the pull request links its approved canvas) | The reviewer and the owner. |

A new rule is added together with its check, or with a row in "Reading only". A rule a component spec carries goes into that spec's "Rules kept here" and gets a row in the table above.
