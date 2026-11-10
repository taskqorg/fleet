# List and detail

**Code.** `ListDetail`.

**Job.** Read and act on one item while the catalogue stays in view (D5 of specq: no extra screen just to reach a link). These are the second and third columns of the [app shell](app-shell.md).

**Composition.**
* **Control column,** `size-ctl` wide: the section title, then search, filters, counters and grouping ([list view](list-view.md)), then the list. The list fills the rest of the column and is its only scroller.
* **Work area,** the remaining width: the opened item (OR-27). With nothing opened it shows one quiet line, or the section's reference page (Lab shows the use-case registry), never a second copy of the list.

**Catalogue row.** Two lines, `size-row-2` high: the title on the first line, its ID and other secondary facts on the second. The state icon stays leftmost and the service zone stays on the right (OR-21, OR-22). The list has no column heads. The whole row opens the item (OR-26).

**Work area, from top to bottom:**
1. Close icon, then `project · ID` in mono, then the primary actions as a [split button](../controls/split-button.md) (Pause with Cancel, Change account and Hand off) and Share as an icon.
2. Title.
3. State line: the state badge with its word (detail views show the word) and reason, plus an age chip.
4. [Totals](../controls/number.md) with basis.
5. Any open [decision](../controls/decision-block.md) or [problem](../controls/problem-row.md) for this item.
6. Tabs: Executions ([tree](../controls/execution-tree.md)), Transcript ([viewer](../controls/transcript-viewer.md)) and Results ([viewer shell](../controls/result-viewer.md)).

The work area scrolls as a whole. A conversation is the exception: its transcript is the scroller and the composer stays at the bottom (OR-25).

**Narrow widths.** At 1100 px and below only one of the two columns shows: the catalogue, or the opened item with its Close icon. Opening an item moves focus to it (WCAG 2.4.3).

**Rules.**
* The item's URL includes the item.
* Live updates change values in place.
* Tabs keep their state while the person moves between items.
