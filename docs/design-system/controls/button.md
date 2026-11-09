# Button

**Code.** `Button` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Starts an action. Every action in the inventory is a button or a menu item that calls the same system operation (R2).

**Anatomy.** Label (verb first: "Accept", "Raise capacity"), optional shortcut hint in `kbd`, optional leading registry icon. *Icon-only* buttons (OR-1) show the label as tooltip and accessible name; use them for small actions beside their subject (pin, open, download, retry). Height `size-control`; phone `size-hit-phone`.

**Variants.** *Primary*: `accent` fill, at most one per area. *Default*: `surface` with `line` border. *Quiet*: text only in `accent`, for "Clear" and inline links. *Danger*: `danger-fg` text and border; used only for cancel, revoke, delete.

**States.** Rest, hover (`hover` fill), focus (2 px `accent` ring, 2 px offset), pressed, disabled (`fg-faint`, with a tooltip saying why), busy (label stays, a small spinner replaces the icon, button disabled until the operation answers).

**Behaviour.** A click sends the operation once. Busy state lasts until the operation answers; the result shows where the work is (the row, the block), not in a toast only. Danger buttons confirm in place (never in a dialog, OR-14): the button turns into "Cancel task? Yes, cancel · Keep" for 6 s.

**Keyboard.** `Enter` and `Space`. Shortcut letters only within their focused block ([principles §5](../principles.md#5-keyboard)).

**Accessibility.** Accessible name is the label; icon-only buttons have `aria-label`. Disabled buttons stay focusable via `aria-disabled` so the reason can be read.

**Do.** Name the result: "Request rework". **Don't.** "OK", "Submit", or two primary buttons side by side.

**Used in.** Everywhere.
