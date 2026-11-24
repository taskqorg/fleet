# Home (screen 1)

**Status.** Target ([OR-50](../owner-rules.md)). Until it is built, `/` opens the [Overview](overview.md).

**App route:** `/home`, and `/` opens it. Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** Answers "who works for me, and what do they need from me?" ([principles](../owner-rules.md#principles) 2, then 1). Work is done by talking to sessions; Home is where every session starts and where its questions are answered.

## Layout

A full-width page right of the navigation ([OR-24](../owner-rules.md)). The shell header holds only the section title, the focus chip and the search icon ([OR-43](../owner-rules.md)). From top to bottom:

1. **Greeting and pulse line.**
   * A greeting with the person's name.
   * One pulse line, read left to right: working, queued, done today, then blockers. Counts follow OR-29.
   * Each blocker is one chip in the danger tone, with its cause and `×N` ([OR-56](../owner-rules.md), [OR-58](../owner-rules.md)); a click opens [Live](live.md) at that host or cause.
   * The line ends with `Live ›`.
2. **Composer.** Picks who to talk to and sends the first message; see **Composer** below.
3. **Session groups.** Cards in this order, each group with its count; an empty group is hidden:
   1. **Waiting for you:** sessions with an open question to this person, oldest question first.
   2. **Failed:** sessions whose latest run failed.
   3. **Working:** sessions with a running agent run, most recent activity first.
   4. **Queued:** sessions waiting for a slot.
4. **Asleep.** Bots with no active run, as one row of dashed tiles: name, what the bot is for, when it was last used, and a Wake button.

A group longer than one row shows its latest cards and `+K` (OR-30); `All conversations ›` opens [Conversations](conversations.md) with the same filter. A finished session leaves Home and stays in Conversations.

The focus (OR-34) narrows every group. Ignored projects never show.

## Session card

One card per session; the board shows its anatomy ([design board](#design-board)). Content, top to bottom:

* **Head:** avatar (the bot's mark, or `P` for a project PM in the project's colour), name and project chip.
* **State:** the word and dot from the [state vocabulary](../owner-rules.md) (OR-55).
* **Now:** one line saying what the session does now.
* **Progress:** a thin bar, only when the session has a plan with steps.
* **Meta line:** step, elapsed time (OR-2), host, and the next timer when one is set.
* **Question:** at most one, the oldest open one, with its choices as buttons and a free-text answer (OR-53). Answering edits the card in place; the next question, if any, takes its place.

Behaviour:

* The whole card opens the session in the [one session view](../owner-rules.md) (OR-26, OR-51); buttons act in place.
* A card never shows model, tokens, cost or ids (OR-54).
* Cards update in place without moving the card being read (OR-6, OR-23). A session that changes group moves after a short highlight.

## Composer

* **Picker.** Lists, in this order:
  1. Project PMs (from session presets).
  2. Bots, with their state.
  3. "New session with a preset…".
* **Search.** Typing in the picker filters it by name and project.
* **Default.** The picker starts with the last session the person talked to from Home.
* **Send.** Enter sends the message and opens that session. Sending to an asleep bot wakes it with its memory; sending to a project PM that has no open session starts one from the project's PM preset.
* **Same as Telegram.** The picker is the one Telegram uses for `/pm` and `@bot` ([FLT-31](https://youtrack.ufobe.com/issue/FLT-31)).

## States

* **Loading:** card skeletons in the group places, with the composer usable at once.
* **Quiet** (nothing waiting, nothing failed):
  * A summary card replaces the empty groups: what happened since the person's last visit, in numbers (tasks done, pull requests merged, failures), and who keeps watch.
  * Two buttons: one asks the watching bot "what happened?", the other opens Live.
  * No empty grids ([useful idle work area](../patterns/list-detail.md)).
* **Offline or failed read:** the last known cards stay, marked stale with their age and a retry ([offline marker](../controls/offline-marker.md)).
* **Phone (390 px):**
  * The rail is the bottom bar of five (OR-60).
  * The pulse line wraps.
  * Cards are full width, and questions answer in place.

## Inventory

| Inventory item | Shown by |
|---|---|
| Everything waiting for a person: questions, acceptance, approvals | Waiting for you cards with the question in place |
| Sessions, including with the PM: what each does now | Session cards; the card opens the session |
| Bots: which are active, which sleep, run now | Working cards and the Asleep row with Wake |
| Problems that need a look | Blocker chips in the pulse line, aggregated by cause |
| Tasks across projects by state | Pulse line counts; drill-down through Live and Projects |

## Data

| Part | Read or action |
|---|---|
| Groups and cards | `sessions_list` grouped by state, in attention order (waiting, failed, running, preparing); rows carry `activityAt` |
| Questions on cards | `questions_list` for the person, matched to the asking session; answered with `reply` / `question_answer` |
| Asleep bots | `bot_list`; Wake is `bot_run`; a message to a bot's Home is `message_send` |
| Composer | `session_presets`, `session_start`, `message_send` |
| Pulse line | `nav_counts`, `task_page` counts for done today, `executions_list` failures grouped by cause |
| Live updates | `changes_since` over sessions, questions, bot runs and executions |

## Open

* **"Now" line source:** no field holds a one-line summary of what a session does. Candidates: the attached task title with its current step, or the first sentence of the latest agent message.
* **Progress and plan steps:** these come from the session context panel ([FLT-30](https://youtrack.ufobe.com/issue/FLT-30)).
* **Quiet summary:** the "since your last visit" summary needs the person's last-visit time.


## Blocks

* [Pulse line and blocker chip](../controls/pulse-line.md): the glance line under the greeting.
* [Composer with target picker](../controls/composer.md#target-picker): picks who to talk to and sends the first message.
* [Group head](../controls/data-list.md): the name and count of each session group.
* [Session card](../controls/session-card.md): one card per active session.
* [Question card](../controls/decision-block.md#card): the open question on a card, answered in place.
* [State dot and word](../controls/state-badge.md#dot-and-word): the state on cards and picker rows.
* [Bot tile](../controls/bot-tile.md): the Asleep row.
* [Quiet summary](../controls/quiet-summary.md): replaces the empty groups when nothing needs the person.
* [Navigation rail and phone bottom bar](../controls/nav-rail.md#board) and [shell header](../patterns/app-shell.md#board): the frame.

## Design board

The rendered states are on the [design board](../README.md#design-board), canvas [screens/home.bl](../board/screens/home.bl): normal, picker, session open with context panel, quiet, and phone. Every part is an instance of a block on [blocks.bl](../board/blocks.bl) (see Blocks above), so a block change reaches these states; the card anatomy and its states are on that canvas. Names are real; content is illustrative. An approved state is rebuilt from `packages/ui` and the book (OR-13).
