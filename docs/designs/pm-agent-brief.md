# Fleet Product Manager agent

* **Author:** Alex Kirs
* **Date:** 2026-11-10

## Overview

A Product Manager (PM) is an ordinary Fleet agent profile with its own instructions, skills and tools. Any model or harness can run it, and the model can be switched mid-session like any other session. The person who starts a PM session says what to work on: Fleet itself (no project), one project, or several. The PM then forms tasks the way a product manager would: it reads the project context, specs, existing tasks and runs, writes tasks that say what is needed and how, and avoids duplicating work already in progress.

The PM acts only with the rights of the person who directs it. For the owner that is everything: tasks, Fleet settings, agent profiles, pipelines, the Lab, projects and people. For Alina it is project `desiign` only; tasks her PM creates are directed by her and run on her subscription, or are taken by another person under the rules of [fleet-people-access.md](fleet-people-access.md).

A task the PM creates starts held, so it can be refined in conversation. When it is ready, the PM releases it to the queue, and dispatch picks a host and subscription. In watch mode the PM wakes by itself when something in its focus changes (a question, a stuck task, a finished run, an anomaly) or on a timer, and then either asks its person or fixes the issue where it is allowed to.

## Analysis

Seven Goldfish reviewers read the concept against the code. The main findings:

**What already exists.** `config/agents/pm.json` is a PM profile today, but it can only start worker sessions in its own project and has no task tools. Per-session model override already exists (`session_transition`, `packages/coordinator/agent-tools.ts`). Agent-to-agent messages already exist: a creator is woken when a session it started finishes, `ask`/`reply` delivers an answer as the asker's next message, `message_send` queues messages, and the store keeps a durable change log with a cursor (`store.ts`, SSE in `stream.ts`).

**What blocks the concept today.**

* A session has one `projectId` or none, and `bound()` in `agent-tools.ts` forces every `session_start` into the caller's own project. A session with no project gets 403.
* `task_create`, `task_update` and `task_list` are operator operations in `app.ts` and write `createdBy: 'operator'`. No record says which person or agent created a task.
* People and grants are only designed (`fleet-people-access.md`, status Proposed). Auth is one shared token and `me` is hardcoded. Adding Alina for real needs that design's first slices.
* There is no `ready` task state; the runnable state is `queued`. There is no dispatch function yet; the host is chosen when a message is queued.
* `/api/tools` exposes every operation, including task operations, and `/api/agent-tools` serves agent tools without binding. This is an existing access hole that a powerful PM makes worse.

**Main contradiction: a powerful PM vs. its person's limits.** Resolved by giving the PM no rights of its own. The profile never grants rights. Every PM call is checked against its director's grants and roles, and recorded as `actor = execution, on behalf of = person`. The docs currently disagree here: `fleet-data-foundation.md` says an agent acts with its execution's permissions and may update only tasks linked to its own execution; `fleet-people-access.md` says executions inherit `directed_by`. The design must settle this in favour of the director's grants.

**Second contradiction: many projects vs. one `projectId`.** Resolved by splitting scope from focus. Scope is the director's grants, checked on every call. Focus is a filter inside scope (all granted projects or a subset, plus a task filter) that the PM or its person can change; it also sets the default project for new tasks. The one-project lock stays for workers.

**Held tasks.** Reviewers split between reusing `paused` with a reason field and adding a new state. `paused` already exists, keeps the state before the pause and sorts correctly. A new state touches the view-URL state list, UI copy, sort order and `settle()`. The name "held" also clashes with "holder" in the people-access design. Decision deferred to the draft, with a lean towards a distinct state only if dispatch must treat it differently from `paused`.

**Watch mode and the broker.** All seven reviewers reject a message broker. The only missing piece is a subscription: a row `{session, cursor, filter, next wake time}` read by the existing reconcile loop, which queues one wake message per new batch of matching changes (request ID derived from the cursor so repeated wakes collapse). Changes caused by the PM itself are skipped, so it does not wake on its own edits. A stored next-wake time replaces an in-memory timer and survives restarts. Risks: each wake is a full model turn, so cost grows with change volume; `wake()` currently swallows errors; a timer wake must run under the director's grants, not the system's `*` scope.

