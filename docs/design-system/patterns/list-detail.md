# List and detail

**Code.** `ListDetail`.

**Job.** Read and act on one item while the catalogue stays in view (D5 of specq: no extra screen just to reach a link). These are the second and third columns of the [app shell](app-shell.md).

**Composition.**
* **Control column,** `size-ctl` wide: the section title, then search, filters, counters and grouping ([list view](list-view.md)), then the list. A virtual list fills the rest of the column and is its only scroller; a short hand-built catalogue (Hosts, API keys) scrolls with the column.
* **Work area,** the remaining width: the opened item (OR-27). With nothing opened it shows the section's summary: what needs a look there, then its reference page (Lab shows the use-case registry), never a placeholder such as "Pick a row" and never a second copy of the list.

**Catalogue row.** One line, `size-row` high, `size-row-phone` on touch (a hand-built row takes that as its least height and grows when its facts wrap): the title, then its ID, which is never cut or wrapped (OR-38); a longer secondary text after the title (`.sub`) is cut before the title is; other facts sit in the service zone. The state icon stays leftmost and the service zone stays on the right (OR-21, OR-22). The list has no column heads. The whole row opens the item (OR-26).

**Work area, from top to bottom:**
1. Close icon, then `project · ID` in mono, then the primary actions as a [split button](../controls/split-button.md) (Pause with Cancel, Change account and Hand off) and Share as an icon.
2. Title.
3. State line: the state badge with its word (detail views show the word) and reason, plus an age chip.
4. [Totals](../controls/number.md) with basis.
5. Any open [decision](../controls/decision-block.md) or [problem](../controls/problem-row.md) for this item.
6. Tabs: Executions ([tree](../controls/execution-tree.md)), Transcript ([viewer](../controls/transcript-viewer.md)) and Results ([viewer shell](../controls/result-viewer.md)).

The work area scrolls as a whole. A conversation is the exception: its transcript is the scroller and the composer stays at the bottom (OR-25).

**Narrow widths.** At 1100 px and below only one of the two columns shows: the catalogue, or the opened item with its Close icon. Opening an item moves focus to it (WCAG 2.4.3). Known limitation (owner, 2026-11-17): with nothing open, a narrow window shows only the catalogue, so a section's own reference page in the work area (for example Delivery's Pace) is not reachable there. Kept as is.

**Rules.**
* The item's URL includes the item.
* Live updates change values in place.
* Tabs keep their state while the person moves between items.

## Board

The top of the work area is `DetailHead` (`src/controls/detail-head.tsx`; `DefinitionEditor` uses it; demo: [book](../book/index.html#detail-head)), the `Detail head` master on [blocks.bl](../board/blocks.bl), board *Sections*: Close icon ([OR-15](#rules-kept-here)), `project · code` in mono, the primary action (the board shows one button; the built head uses the [split button](../controls/split-button.md)) and Share, then the title and the state line ([OR-39](#rules-kept-here)). The opened item fills the work area ([OR-27](#rules-kept-here)). Facets of the item sit under it in the [tab bar](../controls/tab-bar.md); stacked areas use the [detail section](../controls/detail-section.md#board).

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **OR-15 Return by icon.** A return action is an icon with a tooltip, and only where browser Back does not already serve.
* **OR-27 Open in the work area.** A task, conversation, result, host or any other opened item fills the work area; a session opened from Home fills the area right of the navigation. It is never a floating, sticky or overlaid panel, at any width. Narrow work areas follow the [device rules](../principles.md#narrow-work-areas).
* **OR-39 Detail shows the story.** A detail page shows the statement, lifecycle, result and decision. Technical ids appear only where truly needed, as an icon with tooltip and click-to-copy. No large empty fields.
* **UR-35.** An empty area says why it is empty and what to do, and an idle work area shows a useful summary, never "Pick a row" ([list-detail](../patterns/list-detail.md)). *Check:* Browser: open each section with nothing selected, expect no "Pick" or "Select" placeholder text.
* **UR-88.** A list and its opened item are separate screens, and Back returns to the list at the same place. *Check:* Browser: open an item at 390 px, go Back, compare scroll position.
