# List view

**Code.** `TaskListView`. It is a pattern because conversations, results, executions and trials reuse the same composition with their own columns. Conversations use the whole composition with their own presets, groupings, sorts and row facts ([conversations](../pages/conversations.md)).

**Job.** Find, triage and act on many items fast, with the view each person prefers (D9, OR-4, OR-9, OR-11, OR-12).

**Composition, from top to bottom:**
1. **[Filter bar](../controls/filter-bar.md), first line:** search as an icon (OR-31) that takes the query syntax (OR-36), chips of filters that came with a link, Clear, and the count "84 of 1,240".
2. **Second line:**
   * saved views (OR-33): system presets first (Tasks "Needs me" and "Running": every state that is not terminal), then the person's own with remove, plus "Save view" (its name field closes with Esc or its cross). A system preset sets only the state filter and leaves grouping, sort, projects and search as they are; it shows active whenever the states match. A personal view restores everything it saved;
   * [state counters](../controls/state-counters.md): totals per state, click to filter;
   * Group (registry icon with tooltip, OR-35): priority, state, project, agent, host, or none (D11);
   * Sort (OR-35): changed or created, with a direction toggle; Tasks default to recently changed first. A saved view stores grouping and sort.
3. **[Virtual list](../controls/data-list.md):**
   * group header rows;
   * rows in OR-22 order: state icon, title with its code, then the service zone: row facts (OR-37: since taken into work, executions, time in the current state, agent mark, host) with nothing over them on hover (OR-37).
   * the row menu (`ContextMenu`) on right click, long press, the Menu key or Shift+F10: Open, Pin or Unpin, Select, Copy code, Copy link, and the state actions that apply (Pause, Continue). `p` pins the focused row, `x` selects it.
   * selection mode: the Select square at the left of the tools line, Shift-click or `x` turns it on; a checkbox column appears left of the state icon under it and a click on a row toggles it; `SelectBar` takes its own line under the tools (group and sort stay): select all or none (of the loaded rows), the cross to leave, the count, then Pin, Unpin, Pause, Continue, Cancel (confirmed inline) on the right. Esc or the cross leaves the mode. Checked rows are the view URL's `sel`.
   * a Pinned group at the top holds pinned rows (OR-12), whatever the filter, search or grouping (they are read by id, unfiltered); each pinned row shows `RowPin` before its state icon, and a click on it unpins. Only pinned rows have this column (row class `pin-row`); other rows do not move.

**Behaviour.**
* Tasks default to grouping by project and sorting by most recently changed first (owner, 2026-11-16); priority is a grouping (D11). Grouping by state uses attention order: waiting, failed, in progress, queued, paused, completed, cancelled. Completed and cancelled start collapsed.
* Collapsed groups are remembered per person on the server (`listGroups`, `person_settings_*`, so agents read and write them), per section and grouping, the Pinned group per section. A list stores only the groups the person turned away from the page default, both ways: a group collapsed where the default is open and one opened where the default is collapsed (Tasks: completed and cancelled under state grouping; Delivery: installed under stage grouping). Leaving the section and coming back, a reload and a switch back to the grouping show them at once, without the default flashing first: lists render after the preferences are read. A toggle shows at once and is saved debounced, and at once when the person leaves the section or the tab hides. A page passes its defaults to `useListPage` as `closedGroups` by grouping. A default never changes inside one list, so a remembered flip means the same every time; a view that needs other defaults is a context of its own with its own remembered groups (`groupsContext`, kept as `<grouping>-<context>`: Delivery's `stage-merged` for a picked day or a filter to installed rows, where Installed starts open; Tasks' `state-done` for a filter to completed or cancelled tasks alone). When the loaded rows end inside a collapsed group, the collapsed groups after it show their headers from the server's group counts without reading their rows, and while an open group is still to come the list keeps loading pages as the end is in view; collapsed groups at the end of the list are not read through.
* Pinned items come first within their group.
* The project focus (OR-34) narrows every list whose view names no projects.
* The whole view (filters, states, grouping, sort, search) is in the URL; saved views and focus are stored on the server per person (`person_settings_*`, so agents use them too) ([principles §4](../principles.md#4-filters-and-search)).
* Clicking anywhere on a row opens the item in the work area ([list-detail](list-detail.md), OR-26). The row stays selected.
* New items arrive without moving the row being read. A "N new" pill appears when the person has scrolled away (OR-6, OR-7).

**Server note.** The mockups compute counts and groups on the client. The app gets counts per state from the server for the active filters, so counts stay correct for lists that are only partly loaded.
