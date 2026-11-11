# Fleet PM agent: task tools, scope and focus, held tasks, watch mode

*   **Author:** Alex Kirs
*   **Status:** Accepted (Q7 decided 2026-11-10)
*   **Date:** 2026-11-11 (code references moved to the Go coordinator on main `4e5bf83`)
*   **Depends on:** [fleet-people-access.md](fleet-people-access.md) slices 1-4 (identity, host secrets, visibility, holder and director; live on main as migrations 16-17). This design does not repeat them.

--------------------------------------------------------------------------------

## Overview

A Product Manager (PM) is an ordinary Fleet agent profile. It differs from a worker only in its instructions and in which tools its person may use. It reads project context, specs, tasks and run results, writes tasks, refines them while they are held, releases them to the queue, and in watch mode wakes up by itself when something in its focus changes.

The PM is not a subsystem. This design adds three general capabilities that any agent can use, always inside the rights of the person who directs it:

1.  **Agent task tools** (create, update, hold, release, cancel, list, read) as bound agent operations.
2.  **Scope and focus.** Scope is the director's grants, checked on every call. Focus is a stored filter inside scope that sets what the PM looks at and the default project of new tasks.
3.  **Watch.** A `watch` document on the existing change log plus a stored next-wake time. Wakes are ordinary queued messages to the PM session.

All code references are to the Go coordinator (`go/coordinator/`); the schema is `go/coordinator/schema.sql`, embedded by `store.go`.

## The Problem

*   `config/agents/pm.json` exists but can only start workers in its own project. `bound()` in `go/coordinator/agent.go` forces every `session_start` into the caller's session `projectId` (`ownProject()`); a session with no project gets 403.
*   Host agent calls (`POST /api/hosts/agent-tools`, `agent.go` `init()`) run in one transaction with the coordinator's `*` scope and `q.person == nil`. The person gate (`guardOp`, `people.go`) and row-level security never apply to them, and `director(q)` falls back to the owner.
*   Only the agent table `agentOps` is reachable from agents. Task operations (`task_create`, `task_update`, `task_page` in `tasks.go` `init()`) are not, and `task_create` writes `createdBy = actor(q, "operator")`.
*   Nothing wakes an agent except a finished child (`wake()`, `agent.go`) and a reply. A PM cannot notice a stuck task or a new question by itself.
*   `fleet-data-foundation.md` says an agent acts with its execution's permissions; `fleet-people-access.md` says executions inherit `directed_by`. The two docs disagree on whose rights an agent call uses.

## Goals

*   **G1.** A PM is a profile in `config/agents/`; any harness and model; model switch mid-session works as today (`session_transition`).
*   **G2.** Every agent call is checked against the execution's director (`execution.directedBy`, people-access §5). A profile never grants rights. Audit rows record `actor = execution:<id>`, `onBehalfOf = <director email>`.
*   **G3.** A PM session starts with no project, one, or several. Its focus (a list of projects or all) is stored on the session and changeable by the PM or its person.
*   **G4.** Agent task tools: `task_create`, `task_update`, `task_hold`, `task_release`, `task_cancel`, `task_page`, `task_get`, plus the read tools that already exist. "Delete" is `task_cancel` (Alternative F).
*   **G5.** A task created by an agent starts **Held**. The PM releases it; release puts it in `queued` (ready).
*   **G6.** Fleet management for the owner's PM: settings (hosts, budgets, accounts, connections), profiles, pipelines, Lab, projects and people, through operations, each checked against the director's role and audited with before and after.
*   **G7.** Watch mode: wake on matching changes in focus, and on a stored timer, through the existing message path; survives coordinator restarts.
*   **G8.** Live test on `desiign` (CUJ 1).
*   **G9.** Measure and show what watch costs: wakes per session and model time per wake, on the session view. No caps.

## Non-Goals

*   A message broker or a second transport.
*   Rights granted by a profile.
*   Concurrency caps, wake caps or other invented limits. Fleet measures and shows; the owner decides.
*   A new PM table. Storage is one task column, one change column and the `watch` document kind.
*   Anomaly detection beyond signals Fleet already records (§6).
*   People, grants, director, take and dispatch themselves: [fleet-people-access.md](fleet-people-access.md).

