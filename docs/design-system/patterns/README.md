# Patterns

Compositions of controls for recurring jobs. Code: `packages/ui/src/patterns.tsx`, plus page-level layout classes in `styles.css`. Pages use these patterns and add no styling of their own (OR-13).

| Pattern | Job |
|---|---|
| [app-shell](app-shell.md) | Frame of every page: navigation, control column, work area |
| [navigation-and-columns](navigation-and-columns.md) | Unified 3-column roles, search hierarchy (⌘K vs icon filter), and idle-vs-open Work Area rules |
| [list-view](list-view.md) | Find and act on many items: views, counters, grouping, filters, virtual list |
| [no-horizontal-scroll](no-horizontal-scroll.md) | Reflow tables, key-value grids, long values, code, chips and matrices so nothing scrolls sideways (OR-66) |
| [infinite-list](infinite-list.md) | Load a list a page at a time as it scrolls; previews inside cards open the full list |
| [list-detail](list-detail.md) | Read one item beside its list without losing the list |
| [definition-editor](definition-editor.md) | Read and change a definition kept in git: agent, pipeline, skill, catalogue entry (mockups only) |
| [needs-you](needs-you.md) | Everything waiting for a person, answered in place |
| [ops-summary](ops-summary.md) | Operational counts, capacity, and multi-currency/subscription resource totals (`Totals`, `CostFigures`, `AreaChart`) at a glance |
