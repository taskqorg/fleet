# Filter bar

**Code.** `FilterBar` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Narrows a list by project, task, experiment, host, account, agent or harness, state, kind, time range and cost. Remembers, saves and shares the view.

**Anatomy.** One line above the list: [search](search.md) field; saved filters as pinned chips on their own line (OR-12; click applies); active filter chips ("Project: dashq ×"); "Filter" button; "Clear" (quiet button, only when a filter is active); result count "84 of 1,240" in `font-mono`, right aligned. Wraps to two lines below 900 px; on phone the chips collapse into "Filters (2)".

**States.** No filter; filters active; saved filter applied (its name shows in the menu); modified saved filter ("My waiting •" plus "Save" and "Save as"); loading (count dims, list keeps old rows dimmed); error (count replaced by "Filter failed. Retry").

**Behaviour.**
* "Filter" expands an inline row under the bar, never a popup (OR-14): field buttons, then value buttons for the chosen field. Values show counts. Multi-value fields use checkboxes; time uses presets (last hour, today, 7 days) plus a range; cost uses a min and max.
* Applying a filter updates the list within 100 ms for loaded data; server queries debounce 150 ms ([principles §4](../principles.md#4-filters-and-search)).
* The full state (filters, search, sort, grouping) is written to the URL query on every change, with `replaceState`.
* The last state per list is remembered per person and restored when the list opens without a URL query.
* Saved filters are named per person; the operator can share one by copying its link.
* Group by: a menu next to "+ Filter" groups rows by one field; group headers show the count.

**Keyboard.** `f` opens the inline filter row; `Backspace` in an empty search removes the last chip; `Esc` closes the row.

**Accessibility.** Chips are buttons named "Remove filter Project: dashq". The count is in a polite live region.

**Do.** Show every active filter as a chip. **Don't.** Hide filters in a side panel; reset filters when navigating away.

**Used in.** Every list page.
