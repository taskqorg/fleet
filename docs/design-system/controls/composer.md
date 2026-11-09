# Composer

**Code.** `Composer` in `packages/ui`.

**Purpose.** Type a message to an agent: start a turn, or steer or queue one while the agent is busy.

**Anatomy.** A text area (2 lines, grows up to 6) and a primary Send button.

**Behaviour.**
* `Enter` sends and `Shift+Enter` adds a line.
* The draft is kept per conversation in the browser, so updates and reloads never lose it (specq F3).
* While a send is in progress the button shows busy.
* Offline, both the text area and the button are disabled, with the reason "offline". Nothing is queued (fleet-operator-app D3).

**Accessibility.** The text area is labelled by its placeholder ("Message", or "Steer or queue a message" while the agent runs). On touch screens it is at least 44 px high.

**Used in.** Conversations.
