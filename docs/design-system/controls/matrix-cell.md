# Matrix cell

**Code.** `MatrixCell` in `packages/ui` (`src/controls/matrix-cell.tsx`); Retry is a separate button beside the cell's open button. Demo: [book](../book/index.html#matrix-cell). Board: [blocks.bl](../board/blocks.bl), master `Matrix cell`, board *Data*.

**Purpose.** One example × variant in a [Lab](../pages/laboratory.md) experiment: how its repeats went, at a glance (principle 1).

**Anatomy.** A `surface` cell with a `line` border and `radius-md`: the [state icon](data-list.md#board), the score in mono at `font-size-lg` (per evaluator, never combined across forms), the repeats as `k/n`, and one line of time and cost with their basis ([number](number.md)).

**States.**

| State | Look |
|---|---|
| `pass` | success icon, score |
| `partial` | `attention-line` border, `k/n` in attention, the line says why a repeat was left out |
| `fail` | `danger-bg` with `danger-fg` border, the reason and Retry ([OR-56](../owner-rules.md)) |
| `running` | running border, `step k/n` and live time ([OR-6](../owner-rules.md)) |
| `excluded` | dashed border, "not in this comparison" |

**Comparison form.** In the Lab's Model × Pipeline comparison a cell sums the trials of one model and pipeline (`failRate` set): `k/n` counts succeeded of all trials, the line reads `median 4m · 2% failed`, and tokens and cost move to the tooltip ([OR-54](../owner-rules.md)). `partial` marks a failure rate of 10% or more, `fail` a pair with no succeeded trial.

**Behaviour.** A click opens the cell's trials in the work area ([OR-26](../owner-rules.md), [OR-27](../owner-rules.md)). Confounders (host, harness, model) are in the tooltip ([OR-59](../owner-rules.md)). Values update in place.

**Accessibility.** Each cell is a button named "Example 3, variant codeMode: pass, score 0.91, 3 of 3 repeats".

**Do.** Keep one score per evaluator. **Don't.** Average scores across evaluator forms.

**Used in.** [Lab](../pages/laboratory.md) experiment matrix.
