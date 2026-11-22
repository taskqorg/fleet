# Fleet Swarm

Universal cross-harness multi-agent coordination over Fleet's shared core ([R2, R7](../../fleet.md#principles)). A Swarm coordinator decomposes a goal into structured threads and disjoint worker assignments across any mix of Fleet harnesses (`codex`, `claude`, `antigravity`, `acp`), coordinates via a durable shared message board with event-driven wakes, and surfaces live progress in the Fleet web app.

**Shipped vs target:** Fleet's currently shipped runtime provides parent-child `session_start`, `session_fork`, `session_transition`, `execution_status`, `request_child`, completion `wake()`, PM `watch` mode, and universal [`codeMode`](fleet-code-mode.md). **Not built:** first-class Swarm runs, configurable Swarm strategy profiles, durable shared board tables/tools, structured worker report payloads, the Swarm GUI views, and Lab Swarm benchmark suites described below.

## 1. Purpose

- **One coordination layer across every harness.** Vendor-specific swarm or subagent features tie execution to a single CLI or interactive terminal and cannot mix harnesses, share durable Fleet state, or run controlled comparisons in Lab. Fleet Swarm runs homogeneous or mixed teams (`codex`, `claude`, `antigravity`, `acp`) under one protocol, identity, and accounting model.
- **Role-driven, not a separate daemon.** A Swarm coordinator is an ordinary agent execution configured with coordinator instructions, Fleet tools, and [`codeMode`](fleet-code-mode.md) — never a separate hardcoded broker (`fleet.md` R6).
- **Controlled and observable (`R5`, `R7`).** Every Swarm run, thread, board post, handoff, tool call, screenshot, and token cost lives in Fleet's shared core so agents, operators, and Lab evaluators read the exact same state (`R2`).

## 2. Coordinator and configurable algorithms

A coordinator receives a goal and breaks it into **structured threads** and **disjoint worker tasks** (explicit owned files or targets, non-overlapping write scopes, clear done conditions, and time boxes) according to a configurable **Swarm strategy profile** (versioned as a Fleet definition and comparable in Lab; **not built**):

| Strategy knob | What it configures |
|---|---|
| **Sizing & sharding** | Fixed pool (`n`), task-sharded workers (`tasks` slice list), or adaptive scaling within project/host concurrency and budget limits. |
| **Verifier ratio & gates** | Independent verifiers per N authors (e.g., 1:1 or 1:3), cross-harness verification rules (such as Codex author + Claude verifier), and required checks before landing. |
| **Stall monitoring** | Optional monitor role or coordinator `watch`/timer cursor to detect stalled workers, failing checks, or unanswered blockers without busy-polling. |
| **Workspace isolation** | Per-worker mode: `inherit` (shared workspace for read-only reviewers/verifiers), `share` (dedicated working copy per concurrent author), or `branch` (isolated Git clone/branch merged or PR-landed before cleanup). |
| **Per-worker specialization** | Per-role `harness`, `model`, `codeMode` (`add`/`only`), `tools`, `connections`, `skills`, and tailored instructions. |

Delegation never expands authority: every coordinator and worker call stays inside the execution's `directedBy` grants and project scope ([PM agent](fleet-pm-agent.md#1-rights-director-never-the-profile)).

## 3. Shared board and structured reporting

**Not built:** durable Swarm board storage and MCP/API operations.

- **Durable shared board (`R2`, `R7`).** Swarm threads and board messages persist in PostgreSQL under the parent `task_id` / `swarm_id` and project row-level security. Agents and the operator UI read and write through the same operations.
- **Channels and addressing.** Messages carry sequence numbers (`seq`), optional `threadId` / channel scope, and explicit wake targets:
  - `@<member>` wakes one sleeping worker or coordinator.
  - `@all` wakes the whole Swarm for shared rules, interface contracts, or plan changes (never status chatter).
  - Unaddressed posts stay on the feed for the next natural wake without interrupting active turns.
- **Event-driven wake without polling.** A waiting worker holds no active model turn and consumes zero tokens. It registers a `since` sequence cursor (the last message it actually read, not the board head) and wakes immediately on matching `@mention`, `@all`, dependency completion, or watch timer.
- **Structured worker reports.** Workers emit typed report payloads alongside concise prose so both the coordinator and the GUI can parse state without scraping logs:
  - `progress`: active thread/sub-task, phase, and touched files or paths.
  - `blocker`: unmet dependency, failing check, or decision needed — routed into Fleet's `question` flow with addressee and release condition.
  - `[done]`: final summary, changed artifacts/PR/branch reference, and verification result.
  - `screenshot`: browser, desktop (CUA), or render captures saved as Fleet `artifact` records and attached to the board message and timeline.

## 4. Visual Swarm GUI in Fleet

**Not built:** Swarm UI components and routes.

All Swarm views live inside Fleet's closed **8-section IA** ([IA and journeys](fleet-ia-and-journeys.md#1-closed-8-section-information-architecture)) and follow `OR-1`–`OR-49` using `packages/ui` controls exclusively:

