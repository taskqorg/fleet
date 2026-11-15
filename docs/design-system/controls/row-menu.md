# Row menu

**Code.** `ContextMenu` in `packages/ui`, opened through `VirtualList` `onRowMenu`. Demo: [book](../book/index.html#data-list) (right-click a task row).

**Purpose.** Row actions without buttons on the row (OR-37): pin, select, copy and the state actions that apply.

**Anatomy.** A short floating menu (OR-14 allows it) at the pointer, kept inside the window. Items are left aligned with registry icons (OR-13). Tasks: Open, Pin or Unpin, Select or Deselect, Copy code, Copy link (the task alone, without the current view), and Pause or Continue when the state allows.

**Behaviour.**
* Opens on right click, long press where the browser supports it, the Menu key or Shift+F10 on a focused row. One menu per list (rows are virtual).
* Esc, a click outside, a scroll or leaving the window closes it; focus returns to the row.
* `p` pins the focused row and `x` selects it without opening the menu.

**Keyboard.** Up and Down move between items; Enter runs one.

**Accessibility.** `role=menu` named after the row; items are `menuitem`; a disabled item says why in its tooltip.

**Used in.** Tasks; every list with row actions.
