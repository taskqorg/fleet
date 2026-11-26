# Work facts

**Code.** `WorkFacts` and `peerFence` in `packages/ui` (`src/controls/lists.tsx`), inside the shared [row facts](../patterns/list-view.md) zone (`RowFacts`, [OR-37](../patterns/list-view.md#rules-kept-here)). Demo: [book](../book/index.html#work-facts). Board: frame `Work facts` on [blocks.bl](../board/blocks.bl); the Live rows in [screens/live.bl](../board/screens/live.bl), frame `Work facts · Live rows` (owner pick B).

**Purpose.** One way to show how a piece of work is going, in every list that shows work: how many times it was tried, how long it has been in work, and which agent does it; the latest try and the time in its state on hover. A row that looks fine but has made 500 attempts, or has been in work for days, stands out at a glance ([OR-63](#rules-kept-here)).

**Anatomy.** The quiet form the owner picked (option B, [decision](../decisions.md#2026-11-25-one-row-anatomy-for-work)): compact facts in mono, `fg-muted`, in the row's right zone ([OR-21](data-list.md#rules-kept-here)), always in this order, the time last so the times line up in one column down a list ([OR-64](#rules-kept-here)). The row's other facts (codes, size, resources, the now line, a question chip, a stage) come first, through `before`:

| Part | Shows | Icon | Tooltip |
|---|---|---|---|
| Attempts | `×N` | none | "N attempts" (a list may name them: "N failed starts", "Attempt N of its task") |
| Agent | the agent's [harness mark](harness-counts.md) | the mark | the agent's name |
| Time | the wait for the person when the item waits (since its oldest open question), else the time in work since the first attempt started ([OR-2](number.md#rules-kept-here)) | hourglass, else clock | the exact start; for the time in work also the latest attempt and "`<State>` since", each as an exact time |

The latest attempt and the time in the current state are not on the row: they are in the tooltip of the time in work and in the item's detail. A row with no time in work shows the time since the latest attempt instead (a failing worker slot, repeat icon), else the time in its state (a finished task, `Completed for` in the tooltip). A list whose time is another fact passes it as `time`, still last: a result's Created age, an ended run's duration, a conversation's last activity. The item's state itself is the row's state icon on the left ([OR-22](data-list.md#rules-kept-here)).

**When each part shows.** By meaning, never by a fixed number:
* `×N` from two attempts: one attempt is the usual case and says nothing.
* The waiting time whenever the item waits for the person and the oldest open question is known; it takes the place of the time in work.
* Time in work whenever the first attempt is known.
* In the tooltip, the latest attempt and the state's start only when they are a different moment from the start of work (more than a minute apart).
* The agent mark when the agent is known.

**Outliers.** The list passes its rows' attempts, waiting times and times in work through `peerFence`. A value at or above ten times the peers' median (an order of magnitude) takes the attention tone: `×N` on the attention background, the time as an alert age chip with a warning icon. Work counts and times are multiplicative, so the fence is a decimal order over the typical row, not a spread statistic: with most rows at one attempt, a spread (Tukey, MAD) is zero and would mark every second attempt. Fewer than three peers give no fence. Attention, not danger: an outlier needs a look, it is not proven trouble ([OR-56](../owner-rules.md), [OR-18](../owner-rules.md)).

**States.** Normal; outlier (as above); stale (the row is dimmed, [offline marker](offline-marker.md)).

**Behaviour.** Ages tick in place with fixed-width figures ([OR-6](../owner-rules.md)). A part that appears never moves the title: the facts wrap under it when the row is too narrow ([OR-23](../owner-rules.md), [OR-60](../patterns/app-shell.md#rules-kept-here)).

**Accessibility.** Each age's accessible name is its long form ("3 hours ago"); the tooltip facts are also in the item's detail, so touch reaches them ([OR-20](../owner-rules.md)); `×N` is named "N attempts"; the agent mark is `role="img"` named by the agent.

**Do.** Pass the peers of the same list. **Don't.** Add a fixed limit ("more than 10 attempts"), a cost or a model ([OR-54](../owner-rules.md)).

**Used in.** [Live](../pages/live.md): worker slots, In work, Delivery (after the stage chip), Just now (the event's age after the facts); [Tasks](../pages/tasks.md) rows (peers: the page's open tasks; a finished task has no time in work); [Conversations](../pages/conversations.md) rows (waiting time, time in work of the current run, failed starts; no `×N` of runs: a conversation is not one task); [Results](../pages/results.md) rows (the task's attempts, the agent of the run, the Created age last); [Runners](../pages/runners.md) rows (which attempt of its task the run is, the agent, time in work or the duration last); [Home](../pages/home.md) cards (at the right end of the meta line; no agent mark, the avatar says who). Board: frames `Work facts · Conversations`, `· Results`, `· Runners` and `· Home cards` on the [conversations](../board/screens/conversations.bl), [projects](../board/screens/projects.bl), [system](../board/screens/system.bl) and [home](../board/screens/home.bl) canvases ([decision](../decisions.md#2026-11-25-work-facts-on-four-more-lists-time-last)).

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **OR-63 Work facts.** Every list row of work (a task, a run, a pull request, a repeated event) shows the shared [work facts](../controls/work-facts.md) in its right zone: `×N` attempts from two, agent mark, then the time (OR-64); the latest attempt and the time in its state are in the tooltip of the time in work and in the detail. A row an order of magnitude above its peers' median in attempts or time in work takes the attention tone; no fixed limit decides.
* **OR-64 Time last.** In every row and card the time is the rightmost fact: a waiting time, a time in work, a latest attempt, an age or a duration comes after every other fact and chip, so the times line up in one column down a list and are read at a glance. A row's other facts, its stage and its agent mark come before it ([work facts](../controls/work-facts.md)).
