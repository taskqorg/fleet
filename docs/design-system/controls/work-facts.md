# Work facts

**Code.** `WorkFacts` and `peerFence` in `packages/ui` (`src/controls/lists.tsx`), inside the shared [row facts](../patterns/list-view.md) zone (`RowFacts`, [OR-37](../owner-rules.md)). Demo: [book](../book/index.html#work-facts).

**Purpose.** One way to show how a piece of work is going, in every list that shows work: how many times it was tried, how long ago the latest try started, how long it has been in work, how long it has been in its state, and which agent does it. A row that looks fine but has made 500 attempts, or has been in work for days, stands out at a glance ([OR-63](../owner-rules.md)).

**Anatomy.** Compact facts in mono, `fg-muted`, in the row's right zone ([OR-21](../owner-rules.md)), always in this order:

| Part | Shows | Icon | Tooltip |
|---|---|---|---|
| Attempts | `×N` | none | "N attempts" |
| Latest attempt | time since the latest attempt started ([OR-2](../owner-rules.md)) | repeat | "Latest attempt", then the exact time |
| In work | time since the first attempt started | clock | "In work since the first attempt", then the exact time |
| In state | time in the current state | history | "`<State>` for", then the exact time |
| Agent | the agent's [harness mark](harness-counts.md) | the mark | the agent's name |

Each time has its own icon, so two neighbours never read as one duration (`3h 35m`). The item's state itself is the row's state icon on the left ([OR-22](../owner-rules.md)); work facts carry only its age.

**When each part shows.** By meaning, never by a fixed number:
* `×N` from two attempts: one attempt is the usual case and says nothing.
* Time in work whenever the first attempt is known: it says how long the work has been running.
* Time since the latest attempt only when it is a different moment from the start of work (more than a minute apart); with one attempt it is the same fact and shows once.
* Time in state only when it is a different moment from both starts.
* The agent mark when the agent is known.

**Outliers.** The list passes its rows' attempts and times in work through `peerFence`. A value at or above ten times the peers' median (an order of magnitude) takes the attention tone: `×N` on the attention background, the time as an alert age chip with a warning icon. Work counts and times are multiplicative, so the fence is a decimal order over the typical row, not a spread statistic: with most rows at one attempt, a spread (Tukey, MAD) is zero and would mark every second attempt. Fewer than three peers give no fence. Attention, not danger: an outlier needs a look, it is not proven trouble ([OR-56](../owner-rules.md), [OR-18](../owner-rules.md)).

**States.** Normal; outlier (as above); stale (the row is dimmed, [offline marker](offline-marker.md)).

**Behaviour.** Ages tick in place with fixed-width figures ([OR-6](../owner-rules.md)). A part that appears never moves the title: the facts wrap under it when the row is too narrow ([OR-23](../owner-rules.md), [OR-60](../owner-rules.md)).

**Accessibility.** Each age's accessible name is its long form ("3 hours ago"); `×N` is named "N attempts"; the agent mark is `role="img"` named by the agent.

**Do.** Pass the peers of the same list. **Don't.** Add a fixed limit ("more than 10 attempts"), a cost or a model ([OR-54](../owner-rules.md)).

**Used in.** [Live](../pages/live.md): worker slots, In work, Delivery, Just now. Next: the Tasks, Conversations, Results and Runners lists ([decisions](../decisions.md#2026-11-25-one-row-anatomy-for-work)).
