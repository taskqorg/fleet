# Navigation rail

**Code.** `NavRail` in `packages/ui`. Demo: [book](../book/index.html#nav-rail).

**Purpose.** Moves between sections. There can be many sections, so each person pins the ones they follow ([D9](../decisions.md)).

**Anatomy.** A left column of width `size-rail`, from top to bottom:
0. The head: the Fleet mark and name ([D23](../decisions.md#2026-11-11-d23-name-and-mark)), the "N need you" chip and the collapse button. Collapsed, the head keeps the mark above the button ([app shell](../patterns/app-shell.md), OR-24).
1. The pinned group, in the order the person set.
2. The Search item: opens the [Spotlight palette](spotlight.md) (OR-31, OR-32). The rail has no search field of its own.
3. All other sections, grouped (Operator, Architect) under small group headings.

Each item has a registry icon (OR-3), a name, an optional counter chip in a tone (for example `3` in attention for "need you"), and a pin button.

**Behaviour.**
* The pin button appears on hover and focus, and is always visible on touch. Pinning adds the item to the pinned group and keeps it in its own group, where its pin stays shown as pressed. Unpin from either place.
* Right-click (or the context-menu key, Shift+F10) on any item opens a short menu: Pin or Unpin, and for a pinned item Move up and Move down.
* Drag a pinned item to reorder the pinned group.
* Pins and their order are a person setting (`pins` in `person_settings_get`/`person_settings_set`) and are restored on every device.
* Counters show only when they are not zero and link to the filtered list. A counter's tone follows the [state badge](state-badge.md) mapping.
* The current section has the `selected` background.
* The collapse button shrinks the rail to icons, and below 760 px it is always collapsed: pins first and counters on the icons. Names appear in tooltips, shown only while names are hidden (OR-20). This is interim; open question (d) holds the variants ([D10](../decisions.md)). The rail never disappears.

**Keyboard.** `g` then a letter goes to a section ([principles §5](../principles.md#5-keyboard)). Up and down arrows move through items, `p` pins or unpins the focused item, and Alt+Up and Alt+Down move a focused pinned item.

**Accessibility.** `nav` named "Sections". Each pinned and group list is named. Pin buttons are named "Pin Tasks" or "Unpin Tasks" and expose `aria-pressed`. Counters have accessible names such as "2 failed".

**Do.** Keep the order the person chose. **Don't.** Reorder sections automatically by frequency (specq "stable adaptation"). Don't add a separate Favourites page (OR-12).
