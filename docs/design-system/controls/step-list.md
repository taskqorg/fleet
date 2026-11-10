# Step list and flow strip

**Code.** `StepList` and `FlowStrip` in `packages/ui`. Demo: [book](../book/index.html#step-list).

**Purpose.** Read and edit a pipeline's steps, and read a run of it, in one list ([pipelines](../pages/pipelines.md), [D20](../decisions.md)).

**Anatomy of a step row.** Number; the kind icon (agent, tool, program, pipeline, from the registry); the step's name; its executor; on the second line what it reads and what it writes. In a run the kind icon gives way to the [state badge](state-badge.md), and the second line gives way to a note chip ("reused", "7 of 8", "waits for render"), time and cost with their basis ([number](number.md)).

**Bands.** A band row marks structure: "Repeat until … at most N" before the steps of a loop, "In parallel" before steps that run together. Steps inside carry an inset bar on the left.

**Behaviour.**
* A click on a row opens that step's editor under the row (OR-14, OR-26); a second click closes it. One step is open at a time.
* The open step has the `selected` background and the accent tick.
* A failed step shows its error under the row. The error is never truncated (OR-19).

**Flow strip.** A read-only picture drawn from the same steps: boxes joined by wires in order, a bracket around parallel steps, a dashed frame with a caption around a loop, a dashed box for a step that runs only on a condition. The open step is marked. It has no controls: the list is the one place to edit.

**Keyboard.** Each row is a button: `Tab` moves between rows, `Enter` or `Space` opens.

**Accessibility.** The list has `role="list"` and a name; a row exposes `aria-expanded`. The strip is one image whose accessible name describes the order in words.

**Do.** Keep the executor's note to one short clause. **Don't.** Add a second editable view of the same steps.

**Used in.** Pipelines.