## Open Questions

Defaults below are used in this design. Each can be changed by the owner without changing the architecture.

*   **Q1. Director of children a PM starts.** Default: the PM execution's director (`sessionStart` already copies `directedBy` from `callerExecutionId`).
*   **Q2. Does release need a person's confirmation?** Default: no. The PM releases within its director's grants. The person sees every release in the task history and can hold the task again.
*   **Q3. "Fleet itself": project or global scope?** Default: global scope. A session with no project has no project filter; operations on Fleet itself (settings, profiles, pipelines, people) need the owner or architect role, checked per operation by `guardOp`. No fake "fleet" project.
*   **Q4. Grants revoked while a PM runs.** Default: every call is checked when made, so the next call fails with the reason. The running turn is not stopped. Watch wakes for projects outside the new scope stop matching. Grants change when `people.json` is reloaded (§1, people operations).
*   **Q5. One long session or one per wake?** Default: one session; each wake is a new execution of that session, as with any queued message today. The PM keeps its conversation.
*   **Q6. Which signals count as anomalies?** Default: the ones Fleet already records (§6).
*   **Q7. Do people and grant changes from an agent need a person's confirmation?** **Decided by the owner (2026-11-10): no confirmation by default.** Changes are audited and visible. Confirmation through a `question` is an opt-in setting, not built in this design.

## Critical User Journeys (CUJs)

### CUJ 1: The owner's PM creates and runs a task (live test)

*   **Given:** people-access slices 1-4 are live. Alina has a grant on `desiign` (`people.json`).
*   **When:** the owner starts a PM session (`POST /api/sessions`, then `POST /api/sessions/{id}/profile {profileId: "pm"}`), sets focus `desiign`, and asks for a trivial task ("the agent only answers: what is in README?"). The PM creates it; the task is Held; the owner says "go"; the PM releases it.
*   **Then:** the task shows `createdBy = execution:<PM execution>`, `directedBy = owner`. It runs through take and dispatch (people-access §5-§7): queued, in progress, completed. The PM starts the worker with `session_start {taskId}` and is woken by its finish (`wake()`), reads the result with `execution_status`, and reports it. A worker `ask` on the task opens a question; the PM's watch wakes on it.
*   **Rights check:** a PM directed by Alina gets only `desiign` rows from `task_page`, and its `task_create` with another project's `projectId` fails (404 "Unknown project", the existing `guardOp` answer).

### CUJ 2: Owner's PM watches Fleet

*   **Given:** the owner starts a PM with no project and watch on (all projects, kinds `question`, `execution`, `task`), timer 1 hour.
*   **When:** a worker in project `fleet` raises a question.
*   **Then:** within one watch tick the PM gets one message "Fleet: 1 change in your focus: question q1 on task t1 …". It answers or asks the owner through the `question` table. Its own `task_update` does not wake it again. After an hour without changes it gets a timer wake.

### CUJ 3: Owner's PM changes a setting

*   **When:** the owner tells the PM "pause Simon's PC".
*   **Then:** the PM calls `hosts_update {id, intake: "paused"}`; the call runs with the owner's role; one `setting-change` document records `previous`, `value`, `actor = execution:<id>`, `onBehalfOf = owner`. The same call from Alina's PM is refused by the existing holder check of `hosts_update`.

## Technical Plan (Architecture)

```text
 person ──starts──> PM session {directedBy, focus, watch?}
                          │ each call
                          ▼
   POST /api/hosts/agent-tools  (agent.go)
     1. binding tx (scope *): host, execution, command proven; director = execution.directedBy
     2. q.person = director, q.agent = execution:<id>; ctx carries fleet.actor
     3. name in agentOps → bound() (own session tree, scope *)
        else operation/long operation → deny list, guardOp, runOp in scopeOf(director)
     4. every transaction of the call sets fleet.actor; setting-change gets actor + onBehalfOf
                          │
                          ▼
     change log (change.actor)  ──read by──> watchPass() on its own ticker
                                                   │ watch documents
                                                   ▼
                       submit(queued message, requestId=watch:<id>:<cursor>)
                                                   │
                                                   ▼
                                        PM session next execution
```

