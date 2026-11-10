# Patterns

Compositions of controls for recurring jobs. Code: `packages/ui/src/patterns.tsx`, plus page-level layout classes in `styles.css`. Pages use these patterns and add no styling of their own (OR-13).

| Pattern | Job |
|---|---|
| [app-shell](app-shell.md) | Frame of every page: navigation, control column, work area |
| [list-view](list-view.md) | Find and act on many items: views, counters, grouping, filters, virtual list |
| [list-detail](list-detail.md) | Read one item beside its list without losing the list |
| [definition-editor](definition-editor.md) | Read and change a definition kept in git: agent, pipeline, skill, catalogue entry (mockups only) |
| [needs-you](needs-you.md) | Everything waiting for a person, answered in place |
| [ops-summary](ops-summary.md) | Operational counts and capacity at a glance |
