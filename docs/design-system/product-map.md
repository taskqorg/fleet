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

1. **Unknown and retired routes.** `/publishing` and any unknown path get the server's JSON `404 Route GET:… not found`, not the app. The client-side redirect from `/publishing` to `/delivery` and "This page does not exist" never run, so old deep links die (UX-69, UX-70).
2. **Library › Changes.** `/changes` still says "Checking repository and host drift…" after 5 s. System's own service list shows `system_changes` with 8 errors of 19 calls and 5 s responses in the last hour.
3. **Result detail shows internals.** `/results/<id>` uses the result's UUID in the URL, shows a file name made of a hash and a UUID, and shows raw JSON as the content (OR-38, UX-61).
4. **Bot run causes are raw codes.** `/bots/<id>` lists a run problem as `unknown:abandoned message` (UX-4).
5. **Lab and Library reads fail under load.** The screens opened, but System's service list shows `lab_examples_list` with 11 errors of 21 calls (15 s), `catalogue` 5 of 9 (10 s) and `lab_leaderboard` 4 of 15 (7 s) in the last hour.

Also seen: `/results`, `/results/<id>`, `/delivery/<id>`, Humans and Personal Lab highlight no rail item (OR-57, UX-66); task, conversation, bot, result and experiment links carry UUIDs (OR-38); `/agents`, `/pipelines` and `/catalogue` render one page; `site-agents` cannot open Humans.

