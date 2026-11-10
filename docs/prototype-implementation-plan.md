# Prototype implementation plan (T1–T16)

2026-11-09. Planning only. Basis: [architecture breadth plan](architecture-breadth-plan.md) T1–T16, [configuration](configuration.md), [layered isolation](layered-isolation-model.md), [agent tools inventory](agent-tools-inventory.md), [PDS Codex environment](pds-codex-environment.md), [resources and auth](resources-and-auth-model.md).

Stand: coordinator `candidate-ccc2d24e023d`; executors on Mac (Claude, Codex, Antigravity) and PDS (Codex, Antigravity). Claude never runs on PDS.

Rules for every workstream:
- Smallest change that lets the T-test run. No caps, gates or new entities. Failures surface as existing execution/session states plus an event with `reason` (e.g. `setup-failed`, `teardown-failed`, `sync-failed`, `auth-expired`); attention-item modelling is deferred to the engineering session.
- New executor logic goes into **new modules** so workers do not collide in `crates/executor/src/main.rs`; `main.rs` gets only a `mod` line and a call site, owned by the workstream named below.
- Tests: one small unit/protocol check per non-trivial branch; acceptance is the real T-run on the stand.
- Merges only into `taskqorg/fleet-hello-world*`. dashq, cs, specq: read-only or throwaway branches `fleet-test/*`; never push to their `main`/`live`.

## 1. Workstreams

| WS | Scope | Owned files | Depends on | Acceptance |
|---|---|---|---|---|
| **W1 Project definition** | `project.json` subset: `repos[]` (`remote`, `forge: github\|gitlab`, `defaultBranch`, `connection`), `environments[]` (host + checkout path), `roles` (role → profile), `worktree` (`envMode`, `baseRef`, `startFromOrigin`, `branchPrefix`, `linkPaths`, `sharedDirectories`), `scripts.setup/teardown`, `setupStart`, `integration` (`mode`, `mergeMethod`, `autoMerge`, `syncCheckoutAfterMerge`), `cleanup.keepWhen`, `identity`. Validate, expose in `project_context`, pass to executor in the start command. Absent key = documented default | `packages/coordinator/projects.ts`, `config/projects/**` (new: `fleet-hello-world`, `fleet-hello-world-alt`, `dashq`, `cs`, `specq`), `tests/coordinator/projects.test.ts`, `tests/coordinator/project-fixture.ts` | – | Unknown key reported, not silently dropped; `project_context` returns the subset; start command carries it |
| **W2 Worktree lifecycle** | `FLEET_PROJECT_ROOTS` per host from `environments[]`; base fetch + `startFromOrigin`; branch `<prefix>/<session>`; `linkPaths` symlink, `sharedDirectories` link; setup with `FLEET_ROOT_PATH/FLEET_WORKTREE_PATH/FLEET_SESSION_ID`, harness starts after setup exit (`wait`), `setup: run\|skip` per start; teardown on session finish, keep worktree on uncommitted changes or teardown failure, safe `git branch -d`. Exit code + truncated output in events | new `crates/executor/src/worktree.rs` (moves `worktree()`/`project_roots()` out of `main.rs`), `tests/executor/worktree_lifecycle.py`; call sites in `main.rs` (`run`, `finish`) | W1 (field shape only; can start on a fixture) | T12, T13, T16 pass on Mac and PDS |
| **W3 Integration** | `integration.mode`: `pr` (push branch, open PR/MR, `autoMerge` via `gh pr merge --auto --<method>` / `glab mr merge --auto-merge`), `direct` (push to default branch). After merge, coordinator emits `base-updated{project,repo,sha}`; each executor fetches and fast-forwards its clean checkout, else event `sync-failed` with reason. Replaces the hard-coded delivery identity | `packages/coordinator/delivery.ts`, `packages/coordinator/delivery_worker.py`, `tests/coordinator/delivery.test.ts`, new `crates/executor/src/checkout_sync.rs` | W1, W4 | T14, T15 pass |
| **W4 Git identity and credentials** | Per execution: `GIT_AUTHOR_*/GIT_COMMITTER_*` from `identity`; credential from `connection` resolved on host (SSH key path via `GIT_SSH_COMMAND`, or `gh`/`glab` config dir) into the child env only; ambient `~/.ssh`/`~/.config/gh` not inherited; no secret values in events | new `crates/executor/src/git_access.rs`, `tests/executor/git_access.py`, `tests/coordinator/identity.test.ts`, `config/connections.example.json` (paths only) | W1 | T10, T11 pass; `env` dump of a session shows only its own connection paths |
| **W5 project_readiness** | One read-only operation: per repo × environment check `git ls-remote` with the project connection, checkout present, setup script present, bound accounts `login` state. Result per check `ok\|failed` + reason; failure also as event so the operator/agent sees it. No stored entity | `packages/coordinator/agent-tools.ts` (adds tool), new executor command handler in `crates/executor/src/readiness.rs`, `tests/coordinator/agent-tools.test.ts` | W1, W4 | T9 passes |
| **W6 One operation surface** | Web UI calls the same handlers as MCP tools: one operation table (`name → handler`) served as MCP tools and as `POST /api/ops/<name>`; UI-only routes replaced or listed as violations. Covers start, fork, handoff, steer, ask/reply, delivery, readiness | `packages/coordinator/app.ts`, `server.ts`, `ui.ts`, `project-ui.ts`, `account-ui.ts`, `lab-ui.ts`, `delegation.ts`, `recovery.ts`, `tests/coordinator/api.test.ts`, `tests/coordinator/*-browser.ts` | none to start; rebases on W5's new tool in `agent-tools.ts` (W6 must not edit `agent-tools.ts` until W5 merged) | T2: every UI action in history has the same operation name as T1 |
| **W7 Executor outbox** | Event delivery on its own thread (accepts polled commands without waiting on flush); per-row failure: a row rejected with 4xx is moved to `outbox_failed` with status + reason and emitted as an executor event, the rest keep flowing; 5xx/network retried | `crates/executor/src/outbox.rs` (new, moves `post/queue/flush` out of `main.rs`), `crates/executor/Cargo.toml` | – | Unit check: poisoned row does not block next rows. T3: 25/50 parallel, start spread < 1 s per wave, no stuck outbox |
| **W8 Codex child agents** | Detect `spawn_agent`/collab items in the Codex event stream; record child thread id, parent, binding as events on the parent execution (accounted under the parent); harness version + actual tool list recorded per run. Profile instruction "no subagents" stays the only off switch | `crates/executor/src/codex.rs`, `crates/executor/src/harness.rs` (tool-list record only), `tests/executor/codex_wire.py` | – | T8 child part: child visible with thread id and binding in parent history |

