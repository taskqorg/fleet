# Owner rules

Read this first in every design or UI session. These rules are what the owner likes and wants to see, and they apply everywhere. They are instructions, not gates. `npm run check:design` warns where it can see a breach.

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
| **OR-14 Informational popups only** | Tooltips, previews, right-click menus and short context menus may float. Forms and working tools never float: they open inline, in a side panel within the layout, or on their own route. |
| **OR-15 Return by icon** | A return action is an icon with a tooltip, and only where browser Back does not already serve. |
| **OR-16 Distinct floating surface** | Every floating layer has its own surface colour, border and shadow in both themes. |
| **OR-17 No repeated words** | A word appears once per region. Let position, headings and icons carry repeated meaning. |
| **OR-18 Visual salience** | A small chip changes colour when something needs a look. Example: `2h` turns red when a task has run too long. Hover gives details, following the same rules. |
| **OR-19 Never hide what matters** | Use the fewest elements, but important information that can be shown stays visible. |
| **OR-20 Tooltip discipline** | Add a tooltip only where it gives the operator real value, and decide this per element. Clear notation such as `2h` needs none. A tooltip describes that one element. It appears after a delay, stays small, and never covers a field being typed in. No large combined tooltips. Use `data-tip` with the shared `TooltipLayer`. |
| **OR-21 Hierarchy by importance** | A row's main content (for a task, its title) gets the most space and weight; never squeeze it to two words beside equal-width columns. Service fields (id, state icon, agent, time, counters, actions) are compact and grouped in one zone on the right. |
| **OR-22 State first in every list** | In every list (tasks, results, hosts, executions, artifacts, variants and others) the leftmost column is the item's state icon; it marks both the row and its state. The item's ID sits directly after its name, as a pair. A task row also shows lifecycle facts that matter: the harness mark while it runs, and the delivery stage (PR open, checks, merging, merged) as a compact chip. Service fields stay in the right zone. |
