# List view

**Code.** `TaskListView`. It is a pattern because results, executions and trials reuse the same composition with their own columns.

**Job.** Find, triage and act on many items fast, with the view each person prefers (D9, OR-4, OR-9, OR-11, OR-12).

**Composition, from top to bottom:**
1. **[Filter bar](../controls/filter-bar.md), first line:** search as an icon (OR-31) that takes the query syntax (OR-36), chips of filters that came with a link, Clear, and the count "84 of 1,240".
2. **Second line:**
   * saved views (OR-33): system presets first (Tasks "Running": every state that is not terminal), then the person's own with remove, plus "Save view";
   * [state counters](../controls/state-counters.md): totals per state, click to filter;
   * Group (registry icon with tooltip, OR-35): priority, state, project, agent, host, or none (D11);
   * Sort (OR-35): changed or created, with a direction toggle; Tasks default to recently changed first. A saved view stores grouping and sort.
3. **[Virtual list](../controls/data-list.md):**
   * group header rows;
   * rows in OR-22 order: state icon, title with its code, then the service zone: row facts (OR-37: since taken into work, executions, time in the current state, agent mark, host) with pin and checkbox over them on hover or when set.

**Behaviour.**
* Default grouping is by priority (D11). Grouping by state uses attention order: waiting, failed, in progress, queued, paused, completed, cancelled. Completed and cancelled start collapsed.
* Pinned items come first within their group.
* The project focus (OR-34) narrows every list whose view names no projects.
* The whole view (filters, states, grouping, sort, search) is in the URL; saved views and focus are stored on the server per person (`person_settings_*`, so agents use them too) ([principles §4](../principles.md#4-filters-and-search)).
* Clicking anywhere on a row opens the item in the work area ([list-detail](list-detail.md), OR-26). The row stays selected.
* New items arrive without moving the row being read. A "N new" pill appears when the person has scrolled away (OR-6, OR-7).

**Server note.** The mockups compute counts and groups on the client. The app gets counts per state from the server for the active filters, so counts stay correct for lists that are only partly loaded.