Not changed: lab (T4), recovery (T5), resume (T6), account handoff (T7) already have their operations; they run as tests and only get fixes found by the run.

Collision check: `main.rs` is touched by W2, W7 only (call sites); W7 lands first or they serialize on `main.rs` via small rebases. `agent-tools.ts`: W5 then W6. `harness.rs`: W8 only.

## 2. Test runs

| Test | Workstreams | Setup on real projects | Turns | Pass |
|---|---|---|---|---|
| T1 Agent-orchestrated work | W1, W2, W3, W4 | `fleet-hello-world`: PM Codex (Mac), worker Claude (Mac), reviewer Codex (PDS) | 8–12 | As breadth plan §4: all actions are Fleet tool calls with caller; separate worktrees/accounts; PR merged; history intact after reload |
| T2 Same flow from web | W6 | as T1 | ~6 | Same operation names/records as T1; zero UI-only routes |
| T3 Concurrency | W7 | `fleet-hello-world`, cheap turns; 2 Codex accounts Mac + 1 PDS; 2 Claude only if second account exists | 75–100 | All terminal or vendor error shown; no cross-account rollout; p50/p95, RSS/fd, 429 recorded; no blocked outbox |
| T4 Lab variants | – | lab task on `specq` read-only (docs review, no push) | 6–8 | Per-variant model/effort applied and recorded; balancing setting applied with reasons; recommendation read via MCP |
| T5 Crash and unknown | – | long Codex turn on Mac on `fleet-hello-world`; publication on PDS | ~3 | No native relaunch, no double publication; unknown shown with last known; fork continues |
| T6 Resume qualification | – | Claude (Mac), agy (Mac + PDS) on `fleet-hello-world` | ~4 | Changed prompt/tools applied with context kept, else freeze "handoff" for that harness |
| T7 Account failure | – | logout one Codex binding on PDS during queued follow-up | ~3 | `login-absent`, question routed, handoff delivered, no lost messages |
| T8 Isolation + child agent | W8 | ancestor marker in `~/.claude/CLAUDE.md`/skills; Codex profile allowing subagents | ~3 | No marker seen; child recorded on parent with binding |
| T9 Readiness | W5, W4 | `fleet-hello-world` (Mac + PDS) and `cs` (GitLab, read-only `ls-remote`); then remove PDS credential path | 0 | All `ok`; then PDS `failed` + reason, visible event; no secret/raw stderr |
| T10 Commit + push identity | W4, W2 | `fleet-hello-world`, branch push | ~2 | Author = project identity; push via project connection; `ls-remote` readback; worktree removed |
| T11 Credential separation | W4 | `fleet-hello-world` and `fleet-hello-world-alt` with different connections on one host | ~1 | Cross-project `ls-remote` refused; env holds only own paths |
| T12 Setup build | W2 | `dashq` throwaway: `scripts.setup` = `cargo build --locked` + marker; plus `fleet-hello-world` minimal setup; `setup: skip` run | ~1 | Base = fresh `origin/<default>`; setup once with env vars; agent after setup; output recorded; skip has no marker |
| T13 Teardown | W2 | `fleet-hello-world`, teardown writes marker outside worktree; one committed, one dirty session | ~2 | Clean: teardown ran, removed, branch deleted only if merged; dirty: kept with reason event |
| T14 Merge policy | W3, W4 | `fleet-hello-world` `pr`+`squash`+`autoMerge`; `fleet-hello-world-alt` `direct` | ~2 | One squash commit via PR; direct push without PR; both with project identity |
| T15 Checkout sync | W3 | after T14, new session on Mac and PDS; one checkout made dirty | ~1 | Clean checkout fast-forwarded, new worktree has change; dirty one untouched with `sync-failed` |
| T16 Linked files | W2 | `cs` checkout on Mac (throwaway branch, no push): `linkPaths: [.env.local]`, `sharedDirectories: [node_modules]`; worker runs node script | ~1 | Both present without install; no content in records/logs |

