# Legacy page inventory

> Historical record. It was written against the TypeScript coordinator and the Rust and Python executor, which the Go port deleted; file names in it are not current code. Current code: `go/coordinator`, `go/executor`, `go/gateway` ([stack](fleet-stack.md)).

* **Date:** 2026-11-10
* **Source:** `origin/main` at `74e0186`.
* **Purpose:** what each `/legacy/*` page offered, and whether the operator app (`app/`) covers it.
* **Status:** all legacy pages, their routes and redirects, the old lab and its APIs were removed in one cut with Fleet Lab Stage 0 ([fleet-lab.md](fleet-lab.md), owner, 2026-11-10): no shims, no read-only period; `/legacy/*` returns 404. Every gap below is marked with the app section that rebuilds it (sections from `packages/ui/src/registry.ts`).

Coverage: **covered** means a new screen does the same job; **partly** means the screen shows it but lacks an action or detail; **not covered** means no new screen has it.

## `/legacy`: sessions (`packages/coordinator/ui.ts`)

| Feature or action | API | New app |
|---|---|---|
| Open a session by id, see state, messages, events, executions | `session_get` | Covered: Conversations (`/conversations/:id`). |
| Start a new session | `session_create` | Covered: Conversations, "New conversation". |
| Send a message or steer | `message_send` | Covered: Conversations, composer. |
| Stop the running execution | `execution_stop` | Covered: Conversations, "Stop". |
| Fork a session | `session_fork` | Covered: Conversations, "Fork". |
| Hand off to a chosen profile, with optional named account | `profiles_list`, `session_transition` | Removed, to be rebuilt in Conversations. Before: not covered. |
| Apply a profile to the session | `session_profile` | Removed, to be rebuilt in Conversations. Before: not covered. |
| "Enable scenario: site and review" (sets profile `website-review`) | `session_profile` | Removed, to be rebuilt in Conversations. Before: not covered. |
| Answer an agent question with free text | `reply` | Removed, to be rebuilt in Conversations. Before: partly: Conversations answers option questions through `questions_answer`; no free-text reply. |
| Related work: delegations with parent, child and continuation state | `session_get` (`delegations`) | Removed, to be rebuilt in Conversations. Before: not covered. |
| Compare an execution with the previous one (changed files) | `execution_differences` | Removed, to be rebuilt in Results. Before: not covered. |
| Results: list and inline HTML preview in a sandboxed frame | `/api/results/:id` | Removed, to be rebuilt in Results. Before: not covered (Results is a later slice). |

## `/legacy/lab`: laboratory (`lab-ui.ts`, routes in `lab.ts`)

| Feature or action | API | New app |
|---|---|---|
| List trusted profiles | `GET /api/lab/profiles` | Removed, to be rebuilt in Laboratory (`/lab`, [fleet-lab.md](fleet-lab.md) Stage 3). Before: not covered. |
| Create an experiment: one prompt, two profiles | `POST /api/experiments` | Removed, to be rebuilt in Laboratory (`/lab`, [fleet-lab.md](fleet-lab.md) Stage 3). Before: not covered. |
| Load run history (all executions) | `GET /api/lab/executions` | Removed, to be rebuilt in Laboratory (`/lab`, [fleet-lab.md](fleet-lab.md) Stage 3). Before: not covered. |
| Compare two runs side by side (messages, events, results) | `GET /api/lab/compare` | Removed, to be rebuilt in Laboratory (`/lab`, [fleet-lab.md](fleet-lab.md) Stage 3). Before: not covered. |
| Save a comparison of two existing runs without a new run | `POST /api/experiments/from-runs` | Removed, to be rebuilt in Laboratory (`/lab`, [fleet-lab.md](fleet-lab.md) Stage 3). Before: not covered. |
| Open an experiment with its trials | `GET /api/experiments/:id` | Removed, to be rebuilt in Laboratory (`/lab`, [fleet-lab.md](fleet-lab.md) Stage 3). Before: not covered. |
| Change the selected comparison | `POST /api/experiments/:id/comparison` | Removed, to be rebuilt in Laboratory (`/lab`, [fleet-lab.md](fleet-lab.md) Stage 3). Before: not covered. |
| Save a note on an experiment | `POST /api/experiments/:id/notes` | Removed, to be rebuilt in Laboratory (`/lab`, [fleet-lab.md](fleet-lab.md) Stage 3). Before: not covered. |
| Result preview | `/api/results/:id` | Removed, to be rebuilt in Laboratory (`/lab`, [fleet-lab.md](fleet-lab.md) Stage 3). Before: not covered. |

## `/legacy/projects`: projects and delivery (`project-ui.ts`, routes in `projects.ts`)

