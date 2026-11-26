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

## Card

`QuestionCard` in `packages/ui` ([OR-53](#rules-kept-here)). Board: [blocks.bl](../board/blocks.bl), master `Question card`. Demo: [book](../book/index.html#question-card).

The form a question takes on a [session card](session-card.md), in the conversation and in Telegram: the same record in every place.

* **Anatomy.** An `attention-soft` block with an `attention-line` border and `radius-md`: the question in `font-weight-medium`, then its choices as buttons in one row (the asker's default is primary), then the free-text answer as a one-line field that sends on `Enter`.
* **States.** Open; answering (the chosen button busy); answered (one line in `fg-muted`: "Answered · Compact · 2m", then it leaves the card); failed recovery (the danger tone, with Retry and Open, on a Failed card).
* **Confirmation.** A risky action asks through this card: it names its exact scope and the way back, and only a person allowed to approve that scope sees active buttons ([OR-61](../owner-rules.md)).
* **Do.** Keep the choices the asker gave, in their order. **Don't.** Show more than one question per card; the next one takes its place after the answer.

## Open question

`OpenQuestion` and `useAnswer` in `packages/ui` (`controls/open-question.tsx`, `answer.ts`); the app's writes come from `app/src/answers.ts:useAnswers`. Demo: [book](../book/index.html#open-question).

One open question answered in place, the same way on every screen: Home cards, Conversations, Projects and the session view show it as a [card](#card); the task detail and the Lab's ratings keep the full block with the same choices and answer state.

* **Choices.** `answerLabels`: the asker's options in their order (a string, or an object by its `label`, else its `value`), an acceptance's `accepted` and `rejected`, a key request's `decline`; the first is primary, except `decline`.
* **Key request.** A password field with Add key and decline, on every screen ([connections](../../designs/fleet-connections.md)): the value goes to `connections_key_add`, never to `questions_answer`.
* **Outcome.** Offline, the answer is not sent and the card says so; an unknown outcome says "retrying" while `writeOnce` resends it; a refusal shows its reason under the card. Only a returned write makes the question answered.

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **OR-53 Questions are actions.** *Target.* A question from an agent is a card with its choices as buttons and a free-text answer. The answer edits the card in place. The same question shows on the session's card on Home and in Telegram.
* **UR-24.** Decision shortcuts (for example `a` accept, `r` request rework) work only while the decision has focus and show in the button. *Check:* Browser: focus a decision, press its letter, expect the action; press it elsewhere, expect none.
* **UR-26.** A question, acceptance or approval shows in the conversation that asks it, on that session's Home card and in Telegram, as views of one record ([OR-53](#rules-kept-here), [OR-40](../patterns/needs-you.md#rules-kept-here)); answering in one place updates the others within one update. *Check:* Browser: answer on Home, open the session, expect the answered card.
* **UR-27.** A decision shows who it is addressed to, why the work waits and how long it has waited. *Check:* Review.
* **UR-28.** Picking an offered answer takes one click or one key; a free-text answer is always offered. *Check:* Browser: answer a question with one click.
* **UR-29.** After an answer the card says "Answered by …" and whether delivery is done or pending; a shown action is not proof that it completed. *Check:* Browser: answer, expect the delivery state on the card.
* **UR-58.** (merged with OR-53) A question is answered in place on its card; the answer never needs another page ([OR-53](#rules-kept-here)). *Check:* Browser: answer a question; the URL stays the same.
