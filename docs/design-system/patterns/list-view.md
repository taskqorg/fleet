# List view

**Code.** `TaskListView`. It is a pattern because results, executions and trials reuse the same composition with their own columns.

**Job.** Find, triage and act on many items fast, with the view each person prefers (D9, OR-4, OR-9, OR-11, OR-12).

**Composition, from top to bottom:**
1. **[Filter bar](../controls/filter-bar.md), first line:** search, "Filter" (expands inline), active chips, Clear, and the count "84 of 1,240".
2. **Second line:**
   * saved views: pinned chips with unpin, plus "Save view";
   * [state counters](../controls/state-counters.md): totals per state, click to filter;
   * "Group": priority, state, project, agent, host, or none (D11). A saved view stores its grouping.
3. **[Virtual list](../controls/data-list.md):**
   * group header rows;
   * rows in OR-22 order: state icon, title with its ID, then the service zone (harness mark while running, stage chip, age chip, cost with basis, pin).

**Behaviour.**
* Default grouping is by priority (D11). Grouping by state uses attention order: waiting, failed, in progress, queued, paused, completed, cancelled. Completed and cancelled start collapsed.
* Pinned items come first within their group.
* The whole view (filters, states, grouping, search) is in the URL and remembered per person ([principles §4](../principles.md#4-filters-and-search)).
* Clicking anywhere on a row opens the item in the work area ([list-detail](list-detail.md), OR-26). The row stays selected.
* New items arrive without moving the row being read. A "N new" pill appears when the person has scrolled away (OR-6, OR-7).

**Server note.** The mockups compute counts and groups on the client. The app gets counts per state from the server for the active filters, so counts stay correct for lists that are only partly loaded.
