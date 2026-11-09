# Controls

One file per control. Each file has: purpose, anatomy, states, behaviour, keyboard, accessibility, do and don't, and where it is used. Each control is code in `packages/ui/src` and follows [owner rules](../owner-rules.md), [principles](../principles.md) and [tokens](../tokens.md). The [book page](../book/index.html) is their live demo in light and dark (`npm run book`).

| Control | Main inventory need |
|---|---|
| [button](button.md) | Every action |
| [data-list](data-list.md) | Long lists that scroll well |
| [filter-bar](filter-bar.md) | Filters and grouping, saved and shared |
| [search](search.md) | Fast search |
| [state-badge](state-badge.md) | States with reason and release condition |
| [number](number.md) | Measurement basis |
| [fill-bar](fill-bar.md) | Store fill per project, quota use |
| [execution-tree](execution-tree.md) | Executions and steps as a tree |
| [transcript-viewer](transcript-viewer.md) | Live transcript with paging and search |
| [decision-block](decision-block.md) | Decisions inline |
| [result-viewer](result-viewer.md) | Result viewers |
| [setting-field](setting-field.md) | Settings with applied or apply error |
| [problem-row](problem-row.md) | Signals with cause and actions |
| [status-states](status-states.md) | Empty, loading, error and stale |
