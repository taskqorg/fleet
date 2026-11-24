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
| Session | Waiting | Failed | Preparing, Running | Done | Idle, Stopped |
| Host | | Offline | | Online | |

**Observation overlay.** Unconfirmed state keeps the last known state word and adds a dashed outline and a hollow dot: "Running, seen 4m" (OR-2). It never turns into Failed.

**Compact.** Icon only. The word and the reason are in the tooltip and accessible name, never as visible text (OR-1, D14). Used in dense columns and the execution tree. Lists use the compact form (B1, [D8](../decisions.md)); the word next to the icon is for detail views only. The tooltip follows OR-20.

**Behaviour.** A change fades in over `motion-fast`. The reason comes from the record; when none is recorded, the badge shows no reason rather than inventing one.

**Accessibility.** Accessible name joins state, observation and reason: "Waiting, acceptance by you, since 12 minutes". The state word is always text, so colour is never the only signal.

**Do.** Show the reason in lists. **Don't.** Use icons alone; add new colours per state.

**Used in.** All lists, task detail, execution tree, hosts, settings, result copies.

## Dot and word

*Target* ([OR-55](../owner-rules.md)). Board: [blocks.bl](../board/blocks.bl), master `State`.

The form sessions, bots, tasks and runs share on cards, slots and the context panel: a 6 px dot and the state word in `font-size-sm`, both in the state's `-fg` tone, no fill. One word, icon and tone per state:

| State | Tone | Dot |
|---|---|---|
| Working | running | pulses while live |
| Waiting for you | attention | steady |
| Queued | neutral | steady |
| Done | success | steady |
| Failed | danger | steady |
| Stopped | neutral | hollow |
| Asleep | neutral | dashed ring |

Synonyms map to this set and never show: Succeeded and Completed are Done, Cancelled is Stopped, Idle is Asleep, Preparing and Running are Working. Until this form is built, the mapping table above stays what the app shows.
