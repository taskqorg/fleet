# Fleet Telegram channel

People work with Fleet from Telegram the same way as in the web app: by talking to sessions ([UC13](../use-cases.md#uc13)). The channel is one more interface over the existing operations (R2, R7): a Telegram message becomes an ordinary `message_send` by the person who wrote it, with source `telegram`. The owner chose this design on 2026-11-24 (FLT-31, option b: rooms, work items, a conductor per room, confirmation for radical actions). Code: `go/coordinator/telegram.go`; operations: `contracts/ops-telegram.schema.json`.

## Glossary

- **Bot:** the one Fleet Telegram bot. Its token is the key of the `telegram` connection; only fleet-gateway holds it.
- **Linked person:** a Fleet person with a Telegram user id. Only linked people reach Fleet.
- **Room:** a private chat, a group or a forum topic the bot has seen. A room has a project hint, never a hard binding. People create and name topics; Fleet never creates or deletes them.
- **Home conversation:** a person's own conversation for their private chat with the bot.
- **Work item:** one piece of work in a group room; one Fleet conversation, usually with a task the conversation files.
- **Conductor:** the part of the channel that attaches each group message to one work item.

## Requirements

- **Identity.** A sender counts only when a Fleet person carries their Telegram id; what they may do comes only from that person's Fleet roles and grants. A message is sent by its author's Fleet person, never by a relay identity. Anyone else is ignored: no reply, no session, no question; the journal keeps the update with outcome `unknown-sender` for channel health only.
- **Lossless intake.** Every Telegram update is committed to the journal before Telegram is told it was received; a restart neither loses nor repeats one (pilotq L15, L16).
- **Verbatim delivery.** The text a person wrote reaches the session unchanged. The conductor chooses where it goes and never rewrites it.
- **Quiet chat.** Only replies, questions and Fleet's short notices reach Telegram. Tool output, progress, heartbeats and subagent results stay in Fleet. No footers.
- **Language.** Each linked person has a language (Telegram's tag at link time, changeable). Fleet's own notices use it; agents answer in the language the person writes.
- **Keys.** The bot token is an access entry the owner pastes once in System › Access; Fleet probes it with `getMe` and shows a failure in plain words. Agents never type it.

## Architecture

```text
Telegram ⇄ fleet-gateway (telegram connection, key in the URL path /bot<key>)
          ⇅ coordinator: poll → telegram_update (journal) → intake → message_send as the person
                         reply ← execution ends ← telegram_turn
                         telegram_outbox → send, one item at a time per chat
```

### Gateway

The `telegram` catalogue entry (`go/coordinator/providers.go`) sets `keyPath: "/bot"`: the gateway builds `baseUrl + /bot + key + path` and sends no key header (`contracts/gateway.schema.json` `GatewayProvider.keyPath`). Tools: `get_me`, `get_updates`, `send_rich_message`, `send_rich_message_draft`, `send_message`, `edit_message_text`, `set_message_reaction`, `send_chat_action`, `answer_callback_query`, `set_my_commands`. Every call is a `tool_run` of the coordinator's read token (system activity, excluded from analytics by default). Send tools mark 403 (a user blocked the bot) as a call result, not a key problem. The key never reaches a log: provider replies are redacted as for every connection, and transport errors are not logged.

### Intake

- **Poll** (`telegram-poll`, every 2 s): while the connection takes calls, a `getUpdates` long poll (20 s) with `offset` one past the highest journal row. New updates are inserted (`ON CONFLICT DO NOTHING` on `update_id`) in one transaction; the next poll's offset confirms them.
- **Route** (kit queue `telegram-intake`, one row per update, in `update_id` order):
  1. `/start <code>` links the sender to the person who made the code (`telegram_link_code`, one use, one day).
  2. Unlinked sender: ignored, outcome `unknown-sender`.
  3. The room row is created or refreshed; a 👀 reaction is queued as the receipt (L01).
  4. Private chat: the message goes to the person's home conversation; the first message starts it (`session_start`, preset `discuss`, outside projects). `/new` starts a fresh one with the next message.
  5. Group or topic: the conductor (below).
- **Delivery** runs after the routing step commits, in the person's own scope (`scopedRequest`), with request id `tg:<update_id>`: a repeat after a crash is the same request and delivers once. A refused delivery (no access to the project, a held task) answers "Not delivered: <reason>" in the room.

### Conductor (group rooms)

In order:

1. A Telegram reply to a Fleet message or to a routed human message goes to that message's work item (no model).
2. A room with no open work item, or a message that opens with "Новая задача", "Другой вопрос", "New task" (the person's way to say "start fresh"): the message opens one, titled by its first line; the room gets "New: <title>".
3. A short message (up to 120 characters) that refers back ("этих", "его", "it", "them") within 5 minutes of the author's last routed message in the room goes to that message's work item, unless it opens with a new-work cue ("Новая задача", "Другой вопрос", "new", "another"). Per author, so two people's threads never mix. Any other message within the window goes to the model with that work item as a hint, so a short new question can still open new work.
4. Otherwise one model call (the first OpenAI connection open to all projects with model `gpt-6.2-sol`, or `FLEET_TELEGRAM_CONDUCTOR=<connection>/<model>`; Responses API, `text.format` `json_object`) answers `{choice: <work item> | new | ask, title}` from the open items' titles, how long ago each was active and its last answer, and the message. `telegram_status.conductor` shows the last call: connection, model, choice and why it gave no usable answer.
5. `ask`, a failed call or no model: one message "Is this about: [A] [B] [Something new]". Only the author's tap counts; the tap edits the question to "→ <title>" and delivers the message.

**Status message.** When a room already has open work, the "New: <title>" notice of the next work item is its status message, with an [Open in Fleet] button; the only work item of a room opens quietly and has none. Fleet edits it in place: the title and the facts below ("Working on a message from Simon", the task, the pull request). It stays "working" while any of the item's messages waits for its reply. Every answer in a room carries a [Status] button that opens the status message (supergroups only: `t.me/c/...` links). An edit resolves the message at send time; the chat's order sends the notice first.

A new work item is a `discuss` conversation in the room's project hint (`telegram_room_set`; none: outside projects). Messages of different people are never merged: each is its own `message_send` by its author (L14). Nothing falls back to a wider chat: a reply goes to the room and topic the message came from (L13).

### Replies

Each delivered message has a `telegram_turn` row (kit queue `telegram-reply`). While the run works, the chat shows typing, refreshed every 4 s (L02). In a private chat, once the run has written text, its latest text block shows as a draft instead (`sendRichMessageDraft`, one draft id per message, only when the text changed): a passing preview, never the reply; a refused draft returns the turn to typing. Groups have no drafts. When the run ends, its last agent text block is the reply: sent as a reply to the person's message, with an [Open in Fleet] button (`FLEET_BROWSER_ORIGIN`). A run that ends without text answers a one-line notice. Outbox ids are `reply:<execution>:<part>`, so two messages answered by one run (a queued message joined into it) send the reply once.

**Formatting.** Every text and edit goes first as a rich message (Bot API 10 `sendRichMessage`, `editMessageText` with `rich_message`): GitHub Flavored Markdown up to 32768 characters, so the agent's tables, headings, lists, code, `<details>` and images render as written and a reply splits only past 16000 characters (the limit may count UTF-8 bytes). `tgRich` (`telegram_format.go`) fixes only what Telegram reads differently: `<` and `&` outside code are escaped (model text never becomes markup), except `<details>` and `<summary>`; a single line break inside a paragraph becomes a hard break; a `#` that starts a line without a space is escaped (`#89` is no heading); `$` outside code is escaped, so money is never read as a formula; bare file names (`fleet.md`, `docs/x.go`) go into code spans so Telegram does not link them as domains; `13:47 UTC` and ISO times become date-time entities, so each reader sees their own local time. When Telegram refuses the rich message (HTTP 400 or 404), the item goes again as Telegram HTML (`tgHTML`) in parts of at most 4000 characters, which follow it in the chat's order with the buttons on the last part; when Telegram refuses that formatting too, as plain text without Markdown marks. This is PilotQ's chain (rich, HTML, plain).

### Outbox

Kit queue `telegram-send`: the oldest pending item of each chat, one at a time, so a chat's messages keep their order. A flood limit (HTTP 429) waits its `retry_after`. A message Telegram refuses (HTTP 4xx) is `failed` with Telegram's reason. A `send_message` whose outcome is unknown is `unknown` and never repeated: a second copy is worse than a gap (L11). Edits, reactions and button answers repeat safely and retry. Nothing edits a reply after it is sent (L03, L12).

### Status, commands, questions, milestones (`telegram_status.go`)

Everything here is read from Fleet state; no worker turn runs.

- **Facts** of a conversation, one line each: its newest run (working, waiting for an answer, done or failed, how long ago), open questions, its tasks (`sessionTasks`) and the stage of the newest pull request its runs published (`ThreadPublishingResults`).
- **"So?"** A short message that only asks how things stand ("So?", "??", "ну?", "is it live?") is answered with the facts: of the replied-to work item, else of every open item of the room; in a private chat, `/now`. The conductor's model marks the same with `status: true` for longer phrasings. The message reaches no session.
- **Commands** (`/cmd` or `/cmd@bot`) come from one registry, `telegramCommands` (`telegram_commands.go`): each command's name, the chats it works in, its handler and its words in every language Fleet speaks. Dispatch, `/help` (in the person's language, only what works in that chat) and the bot's command menu (`setMyCommands` for private chats and for groups, per app language, English as the default; and per linked person in their Fleet language, in their private chat and as a member of every known group, since Telegram otherwise picks the menu by the app's language; set at coordinator start and after a link or language change, retried every 10 minutes after a refusal) are built from it, and `TestTelegramCommandRegistry` fails a command missing a language. A text that is no command of the registry goes to the work as any message. `/help`; `/now` (the room's open work; in a private chat the home conversation and the work items the person opened); `/needs` (open questions of the room's work, or the person's, each as a message to answer); `/status [task]` (one task by code or id within the person's projects; without an argument as "So?"); `/stop` (a stop request, as the person, for each run still answering a message of this chat; a stopped run answers "Stopped.", never its partial text, not a failure); `/new` (private chats: the next message starts a new conversation); `/version`.
- **Needs you** (pass `telegram-watch`, every 5 s): an open question of a work item's conversation goes to its room, one of a home conversation to the private chat, once (`q:<question>`). Options that are a list of strings become buttons. A tap or a Telegram reply answers through `questions_answer` as that person (an agent's question gets the reply delivered to its session, as from the web app), within their projects; a key request only links to Fleet and is never answered from chat. An answered or withdrawn question's message is edited to "✓ <who>: <answer>".
- **Overlap** (same pass): when another Fleet conversation, on any host, runs on one of a work item's tasks, the room hears once, with [Join it] (the work item moves to that conversation: later messages go there) and [Work separately]. A tap counts from a linked person who may see the room's project.
- **Milestones** (same pass): a work item's room hears once when its pull request opens, fails its checks or merges, and when its task completes or fails (`ms:<work>:<event>`); the status message follows. Fleet knows delivery as pull request stages; a project's own DEV or LIVE deploys reach the chat as the worker's answers.

### Approvals (`approvals.go`)

Every action an agent takes has a risk level. The defaults are permissive, and a project's `settings.risks` overrides them per action:

| Level | Actions (`ApprovalAction`) | What the agent does |
|---|---|---|
| free | read, analyze, mockup, task, dev-branch | does it and reports |
| reversible | dev-deploy, test-data | does it and reports how to undo |
| radical | live-deploy, delete-accounts, ban-accounts, user-messages, keys, money, data-drop, other-radical | asks first |

- **Card.** Before acting, the agent calls `approval_ask {executionId, requestId, action, what, where, version, rollback}` (the PM profile says so). For a free or reversible action the answer is `not-needed`. A radical action becomes an approval card: an agent question with Approve / Not now whose `data.approval` holds the scope. The card states the agent's understanding of what, where, version and rollback, and who it waits for. It shows in Web Needs you and, through the question push, in the work item's room. The reply reaches the session as any reply does. The agent calls again with the same `requestId` and acts only on `approved`. A changed scope withdraws the old card and asks again.
- **Approvers.** The owner; the people `settings.approvals.<action>` names; else, for every action but keys and money, every person with a grant to the project. An agent identity never approves (a `{kind: "*"}` grant without the owner flag, such as `site-agents`, and any agent caller). Every answer path checks this (`answerQuestion`, `agentReplyTo`); a refused answer says whom the card waits for.
- **Free text.** Explicit approval words ("go live", "live it", "good u can live", "выкатывай") approve the card when exactly one is open in the room's work (a private chat: the home conversation), as the author. A bare "ok", "yes" or "давай" approves only as a Telegram reply to the card, which maps free text onto Approve or Not now. With several open, each card is listed with its buttons. Negations ("not now", "не выкатывай") never approve.
- **Keys.** A key request in chat only links to Fleet; agents point to System › Access and never accept a key in chat.

### Room history (`telegram_history.go`)

A new work item of a group room starts knowing what was said in the room before. Its session gets the room's newest messages (at most 60, about 24,000 characters, the newest kept) as a pending context handoff (strategy `telegram_room`). The handoff goes with the first run, ahead of the person's message, which itself stays verbatim. The history joins three sources: people's delivered messages from the journal, Fleet's sent replies, and messages imported with `telegram_history_import` (table `telegram_history`, migration 110). The import is for the time before Fleet saw a room. The Bot API cannot read past messages, so an import comes from a Telegram export (the PilotQ TEST topics, 2026-11-25). Private chats need none: the home conversation is the history.

## Operations

| Operation | Who | Does |
|---|---|---|
| `telegram_status` | person | bot username, whether the bot reads every group message (`groupReads`: privacy mode off), last poll and its error, the conductor's last model call, waiting intake, replies and outbox, the caller's link |
| `telegram_link_code` | person, not agents | one-time `/start` code and a `t.me` link that opens it |
| `telegram_person_set` | person; owner or full access (agents acting for them included) for others and for links | language, unlink, link a Telegram user id; the bot confirms each link in the user's private chat |
| `telegram_people` | owner | linked people and the Telegram users who wrote without a link |
| `telegram_rooms` | owner | rooms, project hints, open work items |
| `telegram_room_set` | owner | a room's project hint; with `chatId` (and `threadId`) it creates the room ahead of its first message |
| `telegram_work_close` | owner | close a work item; its conversation stays |
| `telegram_history` | owner | a room's newest messages, oldest first: imported history, people's delivered messages, Fleet's replies |
| `telegram_history_import` | owner | import a room's messages from before Fleet saw it (a Telegram export); creates the room when needed |

## Setup

1. Create a bot with @BotFather (`/newbot`). For groups, turn off its privacy mode there (`/setprivacy`) so it reads every message, and add it to the group after that (Telegram applies the mode when the bot joins), or make the bot a group admin. `telegram_status.groupReads` shows the mode.
2. System › Access › Telegram: paste the token once. Fleet checks it with `getMe`.
3. Each person runs `telegram_link_code` (or asks their PM) and opens the link, or the owner links a user id from `telegram_people`.

## Health

`telegram_status` reports each part (pilotq L18): the Telegram API (`pollError`, `polledAt`, `botUsername`, `groupReads`), the intake over the last day (`intake`: updates, ignored strangers, undelivered messages, the newest), the delivery over the last day (`delivery`: sent, refused, unknown and never resent, the newest refusal and send), and the conductor's last model call. Fleet sends no alerts through Telegram, so there is no "alert sent" to be false.

## Hermes rooms

The Hermes Telegram rooms (pds18 `~/.hermes/channel_directory.json`, sessions in `state.db`) are mapped to Fleet rooms. Rooms with a Fleet project get its hint ahead of the bot (`telegram_room_set`: the AI BUDDIES topics 542 and 11275 and Tight Cyprus.bz carry `cs`). P&H rooms work outside projects until P&H is a Fleet project. Monitoring groups stay on Hermes. The cutover is the owner's: add the Fleet bot to the group (privacy off, or as an admin) and remove or mute Hermes there.

## Failure and recovery

- Coordinator restart: unconfirmed updates come again and are skipped by `update_id`; routing, replies and sends resume from their rows.
- Gateway or key down: the poll records the error (`telegram_status.pollError`, System › Access); the queues retry with the kit's backoff.
- The poll pass completes after each long poll (20 s), so its absence rule counts the long poll in its period; a healthy poller raises nothing.
- One coordinator polls. A second poller would make Telegram refuse both (HTTP 409); it shows as a poll error.

## Not built

- Photos, documents and albums as session attachments: the gateway's download mode fetches a file without the key, while Telegram's file URL carries it (`/file/bot<key>/<path>`); the bytes then go to the project's artifact store through an upload. Edits of sent messages.
- Channel health on the System screen (a web view: drawn on the board first); agents read it from `telegram_status`.
- The cutover of the Hermes rooms: the map is ready, the switch is the owner's (below).
- Learning a room's project hint from use; the owner sets it with `telegram_room_set`.

## Verification

`go test ./gateway -run KeyPath`; `go test ./coordinator -run TestTelegram` (DM delivery once, stranger silence, receipt, typing; conductor open, ask, tap). Live: a DM to the bot reaches the home conversation on fleet.ufobe.com and the reply comes back.
