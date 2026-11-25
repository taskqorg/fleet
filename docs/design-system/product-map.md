# Product map

What exists in the Fleet web app today, where it goes in the seven-section rail, and whether it works on prod. It joins three sources: the [inventory](inventory.md) (what people must see and do), the app routes (`app/src/main.tsx` and `workspaces` in `packages/ui/src/registry.ts`), and the [route consolidation map](../designs/fleet-ia-and-journeys.md#route-consolidation-map). The rebuild on the block library starts from this map. Rules for each screen are in [principles](principles.md) and [owner rules](owner-rules.md).

## Columns

* **Section:** the target rail section (Home, Live, Conversations, Projects, Lab, Library, System) from the [information architecture](../designs/fleet-ia-and-journeys.md#1-closed-information-architecture).
* **Route and screen:** the current route and what it renders; `<id>` is an opened item.
* **Built:**
  * *built*: the capability works as the inventory asks, on the current screen.
  * *partly*: part of it exists, or it exists without the target behaviour (the owner rule it misses is named).
  * *not built*: nothing in the app does it.
* **Prod:**
  * *checked*: opened on https://fleet.ufobe.com as `site-agents`, rendered its content, with no console error and no failed coordinator request.
  * *broken*: with the one-line cause.
  * *not checked*: with the reason. Actions with side effects (send, answer, stop, save, delete) were not clicked; only their presence was seen.
* **Read or action:** coordinator operation names (`op(...)` in `go/coordinator`), as the screen calls them.

Checked on 2026-11-25 against release `1.261124.39`, with Playwright at 1440 × 900 and 390 × 844. Failed requests aborted by navigation (`/api/stream` and reads in flight) are not counted.

## Totals

| | Routes | Capabilities | All |
|---|---|---|---|
| Rows | 45 | 87 | 132 |
| Built | 34 | 49 | 83 |
| Partly | 9 | 27 | 36 |
| Not built | 2 | 11 | 13 |
| Prod checked | 31 | 49 | 80 |
| Prod broken | 5 | 5 | 10 |
| Prod not checked | 9 | 33 | 42 |

Worst broken items:

1. *Fixed by FLT-43:* `/publishing` and unknown paths now reach the app's redirect and its not-found page (UR-69, UR-70).
2. **Library › Changes.** `/changes` still says "Checking repository and host drift…" after 5 s. System's own service list shows `system_changes` with 8 errors of 19 calls and 5 s responses in the last hour.
3. **Result detail shows internals.** `/results/<id>` uses the result's UUID in the URL, shows a file name made of a hash and a UUID, and shows raw JSON as the content (OR-38, UR-61).
4. **Bot run causes are raw codes.** `/bots/<id>` lists a run problem as `unknown:abandoned message` (UR-4).
5. **Lab and Library reads fail under load.** The screens opened, but System's service list shows `lab_examples_list` with 11 errors of 21 calls (15 s), `catalogue` 5 of 9 (10 s) and `lab_leaderboard` 4 of 15 (7 s) in the last hour.

Also seen: `/results`, `/results/<id>`, `/delivery/<id>`, Humans and Personal Lab highlight no rail item (OR-57, UR-66); task, conversation, bot, result and experiment links carry UUIDs (OR-38); `/agents`, `/pipelines` and `/catalogue` render one page; `site-agents` cannot open Humans.

## Pages and rules

Which rules apply to each [page spec](pages/README.md) and whether the built page meets them today. This is a view for people and agents, never a check that fails. Evidence is the prod run above; a rule not named in a row is not checked.

Every page follows the common set: UR-1 to UR-50, UR-62 to UR-93, and the owner rules that are not *Target* ([owner rules](owner-rules.md)). The third column adds what applies to that page in particular.

| Page | Route today | Applies beyond the common set | Meets | Breaks | Not checked |
|---|---|---|---|---|---|
| [Home](pages/home.md) | `/home`, `/home/<code>` | OR-50, OR-53, OR-54, OR-55, OR-58, UR-51 to UR-61 | — | — | built (FLT-48); not yet measured on the deployed app |
| [Live](pages/live.md) | `/live`, `/live?wall` | OR-56, OR-58, OR-59, UR-51 to UR-55 | — | — | built (FLT-50); not yet measured on the deployed app |
| [Overview](pages/overview.md) | retired: `/overview` redirects to `/home` | OR-29, OR-30, OR-40 | UR-38 (no console error or failed read), UR-50 (no sideways scroll at 390 px), UR-71 (title matches the rail item) | UR-69, OR-38: 33 of 43 links carry a UUID | UR-51 (to be measured on Home) |
| [Conversations](pages/conversations.md) | `/conversations`, `/conversations/<id>` | OR-51, OR-52, OR-53, OR-54, UR-56 to UR-61 | UR-38, UR-50, UR-71 | OR-54 (Target): a metrics header (elapsed, CPU, memory, tokens, cost) opens the conversation; UR-69, OR-38: the URL is a UUID | UR-57, UR-59, UR-81 |
| [Projects](pages/projects.md) | `/projects`, `/projects/<id>` | OR-45, OR-46 (facets) | UR-38, UR-50, UR-69 (no UUID links), OR-45 (inherited defaults folded) | OR-59 (Target): `≈`, `≥` and "partial" fill the main view | UR-63 |
| [Tasks](pages/tasks.md) | `/projects/tasks`, `/projects/tasks/<id>` | OR-22, OR-37, OR-39 | UR-38, UR-71 | UR-69, OR-38: 66 of 76 links and the URL carry a UUID | UR-63, UR-77 |
| [Results](pages/results.md) | `/projects/results`, `/projects/results/<id>` | OR-22, OR-38 | UR-38 | UR-66, OR-57: no rail item highlighted; OR-38, UR-61: the name holds a hash and a UUID, the content is raw JSON | UR-64 |
| [Delivery](pages/delivery.md) | `/projects/delivery`, `/projects/delivery/<id>` | OR-22 (stage chip) | UR-38, UR-69 (no UUID links) | UR-66, OR-57: no rail item highlighted; UR-82: one concept, three names (route Delivery, title Publishing, spec Delivery); UR-69: old `/publishing` links answer 404 | UR-72 (merge actions not clicked) |
| [Agents](pages/agents.md) | `/library/agents`, `/library/agents/<id>` | OR-45, OR-48 | UR-38, UR-69, UR-71 | UR-68: `/library/agents` opens the same page as `/library/pipelines` and `/library/methods`, so the link does not restore the root | the definition editor (no agent opened) |
| [Pipelines](pages/pipelines.md) | `/library/pipelines`, `/library/pipelines/<id>` | OR-45 | UR-38, UR-69, OR-45 (only differences shown, inherited defaults folded) | UR-68: as Agents | UR-64 |
| [Catalogue](pages/catalogue.md) | `/library/methods`, `/library/methods/<id>` | OR-45 | UR-38, UR-69 | UR-68: as Agents | UR-60 |
| [Changes](pages/changes.md) | `/library/changes` | OR-30 | UR-69 | UR-36: still "Checking repository and host drift…" after 5 s, with no error or retry | UR-72 (take and export not clicked) |
| [Laboratory](pages/laboratory.md) | `/lab`, `/lab/new`, `/lab/<id>` | J1, J2 ([journeys](../designs/fleet-ia-and-journeys.md#3-core-user-journeys-j1j5)), OR-45 | UR-38, UR-71, UR-52 (decisions waiting lead the idle page) | UR-69, OR-38: experiment links and URLs carry a UUID | J1 under 5 s, J2 in 3 clicks |
| [Hosts](pages/hosts.md) | `/system`, `/system/hosts/<id>`, `/system/accounts`, `/system/connections` | OR-56, UR-32 | UR-38, UR-50, UR-69, UR-71 | — | UR-33 (no setting changed) |
| [Runners](pages/runners.md) | `/system/runners` | OR-29 | UR-38, UR-52 (today's CI total leads) | — | UR-37 |
| [Spend](pages/spend.md) | `/system/spend` | OR-54 (cost is the question here), OR-59 | UR-38, UR-69, UR-8 (unpriced usage is named) | OR-59 (Target): `≈` marks in the main totals | UR-63 |

## Routes

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Start screen | Home | `/`, redirects to `/home` | built | checked: fixtures | — |
| Home: sessions, their questions, composer, pulse | Home | `/home`, Home | built (FLT-48) | checked: fixtures at 1440 and 390 px (`tests/ui/home.check.ts`) | `sessions_list`, `questions_list`, `executions_list`, `bot_list`, `bot_run`, `project_list`, `session_presets`, `session_start`, `message_send`, `questions_answer`, `person_visit`, `publishing_status` ([home](pages/home.md#data)) |
| Live: hosts, slots, delivery, CI, events | Live | `/live`, Live | built | checked at 1440 and 390 px against production data | `hosts_list`, `executions_runners`, `executions_list`, `task_page`, `delivery_status`, `executions_ci`, `project_list` ([live](pages/live.md#data)) |
| Overview: Needs you, hosts, delivery in flight | Home and Live | `/overview`, redirects to `/home` | retired: Home replaces it (FLT-48) | — | — |
| Session from Home | Home | `/home/<code>`, the session | partly: the conversation view until FLT-49 | checked: fixtures | `entity_resolve`, as Conversations |
| Conversation list with summary | Conversations | `/conversations`, list and summary by project | built | checked: 539 conversations | `sessions_list`, `project_list`, `nav_counts` |
| New conversation from a preset | Conversations | `/conversations/new`, preset picker in the work area | built | checked: presets listed | `session_presets`, `project_list`, `session_start` |
| One conversation | Conversations | `/conversations/<uuid>`, transcript, composer, metrics header | partly: metrics header and tool rows shown (OR-51, OR-52, OR-54), UUID in the URL (OR-38) | checked | `session_get`, `thread_entries`, `thread_blocks`, `thread_composer`, `thread_search`, `thread_export`, `message_send`, `questions_answer`, `session_rename`, `session_archive`, `session_unarchive`, `session_transition`, `session_fork`, `session_handoffs`, `execution_stop`, `account_list`, `profiles_list` |
| Project list and resource analytics | Projects | `/projects`, list and cross-project resources | built | checked | `project_list`, `analytics_query`, `list_spend`, `project_resources`, `project_resource_items` |
| One project | Projects | `/projects/<id>`, readiness, active work, settings from defaults, resources | built | checked: `cs` opened | `project_detail`, `project_context`, `project_readiness`, `project_update`, `project_create`, `project_initial_access_revoke`, `task_page`, `executions_list`, `publishing_status`, `artifact_list`, `catalogue`, `recommendation_set`, `execution_status` |
| Task list | Projects (facet) | `/projects/tasks`, list and summary by project | built: a facet of Projects (`/projects/<code>/tasks` for one project) | checked: 93 tasks | `task_page`, `list_membership` |
| One task | Projects (facet) | `/projects/tasks/<uuid>`, statement, answers, result | partly: UUID in the URL (OR-38) | checked | `task_get`, `task_update`, `questions_answer`, `project_resources` |
| Result list | Projects (facet) | `/projects/results`, last 24 h of results | built | checked: 17,590 results; no rail item highlighted (OR-57) | `artifact_list`, `project_list` |
| One result | Projects (facet) | `/projects/results/<uuid>`, file viewer | partly | broken: UUID in the URL and the name, raw JSON shown (OR-38) | `artifact_get` |
| Delivery list | Projects (facet) | `/projects/delivery`, pull requests by stage; title "Publishing" | built | checked: 33 pull requests | `publishing_status`, `delivery_history`, `delivery_baseline`, `delivery_regressions` |
| One pull request | Projects (facet) | `/projects/delivery/<owner~repo~n>`, stage, head, merge actions | built | checked: `#834`; no rail item highlighted (OR-57) | `publishing_status`, `publishing_mark_ready`, `publishing_request_merge`, `publishing_cancel_merge_request`, `delivery_history` |
| Old delivery route | Projects | `/publishing`, redirects to `/projects/delivery` | built | not checked: the server now serves the app shell for it | — |
| Bot list | Home and Library › Bots | `/library/bots`, bots with state | built | checked: HAL0 listed | `bot_list`, `project_list` |
| One bot | Home and Library › Bots | `/library/bots/<uuid>`, status, runs, automations, questions, analytics, settings | partly: UUID in the URL (OR-38) | broken: run problems show the raw cause code `unknown:abandoned message` | `bot_get`, `bot_runs`, `bot_tasks`, `bot_run`, `bot_pause`, `bot_stop`, `bot_settings_save`, `bot_memory_save`, `bot_automations`, `bot_automation_save`, `questions_list`, `questions_answer`, `executions_list`, `analytics_query`, `session_presets` |
| New bot | Library › Bots | `/library/bots/new`, create form | built | checked: form shown, not submitted | `bot_settings_save`, `project_list`, `session_presets` |
| Lab: use cases, runs, decisions waiting | Lab | `/lab`, catalogue and leaderboard | built | checked: 75 use cases, 49 experiments wait for a decision | `lab_registry`, `lab_experiments_list`, `lab_leaderboard`, `lab_examples_list`, `lab_ratings_list` |
| New experiment | Lab | `/lab/new`, draft form | built | checked: form shown, not submitted | `lab_experiment_create`, `lab_registry`, `lab_examples_list`, `definition_list`, `project_list`, `project_detail`, `project_context` |
| One experiment | Lab | `/lab/<uuid>`, record, matrix, trials | partly: UUID in the URL (OR-38) | checked: a draft opened | `lab_experiment_get`, `lab_experiment_action`, `lab_experiment_decide`, `lab_experiment_note`, `lab_cell_action`, `lab_rating_create`, `lab_promote`, `pipeline_get`, `pipeline_run`, `questions_answer` |
| One use case or suite | Lab | `/lab/use-case:<id>`, `/lab/suite:<key>` | built | not checked: no link followed | `lab_registry`, `lab_leaderboard`, `lab_examples_list` |
| Personal Lab | Lab | `/lab/mine`, drafts and submissions | built | checked: empty for `site-agents`; no rail item highlighted | `lab_personal_list`, `lab_personal_save`, `lab_personal_promote`, `lab_personal_withdraw`, `me` |
| Library summary | Library | `/library`, idle page with drift, methods, pipelines, agents, skills; `/library/<root>` opens one root | built | checked before the Library section was built: the three earlier paths rendered the same page | `definition_list`, `skills_list`, `system_changes`, `definition_take_repository`, `definition_add_skill`, `catalogue` |
| One agent | Library | `/library/agents/<id>`, definition editor | built | not checked: no agent row link found; the route answers 200 | `definition_get`, `definition_draft_get`, `definition_draft_put`, `definition_draft_discard`, `definition_save`, `definition_diff`, `definition_history`, `definition_usage`, `agent_effective`, `agent_compatibility` |
| One pipeline | Library | `/library/pipelines/<id>`, identity, differences, steps | built | checked: `worker-agent` | as one agent, plus `pipeline_run_create` |
| One catalogue entry | Library | `/library/methods/<id>`, methods and Lab comparison | built | checked: `catalogue` | as one agent, plus `lab_compare_methods`, `lab_registry`, `recommendation_set` |
| Changes and drift | Library | `/library/changes`, repository and host drift | built | broken: "Checking repository and host drift…" after 5 s; `system_changes` errors and is slow | `system_changes`, `definition_take_repository`, `definition_export` |
| System summary and service health | System | `/system`, findings and services in the last hour | built | checked | `hosts_list`, `telemetry_summary`, `telemetry_findings`, `connections_list`, `me` |
| One host | System | `/system/hosts/<id>`, state, daemon health, executions, CLIs | built | checked: `pds18-native` | `hosts_list`, `telemetry_summary`, `hosts_remove`, `system_purge` |
| Accounts and limits | System | `/system/accounts`, subscription windows and burn | built | checked | `subscription_limits`, `subscription_limits_refresh` |
| CI runners | System and Live | `/system/runners`, CI jobs and self-hosted runners | built | checked: 65 jobs today, 4 self-hosted runners | `executions_runners`, `executions_ci` |
| Connections and keys | System | `/system/connections`, connections with keys and limits | built | checked | `connections_list` |
| One connection | System | `/system/connections/<id>`, keys, access, intake, balancing, limits | built | checked: `tavily` | `connections_key_add`, `connections_key_state`, `connections_set_access`, `connections_set_intake`, `connections_set_key_policy`, `connections_set_limits` |
| Spend | System | `/system/spend`, totals and breakdown by purpose | built | checked | `spend_summary` |
| People and access | System › People | `/system/people`, `/system/people/<id>`, `/system/people/new` | built | not checked: `site-agents` sees "Only the Fleet owner can manage Humans." | `people_list`, `people_update`, `people_access_sync`, `project_list`, `me` |
| Unknown route | — | any other path | built: the server serves the app shell, which shows "This page does not exist" (UR-70) | not checked | — |
| Phone width | all | `/home`, `/conversations`, a conversation, `/projects`, `/system` at 390 × 844 | built: the bottom bar of five (OR-60) | checked: no page scroll sideways | — |
| Palette | all | ⌘K over any route | built | not checked in this run | `search` |
| Entity card | all | any `EntityCode` | built | not checked in this run | `entity_card` |
| Navigation counters | all | the rail | built | checked: Conversations and Tasks counters shown | `nav_counts` |
| Saved views, focus, widths | all | the shell | built | not checked in this run | `person_settings_get`, `person_settings_set` |
| Live updates | all | the shell | built | checked: the stream opens on every route | `changes_since` |

## Retired routes

The earlier screens have no route of their own: each is a facet, a root or a saved view of its section, and its earlier address redirects there, keeping the item and the query (`app/src/routes.ts`, UR-69). Inside the app the views still speak the earlier paths, which the coordinator's view links share (`contracts/vocabulary.json` views).

| Earlier route | Redirects to | Screen now |
|---|---|---|
| `/`, `/overview`, `/overview/<id>` | `/home`, `/home/<id>` | Home |
| `/tasks`, `/tasks/<id>` | `/projects/tasks`, `/projects/<code>/tasks/<id>` | Projects › Tasks facet |
| `/results`, `/results/<id>` | `/projects/results`, `/projects/<code>/results/<id>` | Projects › Results facet |
| `/delivery`, `/delivery/<id>`, `/publishing` | `/projects/delivery`, `/projects/<code>/delivery/<id>` | Projects › Delivery facet |
| `/personal-lab`, `/personal-lab/<id>` | `/lab/mine`, `/lab/mine/<id>` | Lab › Mine |
| `/catalogue`, `/catalogue/<id>` | `/library/methods`, `/library/methods/<id>` | Library › Methods |
| `/agents`, `/agents/<id>`, `/agents/skill:<id>` | `/library/agents`, `/library/agents/<id>`, `/library/skills/<id>` | Library › Agents, Skills |
| `/pipelines`, `/pipelines/<id>` | `/library/pipelines`, `/library/pipelines/<id>` | Library › Pipelines |
| `/bots`, `/bots/<id>`, `/bots/new` | `/library/bots`, `/library/bots/<id>`, `/library/bots/new` | Library › Bots |
| `/changes` | `/library/changes` | Library › Changes |
| `/hosts`, `/hosts/<id>` | `/system`, `/system/hosts/<id>` | System, System › Hosts |
| `/hosts/limits`, `/hosts/limits/<id>` | `/system/accounts`, `/system/accounts/<id>` | System › Accounts & limits |
| `/keys`, `/keys/<id>` | `/system/connections`, `/system/connections/<id>` | System › Connections & keys |
| `/runners` | `/system/runners` | System › Runners |
| `/spend` | `/system/spend` | System › Spend |
| `/humans`, `/humans/<id>`, `/humans/new` | `/system/people`, `/system/people/<id>`, `/system/people/new` | System › People |

## Capabilities

### Cross-cutting

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Long lists that scroll without jumps | all | every list | built | checked: lists render on every route | `task_page`, `sessions_list`, `artifact_list` |
| Filters, grouping, saved views in the URL and per person | all | list headers | built | not checked in this run | `person_settings_get`, `person_settings_set` |
| Live state with observation state | all | rows and details | built | checked: hosts "now", runs live | `changes_since` |
| One state vocabulary | all | every state badge | partly: Done, Completed and Succeeded still all appear (OR-55) | checked | — |
| Measurement basis per number | all | totals and resources | built; *Target* OR-59 moves marks to Details | checked: `≈` and `≥` shown in lists and headers | `analytics_query`, `spend_summary` |
| Drill-down from a total or problem to its cause | all | totals, Needs you | partly: often two or more clicks (UR-63) | not checked: not walked | `executions_list`, `analytics_query` |
| Decisions inline, in the work and in one place | Home | Home cards, task, conversation | built | checked: fixtures, answered in place | `questions_list`, `questions_answer` |
| Result viewers (text, diff, image, video) | Projects | `/projects/results/<id>` | partly: raw JSON and text only seen | broken: see one result | `artifact_get` |
| Light and dark, desktop and phone | all | the shell | partly: phone rail not built (OR-60) | checked at 390 px in dark | — |

### Home and Overview

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Tasks across projects by state, priority and progress | Home (pulse), Projects | `/projects/tasks` | built | checked | `task_page` |
| Everything waiting for a person | Home | `/home` Waiting for you | built | checked: fixtures | `questions_list`, `sessions_list` |
| Change priority; pause or continue work | Projects | `/projects/tasks/<id>` | built | not checked: action | `task_update` |
| Problems aggregated by cause with count and newest time | Home, Live | `/home` blocker chips | partly: Home chips with `×N`; Live not built | checked: fixtures | `executions_list`, `questions_list` |
| Sessions with what each does now | Home | `/home` session cards | built | checked: fixtures | `sessions_list` `now` ([home](pages/home.md#data)) |
| Composer that picks who to talk to | Home | `/home` composer | built | checked: fixtures, picker | `session_presets`, `session_start`, `message_send`, `bot_run` |
| Quiet Home: what happened while away | Home | `/home` quiet summary | built | checked: fixtures | `person_visit`, `sessions_list`, `publishing_status` |

### Live

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Load and capacity by host and account, placement reasons | Live, System | `/system`, `/system/hosts/<id>`, `/home` | partly: no placement reasons | checked | `hosts_list`, `telemetry_summary` |
| Account and quota state per subscription | System | `/system/accounts` | built | checked | `subscription_limits` |
| Worker slots per host with 24 h of runs | Live | `/live` host bands | built | checked | `hosts_list`, `executions_runners` |
| Wall mode for a TV | Live | `/live?wall`; a viewer sees only Live | built | checked at 1920 and 390 px; the viewer path not checked: no viewer credential | `hosts_list` and the reads above |

### Projects

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Purpose, context materials, rules | Projects | `/projects/<id>` | built | checked | `project_detail`, `project_context` |
| Connections bound to repositories, boards, CI and services | Projects | `/projects/<id>` settings | built | checked: repositories and environments shown | `project_detail`, `project_update` |
| Selected agents, environments, tools and recommendations | Projects | `/projects/<id>` methods | built | checked | `catalogue`, `recommendation_set` |
| Artifact placement for lab and production | Projects | `/projects/<id>` settings | partly: in inherited defaults only | not checked: folded | `project_update` |
| Retention rules with preview and confirm | Projects | none | not built | not checked: not built | — |
| Acceptance rules | Projects | `/projects/<id>` settings | partly | not checked: folded | `project_update` |
| Repository publishing status and actions | Projects | `/projects/delivery`, `/projects/delivery/<id>` | built | checked: actions not clicked | `publishing_status`, `publishing_mark_ready`, `publishing_request_merge`, `publishing_cancel_merge_request` |
| Readiness checks | Projects | `/projects/<id>` | built | checked: button shown, not run | `project_readiness` |
| Project facets: Sessions · Tasks · Delivery · Results · Rules | Projects | `/projects/<id>`, `/projects/<id>/<facet>` | built: one view for the catalogue and its facets (FLT-52) | checked | — |

### Bots

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Automations by bot with trigger, status, next time, last outcome | Library › Bots | `/library/bots/<id>` Automations | built | checked: tab shown | `bot_automations` |
| Bot name, instructions, schedule under its grants | Library › Bots | `/library/bots/<id>` Settings | built | not checked: action | `bot_automation_save`, `bot_settings_save` |
| Create, edit, enable, pause, run now | Home, Library › Bots | `/library/bots/<id>`, `/library/bots/new` | built | not checked: actions | `bot_run`, `bot_pause`, `bot_stop`, `bot_automation_save` |
| Run history with causes and links | Library › Bots | `/library/bots/<id>` Runs | partly | broken: a cause shows as the raw code `unknown:abandoned message` | `bot_runs` |
| Active and asleep bots; wake one | Home | `/home` Asleep row, `/library/bots` | built | checked: fixtures | `bot_list`, `bot_run`, `message_send` |

### Conversations

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Messages, tool activity, results and questions with options | Conversations | `/conversations/<id>` | partly: tools not folded to one line (OR-52), metrics header (OR-54) | checked | `thread_entries`, `thread_blocks`, `questions_answer` |
| Attach materials; assign work; create tasks for several projects | Conversations | composer | partly | not checked: action | `artifact_upload_begin`, `message_send` |
| Continue with another agent or account: handoff and fork | Conversations | conversation header | built | not checked: action | `session_transition`, `session_fork`, `session_handoffs` |
| Steer or queue a message to a running agent | Conversations | composer | built | not checked: action | `message_send` |
| Start a conversation with a PM or a bot in one move | Conversations, Home | `/conversations/new`, project "Open PM conversation" | built | checked: presets and button shown | `session_presets`, `session_start`, `message_send` |
| Session context panel: plan, timers, automations | Conversations | none | not built ([FLT-30](https://youtrack.ufobe.com/issue/FLT-30)) | not checked: not built | — |

### Tasks and results

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Expected outcome, inputs, requirements, dependencies | Projects | `/projects/tasks/<id>` | built | checked | `task_get` |
| State with reason and release condition | Projects | `/projects/tasks/<id>` | built | checked | `task_get` |
| Executions and steps as a tree with time and cost | Projects | `/projects/tasks/<id>` | partly | checked | `task_get`, `executions_list` |
| Live transcript with paging and search | Conversations | `/conversations/<id>` | built | checked | `thread_entries`, `thread_search` |
| Results and their checks | Projects | `/projects/tasks/<id>` | built | checked: report shown | `artifact_list`, `artifact_get` |
| Accept, rework, pause, continue, cancel, retry, change account, hand off | Projects, Conversations | `/projects/tasks/<id>`, conversation | built | not checked: actions | `task_update`, `questions_answer`, `execution_stop`, `session_transition` |
| Share a task or result by email with expiry | Projects | none | not built | not checked: not built | — |
| Find results by project, task, type and time | Projects | `/projects/results` | built | checked | `artifact_list` |
| Open in the right viewer, compare versions, annotate | Projects | `/projects/results/<id>` | partly: no compare or annotate | broken: see one result | `artifact_get` |
| Copy state per location | Projects | `/projects/results` | partly: stored and failed only | checked | `artifact_list` |

### Library

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Agent configurations | Library | `/library/agents/<id>` | built | not checked: no agent opened | `definition_get`, `agent_effective` |
| Diff between revisions; where a configuration is used | Library | `/library/agents/<id>`, `/library/pipelines/<id>` Diff, Usage | built | checked: pipeline tabs shown | `definition_diff`, `definition_history`, `definition_usage` |
| Harness facts and limitations per version | Library, System | `/system/hosts/<id>` capabilities | partly | checked | `agent_compatibility`, `hosts_list` |
| Purpose, category, tags and mark of each agent | Library | `/library/agents` | built | checked: by harness and category | `definition_list` |
| Active or unused; what each agent is chosen for | Library | `/library/agents` | built | checked: 30-day runs shown | `definition_list`, `definition_usage` |
| Lab variants folded under their agent | Library | `/library/agents` | partly | not checked: not opened | `definition_list` |
| Agents waiting for a repository version; take many at once | Library | `/library/agents` drift banner | built | checked: "Take repo (2)" shown, not clicked | `definition_take_repository` |
| Definitions made in Fleet; save them to the repository | Library | `/library/changes` | built | broken: `/library/changes` does not finish loading | `definition_export` |
| Skills with files and carrying agents | Library | `/library/agents` Skills | built | checked: 7 skills | `skills_list` |
| How far a skill change has reached | Library | `/library/agents` | partly | not checked: not opened | `definition_list` |
| Add a skill to agents | Library | `/library/agents` | built | not checked: action | `definition_add_skill` |
| Catalogue entries with purpose, inputs, results, recommendations | Library | `/library/methods/<id>` | built | checked | `catalogue`, `recommendation_set` |
| Pipelines with steps, where each lives, 30-day runs and price | Library | `/library/pipelines/<id>` | built | checked | `definition_get`, `definition_list` |
| Bot definitions as a Library root | Library › Bots | `/library/bots` | built: a Library root | checked | `bot_list` |

### Lab

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Create an experiment draft with variants, evaluators, task set | Lab | `/lab/new` | built | checked: form shown, not submitted | `lab_experiment_create` |
| Trials per variant with scores, tokens, time and cost | Lab | `/lab/<id>` matrix | built | checked: draft with 0 trials | `lab_experiment_get` |
| Comparison of variants with drill-down | Lab | `/lab` leaderboard | built | checked | `lab_leaderboard`, `analytics_query` |
| Assessments and recommendations per kind of work | Lab | `/lab/<id>`, ratings | built | not checked: action | `lab_rating_create`, `lab_ratings_list`, `lab_promote` |
| Experiments as questions by series, with decision | Lab | `/lab` | built | checked: 49 wait for a decision in 19 series | `lab_experiments_list`, `lab_experiment_decide` |
| Confounders between compared variants | Lab | `/lab/<id>` | partly | not checked: no finished experiment opened | `lab_experiment_get` |
| Use case card with examples, history and Run in Lab | Lab | `/lab/use-case:<id>` | built | not checked: not opened | `lab_registry`, `lab_examples_list` |

### System

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Hosts: platform, last contact, observation, versions, readiness | System | `/system`, `/system/hosts/<id>` | built | checked | `hosts_list` |
| Environments and account users per host | System | `/system/hosts/<id>` | built | checked | `hosts_list` |
| Stores: capacity, used, free, tunnel, fill per project | System | `/system/hosts/<id>` | partly: free disk only seen | checked | `hosts_list`, `telemetry_summary` |
| Store capacity, service bounds, free-space warning settings | System | none | not built: no coordinator operation sets a host setting | not checked: not built | `setting_changes` (read) |
| Low space: raise capacity, move locations, add retention | System | none | not built | not checked: not built | — |
| Where each change has reached | Library, System | `/library/changes` | built | broken: `/library/changes` does not finish loading | `system_changes` |
| Connections with accounts, limits, projects, cost per call | System | `/system/connections`, `/system/connections/<id>` | built | checked | `connections_list`, `connections_set_limits` |
| People, roles and project grants | System › People | `/system/people` | built | not checked: owner only | `people_list` |
| Shares: email, object, expiry; revoke | System | none | not built | not checked: not built | — |
| Humans roster, admission, Cloudflare rule, revocation | System › People | `/system/people/<id>`, `/system/people/new` | built | not checked: owner only | `people_update`, `people_access_sync` |
| Access status between Cloudflare and Fleet | System › People | `/system/people` | partly | not checked: owner only | `people_access_sync` |
| Time, cost, tokens, CPU, waits by any dimension | System, Projects | `/projects` resources | built | checked | `analytics_query` |
| Spend by purpose and what it bought | System | `/system/spend` | built | checked | `spend_summary` |
| Drill-down from a total to runs and transcripts | System, Projects | `/projects`, `/system/spend` | partly | not checked: not walked | `analytics_query`, `thread_entries` |
| Discuss findings with an agent; start an experiment from selected work | Conversations, Lab | none | not built | not checked: not built | — |
| Service health: errors and latency per operation | System | `/system` summary | built | checked: shows `system_changes`, `lab_examples_list`, `catalogue` failing | `telemetry_findings`, `telemetry_summary` |
