
## Worker goblin: T1, T10, T12–T16 on fleet-hello-world (2026-11-09)

Stand: coordinator `candidate-28517364d2bd` (PDS, port 9470), executors `owner-mac` and `pds18-native`. All actions went through `POST /api/ops/*` or Fleet MCP (host agent tools). There was no deploy and no restart. Live config changes: `dist/config/projects/fleet-hello-world/project.json` now has setup/teardown/linkPaths/sharedDirectories (backup at `/root/fhw.project.json.bak` on PDS). Added the `dist/config/agents/pm.json` profile. Mac and PDS checkouts have `dist/`, `.env.local`, `node_modules` in `.git/info/exclude`, a dummy `.env.local` and a `node_modules/hello` module.

| Test | Result | Evidence ids |
|---|---|---|
| T1 PM→worker→reviewer via MCP | **partial** | PM session `6366b07e-c4ca-46db-9bd2-b8814d6d560d` (executions `229dc1ec`, `38e02700`, `25579809`). Worker `feeda859-2593-422b-be50-82b2f39ffa64` (Claude, owner-mac, exec `9d981b2d`). Reviewer `91a97d43-f673-4ff5-a7dd-ce5472b989a4` (Codex, pds18-native, execs `e1c27bb3`, `c069344a`). Question `9349444d-0ad1-45fe-9cb7-dbabd3907184` answered by the PM. Verdict APPROVE |
| T10 commit+push with project identity | **pass** | Branch `fleet/feeda859-…` = `a3c063e1a46055224da670a340e61b397d15d985` (ls-remote readback). Author `fleet-bot <fleet-bot@users.noreply.github.com>`. Worktree removed at archive |
| T12 setup waited | **pass** (fleet-hello-world part) | `setup` event exit 0 before `harness` in execs `fd5a8650`, `229dc1ec`, `9d981b2d`, `e1c27bb3` (PDS). Worker read `dist/marker` = session id + worktree path. HEAD = fresh `origin/main` `0038aa9`. A `setup: skip` run was not done |
| T13 teardown on archive | **pass on host, fail in records** | `session_archive` commands `c87404a1` (worker: teardown ran, worktree removed, unmerged branch kept) and `da090000` (generation session: kept, `uncommitted`). Marker in `/tmp/fleet-hello-world-teardown.log` on Mac. Both teardown events rejected 409 and are in the Mac `outbox_failed` |
| T14 PR + squash merge by Fleet | **blocked** | Delivery `0b7c149d-3df8-4f67-9dad-ddf2e002f79c` is `blocked`: "Expected bounded static HTML/CSS artifacts". Generation wrote `output/index.html` inside the worktree; the executor collects only `<session>/output` |
| T15 base-updated sync on both hosts | **blocked** | Depends on the T14 merge |
| T16 linked ignored files | **pass** | Worker saw `.env.local` (presence only) and `require('hello')` = `hello-from-shared-node_modules` via the symlinked `node_modules`. No content of `.env.local` appears in the records |

### Defects (fixed in this branch, need coordinator + executor deploy)
1. Project-session worktree output was never collected as artifacts. Delivery stays blocked for any project with a checkout. Fix in `crates/executor/src/main.rs` reads `worktree/output` first (23c5418).
2. Host agents could read only their own `execution_status`. A PM cannot watch its worker. Fix: `execution_status{sessionId}` for owned sessions, and the MCP schema in `delegation.py` (23c5418).
3. The teardown event used seq 0 on the reused last execution. The coordinator rejects it with 409 `Batch/sequence conflict`, so the teardown outcome is lost from records. Fix: fresh seq (2011622).
4. The `baseline` profile's "Do not use subagents" made the Codex PM refuse to start Fleet sessions. Added the `pm` profile (2011622).

### Observations
- The Codex PM sandbox has no network: `git ls-remote` failed (cannot resolve github.com). The reviewer on PDS could not `git fetch` (read-only git metadata in the sandbox), but it reviewed the commit through local objects. Agents that must use git remotes need sandbox network and writable common `.git`, or a Fleet op.
- Two operator nudges were needed: the PM could not observe the worker's push or state (defects 2 and 4). T1 is therefore not "first message only".

