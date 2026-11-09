# List and detail

**Job.** Read and act on one item while the list stays in view (D5 of specq: no extra screen just to reach a link).

**Composition.**
* The list on the left, with flexible width.
* The detail panel on the right, `size-detail` wide.
* Below 1100 px the detail stacks under the list. On phone it is its own route, and Back returns to the list position.

**Detail panel, from top to bottom:**
1. Close icon, then `project · ID` in mono, then the primary actions as a [split button](../controls/split-button.md) (Pause with Cancel, Change account and Hand off) and Share as an icon.
2. Title.
3. State line: the state badge with its word (detail views show the word) and reason, plus an age chip.
4. [Totals](../controls/number.md) with basis.
5. Any open [decision](../controls/decision-block.md) or [problem](../controls/problem-row.md) for this item.
6. Tabs: Executions ([tree](../controls/execution-tree.md)), Transcript ([viewer](../controls/transcript-viewer.md)) and Results ([viewer shell](../controls/result-viewer.md)).

**Rules.**
* The detail's URL includes the item.
* Live updates change values in place.
* Tabs keep their state while the person moves between items.
