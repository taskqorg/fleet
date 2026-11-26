# Needs you

**Job.** One place for everything waiting for this person: questions, acceptance, approvals and problems. Each is answered in place ([principles §6](../principles.md#6-decisions-inline), D13 of specq: attention first).

**Composition.** A panel with:
* items in urgency order, then the most recent first (OR-40): [decision blocks](../controls/decision-block.md) that block work, then [problem rows](../controls/problem-row.md), then acceptance decisions;
* the latest 3 (`Preview` in `packages/ui`); the title, with the count, opens the full list (OR-30, OR-65).

Each item names the work that waits ("T-9989 Write release notes 95"), which links to its detail.

**Rules.**
* It is the same record as the decision inside the task. Answering here updates the task at once, and the reverse.
* An answered item collapses to its answered line, then leaves the list on the next refresh.
* The navigation's "N need you" chip counts this list. The focus chip's "N need you outside focus" counts the items of this list outside the focus, never those of ignored projects ([focus chip](../controls/focus-control.md)); it opens the Overview without the focus (`/overview?all`).
* An empty list says "Nothing is waiting for you."

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **OR-40 Needs you first.** A question to the person shows on the card of the session that asks (OR-53); Home lists Waiting for you first, oldest first. Repeated problems collapse into one item (OR-58). *Target*; until Home is built, Overview "Needs you" sorts by urgency, then recency, and shows the latest 3; its title opens the full list (OR-30), and project and host rows follow OR-29.