specq extra check inside T12: `envMode: local`, no setup, start succeeds with no worktree.

## 3. Execution order (max 3 parallel workers)

1. **Wave 1 (parallel):** W1 · W7 · W8. Disjoint files. Then run T3, T8 (child part).
2. **Wave 2 (parallel):** W2 · W4 · W6. W2 and W4 both new modules; `main.rs` call sites rebased after W7. W6 avoids `agent-tools.ts`. Then run T10, T11, T12, T13, T16.
3. **Wave 3 (parallel):** W3 · W5. Then run T9, T14, T15, then T1 and T2 end to end.
4. Independent of waves, any free slot: T4, T5, T6, T7 (no code needed; fixes only if the run fails).

Each wave: deploy one coordinator candidate + both executors, run its tests, record evidence under `evidence/<test>-<date>/`.

## 4. Owner blockers

1. **cs GitLab credential** for T9/T16: which connection (deploy key read-only, or personal `glab` token) on Mac and PDS? Without it T9 uses GitHub repos only and the GitLab forge path stays unproven.
2. **Project identity** for T10/T14: name/email and push credential (bot account deploy key or owner `gh`) for `taskqorg/fleet-hello-world*`.
3. **Two connections** for T11: second deploy key or second GitHub account for `fleet-hello-world-alt`.
4. **Second Claude account** for T3 Claude part: exists or skip.
5. **auto-merge** on `fleet-hello-world`: enable repo setting "Allow auto-merge" (needs repo admin) or merge via Fleet after checks.