### Architecture findings
- No operation binds a new session to a project. `session_create`/`session_profile` ignore `projectId`. The only entry is `delivery_create`, then `session_fork` (fork inherits `projectId`). A project-session start op is missing.
- Host agents have no delivery/PR op (`delivery_*`, `session_archive` are not in the bound tool set). The agent path (worker pushes a branch) and the delivery path (program worker commits artifacts) are two different integration routes. The PM cannot ask Fleet to open or merge a PR from the worker branch.
- There is no completion wake: the worker's finish does not notify the session that created it. The PM must poll.

## Worker goblin-2: T2, T9, T11 on fleet-hello-world-alt (2026-11-09)

Stand candidate-28517364d2bd, executors owner-mac and pds18-native. Operations called on PDS loopback `/api/ops/*` (browser secret read from the process env in memory, never printed). Evidence: `evidence/t2-t9-t11-goblin/`.

| Test | Result | Evidence |
|---|---|---|
| T2 web uses same operations | Partial pass | `t2-ui-calls.json`, `t2-sessions-{desktop,mobile}.png`, `t2-projects-{desktop,mobile}.png`; session via UI, delivery `03b67fc0-e042-4b22-b5e4-4e8ebe84730c`, execution `ad0b0ef4-2ceb-43b2-a2ed-36edfc8be4e6` |
| T9 readiness | Pass (one criterion gap) | ok: executions `77876b05…` (mac), `fe25c898…` (pds); negative throwaway project: `f60c0a98…` (mac, bad remote), `9ea3f389…` (pds, missing checkout + bad remote) |
| T11 credential separation | Pass (env/connection level) | agent env check in execution `ad0b0ef4…`; cross-connection readiness `585218b4…` (mac), `ce82ad22…` (pds) failed; control `fleet-hello-world` readiness `5574ff81…`, `04c30035…` ok |

### T2
- Every UI write went to `/api/ops/<name>`: `session_create`, `message_send` (sessions page); `delivery_create`, `question_answer`, `delivery_generate`, `delivery_history`, `question_history` (projects page). These are the same names served as MCP tools. A codex turn started from the UI completed.
- UI reads use non-op GET routes (`/api/sessions/:id` ×109 polls, `/api/projects/:id`, `/catalogue`, `/runs`, `/api/accounts`, `/api/profiles`) while agents read via `project_context`, `catalogue`, `account_list`, `execution_status`. Read surface is not unified.
- Not drivable from the UI: fork, handoff, steer of a running turn (only via message while busy), ask/reply, `project_readiness`. The session UI cannot start a project-bound worker; the only project session path is delivery.
- The projects page select was hard-coded to `hello-world`/`hello-world-alt`; the run injected the `fleet-hello-world-alt` option (fixed in this branch).
- Delivery ended `blocked: Expected bounded static HTML/CSS artifacts`: `output/index.html` was written in the session worktree but not uploaded. Already fixed by 23c5418 (worktree output collection), not deployed.

### T9
- Positive: both hosts `checkout ok`, `remote ok`. This project declares no setup script or account bindings, so those checks are absent (fleet-hello-world control shows `setup` and `account` checks ok).
- Negative (throwaway `t9-goblin-negative`, removed after): mac `remote failed ls-remote-exit-128`; pds `checkout failed checkout-missing` + `remote failed`; plus a `readiness.failed` event; execution state `failed`. No secret or raw stderr in output.
- Gap: no operator question is created on failure (plan criterion "a question to the operator").
- Defect: tool description says "read results with execution_status", but it returned no readiness data; results were only in `/api/executions/:id/events`. Fixed in this branch (`execution_status` returns `readiness`).

### T11
- Agent session of `fleet-hello-world-alt` (codex, owner-mac, from the web delivery): `GIT_SSH_COMMAND` contains own connection `yes`, other project's connection `no`.
- In that session both `ls-remote` calls exited 128, including its own repo (agent sandbox/no-network), so the agent-side refusal does not prove the credential. The credential proof is the executor path: a throwaway project pointing at `fleet-hello-world` with connection `fleet-hello-world-alt` failed `ls-remote` on both hosts, while `fleet-hello-world` with its own connection passed.

