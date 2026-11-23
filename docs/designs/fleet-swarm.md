# Fleet Swarm

Universal cross-harness multi-agent coordination over Fleet's shared core ([R2, R7](../../fleet.md#principles)). Under **Variant C**, Fleet Swarm adds a thin durable coordination layer (`work_plan` and `board_message`) directly over Fleet's existing `task`, `pipeline_run`, `session`, and `execution` primitives rather than duplicating entity or membership tables. A Swarm coordinator decomposes a goal into structured threads and disjoint worker tasks across any mix of Fleet harnesses (`codex`, `claude`, `antigravity`, `acp`), coordinates via a durable shared message board, and surfaces live progress in Fleet's web app and Lab.

Fleet ships the shared board, mention-driven wakes, the `swarm-coordinator` profile, the `swarm-plan-execute` pipeline and Swarm views. The live task [T-FLT-22](https://fleet.ufobe.com/tasks/486357fa-2fe2-4d31-95d9-14a90b005574) showed a task-scoped board with a Codex coordinator, a Codex implementation worker and a Claude verifier. The task was in progress and a board wake was queued. Completion and other harness pairings require their own evidence.

## External-agent quickstart

Use an ordinary task for project work. Use the `swarm-plan-execute` pipeline in [Lab](fleet-lab.md) to evaluate Swarm variants on examples. Lab boards use `scopeKind: "pipeline_run"` with the run ID. The task launch below starts a coordinator session and uses the task ID for the shared board.

### Authenticate and discover the profile

Use Cloudflare Access credentials issued for your Fleet identity. For a service token, send `CF-Access-Client-Id` and `CF-Access-Client-Secret` only to the Fleet origin. Send `Origin: https://fleet.ufobe.com` on POSTs to the deployed site. Fleet records the authenticated person as `directedBy` on the task and coordinator session. Use an identity authorized to create and release tasks and start sessions in the target project. Delegation stays within that director's grants. [Agent access](fleet-people-access.md#agent-access-to-the-web-app)

- **Operations API.** Send JSON to `POST /api/ops/<operation>` with `Content-Type: application/json`. Call `me {}` to confirm your identity and `project_list {}` to find the project ID.
- **MCP.** Connect to `https://fleet.ufobe.com/api/tools` over HTTP with the same authentication and origin headers. Use `tools/list` for schemas and `tools/call` for calls. Both transports accept the operation arguments below. Operation errors can return a non-2xx HTTP status. For example, `execution_status` without `executionId` returns HTTP 400. After checking the status, check the JSON-RPC response for errors before decoding the successful operation reply from `result.content[0].text` as JSON.
- **Profile.** Call `session_presets {"projectId":"<project-id>"}` for project presets, resolved profiles and unavailability reasons. With architect access, call `profiles_list {}` to find the installed `swarm-coordinator` profile. Confirm authorization and host readiness separately. Fleet-hosted callers use their execution's bound tools, where project role restrictions also apply.

For example, POST this JSON-RPC request to `/api/tools` to confirm the calling identity.

```json
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"me","arguments":{}}}
```

### Create, release and start

Use the same project throughout. Replace placeholders with your IDs. Generate a unique marker for this task create and a request ID unique to this session start within the project.

1. Call `task_create` to create a Held task. Save the response's `id` as the task ID.

   ```json
   {"projectId":"<project-id>","title":"Check the project documentation links [<create-marker>]","expectedOutcome":"Report broken links with file paths and verification results.","held":true}
   ```

2. After the director authorizes the start, call `task_release` using the authorized identity.

   ```json
   {"taskId":"<task-id>"}
   ```

3. Call `session_start` and save the returned `sessionId` and first-turn `executionId`. Include the goal, acceptance requirements and task board scope in the first-turn text.

   ```json
   {"projectId":"<project-id>","profileId":"swarm-coordinator","taskId":"<task-id>","requestId":"<start-request-id>","text":"Check the project documentation links. Keep the plan and all posts on scopeKind task, scopeId <task-id>. Report broken links with file paths and verification results. Leave repository files unchanged."}
   ```

The example defaults to a write workspace with a branch. The prompt asks the coordinator to leave repository files unchanged. To enforce read-only workspace access, add `"access":"read","branch":false` to the start arguments. A read-only coordinator can start only read-only child sessions.

- **Uncertain create.** `task_create` has no idempotency key. Read `task_list {"projectId":"<project-id>"}` and reuse a task ID only if you can identify the task unambiguously by its marker and create details. Multiple matches or an empty read while the original request may be in flight leave the outcome unresolved. Resolve that ambiguity before releasing a task or repeating create. Repeat create only once you have confirmed the original request ended without creating a task.
- **Uncertain release.** Read `task_get {"taskId":"<task-id>"}` and retry release only if the task still has `state: "paused"` and `pauseReason: "held"`.
- **Uncertain start.** Retry `session_start` with the same project, request ID and payload. A replay returns the existing `sessionId` with `existing: true` and omits `executionId`. Call `session_get {"sessionId":"<session-id>"}` and use the `executionId` on the message whose `requestId` is `start:<start-request-id>`. Later turns in the same session have their own execution IDs.

### Read back progress and completion

- **Accepted.** A fresh successful start with the example's nonempty `text` returns `sessionId` and `executionId`. Fleet creates that execution in `preparing` before a host is assigned, so you can read its status and readiness before placement.
- **Preparing or running.** Call `execution_status {"executionId":"<execution-id>"}` for the specific coordinator or member turn. `preparing` covers launch preparation. `running` with `observation: "confirmed"` shows that the turn is running. Inspect `readiness`, `reason` or `report` when present. Treat `unconfirmed` as unknown.
- **Board progress.** Call `work_board_get {"scopeKind":"task","scopeId":"<task-id>"}`. Read `plan`, `roster`, `messages` and `summary` for the plan, linked sessions and executions, reports and blockers. The task board can be empty until the coordinator writes its plan or first post.
- **Page through Board posts.** The first read returns up to 200 messages by default, earliest first. For each next page, set `since` to the last message's `seq`. For example, if a page ends at sequence 2, call `work_board_get {"scopeKind":"task","scopeId":"<task-id>","since":2,"limit":2}` to read the next two posts. Continue until the last sequence read reaches `headSeq` from the latest reply. Keep that cursor for later reads to pick up new posts, including the final reports.
- **Complete.** `execution_status` can return `succeeded` for one turn while the task is still in progress. Read the final board reports and verify the retained result and required checks. Read `task_get {"taskId":"<task-id>"}` for the result, acceptance and task state. Fleet records task completion as `completed`.

### Inspect the task in Fleet

Open **Tasks**, select the task and choose **Swarm** once the board has a plan, posts or roster entries. **Overview** shows the plan and rollups, **Board** shows posts, **Roster** shows the members, and **Timeline & Screenshots** shows milestones and attached screenshots. Reloading the task reselects **Details**. In **Overview**, the **active workers** count includes an active coordinator. Use **Roster** to see each member's role and state.

In **Roster**, select the conversation code in the **Transcript** column, then select **Open** on its card to reach that member's transcript in **Conversations**. A conversation with a populated session board has its own **Swarm** tab, keyed to the session ID. A coordinator can have an empty session board while its task board is active. Use **Tasks → Swarm** for the task board.

### Independent verification

On T-FLT-22 the default Claude Swarm verifier (`swarm-verifier-claude`) could inspect files and use Fleet tools. Its effective grants offered no shell or browser checks, so its baseline left those checks unverified.

The project's authorized independent review route requires a separate reviewer with read access to the frozen revision and a host and profile with the required project-granted tools. T-FLT-22 recorded browser and forge readiness for the `coding-worker` grant on `owner-mac`. That placement offered a way to run candidate checks in an authorized workspace. Independent verification still requires the reviewer to run the checks and retain their results. If access, readiness or check evidence is missing, record the gap on the board. [Fleet delivery review](fleet-delivery-pipeline.md#8-workers-merge-and-watch-their-own-pull-requests)

## 1. Purpose

- **One coordination layer across every harness.** Vendor-specific swarm or subagent features tie execution to a single CLI or interactive terminal and cannot mix harnesses, share durable Fleet state, or run controlled comparisons in Lab. Fleet Swarm runs homogeneous or mixed teams (`codex`, `claude`, `antigravity`, `acp`) under one protocol, identity, and accounting model.
- **Role-driven, not a separate daemon.** A Swarm coordinator is an ordinary agent execution configured with coordinator instructions (`config/agents/swarm-coordinator.json`), Fleet tools, and [`codeMode`](fleet-code-mode.md) — never a separate hardcoded broker (`fleet.md` R6).
- **Thin Variant C core (`R2`, `R7`).** `work_plan` stores versioned thread and task decomposition (`threads[]` / `tasks[]` linking `ownerMember`, `sessionId`, `executionId`, `workspaceMode`, and `status`) scoped to `(scopeKind, scopeId)` (`task`, `pipeline_run`, or `session`). `board_message` stores an append-only per-scope sequence-ordered feed. Live member state, harness, model, host, durations, tokens, and multi-currency cost come straight from joining `session`, `execution`, `execution_totals`, and `execution_spend` in `work_board_get`, with zero duplicated state machines.

## 2. Plan-driven coordinator and configurable strategies

A coordinator receives a goal and runs a **plan-driven Swarm workflow** governed by a versioned agent profile (`swarm-coordinator`) and pipeline definition (`swarm-plan-execute`):

1. **Plan (`work_plan_set`).** Decompose the goal into structured `threads[]` and disjoint worker `tasks[]` (explicit `ownedPaths`, `workspaceMode`, `role`, `harness`/`model` hints, dependencies, and stop conditions) under `(scopeKind, scopeId)`. Each update atomically increments `work_plan.version` (with optional `expectedVersion` optimistic concurrency).
2. **Spawn & link (`session_start` / `session_fork` -> `work_plan_set`).** Start worker and independent verifier sessions with the appropriate workspace isolation and link their `sessionId` / `executionId` back into `work_plan`.
3. **Dispatch & coordinate (`board_post` / `board_read`).** Post briefs, progress updates, blockers, screenshots, and `[done]` reports with `@<member>` or `@all` mentions; workers and coordinators read incrementally by `since` cursor without polling.
4. **Inspect & verify (`work_board_get`).** Read `{ plan, roster, messages, summary }` in one consistent snapshot joining live `session`, `execution`, `execution_totals`, and `execution_spend` before accepting deliverables.

Coordinators tune decomposition and delegation via versioned strategy instructions and pipeline parameters:

| Strategy knob | What it configures |
|---|---|
| **Sizing & sharding** | Fixed pool, task-sharded workers across `threads[].tasks[]`, or adaptive scaling within project/host concurrency and budget limits. |
| **Verifier ratio & gates** | Independent verifiers per N authors (e.g., 1:1 or 1:3), cross-harness verification rules (such as Codex author + Claude verifier), and required checks before landing. |
| **Stall monitoring** | Optional monitor role or coordinator `watch`/timer cursor to detect stalled workers, failing checks, or unanswered blockers without busy-polling. |
| **Workspace isolation** | Per-task `workspaceMode`: `inherit` (shared read-only workspace for reviewers/verifiers), `share` (dedicated working copy per concurrent author), or `branch` (isolated Git branch merged or PR-landed before cleanup). |
| **Per-worker specialization** | Per-role `harness`, `model`, `codeMode` (`add`/`only`), `tools`, `connections`, `skills`, and tailored instructions. |

Delegation never expands authority: every coordinator and worker call stays inside the execution's `directedBy` grants and project scope ([PM agent](fleet-pm-agent.md#1-rights-director-never-the-profile)).

## 3. Shared board and structured reporting

- **Durable shared board (`R2`, `R7`).** `work_plan` and `board_message` persist in PostgreSQL under `project_id` and Fleet's standard `live` + `project_scope` (`fleet_visible(project_id)`) row-level security, emitting `board` change events on `fleet_change` for live SSE updates. Agents and the operator UI read and write through the same operations (`work_plan_set`, `board_post`, `board_read`, `work_board_get`).
- **Channels and addressing.** Messages carry a per-scope monotonic `seq` (allocated under `board:<scopeKind>:<scopeId>` advisory lock), `threadId` channel scope, explicit + parsed `@mentions`, and resolved `wakeTargets` (matching member names to `sessionId` from `work_plan` and prior posters):
  - `@<member>` targets one sleeping worker or coordinator (`wakeTargets`).
  - `@all` targets every active member with a `sessionId` (excluding the sender) for shared rules, interface contracts, or plan changes (never status chatter).
  - Unaddressed posts stay on the feed for the next natural `board_read` (`since` cursor) without interrupting active turns.
- **Event-driven wake without polling.** A waiting worker holds no active model turn and consumes zero tokens. It tracks its `since` sequence cursor (the last message it actually read, not the board head) and resumes on matching `@mention`, `@all`, dependency completion, or watch timer.
- **Structured worker reports.** Messages carry a typed `kind` (`brief`, `message`, `progress`, `blocker`, `done`, `screenshot`), optional `artifactId` link, and open `payload` alongside prose so both the coordinator and the GUI read state without scraping logs:
  - `progress`: active thread/sub-task, phase, and touched files or paths.
  - `blocker`: unmet dependency, failing check, or decision needed — routed into Fleet's `question` flow with addressee and release condition.
  - `done`: final summary, changed artifacts/PR/branch reference, and verification result.
  - `screenshot`: browser, desktop (CUA), or render captures saved as Fleet `artifact` records and attached via `artifactId`.

## 4. Visual Swarm GUI in Fleet

The shipped task view is described in [Inspect the task in Fleet](#inspect-the-task-in-fleet). The table below describes the target panels.

All Swarm views live inside Fleet's closed **8-section IA** ([IA and journeys](fleet-ia-and-journeys.md#1-closed-8-section-information-architecture)) and follow `OR-1`–`OR-49` using `packages/ui` controls exclusively, backed directly by `work_board_get` and `board_read`:

- **No new top-level section and no second shell bar (`OR-46`, `OR-47`).** Swarm views open inside the Work Area (`detail` slot of `ListDetail`) for:
  - a Swarm-backed task in **Tasks** (via a `TabBar` facet, e.g. `Swarm`),
  - a coordinator session in **Conversations**, or
  - a Swarm trial in **Lab**.
  - Active Swarm blockers (`blocker` / pending `question`) surface in **Overview** (`Needs you`) via existing `ProblemRow` / question routing.

| View panel | What it shows | `packages/ui` controls |
|---|---|---|
| **Swarm Overview** | Goal, decomposed `work_plan` threads and tasks, aggregate `StatStrip` / `Totals`, harness breakdown, and per-run/per-thread progress & spend from `summary`. | `DetailSection`, `StatStrip`, `Totals`, `HarnessCounts`, `StateBadge`, `TimeStrip`, `CostFigures` |
| **Member Roster** | Live `roster` entries (coordinator, workers, verifiers, monitor) with state first (`OR-22`), `EntityCode` (`sessionCode`), `HarnessMark` + model, `workspaceMode` (`inherit`/`share`/`branch`), `threadId`, duration/tokens/cost, and `lastPostSeq`. | `DataTable` / `VirtualList`, `StateBadge`, `EntityCode`, `HarnessMark`, `Ago` |
| **Filtered Message Board** | Real-time durable `board_message` feed filterable by `threadId`, `authorMember`, `@mention`, and `kind` (`brief`, `message`, `progress`, `blocker`, `done`, `screenshot`). | `Thread`, `FilterBar`, `SearchField`, `ResultRow` |
| **Dependency & Handoff Graph** | Parent-child spawning, `dependsOn` task dependencies, and peer handoffs (author → verifier → land) without raw UUIDs (`OR-38`). | `ExecTree`, `FlowStrip`, `StepList` |
| **Screenshot Timeline** | Chronological strip of `screenshot` board messages and linked `artifactId` captures for visual inspection. | `TimeStrip`, `ArtifactView`, `ResultViewer`, `EntityCode` |

## 5. Synergy with Universal Code Mode and Lab evaluation

- **Universal Code Mode across harnesses ([fleet-code-mode.md](fleet-code-mode.md)).** Coordinators and workers run `codeMode` (`add` or `only`) across `codex`, `claude`, `antigravity`, and `acp`. `swarm-coordinator` uses `codeMode: "only"` so a coordinator executes one `code` call to set `work_plan_set`, spawn heterogeneous workers in parallel (`Promise.all`), query unread board posts (`board_read` with `since`), inspect `work_board_get`, and return only the synthesized outcome into model context.
- **Versioned pipeline and profile comparison in Lab ([fleet-lab.md](fleet-lab.md)).** Because `swarm-coordinator` (`config/agents/swarm-coordinator.json`) and `swarm-plan-execute` (`config/pipelines/swarm-plan-execute.json`) are ordinary versioned Fleet definitions, Fleet Lab compares Swarm variants (single-agent `coding-task` / `coding-task-reviewed` baselines vs `swarm-plan-execute` topologies, verifier ratios, `codeMode` modes, and homogeneous vs mixed harness pairings) on the exact same use-case suites across four axes:
  1. **Quality:** evaluator pass rate, check score, and review defect catch rate.
  2. **Latency:** end-to-end wall time and critical-path step duration.
  3. **Token and cost efficiency:** coordinator + aggregate worker input/cached/output tokens and `CostFigure` spend.
  4. **Progress observability:** structured `board_message` report coverage, board signal-to-noise ratio, and `artifactId` screenshot completeness.
- Proven Swarm profiles and pipeline configurations promote from Lab into `Library → Methods` (`experimental` → `recommended`) through the standard `J3` workflow ([IA and journeys](fleet-ia-and-journeys.md#3-core-user-journeys-j1j5)).

## 6. Incremental delivery slices

1. **Slice 1 — Shared core board and coordination primitives (shipped):** Variant C `work_plan` and `board_message` tables (migration 89) with `live` + `project_scope` RLS and `fleet_change` triggers (`board` kind); `work_plan_set`, `board_post` (monotonic `seq` under `board:*` lock, `@mention` parsing, `wakeTargets` resolution), `board_read` (`since` cursor & filters), and `work_board_get` (live `session`/`execution`/`execution_totals`/`execution_spend` roster join + summary counts); starter `swarm-coordinator` agent profile and `swarm-plan-execute` pipeline.
2. **Slice 2 — Cross-harness Code Mode, wake delivery, and Swarm qualification:** `board_post` submits mention wakes to eligible sessions and reports `woken` and any `wakeError` per target. A busy session can queue a wake. Use `wake: false` for posts that only need to appear on the board. The qualification target covers `codeMode` (`add`/`only`) and Swarm tools across `codex`, `claude`, `antigravity`, and `acp`, including `inherit`/`share`/`branch` isolation, per-worker profile and tool scope, and mixed-harness handoffs.
3. **Slice 3 — Fleet Swarm GUI:** the shared panel provides `Overview`, `Board`, `Roster` and `Timeline & Screenshots` under the matching task, session or pipeline run scope. The target panels are listed in [Visual Swarm GUI in Fleet](#4-visual-swarm-gui-in-fleet).
4. **Slice 4 — Lab comparative benchmarks:** parameterized Swarm strategy variants in Lab comparing single-agent baselines vs `swarm-plan-execute` variants, `codeMode` modes, and harness pairings on quality, latency, token/cost efficiency, and observability, ready for `Promote` to `Library → Methods`.
