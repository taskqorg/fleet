# History

**App routes:** `/history` in the operator app (`app/src/views/history.tsx`). Board: [screens/history.bl](../board/screens/history.bl) (option A with the panel and the Lab empty pane, approved by the owner on 2026-11-26).

**Purpose.** See what happened in Fleet, newest first: runs starting and ending, tasks created and moving state, questions asked and answered, experiments, settings and access changes. Narrow it to a section or one object. Design: [history](../../designs/fleet-history.md).

**Layout.** [App shell](../patterns/app-shell.md) and one scrolling page, no control column (OR-24: no catalogue), like Live.
* **Scope:** `All › <section or object>` at the top. The last step has × and removing it widens the scope. The scope is in the address (`?section=lab`, `?taskId=…`, `?sessionId=…`, `?experimentId=…`, `?hostId=…`, `?botId=…`), so a section or an object links to its own history.
* **Filters:** kind chips (All, Runs, Tasks, Questions, Lab, Settings, Access) and a Failed toggle, also kept in the address.
* **Rows:** the [live row](../controls/live-row.md) of Live's Just now: state icon, the subject and what happened, then the [work facts](../controls/work-facts.md) (×N for folded repeats, standing out against the page's rows, and the actor), and the age last ([OR-64](../owner-rules.md)). A row opens its task, experiment, conversation or host. The list loads older entries on scroll ([OR-65](../owner-rules.md)).

**Elsewhere.**
* Lab: the History view chip opens `/history?section=lab`. With nothing selected, Lab's detail pane shows the newest 8 entries under Waiting for you, and the heading opens the same page.
* Phone: History is the third tab of the bottom bar, and Projects moves to More.

**Data.** `history_view`: one read. Its first page rereads on `activity` changes, and older pages load with `before`. Lab's empty pane takes its entries from `lab_view` (`history`), so Lab stays one read.
