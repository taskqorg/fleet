# Agents (architect)

**App routes:** `/agents` and `/agents/<agent id>` (`app/src/views/agents.tsx` with `DefinitionDetail` from `app/src/views/definitions.tsx`). **Mockup routes:** `#/agents` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** Configure and qualify the agents operators may use ([UC5](../../use-cases.md#uc5)), and keep them as a library ([requirements](../../designs/fleet-definition-editors.md#library)): what agents we have, which are active, what each is chosen for, which wait for a repository version.

**Layout.** [App shell](../patterns/app-shell.md), [list and detail](../patterns/list-detail.md), with the [definition editor](../patterns/definition-editor.md) (`DefinitionDetail`) in the work area of the same `ListDetail`. The control column keeps one consistent `VirtualList` of agents (or skills) with an icon-only `SearchField` (`OR-31`) and harness/category filters whether idle or editing an agent. The work area uses the [draft bar](../controls/draft-bar.md), [setting fields](../controls/setting-field.md), [choices](../controls/choice.md) and [detail sections](../controls/detail-section.md).

**Catalogue row.** One line: the agent's mark (icon and monogram), id, a count of folded Lab variants, purpose; on the right an "update" mark when the repository has a newer version, 30-day runs and list price (a draft shows by its state icon, not a word, [formats](../formats.md#rows-and-columns)). Groups: one per category, active first. Facets: active, unused, update waiting, store only, Lab. Filter words: `category:`, `harness:`, `skill:`, `project:`.

**Tabs.** Agents (Library Summary when idle) and Skills are two tabs of the library section; each keeps its own control-column list and idle work area. A `Pipelines` header action links directly to `/pipelines` instead of duplicating the Pipelines catalogue.

**With nothing open.** The work area shows the Agent Library Summary (no duplicate agent table): concise totals (`agents`, active in 30d, 30d runs and tokens), breakdowns by harness and by category (with quick filter actions), pending repository-sync actions (`Take repo`) for agents waiting on a repository version, and folded Lab variants.

**Opened agent.** Head with mark, id, category and purpose, active revision; Chosen for (project roles, presets, pipeline steps, methods); variants; then the existing tabs.

**Skills tab.** Row: state (active, update waits, unused), id, description, who uses it. Opened skill: its `SKILL.md` path and text, the rollout (repository, then k of n agents active), the agents that carry it with "repository only" marked, the agents left out with the reason, and "Add to agents" by name or category.

| Inventory item (Agents) | Shown by |
|---|---|
| Harness, model, instructions, skills, tools, environment requirements | Configuration tab: Identity (purpose, role, harness, model, effort), Instructions, Skills, Tools (harness tools, connections, environment needs) |
| Effective instructions and settings | Effective tab: ordered layers with source and revision |
| Diff between revisions; 30-day usage and per-revision resources | Diff tab; Usage tab (`definition_usage`: 30-day total and `byRevision` executions, wall/CPU time, memory, tokens, multi-currency/subscription `CostFigures`, and link to `/projects` analytics) |
| Harness facts and limitations per version | Compatibility tab |

**Open.** Editing skill files in Fleet; categories as their own definitions.
