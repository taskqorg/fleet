# Entity reference and card

**Code.** `EntityCode`, `EntityCardLayer`, `EntityCards` and `linkCodes` in `packages/ui/src/controls.tsx`; kinds in `registry.ts:entityKinds`; the read is the operation `entity_card` (`go/coordinator/entity_card.go`), which agents call as an MCP tool. The app's source is `app/src/entity-cards.ts`. Demo: [book](../book/index.html#entity-card).

**Purpose.** Wherever a detail names another object by its code, the person can see what that object is without leaving the page, and go to it. Like the hover card GitHub shows on `taskqorg/fleet #643`: kind and code, state, a bold title, who and when, a few facts.

**Anatomy.**

* The reference: the human code in mono (`T-FLT-42`, a pull request's `#643`, a release tag, a profile id) right after the name (OR-22), or, for a kind without a code (run, result, experiment), its kind icon. It is a button with the tooltip "Show details".
* The card: kind icon and code, the state badge on the right (registry family of the kind); the title in bold, up to three lines; one muted line with the project, the harness mark and the compact time with what it is (`changed 4m`, OR-2); one to four facts as label and value; the actions.
* Actions, left to right: **Open** (primary, first), then Copy code (a pull request: Copy reference, `owner/repo#n`), Copy UUID (only kinds that have one), Copy id (a kind with neither, a host), Copy SHA-256 (a result), and Open on GitHub (a pull request). Each copy is a `CopyButton`: its icon turns into a check (a cross on failure) and the result is announced; the row keeps its width (OR-23).

**Behaviour.**

* A click, a tap, Enter or Space on the reference opens its card; the same again closes it. Hover only shows the pointer and the short tooltip. The click never opens the row the reference sits in (OR-26).
* One card at a time: opening another closes the first.
* It closes on Escape, a click outside, focus moving out of it and the reference, or the reference going out of sight (clipped by its column, scrolled out of a virtual list, gone with the view).
* Placement: under the reference, left-aligned with it; above it when there is no room below; always inside the window with an 8 px edge. It follows its reference when a column scrolls (also a live list keeping its anchor) and is placed again when it grows from the skeleton to the loaded card or the window resizes. It floats over the page and moves nothing (OR-23).
* Loading shows a skeleton of three lines in the body's place; a failed read shows the reason ("Not found, or not visible to you" for an unknown or forbidden entity) with Retry. Open is disabled until the card has a path; the copy of the code works at once.
* Open goes to the entity in the work area (OR-27); a modified click (new tab) follows the link.
* D17 codes of tasks, questions and conversations in an agent message (`Markdown`) are references too; codes inside inline code, fenced code and link text stay text. Spec rule codes (`S-…`) are not entities.

**Data.** `entity_card {code}` or `{kind, id}` returns `kind, id, code, uuid, hash, title, state, projectId, harness, at, atLabel, facts[], path, url` (contract `EntityCardReply` in `contracts/ops-tasks.schema.json`). Grants: row-level security and each kind's own gates (definitions and Lab for architects, as their lists), so an unknown and a forbidden entity both answer 404. The app keeps each card as a store doc (`entity-card:<kind>:<id>`, memory and IndexedDB) and reads it again when the change feed touches that entity (`cardTouched`).

| Kind | Reference | State (family) | Title | Line | Facts (zero counts hidden, at most four) | Actions |
|---|---|---|---|---|---|---|
| Task | `T-FLT-42` | task, Held included | title | project, harness of the running or last run, changed | Waiting for (waiting, held or paused), Runs, Open questions, Host, Priority (not normal) | Open, code, UUID |
| Question | `Q-FLT-7` | question | text (200 characters) | project, asked | Asked by, Task, Answer, Answered by | Open (its task, else its conversation), code, UUID |
| Conversation | `C-FLT-311` | session | first message | project, harness, active | Profile, Task, Runs, Open questions | Open, code, UUID |
| Run (execution) | kind icon | execution | its task's title, else the run kind | project, harness, ended or started | Problem, Task, Conversation, Host, Model, Duration | Open (conversation, task, experiment, else Runners), UUID |
| Result | kind icon | artifact | file name | project, stored | Task, Type, Size | Open, UUID, SHA-256 |
| Experiment | kind icon | experiment | goal, else pipeline | project, launched or created | Pipeline, Trials, Variants, Decision | Open, UUID |
| Project | project code | none | name | changed | Open tasks, Running, Open questions | Open, code |
| Host | host id | host (online, offline, polls failing) | id | seen | Version, Running, Queued, Platform | Open, id |
| Agent (profile) | profile id | none | name | harness, changed | Model, Effort, Revision | Open, code |
| Pipeline | pipeline id | none | name | changed | Steps, Revision | Open, id or code |
| Pull request | `#643` | delivery stage (from history: merged or installed) | title | project, since | Repository or Author (history), Release, Opened, Merged | Open (Delivery), reference, GitHub |
| Release | tag, known to the delivery pass or history or run by a host or the site (else 404) | delivery (installing, installed, as the delivery pass decides) | Release 1.2.3, UI release 1.2.3 | none | Hosts (`2 of 3` seen in the hour with a version), Site serves (UI-only), Pull requests | Open (Delivery filtered by the tag), code |

Not covered, by decision:

* Spec rule codes (`S-FLT-D3`): rules, not entities; nothing in Fleet stores them yet.
* Spotlight rows: the palette is its own floating tool (OR-32); its rows open the entity directly.
* The profile id in the conversation header stays text: agent definitions are the architect's to read, so an operator's card would only say "not visible". The agent card serves architects and agents (Agents section references, MCP).
* Live counts that no change names: a project's and an experiment's counts are read again each time the card opens, not on every task or run change.
* The author avatar of the GitHub card: Fleet does not keep pull request authors in the delivery pass; the Delivery detail shows the author from history.
* Pull request checks beyond the stage: the stage already says PR checks, Checks failed, Queue checks or Queue failed; a check list would need a GitHub read per card.
* Needs-you and problem rows: their text names hosts and harnesses as words, not codes; they open their item already.
* Price ids (Resources) and other ids that name no entity: a `CopyButton`.

**Keyboard.** Tab reaches the reference; Enter or Space opens the card and moves focus to Open; Tab moves through the actions; Escape, Tab past the last action, Shift+Tab before the first and Open close it and return focus to the reference. A pointer open leaves focus where it was.

**Accessibility.** The reference is a button, `aria-haspopup="dialog"`, `aria-expanded`, `aria-controls` while open, named "Task T-FLT-42: show details". The card is a non-modal `role="dialog"` named by kind and code (OR-14 allows it: it holds information and the open and copy actions only, no form). States are badges with their word; copy results are announced by a status region. Touch: the reference is 44 px high on coarse pointers, the buttons too.

**Do.** Render every reference to another object with `EntityCode` and its `kind`, so it gets the card. **Don't.** Show a UUID or hash as text (OR-38); open the row from the code; put inputs in the card.

**Used in.** List rows (Tasks, Conversations, Delivery), task detail (code, result, dependencies), conversation header (code, active run), transcript boundary rows (conversation, run), agent messages, Results rows (task), Resources (spawned tasks), Hosts (runs), Runners (parent run), Lab (experiment, trial), Delivery detail (release).
