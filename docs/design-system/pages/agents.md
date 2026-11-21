# Agents (architect)

**App routes:** `/agents` and `/agents/<agent id>` (`app/src/views/agents.tsx` with `DefinitionDetail` from `app/src/views/definitions.tsx`). **Mockup routes:** `#/agents` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** Configure and qualify the agents operators may use ([UC5](../../use-cases.md#uc5)), and keep them as a library ([requirements](../../designs/fleet-definition-editors.md#library)): what agents we have, which are active, what each is chosen for, which wait for a repository version.

**Layout.** [App shell](../patterns/app-shell.md), [list and detail](../patterns/list-detail.md), with the [definition editor](../patterns/definition-editor.md) (`DefinitionDetail`) in the work area of the same `ListDetail`. The control column renders [`CatalogueTree`](../controls/catalogue-tree.md) for agents (with an icon-only `SearchField` per `OR-31` and harness/category filters) and `VirtualList` for the Skills tab. The work area uses the [draft bar](../controls/draft-bar.md), [setting fields](../controls/setting-field.md), [choices](../controls/choice.md), disclosures and [detail sections](../controls/detail-section.md).

**Catalogue tree & row.** Agents group cleanly by role (`Core`, `Research & Design`, `Assets & 3D`, `Other`) with `Lab variants` (`lab-*` and `variantOf` experiment profiles) folded at the bottom (`defaultCollapsed: true`), keeping depth at 2 levels (`<= 3`). Folder collapse and pins (`OR-12`, hoisted `Pinned` group and row context menu) persist per person (`person_settings`), while non-empty search queries auto-expand matching folders without mutating stored collapse state. Each leaf row leads with its [state badge](../controls/state-badge.md) (`OR-22`), agent ID (`OR-38`), concise `OR-45` delta markers in `sub`/`right` (only non-default model/effort/variant deltas, non-default harness mark, and repository update icon, without cluttering unchanged defaults). Filter words: `category:`, `harness:`, `skill:`, `agent:`, `state:`.

**Tabs.** Agents (Library Summary when idle) and Skills are two tabs of the library section; each keeps its own control-column list and idle work area. A `Pipelines` header action links directly to `/pipelines` instead of duplicating the Pipelines catalogue.

**With nothing open.** The work area shows the Agent Library Summary (no duplicate agent table): concise totals (`agents`, active in 30d, 30d runs and tokens), breakdowns by harness and by category (with quick filter actions), pending repository-sync actions (`Take repo`) for agents waiting on a repository version, and folded Lab variants.

**Opened agent (`OR-45 Diff from default`).** Head with `id`, path, active revision and state; Configuration tab shows **Identity** (`id`, category/project roles, `variantOf`/experiment link, custom `purpose`, tags), **Differences from default** (only fields differing from the `variantOf` base profile or system/harness defaults, rendered as `default → override` with an `override` badge), and a single collapsed `Inherited defaults (N)` disclosure holding all unchanged/inherited fields, followed by the Definition `CodeEditor`; then Effective, Diff, Compatibility, Usage and History tabs.

**Skills tab.** Row: state (active, update waits, unused), id, description, who uses it. Opened skill: its `SKILL.md` path and text, the rollout (repository, then k of n agents active), the agents that carry it with "repository only" marked, the agents left out with the reason, and "Add to agents" by name or category.

| Inventory item (Agents) | Shown by |
|---|---|
| Harness, model, instructions, skills, tools, environment requirements | Configuration tab: Identity (purpose, role, harness, model, effort), Instructions, Skills, Tools (harness tools, connections, environment needs) |
| Effective instructions and settings | Effective tab: ordered layers with source and revision |
| Diff between revisions; 30-day usage and per-revision resources | Diff tab; Usage tab (`definition_usage`: 30-day total and `byRevision` executions, wall/CPU time, memory, tokens, multi-currency/subscription `CostFigures`, and link to `/projects` analytics) |
| Harness facts and limitations per version | Compatibility tab |

**Open.** Editing skill files in Fleet; categories as their own definitions.