### 1. Rights: director, never the profile

*   **Director.** The route proves host, execution and command in a first transaction under `*` (trusted coordinator code), as today. It then resolves the director: `execution.directedBy` (set at enqueue from the session, and changed by a take), else the owner. `q.person` becomes that `*Person` from `q.app.people`; a new field `q.agent = "execution:"+executionID` marks the call as an agent's.
*   **Actor.** `q.agent` goes into the request context. `Store.once()` (`store.go`) sets `SET LOCAL fleet.actor` from the context next to `fleet.project_ids` and `fleet.person`, so every transaction of the call, including the own transactions of long operations (Lab, pipelines), carries it. `actor(q, fallback)` (`people.go`) returns `q.agent` first, so `createdBy`, `askedBy` and `answeredBy` written by an agent name the execution. `director(q)` keeps returning the person.
*   **Which operations an agent can reach.** A name is resolved in `agentOps` first (agent names win); those run through `bound()` in the binding transaction under `*`, because they act only on the caller's own session tree, whose rows may have `project_id = NULL` and would be hidden by row-level security from a non-owner scope. Any other name is looked up in `operations` and `longOperations` (`registry.go`, `pipelines.go`) and runs through `runOp` (`ops.go`) in a new transaction with `scopeOf(director)`. Its `gatedObjOp` wrapper applies `guardOp` with the director as `q.person`: the architect gate and the project grant check. An owner director gets `*`. `tools/list` publishes `agentOps` plus every operation name the director may call (non-architects do not see `architectOps` names), minus the deny list. The executor's MCP server answers `tools/list` itself, so a profile with `fleetOperations: true` (the PM) gets the same list in its start command (`fleetOperations`, built at dispatch for the director); the executor lists those tools and forwards them to `/api/hosts/agent-tools`.
*   **Deny list** (person-only, exact names, checked before lookup with 403 "Only a person may do this"): `task_take` (people-access §5), and the operations that return a secret, which must never enter a model's context: `hosts_register`, `hosts_rotate_secret` (host secrets, slice 2) and `connections_token_create` (a gateway token). The list is a constant next to `bound()`.
*   **Host and director.** A host lends a director's rights only when its holder is that director or the owner (owner hosts run taken work, people-access §5); otherwise the call is 403. A call without an execution (`initialize`, `tools/list`) acts for the host's holder; there is no implicit owner fallback.
*   **Audit.** `put` (`records.go`) is the single writer of documents. When the kind is `setting-change` and `q.agent` is set, it sets `actor = q.agent` and `onBehalfOf = q.person.Email`. This amends `fleet-data-foundation.md` (agent permissions): an agent acts with its director's grants; the actor stays the execution.
*   **Worker lock stays.** A session with `projectId` and no `focus` keeps today's `session_start` branch (`ownProject()` in `bound()`). A session with `focus` uses the focus branch (§2). The branch is chosen by `focus` being present on the session document.

### 2. Scope and focus

*   **Scope** is not stored. It is the director's grants at call time (Q4).
*   **Focus** is one field on the session document: `focus: {projectIds: string[] | "*"}`. When it resolves to exactly one project, that project is the default `projectId` of `task_create` and `session_start`; otherwise `projectId` is required (400). No stored task filter: the PM passes filters to `task_page` per call.
*   Focus is set with a new agent operation `focus_set {sessionId?, projectIds}` in `agentOps`. The agent sets its own session; a person calls it with `sessionId` through `/api/ops/focus_set`. Project IDs outside the director's (or the calling person's) grants give 404 "Unknown project", the `guardOp` answer.
*   Focus is a filter, not a right: writes check scope, not focus. Reads without an explicit project (`task_page`, `task_list`, `questions_list`) run in `scopeOf(director)`, so row-level security limits them to scope; `bound()` adds the focus as the project filter when the caller gives none (`task_page` gains `filter.projectIds`).
*   **Children.** `sessionStart` already copies `directedBy` from `callerExecutionId` (Q1). In the focus branch the child's project must be in focus and in scope, and a `profileId` must be in that project's roles map.

### 3. Task tools

