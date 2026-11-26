# Data list

**Code.** `VirtualList` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Shows long, growing lists: tasks, executions, events, results, hosts. The main control of every workspace.

**Anatomy.** Sticky header row with column heads (sortable ones show ▲▼); body of fixed-height rows; optional group headers; new-rows pill ("12 new", no arrow, OR-7); the row menu (OR-12, OR-37) with Pin; footer count is in the [filter bar](filter-bar.md). A task row (OR-21, OR-22), from left to right:
* on a pinned row only, the pin (`RowPin`, OR-37) before the state: a click unpins;
* the state as an icon-only [state badge](state-badge.md), with name and reason in its tooltip (B1);
* the title, which takes the free width at medium weight, with the ID (`font-mono`, `fg-muted`) right after it;
* a compact service zone on the right: the [harness mark](harness-counts.md) while the task runs, the [delivery stage chip](stage-chip.md), the [age chip](number.md), the cost ([number](number.md), right aligned). No other buttons in the row (OR-37).

Every list follows the same order: state first, then name with ID, then the service zone (OR-22, [D12](../decisions.md)). The traceability check in `check:design` (after a row's pin or checkbox) warns about any list row whose first cell is not a state badge.

**Grouping.** Lists can group rows. Each group header row shows the group's icon, name and count. Groups collapse; completed and cancelled start collapsed, and the person's own collapses are remembered ([list view](../patterns/list-view.md)). `endKey` tells the list that rows were loaded behind its end without new items (into a collapsed group), so `onEnd` fires again while the end is in view. Anchor keeping works across groups.

**Sizes.** Row `size-row` (32 px), compact `size-row-compact` per person. A catalogue in the control column uses the same one-line rows ([list and detail](../patterns/list-detail.md)); touch uses `size-row-phone` (`catalogueRow()`).

**Opening.** A row that opens an item has one `row-link` on its title; a click anywhere else on the row follows that link (OR-26). Buttons, checkboxes and other links in the row keep their own action.

**States.** Row: rest, hover, selected (`selected` + 3 px `accent` left bar), focused (ring inside the row), changed (fade from `attention-soft` to none over `motion-highlight`), problem (`attention-soft` fill). List: loading first page (skeleton rows), loading more (thin row at the end after 300 ms), empty, error, stale ([status states](status-states.md)).

**Behaviour.** Follows [principles §3](../principles.md#3-lists-and-scrolling):
* Virtualised: renders visible rows plus 10 rows above and below. Fixed row height; scroll height = row count × row height.
* Anchor keeping: before applying an insert above the viewport, record the first visible row ID and its offset; after rendering, set `scrollTop` so that row keeps its offset. Applied in the same frame, so no visible jump.
* Following: at top of a newest-first list, new rows appear and the view stays at top. Scrolled away: view stays; pill "N new" appears at the top centre; activating it scrolls to top and resumes following.
* Live row updates change cells in place. Re-sorting moves wait until the pointer leaves the list or the person scrolls.
* Pinned rows show first (OR-12).
* Selection, scroll position and sort are kept per list and restored on return.
* Columns: resizable, the title column flexes. Text overflows with ellipsis and full text on hover.
* Selection mode (OR-37) when the list has bulk actions: a checkbox column left of the state icon; `SelectBar` takes its own line under group and sort while it is on ([list view](../patterns/list-view.md)).

**Keyboard.** `j`/`k` or `↓`/`↑` move selection; `Home`/`End` first/last; `Enter` opens; `x` toggles selection; `Shift+↓` extends.

**Accessibility.** `role="grid"` with `aria-rowcount` (total) and `aria-rowindex` per rendered row; one tab stop with roving focus. Sort state in `aria-sort`. Live changes announced in batches by a polite region: "3 tasks changed".

**Do.** Keep one line per row in tables, and two in a catalogue. **Don't.** Card grids for operational lists; infinite "load more" buttons; animating rows into place.

**Implementation.** Bottom-following lists retain the tail through measured row-height and viewport-size changes while the reader is at the end. Readers above the tail retain their anchor and receive the new-entry pill. Rendering window from TanStack Virtual core; anchor keeping and the pill in `VirtualList` (D4). Any collection longer than a screen uses it (OR-9). Threads also load upward: `onStart` fires once per list length when the first rows are in view, rows loaded above keep the row being read in place (OR-23) and do not count in the pill, and `handle.current.scrollToKey(key, align)` jumps to a row (search hits).

**Used in.** Overview, Tasks, Results, Hosts, Laboratory trials, History.

## Prepared windows

`VirtualListHandle.getAnchor()` returns the first visible key and `offsetPx` from the viewport top. `restoreAnchor(key, offsetPx)` preserves that position after layout; an empty window resets to zero. Prepared windows capture the full server sort tuple, preferring an opaque item position when supplied. Refresh uses `around`; a deleted key restores the server's nearest surviving key at the top. Epoch replacement discards cursors and revalidates the captured position. Continuous loading, filtered count, search and start/end jumps share the same control and scroller.

## Board

Masters on [blocks.bl](../board/blocks.bl), board *Sections*, for a catalogue in the control column ([list and detail](../patterns/list-detail.md)). Built: the catalogue row is the leaf row of [`CatalogueTree`](catalogue-tree.md) and the list group its folder row; demo: [book](../book/index.html#catalogue-row).
* `Catalogue row`, variants `rest`, `selected` (the `selected` fill and the accent bar on the left) and `pinned` (the pin before the state, [OR-37](../patterns/list-view.md#rules-kept-here)). Order per [OR-22](#rules-kept-here): state icon, title with its code ([OR-38](../owner-rules.md)), then row facts on the right ([OR-21](#rules-kept-here)). Text names: `title`, `code`, `fact`, `age`.
* `State icon`: the icon-only state of a row, one variant per state of [OR-55](state-badge.md#rules-kept-here) except Asleep.
* `List group`: a group header, `open` and `folded`, with its count.

## Table row

Built: `DataTable` with `TableRow` (`selected`, the whole row opens) and `TableName` (state icon, then the name as the row's button for the keyboard); demo: [book](../book/index.html#table-row). A list with column heads, for figures compared across rows: the Lab leaderboard, the Spend breakdown, a project's Delivery and Results facets. Master `Table row` on [blocks.bl](../board/blocks.bl), board *Data*, variants `header`, `row` and `selected`. The name column takes the free width ([OR-21](#rules-kept-here)); figures are right aligned in mono with fixed-width digits ([OR-6](../owner-rules.md), [OR-28](../owner-rules.md)); a row whose item has a state starts with the [state icon](#board) ([OR-22](#rules-kept-here)). The whole row opens its item ([OR-26](../patterns/app-shell.md#rules-kept-here)). Text names: `name`, `c1`, `c2`, `c3`. A table never scrolls sideways: at narrow widths its rows become stacked rows, and long values wrap or truncate with the full text on click ([OR-66](../patterns/no-horizontal-scroll.md#rules-kept-here)).

## Reflow table

Built: `ReflowTable` with `ReflowRow` (`packages/ui`, `tables.tsx`); used by System › Access (Servers) and a project's Access facet. Where an approved frame draws two-line rows instead of the stacked rows `DataTable` gives ([OR-66](../patterns/no-horizontal-scroll.md#rules-kept-here)): while its frame is wider than 38rem it shows columns (`cols` is the grid tracks, one per header; a container query on its own width, so a narrow work area reflows like a phone); narrower, each row is two lines: the name with its state on the right, then the `meta` cells joined by `·` with the `time` cell last ([OR-64](work-facts.md#rules-kept-here)). An empty meta cell is `—` in columns and left out of the line. The whole row opens it ([OR-26](../patterns/app-shell.md#rules-kept-here)); while selected, its `detail` shows under it at full width. Board: [projects.bl](../board/screens/projects.bl), frame "Project › Access · phone 390".

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **OR-9 Scale-proof collections.** Any collection longer than a screen uses `VirtualList` or a searchable scope control. Never use a plain long list or `<select>`.
* **OR-21 Hierarchy by importance.** A row's main content (for a task, its title) gets the most space and weight; never squeeze it to two words beside equal-width columns. Service fields (id, state icon, agent, time, counters, actions) are compact and grouped in one zone on the right.
* **OR-22 State first in every list.** In every list (tasks, results, hosts, executions, artifacts, variants and others) the leftmost column is the item's state icon; it marks both the row and its state. The item's ID sits directly after its name, as a pair. A task row also shows lifecycle facts that matter: the harness mark while it runs, and the delivery stage (PR open, checks, merging, merged) as a compact chip. Service fields stay in the right zone.
* **UR-1.** A list row is one line, about 32 px tall with a fine pointer and 44 px on touch, and a desktop list shows at least 15 rows without scrolling. *Check:* Browser: open a list at 1440 × 900 and count visible `[role=row]` elements; measure one row's height.
* **UR-10.** (merged with OR-9) A list that can be longer than a screen renders only visible rows plus a buffer ([OR-9](#rules-kept-here)). *Check:* Browser: with 500 rows loaded, the DOM holds fewer than 100 row elements.
* **UR-12.** At the top of a newest-first list, or at the bottom of a live transcript, new rows appear in place and the view follows; scrolled away, the view stays still and a pill says `12 new`. *Check:* Browser: scroll away, wait for a new row, expect the pill; activate it, expect the newest row visible.
* **UR-13.** A row whose state changes updates its cells in place; a move caused by the sort waits until the pointer leaves the list or the person scrolls. *Check:* Browser: hover a row, change its state, expect the same row under the pointer.
* **UR-22.** `j` / `k` or the arrow keys move the row selection, `Enter` opens, and `Esc` closes the opened item and returns focus to its row. *Check:* Browser: open a row with `Enter`, press `Esc`, expect focus on that row.
* **UR-44.** Lists use table or grid semantics, and virtualised rows keep `aria-rowcount` and `aria-rowindex`. *Check:* Browser: read the attributes on a long list.
* **UR-45.** Live updates are announced politely and in batches ("3 tasks changed"), never row by row. *Check:* Browser: count `aria-live` announcements during a burst of updates.
