# Time strip

**Code.** `TimeStrip` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Shows where one run sits in a time window, so a column of runs reads as a compact Gantt chart.

**Anatomy.** 8 px track on `neutral-bg` with `radius-md`, full width of its column; one bar from the run's start to its end in the tone of its [execution state](state-badge.md). A running run reaches now and has a 2 px edge in `fg`. A run that started before the window starts at its left edge; a bar is at least 2 px wide.

**States.** Ended (bar between start and end); running (bar to now, edge mark).

**Behaviour.** None of its own: the row it sits in opens the run's task or conversation (OR-26).

**Accessibility.** `role="img"` with an accessible name that gives the state and start; the row's duration or age chip states the length in text, so colour and position are never the only key.

**Do.** Use one window for every strip on a page. **Don't.** Add axis labels to each strip: the panel head names the window.

**Used in.** [Runners](../pages/runners.md).
