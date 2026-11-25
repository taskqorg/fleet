# UI source package

## Overview

`src/` supplies Fleet's reusable SolidJS 2.0 + `@ui-sdk/solid` components and browser helpers. `index.ts` is the only intended import barrel for app code.

## Interface

- `controls.tsx` re-exports all control modules from `controls/` (`index.ts`, `patterns.tsx`, and book files import through `controls.tsx` or `index.ts`), and `styles/` provides layered CSS (`tokens`, `base`, `layout`, `controls` from `controls.css`, `thread.css`, `tables.css`, `live.css` and `sections.css`, `overlays`, `utilities`) assembled by `scripts/tokens.mjs:uiCss`:
  - `controls/primitives.tsx`: `Icon`, `Button`, `StateBadge`, numbers and live ages (`Num`, `Totals`, `CostFigures`, `useTick`, `Ago`, `AgeChip`), harness/stage marks (`HarnessMark`, `HarnessCounts`, `StageChip`), status feedback (`Skeleton`, `Empty`, `ErrorBanner`, `Stale`, `OfflineMarker`, `StatusLine`), clipboard/popover/menu helpers (`copyText`, `CopyButton`, `focusOnMount`, `popoverAttr`, `useAnchoredPopover`, `SplitButton`, `ActionMenu`, `SearchBox`), and layout rows (`DetailSection`, `KvRow`, `ProblemRow`).
  - `controls/overlays.tsx`: `TooltipLayer` (`popover="hint"`), `EntityCode`, `EntityCardLayer` (`popover="auto"`, mounted once by `AppShell`, reading through `EntityCards`), `linkCodes`, `ContextMenu`, and `MenuItem`.
  - `controls/navigation.tsx`: `NavRail`, `FocusPresets`, `FocusControl` (`popover="auto"` header focus chip with presets, ignore, and outside-focus count), `Spotlight` (`<dialog closedby="any">`), and `sectionProvider`.
  - `controls/lists.tsx`: `FilterBar`, `StateCounters`, `GroupHeader`, `Presets`, `GroupControl`, `SortControl`, `RowFacts`, `Fact`, `SelectBar`, `SelectToggle`, `RowCheck`, and `RowPin`.
  - `controls/tables.tsx`: `DataTable` with `TableRow` and `TableName`, `TabBar` (`@ui-sdk/solid` `Tabs`; tabs that do not fit fold into a More menu), and `Disclosure`.
  - Live blocks, one file each: `controls/status-pill.tsx` (`StatusPill`), `controls/figure-tile.tsx` (`FigureTile`, `Sparkline`), `controls/gauge.tsx` (`Gauge`), `controls/run-strip.tsx` (`RunStrip`), `controls/worker-slot.tsx` (`WorkerSlot`), `controls/host-band.tsx` (`HostBand`) and `controls/live-row.tsx` (`LiveRow`); styled in `styles/live.css`.
  - Catalogue-section blocks: `controls/detail-head.tsx` (`DetailHead`), `controls/diff-row.tsx` (`DiffRow`, `InheritedRows`) and `controls/matrix-cell.tsx` (`MatrixCell`); styled in `styles/sections.css`.
  - `controls/forms.tsx`: `TextField`, `SettingField`, `Composer`, `DropZone`, `CodeEditor`, `DiffText`, `DraftBar`, `InlineForm`, `Choice`, `ProjectPick`, `PresetPicker`, and `StartFacts`.
  - `controls/thread.tsx`: `Thread` and row renderers (`TextRow`, `ToolRow`, `ReasoningRow`, `QuestionRow`, `ResultRow`, `BoundaryRow`, `AgentMark`, `MessageFiles`), `DecisionBlock`, `Markdown` (turns D17 codes in text into references via `linkCodes`), `ResultViewer`, `ArtifactView`, and `SearchField`. `Thread quiet` is the session view's transcript: rows folded by `controls/tool-fold.ts` (`foldBlocks`: one `ToolLine` per run of tool, reasoning and Fleet notice blocks; answered questions as one line; no boundary rows).
  - `controls/metrics.tsx`: `FillBar`, `StatStrip`, `ExecTree`, `OutcomeBlock`, `StepList`, `FlowStrip`, `AreaChart`, and `TimeStrip`.
  - Home and session-view blocks, one file each (styles `home.css`, `session.css`): `controls/state-dot.tsx` (`StateDot`, `toLife`), `controls/avatar.tsx` (`Avatar`, `ProjectChip`), `controls/question-card.tsx` (`QuestionCard`), `controls/open-question.tsx` (`OpenQuestion`, `AnswerNote`; the logic is `answer.ts`: `answerLabels`, `answerChoices`, `useAnswer`, `keyConnection`), `controls/session-card.tsx` (`SessionCard`, `SessionCardSkeleton`), `controls/bot-tile.tsx` (`BotTile`), `controls/pulse-line.tsx` (`PulseLine`, `BlockerChip`), `controls/target-picker.tsx` (`TargetPicker`, the `Composer` `target`), `controls/quiet-summary.tsx` (`QuietSummary`), `controls/tool-line.tsx` (`ToolLine`), `controls/tool-fold.ts` (`foldBlocks`, `toolWords`), and `controls/context-panel.tsx` (`ContextPanel`, `PlanItem`, `TimerRow`, `SwarmMember`); `controls/side-panel.tsx` (`SidePanel`, `PanelToggle`, `ResizeDivider`, `PanelSheet`, `SidePanels`: the one side-panel implementation of the navigation rail and the context panel, side-panel.md). The message block is `TextRow quiet`.
