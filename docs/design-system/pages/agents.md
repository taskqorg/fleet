# Agents (architect)

**Mockup routes:** `#/agents` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Not in the app yet ([D20](../decisions.md)).

**Purpose.** Configure and qualify the agents operators may use ([UC5](../../use-cases.md#uc5)), and keep them as a library ([requirements](../../designs/fleet-definition-editors.md#library)): what agents we have, which are active, what each is chosen for, which wait for a repository version.

**Layout.** [App shell](../patterns/app-shell.md), [list and detail](../patterns/list-detail.md), with the [definition editor](../patterns/definition-editor.md) in the work area. The work area uses the [draft bar](../controls/draft-bar.md), [setting fields](../controls/setting-field.md), [choices](../controls/choice.md) and [detail sections](../controls/detail-section.md).

**Catalogue row.** One line: the agent's mark (icon and monogram), id, a count of folded Lab variants, purpose; on the right an "update" mark when the repository has a newer version, 30-day runs and list price (a draft shows by its state icon, not a word, [formats](../formats.md#rows-and-columns)). Groups: one per category, active first. Facets: active, unused, update waiting, store only, Lab. Filter words: `category:`, `harness:`, `skill:`, `project:`.

**Tabs.** Agents, Skills and Pipelines are three tabs of one library section; each keeps its own list and work area.

**With nothing open.** The work area shows agents waiting for a repository version with the change that caused it and one "Take the repository version" action, where the month's spend went, agents nothing names, and the Lab variants folded away.

**Opened agent.** Head with mark, id, category and purpose, active revision; Chosen for (project roles, presets, pipeline steps, methods); variants; then the existing tabs.

**Skills tab.** Row: state (active, update waits, unused), id, description, who uses it. Opened skill: its `SKILL.md` path and text, the rollout (repository, then k of n agents active), the agents that carry it with "repository only" marked, the agents left out with the reason, and "Add to agents" by name or category.

| Inventory item (Agents) | Shown by |
|---|---|
| Harness, model, instructions, skills, tools, environment requirements | Configuration tab: Identity (purpose, role, harness, model, effort), Instructions, Skills, Tools (harness tools, connections, environment needs) |
| Effective instructions and settings | Effective tab: ordered layers with source and revision |
| Diff between revisions; 30-day usage and per-revision resources | Diff tab; Usage tab (`definition_usage`: 30-day total and `byRevision` executions, wall/CPU time, memory, tokens, multi-currency/subscription `CostFigures`, and link to `/projects` analytics) |
| Harness facts and limitations per version | Compatibility tab |

**Open.** Editing skill files in Fleet; categories as their own definitions.
