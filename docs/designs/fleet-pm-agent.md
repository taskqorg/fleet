# Fleet PM agent: task tools, scope and focus, held tasks, watch mode

*   **Author:** Alex Kirs
*   **Status:** Proposed
*   **Date:** 2026-11-10
*   **Brief:** [pm-agent-brief.md](pm-agent-brief.md). Its "Owner decisions after review" are binding and override earlier brief text.
*   **Depends on:** [fleet-people-access.md](fleet-people-access.md) slices 1-4 (identity, host secrets, visibility, holder and director) and the separate fix that closes `/api/tools` and unbound `/api/agent-tools`. This design does not repeat them.

--------------------------------------------------------------------------------

## Overview

A Product Manager (PM) is an ordinary Fleet agent profile. It differs from a worker only in its instructions and in which tools its person may use. It reads project context, specs, tasks and run results, writes tasks, refines them while they are held, releases them to the queue, and in watch mode wakes up by itself when something in its focus changes.

The PM is not a subsystem. This design adds three general capabilities that any agent can use, always inside the rights of the person who directs it:

1.  **Agent task tools** (create, update, hold, release, cancel, list, read) as bound agent operations.
2.  **Scope and focus.** Scope is the director's grants, checked on every call. Focus is a stored filter inside scope that sets what the PM looks at and the default project of new tasks.
3.  **Watch.** A subscription row on the existing change log plus a stored next-wake time. Wakes are ordinary queued messages to the PM session.

## The Problem

*   `config/agents/pm.json` exists but can only start workers in its own project. `bound()` in `packages/coordinator/agent-tools.ts:233` forces every `session_start` into the caller's `projectId`; a session with no project gets 403.
*   Task operations (`task_create`, `task_update`, `task_list` in `packages/coordinator/app.ts:309-317`) are operator-only and write `createdBy: 'operator'`. No agent can create a task, and no record says which session created it.
*   Nothing wakes an agent except a finished child (`wake()`, `agent-tools.ts:123`) and a reply. A PM cannot notice a stuck task or a new question by itself.
*   `fleet-data-foundation.md` says an agent acts with its execution's permissions; `fleet-people-access.md` says executions inherit `directed_by`. The two docs disagree on whose rights an agent call uses.

## Goals

*   **G1.** A PM is a profile in `config/agents/`; any harness and model; model switch mid-session works as today (`session_transition`).
*   **G2.** Every agent call is checked against the session's director (`directed_by`, people-access slice 4). A profile never grants rights. The audit row records `actor = execution`, `on_behalf_of = person`.
*   **G3.** A PM session starts with no project, one, or several. Its focus (a list of projects or all) is stored on the session and changeable by the PM or its person.
*   **G4.** Agent task tools: `task_create`, `task_update`, `task_hold`, `task_release`, `task_cancel`, `task_list`, `task_get`, plus read tools for project context and run results that already exist.
*   **G5.** A task created by an agent starts **Held**. The PM releases it; release puts it in `queued`.
*   **G6.** Fleet management for the owner's PM: settings, profiles, pipelines, Lab, projects, people, through the existing operations, each checked against the director's role and audited with before/after.
*   **G7.** Watch mode: wake on matching changes in focus, and on a stored timer, through the existing message path; survives coordinator restarts.
*   **G8.** Live test on `desiign` (CUJ 1).
*   **G9.** Measure and show what watch costs: wakes per session and model time per wake, on the session view. No caps.

## Non-Goals

*   A message broker or a second transport.
*   Rights granted by a profile.
*   Concurrency caps, wake caps or other invented limits. Fleet measures and shows; the owner decides.
*   A new PM table or storage beyond one subscription row kind.
*   Anomaly detection beyond signals Fleet already records (§6).
*   People, grants, director, take and dispatch themselves: [fleet-people-access.md](fleet-people-access.md).
*   Closing `/api/tools` and unbound `/api/agent-tools`: separate fix.

## Open Questions

Defaults below are used in this design. Each can be changed by the owner without changing the architecture.