- `charts.tsx` exports the day charts for pace pages: `DayChart`, `ShareBar`, `NormBar` and the scale helper `capOf`.
- `patterns.tsx` exports assembled layouts including `AppShell` (navigation rail plus the shell header: collapse toggle, section title, focus chip slot, search icon), `ListDetail`, `ListFrame`, `TaskListView`, and `DefinitionEditor`. `controls/navigation.tsx:FocusControl` is the header's focus chip with presets, ignore and the outside-focus count.
- `virtual-list.tsx:VirtualList` exports keyed virtual rendering and `VirtualListHandle`.
- `registry.ts` exports icons, state families (with the one vocabulary `states.life` and the OR-55 mapping `lifeOf`), workspaces, actions, harnesses, and stages; `format.ts` exports compact value formatting (including calendar days in the viewer's zone: `day`, `dayStart`, `hourLabel`) and actor/file helpers.
- `markdown.ts`/`markdown.tsx` export parsing and SolidJS 2.0 markdown rendering; `clip.ts` exports clipboard message/HTML helpers; `search.ts:matchSearch` applies supported search fields.

## Invariants

- `Icon` resolves names through `registry.ts:iconSvg`; state-bearing components use `registry.ts:states` families.
- `markdown.ts:SAFE_LINK` permits only `http:`, `https:`, and `mailto:` links in rendered markdown.
- `virtual-list.tsx:VirtualList` keeps item identity through its required key callback and exposes scrolling through `VirtualListHandle`.
- Formatting exposes measured, estimated, subscription, and unavailable values distinctly (`format.ts:Measured`, `format.ts:measured`, `format.ts:splitCostFigures`). `CostFigures` and `Totals` keep metered spend separate from subscription list-price equivalents across currencies.
- Interactive `AreaChart` bucket buttons sit outside `.ac-plot[role="img"]` without `<svg>`, `ExecTree` supports recursive hierarchies (`onSelect`, `costFigures`), and `BoundaryRow` accepts per-execution `resources`.

## Side Effects

- Components update browser DOM, focus, history-adjacent UI callbacks, and local component state.
- Clipboard helpers call `navigator.clipboard`; `Composer` uses injected persistence only; the app owns its storage and authorization.

## Verification

- Run: `pnpm run typecheck`
- S9 AST design rules (`UI-ANY`, `UI-VOCAB` over `src/`): `node --test tests/ui/design-rules.test.ts` ([`design-rules.test.ts`](../../../tests/ui/design-rules.test.ts))
- Design warnings: `pnpm run check:design`

`VirtualListHandle` exposes `getAnchor()` and `restoreAnchor(key, offsetPx)` in addition to key jumps; `fill` makes it the owning column's only scroller. `Composer` accepts an injected `draftPersistence` adapter and is memory-only without one; completion clears only unchanged original text. `Composer` is one box ([composer](../../../docs/design-system/controls/composer.md)): attachment chips (`attachments`, `onAttach` from the `+` picker and paste, `onRemoveAttachment`), the target `chips`, and one Send button that is Stop with `onStop` while the box is empty; the app uploads and owns the files. `DropZone` overlays a drop highlight over its children and hands dropped files over; `MessageFiles` (used by `TextRow` through `Thread.resultSrc`) shows a message's files. `StatusLine` reserves two lines for visible asynchronous feedback without moving adjacent controls.

`AppShell.privateReady` and `privateGeneration` fence its Spotlight state independently of column geometry. `Composer` remounts its private input by adapter key, resetting busy state before another target becomes usable. VirtualList applies requested restoration after its own layout work and uses current viewport edges for continuous loading.
