# Pipelines (architect)

**Mockup routes:** `#/pipelines`, `#/pipelines/render-props` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Not in the app yet ([D20](../decisions.md)).

**Purpose.** Read, change and observe pipelines: the definition and its runs in one view ([UC4](../../use-cases.md), [pipeline execution](../../configuration.md#pipeline-execution)).

**Layout.** [App shell](../patterns/app-shell.md), [list and detail](../patterns/list-detail.md), with the [definition editor](../patterns/definition-editor.md) in the work area. The work area uses the [draft bar](../controls/draft-bar.md), the [step list](../controls/step-list.md), [choices](../controls/choice.md), [detail sections](../controls/detail-section.md) and, for a failed run, the [problem row](../controls/problem-row.md).

**Catalogue row.** State of the last run, name; on the second line the number of steps and agents and the scope; on the right the age of the last run. Groups: changed, shared, then one per project.

**Definition tab, from top to bottom.**
1. **Starts when:** triggers and run settings as chips.
2. **Flow:** a small picture of the steps, drawn from the list below. It is read-only (D20): there is one place to edit.
3. **Run parameters** as chips.
4. **Steps:** the step list.

**Step list.** One row per step: number, kind icon (agent, tool, program, pipeline), name, executor; on the second line what it reads and what it writes. Bands mark structure: "Repeat until … at most N times" around a loop, "In parallel" around steps that run together; steps inside are inset. A plus appears between rows to insert a step.

**Step editor.** A click opens the step in place, under its row (OR-14). It holds: kind; executor with a link to its definition; when the step runs; the assignment; inputs, each bound to a named output of an earlier step; outputs with type and whether required; and "Then", one line per outcome: pass, changes requested (a review outcome, not a failure), the round limit, failure.

**Runs tab.** The same step list with the run's state on top: state icon, time and cost per step, "reused" on steps taken from an earlier run, the error under a failed step, and what a waiting step waits for. A problem bar names the failed step and offers "Rerun failed" (saying what it repeats and what is kept), "Repeat all steps" and "Edit definition". A switch moves between the definition and recent runs.

**Not built in the system, shown only when the definition format carries it** ([configuration](../../configuration.md#review-and-rework)): review branches and rework loops, a step that runs on a condition, a model chosen by a rule, event triggers.
