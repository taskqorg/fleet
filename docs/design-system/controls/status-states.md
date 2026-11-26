# Empty, loading, error and stale states

**Code.** `Skeleton`, `Empty`, `ErrorBanner`, `Stale` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** One look for the moments when a list or area has nothing, is loading, failed or is out of date ([principles §8](../principles.md#8-empty-loading-and-error)).

**Loading.** First load: skeleton rows at the real row height, `neutral-bg` bars, no shimmer when reduced motion is set. Later loads: keep data, dim to 60 % opacity only if the wait exceeds 300 ms.

**Empty.** Centred in the area, `fg-muted`: one sentence for why, one action. Kinds: *filtered empty* ("No tasks match these filters." + Clear filters); *first use* ("No results yet. Results appear when an execution stores one."); *all clear* ("Nothing is waiting for you.").

**Error.** Inline banner at the top of the affected area: `danger-fg` icon and text "Host list did not update: timeout after 10 s", "Retry" button only when the caller supplies a retry callback; a read-only error has no action. Existing data stays below. Whole-page errors only when nothing can be shown.

**Stale.** When data is older than the expected refresh, a `fg-muted` note "Updated 6 min ago" with "Refresh" appears in the area header; values from unconfirmed sources use the observation overlay of the [state badge](state-badge.md).

**Accessibility.** Errors use `role="alert"` once; loading areas set `aria-busy="true"`.

**Do.** Keep the layout during loading. **Don't.** Show spinners over empty pages or blank tables without explanation.

**Used in.** Every list and panel.

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **UR-34.** The first load keeps the layout and shows skeletons in the real row or card size; later loads keep the old data visible. *Check:* Browser: with a slowed network, layout shift on load is below 0.1.
* **UR-36.** An error says what failed, keeps the data already shown, offers retry, and marks only the affected area. *Check:* Browser: block one read with a route rule, expect a retry in that area and the rest intact.