| Feature or action | API | New app |
|---|---|---|
| List projects and open one (context, rules, locations) | `project_list`, `project_context` | Removed, to be rebuilt in Projects. Before: not covered. Tasks filters by project only. |
| Check project readiness on a host | `project_readiness` | Removed, to be rebuilt in Projects. Before: not covered. |
| Start a session in a project with a profile, account and first message | `session_start`, `profiles_list` | Removed, to be rebuilt in Projects. Before: not covered (New conversation has no project, profile or account). |
| Capability catalogue, methods, set the project's recommended method | `catalogue`, `recommendation_set` | Removed, to be rebuilt in Projects. Before: not covered. |
| Run a search pipeline and see its history | `pipeline_history`, `/api/pipeline-runs` | Removed, to be rebuilt in Projects. Before: not covered. |
| Create a site delivery, generate, run a step, see history | `delivery_create`, `delivery_generate`, `delivery_step`, `delivery_history`, `execution_status` | Removed, to be rebuilt in Projects. Before: partly: deliveries are tasks, so Tasks shows them with state and executions; creating and stepping one is not in the app. |
| Ask and answer project questions, see history | `question_history`, `question_answer`, `questions_answer` | Removed, to be rebuilt in Projects. Before: partly: Overview and Tasks answer open questions; no history and no asking. |
| Tasks list and detail | `task_list`, `task_get` | Covered: Tasks. |
| Accounts: create, connect, enable, set project preference | `account_list`, `account_create`, `account_connect`, `account_enabled`, `account_preference` | Removed, to be rebuilt in Access. Before: not covered. |
| Result links | `/api/results/:id` | Removed, to be rebuilt in Projects. Before: not covered. |

## `/legacy/recovery`: transport recovery (`recovery.ts`, page inline)

| Feature or action | API | New app |
|---|---|---|
| Inspect an execution's known and unknown transport state by id | `transport_inspect` | Removed, to be rebuilt in Hosts. Before: partly: Overview lists execution problems (`executions_list {problem:true}`); no inspect. |
| Authorize a retained transport (never reruns native work) | `transport_authorize` | Removed, to be rebuilt in Hosts. Before: not covered. |

## `/legacy/accounts`: accounts and host environments (`account-ui.ts`, routes in `accounts.ts`)

| Feature or action | API | New app |
|---|---|---|
| List managed accounts and their status per host | `account_list`, `account_hosts` | Removed, to be rebuilt in Access. Before: not covered. Hosts shows hosts, not accounts. |
| Create a managed account record | `account_create` | Removed, to be rebuilt in Access. Before: not covered. |
| Discover accounts on hosts | `account_discover` | Removed, to be rebuilt in Access. Before: not covered. |
| Connect, enable or disable an account | `account_connect`, `account_enabled` | Removed, to be rebuilt in Access. Before: not covered. |
| Login handoff to the host | `account_login_handoff` | Removed, to be rebuilt in Access. Before: not covered. |

## Summary

| Page | Covered | Gaps |
|---|---|---|
| `/legacy` sessions | Open, create, send, stop, fork | Profile handoff and switch, scenario button, free-text reply, delegations, execution differences, results |
| `/legacy/lab` | Nothing | Whole page: Laboratory slice |
| `/legacy/projects` | Tasks list and detail | Projects, readiness, project sessions, catalogue, pipelines, delivery actions, question history, accounts |
| `/legacy/recovery` | Problems are listed | Inspect and authorize |
| `/legacy/accounts` | Nothing | Whole page: Access slice |

## Server code that existed only for these pages (removed)

| Code | What it serves | Other callers |
|---|---|---|
| `packages/coordinator/ui.ts` | `/legacy` page HTML | None |
| `packages/coordinator/lab-ui.ts` | `/legacy/lab` page HTML | None |
| `packages/coordinator/project-ui.ts` | `/legacy/projects` page HTML | None |
| `packages/coordinator/account-ui.ts` | `/legacy/accounts` page HTML | None |
| Inline HTML in `recovery.ts` (`GET /legacy/recovery`) | `/legacy/recovery` page | None |
| `LEGACY_ROUTES` and the proxy-mode Bearer exception in `app.ts` | Public legacy pages outside proxy mode | None |
| `GET /api/lab/profiles`, `GET /api/lab/executions`, `GET /api/lab/compare` (`lab.ts`) | Lab page | Tests only |
| `GET/POST /api/experiments`, `/api/experiments/from-runs`, `/api/experiments/:id`, `/:id/comparison`, `/:id/notes` (`lab.ts`) | Lab page; no op equivalent (`app.ts` notes them as UI-only routes) | Tests only |
| Redirects `/lab`, `/projects`, `/recovery`, `/accounts` to `/legacy/*` (`app.ts`) | Old bookmarks | None |

Per-feature REST aliases such as `/api/sessions/*`, `/api/executions/*`, `/api/accounts*`, `/api/projects*`, `/api/pipeline-runs*`, `/api/profiles`, `/api/reconciliation` and `/api/recovery/:operation` are not called by the legacy pages (they use `/api/ops/*`); only tests call them. They are kept as aliases (`app.ts`) and are a separate removal question. `/api/results/:id` was used by the legacy pages and is the planned Results reader, so it stays. The old lab's `experiment` documents were deleted by migration 11 (owner decision; not restorable); their executions stay without the experiment link. `GET /api/profiles` replaces `GET /api/lab/profiles`.