*   **Q1. Director of children a PM starts.** Default: the PM session's director (people-access §5 already says children inherit).
*   **Q2. Does release need a person's confirmation?** Default: no. The PM releases within its director's grants. The person sees every release in the task history and can hold the task again.
*   **Q3. "Fleet itself": project or global scope?** Default: global scope. A session with no project has no project filter; operations on Fleet itself (settings, profiles, pipelines, people) need the owner role, checked per operation. No fake "fleet" project.
*   **Q4. Grants revoked while a PM runs.** Default: every call is checked when made, so the next call fails 403 with the reason. The running turn is not stopped. Watch wakes for projects outside the new scope stop matching.
*   **Q5. One long session or one per wake?** Default: one session; each wake is a new execution of that session, as with any queued message today. The PM keeps its conversation.
*   **Q6. Which signals count as anomalies?** Default: the ones Fleet already records (§6).
*   **Q7. Do people and grant changes from an agent need a person's confirmation?** Default: no; they run under the owner's role, audited and visible (help, not block). Alternative: the PM raises a `question` and the change runs when the owner answers yes. This is the main prompt-injection lever (see Risks).

## Critical User Journeys (CUJs)

### CUJ 1: Alina's PM creates and runs a task (live test)

*   **Given:** people-access slices 1-4 are live. Alina has a grant on `desiign`. A run path exists for a task directed by Alina: either her subscription (slice 5) or a take by the owner (people-access CUJ 4).
*   **When:** Alina starts a PM session with focus `desiign` and asks for a trivial task ("the agent only answers: what is in README?"). The PM creates it, the task is Held; Alina says "go"; the PM releases it.
*   **Then:** the task shows `createdBy = <PM execution>`, `directed_by = Alina`. It runs through queued, in progress, completed. The PM is woken by the finish, reads the result with `execution_status`, and reports it to Alina. Alina's PM `task_create` with `projectId` of another project returns 403.

### CUJ 2: Owner's PM watches Fleet

*   **Given:** the owner starts a PM with no project and watch on (all projects, filter "questions, failed executions, stalls"), timer 1 hour.
*   **When:** a worker in project `fleet` raises a question.
*   **Then:** within one placement-timer tick the PM gets one message "Fleet: 1 change in your focus: question q1 on task t1 …". It answers or asks the owner through the `question` table. Its own `task_update` does not wake it again. After an hour without changes it gets a timer wake.

### CUJ 3: Owner's PM changes a setting

*   **When:** the owner tells the PM "pause Simon's PC".
*   **Then:** the PM calls the existing intake operation; the call runs with the owner's role; one `setting-change` row records previous value, new value, `actor = PM execution`, `on_behalf_of = owner`. The same call from Alina's PM is 403.

## Technical Plan (Architecture)

```text
 person ──starts──> PM session {directed_by, focus, watch?}
                          │ each call
                          ▼
         bound(name,args,executionId)  (agent-tools.ts)
           1. wrapper loads session, director; scope + fleet.actor
           2. scope = director grants + roles (people-access slice 3)
           3. per-op rule: task ops in scope; owner-only ops need owner
           4. run op in store.tx with that scope; audit actor+on_behalf_of
                          │
                          ▼
     store change log (store.changes.since)  ──read by──> watchPass()
                                                         │ subscription rows
                                                         ▼
                                     submit(queued message, requestId=watch:<id>:<cursor>)
                                                         │
                                                         ▼
                                              PM session next execution
```

### 1. Rights: director, never the profile

