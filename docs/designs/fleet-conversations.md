# Fleet Conversations

Fleet organizes conversational work as conversations: one conversation is one line of work with its own subject, clean context and native session ([conversation model](#conversation-model)). The conversation screen shows one conversation, or the history of a task across its conversations and executions: operator messages, agent replies, tool calls, reasoning, questions and results in one order. It opens at the newest entry, pages upward and supports a jump with entries on both sides, without a size cap.

The executor produces one typed transcript projection; server reads, rendering, search and copy consume that projection instead of interpreting native harness records independently. This prevents a harness-specific raw-event shape from hiding an agent reply. Receipts remain durable across authentication/edge failures; a missing final receipt does not establish that an execution exited. In `go/executor/outbox.go`, a host-token 401 is a credential/edge condition: receipts remain queued and earlier failed 401 rows are requeued at startup. [Execution continuity](../execution-continuity.md) preserves prior evidence before continuation.

Task history is derived from tasks and conversations rather than stored as a second entity. `VirtualList` keeps the anchor while rows change; app selection state supports copying across rows outside its rendered window. Behaviour is checked live, without numeric speed budgets.

## Terms

- **OR-n:** the corresponding [owner rule](../design-system/owner-rules.md).
- **Service zone:** the compact row region with identity, state, agent, time, counters and actions.
- **Block key:** `(seq, index)`, the row sequence and position in its blocks array; one event may contain several blocks. Cursors, search hits and selection points use the block key, not `seq` alone.
- **`execution_totals`:** per-execution wall time, tokens and cost.

## Conversation model

A conversation is one line of work: one subject, one clean agent context and one native session. Bots, PM views, Telegram rooms, tasks and projects group conversations. None of them is a container of its own (owner decision, 2026-11-26).

Why: before this model, a person met about 14 containers. "Thread" had four meanings and "Home" had two. A Telegram work item was a whole session, but a bot's work item was a tag inside one session. Five models proposed designs independently. All five promoted the work-item thread to a real unit with its own title, state, rights and native session. Four of the five made that unit the conversation itself.

### Words

| Word | Meaning |
|---|---|
| Conversation | One line of work (table `session`). The product, docs and UI use "conversation". Storage and operation names keep `session` (`session_start`, `session_view`); a rename would change contracts and the host protocol and gain nothing. |
| Subject | The durable thing a conversation is about: `task:<id>`, `question:<id>` (a question without a task), `pr:<repo>#<n>`, `host:<id>`, `delivery:<id>`, `trigger:<id>`, or none. A run is never a subject. |
| Task history | The read view over every conversation and run of one task (formerly "the task thread"). |
| Topic | A Telegram forum topic (`telegram_room.thread_id`). |
| Park | A continuation prefers the host and the account of the conversation's native session. Nothing else. |

"Thread" and "Home" leave the product language. Swarm board threads keep their own meaning inside the board.

### Entities

**Conversation** (`session`)

- `title`, `title_by`: always stored, never a raw key. A conversation with a subject is titled from it at creation, for example "T-FLT-63 · Fix the pricing sync" or "Host pds18-native · readiness". A trigger subject takes the trigger's name. Without a subject, the [title pass](#titles) titles the conversation after the first reply. A person or an agent can rename it at any time.
- Agent: `bot_id`, or the profile and role (pm, worker, reviewer, discuss).
- `subject`: kind and id, nullable. At most one non-archived conversation exists per (agent, subject).
- Projects: a set of zero or more projects, changed while the conversation lives. The first project is the default for new tasks. The PM's `focus.projectIds` becomes this set.
- `origin`: person, trigger, task, telegram, fork, pipeline or lab.
- `listed`: false for pipeline and Lab agent steps. These conversations appear on their run and trial pages, not in the Conversations list.
- Native session: harness, account, host and resume id. Earlier native sessions stay as handoff events on the timeline.
- `forkOf` with the fork point, the people with access, and `archived_at`.

States:
- **Open:** a turn is queued or running, or the subject is unfinished.
- **Idle:** otherwise. The next message resumes the native session.
- **Archived:** after 7 days Idle. A new message opens the conversation again.

The list shows an activity badge on top of the state: Running, Queued, Waiting on you or Failed.

**Message.** A person's text, a Telegram message or a trigger wake addressed to one conversation, with `source` and `sent_by`. Queued messages keep their delivery states (queued, steering, steered, delivered, cancelled, superseded, steer_unknown).

**Run** (`execution`). The kinds stay. `session_id` is set only on runs that belong to a conversation. A `host_job` belongs to the conversation of the pipeline step it serves, if any. A failed host job is a failed run on Live and on the host page, not a conversation. A wake's cause and `trigger_id` are fields of the run and the message.

**Groupings** (read views, not stored containers):

| Grouping | Contents |
|---|---|
| Bot | The bot's conversations, Open first, each with its subject. |
| PM of a project | Conversations with role pm whose project set holds the project, newest first. "New" starts another one. |
| Telegram room | The room's bound conversations (`telegram_work`) and the room's current conversation. A private chat is a room with one person. |
| Task history | Every conversation and run of the task. |
| Project | Conversations whose project set holds it. |

### Rules

- **Turns.** A conversation runs one turn at a time. A message that arrives during a turn waits in the queue or steers the running turn. A bot's conversations run in parallel; there is no per-bot limit. When two conversations of one bot touch the same task or pull request, shared-core state settles it: the task claim and the PR owner, not the scheduler.
- **Native session.** A conversation owns exactly one current native session. A continuation prefers that session's host and account (park). Fleet changes the account only for a new conversation, or when the account cannot serve (quota, auth, not installed on the host). A move is a handoff inside the same conversation: a fresh native session starts from a summary, and the timeline shows a quiet "moved to …" marker.
- **Fork.** Any conversation can be forked from any point. The fork is a new conversation with `forkOf`. It uses a native fork where the harness supports one, else a summary.
- **Triggers.** A firing groups its changes by subject. For each subject, the trigger finds the target's open conversation for that subject, or creates one. It then sends one message there. A session target always gets the message in that session. A subjectless timer of a bot goes to that bot's `trigger:<id>` conversation. A skipped firing is recorded in the trigger's log, not as a run.
- **Visibility.** Rights are the conversation's own: its director, the people with access, and agents in their own conversations. A Telegram binding grants nothing.

### Use cases

1. **A bot works.** HAL0's "Needs you" trigger sees three changes in one pass: a question on task T1, PR 990 failing its checks, and a readiness failure on host H2. It finds or creates HAL0's conversations for `task:T1`, `pr:fleet#990` and `host:H2`, and sends one message to each. The three run in parallel, each in its own context and native session. A second readiness failure on H2 goes to the same "Host H2 · readiness" conversation. The owner opens HAL0 under Conversations, sees its three Open conversations, and answers inside the T1 conversation. To talk to HAL0 about something new, the owner starts a conversation with HAL0 without a subject.
2. **A pipeline works.** A pipeline run and its steps are runs. Each agent step has a hidden conversation (`origin: pipeline`), read from the run page. Script and host steps have none. "Continue" on a step forks its conversation into a listed one.
3. **Lab.** A trial is a pipeline run with experiment and variant. Its agent steps have hidden conversations (`origin: lab`). The experiment page opens a trial's transcript, and "Continue" forks it. Trials do not appear in the Conversations list.
4. **PM.** "PM · fleet" shows the PM conversations with fleet in their project set and starts a new one. The PM creates tasks. Autostart gives each task a worker conversation with subject `task:<id>`. A review gets its own reviewer conversation with the same subject, so its context starts clean. Rework continues the worker conversation. Task history shows them all, with the PM conversation that created the task.
5. **Telegram.** A message in a room is routed by the [conductor](fleet-telegram.md#conductor-group-rooms):
   - a reply goes to the conversation of the message it replies to;
   - otherwise the conductor picks one of the room's bound conversations, or opens a new one.

   Work on a second project adds that project to the conversation's set. A separate line of work becomes another conversation bound to the same room. A private chat routes the same way; its long chat is the room's current conversation. On the web each one is an ordinary conversation marked with its room, and a reply typed there goes back to the room. [Join it] binds the room to the other conversation.

### Agent operations

Agents get the model first, through the existing operations:
- `session_start` takes an optional `subject`, `projects` and `listed`;
- `message_send` addresses a conversation by its id, and its free `thread` string is removed;
- `session_rename` changes the title, and a new `session_update` changes the project set and the people with access;
- `session_fork` forks at an execution;
- `sessions_list` filters by bot, subject, project, room, origin and `listed`.

Not built: everything in this section beyond today's `session_start {taskId}`, `session_rename` and `session_fork`. The [PR series](#migration-to-the-conversation-model) builds it.

### Migration to the conversation model

The data is small (about 1,400 sessions and 10,000 runs), so each step rewrites it in place. The dispatcher is drained before steps that move native sessions.

1. **Park by host and account.** Dispatch prefers the last account as well as the last host, and changes the account only when the account cannot serve. HAL0's 45 handoffs were all account rotations.
2. **Subjects and host jobs.** Trigger changes use subject keys: a host job without a task becomes `host:<id>`, never `execution:<id>`. Every subject kind gets a stored title. A host job no longer creates a hidden session; about 754 such sessions are deleted, and their runs keep `session_id` null.
3. **Conversation fields.** Add `subject`, `origin`, `listed` and the project set, and backfill them. Pipeline and Lab agent-step sessions (about 427) become hidden conversations.
<a id="work-item-threads"></a>
4. **Bot conversations.** Today a bot works in one Home session holding work-item threads: the `thread` column of `message` and `execution` (migration 109), one native session per thread in `nativeSessions[<key>#<thread>]`, fork and handoff for the main thread only. Bot wakes instead find or create the bot's conversation for each subject (`session.subject`, migration 117), and the per-bot limit becomes the per-conversation turn rule: a trigger's message to a busy conversation waits in its queue. Migration 117 moves each thread of a Home session into its own conversation with its messages, runs, events, questions, artifacts, commands, queued messages and bot runs, and the thread's native session as its own; an `execution:` thread folds into its host's subject. The Home session stays as the bot's conversation without a subject: person messages and direct runs, and the turns from before threads. Not built: `bot_run` merged into the run and the trigger log; the `thread` columns dropped.
5. **Telegram.** `telegram_person.home_session_id` becomes the private room's current conversation. `telegram_work` is the room-conversation binding.
6. **Task history.** The task thread view is renamed Task history. It is the task's [history](fleet-history.md) (`history_view {taskId}`): its conversations' runs, questions and state moves, newest first.

Steps 3, 4 and 6 change the screen. They are drawn on the [board](../design-system/README.md#design-board) and approved before UI code (OR-62).

## Goals

*   One conversation per line of work. A task's history spans its conversations; a taskless conversation that later gets a task joins that history, and nothing is copied.
*   The thread opens at the newest entry, loads older entries upward, and a jump to any entry loads entries on both sides of it. No size cap.
*   The row being read moves 0 px when rows load above, rows stream in, a row expands, or media finishes loading.
*   One typed block model (text, tool call, reasoning, question, result, boundary) produced by one normalizer. The screen only renders blocks.
*   Each reply shows its agent mark (harness icon, `opus-5.5 · high`); host, tokens, cost and duration show on hover.
*   Tool calls and reasoning are collapsed to one line, open on click and can be copied.
*   Copy works for one reply, one code block, a selection across messages, and the whole thread, all as markdown.
*   Question cards in the thread: open questions are answerable there or from Tasks with the same effect; answered questions stay, collapsed, with the answer.
*   Result cards in the thread and one shared artifact viewer (any file, plus image and video views). A Results page lists results and artifacts with Tasks-like filters.
*   Stop, steer and answer are always visible. Retry, fork, hand off and switch profile or model are in one menu.
*   A new conversation starts in the work area at `/conversations/new`: a project, a preset of what we are doing now (agent, access, branch, starter instruction), the composer. A conversation can start in every project, also one without roles, and outside projects; agents start with the same presets.
*   A message carries files (attachments): the agent receives them as files in its working area, and the transcript shows them.
*   Server-side search over the whole thread that can drive a jump.
*   Every conversation has a short title: set by a person or an agent, or generated by a small model after the first reply.

## Non-Goals

*   Numeric speed budgets or thread size limits. The check measures and reports; it asserts behaviour only.
*   A stored task-history entity. Task history is derived from `task_id`.
*   A new windowing engine. `VirtualList` is extended.
*   Keeping the old view behind a flag.
*   New agent operations. Retry, fork, hand off and switch use the existing agent tool operations (`go/coordinator/agent.go`).
*   Editing or deleting artifacts from the viewer. Artifact lifecycle stays in the data foundation.

## Defaults and compatibility

These are the existing presentation and membership defaults; this cleanup introduces no new choice.

*   **Model label source.** Default: `format.ts` turns the stored model id into the short label (`claude-opus-5-5` becomes `opus-5.5`). No model registry is added.
*   **Old events without blocks.** Old records are not migrated and no compatibility projection is kept. Events stored before blocks existed show no blocks. The executor `reproject` subcommand stays only as the tool that turns captured fixture records into the projection the executor writes.
*   **Session with executions for two tasks.** Default: its early taskless history belongs to the first task it got; the second task's thread shows the session from its first execution with that task. Alternative: show the early history in both threads.
*   **Steer while a question is open.** Default: steer text goes to the execution as a steer command, as today. It does not answer the question. The composer shows the open question card above it, so the operator sees that an answer is still pending.

## Critical User Journeys (CUJs)

### CUJ 1: Open a long task thread

*   **Given**: Task `T-FLT-42` has three sessions and nine executions with 40,000 events in total.
*   **When**: The operator opens the task in Conversations.
*   **Then**: The newest entries render first, at the bottom. Scrolling up loads older pages; the row the operator is reading does not move. Session and execution boundaries show as thin labelled rows.

### CUJ 2: Jump to a search hit

*   **Given**: The operator is at the bottom of the thread in CUJ 1.
*   **When**: The operator searches for `migration 6` and picks a hit from the first session.
*   **Then**: The server returns the hit's position. The list replaces its window with entries around the hit, the hit row is placed in view and marked, and scrolling either way loads more.

### CUJ 3: Answer a question in the thread

*   **Given**: An execution asked "Use Postgres 17?" and the question is open.
*   **When**: The operator clicks an option on the card in the thread.
*   **Then**: `questions_answer` runs with the card's request id. The card collapses to show the answer. The same question in Tasks shows answered from the live feed. A second click from Tasks with the same request id changes nothing.

### CUJ 4: Copy a cross-message selection

*   **Given**: The operator drags from the middle of reply 12 to the middle of reply 80, and most rows between them are outside the rendering window.
*   **When**: The operator presses Cmd+C.
*   **Then**: The clipboard holds markdown from the start offset in reply 12 to the end offset in reply 80, including the rows that were not in the page.

### CUJ 5: Stop one of two parallel executions

*   **Given**: Two executions of the task run at the same time.
*   **When**: The operator clicks Stop in one execution's boundary row.
*   **Then**: Only that execution gets a stop command. The composer still names the newest running execution as its target.

### CUJ 6: Open an artifact

*   **Given**: An execution produced a screenshot and a log file.
*   **When**: The operator clicks the result card in the thread.
*   **Then**: The artifact viewer opens in the work area. The image shows in a fixed-ratio frame; the log shows as text, read in `Range` pages, with a download link. The Results page lists both under the task.

### CUJ 7: Start a conversation in a project without roles

*   **Given**: Project `cs` maps no roles and defines no presets.
*   **When**: The operator clicks New conversation in the shell header, picks `cyprus.bz`, picks "Fix / build" and sends "Fix the hero".
*   **Then**: `/conversations/new` shows the system presets; "Fix / build" shows the coding worker, "Can edit" and the branch `fleet-test/new`. Send calls `session_start({ projectId: 'cs', preset: 'fix', access: 'write', branch: true, text })`, which falls back on the system profile `coding-worker`, and the new conversation opens. Back returns to `/conversations/new`.

### CUJ 8: A new conversation gets its title

*   **Given**: The operator starts a conversation whose first message opens with the project's standing rules.
*   **When**: The agent's first reply ends.
*   **Then**: Within a pass the list row and the detail header show a short title that names the work, not the rules. Renaming it from the detail menu keeps the new title for good.

## Technical Plan (Architecture)

### Ordering key

Every row the thread needs already has one shared ordering number. `document.seq` is a `bigserial`, and the `execution`, `question` and `artifact` tables take their `seq` default from the same sequence (`go/coordinator/schema.sql`). Events and operator messages are documents. Here `seq` is the row's `seq` column, not the per-execution `data.seq` that host events also carry. So `seq` gives one total order across sessions and executions that overlap in time. Rows use `seq` for ordering; paging cursors use the block key `seq:index`. No new ordering column is needed.

`seq` is taken at insert, not at commit, so two transactions can commit out of `seq` order. The operator app already solves this for its live feed: a read and the feed cursor come from one snapshot, and the feed (`change` table, ordered by `(xid, seq)` up to the oldest transaction of Fleet's database still running) never skips a slow transaction ([fleet-operator-app.md](fleet-operator-app.md)). Thread reads copy that pattern:

*   Each `thread_entries` read runs in one snapshot and returns the change-feed cursor of that snapshot (`feedCursor`).
*   The client follows the feed from `feedCursor`. For a fed change in the thread, the client fetches that row's blocks with `thread_blocks` by the change's kind and id (a change row names the document id, not its `seq`) and places them by block key.
*   A row from a slow transaction can therefore arrive with a key lower than the client's newest loaded key. It is still placed by key, and `VirtualList` anchor keeping holds the row being read in place (OR-6). Nothing is skipped and nothing on screen moves.

### Task history scope

A read key is `{ task: id }` or `{ session: id }`; the routes are `/api/threads/task/:id/...` and `/api/threads/session/:id/...`.

*   **Task history**: all executions with `execution.task_id = <task>` (indexed by `execution_task_seq`). Their sessions are the set of `session_id` values. This includes a taskless session that got the task later, because its later executions carry the `task_id`; its earlier executions join through the session (see below).
*   **Conversation**: one conversation that has no task. Once the session gets a task, its URL opens the task thread.

Membership rule: an entry belongs to task T's thread if its execution has `task_id = T`, or if its execution has no `task_id` and T is the first task its session got (the session's lowest-`seq` execution with a `task_id`). This keeps a session's early taskless history in the task thread once the session joins the task (the thread membership rule), and nothing is copied. The two-task case uses the first-task default described above; it does not duplicate the earlier history into both threads.

As a query: the session set is `SELECT DISTINCT session_id FROM execution WHERE task_id = T`; entries are rows of those sessions whose execution has `task_id = T`, plus rows whose execution has no `task_id` when T is that session's first task.

Joining a task: `session_start` takes an optional `taskId` (a task of the same project); the session records it and every execution it starts carries that `task_id`. `session_task_attach({ sessionId, taskId })` attaches a taskless session: the session records the task, its next executions carry it, and its earlier taskless executions join the task thread by the membership rule; nothing is copied or rewritten. A session that already belongs to another task gets 409; the same call again returns `existing: true`. The attached task counts as the session's first task (D2). Both are agent tools as well (the PM agent and people-access use them); host agents may call them only for their own sessions and project.

Access: the thread operations run under the same project scope as every other read operation. A task's executions belong to the task's project, so plain scope filtering is enough.

### Read path: `thread_entries`

One coordinator operation, also served as `GET /api/threads/{task|session}/:id/entries`:

```text
thread_entries({ key, before?: BlockKey, after?: BlockKey, around?: BlockKey, limit })
  -> { entries: Block[], first: BlockKey, last: BlockKey, hasBefore: bool, hasAfter: bool, total?: number, feedCursor }
```

*   No cursor: the newest `limit` entries (open at newest).
*   `before`: the `limit` highest-key entries with key lower than `before`, returned in ascending key order.
*   `after`: the `limit` lowest-key entries with key higher than `after`.
*   `around`: `floor(limit/2)` entries before `around`, and the rest from `around` onward, including it. A short side is not refilled from the other side; `hasBefore` and `hasAfter` say where more exists.
*   `limit` counts entries (blocks) and is the page size the client asks for. There is no configurable product page-size cap. For JSON operations, `positiveLimit` accepts a positive integer JSON number counting blocks, at most `2^53` (`9007199254740992`); it rejects non-numbers and fractions. The HTTP query adapter parses an unsigned 53-bit integer, so its representable positive range ends at `2^53 - 1` (`9007199254740991`). These are the existing representation/validation bounds, not a new page cap.
*   `total` is the entry count, for the "84 of 1,240" count (OR-11). It is returned only for the open read and for `around`; the client adds live entries to it. It is a count over the thread's index range.

The query finds the thread's member executions (the membership rule), then reads events and messages through `document_execution (kind, executionId, seq)`, questions through `question_execution` and artifacts through `artifacts.list({ executionId })`, which merges the `artifact` table and legacy `artifact` documents, merged by key. It joins `execution` (harness, model, effort) only for the page rows. It reads only the page, never the whole session. `session_get` stops returning `events` and `results`; it keeps the session header fields.

Live entries arrive through the existing change feed (`app/src/feed.ts`) from `feedCursor`. The client inserts a fed entry whose key falls inside its loaded window `[first, last]`, or after `last` when `hasAfter = false`; it ignores other fed entries, because a later page read returns them. The "N new" pill counts only entries past `last`.

`key` may be `{session, thread}`: only the entries of that work-item thread of the session (`''` the main thread), read over `execution_thread`; [migration](#migration-to-the-conversation-model) step 4 removes this key. `full: false` keeps every tool block to its summary: `callId` (`<executionId>/<seq>:<index>`), `toolKind` (classified on the server with the rules of `packages/ui/src/controls/tool-fold.ts` `toolKind()`), a short `target` and, for a failed call, `errorLine`. `tool_output {callId}` loads that call's input and output when it is opened. The default (`full` absent or true) keeps input and output in the block for agents that read a transcript.

### Session view: `session_view`

The conversation screen reads one operation, composed on the server from summaries ([data layer](https://github.com/taskqorg/fleet/pull/959): one read per screen, no stored blobs in a view):

```text
session_view({ sessionId, executionsLimit?, executionsBefore? })
  -> { head, composer, questions, threads, triggers, botTriggers?, context, lastMessage?, executions }
```

*   `head`: code, title (set or generated, else the first message's start), state and active run (the newest turns), project and its code, bot, archived, the agent profile as the composer chips show it, the latest run's error line and the session's cost.
*   `composer`: the session that receives the next message (`thread_composer`'s rule), its running execution and agent, or why none can receive one.
*   `questions`: open questions of the session and of the composer target.
*   `threads`: each work-item thread with its item's title, code and state, its turns, last activity, Open or Idle, and the viewer's unread turns (since the viewer's `person_visit` of page `thread:<session>:<thread>`). Step 4 of the [migration](#migration-to-the-conversation-model) removes this field: a bot's conversations come from `sessions_list`.
*   `triggers` and `botTriggers`: `trigger_list` of the session and of its bot. `context`: `session_context`.
*   `executions`: the newest 20 turns (at most 200) as rows, paged with `executionsBefore`, over `execution_session_turns` (migration 111). A row holds no snapshot, dispatch or configuration; `execution_get` has the whole record.

`session_get` stays for agents and for Details (forks, handoffs, the resource rollup), which the screen reads only while that panel is open. Measured on HAL0's Home session (255 turns): `session_get` was 7.6 MB and took 2-14 s, reread on every change of the session.

### Search: `thread_search`

```text
thread_search({ key, q, before?: BlockKey, limit }) -> { hits: { key: BlockKey, snippet }[], hasMore }
```

It searches the plain text of text, reasoning, tool-call and question blocks in the thread, newest first. The first version uses `ILIKE` on the stored block text over the same index range. A text index is added only when a measurement shows it is needed. A hit is opened with `thread_entries({ around: hit.key })`.

### Block model

One normalizer produces typed blocks. The screen and copy code only read blocks. The types are `Block` and its six members in `contracts/ops-sessions.schema.json` (closed, told apart by `kind`); the app's `Block` and `packages/ui`'s `ThreadBlock` are the generated union, and the coordinator builds its blocks from the generated structs.

```text
Block =
  | { kind: 'text',      key, executionId, role: 'operator'|'agent', markdown, plain, at, by?, stream?, harness?, model?, effort?, attachments? }
  | { kind: 'tool',      key, executionId, at, name, input, output, state, startedAt, endedAt }
  | { kind: 'reasoning', key, executionId, markdown, plain, at, stream? }
  | { kind: 'question',  key, executionId, questionId, text, options, state, askedAt, answeredAt, answer?, answeredBy? }
  | { kind: 'result',    key, executionId, artifactIds, artifacts }
  | { kind: 'boundary',  key, sessionId, executionId, harness, model, effort, state, startedAt, endedAt }
```

*   `text`, `tool` and `reasoning` come from the executor. `project` (`go/executor/harness_blocks.go`) writes `fleetNormalized` per event. It writes a `blocks` array with these three kinds for every supported harness. Tool duration comes from the harness start and end records.
*   Streaming: delta records (projection with `deltaOf`) are stored as documents of kind `event-delta`, not `event`, so pages, search and export never scan them. Ingestion preloads event identities through execution/sequence indexes and writes validated chunks in one `UNNEST` batch. When the stream ends, the executor writes one complete final block on the completing `event`. In the same transaction, the coordinator replaces the closed chunks with compact immutable hash/sequence identities in `closed_delta_identity`; retries cannot resurrect chunks or change their content. Late chunks for an already closed block are compacted too. Open or interrupted streams without a complete closing block retain their durable chunks across coordinator crashes; cleanup never removes their only text. This is the indexed durable alternative to an ephemeral DeltaBroker; no per-agent in-memory text buffer is required. An open stream is shown as one entry at the end of the newest page, built in full from its `event-delta` rows; it is never split across a page edge.
*   Execution end: on an execution's terminal transition the coordinator writes one `event` of type `execution.ended`. It carries no text; its `seq` places the result block. A stream cut off without a final block (the execution ended first) is shown as one closed entry built from its `event-delta` rows at read time, so the executor stays the only producer of text blocks.
*   Records with no raw data or an unknown shape get one `tool` block named after the event type, with the raw JSON as output, so every event has `blocks`.
*   `question` and `boundary` come from the coordinator, from the `question` and `execution` rows, at their own `seq`. `result` comes from the stored `artifact` rows, at the key of the `execution.ended` event.
*   `plain` is the block's text with stable character offsets. Selection and copy use it.

Old stored events are not re-projected (owner decision); the app no longer reads `data.item`.

Projection details:

*   `plain` equals `markdown` in this version; the coordinator sets it, the executor stores only `markdown`.
*   Stream ids are `<kind>:<n>` per execution (`text:1`, `reasoning:2`). The first chunk of a stream carries `opens: true`; the record that closes streams carries `closes: [ids]`. Readers find streams through `opens` rows and never scan every chunk.
*   Lifecycle records (thread start, usage, protocol writes, turn end) get an empty `blocks` array. Executor bookkeeping events (preparation, resource observations, process closed) are not harness records and have no projection. "Unknown shape" means an `unparsed` line, a `*.failed` record or a Claude error result: one `tool` block named after the record type, state `failed`, raw record as output.

### Agent mark

The `boundary` block and each agent `text` block carry the execution's `harness`, `model` and `effort`. `effort` is stored in `execution.config_ref`; the thread reads `config_ref->>'effort'`. The row shows the harness icon and the short label in the right service zone (OR-21). The target boundary row shows duration and cost as visible chips (OR-19); host and tokens belong in their tooltips, one tooltip per chip (OR-20). Not built: the cost chip and host/token telemetry hover; the current `BoundaryRow` exposes duration and the agent mark. Reply rows show only the harness icon and label, with the model id on hover. Fleet has no cost attention rule today, so the cost chip stays neutral; this design invents no threshold (OR-18 applies once such a rule exists).

### Rendering and nothing-jumps

`VirtualList` gains:

*   `onStart`, the mirror of `onEnd`, called once per list length when the first rows come into view.
*   Position keeping on prepend: before the new items commit, it records the anchor key and its offset; after measurement, in the same layout effect, it sets `scrollTop` so the anchor offset is unchanged. The existing anchor logic is reused.
*   `scrollToKey(key, align)` for jumps.

Rules, one invariant ("the anchor row moves 0 px"):

*   Expanding a tool call or reasoning row opens downward; the clicked header stays where it is.
*   Media gets a fixed-ratio placeholder from the artifact's stored size before it loads.
*   Streaming grows only the newest row. Following stops when the operator scrolls up, and the "N new" pill shows (OR-7).
*   The loading bar keeps its space .

### Copy

*   **Reply and code block**: a copy icon in the row's service zone and on each code block. Reply copy gives the block's `markdown`. The icon shows while the row (or the code block) is hovered or the icon has focus, and always on a touch screen; showing it moves nothing (OR-23).
*   **Cross-message selection**: selection is app state, `{ start: { key, offset }, end: { key, offset } }`, set by dragging between reply bodies, by click and Shift+click for whole rows, or by Cmd+A for the whole thread. Dragging retains the anchor by key when its row leaves the window. The app highlights the selected portions of the endpoints and the intervening rows. Cmd+C calls `navigator.clipboard.write([new ClipboardItem({ 'text/plain': promise })])` synchronously in the key handler, where the promise is `thread_export({ key, from: start, to: end })`. If the write fails, the status line says so and offers a retry button.
*   **Whole thread**: the same `thread_export({ key })` without bounds. There is one markdown builder, on the server. The HTTP route streams the markdown as `text/markdown`, so a long thread is not held in memory as one string.

```text
thread_export({ key, from?: { key, offset }, to?: { key, offset } }) -> { markdown: string }
```

Offsets count Unicode code points in the block’s `plain` source and use an exclusive end. A text caret in rendered Markdown maps to that source, including positions after hidden Markdown syntax. Offsets cut `text` and `reasoning` blocks. Other blocks (tool, question, result, boundary) are included whole when their key is inside the range, each as a short markdown line or a fenced block for tool input and output.

Native browser selection is kept inside one row, for copying part of a reply.

### Questions

The thread renders `question` blocks with the existing `DecisionBlock` pattern. Answering calls `questions_answer`, the call Tasks uses. `answerQuestion` (`go/coordinator/tasks.go`) returns the stored question when the same answer repeats and fails with 409 when a different answer arrives; the card then shows the stored answer and who gave it. The update guards the open state so only one conflicting answer wins. The card updates from the change feed. Answered cards collapse to one line with the answer.

### Results and the artifact viewer

Artifact storage provides stored artifact rows, stores and placement; `artifact_list` and `artifact_get` expose them (`go/coordinator/artifact_routes.go`).

*   **Result block**: one per execution end with stored artifacts or a retained publishing observation, at the key of the `execution.ended` event. A late observation or artifact updates the same key. Artifact names wrap onto new lines inside the transcript column. A name longer than the column ends in an ellipsis and its tooltip has the full name, media type and size. Artifact links open their Results entries.
*   **Delivery stage**: the coordinator links publishing observations by source execution and project. Later observations of the same provider, host, repository and request number update the linked result. The result uses the shared `StageChip` for an observed open request, running or failed checks, a merge in progress, or a confirmed merge. An unrecognized state reads “Delivery stage unknown”. Missing check observations supply no checks stage. Task state and delivery stage remain independent.

*   **Viewer**: `ResultViewer` and `ArtifactView` (`packages/ui/src/controls.tsx`) open in the work area (OR-27). The [result-viewer control](../design-system/controls/result-viewer.md) owns the format table and Source/Preview choice. Renderable formats open rendered; copy/download take the source. Images and video use fixed-ratio frames; text loads through `Range` pages as the operator scrolls, without a cap. Unrecognised types are download-only. Raw bytes use `GET /api/results/:id`; interactive HTML uses the separate `GET /api/results/:id/preview` route. HTML scripts run on an opaque sandboxed origin, without same-origin app access; `connect-src 'none'` blocks fetch/XHR connections and the preview CSP explicitly lists permitted static-resource sources. SVG renders as an image, so its scripts do not run. Markdown has no raw HTML. These are the existing rendered-first viewer rules, not a new permission change.

*   **Results page**: the app section listing `artifact` rows with task, project, execution, media type, state and time filters, using the Tasks list pattern (state icon first, OR-22). It uses the same `ResultViewer`. Conversations result cards link into it.

Artifacts live in the `artifact` table. The coordinator projects stored artifacts into thread blocks. The Results page and viewer read artifacts through `artifact_list` and `artifact_get`.

### Controls

Stop, steer and answer are always visible. The rest are in one left-aligned menu with icons (OR-13), in reserved space (OR-23).

*   **Composer target**: `thread_composer` resolves the thread from the open conversation and selects its newest confirmed running execution, independent of the transcript window. A task target must belong to a visible session in the conversation's project that can still receive work for the thread. With no eligible run, the composer addresses the newest eligible session and offers Message. A standalone conversation keeps its own session and active turn controls. Each execution boundary keeps its own Stop.
*   **Stop**: `execution_stop` on that execution. The composer's one button is Stop while the target runs and the composer is empty ([composer](../design-system/controls/composer.md)).
*   **Steer**: `message_send` with `delivery: 'steer'` on the target execution's session. The coordinator checks the displayed target before a new submission and reconciles a known request ID against its original destination. A host without steer support queues the message. Task conversation uploads stay in the project until the message binds them to its destination session.

Menu actions:

| Action | Operation | Kept | New | Where it shows |
|---|---|---|---|---|
| Retry | `message_send` with the last operator message, same session | session, task | execution | same thread, new boundary |
| Switch profile or model | `session_transition` | task, session when `reuse` or `switch_model_in_session` | execution; new session when `create_with_handoff` | same thread, new boundary |
| Fork | `session_fork` | original session unchanged, task | session and execution | same thread, new boundary |
| Hand off | `session_transition` with another harness or account (`create_with_handoff`) | task, context handoff record | session and execution | same thread, new boundary |

Hand off is the existing context handoff (`makeHandoff`, `go/coordinator/agent.go`). Quota-driven handoff is not part of this design (it is also out of scope in [fleet-data-foundation.md](fleet-data-foundation.md)).

### Start a conversation

A preset is what a new conversation does now. It names a project role and a system profile to fall back on, the access, whether the session gets its own branch, and a starter instruction. Presets live in the project definition, `settings.presets` ([configuration](../configuration.md#project-settings)), so people and agents edit them with `project_update`; a project with none gets the system presets (`go/coordinator/presets.go`):

| Preset | Role, else system profile | Access | Branch | Starter |
|---|---|---|---|---|
| `discuss` Discuss | `pm`, else `pm` | read | no | none |
| `design` Design | `worker`, else `coding-worker` | write | yes | read the design rules (`docs/design-system/owner-rules.md`, the book) where the project has them; build from `packages/ui`; `pnpm run check:design` |
| `fix` Fix / build | `worker`, else `coding-worker` | write | yes | none |
| `review` Review | `reviewer`, else `claude-reviewer` | read | no | none |
| `investigate` Investigate a problem | `pm`, else `pm` | read | no | read the project's Bugsink errors (`errors.bugsink`), execution logs, telemetry and code; report the cause; do not change the code (a task for the fix may be written) |

*   **Resolution.** A preset starts with the project's profile for its role when the project maps the role, else with its own profile. A project without roles (cs) therefore starts every system preset on a system profile. A preset that resolves to no trusted profile is listed with `unavailable` and refused at start. A project preset names a role the project maps, or a profile; `project_update` refuses one that names neither a mapped role nor a trusted profile.
*   **`session_presets({ projectId? })`** lists the resolved presets: id, name, description, role (null on a fallback), profile, harness, model, effort, access, branch, starter, source (`project` or `system`), `unavailable`; plus the project's branch prefix and the base a worktree starts from. Without `projectId` it lists the system presets for a conversation outside projects. It is an agent tool too; a host agent reads its own project.
*   **`session_start`** takes `preset`, and `access` (`read`, `write`) and `branch` (boolean) that override it; an explicit `role` or `profileId` wins over the preset's profile. Defaults without a preset are today's: write, with a branch. A person with full access may omit `projectId` (for others `session_presets` marks every preset outside projects unavailable) to start outside projects (no task; the request identity is scoped to that person); an agent's start stays in its own project. An agent starts only profiles its project maps to roles: a preset that would fall back on a system profile is refused for it (403), as an explicit `profileId` outside the roles is. A session started from a read conversation (`callerSessionId` with access read) is read by default and refused as write (403); a `request_child` child of a read session is read too, and so is a session its agent makes with `session_create`. Such sessions also go without a branch. Read access means the code does not change: it limits files and the sessions the conversation starts. Fleet operations stay allowed with the director's authority (owner decision 2026-11-18): a Discuss or Investigate PM can write tasks, start pipelines and message existing sessions. The cards and the access chip say "Code read only" and "The code does not change", never "Nothing changes". An explicit `access` without `branch` gets a branch for write and none for read. A host takes a read or branchless start only while it reports `start-access-v1`, checked at placement and again at poll (also for the teardown of a branchless worktree); the teardown command carries `branch: false`, so the executor never guesses it from refs. Write access needs a branch (400 for `access: write, branch: false`, and a project preset cannot say it): commits on a detached worktree could never be delivered. A repeated `requestId` with another preset, access or branch is 409, never the earlier session. The session records `preset`, `access`, `branch` and the starter; `session_get` returns them and a fork keeps them.
*   **Access.** A read session's start command carries `readOnly: true` ([host protocol](../../contracts/host-protocol.schema.json) `StartPayload`): codex runs its read-only sandbox for the thread and each turn, claude gets only Read, Glob and Grep (plus Skill and web tools the run grants), antigravity and ACP agents run in plan mode, and an ACP agent without a plan mode refuses the start. Fleet operations keep their own authority. Hosts that honour `readOnly` and `branch` report the capability `start-access-v1`; dispatch places a read start, or a branchless one in a project, only on such a host, and otherwise it waits with "executor lacks start-access-v1", so an older host never runs it with write tools.
*   **Branch.** `branch: false` reaches the host as `branch: false`: the session worktree is detached at the same base (`worktree.baseRef`, else fresh `origin/<defaultBranch>`), so no session branch is created and the host checkout stays untouched; teardown removes it as usual, and commits the checkout's own branches hold never count as its unpushed work. Only read sessions go without a branch.
*   **Starter.** The coordinator adds the starter to every turn's instructions after the project context, as "This conversation's preset. <name>: <starter>". The executor delivers it with that frozen context block (after the profile's instructions), also outside projects.
*   **Model and effort.** A preset sets none of its own: every profile fixes its model and effort, and the coordinator refuses a model that differs from the profile. A different model is another profile, or a switch in the conversation (`session_transition`).

The page `/conversations/new` ([conversations page](../design-system/pages/conversations.md), [start picker](../design-system/controls/start-picker.md)) shows the project, the presets of that project, the composer and the chips of what the start uses; Send is `session_start` with the shown preset, access and branch. The New conversation button sits in the shell header next to the section title (`ShellActions`).

### Attachments

A message may carry files. Owner decision of 2026-11-17; the control is the [composer](../design-system/controls/composer.md).

*   **Upload.** A file goes into the artifact store ([data foundation §3](fleet-data-foundation.md#3-artifact-store)) before the message is sent. `artifact_upload_begin({ name, size, mediaType?, sessionId? | projectId? })` selects the store from the project's `artifact_locations`, starts a multipart upload on a staging key and records the upload as an `upload` document in the person's scope: owner, project, session, size, the parts received and the running SHA-256. It returns `{ uploadId, url, partBytes }`. The client PUTs the bytes to `url` on the upload's grid of `partBytes` (8 MiB: over S3's 5 MiB minimum part, far under Cloudflare's 100 MB request limit; a file of more than 10,000 such parts, S3's part limit, gets larger whole-MiB parts up to 95 MiB, and only a file beyond 10,000 parts of 95 MiB is refused at begin), in order, each with `Content-Range: bytes <first>-<last>/<size>`. Each part goes straight to the store as one multipart part; nothing is kept on the coordinator's disk. The record advances only if it still has the count it was read with (409 with `received` otherwise), so two coordinators never record one part twice. A part's request body may take 30 s plus the part at 16 KiB/s, a deadline set only after the person, the upload and the part's range are checked; a cut-off part answers 409 with `received` and is sent again. The store's multipart upload starts with the first part, never inside the begin transaction; its id is recorded by a conditional update before any part goes to it, and a request that loses that race aborts its own, so one multipart upload is ever used. A part's body is read before the upload's in-process lock, so a stalled request never holds back a retry. Part bodies are reserved from a bounded upload memory (`FLEET_UPLOAD_MEMORY_BYTES`, default 1 GiB, a setting) before their buffers exist; a part that waits a minute for it answers 503 and is sent again. A finished record takes no more writes, so concurrent finishes record one artifact. A finish of a file above 256 MiB runs in the background (completing many parts and copying gigabytes could outlast a request through the proxy); a bodyless probe `Content-Range: bytes */<size>` answers `{ received, finishing }` until the artifact is recorded, then the artifact. The browser uploads at most three files at once and stops a removed file at once. A part that does not start where the file ends answers 409 with `received`, so a client resumes; a part already received answers `received`. The part that completes the file completes the multipart upload, copies the staging object to its content key (as `artifactVerified` does), and records an `artifact` row with role `attachment`, no execution, the conversation's `session_id`, state `stored` and its verified copy; it answers the stored `ArtifactView`. Every step is a short transaction around one store call and repeats safely, so a retried last part after a failed finish finishes it, and a retried part of a finished upload answers the same artifact. A finish that completed the store upload but failed to record it finds the whole staging object and does not complete it twice. An upload left unfinished for a day is aborted on the store and its record removed, checked after each begin and every hour (`go/coordinator/uploads.go`).
*   **Limits.** None beyond store capacity, as for every artifact (data foundation §3, "No size limit in Fleet"). The part size is a transfer unit, not a cap.
*   **Access.** The session's project grant: `sessionId` must be a session the person sees (row-level security), `projectId` a granted project; no project needs the architect role. Begin and parts take the same gate (a person, never an agent); the upload record is read in the person's scope and only the person recorded at begin writes its parts; the artifact row is written in the person's scope. Agents do not upload: they attach their own stored outputs.
*   **Names.** Every name that reaches a staged path or the turn text passes `attachmentName`: the base name only, no control or line-separator characters, no leading dots, at most 255 bytes, so a crafted name neither leaves the input folder nor starts a line of its own in the agent's turn; the executor refuses a path or type that still holds one.
*   **Sending.** `message_send` and `session_start` take `attachments: artifactId[]`. Each must be stored, readable in the caller's scope and in the session's project; an operator upload or an agent's own stored output (`artifact_save`, role `original`) qualifies, so an agent attaches files through the same MCP tool. An upload not yet attached joins the session (`session_id`); an upload of another conversation is refused. A repeated request id answers what was sent without checking its files again (a lost reply is retried safely). The message keeps `attachments: [{ artifactId, name, mediaType, size }]`, and the same list goes to its execution's dispatch payload; a request id repeated with other attachments is 409. A message may be files alone (no `text` needed). A message with files is queued while the session is busy, never steered: files are staged before a turn starts, not into a running one.
*   **Delivery to the agent.** The start command gets each attachment twice: as an input `{ artifactId, path }`, which the executor downloads before the harness starts (`stageInputs`), and in `attachments` with path, name, type and size. The path is `attachments/<execution>/<name>` under `FLEET_INPUT_DIR` (the session's `input` folder, so each turn gets its own folder; a repeated name in one message gets its position as a prefix). The executor appends to the turn text one line per file: absolute path, media type and size (`go/executor/attachments.go`). Images (PNG, JPEG, GIF, WebP) are also passed natively: Codex gets a `localImage` input item per image in `turn/start`; Claude gets the session's input folder as `--add-dir` on every turn (files of earlier turns stay readable on a resumed one) and, when its profile grants Read, the line asks it to open the image with Read, which shows images to the model. Other harnesses read the listed paths. Executors report the capability `attachments`; a turn with files is dispatched only to such a host ("executor lacks attachments; update required" otherwise), so an older executor never stages files without telling the agent. Staged names are unique in the folder whatever the file system's case rule.
*   **Transcript.** The operator `text` block carries `attachments` (`ResultArtifact[]`); the thread shows them under the message (images as thumbnails from `/api/results/:id`, other files as chips), and a click opens the result viewer. `thread_export` adds one `*Attachment:* <name>` line per file.
*   **Verification.** `go test ./coordinator -run TestAttachmentName` (names); `go test ./executor -run TestAttachTurn` (turn text, refused paths and Codex image items); paste, drop, progress, Send waits, transcript and Stop by a live check on the deployed site.

### Titles

A conversation's title is `session.title`; `titleBy` says who set it: `auto` for a generated title, else the person or agent.

*   **Explicit**: `session_rename({ sessionId, title })` (operation and agent tool; a host agent renames its own session by default) stores 1 to 200 characters holding a word, whitespace collapsed, never a UUID (OR-38). A person renames a conversation they direct (the owner any). The detail menu's Rename sends once and refreshes the title when the request outcome is unknown. An explicit title is never replaced.
*   **Generated**: the `session-titles` pass (`go/coordinator/session_titles.go`, every 30 s, `FLEET_SESSION_TITLE_TICK_MS`) takes up to 10 unarchived sessions with no title, a first operator message and its first readable agent reply from a run that has ended or has run for 2 minutes, newest first. It sends the first message (up to 3,000 characters: a longer one as its first and last 1,500, since standing rules open it and the task often ends it) and the first reply (1,500) to the title model and stores the first line of its answer that holds a word once cleaned (no wrapping quotes, markdown marks, `Title:` label, final period or UUID; at most 80 characters) only while the session still has no title, so a rename during the call wins. The same pass backfills older conversations; it needs no hand edit. Writing a title does not change `updated_at`, so the list's activity order stays.
*   **Model and cost**: `gemini-3.5-flash-lite` through the `google-ai` connection's `generate_content` (granted to every project; no new key), with a read token of the session's project, only when the source message and reply belong to that project, as the coordinator's other own calls. Each call is a `tool_run` with the provider's token count, recorded as system activity (`system_actor` coordinator); `titleCall` on the session names it. A durable call reservation prevents replay after an uncertain outcome. An explicit rename can resolve the missing title. Titles remain scoped to the project in which they were set. A failed call stores `titleAttemptAt` (the reason goes to the log and the pass span; a project the gateway refuses, only to the log); the session is tried again an hour later, and a session whose own failures (its reply read, an empty or unreadable answer; logged, not a failure of the pass) reach 3 keeps its first message. A gateway refusal for its project (not granted, intake paused, budget reached) uses no try; the session waits an hour and the project is skipped for the rest of the pass. A failure that is not the session's own (token, gateway, key, timeout, rate limit, any provider error such as a 400 for Fleet's own request, a database conflict) fails the pass, stops the batch and uses no try; that session is tried again 5 minutes later. Rename names any conversation at any time. A pass cut short by shutdown marks nothing. A rename during the call keeps the rename; `titleCall` names only the call that made the current title (each call stays a `tool_run`). A project the connection is not granted to gets no second token mint in the same pass. A session written back by another writer keeps its title fields (`putDocument`). Without a gateway (`FLEET_GATEWAY_URL` unset) the pass reads and marks nothing.
*   **Shown**: the list row and the search hit use the title, else the first message; the detail header shows the title, then the code and the profile, muted, on one line with an ellipsis (on a phone the title takes its own line).

Host command sessions (`actor: program-worker`: pipeline script steps, project readiness checks, host tools) hold one host job and no conversation. Runners and executions list them; the Conversations list and search leave them out. Pipeline script steps created such sessions without the mark until migration 72, which marks the earlier ones (a pipeline session with no message, at least one host job and no other execution). A code jump in search leaves them out too.

### Data flow

```text
harness stdout --> executor harness projection --> event doc { data.fleetNormalized.blocks }
                                                      |
question / artifact / execution rows (same seq) ------+
                                                      v
                         coordinator thread_entries / thread_search / thread_export
                                                      |
             change feed (live) ---------------------+
                                                      v
     app: threads.ts window state --> VirtualList (onStart/onEnd/scrollToKey) --> row renderers
                                                      |
                              selection state --> copy (thread_export with range)
```

### Live check

The browser check and its captured fixtures were deleted on 2026-11-25 ([checks per change](fleet-delivery-pipeline.md#checks-per-change)). A change to the conversation screen is checked live on the deployed site: newest entry visible on open; scroll up loads older pages without anchor drift; jump to a search hit; expand keeps the header in place; streaming append with the pill; one question answer; reply copy; stop on one of two executions; at 1440 and 390 px the header shows the title before the code and profile and fits its width; result names stay inside the column; copy icons show on hover or focus without moving the row, and always on touch; Rename stores the title and updates the list.

## Non-Functional Requirements

*   **Security**: Artifacts and tool output are untrusted agent output. Text uses `Range` pages; raw bytes and interactive previews have separate CSPs. The viewer follows the [result-viewer control](../design-system/controls/result-viewer.md): HTML preview scripts have an opaque origin, no same-origin app access and `connect-src 'none'`; permitted static-resource sources remain explicit in the CSP. SVG uses an image; Markdown in replies has no raw HTML.
*   **Performance**: No budgets. Every read is a page over an index range; no operation loads a whole session.
*   **Correctness**: Thread reads return a feed cursor from the same snapshot, so no row is skipped; late rows are placed by key under anchor keeping.
*   **Compatibility**: Old records are not re-projected and the old view is not kept behind a flag.

## Risks & Mitigations

| Risk | Mitigation |
|---|---|
| A slow transaction commits a row with a lower key after the client loaded newer rows. | Snapshot-plus-feed-cursor read; the row is placed by key and anchor keeping holds the reading position. Test case in `threads.test.ts`. |
| A reply cut off before its final block is not found by search (search reads only final blocks). | Accepted trade-off; it is shown in the thread. |
| Search with `ILIKE` is slow on long threads. | A text index is added when a measurement shows the need. |
| `total` count is slow on long threads. | Returned only on open and jump; its time is reported. |
| An event has no raw record or an unknown shape. | It gets one `tool` block with its raw JSON, so nothing is hidden. |
| Untrusted artifact content runs script in the operator's browser. | Separate raw-byte and preview routes; opaque sandbox origin, no same-origin app access, and explicit CSP restrictions. SVG is an image; Markdown has no raw HTML. |
| Clipboard write fails after the server round trip. | `ClipboardItem` with a promise created in the key handler; failure shown in the status line with retry. |
| Two different answers race. | `UPDATE ... WHERE state = 'open'`; the loser gets 409 and sees the stored answer. |

## Implementation

Owning implementation: block projection in `go/executor/harness_blocks.go`; `thread_entries`, `thread_search`, `replyText` and the feed cursor in `go/coordinator/threads.go`; the screen in `app/src/views/conversations.tsx`, `app/src/threads.ts` and `app/src/selection.ts`; the Results page in `app/src/views/results.tsx`; the viewer in `ResultViewer` (`packages/ui/src/controls.tsx`).

*   **Block key**: the string `` `${seq}:${index}` ``, compared numerically by `seq` then `index`. Coordinator-made blocks (`question`, `boundary`, `result`) use index 0 on their own row.
*   **Snapshot and feed cursor**: thread operations run in one read snapshot, and `feedCursor` is read inside it.
*   **Stored event shape**: an `event` document's `data` holds `executionId`, `sessionId`, `seq` (per execution), `type` and `data` (the native record); `data.data.fleetNormalized` is the projection.

### Verification

*   Build: `pnpm run build`; `cd go && go vet ./... && go test ./...`; `pnpm run check:design`.
*   Tests: every block kind, every paging direction, the join of a taskless session, `event-delta` replay, and the repeated and conflicting answer (`tests/ui/threads.test.ts`). Title cleaning: `TestCleanTitle` (`go/coordinator/session_titles_test.go`).
*   Browser: the [live check](#live-check), plus the header button, `/conversations/new` in the work area with Back, the presets and a start in `cs`.
*   Start a conversation: `go/coordinator/presets_test.go` (preset validation and the system presets' profiles), `go/executor` `TestReadOnlyStart`.
