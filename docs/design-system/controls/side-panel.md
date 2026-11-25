# Side panel

**Code.** `SidePanel`, `PanelToggle`, `ResizeDivider`, `PanelSheet` and the `SidePanels` store in `packages/ui/src/controls/side-panel.tsx`. `AppShell` provides the store and builds the navigation rail from the same parts. Check: `tests/ui/side-panel.check.ts`.

**Purpose.** Every column that opens and closes beside the page is one side panel, so all of them behave the same way. Today there are two: the navigation rail on the left and the session's [context panel](context-panel.md) on the right ([OR-41](../owner-rules.md), [OR-42](../owner-rules.md)).

**Anatomy.**

* **Toggle.** An icon button in the [shell header](../patterns/app-shell.md) at a fixed place. The left toggle is the header's first item. Right-panel toggles go after search, in a place the header keeps on every page. The glyph shows the side and what a click does: `panel-left-close` while the rail is open, `panel-right-open` while the right panel is closed. The name says the same: `Collapse navigation`, `Show context`.
* **Column.** The panel's contents at the person's width. The navigation uses `--size-rail` on the shell. A right panel uses `--size-side` on its column, the `size-search` token by default, 200 to 560 px, and at most half of the page beside it.
* **Divider.** On the panel's inner edge (`role=separator`). A drag resizes the panel live and saves the width once, at its end. Left and Right move the divider by 16 px and save on release. A double-click resets the width.
* **Sheet.** At phone width a right panel has no column. The same toggle opens its contents as a native modal `<dialog>` from the bottom edge.

**States.** *Open* and *closed* for a column; the navigation's closed state is its icon rail. At phone width a right panel is a *sheet*, open or closed.

**Behaviour.**

* Open or closed and the width are person settings on the server, by panel key: `panels.<key>` (a key left out is open) and `widths.<key>`. The navigation is `navCollapsed` and `widths.nav`; the context panel is `panels.session-context` and `widths.session-context`. Agents read and write them with `person_settings_get` and `person_settings_set` ([preferences](../../designs/fleet-people-access.md)).
* The state belongs to the panel, not to the page. A panel opens in the same place, at the stored width, after a reload, in another session and on Home.
* Opening or closing never moves the toggle, search or the rows being read ([OR-6](../owner-rules.md), [OR-23](../owner-rules.md)). The session view keeps the transcript row being read in place.
* A stored width is clamped to the panel's limits when read, so a value from an agent or another device never breaks the layout.
* The sheet is a passing view: opening or closing it leaves the stored state as it is. Escape, the Back gesture and a tap outside close it. Focus then returns to the toggle.
* Opening and closing are immediate, with no animation, the same for every panel.

**Keyboard.** The toggle is a button (Enter, Space). The divider takes focus and the arrow keys. The sheet closes on Escape.

**Accessibility.** The toggle's name says what a click does. The divider is a vertical `separator` named `Resize navigation` or `Resize context`. The contents name their own landmark (`Sections`, `Session context`).

**Do.** Build every new panel beside the page from `SidePanel` with its own key. **Don't.** Put a panel's toggle inside the panel or the page head, keep its state in the browser or in the page, or give it a resize of its own.

**Used in.** The navigation rail ([nav rail](nav-rail.md)) and the session view's context panel ([session](../pages/session.md)).
