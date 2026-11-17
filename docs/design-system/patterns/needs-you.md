# Needs you

**Job.** One place for everything waiting for this person: questions, acceptance, approvals and problems. Each is answered in place ([principles §6](../principles.md#6-decisions-inline), D13 of specq: attention first).

**Composition.** A panel with:
* items in urgency order, then the most recent first (OR-40): [decision blocks](../controls/decision-block.md) that block work, then [problem rows](../controls/problem-row.md), then acceptance decisions;
* the latest 3, then a `+K` button that shows the rest in place (OR-30, `Latest` in `packages/ui`).

Each item names the work that waits ("T-9989 Write release notes 95"), which links to its detail.

**Rules.**
* It is the same record as the decision inside the task. Answering here updates the task at once, and the reverse.
* An answered item collapses to its answered line, then leaves the list on the next refresh.
* The navigation's "N need you" chip counts this list. The focus chip's "N need you outside focus" counts the items of this list outside the focus, ignored projects included ([focus chip](../controls/focus-control.md)); it opens the Overview with every project (`/overview?all`).
* An empty list says "Nothing is waiting for you."
