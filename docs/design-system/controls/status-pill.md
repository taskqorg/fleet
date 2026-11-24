# Status pill

**Code.** *Target*: `StatusPill` in `packages/ui`, not built yet. Board: [blocks.bl](../board/blocks.bl), master `Status pill`.

**Purpose.** The fleet's state in words, the first thing read on [Live](../pages/live.md): all is well, or the first problem (principle 1).

**Anatomy.** A `radius-pill` capsule, height `size-chip`: a dot and the text in `font-size-sm`, `font-weight-medium`, in the state's tone on its `-bg` fill.

**States.**

| State | Tone | Text |
|---|---|---|
| Working | success | "All systems working" |
| Problem | danger | the first problem in words, then "· N more" when there are more ([OR-56](../owner-rules.md), [OR-58](../owner-rules.md)) |
| Connecting | neutral | "Connecting…" while the first read loads |
| Stale | neutral | "Last update 2m ago" ([offline marker](offline-marker.md)) |

**Behaviour.**
* A click on the problem pill opens the failing host or cause; in wall mode it opens nothing.
* It changes tone in place; its width follows the text, and nothing after it moves ([OR-23](../owner-rules.md)): the status bar reserves the space.
* Recovery fades the danger tone within one update.

**Accessibility.** `role="status"` with polite announcements; the text is always visible, so colour is never the only signal.

**Do.** Name the cause in words. **Don't.** Show a count alone or an icon alone.

**Used in.** [Live](../pages/live.md) status bar.

## Status bar

The top line of [Live](../pages/live.md), master `Status bar` on [blocks.bl](../board/blocks.bl): the product mark, the status pill, then on the right the focus ([OR-34](../owner-rules.md)) and the clock in mono. Its variants follow the pill: `working` and `problem`. In wall mode it is the only chrome.
