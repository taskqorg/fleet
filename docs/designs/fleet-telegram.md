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

- **Identity.** A message is sent by its author's Fleet person, never by a relay identity. A message from an unlinked Telegram user gets no reply; its update is kept with outcome `unknown-sender` and an audit row (`telegram-user:<id>`).
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

The `telegram` catalogue entry (`go/coordinator/providers.go`) sets `keyPath: "/bot"`: the gateway builds `baseUrl + /bot + key + path` and sends no key header (`contracts/gateway.schema.json` `GatewayProvider.keyPath`). Tools: `get_me`, `get_updates`, `send_message`, `edit_message_text`, `set_message_reaction`, `send_chat_action`, `answer_callback_query`. Every call is a `tool_run` of the coordinator's read token (system activity, excluded from analytics by default). Send tools mark 403 (a user blocked the bot) as a call result, not a key problem. The key never reaches a log: provider replies are redacted as for every connection, and transport errors are not logged.

### Intake

- **Poll** (`telegram-poll`, every 2 s): while the connection takes calls, a `getUpdates` long poll (20 s) with `offset` one past the highest journal row. New updates are inserted (`ON CONFLICT DO NOTHING` on `update_id`) in one transaction; the next poll's offset confirms them.
- **Route** (kit queue `telegram-intake`, one row per update, in `update_id` order):
  1. `/start <code>` links the sender to the person who made the code (`telegram_link_code`, one use, one day).
  2. Unlinked sender: silence, outcome `unknown-sender`.
  3. The room row is created or refreshed; a 👀 reaction is queued as the receipt (L01).
  4. Private chat: the message goes to the person's home conversation; the first message starts it (`session_start`, preset `discuss`, outside projects). `/new` starts a fresh one with the next message.
  5. Group or topic: the conductor (below).
- **Delivery** runs after the routing step commits, in the person's own scope (`scopedRequest`), with request id `tg:<update_id>`: a repeat after a crash is the same request and delivers once. A refused delivery (no access to the project, a held task) answers "Not delivered: <reason>" in the room.

### Conductor (group rooms)

In order:

1. A Telegram reply to a Fleet message or to a routed human message goes to that message's work item (no model).
2. A room with no open work item: the message opens one, titled by its first line; the room gets "New: <title>".
3. Otherwise one model call (the first OpenAI connection open to all projects, or `FLEET_TELEGRAM_CONDUCTOR=<connection>/<model>`) answers `{choice: <work item> | new | ask, title}` from the open items' titles and the message.
4. `ask`, a failed call or no model: one message "Is this about: [A] [B] [Something new]". Only the author's tap counts; the tap edits the question to "→ <title>" and delivers the message.

A new work item is a `discuss` conversation in the room's project hint (`telegram_room_set`; none: outside projects). Messages of different people are never merged: each is its own `message_send` by its author (L14). Nothing falls back to a wider chat: a reply goes to the room and topic the message came from (L13).

### Replies

Each delivered message has a `telegram_turn` row (kit queue `telegram-reply`). While the run works, the chat shows typing, refreshed every 4 s (L02). When the run ends, its last agent text block is the reply: sent as a reply to the person's message, split at 4000 characters on line breaks, with an [Open in Fleet] button on the last part (`FLEET_BROWSER_ORIGIN`). Agent Markdown is sent as Telegram HTML (`telegram_format.go`: bold, italic, code, code blocks, links, headings, bullets; everything else escaped); when Telegram refuses the formatting the same text goes again as plain text. A run that ends without text answers a one-line notice. Outbox ids are `reply:<execution>:<part>`, so two messages answered by one run (a queued message joined into it) send the reply once.

### Outbox

Kit queue `telegram-send`: the oldest pending item of each chat, one at a time, so a chat's messages keep their order. A flood limit (HTTP 429) waits its `retry_after`. A message Telegram refuses (HTTP 4xx) is `failed` with Telegram's reason. A `send_message` whose outcome is unknown is `unknown` and never repeated: a second copy is worse than a gap (L11). Edits, reactions and button answers repeat safely and retry. Nothing edits a reply after it is sent (L03, L12).

## Operations

| Operation | Who | Does |
|---|---|---|
| `telegram_status` | person | bot username, last poll and its error, waiting intake, replies and outbox, the caller's link |
| `telegram_link_code` | person, not agents | one-time `/start` code and a `t.me` link that opens it |
| `telegram_person_set` | person, not agents; owner for others | language, unlink; the owner links a Telegram user id directly |
| `telegram_people` | owner | linked people and the Telegram users who wrote without a link |
| `telegram_rooms` | owner | rooms, project hints, open work items |
| `telegram_room_set` | owner | a room's project hint |
| `telegram_work_close` | owner | close a work item; its conversation stays |

## Setup

1. Create a bot with @BotFather (`/newbot`). For groups, turn off its privacy mode there (`/setprivacy`) so it reads every message, and add it to the group.
2. System › Access › Telegram: paste the token once. Fleet checks it with `getMe`.
3. Each person runs `telegram_link_code` (or asks their PM) and opens the link, or the owner links a user id from `telegram_people`.

## Failure and recovery

- Coordinator restart: unconfirmed updates come again and are skipped by `update_id`; routing, replies and sends resume from their rows.
- Gateway or key down: the poll records the error (`telegram_status.pollError`, System › Access); the queues retry with the kit's backoff.
- The poll pass completes after each long poll (20 s), so its absence rule counts the long poll in its period; a healthy poller raises nothing.
- One coordinator polls. A second poller would make Telegram refuse both (HTTP 409); it shows as a poll error.

## Not built

- Status answers from Fleet state, milestone pushes, Needs you questions in chat (FLT-34).
- Risk levels and approval cards for radical actions (FLT-35).
- Overlap warnings, attachments and albums, edits, channel health on the System screen, moving the Hermes rooms (FLT-36).
- One status message per work item, edited in place (FLT-33 follow-up).
- Learning a room's project hint from use; the owner sets it with `telegram_room_set`.

## Verification

`go test ./gateway -run KeyPath`; `go test ./coordinator -run TestTelegram` (DM delivery once, stranger silence, receipt, typing; conductor open, ask, tap). Live: a DM to the bot reaches the home conversation on fleet.ufobe.com and the reply comes back.