## Routes

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Start screen | Home | `/`, redirects to `/overview` | partly: Home not built (OR-50) | checked | — |
| Home: sessions, their questions, composer, pulse | Home | `/home`, none | not built | not checked: not built; the server answers 404 | target: `sessions_list`, `questions_list`, `bot_list`, `session_presets`, `session_start`, `message_send`, `nav_counts` ([home](pages/home.md#data)) |
| Live: hosts, slots, delivery, CI, events | Live | `/live`, none | not built | not checked: not built; the server answers 404 | target: `hosts_list`, `telemetry_summary`, `executions_runners`, `executions_list`, `spend_summary`, `delivery_status`, `executions_ci` ([live](pages/live.md#data)) |
| Overview: Needs you, hosts, delivery in flight | Home and Live | `/overview`, Overview | built | checked: ready in 2.5 s, 35 need you, 3 hosts | `questions_list`, `executions_list`, `hosts_list`, `publishing_status`, `task_page`, `questions_answer` |
| Overview item | Home | `/overview/<id>`, Overview with the item | built | not checked: no item opened | as above |
| Conversation list with summary | Conversations | `/conversations`, list and summary by project | built | checked: 539 conversations | `sessions_list`, `project_list`, `nav_counts` |
| New conversation from a preset | Conversations | `/conversations/new`, preset picker in the work area | built | checked: presets listed | `session_presets`, `project_list`, `session_start` |
| One conversation | Conversations | `/conversations/<uuid>`, transcript, composer, metrics header | partly: metrics header and tool rows shown (OR-51, OR-52, OR-54), UUID in the URL (OR-38) | checked | `session_get`, `thread_entries`, `thread_blocks`, `thread_composer`, `thread_search`, `thread_export`, `message_send`, `questions_answer`, `session_rename`, `session_archive`, `session_unarchive`, `session_transition`, `session_fork`, `session_handoffs`, `execution_stop`, `account_list`, `profiles_list` |
| Project list and resource analytics | Projects | `/projects`, list and cross-project resources | built | checked | `project_list`, `analytics_query`, `list_spend`, `project_resources`, `project_resource_items` |
| One project | Projects | `/projects/<id>`, readiness, active work, settings from defaults, resources | built | checked: `cs` opened | `project_detail`, `project_context`, `project_readiness`, `project_update`, `project_create`, `project_initial_access_revoke`, `task_page`, `executions_list`, `publishing_status`, `artifact_list`, `catalogue`, `recommendation_set`, `execution_status` |
| Task list | Projects (facet) | `/tasks`, list and summary by project | built; own rail item until the seven-section rail (OR-46) | checked: 93 tasks | `task_page`, `list_membership` |
| One task | Projects (facet) | `/tasks/<uuid>`, statement, answers, result | partly: UUID in the URL (OR-38) | checked | `task_get`, `task_update`, `questions_answer`, `project_resources` |
| Result list | Projects (facet) | `/results`, last 24 h of results | built | checked: 17,590 results; no rail item highlighted (OR-57) | `artifact_list`, `project_list` |
| One result | Projects (facet) | `/results/<uuid>`, file viewer | partly | broken: UUID in the URL and the name, raw JSON shown (OR-38) | `artifact_get` |
| Delivery list | Projects (facet) | `/delivery`, pull requests by stage; title "Publishing" | built | checked: 33 pull requests | `publishing_status`, `delivery_history`, `delivery_baseline`, `delivery_regressions` |
| One pull request | Projects (facet) | `/delivery/<owner~repo~n>`, stage, head, merge actions | built | checked: `#834`; no rail item highlighted (OR-57) | `publishing_status`, `publishing_mark_ready`, `publishing_request_merge`, `publishing_cancel_merge_request`, `delivery_history` |
| Old delivery route | Projects | `/publishing`, client redirect to `/delivery` | partly | broken: the server answers JSON 404 before the app loads | — |
| Bot list | Home and Library › Bots | `/bots`, bots with state | built | checked: HAL0 listed | `bot_list`, `project_list` |
| One bot | Home and Library › Bots | `/bots/<uuid>`, status, runs, automations, questions, analytics, settings | partly: UUID in the URL (OR-38) | broken: run problems show the raw cause code `unknown:abandoned message` | `bot_get`, `bot_runs`, `bot_tasks`, `bot_run`, `bot_pause`, `bot_stop`, `bot_settings_save`, `bot_memory_save`, `bot_automations`, `bot_automation_save`, `questions_list`, `questions_answer`, `executions_list`, `analytics_query`, `session_presets` |
| New bot | Library › Bots | `/bots/new`, create form | built | checked: form shown, not submitted | `bot_settings_save`, `project_list`, `session_presets` |
| Lab: use cases, runs, decisions waiting | Lab | `/lab`, catalogue and leaderboard | built | checked: 75 use cases, 49 experiments wait for a decision | `lab_registry`, `lab_experiments_list`, `lab_leaderboard`, `lab_examples_list`, `lab_ratings_list` |
| New experiment | Lab | `/lab/new`, draft form | built | checked: form shown, not submitted | `lab_experiment_create`, `lab_registry`, `lab_examples_list`, `definition_list`, `project_list`, `project_detail`, `project_context` |
| One experiment | Lab | `/lab/<uuid>`, record, matrix, trials | partly: UUID in the URL (OR-38) | checked: a draft opened | `lab_experiment_get`, `lab_experiment_action`, `lab_experiment_decide`, `lab_experiment_note`, `lab_cell_action`, `lab_rating_create`, `lab_promote`, `pipeline_get`, `pipeline_run`, `questions_answer` |
| One use case or suite | Lab | `/lab/use-case:<id>`, `/lab/suite:<key>` | built | not checked: no link followed | `lab_registry`, `lab_leaderboard`, `lab_examples_list` |
| Personal Lab | Lab | `/personal-lab`, drafts and submissions | built | checked: empty for `site-agents`; no rail item highlighted | `lab_personal_list`, `lab_personal_save`, `lab_personal_promote`, `lab_personal_withdraw`, `me` |
| Library summary | Library | `/agents`, `/pipelines`, `/catalogue`: one page with drift, methods, pipelines, agents, skills | built | checked: all three paths render the same page | `definition_list`, `skills_list`, `system_changes`, `definition_take_repository`, `definition_add_skill`, `catalogue` |
| One agent | Library | `/agents/<id>`, definition editor | built | not checked: no agent row link found; the route answers 200 | `definition_get`, `definition_draft_get`, `definition_draft_put`, `definition_draft_discard`, `definition_save`, `definition_diff`, `definition_history`, `definition_usage`, `agent_effective`, `agent_compatibility` |
| One pipeline | Library | `/pipelines/<id>`, identity, differences, steps | built | checked: `worker-agent` | as one agent, plus `pipeline_run_create` |
| One catalogue entry | Library | `/catalogue/<id>`, methods and Lab comparison | built | checked: `catalogue` | as one agent, plus `lab_compare_methods`, `lab_registry`, `recommendation_set` |
| Changes and drift | Library | `/changes`, repository and host drift | built | broken: "Checking repository and host drift…" after 5 s; `system_changes` errors and is slow | `system_changes`, `definition_take_repository`, `definition_export` |
| System summary and service health | System | `/hosts`, findings and services in the last hour | built | checked | `hosts_list`, `telemetry_summary`, `telemetry_findings`, `connections_list`, `me` |
| One host | System | `/hosts/<id>`, state, daemon health, executions, CLIs | built | checked: `pds18-native` | `hosts_list`, `telemetry_summary`, `hosts_remove`, `system_purge` |
| Accounts and limits | System | `/hosts/limits`, subscription windows and burn | built | checked | `subscription_limits`, `subscription_limits_refresh` |
| CI runners | System and Live | `/runners`, CI jobs and self-hosted runners | built | checked: 65 jobs today, 4 self-hosted runners | `executions_runners`, `executions_ci` |
| Connections and keys | System | `/keys`, connections with keys and limits | built | checked | `connections_list` |
| One connection | System | `/keys/<id>`, keys, access, intake, balancing, limits | built | checked: `tavily` | `connections_key_add`, `connections_key_state`, `connections_set_access`, `connections_set_intake`, `connections_set_key_policy`, `connections_set_limits` |
| Spend | System | `/spend`, totals and breakdown by purpose | built | checked | `spend_summary` |
| People and access | System › People | `/humans`, `/humans/<id>`, `/humans/new` | built | not checked: `site-agents` sees "Only the Fleet owner can manage Humans." | `people_list`, `people_update`, `people_access_sync`, `project_list`, `me` |
| Unknown route | — | any other path | partly | broken: the server answers JSON 404; the app's "This page does not exist" never shows (UX-70) | — |
| Phone width | all | `/overview`, `/conversations`, a conversation, `/projects`, `/hosts` at 390 × 844 | partly: rail is not the bottom bar of five (OR-60) | checked: no page scroll sideways | — |
| Palette | all | ⌘K over any route | built | not checked in this run | `search` |
| Entity card | all | any `EntityCode` | built | not checked in this run | `entity_card` |
| Navigation counters | all | the rail | built | checked: Conversations and Tasks counters shown | `nav_counts` |
| Saved views, focus, widths | all | the shell | built | not checked in this run | `person_settings_get`, `person_settings_set` |
| Live updates | all | the shell | built | checked: the stream opens on every route | `changes_since` |

## Capabilities

### Cross-cutting

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Long lists that scroll without jumps | all | every list | built | checked: lists render on every route | `task_page`, `sessions_list`, `artifact_list` |
| Filters, grouping, saved views in the URL and per person | all | list headers | built | not checked in this run | `person_settings_get`, `person_settings_set` |
| Live state with observation state | all | rows and details | built | checked: hosts "now", runs live | `changes_since` |
| One state vocabulary | all | every state badge | partly: Done, Completed and Succeeded still all appear (OR-55) | checked | — |
| Measurement basis per number | all | totals and resources | built; *Target* OR-59 moves marks to Details | checked: `≈` and `≥` shown in lists and headers | `analytics_query`, `spend_summary` |
| Drill-down from a total or problem to its cause | all | totals, Needs you | partly: often two or more clicks (UX-63) | not checked: not walked | `executions_list`, `analytics_query` |
| Decisions inline, in the work and in one place | Home | Overview Needs you, task, conversation | partly: no session card on Home (OR-53) | checked: 35 need you shown, not answered | `questions_list`, `questions_answer` |
| Result viewers (text, diff, image, video) | Projects | `/results/<id>` | partly: raw JSON and text only seen | broken: see one result | `artifact_get` |
| Light and dark, desktop and phone | all | the shell | partly: phone rail not built (OR-60) | checked at 390 px in dark | — |

### Home and Overview

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Tasks across projects by state, priority and progress | Home (pulse), Projects | `/tasks` | built | checked | `task_page` |
| Everything waiting for a person | Home | `/overview` Needs you | partly: list, not session cards (OR-50, OR-53) | checked | `questions_list`, `executions_list` |
| Change priority; pause or continue work | Projects | `/tasks/<id>` | built | not checked: action | `task_update` |
| Problems aggregated by cause with count and newest time | Home, Live | `/overview` | partly: repeats not yet one item `×N` (OR-58) | checked | `executions_list`, `hosts_list` |
| Sessions with what each does now | Home | none | not built: no "now" line source ([home](pages/home.md#open)) | not checked: not built | `sessions_list` |
| Composer that picks who to talk to | Home | none (`/conversations/new` today) | partly | checked: presets on `/conversations/new` | `session_presets`, `session_start`, `message_send` |
| Quiet Home: what happened while away | Home | none | not built | not checked: not built | — |

### Live

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Load and capacity by host and account, placement reasons | Live, System | `/hosts`, `/hosts/<id>`, `/overview` | partly: no placement reasons | checked | `hosts_list`, `telemetry_summary` |
| Account and quota state per subscription | System | `/hosts/limits` | built | checked | `subscription_limits` |
| Worker slots per host with 24 h of runs | Live | `/runners` (totals only) | not built: no slot tiles or strips | not checked: not built | `hosts_list`, `executions_runners` |
| Wall mode for a TV | Live | none | not built | not checked: not built | — |

### Projects

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Purpose, context materials, rules | Projects | `/projects/<id>` | built | checked | `project_detail`, `project_context` |
| Connections bound to repositories, boards, CI and services | Projects | `/projects/<id>` settings | built | checked: repositories and environments shown | `project_detail`, `project_update` |
| Selected agents, environments, tools and recommendations | Projects | `/projects/<id>` methods | built | checked | `catalogue`, `recommendation_set` |
| Artifact placement for lab and production | Projects | `/projects/<id>` settings | partly: in inherited defaults only | not checked: folded | `project_update` |
| Retention rules with preview and confirm | Projects | none | not built | not checked: not built | — |
| Acceptance rules | Projects | `/projects/<id>` settings | partly | not checked: folded | `project_update` |
| Repository publishing status and actions | Projects | `/delivery`, `/delivery/<id>` | built | checked: actions not clicked | `publishing_status`, `publishing_mark_ready`, `publishing_request_merge`, `publishing_cancel_merge_request` |
| Readiness checks | Projects | `/projects/<id>` | built | checked: button shown, not run | `project_readiness` |
| Project facets: Sessions · Tasks · Delivery · Results · Rules | Projects | `/projects/<id>` | partly: tasks, delivery and results are their own routes (OR-46) | checked | — |

### Bots

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Automations by bot with trigger, status, next time, last outcome | Library › Bots | `/bots/<id>` Automations | built | checked: tab shown | `bot_automations` |
| Bot name, instructions, schedule under its grants | Library › Bots | `/bots/<id>` Settings | built | not checked: action | `bot_automation_save`, `bot_settings_save` |
| Create, edit, enable, pause, run now | Home, Library › Bots | `/bots/<id>`, `/bots/new` | built | not checked: actions | `bot_run`, `bot_pause`, `bot_stop`, `bot_automation_save` |
| Run history with causes and links | Library › Bots | `/bots/<id>` Runs | partly | broken: a cause shows as the raw code `unknown:abandoned message` | `bot_runs` |
| Active and asleep bots; wake one | Home | `/bots` | partly: no Asleep row on Home (OR-50) | checked: state shown | `bot_list`, `bot_run`, `message_send` |

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
| Expected outcome, inputs, requirements, dependencies | Projects | `/tasks/<id>` | built | checked | `task_get` |
| State with reason and release condition | Projects | `/tasks/<id>` | built | checked | `task_get` |
| Executions and steps as a tree with time and cost | Projects | `/tasks/<id>` | partly | checked | `task_get`, `executions_list` |
| Live transcript with paging and search | Conversations | `/conversations/<id>` | built | checked | `thread_entries`, `thread_search` |
| Results and their checks | Projects | `/tasks/<id>` | built | checked: report shown | `artifact_list`, `artifact_get` |
| Accept, rework, pause, continue, cancel, retry, change account, hand off | Projects, Conversations | `/tasks/<id>`, conversation | built | not checked: actions | `task_update`, `questions_answer`, `execution_stop`, `session_transition` |
| Share a task or result by email with expiry | Projects | none | not built | not checked: not built | — |
| Find results by project, task, type and time | Projects | `/results` | built | checked | `artifact_list` |
| Open in the right viewer, compare versions, annotate | Projects | `/results/<id>` | partly: no compare or annotate | broken: see one result | `artifact_get` |
| Copy state per location | Projects | `/results` | partly: stored and failed only | checked | `artifact_list` |

### Library

| Capability | Section | Route and screen | Built | Prod | Read or action |
|---|---|---|---|---|---|
| Agent configurations | Library | `/agents/<id>` | built | not checked: no agent opened | `definition_get`, `agent_effective` |
| Diff between revisions; where a configuration is used | Library | `/agents/<id>`, `/pipelines/<id>` Diff, Usage | built | checked: pipeline tabs shown | `definition_diff`, `definition_history`, `definition_usage` |
| Harness facts and limitations per version | Library, System | `/hosts/<id>` capabilities | partly | checked | `agent_compatibility`, `hosts_list` |
| Purpose, category, tags and mark of each agent | Library | `/agents` | built | checked: by harness and category | `definition_list` |
| Active or unused; what each agent is chosen for | Library | `/agents` | built | checked: 30-day runs shown | `definition_list`, `definition_usage` |
| Lab variants folded under their agent | Library | `/agents` | partly | not checked: not opened | `definition_list` |
| Agents waiting for a repository version; take many at once | Library | `/agents` drift banner | built | checked: "Take repo (2)" shown, not clicked | `definition_take_repository` |
| Definitions made in Fleet; save them to the repository | Library | `/changes` | built | broken: `/changes` does not finish loading | `definition_export` |
| Skills with files and carrying agents | Library | `/agents` Skills | built | checked: 7 skills | `skills_list` |
| How far a skill change has reached | Library | `/agents` | partly | not checked: not opened | `definition_list` |
| Add a skill to agents | Library | `/agents` | built | not checked: action | `definition_add_skill` |
| Catalogue entries with purpose, inputs, results, recommendations | Library | `/catalogue/<id>` | built | checked | `catalogue`, `recommendation_set` |
| Pipelines with steps, where each lives, 30-day runs and price | Library | `/pipelines/<id>` | built | checked | `definition_get`, `definition_list` |
| Bot definitions as a Library root | Library › Bots | `/bots` (own rail item) | partly: not in Library yet (OR-46) | checked | `bot_list` |

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
| Hosts: platform, last contact, observation, versions, readiness | System | `/hosts`, `/hosts/<id>` | built | checked | `hosts_list` |
| Environments and account users per host | System | `/hosts/<id>` | built | checked | `hosts_list` |
| Stores: capacity, used, free, tunnel, fill per project | System | `/hosts/<id>` | partly: free disk only seen | checked | `hosts_list`, `telemetry_summary` |
| Store capacity, service bounds, free-space warning settings | System | none | not built: no coordinator operation sets a host setting | not checked: not built | `setting_changes` (read) |
| Low space: raise capacity, move locations, add retention | System | none | not built | not checked: not built | — |
| Where each change has reached | Library, System | `/changes` | built | broken: `/changes` does not finish loading | `system_changes` |
| Connections with accounts, limits, projects, cost per call | System | `/keys`, `/keys/<id>` | built | checked | `connections_list`, `connections_set_limits` |
| People, roles and project grants | System › People | `/humans` | built | not checked: owner only | `people_list` |
| Shares: email, object, expiry; revoke | System | none | not built | not checked: not built | — |
| Humans roster, admission, Cloudflare rule, revocation | System › People | `/humans/<id>`, `/humans/new` | built | not checked: owner only | `people_update`, `people_access_sync` |
| Access status between Cloudflare and Fleet | System › People | `/humans` | partly | not checked: owner only | `people_access_sync` |
| Time, cost, tokens, CPU, waits by any dimension | System, Projects | `/projects` resources | built | checked | `analytics_query` |
| Spend by purpose and what it bought | System | `/spend` | built | checked | `spend_summary` |
| Drill-down from a total to runs and transcripts | System, Projects | `/projects`, `/spend` | partly | not checked: not walked | `analytics_query`, `thread_entries` |
| Discuss findings with an agent; start an experiment from selected work | Conversations, Lab | none | not built | not checked: not built | — |
| Service health: errors and latency per operation | System | `/hosts` summary | built | checked: shows `system_changes`, `lab_examples_list`, `catalogue` failing | `telemetry_findings`, `telemetry_summary` |
