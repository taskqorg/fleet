# Controls

One file per control. Each file has: purpose, anatomy, states, behaviour, keyboard, accessibility, do and don't, and where it is used. Each control is code in `packages/ui/src` and follows [owner rules](../owner-rules.md), [principles](../principles.md) and [tokens](../tokens.md). The [book page](../book/index.html) is their live demo in light and dark (`npm run book`).

| Control | Main inventory need |
|---|---|
| [button](button.md) | Every action |
| [data-list](data-list.md) | Long lists that scroll well |
| [filter-bar](filter-bar.md) | Filters and grouping, saved and shared |
| [search](search.md) | Fast search |
| [spotlight](spotlight.md) | Cmd+K palette: find and open anything |
| [state-badge](state-badge.md) | States with reason and release condition |
| [number](number.md) | Measurement basis |
| [fill-bar](fill-bar.md) | Store fill per project, quota use |
| [time-strip](time-strip.md) | Where a run sits in a time window |
| [execution-tree](execution-tree.md) | Executions and steps as a tree |
| [step-list](step-list.md) | A pipeline's steps and a run of them; the read-only flow picture |
| [draft-bar](draft-bar.md) | A definition that differs from its saved revision |
| [choice](choice.md) | One or several of a few fixed options |
| [transcript-viewer](transcript-viewer.md) | Live transcript with paging and search |
| [decision-block](decision-block.md) | Decisions inline |
| [result-viewer](result-viewer.md) | Result viewers |
| [text-field](text-field.md) | Unsaved form values with accessible labels and errors |
| [setting-field](setting-field.md) | Settings with applied or apply error |
| [nav-rail](nav-rail.md) | Many sections; pinned per person |
| [split-button](split-button.md) | Default action with alternatives |
| [state-counters](state-counters.md) | Counts per state, quick state filter |
| [stat-strip](stat-strip.md) | Operational counts |
| [harness-counts](harness-counts.md) | Which harness runs it; workers per harness |
| [stage-chip](stage-chip.md) | Delivery stage of a task |
| [outcome-block](outcome-block.md) | Task outcome, requirements and inputs |
| [detail-section](detail-section.md) | Stacked detail areas |
| [composer](composer.md) | Message an agent |
| [offline-marker](offline-marker.md) | Data freshness: offline, updating, signed out |
| [problem-row](problem-row.md) | Signals with cause and actions |
| [status-states](status-states.md) | Empty, loading, error and stale |
