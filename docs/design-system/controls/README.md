# Controls

One file per control. Each file has: purpose, anatomy, states, behaviour, keyboard, accessibility, do and don't, and where it is used. Each control is code in `packages/ui/src` and follows [owner rules](../owner-rules.md), [principles](../principles.md) and [tokens](../tokens.md). The [book page](../book/index.html) is their live demo in light and dark (`npm run book`).

| Control | Main inventory need |
|---|---|
| [button](button.md) | Every action |
| [catalogue-tree](catalogue-tree.md) | Planned 1–3 level structural navigator for libraries, suites and runs |
| [data-list](data-list.md) | Long lists that scroll well |
| [entity-picker](entity-picker.md) | Planned compact chip + searchable tree picker for entities and variants |
| [row-menu](row-menu.md) | Row actions on right click, no hover buttons |
| [selection](selection.md) | Several rows at once: select square, checkbox column, selection bar |
| [row-pin](row-pin.md) | A pinned row's pin, unpin in one click |
| [filter-bar](filter-bar.md) | Filters and grouping, saved and shared |
| [search](search.md) | Fast search |
| [spotlight](spotlight.md) | Cmd+K palette: find and open anything |
| [entity-card](entity-card.md) | What a referenced object is, and going to it |
| [state-badge](state-badge.md) | States with reason and release condition |
| [number](number.md) | Measurement basis, `CostFigures`, and `Totals` |
| [fill-bar](fill-bar.md) | Store fill per project, quota use |
| [time-strip](time-strip.md) | Where a run sits in a time window |
| [area-chart](area-chart.md) | Quota and resource usage trends (CSS only) |
| [day-chart](day-chart.md) | One figure per calendar day on a shared day axis |
| [share-bar](share-bar.md) | One whole split into parts, values on the parts |
| [norm-bar](norm-bar.md) | Time in the current stage against the stage's norm |
| [code-editor](code-editor.md) | Definition text and unified revision diffs |
| [execution-tree](execution-tree.md) | Executions and steps as a tree |
| [step-list](step-list.md) | A pipeline's steps and a run of them; the read-only flow picture |
| [draft-bar](draft-bar.md) | A definition that differs from its saved revision |
| [choice](choice.md) | One or several of a few fixed options |
| [transcript-viewer](transcript-viewer.md) | Live transcript with paging and search |
| [decision-block](decision-block.md) | Decisions inline |
| [result-viewer](result-viewer.md) | Result viewers |
| [text-field](text-field.md) | Unsaved form values with accessible labels and errors |
| [setting-field](setting-field.md) | Settings with applied or apply error |
| [nav-rail](nav-rail.md) | Many sections; pinned per person; the installed version and the update |
| [focus-control](focus-control.md) | The projects the person watches, ignored projects, what needs them outside the focus |
| [split-button](split-button.md) | Default action with alternatives |
| [state-counters](state-counters.md) | Counts per state, quick state filter |
| [stat-strip](stat-strip.md) | Operational counts |
| [harness-counts](harness-counts.md) | Which harness runs it; workers per harness |
| [stage-chip](stage-chip.md) | Delivery stage of a task |
| [outcome-block](outcome-block.md) | Task outcome, requirements and inputs |
| [detail-section](detail-section.md) | Stacked detail areas |
| [composer](composer.md) | Message an agent |
| [start-picker](start-picker.md) | Start a conversation: project, preset, what the start uses |
| [offline-marker](offline-marker.md) | Data freshness: offline, updating, signed out |
| [problem-row](problem-row.md) | Signals with cause and actions |
| [status-states](status-states.md) | Empty, loading, error and stale |
| [session-card](session-card.md) | One active session: what it does now and what it needs from you |
| [bot-tile](bot-tile.md) | An asleep bot and Wake |
| [pulse-line](pulse-line.md) | Work in flight and blockers at a glance |
| [tool-line](tool-line.md) | Tool calls folded into one quiet line |
| [context-panel](context-panel.md) | A session's plan, timers, automations and swarm |
| [side-panel](side-panel.md) | Every column that opens and closes beside the page: one toggle place, a resizable width, state stored per person |
| [status-pill](status-pill.md) | The fleet's state in words |
| [figure-tile](figure-tile.md) | One headline figure with its trend |
| [host-band](host-band.md) | One host: health, slots, runs |
| [worker-slot](worker-slot.md) | What runs in one worker slot |
| [live-row](live-row.md) | A project, task in work, pull request, runner or event on Live |
| [work-facts](work-facts.md) | Attempts, time in work and agent; latest attempt and time in state on hover: one row anatomy for work |
| [matrix-cell](matrix-cell.md) | One example × variant of a Lab experiment |
| [tab-bar](tab-bar.md) | Facets of one opened entity |
| [quiet-summary](quiet-summary.md) | What happened while you were away, when nothing waits |

