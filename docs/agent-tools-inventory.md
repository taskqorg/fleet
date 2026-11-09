# Agent tools inventory

2026-11-09. Read-only source inventory of Orca (`~/Projects/orca` @ `0b93cef4450`) and T3 Code (`~/Projects/t3code`), compared with Fleet. Purpose: find tested logic Fleet can copy so it can help agents. Fleet adds no limits, caps or admission rules here. Balancing and settings stay owner-configured ([R6](../fleet.md#principles)).

Path prefixes: Orca `O/` = `src/main/runtime/rpc/methods/orchestration/`, `W/` = `O/worker/`. T3 `T/` = `apps/server/src/orchestration-v2/`, `P/` = `packages/provider-core/src/server/`.

## Orca summary

- Interface: the agent-facing `orca orchestration` CLI plus skills (`skills/orchestration`, guides via `orca skills get --full`). Each worker gets an instruction block (`src/main/runtime/orchestration/preamble.ts`). No orchestration MCP.
- Runs: `run-create/list/show/use`. `run-use --takeover-legacy` resumes a coordinator (`src/main/runtime/orchestration/db/runs/run-binding.ts`).
- Work: a task DAG (`task-create --deps --parent`), decision gates (`gate-*`), `dispatch --task --to <terminal>`.
- Workers: `worker-start` (`W/local-worker-start.ts`; `--on <env>` → `federation/federated-worker-start.ts`), `worker-show/list/read` (liveness `live|exited|missing|identity_changed|unverifiable`, plus a suggested next action, `W/worker-observation.ts`).
- Ending workers: `worker-stop` returns `stop_unknown` when it can't prove the stop. `worker-abandon` settles the dispatch and kills nothing. `worker-release` archives output, then closes only terminals Orca owns.
- Messaging: `send` point-to-point or to groups (`@idle`, `@codex`, …; `messaging/send-*.ts`). `ask` is non-blocking (`--wait` or `--resume <msg>`); `reply`; `check/inbox`.
- Completion: `worker_done` is checked against the active dispatch (`orchestration/lifecycle-reconciliation.ts:98`) and saves the native resume session (`W/worker-resume-session-capture.ts`).
- Resume: one command template per agent in `src/shared/agent-launch/startup/agent-resume-argv.ts` (Claude `--resume`, Codex `codex resume`, agy `--conversation`, OpenCode `--session`, …).
- Follow-ups: a follow-up to a finished worker goes, in order, to its open chat, then its live terminal, then a tab already holding the session, then a reopen via the resume argv, then a fresh agent with an `earlierWork` summary (`W/worker-follow-up-choice.ts:49`).
- Switching agent type: a fresh launch with `--retry-of`. There is no session transfer.
- Unknown state: only proof counts. Silence never stops, retries or releases a worker. `request-show --retry-request` recovers a mutation whose result was unknown. User-taken-over terminals are never closed.
- Placement: worktree `current|<sel>|new-child|new-top-level` (`W/worker-worktree-creation.ts`) and remote environments via `--on`.
- Built-in limits Fleet should **not** copy: a start retry circuit breaker of 3 (`db/worker-dispatch/worker-dispatch-outcome.ts:86`) and a prompt budget (`W/worker-start-prompt-budget.ts`).

## T3 Code summary

- Provider contract: `P/ProviderAdapter.ts:617-721` — `openSession`, `ensureThread`, `resumeThread`, `startTurn`, `steerTurn`, `interruptTurn`, `respondToRuntimeRequest`, `readThreadSnapshot`, `rollbackThread`, `forkThread`, `getCapabilities`, `planSelectionTransition`.
- Codex adapter (app-server JSON-RPC, `T/Adapters/CodexAdapterV2.ts`):
  - threads: `thread/start` :7167, `thread/resume` :7195, `thread/fork` :8017;
  - turns: `turn/start` :6882, `turn/steer` :7358;
  - interrupt :7398 waits for `turn/started` first;
  - approvals arrive as server requests :5359-5502.
- Claude adapter (Agent SDK, `T/Adapters/ClaudeAdapterV2.ts`): SDK `resume: <sessionId>` and `forkSession` (:1560); `resumeThread` :11887, `forkThread` :12280; approvals via `canUseTool` :10302; `AskUserQuestion` handled.
- Other adapters: Antigravity, ACP, OpenCode, Cursor, Grok, Kiro, Pi, Fx, Muse (`apps/server/src/provider/builtInDrivers.ts:33`).
- Sessions: `T/ProviderSessionManager.ts:158` (`open`, `isLive`, `release`, `detach`, `releaseUnlessBusy`).
- Turns:
  - start: `T/ProviderTurnStartService.ts`, which recreates a missing worktree (:191) and rebuilds the handoff when a start is retried (:1029);
  - steer and interrupt: `T/ProviderTurnControlService.ts`.
- Provider switch:
  - planning: `T/ProviderSwitchService.ts:36`;
  - policy: `T/ProviderSessionTransitionPolicy.ts:32` → `reuse | switch_model_in_session | restart_and_resume | create_with_handoff | reject`.
- Context transfer: `T/ContextHandoffService.ts` (`prepareProviderHandoff` :70 sends a delta since the target last saw the thread, or a full summary; `prepareForkDelta` :57; side chat :88). It is delivered inline before the next user turn (`T/ContextHandoffDelivery.ts`). If it is unclear whether delivery happened, a fresh native thread is used.
- Fork: `T/ThreadForkService.ts:51` uses the native fork when the adapter has one, otherwise a context transfer.
- Recovery:
  - on startup and shutdown, `T/ProviderRuntimeRecoveryService.ts:882` ends dead runs and closes pending requests;
  - optional auto-continue after a restart (`T/RestartContinuation.ts`, behind a settings flag);
  - `thread.interrupt.unresponsive` marks a stuck interrupt so it can be force-stopped.
- Runtime requests: `T/RuntimeRequestService.ts:51`. When an answer's delivery fails, the user is told it is unconfirmed.
- Routing: none. The user picks the provider instance per thread (`ModelSelection.instanceId`).

## Feature matrix

| Capability | Orca | T3 Code | Fleet current | Recommendation |
|---|---|---|---|---|
| Create session | `worker-start` (chat or terminal, agent alias, model/effort) | `ThreadMessageIntake.launchThread` → `openSession`+`startTurn` | `POST /api/sessions` (`packages/coordinator/app.ts`), the executor launches the harness (`crates/executor/src/harness.rs`) | Keep Fleet. |
| Send / continue | `send`; a follow-up to a finished worker becomes its next task | `sendToThread`; `steerTurn` steers a running turn | `POST /api/sessions/:id/messages`; `409` while active | Copy T3's **queued message + steer** so a message to a busy session is queued or steered, not refused (`T/ProviderTurnControlService.ts`, `queued-message.promote-to-steer`). |
| Resume | Resume-argv template for every agent; session captured at `worker_done` | Adapter `resumeThread` (Codex `thread/resume`, Claude SDK `resume`) | Codex app-server resume + `inject_items` (proven); Claude `--resume` (unproven); agy ACP `session/resume`/`load` | Copy Orca's **per-agent resume table** and session capture as the data. Qualify Claude through T3's SDK `resume` path. |
| Fork | None (a retry is a fresh start) | `thread.fork`: native `forkThread` (Codex `thread/fork`, Claude `forkSession`), otherwise `prepareForkDelta` | None | Copy T3 `ThreadForkService` + `ContextHandoffService.prepareForkDelta`. This also answers a session stuck in `unknown`: fork it and leave the original inspectable. |
| Handoff to another worker type | Fresh launch with `--retry-of --agent X`; `earlierWork` summary | `ProviderSessionTransitionPolicy` `create_with_handoff` + `prepareProviderHandoff` (delta or summary, delivered inline) | Harness/account fixed per session (`409`) | Copy T3's **transition policy + context handoff**. Fleet's per-session pin becomes "same harness → resume, other harness → new native session with handoff". |
| Stop / cancel | `worker-stop` (`stop_unknown` when unproven), `abandon`, `release` | `interruptTurn` (waits for turn start), `thread.stop`, `interrupt.unresponsive` → force | `POST /api/executions/:id/stop` | Copy Orca's **proven-or-unknown stop result** and T3's **unresponsive → force** step. |
| Ask coordinator | `ask` (non-blocking, `--wait`/`--resume`), `reply`, gates | Runtime requests (approvals, `AskUserQuestion`); delivery-failed notice | Delegation children (`packages/coordinator/delegation.ts`); no ask tool | Copy Orca's **ask/reply** messages as a Fleet tool; T3's delivery-unconfirmed state. |
| Report done | `worker_done` checked against the dispatch, captures the resume session | Run finalization (`RunFinalizationService.ts`) | Execution reaches a terminal state from executor receipts and events | Copy Orca's **stale-dispatch check** (`lifecycle-reconciliation.ts:98`) for delegated children. |
| List / inspect | `worker-list/show/read`, liveness + suggested `next` | `readThreadSnapshot`, `isLive` | Session/execution/events/differences/results APIs | Add Orca's **liveness enum + suggested next action** to the execution view. |
| Recovery of unknown/stuck | Proof only; `request-show --retry-request`; restart recovery of terminals | Startup/shutdown reconcile; optional auto-continue; force on an unresponsive interrupt | `unknown` blocks the session; narrow transport recovery (`packages/coordinator/recovery.ts`) | Copy T3's **startup reconcile** (`ProviderRuntimeRecoveryService.ts`) on the executor side, plus fork-to-continue (above). No promotion without evidence. |
| Multi-environment placement | Worktree modes; `--on <env>` remote by Dispatch ID | Worktree strategies, recreate a missing worktree; relay environments | One host from config (`config.hostId`) | Copy Orca's **worktree modes + remote by id** when a second host exists. The operator chooses the host (setting, not rule). |
| Routing / balancing | Agent aliases from config; group messaging `@idle` | None (user picks the instance) | Managed account resolution per execution (`packages/coordinator/accounts.ts`) | Owner-configured setting only. Copy Orca's config **agent aliases**. No caps. |
| Agent-facing tool set | CLI + skills + injected preamble | Adapter MCP tools/resources | Project/recovery tools (`/api/project-tools`, `/api/recovery-tools`) | Expose the rows above as Fleet tools over one core state (R2), following Orca's preamble pattern. |

## Port first

1. Context handoff + provider transition policy (T3 `ProviderSessionTransitionPolicy.ts`, `ContextHandoffService.ts`, `ContextHandoffDelivery.ts`): this lets work move across harnesses and is the base for fork.
2. Fork (T3 `ThreadForkService.ts`; Codex `thread/fork`, Claude SDK `forkSession`): continues a session stuck in `unknown` without replaying it.
3. Per-agent resume table + session capture (Orca `agent-resume-argv.ts`, `worker-resume-session-capture.ts`, `worker-follow-up-choice.ts`).
4. Ask/reply + checked done (Orca `messaging/ask-methods.ts`, `reply-method.ts`, `lifecycle-reconciliation.ts`).
5. Queue/steer for busy sessions (T3 `ProviderTurnControlService.ts`, `QueuedRunOrder.ts`).
6. Startup reconcile + proven/unknown stop (T3 `ProviderRuntimeRecoveryService.ts`; Orca `worker-stop.ts`).
