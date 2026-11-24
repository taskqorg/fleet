# Catalogue (architect)

**Target home.** [Library](library.md) › Methods: `/library/methods/<id>`; `/catalogue` redirects there.

**App routes:** `/catalogue` and `/catalogue/<id>` (`app/src/views/definitions.tsx`). **Mockup routes:** `#/catalogue` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). The app uses `Definitions` / `DefinitionDetail` with a Methods tab ([D20](../decisions.md)).

**Purpose.** See each capability, the methods that provide it and which method work will use ([capabilities, methods and tools](../../configuration.md#capabilities-methods-and-tools)).

**Layout.** [App shell](../patterns/app-shell.md), [list and detail](../patterns/list-detail.md), with the [definition editor](../patterns/definition-editor.md) in the work area. The work area uses [detail sections](../controls/detail-section.md) and [numbers](../controls/number.md) with their basis.

**Catalogue row.** State (recommended method healthy, no recommendation, last run failed), capability name; on the second line the number of methods and projects; on the right the call count. Groups: shared, then one per project.

**Work area.**
1. **What the caller gets:** the input and result contract.
2. **Methods:** each method row shows its name, ID, scope, pipeline, optional `capability` chip, lifecycle `status` badge (`recommended`, `experimental`, `deprecated`), optional `description`, quality/time/cost from Lab assessments with basis, and "Recommended for".
3. **Which method a call uses:** the selection order as three lines: the task's explicit choice, the project's recommendation, the shared default.
4. Actions: "Recommend for a project" and "Compare methods in Lab". Changing a recommendation is its own action; an assessment never changes it.

The Methods tab selects a project, lists scoped method evidence and recommended projects, and keeps configuration/diff/history in the same work area. Quality shows its basis, observed eligible/expected population, split and evaluator version; partial values are labelled and unknown values remain unavailable. Typed comparison score is not selective accuracy. Evidence links stay within ordinary project grants.

Compare is an inline selection of at least two methods, a selected baseline and one or more use cases via [`EntityPicker`](../controls/entity-picker.md) (`mode="multiple"`). The picker groups active use cases into `LLM Code Benchmarks`, `Real Work`, and `Fleet & Projects`, hides archived (`obsolete`/`duplicate`) entries in `archivedNodes` behind the `Include archived` toggle, excludes `lab_fit === 'none'` from active selection by default, and labels each node with its ID and short goal. A visible selected recommendation may default the baseline; the saved first variant never reorders after recommendation changes. Creation opens an unlaunched experiment in the existing Lab screen (creating one experiment per selected use case and opening the first). The person supplies and durably saves goal/hypothesis there before explicit Launch. Each variant uses its own pipeline through retry, recovery and Promote.
