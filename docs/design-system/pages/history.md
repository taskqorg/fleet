# History

**App routes:** `/history` in the operator app (`app/src/views/history.tsx`). Board: [screens/history.bl](../board/screens/history.bl) (option A with the panel and the Lab empty pane, approved by the owner on 2026-11-26).

**Purpose.** See what happened in Fleet, newest first: runs starting and ending, tasks created and moving state, questions asked and answered, experiments, settings and access changes. Narrow it to a section or one object. Design: [history](../../designs/fleet-history.md).

**Layout.** [App shell](../patterns/app-shell.md) and one scrolling page, no control column (OR-24: no catalogue), like Live.
* **Scope:** `All › <section or object>` at the top. The last step has × and removing it widens the scope. The scope is in the address (`?section=lab`, `?taskId=…`, `?sessionId=…`, `?experimentId=…`, `?hostId=…`, `?botId=…`), so a section or an object links to its own history. An object's chip shows its name (`scopeTitle`: a task's code and title, a conversation's title, an experiment's lab group), not its id.
* **Focus:** the header's project focus narrows the list as on Live (`projectIds`, else the ignored projects as `excludeProjectIds`).
* **Filters:** kind chips (All, Runs, Tasks, Questions, Lab, Settings, Access) and a Failed toggle, also kept in the address.
* **Days:** a heading where the day changes: Today, Yesterday, then the date.
* **Rows:** the [live row](../controls/live-row.md) of Live's Just now: state icon, the subject and what happened, then the [work facts](../controls/work-facts.md) (×N for folded repeats, standing out against the page's rows, and the actor), and the age last ([OR-64](../controls/work-facts.md#rules-kept-here)). A row opens its task, experiment, conversation or host. A run with no task, conversation title or experiment reads `<harness> run`. An experiment's trials are one row per experiment in a page, however their starts and ends alternate: `dam-doc-recognition: 24 trials, 22 finished, 2 failed`. At phone width the age stays last on the first line, and the actor and ×N wrap below it. The list loads older entries on scroll ([OR-65](../patterns/infinite-list.md#rules-kept-here)).

**Elsewhere.**
* A task's History tab is this list scoped to the task (`taskId`); a row opens its conversation.
* Live's Just now is its newest 5 entries in the focus; the heading opens this page.
* Lab: the History view chip opens `/history?section=lab`. With nothing selected, Lab's detail pane shows the newest 8 entries under Waiting for you, and the heading opens the same page.
* Phone: History is the third tab of the bottom bar, and Projects moves to More.

**Data.** `history_view`: one read. Its first page rereads on `activity` changes, and older pages load with `before`. Lab's empty pane takes its entries from `lab_view` (`history`), so Lab stays one read.
