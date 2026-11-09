# Operations summary

**Job.** Answers the operator's and architect's frequent questions at a glance: what needs me, what failed, what is running against capacity, where work waits, and what it costs (D9, specq W1–W15).

**Composition.**
* A [stat strip](../controls/stat-strip.md) at the top of the page: need you, failed today, running/slots, queued, spent today (with basis).
* *Workers per host:* rows with host state icon, name, running/slots and queued. An offline host shows its last known value as a lower bound.
* *Accounts:* quota window fill as [fill bars](../controls/fill-bar.md) in percent, with reset time. Unknown is shown as unavailable.
* *Projects:* [state counters](../controls/state-counters.md) per project for waiting, failed, in progress and queued. A click opens the filtered task list.

**Rules.**
* Every number links to the records behind it.
* Values refresh in place and show their age (`updated 20s`).
* No gauges, no big cards.
