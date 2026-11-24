# Composer

**Code.** `Composer`, `DropZone` and `MessageFiles` in `packages/ui`.

**Purpose.** Type a message to an agent, with files: start a turn, or steer or queue one while the agent is busy.

**Anatomy.** One bordered box, as in Orca, Claude Code and Codex ([decision](../decisions.md#2026-11-17-composer-as-one-box-message-attachments)):

* Attachment chips above the text, one per file: a thumbnail for an image or a file icon, the name, the size (the upload percentage while uploading, `failed` after a failure), and × to remove it. A thin accent bar under the chip shows the upload progress.
* The text area (2 lines, grows with the text up to 6).
* The bottom row: `+` (attach files), the chips of the target (the agent profile as the harness mark with model and effort, `opus-5.5 · high`, and the permissions the profile grants as a shield with `Workspace` or the profile's mode, plus `network` when it has git network access), then on the right one primary icon button: Send (arrow up), or Stop (square) while the agent runs and the box is empty.

**Behaviour.**
* `Enter` sends and `Shift+Enter` adds a line; a composition in progress (IME) never sends.
* Files come three ways: the `+` picker (several at once), a paste (Cmd/Ctrl+V of a screenshot, an image or files; pasted text longer than 4,000 characters becomes a `pasted-<n>.txt` file instead of filling the box), and a drop anywhere over the conversation (`DropZone`): while files are dragged over it, a dashed accent overlay says "Drop files to attach"; it covers the area and moves nothing (OR-23).
* Each file uploads as soon as it is added, in parts that resume after a dropped connection. Send waits for the uploads still running (the button shows busy) and then sends the text with the files; a failed file blocks Send until it is removed ("Remove the failed file"). A message may be files alone.
* The profile chip opens Switch profile or model (the existing `session_transition`); the permissions chip only shows, with the tools and network access in its tooltip. The app passes the chips; without them the row holds `+` and the button.
* Stop calls `execution_stop` on the running execution. Typing turns the button back into Send, which steers (or queues) the text; Stop stays in reach as a quiet icon beside Send while the draft is not empty ("Stop, steer and answer are always visible"). A message with files is queued, never steered: its files are staged before a turn starts.
* The app may keep a draft per validated identity and authorized target through the injected persistence adapter (specq F3).
* Offline, the box is dimmed and the text area, the chips and the button are disabled, with the reason "offline". Nothing is queued (fleet-operator-app D3).

**In the transcript.** An operator message shows its files under the text (`MessageFiles`): an image as a fixed 72 px thumbnail (its space is reserved, so a loading image moves nothing), any other file as a chip with icon, name and size. A click opens the file in the result viewer; Cmd/Ctrl-click opens it in a new tab.

**Accessibility.** The text area is labelled by its placeholder ("Message", or "Steer" while the agent runs). Every icon button has its name as tooltip and accessible name ("Attach files", "Send", "Stop", "Remove <file>"). On touch screens the text area, the chips and × are at least 44 px high.

**Used in.** Conversations: a conversation and the new-conversation page ([start picker](start-picker.md)), the same way: on the new page the start facts (project, agent, access, branch) are the chips of the bottom row, files upload into the chosen project before the session exists and go with the first message (`session_start.attachments`), and the header `+` there focuses the text.

**Do and don't.** Do keep one button for Send and Stop. Don't add a second Stop above the composer; a boundary row keeps its own Stop for that execution (CUJ 5). Don't upload when the message is sent: the files are already stored by then.

## Authorized persistence

The app injects a scoped persistence adapter after validating the identity and target. The shared control has no storage access; without an adapter it is memory-only. Closing the access gate quarantines authored unsent text under its original identity and target. Restore requires the same validated identity and freshly authorized target, including after changed grants. Other actors never inspect it. Legacy unscoped drafts remain quarantined without attribution or deletion.

Send completion clears only the originally submitted text if unchanged. Newer input and newer-generation records survive late success. Unknown submission outcomes retain the original request identity and body; reopening never automatically resends them. Storage failure preserves the authorized in-memory input and exposes a warning.

Submission metadata freezes the complete message/start body and SHA-256, including delivery mode and attachments. Explicit retries of unchanged authored text and files retain that exact body and request identity after fresh target authorization; the same text with other files, or files alone after other files, is a new submission (`api.ts:submissionKey`). A reload keeps the text but not the chips, so a resend there without the files is a new message. Editing another submission keeps unresolved metadata under the original scope; returning to its text reuses its original identity.

## Target picker

*Target* ([OR-50](../owner-rules.md)). Board: [blocks.bl](../board/blocks.bl), masters `Composer` and `Target picker`.

On [Home](../pages/home.md) the composer starts with a target chip that picks who to talk to.

* **Chip.** The target's avatar and name (`PM · fleet`) with a caret, first in the box, left of the text. It starts on the last session the person talked to from Home.
* **Picker.** Opens under the chip as an informational floating list ([OR-14](../owner-rules.md), [OR-16](../owner-rules.md)) with a search field first. Groups in order: project PMs, bots with their [state](state-badge.md#dot-and-word), then "New session with a preset…" ([start picker](start-picker.md)). Typing filters by name and project; arrow keys move, `Enter` picks, `Escape` closes.
* **Send.** `Enter` sends and opens that session. A message to an asleep bot wakes it with its memory; a message to a project PM without an open session starts one from the project's PM preset.
* **Same as Telegram.** The picker is the one Telegram uses for `/pm` and `@bot` ([FLT-31](https://youtrack.ufobe.com/issue/FLT-31)).
