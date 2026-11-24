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

Masters on [blocks.bl](../board/blocks.bl), board *Sections*, for a catalogue in the control column ([list and detail](../patterns/list-detail.md)):
* `Catalogue row`, variants `rest`, `selected` (the `selected` fill and the accent bar on the left) and `pinned` (the pin before the state, [OR-37](../owner-rules.md)). Order per [OR-22](../owner-rules.md): state icon, title with its code ([OR-38](../owner-rules.md)), then row facts on the right ([OR-21](../owner-rules.md)). Text names: `title`, `code`, `fact`, `age`.
* `State icon`: the icon-only state of a row, one variant per state of [OR-55](../owner-rules.md) except Asleep.
* `List group`: a group header, `open` and `folded`, with its count.