## Blocks of Home, Live and the session view

The screens of the conversation-first interface, and the catalogues and opened items of the other sections, ([OR-50](../owner-rules.md) to [OR-61](../owner-rules.md)) are composed of these blocks. Each block is a component on the design board ([blocks.bl](../board/blocks.bl)) with all its states, and the screen canvases use its instances, so a block change reaches every screen. *New* means the block has no `packages/ui` code yet; it is built from its spec before the page that uses it (OR-13).

| Block | Spec | `packages/ui` | Screens |
|---|---|---|---|
| Session card | [session-card](session-card.md) | `SessionCard`, `Avatar`, `ProjectChip` | Home |
| Bot avatar | [avatar](avatar.md) | `Avatar` | Library, Home, rail |
| Question card | [decision block, card](decision-block.md#card) | `QuestionCard` | Home, session view |
| Bot tile | [bot-tile](bot-tile.md) | `BotTile` | Home |
| Quiet summary | [quiet-summary](quiet-summary.md) | New (`QuietSummary`) | Home |
| Composer with target picker | [composer, target picker](composer.md#target-picker) | `Composer` with `TargetPicker` | Home |
| Pulse line, blocker chip | [pulse-line](pulse-line.md) | `PulseLine`, `BlockerChip` | Home |
| State dot and word | [state badge, dot and word](state-badge.md#dot-and-word) | `StateDot` (`states.life`, `lifeOf`) | Home, Live, session view |
| Session group head | [data list](data-list.md) | `GroupHeader` | Home |
| Message | [transcript viewer](transcript-viewer.md) | `TextRow quiet`, `MessageFiles` | Session view |
| Folded tool line | [tool-line](tool-line.md) | `ToolLine` | Session view |
| Context panel | [context-panel](context-panel.md) | `ContextPanel`, `PlanItem`, `TimerRow`, `SwarmMember` | Session view |
| Status pill, status bar | [status-pill](status-pill.md), [status bar](status-pill.md#status-bar) | `StatusPill`; status bar new | Live |
| Figure tile with sparkline | [figure-tile](figure-tile.md) | `FigureTile`, `Sparkline`, value by `Num` | Live |
| Host band | [host-band](host-band.md) | `HostBand` | Live |
| Gauge | [fill bar, gauge](fill-bar.md#gauge) | `Gauge` (the fill bar's gauge form) | Live |
| Worker slot | [worker-slot](worker-slot.md) | `WorkerSlot` | Live |
| Run strip | [time strip, run strip](time-strip.md#run-strip) | `RunStrip` (the time strip's run strip form) | Live |
| Live row | [live-row](live-row.md) | `LiveRow`, stage by `StageChip` | Live |
| Work facts | [work-facts](work-facts.md) | `WorkFacts`, `peerFence` | Live; next Tasks, Conversations, Results, Runners |
| Catalogue row, state icon, list group | [data list](data-list.md#board) | `CatalogueTree` leaf and folder rows | Catalogues in the control column |
| Filter bar, search, view chip, state counters | [filter bar](filter-bar.md#board) | `FilterBar`, `SearchBox`, `Presets`, `StateCounters` | Catalogues in the control column |
| Detail head | [list and detail](../patterns/list-detail.md#board) | `DetailHead` | Every opened item |
| Detail section, key row | [detail section](detail-section.md#board) | `DetailSection`, `KvRow` | Opened items |
| Tab bar | [tab-bar](tab-bar.md) | `TabBar` | Opened items with facets |
| Text field | [text field](text-field.md#board) | `TextField` | Forms in the work area |
| Setting field | [setting field](setting-field.md#board) | `SettingField` | Settings in System and Projects |
| Table row | [data list, table row](data-list.md#table-row) | `DataTable`, `TableRow`, `TableName` | Lab leaderboard, Spend, project Delivery and Results |
| Diff row | [definition editor, diff row](../patterns/definition-editor.md#diff-row) | `DiffRow`, `InheritedRows` | Library, project Rules & methods, Lab variant |
| Share bar | [share bar](share-bar.md#board) | `ShareBar` | Spend, Delivery |
| Step row | [step list](step-list.md#board) | `StepList` | Library pipelines, Lab trial plan, session plan |
| Matrix cell | [matrix-cell](matrix-cell.md) | `MatrixCell` | Lab experiment matrix |
| Problem row | [problem row](problem-row.md#board) | `ProblemRow` | System readiness, Library sync, Delivery |
| Navigation rail, phone bottom bar | [nav-rail](nav-rail.md#board) | `NavRail`, bottom bar new | Every page |
| Shell header | [app-shell](../patterns/app-shell.md#board) | `AppShell` | Every page except wall mode |
