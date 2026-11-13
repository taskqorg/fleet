# Projects

**App routes:** `/projects` and `/projects/:id` in the operator app (`app/src/views/projects.tsx`). The app checks (`tests/ui/projects.check.ts`, `tests/ui/app.check.ts`) verify them. There is no static mockup.

Projects are the operator's configured work context. The control column lists authorized projects; create, analytics overview, and detail open in the work area. Use only packages/ui controls. Creation, current saved revision and resource readiness are distinct facts.

**Resource analytics (`analytics_query` and `ProjectContextReply.resources`).**
* **Workspace overview (`/projects`) and project detail (`/projects/:id`):** Renders unified resource accounting for the selected project (or across authorized projects in the overview) via `analytics_query` (`window`, `dimensions`, `measures`, `filters`, optional `projectId`).
* **Controls:** Summary metrics use [Totals and CostFigures](../controls/number.md) keeping every `(currency, basis)` separate (never summing metered spend with `'subscription'` list-price equivalents). Time-series trends use interactive CSS-only [AreaChart](../controls/area-chart.md) (`hour` / `day` / `week` grains) with `<button>` bucket selectors placed outside `role="img"` and hover/focus details via `data-tip` (`OR-20`). Selecting a bucket filters the dimension breakdown in place.
* **Breakdowns & completeness:** Dimension selector supports `model`, `harness`, `pipeline_revision`, `project`, `profile_id`, `host`, `outcome`, and `state`. Surface `unknown` and `hidden` counters prominently whenever `> 0`. Provide drill-down links to filtered [Tasks](tasks.md), [Conversations](conversations.md), [Agents](agents.md), [Pipelines](pipelines.md), [Runners](runners.md), and [Hosts](hosts.md).

Edit name, owned context, repository references, existing host environments and role configuration using the existing project field meanings. Save uses shared `project_create` or `project_update` with optimistic revision checking; readback confirms the current version. Conflicts preserve the edit for review. Do not register/rekey a host, add a credential or grant a connection as a side effect.

Opening a PM conversation supplies the chosen project and mapped PM configuration through shared session operations. A projectless conversation remains available. Context and actual tools follow the directing person's rights; a role label is not an authority grant.

Owner access administration distinguishes initial membership from explicit grants and offers revocation of initial membership. A saved project with an unavailable resource remains configured and shows its readiness reasons. No destructive project lifecycle action is required.
