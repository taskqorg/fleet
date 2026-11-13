# Decision block

**Code.** `DecisionBlock` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Lets a person answer a question, acceptance or approval where the work is, and in the Overview list of everything waiting ([principles §6](../principles.md#6-decisions-inline)).

**Anatomy.** Block with `attention-soft` fill and a 3 px `attention-fg` bar on the left, like the [problem row](problem-row.md) (D19):
1. Heading: the question ("Accept this result?").
2. Meta line in `fg-muted`: asked by, addressed to, waiting time as an age chip (OR-18), what waits on it.
3. Context: short summary and links to evidence (result, checks).
4. Answers: option buttons (first is primary when the asker marked a default), a free-text field "Answer", and a send button that appears when text is entered.

**Variants.** *Full* (task detail); *row* (Overview list: question, project, waiting time, option buttons inline; free text opens the full block).

**States.** Open; answering (buttons busy); answered ("Answered by Alex · Accept · delivered 10:42" in `success-fg`, block turns neutral and collapses to one line); delivery pending ("Answer saved, delivery pending" in `attention-fg`); not addressed to you (read only, shows the addressee); withdrawn (asker no longer needs it, neutral).

**Behaviour.** One click or one key answers with an option. Typed answers need `Enter` (`Shift+Enter` for a new line). Answering in one place updates the other views of the same question at once. In a conversation, a confirmed reply immediately updates transcript history and removes the current answer controls even while live updates are delayed. Failed or unknown writes do not establish an answered state. Destructive answers (cancel) confirm in place.

**Keyboard.** Within the focused block: option shortcuts shown in each button (`a`, `r`, digits `1`–`9` for numbered options); `t` focuses the text field.

**Accessibility.** `role="group"` named by the question; meta line linked by `aria-describedby`; state changes announced politely.

**Do.** Show who and what is waiting. **Don't.** Open a modal for an ordinary answer; hide the free-text option.

**Used in.** Task detail, Sessions, Overview waiting list, Laboratory recommendations, setting proposals from agents.
