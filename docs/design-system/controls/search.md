# Search

**Code.** `SearchBox` (also inside `FilterBar` and `SearchField`) in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Finds items by title, ID and key text within the current list, or everywhere.

**Anatomy.** A search icon button (OR-31). Clicking it shows the input, focused, with a placeholder that names the query syntax ("Search tasks, agent:claude host:mac"). The input stays while it holds text or focus; an empty field that loses focus turns back into the icon.

**States.** Empty, typing, searching (old results dimmed, no spinner under 300 ms), results, no results ("No tasks match 'hero'. Search everywhere"), error.

**Behaviour.** Local filtering on each keystroke for loaded rows; server query debounced 150 ms. Matches are highlighted in titles. A code (for example `T-FLT-42`) matches its item. The query takes the search syntax: words plus `agent:`, `host:`, `state:`, `project:` terms ([Spotlight](spotlight.md)). The query is part of the filter state in the URL.

**Keyboard.** `Esc` clears and leaves the field; `Enter` searches where a scope searches on demand (a thread).

**Accessibility.** `role="searchbox"` with label; result count announced politely.

**Do.** Keep results visible while typing. **Don't.** Require pressing Enter; clear the list while a query runs.

**Used in.** Filter bar on every list; transcript viewer; global search in the header.

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **OR-31 Search is an icon.** Search in lists is an icon; the field shows only while focused or not empty. The app-wide search is the search icon on the right of the shell header (OR-43), tooltip "Search ⌘K"; it opens the palette (OR-32).
* **UR-16.** Filtering loaded data reacts within 100 ms; a server query keeps the old rows, dimmed, until the new ones arrive, and the list never empties while loading. *Check:* Browser: type a query and time the first change; with a slowed network the row count never reaches 0 during the load.
* **UR-19.** Search matches titles, codes and key text, highlights matches, and stays in the current list unless the person picks "everywhere" (the palette, [OR-32](spotlight.md#rules-kept-here)). *Check:* Browser: search a known code, expect it first and highlighted.
* **UR-21.** `/` focuses list search and `Esc` clears it, then leaves it; `⌘K` opens the palette ([OR-32](spotlight.md#rules-kept-here)). *Check:* Browser: press the keys, check `document.activeElement`.