### Defects
1. `execution_status` hides readiness results — fixed (packages/coordinator/agent-tools.ts, test in agent-tools.test.ts).
2. Projects page select hard-coded — fixed: options loaded from `/api/projects` (project-ui.ts; projects-browser passes).
3. Delivery artifacts from worktree not uploaded — fixed by 23c5418 (other worker). Needs coordinator + executor deploy.

### Architecture findings
- Credential isolation is environment-only: both deploy keys are readable by the same OS user (`fleet` on PDS, owner on Mac). An agent of project A can read B's key file directly. Real isolation needs a per-project OS user or a credential broker that does not expose key files.
- Readiness reason `ls-remote-exit-128` does not distinguish auth denied, missing repo, and network failure.
- Readiness failure needs an operator question/notification, not only an event.
- The codex sandbox blocks network for the agent, so agent-side git push/ls-remote depends on executor-run steps (delivery), not on the agent.
- R2: read operations are not in the op table; the UI polls REST reads. Either add the reads to the op table or document reads as exempt.

## Wave3 status (goblin, 2026-11-09)

Live release `candidate-b02670f3cddf` (source `b02670f3cddf`, archive SHA256 `f35e19544c79b36b7f49f852debabde63bea11b9b8b7926d50bbc708cb94578d`). PDS executor `executor-b02670f3cddf` (SHA256 `a6633a93…`), Mac executor SHA256 `ad16b785…`, both program workers updated. Deployed from a clean `git archive` at 0 unfinished (only the known stuck `6b00f0fb` on `pds18`). Readback: 4 hosts heartbeat, roles and profiles served, readiness ok on both hosts.

Rollback: `ln -sfn candidate-28517364d2bd /home/fleet/releases/current`, `ln -sfn /home/fleet/runtime/executor-28517364d2bd /home/fleet/runtime/current-executor`, restart the three user services. Mac: restore `bin/fleet-executor.pre-b02670f3cddf`, `program-worker/delivery_worker.py.pre-b02670f3cddf`, `~/.config/fleet/program-worker.prototype.json.pre-b02670f3cddf`, then kickstart both agents.

