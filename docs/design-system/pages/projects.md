# Projects

## Target

**Status.** Target, rail section Projects ([information architecture](../../designs/fleet-ia-and-journeys.md#1-closed-information-architecture)). Tasks, Results and Delivery lose their own rail items and become facets of a project ([route consolidation map](../../designs/fleet-ia-and-journeys.md#route-consolidation-map)). The sections after **Current build** describe the page as it runs today.

**Purpose.** What are our products, who works on each, what they produce and by which rules ([principles](../owner-rules.md#principles) 2: work is reached through who does it).

**Routes** (section, kind and human code, [OR-57](../owner-rules.md), UR-69):

| Route | Opens |
|---|---|
| `/projects` | Idle: cross-project summary |
| `/projects/FLT` | Project FLT, facet Sessions |
| `/projects/FLT/tasks`, `/projects/FLT/tasks/T-FLT-42` | Tasks facet; one task |
| `/projects/FLT/delivery`, `/projects/FLT/delivery/1234` | Delivery facet; one pull request |
| `/projects/FLT/results`, `/projects/FLT/results/<id>` | Results facet; one result |
| `/projects/FLT/rules` | Rules & methods facet |
| `/tasks/<uuid>`, `/results/<uuid>`, `/delivery/<id>`, `/publishing` | Redirect to the facet route of the item's project |
| `/tasks`, `/results`, `/delivery` with no item | Redirect to `/projects` with the facet's cross-project view (see **Across projects**) |

### Layout

Three columns ([OR-24](../owner-rules.md)). Shell header: title, New project (`plus`), focus chip, search.

* **Control column** (`ListPagePanel`): list header with count, search icon and saved views; one row per project: state icon (readiness), name and code (`FLT`), the PM's state dot, and on the right open questions (attention), sessions working, tasks in flight ([OR-22](../owner-rules.md), [OR-29](../owner-rules.md)). Pinned projects first ([OR-12](../owner-rules.md)).
* **Work area, idle:** the cross-project summary: projects whose PM waits for the person, as question cards ([OR-53](../owner-rules.md)); then per project working, queued, done today and blocked as one counter line; then delivery in flight ([UR-52](../principles.md)).
* **Work area, a project open:**
  1. Project head: name, code, readiness, and "Talk to PM" (opens the PM session, [OR-51](../owner-rules.md)).
  2. Facet tab bar ([OR-46](../owner-rules.md), [OR-49](../owner-rules.md)): **Sessions · Tasks · Delivery · Results · Rules & methods**. A tab never rebuilds the control column ([OR-47](../owner-rules.md)).
  3. The facet:
     * **Sessions** (default, [owner choice](#owner-choices-target)): the PM card first, then this project's active session cards as on Home, then the latest finished ones as rows. A card opens the [session view](session.md).
     * **Tasks:** the task list with saved views (Running is every non-terminal state), rows as [Tasks](tasks.md) specifies; an opened task replaces the list inside the facet with the task detail ([OR-39](../owner-rules.md)) and a link to the session that does it; Close returns to the list ([OR-15](../owner-rules.md)).
     * **Delivery:** the stage list and pace of [Delivery](delivery.md), filtered to the project.
     * **Results:** the result list and viewer of [Results](results.md), filtered to the project.
     * **Rules & methods:** purpose, context, repository, roles and environments as differences from the shared defaults, with "Inherited defaults (N)" folded ([OR-45](../owner-rules.md)); then the recommended method per capability with its Lab evidence and a link to [Library](library.md) and [Lab](laboratory.md).

### Across projects

Tasks, results and delivery of all projects in focus are one facet away: on `/projects` with nothing open, the same facet tab bar shows **Summary · Tasks · Delivery · Results** over every project in the focus ([OR-34](../owner-rules.md)). Spotlight finds a task, result or pull request by code and opens it in its project ([OR-32](../owner-rules.md)).

### States

* **Not ready:** the project head shows readiness reasons in the attention tone, with the fix action named; the project still opens.
* **Phone (390 px):** project list; an opened project shows its facets as a scrollable tab bar inside the work area; a task opens full screen.

### Owner choices (target)

1. **Default facet.** *Recommended:* Sessions, since work is reached through who does it. *Other:* Tasks, the list operators use today. Both are drawn on [screens/projects.bl](../board/screens/projects.bl).

### Open (target)

* Results have no human code yet, so a result route cannot avoid an id ([OR-38](../owner-rules.md), UR-69); a result code is needed before the Results facet is built.

### Blocks

* [Navigation rail](../controls/nav-rail.md) and [shell header](../patterns/app-shell.md).
* Catalogue row, list header, saved views, filter search, facet tab bar and detail pane frame (block list, PR C); [tab item](../controls/README.md).
* [Session card](../controls/session-card.md), [question card](../controls/decision-block.md#card), [state dot and word](../controls/state-badge.md#dot-and-word).
* [Live row](../controls/live-row.md) kind delivery for pull requests; diff rows of the [definition editor](../patterns/definition-editor.md) for Rules & methods (stand-in on the board until the diff row master exists).

### Design board

Canvas [screens/projects.bl](../board/screens/projects.bl): idle, project open on Sessions, a task open in Tasks, Rules & methods, both default facets, phone.

## Current build

**App routes:** `/projects` and `/projects/:id` in the operator app (`app/src/views/projects.tsx`). The app checks (`tests/ui/projects.check.ts`, `tests/ui/app.check.ts`) verify them. There is no static mockup.

Projects are the operator's configured work context. The control column lists authorized projects; create, analytics overview, and tabbed detail (`ProjectDetail`) open in the work area. Use only packages/ui controls. Creation, current saved revision and resource readiness are distinct facts.

**Project detail tabs (`TabBar` in `ProjectDetail`).**
When an existing project is opened at `/projects/:id`, `ProjectDetail` organizes the work area with a shared [`TabBar`](../patterns/navigation-and-columns.md) across four tabs (preserving a single vertical scroller per `OR-25`):
* **Work & Rules** (default): Preserves the `ProjectEditor` sections (header save/reload, Context, Repository, Environment, Agent roles, Readiness with `Open PM conversation`, Initial access, Project resources, and Resource analytics), followed by active project tasks/executions (`task_page`, `executions_list`, and quick link to `/tasks?project=...`) and the `OR-45 Diff from default` settings overview.
* **Methods**: Reads scoped `catalogue` evidence for the project (`recommendedMethod`, `recommendationSource`, `sharedDefault`, and `methods` list with lifecycle status, Lab assessment quality/time/cost, and `recommendation_set`), highlighting when the project overrides `sharedDefault`, plus links to `/catalogue` and `/lab`.
* **Delivery**: Reads project-filtered pull requests via `delivery_status` (`projectIds: [projectId]`) with stage `StateCounters`, search, and `DataTable` rows linking to `/delivery/:id` and `/delivery?q=project%3A...`.
* **Results**: Reads project-filtered stored results and artifacts via `artifact_list` (`projectIds: [projectId]`) with artifact `StateCounters`, search, `Latest` row list linking to `/results/:id`, and a quick link to `/results?project=...`.

**Settings from defaults (`OR-45 Diff from default`).**
Within `Work & Rules`, `ProjectSettingsDiff` compares the project's resolved and authored settings against shared project defaults (`worktree`, `scripts`, `setupStart`, `integration`, `cleanup`, `identity`, `subscriptions`, `execution`, `errors`, `presets`, `contextSources`, `artifact_locations`, `artifact_rules`, `roles`, `repos`, `environments`, `capability`, `recommendedMethod`). Customized settings appear prominently with `default → override` values and an `override` badge; unchanged inherited defaults stay collapsed inside a single `<Disclosure summary="Inherited defaults (N)">`.

**Resource analytics (`analytics_query` and `ProjectContextReply.resources`).**
* **Workspace overview (`/projects`) and project detail (`/projects/:id`):** Renders unified resource accounting for the selected project (or across authorized projects in the overview) via `analytics_query` (`window`, `dimensions`, `measures`, `filters`, optional `projectId`).
* **Controls:** Summary metrics use [Totals and CostFigures](../controls/number.md) keeping every `(currency, basis)` separate (never summing metered spend with `'subscription'` list-price equivalents). Time-series trends use interactive CSS-only [AreaChart](../controls/area-chart.md) (`hour` / `day` / `week` grains) with `<button>` bucket selectors placed outside `role="img"` and hover/focus details via `data-tip` (`OR-20`). Selecting a bucket filters the dimension breakdown in place.
* **Breakdowns & completeness:** Dimension selector supports `model`, `harness`, `pipeline_revision`, `project`, `profile_id`, `host`, `outcome`, and `state`. Surface `unknown` and `hidden` counters prominently whenever `> 0`. Provide drill-down links to filtered [Tasks](tasks.md), [Conversations](conversations.md), [Agents](agents.md), [Pipelines](pipelines.md), [Runners](runners.md), and [Hosts](hosts.md).

Edit name, owned context, repository references, existing host environments and role configuration using the existing project field meanings. Save uses shared `project_create` or `project_update` with optimistic revision checking; readback confirms the current version. Conflicts preserve the edit for review. Do not register/rekey a host, add a credential or grant a connection as a side effect.

Opening a PM conversation opens `/conversations/new?in=<project>&role=pm` with the project picked and the first preset on its pm role (Discuss with the system presets); Send starts it through `session_start`. "No project" is one choice there. Context and actual tools follow the directing person's rights; a role label is not an authority grant.

Owner access administration distinguishes initial membership from explicit grants and offers revocation of initial membership. A saved project with an unavailable resource remains configured and shows its readiness reasons. No destructive project lifecycle action is required.

## Independent configuration and confirmed resources

When advertised by the server, `project_detail` loads the editor independently of `project_resources` by default, including a fresh ordinary project URL. Explicit per-tab opt-out retains the legacy path. Summary preparation, failure and refresh never block editing, saving or configuration readback. Summary updates never replace dirty fields. A changed definition while dirty shows an explicit conflict; optimistic revision checking remains authoritative. Save readback verifies revision and content hash through configuration only.

Prepared figures retain their basis, measured zero and unknown coverage. Published version, validation time, processing progress and underlying observation age are separate; publication time is not observation time. Unchanged figures may still have pending or failed processing. Figures retain their last confirmed value during refresh failure with a visible status in reserved space. Breakdowns use continuous virtual windows with count, search and start/end jumps; no per-page scroller. See [data list](../controls/data-list.md).

Capability-off or unavailable old servers retain the established composite path. Client activation and rollback are specified in [prepared project resources](../../designs/fleet-analytics.md#prepared-project-resources). Rendered-value qualification remains separate from delivery.

Authored configuration edits survive a focus/access generation fence under the original identity and editor target. Restore existing-project edits only after a fresh authorized configuration read, retaining their optimistic base revision/content hash and showing conflict when the source changed. New-project input has a separate actor-scoped creation target. Neither reopening nor an uncertain prior save automatically resends content.

Project draft fields use controlled `TextField` values; typing changes only the authored local bundle. The header Save project action remains the single complete configuration write, so intermediate field input also survives authorized restoration.

An unverified save retains its original operation body and request identity separately from newer edits. After revalidation, Retry original save is explicit and sends only that frozen body. Verified readback advances the base; it clears the submitted draft only when no newer input exists.

Creation fields become editable only after the validated actor scope and any authored draft have been restored. A late initialization cannot overwrite input typed into an already usable form; an authored project ID is never used as an authorization lookup.
