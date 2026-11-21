# Projects

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
