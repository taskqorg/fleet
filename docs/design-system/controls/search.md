# Search

**Code.** Inside `FilterBar` and `Transcript` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Finds items by title, ID and key text within the current list, or everywhere.

**Anatomy.** Input with placeholder "Search tasks" and a `/` hint; clear button when not empty; scope toggle "This list · Everywhere" when global search applies.

**States.** Empty, typing, searching (old results dimmed, no spinner under 300 ms), results, no results ("No tasks match 'hero'. Search everywhere"), error.

**Behaviour.** Local filtering on each keystroke for loaded rows; server query debounced 150 ms. Matches are highlighted in titles. Exact ID match (for example `T-1042`) jumps to that item. The query is part of the filter state in the URL.

**Keyboard.** `/` focuses; `Esc` clears, second `Esc` leaves; `↓` moves into results.

**Accessibility.** `role="searchbox"` with label; result count announced politely.

**Do.** Keep results visible while typing. **Don't.** Require pressing Enter; clear the list while a query runs.

**Used in.** Filter bar on every list; transcript viewer; global search in the header.
