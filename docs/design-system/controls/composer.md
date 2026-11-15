# Composer

**Code.** `Composer` in `packages/ui`.

**Purpose.** Type a message to an agent: start a turn, or steer or queue one while the agent is busy.

**Anatomy.** A text area (2 lines, grows up to 6) and a primary Send button.

**Behaviour.**
* `Enter` sends and `Shift+Enter` adds a line.
* The app may keep a draft per validated identity and authorized target through the injected persistence adapter (specq F3).
* While a send is in progress the button shows busy.
* Offline, both the text area and the button are disabled, with the reason "offline". Nothing is queued (fleet-operator-app D3).

**Accessibility.** The text area is labelled by its placeholder ("Message", or "Steer or queue a message" while the agent runs). On touch screens it is at least 44 px high.

**Used in.** Conversations.

## Authorized persistence

The app injects a scoped persistence adapter after validating the identity and target. The shared control has no storage access; without an adapter it is memory-only. Closing the access gate quarantines authored unsent text under its original identity and target. Restore requires the same validated identity and freshly authorized target, including after changed grants. Other actors never inspect it. Legacy unscoped drafts remain quarantined without attribution or deletion.

Send completion clears only the originally submitted text if unchanged. Newer input and newer-generation records survive late success. Unknown submission outcomes retain the original request identity and body; reopening never automatically resends them. Storage failure preserves the authorized in-memory input and exposes a warning.

Submission metadata freezes the complete message/start body and SHA-256, including delivery mode. Explicit retries of unchanged authored text retain that exact body and request identity after fresh target authorization. Editing another submission keeps unresolved metadata under the original scope; returning to its text reuses its original identity.
