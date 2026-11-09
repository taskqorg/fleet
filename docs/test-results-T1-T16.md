
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
