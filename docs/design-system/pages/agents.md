# Agents (architect)

**Mockup routes:** `#/agents` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Not in the app yet ([D20](../decisions.md)).

**Purpose.** Configure and qualify the agents operators may use ([UC5](../../use-cases.md#uc5)).

**Layout.** [App shell](../patterns/app-shell.md), [list and detail](../patterns/list-detail.md), with the [definition editor](../patterns/definition-editor.md) in the work area. The work area uses the [draft bar](../controls/draft-bar.md), [setting fields](../controls/setting-field.md), [choices](../controls/choice.md) and [detail sections](../controls/detail-section.md).

**Catalogue row.** State, name; on the second line the harness, model and role; on the right the run count or the number of unsaved changes. Filters: harness, role.

| Inventory item (Agents) | Shown by |
|---|---|
| Harness, model, instructions, skills, tools, environment requirements | Configuration tab: Identity (purpose, role, harness, model, effort), Instructions, Skills, Tools (harness tools, connections, environment needs) |
| Effective instructions and settings | Effective tab: ordered layers with source and revision |
| Diff between revisions; where a configuration is used | Diff tab; Usage tab (pipelines, catalogue methods, 30-day executions, time, cost) |
| Harness facts and limitations per version | Compatibility tab |

**Open.** The skill editor; roles as their own definitions.
