# Fleet Swarm

Universal cross-harness multi-agent coordination over Fleet's shared core ([R2, R7](../../fleet.md#principles)). Under **Variant C**, Fleet Swarm adds a thin durable coordination layer (`work_plan` and `board_message`) directly over Fleet's existing `task`, `pipeline_run`, `session`, and `execution` primitives rather than duplicating entity or membership tables. A Swarm coordinator decomposes a goal into structured threads and disjoint worker tasks across any mix of Fleet harnesses (`codex`, `claude`, `antigravity`, `acp`), coordinates via a durable shared message board, and surfaces live progress in Fleet's web app and Lab.

**Shipped vs target:** Fleet's currently shipped runtime provides parent-child `session_start`, `session_fork`, `session_transition`, `execution_status`, `request_child`, completion `wake()`, PM `watch` mode, universal [`codeMode`](fleet-code-mode.md), and **Slice 1** (`work_plan` and `board_message` PostgreSQL tables, `work_plan_set`, `board_post`, `board_read`, `work_board_get` operations/MCP tools, and starter `swarm-coordinator` agent profile + `swarm-plan-execute` pipeline). **Not built:** automatic wake delivery for `board_post` `wakeTargets`, cross-harness Swarm qualification (Slice 2), Swarm GUI views (Slice 3), and Lab Swarm benchmark suites (Slice 4).

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

**Not built:** Swarm UI components and routes.

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
2. **Slice 2 — Cross-harness Code Mode, wake delivery, and Swarm qualification:** deliver `board_post` `wakeTargets` into sleeping sessions cleanly, and verify `codeMode` (`add`/`only`) + Swarm board/delegation tools across `codex`, `claude`, `antigravity`, and `acp`, including `inherit`/`share`/`branch` workspace isolation, per-worker profile/tool scoping, and mixed-harness handoffs.
3. **Slice 3 — Fleet Swarm GUI:** `Swarm` Work Area views (`Overview`, `Roster`, `Board`, `Handoff Graph`, `Screenshot Timeline`) inside `Tasks`, `Conversations`, and `Lab` using `packages/ui` under `OR-1`–`OR-49` and verified by live Playwright checks.
4. **Slice 4 — Lab comparative benchmarks:** parameterized Swarm strategy variants in Lab comparing single-agent baselines vs `swarm-plan-execute` variants, `codeMode` modes, and harness pairings on quality, latency, token/cost efficiency, and observability, ready for `Promote` to `Library → Methods`.
