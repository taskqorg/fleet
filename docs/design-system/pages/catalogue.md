# Catalogue (architect)

**Mockup routes:** `#/catalogue` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Not in the app yet ([D20](../decisions.md)).

**Purpose.** See each capability, the methods that provide it and which method work will use ([capabilities, methods and tools](../../configuration.md#capabilities-methods-and-tools)).

**Layout.** [App shell](../patterns/app-shell.md), [list and detail](../patterns/list-detail.md), with the [definition editor](../patterns/definition-editor.md) in the work area. The work area uses [detail sections](../controls/detail-section.md) and [numbers](../controls/number.md) with their basis.

**Catalogue row.** State (recommended method healthy, no recommendation, last run failed), capability name; on the second line the number of methods and projects; on the right the call count. Groups: shared, then one per project.

**Work area.**
1. **What the caller gets:** the input and result contract.
2. **Methods:** a table with state, name and ID, scope, what it runs (pipeline and agent), then quality, time and cost from Lab assessments, each with its basis, and "Recommended for".
3. **Which method a call uses:** the selection order as three lines: the task's explicit choice, the project's recommendation, the shared default.
4. Actions: "Recommend for a project" and "Compare methods in Lab". Changing a recommendation is its own action; an assessment never changes it.
