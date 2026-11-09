# Data list

**Code.** `VirtualList` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Shows long, growing lists: tasks, executions, events, results, hosts. The main control of every workspace.

**Anatomy.** Sticky header row with column heads (sortable ones show ▲▼); body of fixed-height rows; optional group headers; new-rows pill ("12 new", no arrow, OR-7); pin button per row (OR-12, visible on hover, focus and touch); footer count is in the [filter bar](filter-bar.md). A task row (OR-21, OR-22), from left to right:
* the state as an icon-only [state badge](state-badge.md), with name and reason in its tooltip (B1);
* the title, which takes the free width at medium weight, with the ID (`font-mono`, `fg-muted`) right after it;
* a compact service zone on the right: the [harness mark](harness-counts.md) while the task runs, the [delivery stage chip](stage-chip.md), the [age chip](number.md), the cost ([number](number.md), right aligned), and the pin button (always visible, B2).

Every list follows the same order: state first, then name with ID, then the service zone (OR-22, [D12](../decisions.md)). The traceability check in `check:design` warns about any list row whose first cell is not a state badge.

**Grouping.** Lists can group rows. Each group header row shows the group's icon, name and count. Groups collapse; completed and cancelled start collapsed. Anchor keeping works across groups.

**Sizes.** Row `size-row` (32 px), compact `size-row-compact` per person, phone `size-row-phone` with two lines: title on line 1, state and one key value on line 2.

**States.** Row: rest, hover, selected (`selected` + 3 px `accent` left bar), focused (ring inside the row), changed (fade from `attention-soft` to none over `motion-highlight`), problem (`attention-soft` fill). List: loading first page (skeleton rows), loading more (thin row at the end after 300 ms), empty, error, stale ([status states](status-states.md)).

**Behaviour.** Follows [principles §3](../principles.md#3-lists-and-scrolling):
* Virtualised: renders visible rows plus 10 rows above and below. Fixed row height; scroll height = row count × row height.
* Anchor keeping: before applying an insert above the viewport, record the first visible row ID and its offset; after rendering, set `scrollTop` so that row keeps its offset. Applied in the same frame, so no visible jump.
* Following: at top of a newest-first list, new rows appear and the view stays at top. Scrolled away: view stays; pill "N new" appears at the top centre; activating it scrolls to top and resumes following.
* Live row updates change cells in place. Re-sorting moves wait until the pointer leaves the list or the person scrolls.
* Pinned rows show first (OR-12).
* Selection, scroll position and sort are kept per list and restored on return.
* Columns: resizable, the title column flexes. Text overflows with ellipsis and full text on hover.
* Multi-select with `x` or checkbox column when the list has bulk actions; a bulk bar replaces the filter bar actions while rows are selected.

**Keyboard.** `j`/`k` or `↓`/`↑` move selection; `Home`/`End` first/last; `Enter` opens; `x` toggles selection; `Shift+↓` extends.

**Accessibility.** `role="grid"` with `aria-rowcount` (total) and `aria-rowindex` per rendered row; one tab stop with roving focus. Sort state in `aria-sort`. Live changes announced in batches by a polite region: "3 tasks changed".

**Do.** Keep one line per row on desktop. **Don't.** Card grids for operational lists; infinite "load more" buttons; animating rows into place.

**Implementation.** Rendering window from TanStack Virtual core; anchor keeping and the pill in `VirtualList` (D4). Any collection longer than a screen uses it (OR-9).

**Used in.** Overview, Tasks, Results, Hosts, Laboratory trials, History.