*   Task operations reach agents through the operation lookup (§1). `bound()` cannot special-case them (they do not pass through it), so the operations read `q.agent`:
    *   `task_create`: when `q.agent` is set, the task is created Held (§4) whatever the caller passes, `createdBy = q.agent` (through `actor()`), `directedBy = director(q)`, and a missing `projectId` comes from a one-project focus. A person may pass `held: true`; persons keep `queued` by default.
    *   `task_update`: when `q.agent` is set, an `action` argument is rejected with 400 "Use task_hold, task_release or task_cancel"; field updates (`title`, `expectedOutcome`, `priority`, `resultDestination`) work as today.
    *   `task_hold`, `task_release`, `task_cancel`: new operations in `tasks.go`, calling `taskTransition()`; available to persons too.
*   Context reads are whatever `tools/list` publishes for the director: `task_page`, `task_get`, `questions_list`, `executions_list`, `changes_since`, `project_context`, `pipeline_get`, `lab_*` reads, plus agent `execution_status` and `questions`.
*   **Avoiding duplicates** is an instruction: the PM profile tells the model to call `task_page` before `task_create`. Fleet does not block similar titles.

### 4. Held tasks: `paused` with reason `held`

*   Visible state is **Held**. Storage reuses `paused` and adds `task.pause_reason text NULL` (`held`, or null for an ordinary pause).
*   `createTask` with `held` inserts the row directly as `state='paused', state_before_pause='queued', pause_reason='held'`: one write, no transient `queued` state.
*   `taskTransition()` writes `pause_reason` on `pause` (new trigger `hold`, same rule as `pause` from `queued` only) and clears it on any other trigger, the same way `state_before_pause` is handled.
*   Rules:

| Call | From | Result |
| --- | --- | --- |
| `task_hold` | `queued` | `paused`, reason `held` |
| `task_hold` | `in_progress`, `waiting`, `paused` | 409 "Task <state> cannot hold" (running work is paused, not held) |
| `task_release` | `paused` + `held` | back to `queued` |
| `task_release` | `paused` + null | 409 "Paused by a person: resume it" |
| UI resume (`task_update {action: resume}`) | `paused` + `held` | same as release |
| `task_cancel` | any open state | `cancelled` (existing trigger) |

*   Why not a new state: `paused` already keeps the previous state, is skipped by `settleTask()`, and has every transition needed. A new state would touch `taskStates`, `openTask`, `taskTransitions`, the dispatch queries, sort order and every UI state map with no difference in behaviour.
*   **Views.** `pause_reason` is returned as `pauseReason` by `taskListCols`, `taskGet` and `taskRecord`. `taskWhere()` accepts `held` as a state value: `held` becomes `t.state='paused' AND t.pause_reason='held'`, `paused` becomes `t.state='paused' AND t.pause_reason IS DISTINCT FROM 'held'`. State counts return `held` as its own key. `packages/view-url/view-url.ts` `TASK_STATES` gains `held`.
*   The app (`app/src/views/tasks.tsx`) maps `paused + held` to the label "Held" with its own chip from `packages/ui`.

### 5. Watch mode

