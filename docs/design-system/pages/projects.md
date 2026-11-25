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
| `/projects/FLT/access`, `/projects/FLT/access?grant=<id>` | Access facet; one grant open |
| `/tasks/<uuid>`, `/results/<uuid>`, `/delivery/<id>`, `/publishing` | Redirect to the facet route of the item's project |
| `/tasks`, `/results`, `/delivery` with no item | Redirect to `/projects` with the facet's cross-project view (see **Across projects**) |

### Layout

Three columns ([OR-24](../owner-rules.md)). Shell header: title, New project (`plus`), focus chip, search.

* **Control column** (`ListPagePanel`): list header with count, search icon and saved views; one row per project: state icon (readiness), name and code (`FLT`), the PM's state dot, and on the right open questions (attention), sessions working, tasks in flight ([OR-22](../owner-rules.md), [OR-29](../owner-rules.md)). Pinned projects first ([OR-12](../owner-rules.md)).
* **Work area, idle:** the cross-project summary: projects whose PM waits for the person, as question cards ([OR-53](../owner-rules.md)); then per project working, queued, done today and blocked as one counter line; then delivery in flight ([UR-52](../principles.md)).
* **Work area, a project open:**
  1. Project head: name, code, readiness, and "Talk to PM" (opens the PM session, [OR-51](../owner-rules.md)).
  2. Facet tab bar ([OR-46](../owner-rules.md), [OR-49](../owner-rules.md)): **Sessions · Tasks · Delivery · Results · Rules & methods · Access**. A tab never rebuilds the control column ([OR-47](../owner-rules.md)).
  3. The facet:
     * **Sessions** (default, [owner decision](#owner-decision-target)): the PM card first, then this project's active session cards as on Home, then the latest finished ones as rows. A card opens the [session view](session.md).
     * **Tasks:** the task list with saved views (Running is every non-terminal state), rows as [Tasks](tasks.md) specifies; an opened task replaces the list inside the facet with the task detail ([OR-39](../owner-rules.md)) and a link to the session that does it; Close returns to the list ([OR-15](../owner-rules.md)).
     * **Delivery:** the stage list and pace of [Delivery](delivery.md), filtered to the project.
     * **Results:** the result list and viewer of [Results](results.md), filtered to the project.
     * **Rules & methods:** purpose, context, repository, roles and environments as differences from the shared defaults, with "Inherited defaults (N)" folded ([OR-45](../owner-rules.md)); then the recommended method per capability with its Lab evidence and a link to [Library](library.md) and [Lab](laboratory.md).
     * **Access** (approved 2026-11-26, board frame "Project › Access: grants and approval card"): the project's data class when its settings name one (`data`), then its [resource grants](../../designs/fleet-project-access.md): resource (kind, the database or tables count or paths count, and server), level, state, the last probe (read works, write refused, or failed, with its age) and the gateway connection that serves it. A row opens its detail under it: scope, reason, the recipe lines, the probe steps, the error, the credential's fingerprint and the approval question, with **Check** (`access_grant_check`; **Provision again** on a failed grant, which the server provisions anew) and, for the owner, **Revoke** (`access_grant_revoke`; Withdraw for an asked grant, Retry revoke after a failed revoke), which confirms inline with its scope and the way back. The approval itself is the Needs-you question card.

### Across projects

Tasks, results and delivery of all projects in focus are one facet away: on `/projects` with nothing open, the same facet tab bar shows **Summary · Tasks · Delivery · Results** over every project in the focus ([OR-34](../owner-rules.md)). Spotlight finds a task, result or pull request by code and opens it in its project ([OR-32](../owner-rules.md)).

### Data

One read for the catalogue and the Summary: `projects_view` (a view operation, [data layer](../../designs/fleet-data-layer.md)) with the focus. The server counts each project's open questions, sessions working (preparing or running) and tasks in flight (the Tasks Running view's states); the catalogue gets every project's counts, the Summary the focus's, its first three open questions with their total, and the pull requests in flight. The project list is the shell's `project_list`; the facets keep their list reads (Tasks: `task_page` and `list_spend`). The change stream rereads `projects_view` on session, execution, task, question and delivery changes, at most once in 2 s.

### States

* **Not ready:** the project head shows readiness reasons in the attention tone, with the fix action named; the project still opens.
* **Phone (390 px):** project list; an opened project shows its facets as a scrollable tab bar inside the work area; a task opens full screen.

### Owner decision (target)

A project opens on Sessions (owner, 2026-11-25).

### Review of the current build

Checked against the [product map](../product-map.md) rows for Projects, Tasks, Results and Delivery.
* **Keep:** New project and the project editor (context, repository, environments, roles, initial access, readiness with Talk to PM) with one Save and the revision check, now in Rules & methods; settings from defaults ([OR-45](../owner-rules.md)); methods with Lab evidence; the Tasks, Delivery and Results lists and details with their filter bars, saved views, grouping, sorting, pins, selection and row menus, as [Tasks](tasks.md), [Delivery](delivery.md) and [Results](results.md) specify.
* **Change:** the four tabs Work & Rules, Methods, Delivery, Results become the five facets; Tasks, Results and Delivery lose their rail items.
* **Drop:** the resource analytics block on `/projects` and on a project (area chart, dimension picker, breakdown): cost is the question only on System › Spend ([OR-54](../owner-rules.md)), which filters by project; the project head keeps one Resources line in Details. The `override` badge and `default → value` pairs (replaced by the diff row, [OR-45](../owner-rules.md), [OR-7](../owner-rules.md)). The links out to `/catalogue`, `/lab` and `/tasks?project=` (the facets and the method links replace them).

### Open (target)

* A pull request route keeps its `owner~repo~number` id: pull requests have no D17 code.

### Blocks

* [Navigation rail](../controls/nav-rail.md) and [shell header](../patterns/app-shell.md).
* [Filter bar](../controls/filter-bar.md#board), [catalogue row and list group](../controls/data-list.md#board), [detail head](../patterns/list-detail.md#board), [tab bar](../controls/tab-bar.md) for the facets (words only, no icons).
* [Session card](../controls/session-card.md), [question card](../controls/decision-block.md#card), [state dot and word](../controls/state-badge.md#dot-and-word).
* [Live row](../controls/live-row.md) kind delivery for pull requests; [diff row](../patterns/definition-editor.md#diff-row) for Rules & methods.

### Design board

Canvas [screens/projects.bl](../board/screens/projects.bl): idle, project open on Sessions, a task open in Tasks, Rules & methods, phone.

## Current build

**App routes:** the Target routes above, served by one view (`app/src/views/projects.tsx`) for the four internal screens `projects`, `tasks`, `results` and `delivery` (`app/src/routes.ts` maps `/projects/FLT/tasks/T-FLT-42` to the Tasks view filtered to the project, `/tasks/T-FLT-42?project=<id>`). Check: a live check on the deployed site.

* **Control column:** one row per project, every project including those with no work: readiness, name and code, then open questions, sessions working and tasks in flight, or "nothing in flight". Search by name, id or code. Ignored projects are left out (OR-34). New project is the shell action.
* **Idle:** the tab bar Summary · Tasks · Delivery · Results over the projects in focus. Summary: open questions as question cards, each project's sessions working and tasks in flight, delivery in flight.
* **A project open:** detail head (`project · FLT`, name, waiting state, questions, sessions working, readiness reason; Talk to PM opens `/conversations/new?in=<project>&role=pm`), then the facets:
  * **Sessions:** the newest session on the project's pm profile as the PM card (or Talk to PM when none), active sessions as session cards with their open question, finished ones as rows. A card opens `/conversations/<code>`, the session view.
  * **Tasks, Results, Delivery:** the list views of [Tasks](tasks.md), [Results](results.md) and [Delivery](delivery.md), scoped to the project; an opened item replaces the list. A task opens by its code, a result by its code; an earlier link by id, or one outside its project, is replaced by the facet route of the item's project.
  * **Rules & methods:** the project editor (Save, Reload, readiness, context, repositories, environments, roles, initial access, prepared resources) with the settings that differ from the shared defaults (`DiffRow`, `Inherited defaults (N)` folded) and the methods with their Lab evidence.
  * **Access:** `access_grants {projectId}` as the Target describes (`app/src/views/access-grants.tsx`); the read repeats every 3 s only while a grant provisions or revokes or a Check waits for its probe, since `access_grants` has no change kind.

## Independent configuration and confirmed resources

When advertised by the server, `project_detail` loads the editor independently of `project_resources` by default, including a fresh ordinary project URL. Explicit per-tab opt-out retains the legacy path. Summary preparation, failure and refresh never block editing, saving or configuration readback. Summary updates never replace dirty fields. A changed definition while dirty shows an explicit conflict; optimistic revision checking remains authoritative. Save readback verifies revision and content hash through configuration only.

Prepared figures retain their basis, measured zero and unknown coverage. Published version, validation time, processing progress and underlying observation age are separate; publication time is not observation time. Unchanged figures may still have pending or failed processing. Figures retain their last confirmed value during refresh failure with a visible status in reserved space. Breakdowns use continuous virtual windows with count, search and start/end jumps; no per-page scroller. See [data list](../controls/data-list.md).

Capability-off or unavailable old servers retain the established composite path. Client activation and rollback are specified in [prepared project resources](../../designs/fleet-analytics.md#prepared-project-resources). Rendered-value qualification remains separate from delivery.

Authored configuration edits survive a focus/access generation fence under the original identity and editor target. Restore existing-project edits only after a fresh authorized configuration read, retaining their optimistic base revision/content hash and showing conflict when the source changed. New-project input has a separate actor-scoped creation target. Neither reopening nor an uncertain prior save automatically resends content.

Project draft fields use controlled `TextField` values; typing changes only the authored local bundle. The header Save project action remains the single complete configuration write, so intermediate field input also survives authorized restoration.

An unverified save retains its original operation body and request identity separately from newer edits. After revalidation, Retry original save is explicit and sends only that frozen body. Verified readback advances the base; it clears the submitted draft only when no newer input exists.

Creation fields become editable only after the validated actor scope and any authored draft have been restored. A late initialization cannot overwrite input typed into an already usable form; an authored project ID is never used as an authorization lookup.
