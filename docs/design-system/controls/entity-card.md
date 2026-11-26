# Entity reference and card

**Code.** `EntityCode` (the reference, in three forms by its `form` prop), `EntityCardLayer`, `EntityCards` and `linkCodes` in `packages/ui/src/controls/overlays.tsx`; kinds in `registry.ts:entityKinds`; the card read is the operation `entity_card` and the live state of many references the operation `entity_refs` (`go/coordinator/entity_card.go`), which agents call as MCP tools. The app's source is `app/src/entity-cards.ts`. Demo: [book](../book/index.html#entity-card). Board: [entity-references](../board/screens/entity-references.bl).

**Purpose.** Wherever the interface names an object (a task, a question, a conversation, a run, a result, an experiment, a project, a host, an agent, a pipeline, a pull request, a release), it shows a reference to it, never a bare word or id: what kind it is, what state it is in now, its human code. Resting the pointer on it shows that object's card with the details and the way to go to it, like the hover card GitHub shows on `taskqorg/fleet #643`. The state a reference shows is live: when the object changes, every reference to it on the screen changes in place.

## The reference

**Forms.** One control, `EntityCode`, three forms (`form` prop). Each shows the same object the same way wherever it appears (OR-28); a screen picks the form by what is around the reference.

| Form | Shows | Use it where |
|---|---|---|
| `code` | the human code in mono and the copy slot | the object's own code beside its own state and title: a list row's code (the row already shows its state dot and title), a detail header, a card's own facts; a column of codes in a dense table |
| `chip` (default) | the kind icon in the tone of the object's state, then the code | a reference to another object in a row, a detail fact, a transcript row and running text (agent messages, `linkCodes`): the reader sees what it is and whether it is done without opening it |
| `full` | the kind icon in its state's tone, the code, the title (one line, truncated; left out when it is the code), and the state badge | a related object shown on its own line inside a detail (a task's result, a release's pull requests, a conversation's tasks), a Needs-you item that names one object |

* A kind without a code (run, experiment, a result without a code) shows its kind icon; in `chip` the icon carries the state tone and a short label (`Run`); in `full` the title follows.
* Kinds without a state (agent, pipeline) show the kind icon in the neutral tone. A project shows its 16 px [project tile](project-tile.md) in its identity colour in place of the kind icon (the app sets the colours from `project_list`).
* The state is never colour alone: the accessible name and the tooltip-free card carry its word; `full` shows the word in its badge.
* The copy slot (`code` and `chip` with a code): a slot reserved immediately to the right; hover, keyboard focus or touch reveals Copy, which writes the displayed code and briefly replaces the icon with a check. The slot keeps its size (OR-23).
* A reference never becomes a plain link or plain text, and no screen draws its own code chip (the check-design warning below). One exception: a row that is itself a link to its entity (a project overview line, a live row) shows its own code as text inside that link, since a button cannot sit inside a link; the row opens the entity.

**Live state (OR-6).** A reference shows the state the server has now. When the object's state or title changes while the reference is on screen (a conversation, a list, a detail, an open card), the reference updates in place: the icon's tone and the badge change, the width does not (the badge slot is fixed per family), and the change tints briefly in the neutral tone, green or red only where the state means success or failure (merged, failed). Nothing moves around it (OR-23). A reference that is no longer visible to the reader (forbidden, deleted) keeps its code and loses the tone (neutral), and its card says why.

## The card

**Anatomy.** Kind icon and code, the state badge on the right (registry family of the kind); the title in bold, up to three lines; one muted line with the project, the harness mark and the compact time with what it is (`changed 4m`, OR-2); the kind's preview, if it has one; one to four facts as label and value; the actions.

