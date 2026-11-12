# Owner rules

Read this first in every design or UI session. These rules are what the owner likes and wants to see, and they apply everywhere. They are instructions, not gates. `pnpm run check:design` warns where it can see a breach.

Source wording: [specq owner preferences P1–P16](https://github.com/taskqorg/specq/blob/main/design/owner-preferences.md). Decisions: [D6](decisions.md).

If an owner rule conflicts with common practice, the owner rule wins. Two exceptions always hold: touch targets are at least 44 px on touch devices, and contrast meets the WCAG minimums.

| Rule | What to do |
|---|---|
| **OR-1 Icon first** | If a registry icon shows the meaning, show the icon with a tooltip, not a word. Every icon has an accessible name. |
| **OR-2 Compact time** | Write times as `4m`, `2h`, `3d`, `12 Nov`, `12 Nov 2025`. The exact time is on hover, and the long form ("4 minutes ago") is the accessible name. Always use `format` from `packages/ui`. |
| **OR-3 Navigation anchors** | Every section, menu item and workspace has one stable registry icon next to its name. |
| **OR-4 Everyday controls first** | Design lists, tables, filters and forms before pages. This is the order of work. |
| **OR-5 Apple baseline** | Apple HIG numbers fill any gap no owner rule covers. The owner rules make Fleet denser, because it is a tool for professionals. |
| **OR-6 Stable live updates** | Values update in place with fixed-width figures. A changed value tints briefly: green or red only where up or down has a known meaning, otherwise neutral. Inserted rows never move the row you are reading. |
| **OR-7 No added arrows** | Never add an arrow next to a value or in a pill to show direction. Example: the new-rows pill says `12 new`. |
| **OR-8 One icon set** | Icons come only from the registry, which holds the TaskQ Lucide set. No inline SVG, icon fonts or emoji. |
| **OR-9 Scale-proof collections** | Any collection longer than a screen uses `VirtualList` or a searchable scope control. Never use a plain long list or `<select>`. |
| **OR-10 System first** | Work in broad strokes: map the whole system before polishing one page. |
| **OR-11 Infinite scroll** | Collections load continuously with filters. Show a count ("84 of 1,240") and jump to start or end. No pagination unless a decision allows it. |
| **OR-12 Pin anywhere** | Every catalogue, list and menu lets the person pin items. Pinned items show first in that same place. There is no separate Favourites section, and the pin is an icon. |
| **OR-13 Book before pages** | Pages use only controls from the book. Menus are left-aligned with action icons, and a page is not a pile of text buttons. |
| **OR-14 Informational popups only** | Tooltips, previews, right-click menus and short context menus may float. Forms and working tools never float: they open in the work area (OR-27) or inline. |
| **OR-15 Return by icon** | A return action is an icon with a tooltip, and only where browser Back does not already serve. |
| **OR-16 Distinct floating surface** | Every floating layer has its own surface colour, border and shadow in both themes. |
| **OR-17 No repeated words** | A word appears once per region. Let position, headings and icons carry repeated meaning. |
| **OR-18 Visual salience** | A small chip changes colour when something needs a look. Example: `2h` turns red when a task has run too long. Hover gives details, following the same rules. |
| **OR-19 Never hide what matters** | Use the fewest elements, but important information that can be shown stays visible. |
| **OR-20 Tooltip discipline** | Add a tooltip only where it gives the operator real value, and decide this per element. Clear notation such as `2h` needs none. A tooltip describes that one element. It appears after a delay, stays small, and never covers a field being typed in. No large combined tooltips. Use `data-tip` with the shared `TooltipLayer`. |
| **OR-21 Hierarchy by importance** | A row's main content (for a task, its title) gets the most space and weight; never squeeze it to two words beside equal-width columns. Service fields (id, state icon, agent, time, counters, actions) are compact and grouped in one zone on the right. |
| **OR-22 State first in every list** | In every list (tasks, results, hosts, executions, artifacts, variants and others) the leftmost column is the item's state icon; it marks both the row and its state. The item's ID sits directly after its name, as a pair. A task row also shows lifecycle facts that matter: the harness mark while it runs, and the delivery stage (PR open, checks, merging, merged) as a compact chip. Service fields stay in the right zone. |
| **OR-23 Nothing jumps** | An element that appears or disappears (a loading bar, a status line, a badge) never moves the content around it. Reserve its space and change only its look, or overlay it. Example: the list loading bar keeps its height and is transparent when idle. |
| **OR-24 Three columns, no top bar** | Every screen is three columns: navigation (collapses to icons), the control column (the section's catalogue with search, filters and counters) and the work area (the opened item, or the section's page). A section with no catalogue (Overview) has no control column. There is no top bar; the product name and the "need you" count sit in the navigation. |
| **OR-25 One scroll per column** | A column has at most one vertical scroller, and it is a plain browser scroller. A scroller never sits inside another one, and the page itself does not scroll. Only code and wide tables scroll sideways, inside their own frame. |
| **OR-26 The whole row opens** | A click anywhere on a list row opens the item. Controls inside the row (pin, checkbox, menu) act on their own. Never make the person aim at the title. |
| **OR-27 Open in the work area** | A task, conversation, result, host or any other opened item fills the work area. It is never a floating, sticky or overlaid panel, at any width. |
| **OR-28 One format per value** | Each kind of value (code, time, duration, count, money, score, state, person, agent, path, hash) has one look everywhere, listed in [formats](formats.md). Mono is for codes and numbers, never for sentences. Each screen shows first what the owner comes to it for ([what each screen answers](formats.md#what-each-screen-answers)). |
| **OR-29 Sum first, no zeros** | A counter block starts with the bold total (`Σ 12`), then the breakdown. A zero chip is hidden, and a block whose values are all zero is hidden. A status quick-filter chip with 0 shows only its icon, never `0`. |
| **OR-30 Latest N, then +K** | A long feed shows its N latest items and a `+K` expander. A block never grows past the page; it never pushes the rest of the column out of view. |
| **OR-31 Search is an icon** | Search in lists and in the navigation is an icon. The field shows only while focused or not empty. |
| **OR-32 One palette** | The navigation search is a Spotlight palette on Cmd+K. It is the only floating working tool, an exception to OR-14. It jumps by entity code, tasks by title, sections, projects and hosts; each kind is one provider, so new kinds plug in. Its query syntax (`agent:claude host:mac`) is the same as list search. |
| **OR-33 Saved views** | Every list uses one shared saved-views control: system presets (Tasks "Running" is every non-terminal state) and personal views. Views are stored on the server per person, and agents read and write them through MCP. |
| **OR-34 Focus** | One global project multi-select applies across the whole app and changes in one or two clicks. Focus presets are system or saved custom ones, stored on the server per person. |
| **OR-35 Group and sort are shared** | Group and Sort (field and direction) are shared list controls and part of a saved view. Group is a registry icon with a tooltip, not a word. Tasks sort by most recently changed first by default. |
| **OR-36 Filters live in the query** | There is no separate Filter button. Filters are typed in search with the query syntax (OR-32). |
| **OR-37 Affordances on hover, facts in the space** | Row affordances (pin, checkbox) show only on hover or when set. The freed space shows live row facts in short form: time since taken into work, number of executions, time in the current state, agent icon, host icon. Row facts are one shared pattern that other lists reuse. |
| **OR-38 Human codes, never UUIDs** | Entities show their human code (`T-FLT-42`, [D17](decisions.md#2026-11-10-d17-human-entity-codes)) everywhere. A UUID or hash is never shown; it is a tooltip with click-to-copy only. A project abbreviation can change at any time and old codes still resolve. Generated titles never contain a UUID. |
| **OR-39 Detail shows the story** | A detail page shows the statement, lifecycle, result and decision. Technical ids appear only where truly needed, as an icon with tooltip and click-to-copy. No large empty fields. |
| **OR-40 Needs you first** | Overview "Needs you" sorts by urgency, then recency, and shows the latest 3 plus `+K` (OR-30). Project and host rows follow OR-29. |
| **OR-41 Resizable columns** | The navigation and the control column resize by dragging their divider. Widths are stored on the server per person; a double-click on the divider resets the width. |
| **OR-42 Collapse without a jump** | The navigation collapse toggle sits at the top of the column right after the navigation (the control column, or the work area on Overview). Collapsed and expanded navigation map 1:1: same icons, same heights, only labels change (OR-23). The navigation ends with the current user block (avatar and name, avatar only when collapsed); a click opens a context menu with Logout (Cloudflare Access logout). |

## How each rule is checked

A rule without a check is kept only by reading it, so each rule names its check. `pnpm run check:design` prints all of them as warnings.

| Check | Rules | Where |
|---|---|---|
| Rendered page (mockups and the built app) | OR-24 (no top bar, name in the navigation, all columns present), OR-25, OR-26, OR-27, OR-28 (prose in mono, wrapped column headers) | `tests/ui/layout.check.ts`. It reads each route as it loads and does not open tabs. `tests/ui/layout-break.check.mjs` injects a breach of each layout rule and fails if its check stays quiet. |
| Rendered page (mockups) | OR-17, OR-18, OR-19, OR-22 | `tests/ui/ux.check.mjs`, `tests/ui/trace.check.mjs` |
| Source text | OR-2, OR-7, OR-8, OR-9, OR-14 (dialog markup), OR-15, OR-25 (viewport height outside the shell), OR-38 (a short UUID or a hand-made code in markup; codes issued and resolved: `tests/go/codes.test.ts`) | `RULES` in `packages/ui/scripts/check-design.mjs` |
| Rendered page (built app) | OR-29 (Overview counters: no zero chip, total first, no host lower bound), OR-30 and OR-40 (Needs you shows 3 and `+K`), OR-39 (no empty totals in the task detail) | `tests/ui/app.check.ts` |
| Reading only | OR-1, OR-3 to OR-6, OR-10 to OR-13, OR-16, OR-20, OR-21, OR-23, the rest of OR-28, the rest of OR-29, OR-30, OR-39 and OR-40, OR-31 to OR-37, OR-41, OR-42 (until each gets its check) | The reviewer and the owner. |

A new rule is added together with its check, or with a row in "Reading only".