*   **Where the scope is set.** Host agent calls arrive at `/api/hosts/agent-tools` (`app.ts:169`), which wraps `agent.bound()` in `store.tx` or `store.read`. The wrapper loads the calling execution's session and its `directed_by`, then opens the transaction with the director's scope: `store.tx(fn, {scope: {projectIds, person}, actor: 'session:'+sessionId+'/execution:'+executionId})`. The wrapper reads the execution, session and `directed_by` under `*` before that transaction (trusted coordinator code; the host secret already proved which execution calls). Session-own rows (`session` focus, `watch`) are also read and written by trusted code under `*`, because a PM session with no `projectId` has `project_id = NULL` and row-level security would hide it from a non-owner scope. Only the operation the agent asked for runs under the director's scope. The `scope` argument comes from people-access §2 (slice 3); this design adds `actor`, which the store sets as `SET LOCAL fleet.actor` next to `fleet.project_ids`. An agent call gets `*` only when its director is the owner.
*   **Which operations an agent can reach.** Today `bound()` ends in the agent-tools `operations` map (`agent-tools.ts:202-211`) and `tools/list` publishes only that map. This design passes the app operations into `agentTools` as a getter `deps.ops: () => ops`, because `ops` is built after `agentTools` (`app.ts:148` vs `:338`) and already spreads `agent.operations`. `bound()` resolves a name in the agent map first (agent names win), then in `deps.ops()`. `tools/list` publishes both, with each operation's role tag from people-access §2. Rule: an agent may call any operation its director's role allows (owner: all; operator: `operator`-tagged ops on rows in scope).
*   **Deny list** (person-only, exact names, checked in `bound()` with 403 "Only a person may do this"): `take` and `take_rule_set` (people-access §5), `host_secret_rotate` (slice 2), `session_create` (the UI's own create, `app.ts:88`; agents use `session_start`). Names follow the slices that add them; the deny list is a constant next to `bound()`.
*   **Audit.** Every audit row written in an agent call reads `actor` from `fleet.actor` and `on_behalf_of` from `fleet.person`. For settings this is the `setting-change` row of people-access slice 4 (`{target, field, previous, value, actor, at}`), which gains `on_behalf_of`. This amends `fleet-data-foundation.md` (agent permissions): an agent acts with its director's grants; the actor stays the execution.
*   **Worker lock stays.** A session with `projectId` and no `focus` keeps today's `session_start` branch (`agent-tools.ts:242-246`). A session with `focus` uses the focus branch (§2). The branch is chosen by `focus !== undefined`.

### 2. Scope and focus

*   **Scope** is not stored. It is the director's grants at call time (Q4).
*   **Focus** is one field on the session: `focus: {projectIds: string[] | '*'}`. When it holds exactly one project, that project is the default for `task_create` and `session_start`; otherwise `projectId` is required (400). No stored task filter: the PM passes filters to `task_page` per call.
*   Focus is set when the session is created (UI create in `app.ts:88` gains an optional `focus`; `session_start` from a PM passes `focus` for a child PM) and changed with a new agent operation `focus_set {projectIds}`. Both reject project IDs outside the director's scope (403). The person edits focus from the session view, which calls `focus_set` as the person.
*   Focus is a filter, not a right: writes check scope, not focus. Reads without an explicit project filter (`task_page`, `task_list`, `questions`, `queue_list`) run inside the scoped transaction, so row-level security limits them to scope; the focus adds a `projectId IN focus` filter on top.
*   **Children.** `start()` (`agent-tools.ts:194`) copies `directed_by` from the caller's session (people-access §5 rule; Q1). In the focus branch the child's project must be in scope, and its profile must be in that project's roles map.

### 3. Task tools

*   Task operations reach agents through `deps.ops` (§1). `bound()` adds three special cases:
    *   `task_create`: `bound()` deletes any caller-supplied `createdBy` and `held`, then calls the operation with a trusted second argument `ctx {createdBy, held: true}`; the UI route passes only an optional `held` and keeps `createdBy: 'operator'`. It sets `createdBy = 'execution:'+executionId` (matching the existing `'session:'+id` form of delivery tasks, `tasks.ts:184`; the UI shows both as a link) and `held: true` (§4). `directed_by` and `requested_by` are set by the coordinator from the creating execution (people-access §5).
    *   `task_update`: accepts `title`, `expectedOutcome`, `priority`, `resultDestination`, the fields `tasks.update()` already supports (`tasks.ts:109-113`). An `action` argument is rejected with 400; agents change state only through the three operations below.
    *   `task_hold`, `task_release`, `task_cancel`: new operations in `app.ts`, calling `transition()` with `by = 'execution:'+executionId` (or the person for UI calls).
*   Context reads come through the same route: `task_page`, `task_get`, `questions_list`, `executions_list`, `changes_since` from `app.ts`, plus `execution_status`, `questions` from agent tools, plus project, pipeline and Lab reads from their modules. The full list is whatever `tools/list` publishes for the director's role.
*   **Avoiding duplicates** is an instruction: the PM profile tells the model to call `task_page` before `task_create`. Fleet does not block similar titles.

### 4. Held tasks: `paused` with reason `held`

*   Visible state is **Held**. Storage reuses `paused` and adds `task.pause_reason text NULL` (`held`, or null for an ordinary pause).
*   `create({held: true})` inserts the row directly as `state='paused', state_before_pause='queued', pause_reason='held'`: one write, no transient `queued` state.
*   `transition(id, trigger, {by, pauseReason?})` writes `pause_reason` on `pause` and clears it on any other trigger, the same way `state_before_pause` is handled (`tasks.ts:53`).
*   Rules:

| Call | From | Result |
| --- | --- | --- |
| `task_hold` | `queued` | `paused`, reason `held` |
| `task_hold` | `in_progress`, `waiting` | 409 "Running task: pause it instead" (running work is not held) |
| `task_hold` | `paused` | 409 "Already paused" |
| `task_release` | `paused` + `held` | back to `queued` |
| `task_release` | `paused` + null | 409 "Paused by a person: resume it" |
| UI resume | `paused` + `held` | same as release |
| `task_cancel` | any open state | `cancelled` (existing trigger) |

*   Why not a new state: `paused` already keeps the previous state, is skipped by `settle()` (`tasks.ts:57`), and has every transition needed. A new state would touch `OPEN`, `TRANSITIONS`, `settle()`, sort order and every UI state map with no difference in behaviour.
*   **Views.** `pause_reason` is added to the list query (`cols` and `listRow`, `tasks.ts:143-144`). `where()` (`tasks.ts:139`) accepts `held` as a state value and translates: `held` becomes `t.state='paused' AND t.pause_reason='held'`; `paused` becomes `t.state='paused' AND t.pause_reason IS DISTINCT FROM 'held'`. State counts return `held` as its own key. Group order puts `held` right before `paused`. `packages/view-url/view-url.ts` `TASK_STATES` gains `held` so view URLs can carry it.
*   The UI maps `paused + held` to the label "Held" with its own chip. Operator `task_create` from the UI is unchanged (`queued`), with an optional `held: true`.

### 5. Watch mode

*   **Subscription.** One document of kind `watch` per session: `{id, sessionId, kinds, cursor, timerMs?, nextWakeAt?, wakes, lastWakeAt, lastError}`. Agent operations `watch_set {kinds, timerMs?}` and `watch_stop`. `watch_set` initialises `cursor` from `changes.since(null)` (the head), so a new watch starts from now.
*   **Who knows the actor.** The `change` table (`schema.sql:207-218`) has no actor today. This design adds `change.actor text NULL`; `fleet_change()` fills it with `current_setting('fleet.actor', true)`, which the store sets per transaction (§1). `changes.since` returns it. Rows with null actor (operator UI, system) count as foreign changes.
*   **When the pass runs.** `reconcile()` runs only at startup (`app.ts:298`), so it is not used. A new function `watchPass()` in `agent-tools.ts` runs on the existing placement timer tick (`app.ts:300-302`, single-flight, `placementIntervalMs`), after `runPlacement()`. No new timer, no new setting.
*   **One pass, per watch row, in its own transaction with the director's scope:**
    1.  Read changes after `cursor` with `changes.since`, paging by 500 until done.
    2.  On `resync: true` (cursor pruned), set `cursor` to head and queue one message "Fleet: change history after your last wake was pruned; re-read your focus with task_page."
    3.  Keep changes whose `project_id` is in focus. Rows with no project (hosts) are visible only under the owner's `*` scope by row-level security (`schema.sql:223`), so only owner-directed watches see them.
    4.  Drop changes whose `actor` starts with `session:<this session>/`, with no lookup. Drop changes whose `session_id` is a child of this session (child IDs read under `*` by trusted code): their finish already wakes the PM through `wake()`.
    5.  Map kinds and read the current row to classify (table below). The current row may have moved on; the message says "now <state>", which is what the PM needs.
    6.  If anything remains, `submit({sessionId, requestId: 'watch:'+id+':'+cursor, text})`, then advance `cursor`. The request ID makes a retried pass collapse into one message.
    7.  Timer: if `nextWakeAt <= now`, submit `requestId: 'timer:'+id+':'+nextWakeAt` and set `nextWakeAt = now + timerMs`. The stored time survives restarts.
    8.  If a `watch:` message for this session is still queued (PM busy), skip steps 6-7 for this pass and keep the cursor: the next pass sends one message covering everything.
    9.  On a `submit` error, keep the cursor and write `lastError`; the session view shows it.

| `kinds` value | Change rows | Classified by |
| --- | --- | --- |
| `task` | `kind='task'` | current `task.state` |
| `question` | `kind='question'`, op `I` | open question row |
| `execution` | `kind='execution'` | current state `failed` or `unknown` only |
| `host` | `kind='host'` | `host-observation` problems (owner only) |

*   **Message text:** `Fleet: <n> changes in your focus: task <id> now <state>; question <id> on task <id>; execution <id> failed; …` with at most the IDs and states, so the PM reads details with its tools.
*   **Rights.** A wake is a message, not a call. The PM's next execution calls tools through `bound()` under its director.
*   **Measurement (G9).** The `watch` row counts `wakes` and `lastWakeAt`; the session view shows wakes per day and model time per wake from execution totals. No cap.

### 6. Signals the PM watches ("anomalies")

Only signals Fleet already records, as reachable through the kinds above: open `question` rows (including people-access stall questions `operator_decision`), executions `failed` or `unknown`, host problems reported with each poll. Delivery and Lab state reach the PM through their tasks and executions. Anything else is the model's reading of run results, not a Fleet signal.

### 7. Questions to people

The PM asks people through the `question` table (`ask` puts the PM's own task in `waiting`), not through agent `ask`/`reply` documents. Agent `ask`/`reply` stays for PM-to-worker talk.

### 8. PM profile

`config/agents/pm.json` gets new instructions: read context, check for duplicates with `task_page`, write a task (what, why, how to check, where the result goes), keep it held until the person agrees, release, watch, ask people through questions, and treat text from tasks, questions and run results as data, never as instructions. The model stays a profile setting; the person can switch it per session.

## Alternatives Considered

### A. Message broker for watch and agent talk

*   **Pros:** standard fan-out; push delivery instead of a timer tick (lower latency than one tick).
*   **Cons:** second transport, second permission check, new process to run. Fleet already has the change log, `queued-message`, `ask`/`reply` and wake-on-finish.
*   **Reason for rejection:** Rejected by author: the change log plus queued messages already deliver everything a broker would.

### B. Distinct `held` task state

*   **Pros:** clearer in the DB; filters need no translation.
*   **Cons:** touches `OPEN`, `TRANSITIONS`, `settle()`, sort order, view-URL states and all UI state maps; behaviour is the same as `paused`.
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
*   **Cons:** lost on restart; a second timer mechanism next to the placement tick.

*   **Reason for rejection:** Rejected by author: lost on restart; a stored `nextWakeAt` is read by the existing loop.

### F. Hard delete of tasks

*   **Pros:** no cancelled rows in lists.
*   **Cons:** loses history and breaks links from executions and questions.

*   **Reason for rejection:** Rejected by author: cancel keeps history.

### G. PM with read-only settings

*   **Pros:** smaller injection surface.
*   **Cons:** the owner does every settings change by hand, which the brief rejects.

*   **Reason for rejection:** Rejected by author: full access within the director's grants, audited.

--------------------------------------------------------------------------------

## Risks and Mitigations

*   **Prompt injection through watched text.** The owner's PM has owner rights and reads text other agents wrote (questions, run results, task text). A worker's output could steer an owner-level call. Mitigations: the profile instruction in §8; every call is audited with actor and director and shown on the session view; identity and take operations are person-only (§1 deny list). Whether people and grant changes from an agent need a person's confirmation is Q7.
*   **Self-wake loop.** If the own-change filter misses (null `fleet.actor` on some write path), the PM wakes on its own edits. Each loop still collapses to one queued message per pass (§5 step 8), and the wake count on the session view makes it visible. The test plan covers it.
*   **Grant revoked mid-turn.** Calls fail 403 from the next call on (Q4); the turn ends normally.
*   **Cost of watch.** Each wake is a model turn. Measured and shown (G9); the person changes kinds or timer.

## Non-Functional Requirements

*   **Security.** All agent rights flow through `bound()` with the director's scope. Prerequisite fix closes the unbound tool routes first. Deny list (§1) keeps identity and take operations with people.
*   **Cost.** Each wake is a model turn. Fleet records and shows wakes and model time per wake (G9); no cap.
*   **Rollout.** Early prototype: no feature flag, no shims. Existing `pm.json` sessions keep working; `createdBy: 'operator'` rows stay as they are.

--------------------------------------------------------------------------------

### Additional Resources

*   [pm-agent-brief.md](pm-agent-brief.md)
*   [fleet-people-access.md](fleet-people-access.md) §1-§7, slices 1-4
*   [fleet-data-foundation.md](fleet-data-foundation.md) (agent permissions, change log)

### Future Improvements

*   Watch filters by text or label, if the change kinds prove too coarse.
*   A PM-specific task template, if free-text tasks prove uneven.

--------------------------------------------------------------------------------

## Detailed Implementation Plan

Order: prerequisites first (people-access slices 1-4; the tool-route fix). Then slices P1-P4 here. Each ends with its check.

| Slice | Content | Check |
| --- | --- | --- |
| P1 Rights | director scope and `fleet.actor` in the host agent-tools wrapper; `deps.ops` route; deny list; `on_behalf_of` | Alina-directed agent `task_page` returns only `desiign`; an owner-only op from her PM is 403; `take` from the owner's PM is 403 |
| P2 Task tools and Held | `task_create` special case, `task_update` whitelist, hold/release/cancel, `pause_reason`, views | agent-created task is Held with `createdBy = execution:<id>`; release makes it `queued`; `task_update` with `action` is 400 |
| P3 Scope and focus | `focus` on session, `focus_set`, focus branch of `session_start`, director copied to children | PM with focus `*` starts a worker in `fleet`; `focus_set` outside scope is 403 |
| P4 Watch | `change.actor`, `watch` document, `watchPass()`, metrics | CUJ 2 checks below |
| Live test | CUJ 1 on `desiign` | passes end to end |

### Target Files to Create/Modify

#### 1. `packages/coordinator/agent-tools.ts` (Modify) — P1-P4

*   `bound()`: deny list constant; resolve names in the agent map, then `deps.ops`; special cases for `task_create` and `task_update` (§3); focus branch for `session_start` chosen by `focus !== undefined`; `focus_set`, `watch_set`, `watch_stop` operations.
*   `start()` (`:194`): copy `directed_by` and, when passed, `focus` to the child session.
*   `mcp` `tools/list`: publish agent ops plus `deps.ops` names allowed for the director's role, with their descriptions.
*   New `watchPass()` (§5 steps 1-9), exported in the returned object.

#### 2. `packages/coordinator/app.ts` (Modify) — P1-P4

*   `/api/hosts/agent-tools` wrapper (`:169`): load session and `directed_by`; call `store.tx`/`store.read` with `{scope, actor}`.
*   Pass `deps.ops: () => ops` into `agentTools`; wrapper reads session and `directed_by` under `*` first.
*   New operations `task_hold`, `task_release`, `task_cancel`; `task_create` accepts `createdBy` (agent path only) and `held`.
*   `session_create` (`:88`): optional `focus`, validated against the person's scope.
*   Placement timer tick (`:300-302`): call `agent.watchPass()` after `runPlacement()`.

#### 3. `packages/coordinator/tasks.ts` (Modify) — P2

*   `create()`: `held` inserts `paused` with `state_before_pause='queued'`, `pause_reason='held'`.
*   `transition()`: `pauseReason` in the detail argument; write it on `pause`, clear it on other triggers. Hold/release rules from the §4 table with the given 409 messages.
*   `cols`, `listRow`, `view()`: `pauseReason`. `where()`: `held`/`paused` translation. Counts: `held` key. `GROUPS.state`: `held` before `paused`.

#### 4. `packages/coordinator/db/schema.sql` (Modify) — P2, P4

*   Append one `-- migration N` section (numbered after the people-access migrations; applied by `store.migrate()`, `store.ts:117-125`): `ALTER TABLE task ADD COLUMN pause_reason text NULL`; `ALTER TABLE change ADD COLUMN actor text NULL`; `CREATE OR REPLACE FUNCTION fleet_change()` that also inserts `current_setting('fleet.actor', true)`.

#### 5. `packages/coordinator/store.ts` (Modify) — P1, P4

*   `tx`/`read`/`atomic` options gain `actor`, set with `SET LOCAL fleet.actor` beside the people-access scope settings.
*   `changes.since` selects and returns `actor`.

#### 6. `packages/view-url/view-url.ts` (Modify) — P2

*   `TASK_STATES` gains `held`.

#### 7. `config/agents/pm.json` (Modify) — P2

*   New instructions (§8).

#### 8. UI (Modify) — P2-P4

*   Task list and task view in `packages/ui` and the app screens that render task state: "Held" label and chip; `createdBy` link for `execution:` and `session:` values.
*   Session view: focus chips with edit, watch status (kinds, timer, wakes per day, model time per wake, last error).
*   `packages/ui/book/fixtures.ts`: add a held task fixture. Built from `packages/ui` only; `pnpm run check:design` passes.

#### 9. `docs/designs/fleet-data-foundation.md` (Modify) — P1

*   Agent permissions paragraph: agents act with their director's grants; the actor stays the execution.

#### 10. Tests (Create) — P1-P4

*   `tests/coordinator/pm-rights.test.ts`: Alina-directed execution calls `task_page` and gets only `desiign` rows; calls an `architect` op and gets 403; calls `take` and gets 403; audit row has `actor = execution:<id>`, `on_behalf_of = alina`; owner's PM changes host intake and one `setting-change` row has previous, value, actor and `on_behalf_of = owner`, while the same call from Alina's PM is 403.
*   `tests/coordinator/pm-focus.test.ts`: Alina's no-project PM with focus `[desiign]` runs `focus_set`, `watch_set`, `execution_status` successfully; `task_create` without `projectId` lands in `desiign`; with focus of two projects and no `projectId` gives 400; `focus_set` outside scope gives 403; `session_create` with focus outside scope gives 403; a child started by the PM has the PM's `directed_by`.
*   `tests/coordinator/pm-tasks.test.ts`: agent passing `held:false` or `createdBy:'operator'` is overridden (held, `execution:<id>`); agent `task_create` gives `paused`/`held`, `createdBy = execution:<id>`; `task_release` gives `queued`; `task_release` on an operator pause gives 409; `task_hold` on `in_progress` gives 409; `task_update {action:'cancel'}` gives 400; `task_page {state:'held'}` returns it and `{state:'paused'}` does not.
*   `tests/coordinator/pm-watch.test.ts`: a foreign task change queues one `queued-message` with `requestId watch:<id>:<cursor>`; running the pass twice queues one; a change written with this session's `fleet.actor` queues none; a child session's finish queues none from watch; pruned cursor gives one resync message and cursor at head; a `watch` row with past `nextWakeAt` after a store reopen queues one timer message.

### Verification Plan

*   **Build:** `pnpm run build`.
*   **Tests:** the coordinator test suite including the three new files.
*   **Design check:** `pnpm run check:design`.
*   **Live:** CUJ 1 on `desiign`; CUJ 2 and CUJ 3 on the owner's coordinator; record wakes and model time per wake.

--------------------------------------------------------------------------------

*Drafted with EGM*
