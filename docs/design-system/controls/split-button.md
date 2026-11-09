# Split button

**Code.** `SplitButton`. Demo: [book](../book/index.html#split-button).

**Purpose.** Runs the default action for a place with one click, and offers alternative actions for the same place without adding more buttons, for example Retry with "Retry on another host", "Continue on codex" and "Full rerun".

**Anatomy.**
* The main button: icon and verb.
* A joined chevron button that opens a short action menu below, aligned right. The menu uses the `float` surface with border and shadow (OR-16).
* Menu items: icon and label.

**Behaviour.**
* Clicking the main part runs the default action.
* The chevron toggles the menu. Choosing an item runs it and closes the menu. A click outside or `Esc` closes it.
* The menu holds actions only, never fields or forms, which OR-14 allows. An action that needs input opens inline after it is chosen.
* Destructive alternatives confirm in place, like [button](button.md).

**Keyboard.** `Enter` on the chevron opens the menu. Arrow keys move between items, `Enter` runs one, `Esc` closes.

**Accessibility.** The chevron is named "More actions for Retry" and carries `aria-haspopup="menu"` and `aria-expanded`. The menu uses `role="menu"` with `menuitem` entries.

**Do.** Put the most common action in the main part. **Don't.** Put more than about six alternatives in the menu; split them by place instead.
