# State badge

**Code.** `StateBadge` (states and icons in `registry.ts`) in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Shows the state of a task, execution, copy, setting or host, with its reason and release condition.

**Anatomy.** Registry icon + state word in `font-size-sm`, `font-weight-medium`, on a tinted fill with `radius-sm`; reason text after it in `fg-muted`; release condition on hover and in detail views.

**Mapping.**

| Family | Attention | Danger | Running | Success | Neutral |
|---|---|---|---|---|---|
| Task | Waiting | Failed | In progress | Completed | Queued, Paused, Cancelled |
| Execution | Waiting | Failed | Preparing, Running | Succeeded | Cancelled |
| Copy | | Unavailable | Pending | Stored, Placed | |
| Setting | | Apply error | Pending | Applied | |
| Host | | Offline | | Online | |

**Observation overlay.** Unconfirmed state keeps the last known state word and adds a dashed outline and a hollow dot: "Running, seen 4m" (OR-2). It never turns into Failed.

**Compact.** Icon only, word in the tooltip and accessible name (OR-1). Used in dense columns and the execution tree. Which variant task lists use is open choice B1.

**Behaviour.** A change fades in over `motion-fast`. The reason comes from the record; when none is recorded, the badge shows no reason rather than inventing one.

**Accessibility.** Accessible name joins state, observation and reason: "Waiting, acceptance by you, since 12 minutes". The state word is always text, so colour is never the only signal.

**Do.** Show the reason in lists. **Don't.** Use icons alone; add new colours per state.

**Used in.** All lists, task detail, execution tree, hosts, settings, result copies.
