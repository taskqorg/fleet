# Architecture breadth plan

> Historical record. It was written against the TypeScript coordinator and the Rust and Python executor, which the Go port deleted; file names in it are not current code. Current code: `go/coordinator`, `go/executor`, `go/gateway` ([stack](designs/fleet-stack.md)).

2026-11-09. Analysis and planning only. No native call, code change or deploy. Goal: use the prototype to check the architecture **in breadth**, freeze the global architecture, then move to technical/infrastructure decisions, final software architecture and the V1 plan.

Basis: [fleet.md](../fleet.md) (canonical), [use cases](use-cases.md), architecture coverage, next-stage plan, stage3 findings, [agent tools inventory](agent-tools-inventory.md), [resources and auth](designs/fleet-people-access.md#4-subscriptions), [multi-account same env](designs/fleet-people-access.md#4-subscriptions), [layered isolation](designs/fleet-people-access.md#isolation-layers), [PDS Codex environment](pds-codex-environment.md), lab/continuation/recovery/connected evidence docs, and `packages/coordinator/*` at `3e37b33` plus uncommitted work in progress (isolation fixes by another agent are not assessed here).

Legend: **L** proven live (real native/provider/host), **F** fake/fixture only, **–** missing.

## 1. Logical blocks

| # | Block | Canonical source |
|---|---|---|
| B1 | Agent configuration: harness, account, profile (instructions, skills, tools/MCP, computer use, model/effort), role | fleet.md §Agent execution, [configuration](configuration.md) |
| B2 | Environments, hosts, accounts/bindings, subscriptions and isolation | fleet.md §Environments, [resources and auth](designs/fleet-people-access.md#4-subscriptions), [layered isolation](designs/fleet-people-access.md#isolation-layers) |
| B3 | Sessions: create, continue, resume, fork, handoff, steer, queue, stop | fleet.md §Agent execution, [execution continuity](execution-continuity.md) |
| B4 | Work management: projects, tasks, questions, results, completion | fleet.md §Work management |
| B5 | Pipelines/programs and catalogue (methods, recommendations) | fleet.md §Tools and pipelines, [capabilities and pipelines](configuration.md#capabilities-methods-and-tools) |
| B6 | Laboratory: experiments, variants, assessments, recommendations, balancing settings | fleet.md §Observation and evaluation, [experiments](designs/fleet-lab.md) |
| B7 | Agent tool surface over Fleet (MCP) | fleet.md R2, §Tools and pipelines last paragraph |
| B8 | Web on the same operations as agents (one API/MCP surface) | fleet.md R2, [interfaces](interfaces.md) |
| B9 | Repositories, worktrees, merge, publication/deploy | fleet.md §Work management (delivery), [deployment](deployment.md) |
| B10 | Allocation and balancing across hosts/accounts | [work allocation](work-allocation.md), UC11 |
| B11 | Recovery and unknown effects | fleet.md §System requirements (reliability), UC3 |
| B12 | Evidence and observability (history, usage, resources) | fleet.md R5, §Observation and evaluation |
| B13 | Security and authority (operator/architect/agent scopes, machine auth, credentials) | fleet.md §System requirements (authority) |

## 2. Block matrix

| Block | Intent | Exists (code) | Live | Fake only | Missing | Contradiction intent vs reality |
|---|---|---|---|---|---|---|
| B1 Config | Config = harness+model+instructions+skills+tools+env reqs; no personal inheritance; resolve at start | `config/agents/*.json`; `/api/profiles`; executor maps profile to Codex `developer_instructions`, Claude `--system-prompt`/`--tools`, agy `--gemini-dir` | Profile change applied (lab baseline vs design-rules MCP, lab live); Codex static bleed test clean ([isolation](designs/fleet-people-access.md#isolation-layers) test c) | Unknown-key refusal, profile freeze | Skills (disabled everywhere); computer use (disabled); role catalogue; project override resolution chain | Executor pins Codex `gpt-6.1-sol`/`low` (collision 10) vs profile-owned model; Codex collaboration tools cannot be disabled (collision 1) vs "Fleet prepares tools explicitly"; Claude/agy run with personal `HOME` (collision 4, unverified bleed) |
| B2 Env/accounts | Account = identity, binding = account×environment, credential host-local; environment = credential store | `accounts.ts` (create/connect/discover/login_handoff/enabled/preference), `/api/hosts/accounts`, `FLEET_NATIVE_HOST_IDS` | Mac: 2 Codex, Claude, agy-current ready; PDS18: Codex + agy ready, live turns ([PDS](pds-codex-environment.md)); 10 parallel Codex on one account (outside coordinator) | Disabled/alias gates | Environment as first-class record (OS user/container); Windows host; second Claude account; agy second principal; binding credential kind/refresh owner | Environment is modeled as host, but the real boundary is the credential store (agy one principal per OS user) |
| B3 Sessions | Session distinct from execution/task; continuation and handoff across accounts/agents/env | `app.ts` sessions/messages/profile; `agent-tools.ts` transition/handoff/fork/resume/steer/queue/stop; `resume-argv.ts` | Codex steer, native fork, resume of fork; Claude native fork; handoff Codex→Claude→agy→Codex; Mac→PDS cross-env handoff ([inventory](agent-tools-inventory.md), [PDS](pds-codex-environment.md)); Codex resume with changed developer context | Queue order, ask resume, worker_done stale check | Claude resume with changed prompt/tools (D3); agy resume on final binary (D4); agy fork/steer; Claude steer (harness has none) | Old 409 pin replaced by transition policy, but stage3 D3 recommends no Claude `--resume` while handoff/fork code uses it |
| B4 Work mgmt | Projects, tasks with states, questions routed, results, completion vs creation vs publication | `projects.ts` (context, question_ask/answer, delivery_*), project UI | Two repo-backed projects, durable question answered, four deliveries (connected) | Wait/answer before generation | **Task object and task states** (queued/waiting/paused/cancelled); PM session creating tasks; project-rule acceptance; pause/cancel | Spec centers on tasks; prototype has deliveries and sessions only. "Created tasks continue without PM session" untested |
| B5 Pipelines/catalogue | Text-defined DAG, concurrent steps, failed-step blocking, rerun with reuse; catalogue + recommendations | `projects.ts` pipeline_run/get/history, `delivery.ts`, `delivery_worker.py`, `connection_call.py` | Two web searches; generation→integration→publication ×4; controlled publication failure | Slow provider ack, stable effect ids | Generic DAG, parallel steps, rerun reuse (UC4); pipeline exposed as agent tool via catalogue | Single hard-coded flow vs "structured text definitions"; program workers serial |
| B6 Lab | Runs, append-only assessments, recommendations with scope; one run mechanism with projects; budgeted iteration | Old lab removed in [Lab](designs/fleet-lab.md) Stage 0; `experiment`/`score` tables (migration 11), `/lab` rebuilt in Stage 3 | Old evidence removed with the old lab | Lab render fence | Variants beyond two fixed profiles; balancing settings; parallel trials; assessment→recommendation write path; budget loop (UC9) | Lab trials sequential and separate from project pipeline run (spec: one run mechanism); model/effort pinned blocks variants |
| B7 Agent tools | Agents find/launch agents, use env, create projects/tasks, run pipelines, read state | MCP `/api/hosts/agent-tools` (session-bound), tools: execution_status, ask, reply, handoff, session_transition, session_fork, resume, steer, stop; project tools separate MCP `/api/project-tools` | Native Codex called `execution_status`, `ask`; delegation `request_child` (continuation live) | All other tools | agent.list/prepare, environment.list, task create/track, pipeline run from native turn, catalogue/recommendation query from native turn | Three MCP endpoints (agent, project, recovery) instead of one surface |
| B8 Web = same ops | Interfaces add no operations of their own (R2) | Pages `/`, `/projects`, `/lab`, `/accounts`, `/recovery`; project and account UI call `/api/project-operations/*` | Owner browser used all pages | Browser fixtures | Session UI has no fork/handoff/steer/ask; lab/sessions use REST not shared operations | **Main session UI and lab use their own REST routes**; agent ops live under `/api/ops` (operator) and `/api/hosts/agent-tools` (bound agents). R2 holds for projects/accounts/recovery only |
| B9 Repos/worktrees/delivery | Several repos per project; result destinations; merge/publish/deploy tracked as operations | Mac integration worker (git push), PDS publication with versioned dirs | Four commits in two repos, four publications, browser/HTTP readback | Local bare git | Per-session worktree (in progress by other agent); merge/conflict/review; anonymous publication | Session dirs are flat, not worktrees (collision 8) |
| B10 Allocation | Select eligible host+agent; operator steers placement/concurrency; show waiting reasons | Host follows account binding (session's last host, default, extra hosts) | Mac→PDS host switch via binding | – | Owner balancing settings (prefer Codex, keep work off Mac, cap Windows); waiting reasons; queued re-evaluation | Placement is implicit by account, not an explicit decision point (D5) |
| B11 Recovery | Check original op before retry; unknown is not failed; no repeated external effect | `recovery.ts` (closure-sequence authorization), reconcile at startup, stop proven/unknown, context fork of unknown session | – (only fixture crash) | SIGKILL program worker, injected transport conflict, reconcile | Live executor crash/coordinator restart mid-turn; resolution path for unknown session other than fork | Recovery is a source-specific workaround, admitted as non-canonical |
| B12 Observability | Full history, usage, CPU/mem/disk, cost, attribution, missing visible | Events, differences, results, native frames, usage, leader CPU/RSS | Usage after reload, tool calls/results, screenshots | – | Child tree CPU/IO, per-task disk, cost, sum of counters, Codex child-agent accounting, harness version + actual tool list per run | Hidden Codex child agents break accounting (collision 1) |
| B13 Security | Permissions = project grant ∩ agent config ∩ task; agents never see credentials; history per project | Cloudflare Access + JWT; machine bearer per host; session-bound host tools; credential paths only | Bearer rotation; Access bypass for machine route; no credential env leaks | Origin/owner checks | Operator vs architect roles; project grants; per-project history access | Single owner role in practice; grants are per tool (`request_child`) not per project |

## 3. Use-case scenarios

| # | Scenario | Source | Status |
|---|---|---|---|
| UC1 | Create website page via PM → task → worker → review → publish | [use cases](use-cases.md) | **Partial**: deliveries, review child, publication live; no PM-created task, no acceptance rule |
| UC2 | Compare methods, recommend per use | use cases | **Partial**: two-variant lab live; recommendation write and agent query of it not run |
| UC3 | Failure/interruption: executor fail, lost contact, pause, cancel | use cases | **Partial**: stop and controlled failure live; lost contact/pause/cancel not run |
| UC4 | Pipeline with failed step and rerun reuse | use cases | **Not run** (no DAG) |
| UC5 | Qualify agent, use in project | use cases | **Partial**: profile qualification live; compatibility facts, grants not run |
| UC6 | Refine through conversation with rework | use cases | **Partial**: follow-up and delegated review→fix live |
| UC7 | Evaluate development approach (SDD variant) | use cases | Not run |
| UC8 | Delegated or event-initiated work, ask/reply | use cases | **Partial**: delegation child and native `ask` live; event trigger not run |
| UC9 | Budgeted iterative improvement | use cases | Not run |
| UC10 | Analyze project operations | use cases | Not run |
| UC11 | Balance a distributed fleet | use cases | **Partial**: Mac+PDS hosts live; no balancing settings |
| S1 | Interactive session: steer, queue, fork, resume | inventory | **Proven live** (Codex); Claude fork live |
| S2 | Cross-harness/cross-env handoff | inventory, PDS | **Proven live** |
| S3 | Second account routing, parallel accounts | second-codex, isolation | **Partial**: parallel outside coordinator only |
| N1 | Parallel sessions through real coordinator (tens) | *new, needed* | Not run |
| N2 | Agent-driven orchestration: a native PM agent launches, tracks and collects workers only via Fleet MCP | *new, needed* | Not run |
| N3 | Project work in per-session worktrees with merge back | *new, needed* | Not run |
| N4 | Live crash recovery mid-turn (executor kill, coordinator restart) | *new, needed* | Not run |
| N5 | Lab with N variants (harness/model/profile) under owner balancing setting, parallel | *new, needed* | Not run |
| N6 | Account exhausted/logged out mid-work → question → handoff to other account | *new, needed* | Not run |
| N7 | Web operator does the same flow as N2 via the same operations | *new, needed* | Not run |
| N8 | Computer-use agent in its own environment | *new, needed* | Not run |

## 4. Minimal real end-to-end tests to freeze the architecture

Ordered by value. "Turns" are native model turns. All reuse existing operations where they exist; where an operation is missing the test is the forcing function to add it minimally.

### T1. Agent-orchestrated project work (B1 B3 B4 B7 B9 B12 B13)
- Goal: prove an agent can do the operator's job through Fleet MCP only (R2, N2).
- Setup: PM profile (Codex, Mac) with Fleet MCP; project `hello-world`; one Claude worker profile, one Codex reviewer profile; per-session worktrees.
- Steps: PM reads `project_context` → starts a worker session (Claude, Mac) → worker edits in its worktree → PM `execution_status` until done → PM starts reviewer (Codex, PDS) → reviewer `ask` → PM `reply` → fix → PM triggers delivery/publication.
- Pass: every action is a Fleet tool call in history with caller; separate worktrees and accounts; publication readback; full history after reload; no operator UI action besides the first message.
- Turns: ~8–12.

### T2. Same flow from the web (B8)
- Goal: prove the web uses the same operations (N7).
- Setup: as T1.
- Steps: operator performs start worker, fork, handoff, steer, ask reply and delivery from the UI.
- Pass: each UI action hits the same handler as the agent tool (one route set); identical records as T1. Any UI-only route is a recorded R2 violation.
- Turns: ~6.

### T3. Concurrency through the real coordinator (B2 B10 B12)
- Goal: actual scale behavior, not a cap (N1, isolation tests 1–2).
- Setup: Mac and PDS executors; two Codex accounts on Mac, one on PDS; cheap one-line turns.
- Steps: 25, then 50 sessions, split across accounts/hosts; then 2 parallel Claude (if second Claude account exists).
- Pass: all reach terminal state or show observed vendor error; no cross-account rollout; p50/p95 latency, executor RSS/fd, 429 counts recorded; queue/receipt ordering correct.
- Turns: ~75–100 (cheap).

### T4. Lab with variants and balancing setting (B1 B6 B10 B5)
- Goal: lab uses the same run mechanism with multiple variants (N5).
- Setup: 4 variants: Codex low/medium, Claude, agy, same task; owner setting e.g. "agy max 1 concurrent, prefer PDS for Codex".
- Steps: run variants in parallel; record assessment; write a recommendation; a project worker queries it and follows it.
- Pass: per-variant model/effort actually applied (harness version, tool list recorded); setting visibly applied with reasons; recommendation read by agent through MCP.
- Turns: ~6–8.

### T5. Live crash and unknown (B11 B3 B12)
- Goal: real reliability claim (N4, UC3).
- Setup: one long Codex turn on Mac, one program publication on PDS.
- Steps: kill executor mid-turn, restart; restart coordinator mid-publication; then fork the unknown session to continue.
- Pass: no native relaunch, no repeated publication; state unknown with last-known shown; reconcile result recorded; fork continues with context.
- Turns: ~3.

### T6. Resume qualification for Claude and agy (B3 B1)
- Goal: close D3/D4.
- Steps: Claude session, change system prompt/tools, `--resume`; agy ACP resume on final binary with changed profile.
- Pass: changed instructions/tools observably applied and context retained; otherwise freeze "handoff instead of resume" for that harness.
- Turns: ~4.

### T7. Account failure and handoff (B2 B3 B4)
- Goal: N6; account unavailability becomes a question and continuation.
- Steps: disable/logout one Codex binding during a queued follow-up; owner answers question by choosing another account; handoff continues.
- Pass: binding `login-absent`, question routed, handoff delivered, no lost messages.
- Turns: ~3.

### T8. Isolation markers (B1 B2 B13)
- Goal: close collisions 1, 4, 7.
- Steps: Claude and agy runs with marker in ancestor `~/.claude/CLAUDE.md`, `~/.claude/skills`; Codex run asked to spawn a child agent.
- Pass: no marker seen; child agent recorded with account and accounted, or the profile choice disables it.
- Turns: ~3.

Not needed to freeze (deferred to V1): generic DAG engine performance, Windows host (needs only T3-style check once provisioned), computer use (N8) beyond a single design check that environment = credential store + GUI.

## 5. Open decisions for the owner

1. **Task object**: introduce tasks with states now (spec) or keep sessions+deliveries for V1?
2. **One surface**: merge agent, project, recovery tools and web REST into one operation set (recommended) — confirm.
3. **Resume policy**: per harness "resume if qualified, else handoff" (after T6).
4. **Environment = credential store** as a first-class record (OS user/container), needed for second agy account and computer use.
5. **Codex collaboration tools**: accept and account child agents, or forbid via profile when possible.
6. **Placement**: explicit host+account choice vs owner balancing settings (which settings exist: per-host concurrency, harness preference, per-account share).
7. **Pipelines**: generic text DAG in V1 or only the delivery flow plus agent-driven orchestration.
8. **Auth choices** from [resources and auth](designs/fleet-people-access.md#4-subscriptions): Claude setup-token vs per-env login; WSL eligibility; Codex `auth.json` seeding.
9. **Roles**: operator/architect separation and project grants in V1 or single owner.
10. **Publication**: anonymous test-site destination.

## 6. After the freeze (outline)

1. **Tech/infra decisions**: keep or replace SQLite single-coordinator, polling vs streaming, executor packaging per OS, environment provisioning (OS users/containers on Mac/PDS/Windows), secrets layout, observability store, CI and release flow.
2. **Final software architecture**: module map (coordinator domains: config, sessions, work, pipelines, lab, allocation, recovery, observation; executor: adapters per harness, environment manager, worktree manager), one operation contract with generated MCP+web clients, data model, failure model.
3. **V1 plan**: rebuild prototype paths onto the final modules in vertical slices (sessions+agent tools → tasks/projects → delivery/worktrees → lab/recommendations → allocation settings → recovery), each closing with the matching T1–T8 test re-run as acceptance.

## 7. Project setup and access tests (added)

From [project setup and access](configuration.md#project-setup). Real environments, no secret values in records or logs.

### T9. Project readiness (B4 B2 B13)
- Setup: project with one repo, a deploy-key or `gh` connection on Mac and PDS, worker profile bound to Codex accounts on both.
- Steps: run `project_readiness`; then remove the credential path on PDS and rerun.
- Pass: first run all `ok`; second run reports PDS repo access `failed` with reason and a question to the operator; no secret or raw stderr in output.
- Turns: 0.

### T10. Agent commit and push with project identity (B1 B4 B13)
- Steps: worker session in its worktree edits a file, commits, pushes a branch; readback via `ls-remote`.
- Pass: commit author = project identity; push used the project connection (not ambient); branch readback matches; worktree removed after.
- Turns: ~2.

### T11. Cross-project credential separation (B2 B13)
- Setup: two projects on one environment with different repo connections.
- Steps: session of project A tries `git ls-remote` on project B's repo.
- Pass: refused for lack of credential; A's process env contains only A's connection paths.
- Turns: ~1.

## 8. Project settings tests (added)

From [project settings matrix](configuration.md#project-settings). Real repos `taskqorg/fleet-hello-world*`, real executors, settings only from `project.json`.

### T12. Worktree setup script runs a build (B1 B13)
- Setup: repo with `scripts.setup` = install + build that writes `dist/marker`; `setupStart: wait-for-setup`; `startFromOrigin: true`.
- Steps: start a worker session; worker reads `dist/marker`.
- Pass: worktree base = fresh `origin/<default>`; setup ran once in the worktree with `FLEET_WORKTREE_PATH`; agent started after setup exit; setup exit/output recorded; `setup: skip` start has no marker.
- Turns: ~1.

### T13. Teardown hook on worktree removal (B1 B13)
- Setup: `scripts.teardown` writes a marker outside the worktree; `cleanup.keepWhen: uncommitted-changes`.
- Steps: finish a session with committed work, then one with an uncommitted file.
- Pass: first worktree: teardown ran, worktree removed, branch deleted only if merged; second worktree kept with reason recorded.
- Turns: ~2.

### T14. Per-project merge policy (B1 B4 B13)
- Setup: `fleet-hello-world` with `integration.mode: pr`, `mergeMethod: squash`; `fleet-hello-world-alt` with `mode: direct`.
- Steps: same small task delivered in both projects.
- Pass: first: branch pushed, PR opened, merged by squash (one commit on main); second: commit pushed to default branch, no PR; both with project identity.
- Turns: ~2.

### T15. Main checkout updated after merge (B1 B13)
- Setup: `syncCheckoutAfterMerge: true`.
- Steps: after T14 merge, start a new session in the same project.
- Pass: environment checkout fast-forwarded to merged main; new worktree contains the merged change; local changes in the checkout, if any, are not overwritten (sync recorded as `failed`).
- Turns: ~1.

### T16. Linked ignored files (B1 B13)
- Setup: checkout has ignored `.env.local` and `node_modules/`; `linkPaths: [".env.local"]`, `sharedDirectories: ["node_modules"]`.
- Steps: start a session; worker reads `.env.local` presence and runs a node script.
- Pass: both present in the worktree without copy-install; nothing from them in records or logs.
- Turns: ~1.
