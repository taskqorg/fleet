# Agents (architect)

**Mockup routes:** `#/agents` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Not in the app yet ([D20](../decisions.md)).

**Purpose.** Configure and qualify the agents operators may use ([UC5](../../use-cases.md#uc5)).

**Layout.** [App shell](../patterns/app-shell.md), [list and detail](../patterns/list-detail.md), with the [definition editor](../patterns/definition-editor.md) in the work area. The work area uses the [draft bar](../controls/draft-bar.md), [setting fields](../controls/setting-field.md), [choices](../controls/choice.md) and [detail sections](../controls/detail-section.md).

**Catalogue row.** State, name; on the second line the harness, model and role; on the right the run count over 30 days (a draft shows by its state icon, not a word, [formats](../formats.md#rows-and-columns)). Filters: harness, role.

| Inventory item (Agents) | Shown by |
|---|---|
| Harness, model, instructions, skills, tools, environment requirements | Configuration tab: Identity (purpose, role, harness, model, effort), Instructions, Skills, Tools (harness tools, connections, environment needs) |
| Effective instructions and settings | Effective tab: ordered layers with source and revision |
| Diff between revisions; 30-day usage and per-revision resources | Diff tab; Usage tab (`definition_usage`: 30-day total and `byRevision` executions, wall/CPU time, memory, tokens, multi-currency/subscription `CostFigures`, and link to `/projects` analytics) |
| Harness facts and limitations per version | Compatibility tab |

**Open.** The skill editor; roles as their own definitions.