Code (commits `94c2da0`, `b02670f`): `session_start` (project + role/profile; the binding picks account and host), `session_deliver` (host agent asks Fleet to open a PR from its session branch and merge it after checks), completion wake (a finished child turn is queued as the creator's next message), `gitNetwork` (Codex network + writable checkout `.git`, default on for pm/reviewer/worker of git projects), web reads via `/api/ops` (session_get, execution_differences, profiles_list, project_list, account_list, project_context, catalogue, pipeline_history), UI fork/handoff/reply/readiness/start, classified ls-remote reasons. cargo 28 pass, npm 28 pass, browser checks pass.

| Test | Result | Evidence |
|---|---|---|
| T1 no operator nudges | **pass** | PM `68e0b116` (codex, Mac) started worker `1b116f69` (Claude, Mac, pushed `58afa67e`), woken, started reviewer `e30da2b0` (codex, PDS, APPROVE after `git fetch`), woken, called `session_deliver`, reported the merge. Only the first message came from the operator |
| T14 PR + Fleet merge | **pass** | Delivery `cda09060` state `merged`, https://github.com/taskqorg/fleet-hello-world/pull/1 |
| T15 checkout sync | **likely pass, not read back** | base-updated executions `1dbc87a2` (Mac) and `730cbb98` (PDS) completed; checkout HEADs not checked |
| setup:skip | pending | not run (session stopped) |
| T2 web delivery on alt | pending | not run |
| ls-remote negative reasons | unit-tested only | |

Findings:
- ARCHITECTURE: credential isolation is env-only. One OS user (`fleet` on PDS, owner on Mac) can read every project's deploy key, so an agent of project A can read B's key file. Not fixed.
- Security tradeoff (as requested): `gitNetwork` gives the agent full network and a writable common `.git` (hooks/config of the shared checkout). Narrower options: per-session clone, or Fleet-mediated push/fetch.
- `gh pr checks` "no checks reported" is detected from text output; structured `--json` output would be more robust.
- `session_deliver` completion does not wake the PM; the PM polls with repeated calls.

## Postgres stand, candidate-1ddecc160382 (2026-11-09)

Stand: PDS18 coordinator on Postgres (`FLEET_DATABASE_URL`, role `fleet_app`, database `fleet`, table `document`), executors `owner-mac` and `pds18-native`. Method as in Wave3: PM (role `pm`, Codex, Mac) started by `session_start` over PDS loopback `/api/ops/*` with the Wave3 T1 prompt (new line and request ids). No code, config or release changes by this run.

| Test | Result | Evidence |
|---|---|---|
| T1 no operator nudges | **fail (blocked by coordinator defects)** | Run 1 on `candidate-1ddecc160382`: PM `05e696a1-f925-4d58-abe1-8073f74eb965` (exec `d89cc199`) called `session_start`; the tool call failed with `{"status":"pending","instruction":"Outcome unknown; inspect with execution_status before repeating effects."}` although worker session `060c3cd8-7325-477f-ae43-5fe052cbd661` (exec `1ee2faa9`, command `c6869fe8`) was written. The worker stayed `queued`. Run 2 on `candidate-dbd0585eec13`: PM `eb73b3bf-eae5-4990-94ed-8cd0b793d80b` (exec `3db9141c`, command `726f1f2c`) stayed `queued`; the Mac executor never got it. All three executions stopped by `execution_stop` (commands `c0a0656c`, `f490cb2a`, `d37a8e3b`) |
| T14 PR + Fleet merge | **not reached** | No delivery and no PR; depends on T1 |

Postgres readback: session, execution and command documents above are in `document` in the expected `seq` order (session `16183` before its execution `16187` and command `16190`). Writes land in Postgres.

### Defects
1. **Slow ops on Postgres** (fixed by PR #12, `candidate-dbd0585eec13`). On `candidate-1ddecc160382`: `execution_status` took 50.1 s on loopback, node at 166% CPU, `pg_stat_activity` showed 10 concurrent `SELECT data FROM document WHERE kind=$1 ORDER BY seq` (pool saturated) and `idle in transaction` sessions at `RELEASE SAVEPOINT s3318`. Agent MCP calls timed out ("Outcome unknown"). Program worker logged `TimeoutError: timed out`. After PR #12 (coordinator measurement): `/api/hosts/poll` 6–8 ms, node ~20% CPU; this run measured `execution_status` 0.065 s, node 9% CPU.
2. **Serialization failures are not retried; `/api/hosts/receipts` returns 500** (open, on `candidate-dbd0585eec13`). Replaying the head Mac outbox row (receipt for command `039afb26`, exec `d89cc199`) to `/api/hosts/receipts` on loopback returned `500 {"code":"25P02","message":"current transaction is aborted, commands ignored until end of transaction block"}`. The Postgres log shows the cause: `ERROR: could not serialize access due to read/write dependencies among transactions` (`Reason code: Canceled on identification as a pivot, during conflict out checking`) on `SELECT data FROM document WHERE id=$1 AND kind=$2` and `... kind=$1 AND data @> $2 ORDER BY seq`, then the handler kept issuing statements (the `INSERT ... ON CONFLICT` fails with 25P02). Count in the current Postgres log since 15:35 UTC: 7259 `could not serialize`, 776 `transaction is aborted`. Fix needed: retry the whole transaction on 40001, and stop running statements after an error inside a transaction.
3. **Mac executor head-of-line block** (open). The Mac outbox has 115 rows (83 events, 28 receipts, 4 agent-tools) and grows; `executor.stderr.log` repeats `executor outbox delivery failed; rows retained, retry pending` and `executor exchange failed; retained journal, retry pending` with no status code. Because of defect 2 the head row never succeeds, so no later row and no new command reach the Mac. `outbox_failed` has only the 2 old T13 teardown rows (409); no new 409/5xx rows were moved there. The log should include the HTTP status and reason.

### Notes
- No operator nudge was given to the PM; the run did not get far enough for T1 to be judged on nudges.
- Unhandled rejections: none seen in the coordinator journal; errors surface as 500 responses.
- While reading the Mac executor process environment, the Mac `FLEET_HOST_TOKEN` appeared in this worker's tool output (not in any file or record). Rotate that host token.
- Rerun T1/T14 after defect 2 is fixed; the Mac outbox should drain first.
