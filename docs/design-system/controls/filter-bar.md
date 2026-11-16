# Filter bar

**Code.** `FilterBar` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Narrows a list by project, task, experiment, host, account, agent or harness, state, kind, time range and cost. Remembers, saves and shares the view.

**Anatomy.** First line: [search](search.md) as an icon (OR-31); chips of filters that came with a link ("task: 3f…  ×"); "Clear" (quiet, only with chips); the count "84 of 1,240" in `font-mono`, right aligned, shown only while a filter narrows the list (the Σ counter already gives the total, OR-17). Saved views follow the search on the first line. Second line: the [state counters](state-counters.md) quick filter (`counters`), alone. Third line: Group and Sort (`tools`).

**Filters are typed (OR-36).** There is no Filter button. A filter is a `key:value` word in the search: `agent:claude host:mac state:failed project:fleet` on Tasks, `media:image since:7d` on Results; the rest is free text. The server reads the same syntax (`task_page` filter.search), so an agent's search and a person's give the same list. A key the list does not know stays text.

**Views (OR-33).** `Presets` shows the system presets first (not removable; Tasks "Running" is every state that is not terminal), then the person's own views with a remove `×`, then "Save view" (an icon; it opens an inline name field, OR-14). A view stores state selection, search, project, grouping and sort. Views are stored on the server per person (`person_settings_set` `views`), so they follow the person and agents read and write them.

**Group and Sort (OR-35).** `GroupControl`: one chip, the Group registry icon and the current grouping; a click opens the choices in a short menu (the line never changes), and the cross removes the grouping in one click. With no grouping the chip is the icon alone. `SortControl`: one chip led by the direction icon, which flips the direction on click. The icon's bars draw the values as the rows show them, top to bottom. Each field says what its rows show: `age` (the default, a moment shown as `4m`, `3d`), where Newest first is the narrow-to-wide icon and Oldest first the wide-to-narrow one; or `value` (a number or text), where descending is wide-to-narrow. Then comes the field; a click on the field opens a short menu of fields and directions. Direction names follow the field (Newest first, A to Z). Sorting by state then by time is Group: State with Sort: Changed. The value is the field, with `-` for descending.

**Selection.** `SelectToggle` (a square) starts the tools line, above the checkbox column it opens. In selection mode `SelectBar` takes a line of its own under the tools: select all, leave (cross), the count, and the actions on the right. Group and sort stay where they are.

**Behaviour.**
* The full state (filters, search, sort, grouping) is in the URL query; searching replaces the history entry while typing.
* Saved views can be shared by copying the link.

**Keyboard.** `Esc` in the search clears it and leaves the field.

**Accessibility.** Chip buttons are named "Remove filter task: …". The count is in a polite live region.

**Do.** Show the filters in the query text, so they can be read and edited in one place. **Don't.** Hide filters in a side panel; reset filters when navigating away.

**Used in.** Every list page.
