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