* **Preview.** A result that is an image shows its thumbnail (or the image itself when it has no thumbnail and is small) at most 240 × 160 px, `object-fit: contain` on the inset surface; a PDF shows its first page's thumbnail; a video shows its poster with the play icon. The preview reserves its box while it loads (OR-23) and is left out when the result has none. A click on the preview opens the result, as Open does.
* **Per kind.** Each kind has its own card: its own facts, its own actions, and its own preview (only results have one today). A pull request card shows the delivery stage, the repository and the release; a result card shows the picture, the type and the size. The table in [Data](#data) lists every kind.
* **Actions**, left to right: **Open** (primary, first), then Copy code (a pull request: Copy reference, `owner/repo#n`), Copy UUID (only kinds that have one), Copy id (a kind with neither, a host), Copy SHA-256 (a result), and Open on GitHub (a pull request). Each copy is a `CopyButton`: its icon turns into a check (a cross on failure) and the result is announced; the row keeps its width (OR-23).

## Behaviour

* **Hover (a fine pointer).** Resting the pointer on the reference for 500 ms shows its card (a *peek*). Moving the pointer into the card keeps it; leaving both the reference and the card for 300 ms hides it. Passing over references on the way somewhere shows nothing (the delay restarts on each one). While one card is shown, moving to another reference shows the other after 150 ms.
* **Click pins.** A click, Enter or Space on the reference opens its card pinned; a click on a peek pins it; the same again closes it. A pinned card stays until Escape, a click outside, focus moving out of it and the reference, the reference again, or the reference going out of sight. The click never opens the row the reference sits in (OR-26) and never navigates by itself.
* **Touch (a coarse pointer).** There is no hover: a tap opens the card pinned; a tap outside or on the reference again closes it.
* **Keyboard focus** alone opens nothing (a peek on focus would cover the list being tabbed through); Enter or Space opens it pinned.
* One card at a time: showing another closes the first.
* Placement: under the reference, left-aligned with it; above it when there is no room below; always inside the window with an 8 px edge. It follows its reference when a column scrolls (also a live list keeping its anchor) and is placed again when it grows from the skeleton to the loaded card or the window resizes. It floats over the page and moves nothing (OR-23). A peek never covers the reference it hangs from, so the pointer can always reach the card across a 4 px gap.
* Loading shows a skeleton of three lines in the body's place; a failed read shows the reason ("Not found, or not visible to you" for an unknown or forbidden entity) with Retry. Open is disabled until the card has a path; the copy of the code works at once. A reference that already has the live state shows its kind, code, state and title at once, before the card's facts load.
* Open goes to the entity in the work area (OR-27); a modified click (new tab) follows the link. From the card the person always reaches the full detail view; the card never replaces it.
* D17 codes of tasks, questions, conversations and results in an agent message (`Markdown`) are references too (`chip`), and so are a GitHub pull request link or `owner/repo#n` and a UUID: the server finds which run, task, conversation, result, question or experiment it names; a UUID that names nothing the person may see shows only Copy id, never itself (OR-38). Codes inside inline code, fenced code and link text stay text, except a UUID in inline code, which is a reference too (it is never shown). Spec rule codes (`S-…`) are not entities.
* A conversation title never shows a raw link: until it has a title, its first message names a pull request link as `owner/repo#n` and any other link by its host and last path part.

## Data

**Card.** `entity_card {code}` or `{kind, id}` returns `kind, id, code, uuid, hash, title, state, projectId, harness, at, atLabel, facts[], preview, path, url` (contract `EntityCardReply` in `contracts/ops-tasks.schema.json`); `preview` is `{resultId, view}` (`view` image, pdf or video) of the bytes to show, read from `/api/results/<id>`. Grants: row-level security and each kind's own gates (definitions and Lab for architects, as their lists), so an unknown and a forbidden entity both answer 404. The app keeps each card as a store doc (`entity-card:<kind>:<id>`, memory and IndexedDB) and reads it again when the change feed touches that entity.

**Live state of many references.** `entity_refs {refs: [{kind, id} | {code} | {id}]}` (at most 200 per call) returns, in one read, `{items: [{ref, kind, id, code, state, title}]}` for the references the caller may see; a forbidden or unknown one is left out (the reference shows it as not visible). Its codes resolve in one query and each entity is read by its kind's card reader inside the one request, under the same grants as `entity_card`; the same entity twice is read once.

The app holds one live-state registry (`app/src/entity-cards.ts`):

* Every rendered reference registers its entity when it mounts and releases it when it unmounts; references to the same entity share one entry (a count). A virtual list unmounts what scrolls away, so only what is rendered is held.
* New entries are read together: all references that mount in one frame become one `entity_refs` call (split by 200), never one read per reference and never a poll.
* The change feed (the one SSE stream every screen already has) is matched against the registry by entity key (`kind:id`), so a batch of changes costs a map lookup per change, not a test per reference. Each matched entry is marked dirty; dirty entries are read again in one `entity_refs` call at most once per second (a run's totals change many times a second). Changes that name no single entity (delivery) mark every pull request and release entry dirty.
* A hidden tab reads nothing; it reads its dirty entries once when it shows again. A resync of the feed marks every entry dirty.
* An open card's reply also updates its entry, and the entry's state seeds the card's header, so the reference and its card never disagree.

Budget (measured in the book's load page and on prod): a conversation with 200 references mounts with one `entity_refs` call, a feed batch of 100 changes costs under 1 ms of matching, and a pull request merging updates every reference to it within one feed frame plus one read.

| Kind | Reference | State (family) | Title | Line | Preview | Facts (zero counts hidden, at most four) | Actions |
|---|---|---|---|---|---|---|---|
| Task | `T-FLT-42` | task, Held included | title | project, harness of the running or last run, changed | none | Waiting for (waiting, held or paused), Runs, Open questions, Host, Priority (not normal) | Open, code, UUID |
| Question | `Q-FLT-7` | question | text (200 characters) | project, asked | none | Asked by, Task, Answer, Answered by | Open (its task, else its conversation), code, UUID |
| Conversation | `C-FLT-311` | session | first message | project, harness, active | none | Profile, Task, Runs, Open questions | Open, code, UUID |
| Run (execution) | kind icon | execution | its task's title, else the run kind | project, harness, ended or started | none | Problem, Task, Conversation, Host, Model, Duration | Open (conversation, task, experiment, else Runners), UUID |
| Result | `R-FLT-9`, else kind icon | artifact | file name | project, stored | image thumbnail, PDF first page, video poster | Task, Type, Size | Open, code, UUID, SHA-256 |
| Experiment | kind icon | experiment | goal, else pipeline | project, launched or created | none | Pipeline, Trials, Variants, Decision | Open, UUID |
| Project | project code | none | name | changed | none | Open tasks, Running, Open questions | Open, code |
| Host | host id | host (online, offline, polls failing) | id | seen | none | Version, Running, Queued, Platform | Open, id |
| Agent (profile) | profile id | none | name | harness, changed | none | Model, Effort, Revision | Open, code |
| Pipeline | pipeline id | none | name | changed | none | Steps, Revision | Open, id or code |
| Pull request | `#643` | delivery stage (from history as Delivery puts it: releasing, installing, installed or merged) | title | project, since | none | Repository or Author (history), Release, Opened, Merged | Open (Delivery), reference, GitHub |
| Release | tag, known to the delivery pass or history or run by a host or the site (else 404) | delivery (installing, installed, as the delivery pass decides) | Release 1.2.3, UI release 1.2.3 | none | none | Hosts (`2 of 3` seen in the hour with a version), Site serves (UI-only), Pull requests | Open (Delivery filtered by the tag), code |

Not covered, by decision:

* Spec rule codes (`S-FLT-D3`): rules, not entities; nothing in Fleet stores them yet.
* Spotlight rows: the palette is its own floating tool (OR-32); its rows open the entity directly.
* The profile id in the conversation header stays text: agent definitions are the architect's to read, so an operator's card would only say "not visible". The agent card serves architects and agents (Agents section references, MCP).
* Live counts that no change names: a project's and an experiment's counts are read again each time the card opens, not on every task or run change.
* The author avatar of the GitHub card: Fleet does not keep pull request authors in the delivery pass; the Delivery detail shows the author from history.
* Pull request checks beyond the stage: the stage already says PR checks, Checks failed, Queue checks or Queue failed; a check list would need a GitHub read per card.
* Needs-you and problem rows: their text names hosts and harnesses as words, not codes; they open their item already.
* Price ids (Resources) and other ids that name no entity: a `CopyButton`.

**Keyboard.** Tab reaches the reference; Enter or Space opens the card pinned and moves focus to Open; Tab moves through the actions; Escape, Tab past the last action, Shift+Tab before the first and Open close it and return focus to the reference. A pointer open (peek or click) leaves focus where it was.

**Accessibility.** The reference is a button, `aria-haspopup="dialog"`, `aria-expanded`, `aria-controls` while open, named with its kind, code and state ("Pull request #643, merged: show details"). The card is a non-modal `role="dialog"` named by kind and code (OR-14 allows it: it holds information and the open and copy actions only, no form). A peek is the same dialog; it never takes focus. States are badges with their word; a live state change is not announced (it would talk over the conversation), the new state is in the name. Copy results are announced by a status region. Touch: the reference is 44 px high on coarse pointers, the buttons too. The preview image has the file name as its `alt`.

**Do.** Render every reference to another object with `EntityCode` and its `kind`, in the form the table above names, so it gets the live state and the card. **Don't.** Show a UUID or hash as text (OR-38); draw a code in a hand-made chip, link or `mono` span; open the row from the code; put inputs in the card; poll a reference's state.

**Check.** `pnpm run check:design` warns (help, not block) where markup shows an entity's code by hand: a `code`, `taskCode`, `sessionCode` or `number` value in an `id` or `mono` span outside `overlays.tsx` (the link rows of the exception above are skipped).

**Used in.** List rows (Tasks, Conversations, Projects: `code`), detail headers (task, pull request, the conversation's Session: `code`), task detail (result `full`, dependencies `chip`), conversation header (active run `chip`), transcript boundary rows (run `chip`, beside its state badge), agent messages (`chip`), Results rows (task `chip`), Resources (spawned tasks `chip`), Hosts (runs `chip`), Runners (parent run `chip`), Lab (experiment, trial `chip`), Delivery detail (release `chip`, pull requests `full`), Delivery's Needs a look (the pull request of a stuck or failed row `full`, the pull requests a slowdown first went over with `chip`), Needs you (the object asked about, `chip`).

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **OR-44 Entity reference and card.** Wherever the interface shows an object (task, question, conversation, run, result, experiment, project, host, agent, pipeline, pull request, release), it shows a reference: kind icon, live state and human code, in one control, `EntityCode`, in one of three forms: `code` (code and copy), `chip` (icon in the state's tone and code, the default) and `full` (icon, code, title and state badge). Resting the pointer on it for half a second shows its card, leaving hides it, and a click pins it (GitHub's hover card); a tap opens it on touch. Each kind has its own card: state, bold title, project, agent and compact time, a preview where the object is a picture or a document, a few facts, then Open first and the copy actions; from the card the person goes to the detail view. The state a reference shows is live: when the object changes, every reference to it on the screen updates in place (OR-6) through one batched read, never a poll per reference. The card is an informational floating layer (OR-14, OR-16), one at a time, and moves nothing (OR-23). See [entity card](../controls/entity-card.md).
