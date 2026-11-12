# Filter bar

**Code.** `FilterBar` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Narrows a list by project, task, experiment, host, account, agent or harness, state, kind, time range and cost. Remembers, saves and shares the view.

**Anatomy.** First line: [search](search.md) as an icon (OR-31); chips of filters that came with a link ("task: 3f…  ×"); "Clear" (quiet, only with chips); the count "84 of 1,240" in `font-mono`, right aligned, shown only while a filter narrows the list (the Σ counter already gives the total, OR-17). Second line: saved views, [state counters](state-counters.md), Group and Sort.

**Filters are typed (OR-36).** There is no Filter button. A filter is a `key:value` word in the search: `agent:claude host:mac state:failed project:fleet` on Tasks, `media:image since:7d` on Results; the rest is free text. The server reads the same syntax (`task_page` filter.search), so an agent's search and a person's give the same list. A key the list does not know stays text.

**Views (OR-33).** `Presets` shows the system presets first (not removable; Tasks "Running" is every state that is not terminal), then the person's own views with a remove `×`, then "Save view" (an icon; it opens an inline name field, OR-14). A view stores state selection, search, project, grouping and sort. Views are stored on the server per person (`person_settings_set` `views`), so they follow the person and agents read and write them.

**Group and Sort (OR-35).** `GroupControl`: the Group registry icon with a tooltip, then the choices. `SortControl`: a direction toggle (sort icons) and the fields; the value is the field, with `-` for descending.

**Behaviour.**
* The full state (filters, search, sort, grouping) is in the URL query; searching replaces the history entry while typing.
* Saved views can be shared by copying the link.

**Keyboard.** `Esc` in the search clears it and leaves the field.

**Accessibility.** Chip buttons are named "Remove filter task: …". The count is in a polite live region.

**Do.** Show the filters in the query text, so they can be read and edited in one place. **Don't.** Hide filters in a side panel; reset filters when navigating away.

**Used in.** Every list page.