**Two question systems.** Agent `ask`/`reply` lives in documents; task questions live in the `question` table and put a task in `waiting`. The PM should use the `question` table for anything a person must answer.

**Settings changes** are the riskiest surface: no agent tool exists, and there is no audit row with before, after and actor. Each settings operation the PM can call must check the person's role and write an audit row.

**Ideal final result.** The PM is not a new subsystem. It is a profile plus three general capabilities every agent can use within its person's rights: task tools, a scope wider than one project, and focus-based wake-ups.

## Goals vs. Non-Goals

* **In-Scope Goals:**
  * PM is a profile in `config/agents/` with instructions and skills Fleet's owner writes; any harness and model.
  * A PM session starts with no project, one, or several; focus (projects plus task filter) is set at start and changeable.
  * Agent task tools: create, update, cancel (instead of delete), list and read, with project context, specs and run results.
  * Fleet management tools for the owner's PM: settings, profiles, pipelines, Lab, projects, people, run analysis; each checked against the person's role and audited.
  * Held tasks, released to the queue by the PM.
  * Every session and task records its director; all PM calls run under that person's grants.
  * Watch mode: wake on focus changes and on a timer, through the existing message path.
  * Live test on `desiign`: add Alina, configure the project, Alina's PM creates a trivial task (the agent only answers), the task runs through the full cycle, the PM is asked about the result and reports it.
* **Out-of-Scope Non-Goals:**
  * A message broker.
  * Rights granted by a profile.
  * Concurrency caps or other invented limits.
  * Anomaly detection beyond signals Fleet already records (to be named in the draft).

## Alternatives Weighed

* **Message broker for agent-to-agent talk.** Rejected by all reviewers: Fleet already has wake-on-finish, `ask`/`reply`, `message_send` and a durable change log. A broker adds a second transport and a second place to check permissions.
* **A new PM subsystem with its own storage.** Rejected: a profile plus general agent capabilities is enough.
* **One session per project for the PM.** Rejected by the owner: a PM changes focus flexibly across projects.
* **Hard delete of tasks.** Rejected in favour of cancel, which keeps history.
* **PM with read-only access to settings.** Rejected by the owner: full access within the person's grants.
* **In-memory timers for watch mode.** Rejected: they do not survive coordinator restarts; a stored next-wake time does.

## Key Decisions & Staging for Next Steps

1. PM = profile; rights come only from the director's grants. The docs' rule on agent permissions is amended accordingly.
2. Scope (grants) vs. focus (filter) replaces the one-project lock for PM sessions.
3. Task tools move into bound agent operations; `createdBy` records the session, `directedBy` the person.
4. Held state vs. `paused` with a reason: decide in the draft.
5. Watch mode = subscription row on the change log plus stored timer, delivered as queued messages. No broker.
6. Close the `/api/tools` and unbound `/api/agent-tools` holes before giving agents task and settings tools.
7. The live test depends on `fleet-people-access.md` slices for people, grants and director, and on a minimal dispatch for queued tasks. The draft states whether the PM design waits for them or carries the minimum itself.

Owner decisions after review (binding for the draft):

* The live Alina test runs after `fleet-people-access.md` slices 1-4. The PM design does not carry its own minimum of people, grants or director.
* "Held" is the visible state name. Storage may reuse `paused` plus a reason if the draft shows that is cleaner.
* No broker. Watch mode is a change-log subscription plus a stored timer, delivered through the existing message path.
* Closing the `/api/tools` and unbound `/api/agent-tools` holes is a prerequisite, handled by a separate fix.

Open questions for the draft:

* Director of children started by a PM: the parent session's person.
* Whether releasing a task to the queue needs a person's confirmation when the director is an operator.
* Whether "Fleet itself" is a project with grants or the global scope.
* What happens to a running PM session when its director's grants are revoked.
* Whether a PM is one long session or one execution per wake.
* Which recorded signals count as "telemetry anomalies".
