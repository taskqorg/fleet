# Navigation rail

**Code.** `NavRail` in `packages/ui`. Demo: [book](../book/index.html#nav-rail).

**Purpose.** Moves between sections. There can be many sections, so each person pins the ones they follow ([D9](../decisions.md)).

**Anatomy.** A left column of width `size-rail`, from top to bottom:
1. The pinned group, in the order the person pinned it.
2. A "Sections" search field.
3. All other sections, grouped (Operator, Architect) under small group headings.

Each item has a registry icon (OR-3), a name, an optional counter chip in a tone (for example `3` in attention for "need you"), and a pin button.

**Behaviour.**
* The pin button appears on hover and focus, and is always visible on touch. Pinning moves the item into the pinned group; unpinning returns it to its group.
* Pins are stored per person and restored on every device.
* Counters show only when they are not zero and link to the filtered list. A counter's tone follows the [state badge](state-badge.md) mapping.
* Search filters the items by name as you type.
* The current section has the `selected` background.
* Below 760 px the rail shrinks to icons, with pins first and counters on the icons. Names appear in tooltips, shown only while names are hidden (OR-20). This is interim; open question (d) holds the variants ([D10](../decisions.md)). The rail never disappears.

**Keyboard.** `g` then a letter goes to a section ([principles §5](../principles.md#5-keyboard)). Up and down arrows move through items, `p` pins the focused item.

**Accessibility.** `nav` named "Sections". Each pinned and group list is named. Pin buttons are named "Pin Tasks" or "Unpin Tasks" and expose `aria-pressed`. Counters have accessible names such as "2 failed".

**Do.** Keep the order the person chose. **Don't.** Reorder sections automatically by frequency (specq "stable adaptation"). Don't add a separate Favourites page (OR-12).