- **No new top-level section and no second shell bar (`OR-46`, `OR-47`).** Swarm views open inside the Work Area (`detail` slot of `ListDetail`) for:
  - a Swarm-backed task in **Tasks** (via a `TabBar` facet, e.g. `Swarm`),
  - a coordinator session in **Conversations**, or
  - a Swarm trial in **Lab**.
  - Active Swarm blockers (`blocker` / pending `question`) surface in **Overview** (`Needs you`) via existing `ProblemRow` / question routing.

| View panel | What it shows | `packages/ui` controls |
|---|---|---|
| **Swarm Overview** | Goal, decomposed threads and sub-tasks, aggregate `StatStrip` / `Totals`, harness breakdown, and per-run/per-thread progress & spend. | `DetailSection`, `StatStrip`, `Totals`, `HarnessCounts`, `StateBadge`, `TimeStrip`, `CostFigures` |
| **Member Roster** | Coordinator, workers, verifiers, and monitor with state first (`OR-22`), human entity code (`OR-38`), `HarnessMark` + model, workspace mode (`inherit`/`share`/`branch`), current thread, and compact age (`OR-2`). | `DataTable` / `VirtualList`, `StateBadge`, `EntityCode`, `HarnessMark`, `Ago` |
| **Filtered Message Board** | Real-time durable board feed filterable by thread/channel, member, `@mention`, and report kind (`progress`, `blocker`, `[done]`, `screenshot`). | `Thread`, `FilterBar`, `SearchField`, `ResultRow` |
| **Dependency & Handoff Graph** | Parent-child spawning, thread dependencies, and peer handoffs (author → verifier → land) without raw UUIDs (`OR-38`). | `ExecTree`, `FlowStrip`, `StepList` |
| **Screenshot Timeline** | Chronological strip of worker screenshot `artifact` captures linked to threads and messages for visual inspection. | `TimeStrip`, `ArtifactView`, `ResultViewer`, `EntityCode` |

## 5. Synergy with Universal Code Mode and Lab evaluation

- **Universal Code Mode across harnesses ([fleet-code-mode.md](fleet-code-mode.md)).** Coordinators and workers run `codeMode` (`add` or `only`) across `codex`, `claude`, `antigravity`, and `acp`. A coordinator executes one `code` call to shard a goal, spawn heterogeneous workers in parallel (`Promise.all`), query unread board posts (`since`), filter structured `[done]` summaries, and return only the synthesized outcome into model context. Workers use `code` to batch reads, searches, edits, shell checks, and board posts in fewer turns.
- **Lab evaluation of Swarm algorithms ([fleet-lab.md](fleet-lab.md)).** Fleet Lab runs Swarm strategy variants (single-agent baseline vs Swarm topologies, sizing, verifier ratios, `codeMode` modes, and homogeneous vs mixed harness pairings) on the same use-case suites and measures four axes:
  1. **Quality:** evaluator pass rate, check score, and review defect catch rate.
  2. **Latency:** end-to-end wall time and critical-path step duration.
  3. **Token and cost efficiency:** coordinator + aggregate worker input/cached/output tokens and `list-price equivalent` / API spend.
  4. **Progress observability:** structured report coverage, board signal-to-noise ratio, and screenshot/artifact completeness.
- Proven Swarm profiles and harness pairings promote from Lab into `Library → Methods` (`experimental` → `recommended`) through the standard `J3` workflow ([IA and journeys](fleet-ia-and-journeys.md#3-core-user-journeys-j1j5)).

## 6. Incremental delivery slices

1. **Slice 1 — Shared core board and coordination primitives:** durable `swarm`, `swarm_thread`, `swarm_member`, and `swarm_message` records; `@mention`/`@all`/channel routing; `since`-cursor event wake without polling; and structured report payloads (`progress`, `blocker`, `[done]`, `screenshot` artifacts) exposed uniformly to agents and the UI (`R2`, `R7`).
2. **Slice 2 — Cross-harness Code Mode and Swarm qualification:** verify `codeMode` (`add`/`only`) + Swarm board/delegation tools across `codex`, `claude`, `antigravity`, and `acp`, including `inherit`/`share`/`branch` workspace isolation, per-worker profile/tool scoping, and mixed-harness handoffs.
3. **Slice 3 — Fleet Swarm GUI:** `Swarm` Work Area views (`Overview`, `Roster`, `Board`, `Handoff Graph`, `Screenshot Timeline`) inside `Tasks`, `Conversations`, and `Lab` using `packages/ui` under `OR-1`–`OR-49` and verified by live Playwright checks.
4. **Slice 4 — Lab comparative benchmarks:** parameterized Swarm strategy variants in Lab comparing single-agent vs Swarm, `codeMode` modes, and harness pairings on quality, latency, token/cost efficiency, and observability, ready for `Promote` to `Library → Methods`.
