# Number with basis

**Code.** `Num`, `CostFigures`, `Totals`, `AgeChip`; text and `splitCostFigures` from `format.ts` in `packages/ui`. Demo: [book](../book/index.html).

**Purpose.** Shows any count, time, size or cost with its measurement basis: measured, estimated, subscription or unavailable ([principles §2](../principles.md#2-measurement-basis)). Multi-currency `(currency, basis)` `CostFigure[]` lists render via `CostFigures` so metered spend and subscription list-price equivalents never mix or sum into one figure.

**Anatomy.** Value in `font-mono` with tabular figures; unit; basis marker. `Totals` renders a ruled strip of labelled figures alongside optional notes (`sub`), keeping multi-currency metered and subscription figures separate.

| Basis | Look | Accessible name |
|---|---|---|
| Measured | `$3.10` in `fg` | "3.10 dollars" |
| Estimated | `≈ $0.42` in `estimated-fg`, dotted underline | "about 0.42 dollars, estimated from tokens" |
| Subscription | `≈ $0.42` + subscription indicator in `estimated-fg`, dotted underline (or `—` when unpriced) | "about 0.42 dollars, subscription list-price equivalent" (or "included in subscription") |
| Unavailable | `—` in `fg-muted` | "unavailable: no gateway" |
| Partial total | `≥ 182k` plus sub-label "2 of 3 measured" | "at least 182 thousand tokens, 2 of 3 executions measured" |

**Variants.** *Cell* (right aligned in a list); *total* (label above in `font-size-sm` `fg-muted`, value in `font-size-lg`, sub-label in `font-size-xs`).

**Change tint (OR-6).** A changed value tints for 2 s: green or red when the caller says what a rise means (`rise="good"|"bad"`), otherwise neutral. No arrow (OR-7). Reduced motion removes the fade, not the tint.

**Age chip (OR-2, OR-18).** An alert chip is never shown by colour alone: it carries a warning icon, and its accessible name and tooltip state the meaning, for example "13 hours ago, running too long" (D14, WCAG 1.4.1). `AgeChip` shows compact age (`4m`, `2h`, `3d`, `12 Nov`) and turns to an attention or danger tone past a threshold the caller supplies (for example a task running over its expected time). Exact time on hover.

**Behaviour.** Hover or focus shows the source: meter, estimate method, missing reason, time of measurement. Totals open their source records ([drill-down](../inventory.md#cross-cutting-needs)). Units are compact (`14m`, `1h 02m`, `182k`, `380 GB`) and the full value is in the tooltip. Live values change without animation.

**Accessibility.** Each value is focusable when it has a tooltip; the accessible name includes the basis.

**Do.** Show `—` for unknown. **Don't.** Show `0` or an empty cell for unknown; round an estimate into a measured-looking number.

**Used in.** Projects analytics, Task list and totals, Conversations boundaries/totals, Agents and Pipelines usage, Runners, Execution tree, Quota, Host stores, Laboratory comparison, History.
