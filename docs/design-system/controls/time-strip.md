# Time strip

**Code.** `TimeStrip` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Shows where one run sits in a time window, so a column of runs reads as a compact Gantt chart.

**Anatomy.** 8 px track on `neutral-bg` with `radius-md`, full width of its column; one bar from the run's start to its end in the tone of its [execution state](state-badge.md). A running run reaches now and has a 2 px edge in `fg`. A run that started before the window starts at its left edge; a bar is at least 2 px wide. With `parts`, one lane holds several consecutive bars, each in its own tone with its name as a tooltip; the strip's accessible name lists them.

**States.** Ended (bar between start and end); running (bar to now, edge mark).

**Behaviour.** None of its own: the row it sits in opens the run's task or conversation (OR-26).

**Accessibility.** `role="img"` with an accessible name that gives the state and start; the row's duration or age chip states the length in text, so colour and position are never the only key.

**Do.** Use one window for every strip on a page. **Don't.** Add axis labels to each strip: the panel head names the window.

**Used in.** [Runners](../pages/runners.md); the stages of an opened pull request on [Delivery](../pages/delivery.md).

## Run strip

`RunStrip` in `packages/ui` (`src/controls/run-strip.tsx`); the window is 24 hours unless `hours` says otherwise. Demo: [book](../book/index.html#run-strip). Board: [blocks.bl](../board/blocks.bl), master `Run strip`.

The 24-hour strip of one host on [Live](../pages/live.md): one tick per run window on one 8 px track, the running tone for runs and `danger-fg` for failures ([OR-56](../owner-rules.md)). Under the track, marks at −24 h, −18 h, −12 h, −6 h and now in `font-size-xs`, `fg-faint`. Ticks update in place as runs start and end ([OR-6](../owner-rules.md)). It is `role="img"` named "24 hours: 44 runs, 3 failed".
