# Stage 6: Comprehension test (fleet-operator-app.md)

Reviewer: egm-comprehension. It read the document only.

## What this proposes (reviewer's paraphrase)

A single-page Preact app at `fleet.ufobe.com` replaces the coordinator's inline-HTML prototype pages. It is built only from `packages/ui`, with four first-slice views: Tasks, Conversations, Overview and Hosts. Each view can be installed as a separate PWA window.

It reads and writes only through coordinator operations. It keeps rows in a client store built on Preact Signals and asks the server for list membership (`list_membership`).

Live updates:
* Postgres triggers write every change to a `change` log and NOTIFY.
* The coordinator streams changes over SSE, keyed by `seq`.
* The client catches up with `changes_since` after a drop, and gets `resync` if its cursor is too old.

Offline:
* A service-worker shell and an IndexedDB mirror, shown labelled with its age.
* Actions are disabled offline; nothing is queued.

Old pages move to `/legacy/*` and are deleted once each has a replacement.

## How the existing system works, per this document

One Fastify coordinator on Postgres (PGlite in tests) exposes `POST /api/ops/:name`, which are also MCP tools. There is no push channel today, so the prototype polls.

Requests go through Cloudflare Access, then the origin guard (JWT check, Bearer injected), then Fleet. There is a single owner with scope `*`. RLS is by `project_id`. Grants, artifacts and host metrics come in later phases.

## Findings

| # | Severity | Location | Finding | Fix |
|---|---|---|---|---|
| 1 | Major | CUJ 5 | "30 s without a poll" conflicts with "seen 2m"; the writer of the observation is unnamed. | Align the times and name the writer. |
| 2 | Major | §4, item 1 | The `pg_notify(..., NEW.seq)` payload is ambiguous: the source row has no `seq`, and `NEW` is NULL on DELETE. | Use `seq` from `INSERT ... RETURNING`, cast to text. |
| 3 | Major | §3 and §4 | There is no map from client kinds (`session`, `host`) to tables or document kinds. `task_dependency` handling is undefined. | Add a kind-map table. |
| 4 | Major | §6 and item 5 | `list_membership` is promised for sessions and hosts but implemented and tested only for tasks. | Add the session and host queries and tests. |
| 5 | Major | G1 and item 11 | Retry has no operation. | Name an operation or drop retry. |
| 6 | Major | Overview | No read operations or kinds are listed for the Overview. | List them. |
| 7 | Major | §4 and item 14 | `?cursor=N` and `Last-Event-ID` are both used; why `changes_since` runs before reconnect is not explained. | Pick one mechanism and explain the catch-up path. |
| 8 | Major | §4 ordering | The overlap window mixes a count and a time. Late lower-`seq` dedup is undefined. | Define the window and the client dedup memory. |
| 9 | Major | item 1 | RLS on change rows with NULL `project_id` is undefined. | State the rule. |
| 10 | Major | §3 and §5 | Nothing says when `/api/me` is re-read or how sign-out is detected. | Describe the triggers. |
| 11 | Major | §3 | Counter re-read is missing from the apply steps. | Add it as a step. |
| 12 | Major | various | Undefined references: R2, specq F7, principles §§, scroll check, list-view server note, registry, trace check, GLOBAL write, proxy mode, harness mark, unconfirmed overlay, TaskQ. | Define or link each. |
| 13 | Nit | D4 and §6 | Two `list_membership` signatures. | Unify. |
| 14 | Nit | §2 | What `/` shows is unstated. | Redirect to `/overview`. |
| 15 | Nit | §5 | "Offline" wording is also used when the app is online but not yet current. | Use separate wording. |
| 16 | Nit | §4 and item 3 | Prune has two owners. | Name one owner. |