*   **Watch document.** One document of kind `watch` per session, id `watch:<sessionId>` (document ids are unique across kinds): `{id, sessionId, kinds, cursor, timerMs?, nextWakeAt?, wakes, lastWakeAt, lastError}`. Agent operations `watch_set {kinds, timerMs?}` and `watch_stop`. `watch_set` initialises `cursor` from `head()` (`stream.go`), so a new watch starts from now.
*   **Who knows the actor.** The `change` table has no actor today. Migration 20 adds `change.actor text NULL`; `fleet_change()` fills it with `current_setting('fleet.actor', true)` (§1). `since()` (`stream.go`) returns `actor` and `projectId`. Rows with null actor (operator UI, system) count as foreign changes.
*   **When the pass runs.** A ticker in `onStart` (`artifact_routes.go` pattern) runs `watchPass()` single-flight every `FLEET_WATCH_TICK_MS` (default 15000; tests set it short). It is a setting, not a limit: the owner trades latency for load.
*   **One pass, per watch document:**
    1.  Read changes after `cursor` with `since()`, paging by 500 until done, in a read transaction with `scopeOf(director)`, so row-level security drops changes outside scope. Rows with no project (hosts) are visible only to the owner.
    2.  On `resync` (cursor pruned), set `cursor` to head and queue one message "Fleet: change history after your last wake was pruned; re-read your focus with task_page."
    3.  Keep changes whose project is in focus and whose kind is in `kinds`.
    4.  Drop changes whose `actor` is an execution of this session (one lookup of the session's execution IDs). Drop changes whose `sessionId` is a child of this session: their finish already wakes the PM through `wake()`.
    5.  Classify each change by its current row (table below). The row may have moved on; the message says "now <state>", which is what the PM needs.
    6.  If a `watch:` or `timer:` message for this session is still queued (PM busy), skip steps 7-8 and keep the cursor: the next pass sends one message covering everything.
    7.  If anything remains, `submit({sessionId, requestId: "watch:"+id+":"+cursor, text})`, then advance `cursor`, count `wakes`, set `lastWakeAt`. The request ID makes a retried pass collapse into one message.
    8.  Timer: if `nextWakeAt <= now`, submit `requestId: "timer:"+id+":"+nextWakeAt` and set `nextWakeAt = now + timerMs`. The stored time survives restarts.
    9.  On a `submit` error, keep the cursor and write `lastError`; the session view shows it.

| `kinds` value | Change rows | Classified by |
| --- | --- | --- |
| `task` | `kind='task'` | current `task.state` (`held` for a held pause) |
| `question` | `kind='question'`, op `I` | open question row |
| `execution` | `kind='execution'` | current state `failed` or `unknown` only |
| `host` | `kind='host'` | `host-observation` problems (owner only) |
| `limits` | none (read each pass) | a pool holding a subscription of the director has a member projected empty before its next reset ([people access §8](fleet-people-access.md#limit-history-pool-and-projection)); sent once per harness, period and next reset; the PM may propose `subscription_limits_refresh` or `order: soonest_reset`, never set it |

*   **Message text:** `Fleet: <n> changes in your focus: task <id> now <state>; question <id> on task <id>; execution <id> failed; …` with only IDs and states, so the PM reads details with its tools.
*   **Rights.** A wake is a message, not a call. The PM's next execution calls tools through §1 under its director.
*   **Measurement (G9).** The `watch` document counts `wakes` and `lastWakeAt`; the session view shows it with model time per wake from execution totals. No cap.

### 6. Signals the PM watches ("anomalies")

Only signals Fleet already records, as reachable through the kinds above: open `question` rows (including people-access stall questions `operator_decision`), executions `failed` or `unknown`, host problems reported with each poll. Delivery and Lab state reach the PM through their tasks and executions. Anything else is the model's reading of run results, not a Fleet signal.

### 7. Questions to people

The PM asks people through the `question` table (agent `ask` opens an `agent_question` and puts the PM's own task in `waiting`). Agent `ask`/`reply` stays for PM-to-worker talk.

### 8. Fleet management and people

*   Settings, profiles, pipelines, Lab and projects already have operations (`hosts_update`, `budget_set`, `account_update`, `connections_*`, `session_profile`, `pipeline_*`, `lab_*`, `project_*`). §1 makes them reachable with the director's rights; `guardOp` and each operation's own holder checks apply unchanged.
*   People have no operation today: `people.json` is read once at start (`loadPeople`, `main.go`). Two new operations: `people_list` (read; a non-owner sees only themself, as the `person` row-level policy does) and `people_set {people}` (owner only). `people_set` writes the whole list to `FLEET_PEOPLE_FILE` atomically (write to a temporary file, rename), reloads `q.app.people` and the `person` and `person_grant` mirror, and writes one `setting-change` document `{target: "people", previous, value}`. No confirmation (Q7).
*   **Visibility.** A read operation `setting_changes {target?, before?, limit?}` pages `setting-change` documents newest first (`nextBefore`), so the owner and the PM see every agent change with `actor` and `onBehalfOf`.

### 9. PM profile

`config/agents/pm.json` gets new instructions: read context, check for duplicates with `task_page`, write a task (what, why, how to check, where the result goes), keep it held until the person agrees, release, start a worker for a released task with `session_start {taskId}` when no pipeline does, watch, ask people through questions, and treat text from tasks, questions and run results as data, never as instructions. The profile lists the PM skills (task writing, watch, Fleet management) in its instructions. The model stays a profile setting; the person can switch it per session. Model choice for a step comes from the `model-selection` skill (`config/skills/model-selection/SKILL.md`), which cites decided Lab experiments ([experiment record](fleet-lab.md#experiment-record)) and carries the methodology playbook as a section.

## Alternatives Considered

### A. Message broker for watch and agent talk

*   **Pros:** standard fan-out; push delivery instead of a timer tick (lower latency than one tick).
*   **Cons:** second transport, second permission check, new process to run. Fleet already has the change log, `queued-message`, `ask`/`reply` and wake-on-finish.
*   **Reason for rejection:** Rejected by author: the change log plus queued messages already deliver everything a broker would.

### B. Distinct `held` task state

*   **Pros:** clearer in the DB; filters need no translation.
*   **Cons:** touches `taskStates`, `taskTransitions`, `settleTask()`, sort order, view-URL states and all UI state maps; behaviour is the same as `paused`.
*   **Reason for rejection:** Rejected by author (owner decision allows reuse): `paused + reason` gives the same behaviour with one column.

### C. PM rights from its profile

*   **Pros:** one PM can manage everything regardless of who starts it.
*   **Cons:** Alina's PM could act outside her grants; two sources of rights.
*   **Reason for rejection:** Rejected by author: rights come only from the director.

### D. One PM session per project

*   **Pros:** reuses the one-`projectId` lock unchanged.
*   **Cons:** cross-project work needs several PMs that cannot see each other's tasks.
*   **Reason for rejection:** Rejected by author: a PM changes focus across projects.

### E. In-memory timers

*   **Pros:** no stored field.
*   **Cons:** lost on restart.
*   **Reason for rejection:** Rejected by author: a stored `nextWakeAt` is read by the watch ticker.

### F. Hard delete of tasks

*   **Pros:** no cancelled rows in lists.
*   **Cons:** loses history and breaks links from executions and questions.
*   **Reason for rejection:** Rejected by author: cancel keeps history; "delete" in the task tools is `task_cancel`.

### G. PM with read-only settings

*   **Pros:** smaller injection surface.
*   **Cons:** the owner does every settings change by hand, which the brief rejects.
*   **Reason for rejection:** Rejected by author: full access within the director's grants, audited.

### H. Watch pass on the placement ticker

*   **Pros:** no second ticker.
*   **Cons:** placement runs every 60 s and can take long (S3 copies); a question would wait behind it.
*   **Reason for rejection:** Rejected by author: a separate single-flight ticker with its own setting.

--------------------------------------------------------------------------------

## Risks and Mitigations

*   **Prompt injection through watched text.** The owner's PM has owner rights and reads text other agents wrote (questions, run results, task text). A worker's output could steer an owner-level call. Mitigations: the profile instruction in §9; every call is audited with actor and director and shown; host secrets and take are person-only (§1 deny list). People and grant changes from an agent need no confirmation by default (Q7, owner decision).
*   **Self-wake loop.** If the own-change filter misses (a write path without `fleet.actor`), the PM wakes on its own edits. Each loop still collapses to one queued message per pass (§5 step 6), and the wake count makes it visible. The tests cover it.
*   **Grant revoked mid-turn.** Calls fail from the next call on (Q4); the turn ends normally.
*   **Cost of watch.** Each wake is a model turn. Measured and shown (G9); the person changes kinds or timer.

## Non-Functional Requirements

*   **Security.** All agent rights flow through the agent-tools route with the director as `q.person`. The deny list keeps host secrets and take with people.
*   **Cost.** Each wake is a model turn. Fleet records and shows wakes (G9); no cap.
*   **Rollout.** Early prototype: no feature flag, no shims. Existing `pm.json` sessions keep working; the owner path (UI calls without `q.agent`) is unchanged; `createdBy: 'operator'` rows stay as they are.

--------------------------------------------------------------------------------

### Additional Resources

*   [fleet-people-access.md](fleet-people-access.md) §1-§7, slices 1-4
*   [fleet-data-foundation.md](fleet-data-foundation.md) (agent permissions, change log)

### Future Improvements

*   Watch filters by text or label, if the change kinds prove too coarse.
*   A PM-specific task template, if free-text tasks prove uneven.
*   Opt-in confirmation of agent people changes through a `question` (Q7).

--------------------------------------------------------------------------------

## Detailed Implementation Plan

One pull request per stage. Each ends with its check.

| Stage | Content | Check |
| --- | --- | --- |
| S1 Rights, scope and focus | director as `q.person` and `q.agent` on the agent-tools route; `fleet.actor` through the context; operation lookup and deny list; `tools/list` by role; `focus` on the session, `focus_set`, focus branch of `session_start`, default project | Alina-directed agent `task_page` returns only `desiign`; an architect op from her PM is 403; `task_take` from the owner's PM is 403; PM with focus `*` starts a worker in a project; `focus_set` outside scope is 404 |
| S2 Task tools and Held | migration 20 (`pause_reason`); agent `task_create` Held, `task_update` without `action`, `task_hold`, `task_release`, `task_cancel`, views, app chip | agent-created task is Held with `createdBy = execution:<id>`; release makes it `queued`; agent `task_update` with `action` is 400 |
| S3 Fleet management and audit | `onBehalfOf` on `setting-change`; `people_list`, `people_set` | owner PM `hosts_update` writes one `setting-change` with actor and `onBehalfOf`; `people_set` from a non-owner is 403 |
| S4 Watch | migration 21 (`change.actor`, `fleet_change()`); `watch` document, `watch_set`, `watch_stop`, `watchPass()`, ticker, session view | CUJ 2 checks below |
| S5 PM profile | `config/agents/pm.json` instructions and skills | profile loads; `tools/list` shows the task tools |
| Live | CUJ 1 on `desiign` | passes end to end |

### Target Files to Create/Modify

*   `go/coordinator/agent.go`: agent-tools route (director, `q.agent`, operation lookup, deny list, `tools/list`); `bound()` focus branch; `focus_set`, `watch_set`, `watch_stop`; `watchPass()`.
*   `go/coordinator/main.go`: `Request.agent`; watch ticker start.
*   `go/coordinator/store.go`: `fleet.actor` from the context in `once()`.
*   `go/coordinator/people.go`: `actor()` returns `q.agent` first; `people_list`, `people_set`.
*   `go/coordinator/records.go`: `putDocument` sets `actor` and `onBehalfOf` on agent `setting-change` rows.
*   `go/coordinator/tasks.go`: Held create, `hold` trigger, `pause_reason`, `task_hold`, `task_release`, `task_cancel`, `held` filter and count.
*   `go/coordinator/stream.go`: `since()` returns `actor` and `projectId`.
*   `go/coordinator/schema.sql`: migration 20 (`task.pause_reason`), migration 21 (`change.actor`, `fleet_change()` with the actor, copied from migration 15's latest body).
*   `packages/view-url/view-url.ts`: `TASK_STATES` gains `held`.
*   `app/src/views/tasks.tsx`, `app/src/views/conversations.tsx`: Held chip; `createdBy` link; focus and watch status on the session.
*   `config/agents/pm.json`: instructions (§9).
*   `docs/designs/fleet-data-foundation.md`: agent permissions paragraph.
*   `tests/go/pm.test.ts`: outside HTTP tests run by `tests/go/run.sh`. An agent call is driven as a host: `hosts_register`, a queued execution claimed by `/api/hosts/poll`, then `/api/hosts/agent-tools` with the start command's ID.

### Verification Plan

*   **Build:** `cd go && go vet ./... && go build -o bin/fleet-coordinator ./coordinator`.
*   **Tests:** `FLEET_TRIAL_TESTS=tests/go/pm.test.ts node tests/trial/run.ts` locally; the full gate in CI (`fleet.md`).
*   **Design check:** `pnpm run check:design` for app changes.
*   **Live:** CUJ 1 on `desiign`; CUJ 2 and CUJ 3 on the owner's coordinator; record wakes.

--------------------------------------------------------------------------------

*Drafted with EGM*
