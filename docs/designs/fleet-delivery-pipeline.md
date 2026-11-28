# Fleet delivery pipeline

* **Author:** Alex Kirs
* **Date:** 2026-11-11
* **Status:** the merge queue (section 1) and the automatic release (section 4) run on `main`; section 8 is being built. Amends [fleet-self-update.md](fleet-self-update.md): Goals, the version decision, CUJ 1, section 1 (release trigger and gate), 3.4 (health) and 3.5 (units). Section 8 (2026-11-11, owner decision) moves enabling auto-merge from the orchestrator to the worker that owns the pull request.

**Terms.** *Orchestrator*: the one agent session that coordinates the lanes and holds the dequeue veto for the project ([one orchestrator](../../fleet.md)); it is not the coordinator service. *Coordinator*: the Fleet service on PDS18 ([fleet-self-update.md](fleet-self-update.md#terms)). *Batch*: the commits that land on `main` while one release run is busy.

## Overview

A pull request travels: review, merge to `main`, release, install on every host. Today each step waits on a person or a timer. This design hands merging to GitHub's merge queue and releases every green `main` batch without a tag. PDS18 learns of a release within a minute instead of five; executors start their update as soon as their poll brings a new target. The `fleet-update` health probe stays the only deploy gate and keeps its rollback.

Targets, measured from "the worker enables auto-merge on its reviewed, green pull request":

* On `main` after one queue CI run (about 1 minute when the queue is empty, [checks per change](#checks-per-change)).
* Running healthy on every host with auto-update on within 15 minutes, with nobody watching, when PDS18 is up and at most one release behind.

## The Problem

Facts from 2026-11-11:

* About 40 small pull requests a day come from 4 to 5 agent lanes. `main` has no branch protection and no ruleset ([GitHub API](https://docs.github.com/en/rest/repos/rules): `GET /repos/taskqorg/fleet/rulesets` returns `[]`).
* The orchestrator merges with a script, one pull request at a time. It updates each branch from `main` and waits for CI again (3 to 4 minutes), so each pull request waits for CI twice. Twelve pull requests take hours.
* Two pull requests, #189 and #192, were each green on their own and broke `main` together. The script's update-and-rerun was added after that.
* Deleting the base branch of a stacked pull request closed the child once.
* Flaky gateway tests failed at least 4 runs.
* A release needs a person to push an annotated tag `v1.YYMMDD.N` whose date is the UTC date. One tag was rejected for a local date.
* Hosts poll on 5-minute timers. A worker waits on the host version, then runs the agent smoke test by hand.

## Goals

* Every pull request is tested as part of its queue group: `main` plus everything ahead of it plus itself. No pull request waits for a second run of its own.
* A worker reviews its change with `/code-review` before it opens the pull request and enables auto-merge on its own pull request after the `code-review` status, and only then; GitHub merges. The orchestrator can take any pull request out of the queue ([section 8](#8-workers-merge-and-watch-their-own-pull-requests)).
* A flaky failure is recorded and never turns a red run green. A merge always needs a clean pass.
* Every green `main` batch releases without a person. The pipeline computes the version, so there is no tag date to get wrong.
* Releases come only from the pipeline. One switch held by the orchestrator pauses releasing.
* A release reaches every host within 15 minutes. A release that fails health on a host is rolled back by `fleet-update` itself, as today.

## Non-Goals

* A new inbound endpoint, a webhook, or an Access token stored in GitHub Actions secrets.
* A smoke test or rollback run from a GitHub runner. Runners cannot reach the Mac or WSL.
* A custom quiet-window script, retarget script or flaky-test classifier.
* Release channels or staged rollouts (still out of scope, as in [fleet-self-update.md](fleet-self-update.md#non-goals)).

## GitHub facts this design relies on

Checked against GitHub's docs on 2026-11-11:

* Merge queues are available "in any public repository owned by an organization" ([Managing a merge queue](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue)). `taskqorg/fleet` is a public org repository on the free plan, so the queue is available.
* Workflows that report required checks must also trigger on `merge_group`. Without it the check is never reported and the merge fails (same page).
* The queue tests each group head (not each pull request alone) on a temporary `gh-readonly-queue/main/...` branch that holds `main`, the pull requests ahead of it and this one. A failing pull request is removed, and the groups behind it are rebuilt without it (same page).
* Queue settings: merge method, build concurrency (1 to 100), "only merge non-failing pull requests", status check timeout, and minimum or maximum group size with a wait time (same page).
* `gh pr merge --auto` on a branch that requires a merge queue adds the pull request to the queue once its checks pass ([Merging a pull request with a merge queue](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/incorporating-changes-from-a-pull-request/merging-a-pull-request-with-a-merge-queue)).
* A pull request enters a queue only for its own base branch. A stacked child (base = its parent's branch) is not in the `main` queue.
* When a head branch is deleted after its pull request merged, GitHub retargets the open pull requests based on it to the merged pull request's base ([Merging a pull request](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/incorporating-changes-from-a-pull-request/merging-a-pull-request)). Deleting a base branch that has not merged closes its children; that is the incident above.
* Events made with the workflow's `GITHUB_TOKEN` do not start new workflow runs, except `workflow_dispatch` and `repository_dispatch` ([Triggering a workflow](https://docs.github.com/en/actions/using-workflows/triggering-a-workflow)). A tag created by the release run therefore never starts another workflow.
* A workflow skipped by a path or branch filter leaves its required checks "Pending" and blocks the merge (same page). Required checks therefore come from jobs that always run.

## Technical Plan

```
worker: /code-review, open PR, status ─────► gh pr merge --auto <n>   (orchestrator: dequeue veto)
                                             │
GitHub merge queue ── merge_group run of ci on main+ahead+PR, in parallel:
        │   go, app (required, about a minute each; checks per change)
        │ green: main moves to the tested commit   red: PR removed, its worker fixes
        ▼
push to main ──► release.yml (on: push main; concurrency: release, no cancel)
        gate: FLEET_RELEASE_PAUSE unset; sha = head of main; ci green for sha
        ─► build + selftest + migration check (PDS18 runner), publish (signed SHA256SUMS, latest.json, tag by API)
        ▼
PDS18 fleet-update (timer 10 s, conditional GET of latest.json) ── install, health, smoke,
        3 starts ─► rollback; healthy ─► coordinator healthyTarget = new version
        ▼
executor poll reply carries target ─► executor rewrites update.json
        ─► systemd .path / launchd WatchPaths starts fleet-update executor at once
```

### 1. Merge: ruleset and merge queue

* **Ruleset** on `main` (repository ruleset, created with `gh api repos/taskqorg/fleet/rulesets`), enforcement active:
    * Require a pull request (no required approvals: review is the coordinator's `/code-review`, not a GitHub approval).
    * Require status checks `go`, `app` and `code-review` (`browser` until 2026-11-25, [checks per change](#checks-per-change)) ([section 8](#8-workers-merge-and-watch-their-own-pull-requests)). `go`, `app` and `browser` are the `ci` jobs on `main` after [#191](https://github.com/taskqorg/fleet/pull/191), which run on every pull request and, with this change, every merge group. `comment` and `docs-order` are never required: they run only on `pull_request` or `push`. If #191 lands with other job names, the ruleset uses those.
    * Require merge queue, set explicitly (ruleset rule `merge_queue`): `merge_method: MERGE` (as set on the live ruleset; a pull request's commits land as they are, so its branch must hold no merge commits, section 8), `max_entries_to_build: 5`, `min_entries_to_merge: 1`, `max_entries_to_merge: 5`, `min_entries_to_merge_wait_minutes: 0`, `check_response_timeout_minutes: 60` (a queue run waiting for hosted runners under load is not dropped at 30 minutes), `grouping_strategy: ALLGREEN` ("only merge non-failing pull requests"). 5 matches the 4 to 5 lanes; a wait of 0 merges a lone pull request at once. These are settings, not limits on lanes.
    * Block force pushes and deletion of `main`.
    * Bypass: repository admins only, for the rollback drill in [deploy/README.md](../../deploy/README.md#rollback-drill).
* **Repository setting** "Automatically delete head branches" on. GitHub then retargets stacked children after the parent merges.
* **`ci.yml`** adds `merge_group:` to `on:`. The required jobs `go` and `app` report on all three events with no path filter ([checks per change](#checks-per-change)). The docs report (`scripts/docs-report.ts`, warning only) picks its base itself (merge base on a pull request, `HEAD^` on a push). The `push` run on `main` stays: it feeds `docs-order` and the release gate and covers an admin bypass.
* **Worker**, after its `/code-review` and the `code-review` status: `gh pr merge <n> --auto` (the ruleset sets the method). The serial merge script is deleted. Nobody merges without the queue.
* **A removed pull request** (red queue run, conflict, timeout) shows the reason on its timeline. The worker that owns it reads the reason (`scripts/watch-landing.sh` prints it) and fixes the conflict or applies the flaky rule below.

### 2. Stacked pull requests

* Workers do not stack. A lane that needs one change on top of another waits for the first to merge, then branches from `main`.
* A stack that already exists: the worker enables auto-merge on the child only after its parent merged and the child's base is `main`. With auto-delete on, GitHub retargets the child itself. If the child shows conflicts, its lane rebases it on `main`. No new stacks ([section 8](#8-workers-merge-and-watch-their-own-pull-requests)).
* Nobody deletes a branch by hand while a pull request is based on it.

### 3. Flaky tests

* For a transient failure on an opted-in PR head, the worker uses `project-publishing rerun_failed`. Fleet records one round on the exact head and reads back the replacement attempt. Build and smoke failures go to the worker. See [PR-head CI recovery](../../fleet.md#project-publishing).
* GitHub's "re-run failed jobs" is never used on a queue run.
* When the queue removes a pull request for a failed check the first time, its worker enqueues it again once (`gh pr merge --auto` again). A second red run of the same pull request is a real failure; the pull request goes back to its lane.
* A test that failed once and passed on the same head is deleted in the next change (owner decision 2026-11-25, [checks per change](#checks-per-change)); the `flaky` issue is no longer kept.

### 4. Automatic release

Replaces [fleet-self-update.md section 1](fleet-self-update.md#1-release-workflow) trigger and gate. Signing and publishing stay as they are.

* **Speculative build in the merge queue** (2026-11-12; removed 2026-11-25, [checks per change](#checks-per-change): the merge queue runs only the `go` and `app` checks, and every release builds itself in `build` below; the gate's use of a speculative artifact stays for a `ci` run that has one): the merge queue commit is the commit `main` moves to, and its version is a function of that commit (below), so the `merge_group` run of `ci` builds the release in parallel with the tests: job `release-build` runs `deploy/build-release.sh` for both targets, the linux selftests and the migration check of section 5 (`deploy/migrate-check.sh`) against the window of that moment, and uploads the tarballs with `speculative.json` (version, commit, `checkedWith`; `previous` in the tarballs' `release.json` is the latest release when they were built: when a release published in between, the coordinator's version-behind flag starts one release late, an N-2 executor is not flagged until the next release; nothing else reads it). It runs on the warm PDS18 runner pool when it has a free slot (job `release-build-pds18`, through the main-pinned `pds18.yml`; [runner pool](../../deploy/infrastructure/pds18-ci/README.md#routing-and-fallback)), otherwise hosted. It is not a required check itself, but the required `app` check of a merge group fails unless `release-build` passed, so `main` never holds a commit whose release does not build, selftest or pass the migration check (it ran in `app-tests` as a `0.0.0` pack before, now removed). No macOS runner (owner decision, 2026-11-12): the darwin binaries are `CGO_ENABLED=0` cross-builds, `deploy/verify-release.sh` checks every binary of both targets is static (in `release-build` and in the fallback `build`), and each Mac's `fleet-update` runs the `selftest` of every binary it runs before switching and refuses the release otherwise (`refused`, reason `selftest`, [self-update section 3](fleet-self-update.md)); a macOS job only added up to 5 minutes of runner queue to each release. Not covered: the darwin `fleet-coordinator` and `fleet-gateway`, which no Mac runs. The gate takes the artifact only from the `ci` run it already qualified (exact commit, `merge_group` or `push` event), only when `release-build` (or `release-build-pds18 / run`, whichever ran) passed and `speculative.json` names the gate's commit and version; the `SHA256SUMS` digest must equal the one `release-build` announced in its own job annotation (no other job of the run can write it, so an artifact another job uploaded under the same name is refused); the gate waits at most 10 minutes for the job and records that digest, `migrate` and `publish` take the artifact only with that digest and tarballs matching it, and `publish` signs those bytes. When a release published after the queue run, its window moved; `deploy/window-covered.sh` decides whether the check still covers it: every version checked then keeps its asset digest, and every version that entered since has the same `go/`, `config/` and `deploy/` trees as the newest checked one, so its coordinator is that checked coordinator rebuilt: it builds the same database and serves on the new one exactly as the checked pair did. Any Go change in a new release runs the check again. Otherwise job `migrate` runs the migration check again on the same bytes against the current window (hosted, about 30 seconds with downloads), and `latest.json` always records the current window as `checkedWith`. Without a usable artifact (an admin push, a rollback drill, a version taken) the release builds itself as below.
* **Superseded publish**: GitHub refuses the workflow token, which cannot hold the `workflows` permission, a new tag on a commit whose `.github/workflows` differ from the head of `main` (HTTP 403 "Resource not accessible by integration"; run 43237511138 on 2026-11-12, when `main` had moved on to a merge that changed workflows). That head contains the commit, and its push started the next release run. Job `supersede` checks this before `publish` (`deploy/workflows-moved.sh`, `compare/<sha>...main`; an API error or a comparison of 300 files or more publishes as before), and `publish` checks again when the tag creation fails with exactly that 403, deleting its draft; and then ends green without publishing (push runs only; a dispatched older commit is released as it is); the next run publishes a release that contains the commit, under its own version.
* **Fallback build**: `build` runs on the self-hosted PDS18 pool (`deploy/infrastructure/pds18-ci`), whose disk is capped, so the job avoids writes: it restores no GitHub pnpm or Go cache (the runner keeps its own stores between runs; the hosted fallback still restores them), and it runs the migration check of section 5 in the same job on the tarball it just built instead of a second job with its own checkout and artifact download.

* **Trigger**: `on: push: branches: [main]`, plus `workflow_dispatch` with an optional `sha` input (a commit on `main` newer than the latest release; empty means the head of `main`).
* **Batching**: `concurrency: {group: release, cancel-in-progress: false}`. GitHub keeps at most one running and one pending run in a group; a newer pending run replaces the older pending one. While a release runs (about 2 minutes), every merge that lands is collected into the next release. No quiet-window logic.
* **Pause**: repository variable `FLEET_RELEASE_PAUSE`. Set to any value, a `push` run's `gate` ends successfully without releasing and writes "paused" to the job summary; a `workflow_dispatch` run ignores it, so a paused orchestrator can still release one chosen commit. Merges that land during a pause go out in the first release after it: after deleting the variable, the orchestrator runs `gh workflow run release` once. The orchestrator owns the variable (`gh variable set FLEET_RELEASE_PAUSE --body on`, `gh variable delete FLEET_RELEASE_PAUSE`). It stops new releases; it does not touch hosts. The per-host `autoUpdate` hold stays the way to stop one host from installing. Pausing is not needed to let a busy host catch up: hosts pace their installs ([host updates section 7](fleet-host-updates.md#7-release-pacing)).
* **Gate**: the commit (`sha`) is the dispatch input or the head of `main` when the gate starts; an older pending commit is superseded. The gate outputs `sha`; `build` builds it (not `$GITHUB_SHA`) and `publish` tags it. CI lookup: `gh run list --workflow ci --commit <sha> --event merge_group`, else `--event push`; every required job must have concluded `success`. The wait ends after 30 minutes (`timeout-minutes` on the gate, today 360) and fails the run.
* **Version**: computed, never typed. `1.<yymmdd>.<M>` of the commit itself (`deploy/release-version.sh`): `yymmdd` is the UTC date of its commit time and `M` its 0-based position among that day's first-parent commits of `main`, so the merge queue run and the gate compute the same version, and later commits on `main` get higher versions. The gate takes it when it is newer than every `v1.*` and `ui-*-v1.*` tag; otherwise (a rollback drill of an older commit, a version a release took before this scheme) it falls back to the UTC date of the gate run and one more than the highest tag of that day, and builds itself. A deleted release or orphan tag never has its number reused; the `release` concurrency group means two runs never allocate together. The shape is unchanged, so `fleet-update`, the coordinator's `olderVersion` and `healthyTarget` need no code change, and the next release compares newer than every release hosts run today.
* **Tag**: `deploy/publish-release.sh` creates a draft of `v<ver>` for `<sha>`, uploads the assets and publishes it, which creates the tag by API. It is idempotent: a run that finds this commit's draft of its version reuses it, keeps an asset whose sha256 digest matches and replaces any other, deletes a draft of the version made for another commit, and leaves a release already published for this commit as it is. GitHub API errors 5xx, 429 and network failures are retried with backoff, and after a failed create, upload or publish the state is read again before the next attempt; any other 4xx fails at once. So a run that died mid-publish (2026-11-25: HTTP 500 at publish, then a re-run's upload hit 422 "already exists" and its cleanup another 500) leaves nothing that blocks the next run of that commit. The tag-name and tagger-date checks of today's gate are removed: there is no human tag to check. A human-pushed `v*` tag no longer starts anything.
* **Environment `release`** (holds `FLEET_RELEASE_KEY`, used by `publish`): no required reviewers, deployment branches limited to `main`. A tag-only branch policy would block every run.

### 5. Compatibility across skipped versions

Before this change the `migrate-check` job proved only "release N's migrations, then release N-1's coordinator on that database". With a release per batch, PDS18 can be several releases behind when it installs (it was paused or down), and a rollback returns to that older version.

* **The migration check covers the last 5 published releases.** The newest published release builds the database first, as PDS18's was built; N then applies only its new migrations on top (2026-11-11: a fresh database alone hid a new migration that relied on an edited, already released one), and each of the 5 newest published releases' coordinators then starts on it in turn; each must answer `/api/health` 200. A released migration is never edited. `latest.json` records them as `checkedWith`. Each start takes seconds. The check is a step of `release-build` in the merge queue, of `migrate` when the window moved since, or of the fallback `build` (section 4), all through `deploy/migrate-check.sh`; on the PDS18 runner the recent releases' coordinators (`fleet-coordinator`, `config/` and `release.json`, with the sha256 digest of the asset they came from) stay extracted under `~/fleet-releases/v/<version>` of each pool slot's user, so a release is downloaded once per slot, when it first enters the window or when its asset digest changed (the gate passes the digests with `recent`), checked against that digest (an asset GitHub reports no digest for is fetched every run, unchecked, as before), and entries that left the window are removed; the throwaway PostgreSQL runs with `fsync` off (`initdb --no-sync`), which changes durability only, not what the migrations do. The guest must keep the runner user's IPC across logins ([runner notes](../../deploy/infrastructure/pds18-ci/README.md#logins-on-the-guest)). Why 5: PDS18 checks every 10 seconds and a release takes at least 3 minutes from merge, so in normal running PDS18 is at most one release behind; 5 covers an outage of about 15 minutes at the peak rate. It is a setting, not a limit on releases.
* **PDS18 beyond the checked window.** When PDS18 runs a version not in the candidate's `checkedWith`, `fleet-update pds18` still installs (fix forward; the pre-install dump is taken as always) and opens one needs-you question: "pds18: <running> to <candidate> rollback pair unchecked". Rollback, if needed, is then a manual decision with the dump.
* **A refused release** is skipped as today (`cycle` skips refused candidates). PDS18 installs the next release once one is published; its `checkedWith` includes the version PDS18 still runs, as long as that is within the last 5.
* **Executors** do not touch the database. The existing rule stands: the coordinator serves N-1 and N, older executors show `version-behind` and keep working, and every host protocol change is expand-then-contract. New here: the contract step (removing a field) merges only when `hosts_list` shows no host older than the release that added the replacement. The worker checks that before enabling auto-merge on such a pull request.
* **Dumps**: `fleet-update` keeps pre-install dumps of `current`, `previous` and the 5 highest refused versions (ordered by `newer()`), not of every refused version (today `update.go` keeps all refused ones).

### 6. Faster pickup on hosts

* **PDS18**: `fleet-update.timer` `OnUnitActiveSec` 10 seconds (was 5 minutes, then 1 minute; 2026-11-12: a UI release waited up to a minute for pickup) with `AccuracySec=1s` (systemd's default accuracy of 1 minute let it fire up to 60 s late). The fetch of `latest.json` sends `If-None-Match` with the last ETag, so an unchanged file is a 304 with no body.
* **Executors**: the poll reply already carries `target` (`go/coordinator/host_poll.go`), and the executor rewrites `update.json` (temp file, then rename) only when a value changes. A new trigger starts `fleet-update executor` when that file changes:
    * Each `fleet-update executor` run writes the trigger for the real data dir (`FLEET_DATA_DIR` or the default) and reloads it only when its content changed (`go/update/trigger.go`); no host edits a path.
    * Linux and WSL: `~/.config/systemd/user/fleet-update.path`, `PathChanged=<data dir>/update/update.json`, `Unit=fleet-update.service`, enabled with `systemctl --user enable --now`.
    * Mac: `~/Library/LaunchAgents/com.ufobe.fleet-update-watch.plist`, `WatchPaths` on `<data dir>/update/update.json`, running `launchctl kickstart gui/<uid>/com.ufobe.fleet-update`. A separate job, so the update job never boots itself out.
    * Not on PDS18: its executor is updated by `fleet-update pds18`, not by an executor-mode run.
    * The 5-minute timer stays as the fallback.
* No coordinator change and no new credential. The coordinator offers a target only after PDS18 marked it healthy, as today.
* **A failing poll is seen on the server** (2026-11-17: every poll of all three hosts answered 409 for about 2.5 hours; `lastSeenAt` kept moving, so hosts looked healthy without commands or a target). The target reaches a host only in a successful poll reply, and the executor's `pollOk: false` stays on the host. So the coordinator records the failure itself (`go/coordinator/host_poll_health.go`): any non-2xx poll answer except 410 sets `pollError {status, reason, message, since}` on the host's observation, shown by `hosts_list`, `system_versions` (the version overview: "polls failing", danger once the problem below is open, `pollProblem`) and the Hosts page. After 2 minutes it opens the owner problem `problem:host-poll:<host>` with fixed text (host id and reason code). A poll 4xx the request did not cause (not 400, 401 or 410) also goes to Bugsink with the host id as a tag, once per run of one failure. The next successful poll clears the record and withdraws the problem; the host's removal withdraws the problem. A host that stopped polling shows offline, not its old failure; its problem stays until it polls successfully or is removed.
* **One command never fails the poll.** Each pending command is prepared under its own savepoint. A command whose handoff or authorization fails for a lasting reason (an authority refusal, a missing or malformed stored row) is stopped with its reason (`refused: ...` for an authority refusal, `handoff failed: ...` otherwise) and never handed over; a refused pipeline start or script that no host has seen also cancels its execution. A conflict, a busy dependency or another database error leaves the command pending for the next poll and reaches Bugsink once. The other commands and the target still go out; only a deadlock, a serialization failure or a lost connection still fails the poll, for its retry.

For idle executors, worst case from enable-auto-merge to the last executor, PDS18 up: queue CI 4 min + waiting behind a running release 2 min + release 2 min + PDS18 timer 10 s + PDS18 settle up to 1.5 min + executor poll and settle under 1 min, about 12 minutes. An executor that was several releases behind jumps straight to the current target; it does not restart at each version in between. Active executor work and retained completion delivery defer replacement under [the drain contract](fleet-self-update.md#33-restarts); this waiting time has no forced-stop deadline. A self-delivering worker queues a retained task-linked continuation and finishes its native turn before host replacement, then verifies landing in that continuation.

### 7. Smoke and rollback on the host

* The health probe of [fleet-self-update.md section 3.4](fleet-self-update.md#34-health-and-rollback) gains a smoke step on PDS18. After the new coordinator and gateway answer health, `fleet-update pds18` waits up to 30 seconds for the still-running PDS18 executor to complete a poll against the new coordinator: its `update.json` (under `/home/fleet-agent/.local/state/fleet/executor/update/`) shows `pollOk: true` with `at` later than the restart. That proves host authentication, the database and the poll route end to end with no new credential and no new route. A missing poll counts as an unhealthy start, so three failures roll back and raise the existing needs-you question.
* An ops-route smoke (`/api/ops/...`) is not used: those routes need the browser secret, a named person and an Origin (`go/coordinator/main.go`, `authorize`), which `fleet-update` does not hold.
* The Access-path check `deploy/infrastructure/agent-access-smoke.sh` stays a manual check for agents. It is no longer a deploy step and nobody waits on a host version to run it.
* The deploy rule stands in this form: hosts install only releases published by `release.yml`; nobody installs a branch build or CI artifact on a host.

### Design approval

**Target.** Keep the existing required checks. Enroll `design-approval` in the main ruleset after qualifying its dedicated trusted issuer and the [Fleet owner decision path](../design-system/README.md#design-first). The verifier runs from a protected revision, with decision lookup and status writing outside any credential-free candidate build. It publishes explicit success for proven nonvisual PRs and groups. For visible changes it verifies the owner's active selection, retained capture evidence, recipe and covered impact on the exact PR head and combined queue tree. Recompute visual scope from both trees, including shared dependencies and semantic effects. Missing authority, evidence or coverage blocks. Release consumes the qualifying receipt for its exact source. Approval binds the visual inputs. Code review remains exact-head on every push.

`design_verifier_status` exposes the Fleet issuer registration and authenticated provider and check observations. The service reports using the project-scoped `fleet-design-verifier` machine identity. `design_verifier_evidence` resolves candidate task and writer identity from Fleet's exact-head managed publishing records and revalidates the active sealed decision and original bytes. The GitHub issuer key stays in its protected systemd credential. Installation, rotation and readiness are in the [verifier runbook](../../deploy/design-verifier/README.md).

Managed publishing has its own host mediator and project grant. `project-publishing/capabilities` and `project-publishing/status` read that path without exposing credentials. Its exact-head `code-review` status and the protected App's `design-approval` check establish different facts.

### 8. Workers merge and watch their own pull requests

Amendment of 2026-11-11, decided by the owner. Data from pull requests #171 to #270: the orchestrator enqueued all 77 queue entries by hand; 93 of 290 commits were merges of `main` or of a sibling branch (#207 merged `main` 8 times); the queue removed pull requests 15 times for a conflict and once for a failed check; CI failed 8 of 146 pull request runs. The time went into hand-offs and conflicts, not into CI. The orchestrator also relayed "X landed, merge main" and "released, run the live test" to each lane, and workers sat idle until it did.

**Review gate.**

* A worker runs `/code-review` on its whole branch (`git fetch origin`, then the diff `origin/main...HEAD`) before the first push and fixes the findings there; only then does it push and open the pull request. Reviewed after opening, each review fix was another push and another PR CI run while the pull request waited: in `delivery_history`, `pr_ci` rose from about 140 s to about 1000 s on 2026-11-16 and 17, while the CI run on the final head stayed about 160 s.
* Once the pull request is open, the worker posts the commit status `code-review` = `success` on its head, the commit it reviewed: `gh api repos/taskqorg/fleet/statuses/<sha> -f state=success -f context=code-review -f description="<review level, findings fixed>"`.
* The ruleset requires `code-review` next to `go` and `app`, so the queue refuses a pull request without it. A status belongs to one commit: any new push (a CI fix, a conflict rebase) drops it, so the worker reviews the whole branch again before that push and posts the status on the new head.
* In the queue, `.github/workflows/review-gate.yml` (only `on: merge_group`) reports a job named `code-review` on the group commit. It does not review anything: the pull request could only enter the queue with the status on its head. It runs on no other event, so no skipped `code-review` check ever satisfies the rule on a pull request.
* The status is a statement by the worker, not a security control: every lane uses the owner's GitHub login and could post it, and an admin can bypass the ruleset (`gh pr merge --admin`). Workers never use `--admin`. The status is still worth one command: the queue now refuses a pull request whose head was pushed after the review, which a rule in prose cannot do.

**Merge.**

* Then the worker runs `gh pr merge <n> --auto`. It does this only for its own pull request.
* The orchestrator keeps the veto: `gh pr merge <n> --disable-auto` takes a pull request out of the queue, and the worker does not enable it again until the orchestrator says so.

**Branches.**

* A worker brings its branch up to date by `git rebase origin/main` and `git push --force-with-lease`, never by merging `main` or a sibling branch into it. It rebases only when GitHub reports a conflict or the queue removed the pull request for one; an out-of-date branch without a conflict needs nothing, because the queue tests it on top of `main`.
* A pull request executes its selected affected families directly before entering the queue ([section 1](#1-merge-ruleset-and-merge-queue)); a heavy-family failure blocks entry on the pull request itself instead of wasting serial queue time, and any remaining queue failure removes the pull request under the flaky rule of section 3.
* On `pull_request` only, the `selection` job fails when the pull request's commits include a merge commit (`gh api repos/{repo}/pulls/<n>/commits`, any commit with two parents) and prints the rebase command. A merge group has no pull request number and its own commit is a merge, so the step does not run there.
* No stacks. A change that needs another one waits for it to merge, then branches from `main`. Section 2 still covers a stack that already exists.
* A migration is its own file, `go/coordinator/migrations/<yyyymmdd>T<hhmm>Z_<slug>.sql`; the name is its id in `schema_migration`. Two branches add two different files, so they never conflict in git or take the same id, and the runner applies every id a database lacks, whichever lands first. The numbered sections of `go/coordinator/schema.sql` (1 to 122) are closed: `go/coordinator/migrations_test.go` fails on a new or repeated number and names the file form.
* Generated files (`go/contract/*_gen.go`) are never merged by hand. On a conflict in one, take either side, run `cd go && go generate ./contract`, add the result and continue the rebase. Their schemas in `contracts/` are merged by hand, as source.

**Watch to the hosts.**

* Amendment of 2026-11-12, decided by the owner: by default a worker reports done once its pull request is green, reviewed and has auto-merge on. It does not wait for the merge, the release or the hosts. A task that needs the deployed result says so in its spec with one line, `Landing: wait — live check: <what>`; this is the task's completion requirement ([fleet.md](../../fleet.md), Completion), so the orchestrator or operator writes it, never the worker.
* A worker with that line runs `scripts/watch-landing.sh <n>` after enabling auto-merge and reports from its output. The script waits for the merge, then for the first release tag that contains the merge commit (`git tag --contains`; a release can carry several merges, so a run ID is never used), then for the observed auto-update hosts to meet the [landing requirement under release pacing](fleet-host-updates.md#what-is-shown) (`hosts_list` through the deployed app as `site-agents`). "Done" is the line `on hosts v<version>` (`on site <tag>` for a UI release), then the named live check.
* Landings nobody waits on are watched by the orchestrator: after each `worker_done` that leaves auto-merge on, it runs `scripts/watch-landings.sh` in the background. That runs `watch-landing.sh` for every open pull request with auto-merge on or in the merge queue and every one merged in the last hour, prints `#<n> <last line>` for each, and exits 1 if any line is not `on hosts`/`on site`. A removal from the queue, a veto (`auto-merge off`), GitHub's own disable of auto-merge (`auto-merge disabled by GitHub (<code>)`), a release that never comes (`timeout at: release`) and a host that refuses or rolls back (`timeout at: hosts, behind: ...`) all end there. The orchestrator sends a failure to the pull request's worker or a new one; the worker acts on it as in `AGENTS.md` step 4.
* `watch-landing.sh` stops after 45 minutes (three times the 15-minute target of this design; a setting of the script) and prints the stage it reached. A timeout is a finding to report, not a reason to wait longer.
* Fleet sending these events to waiting sessions itself is later work.

**Rollout.** `review-gate.yml` lands on `main` first; then the ruleset's `required_status_checks` gains `{"context": "code-review"}` with no `integration_id` (so both the commit status and the Actions job count), by `gh api -X PUT repos/taskqorg/fleet/rulesets/26997222` with the full current rules plus that entry. Until then the status is posted but not required.

**Shared files.**

* `docs/designs/fleet-stack.md` is split by part into separate design files, so lanes that change different parts touch different files.
* `contracts/ops.schema.json` is split by domain into `contracts/ops-<domain>.schema.json` (types only one domain reaches; shared ones stay in `ops.schema.json`, [contracts/README.md](../../contracts/README.md)), each generating `go/contract/ops_<domain>_gen.go` and `contracts/generated/ops-<domain>.ts`. The generators merge the domain files into the one `Operations` map and `Ops` type, so callers do not change.

**Measure.** Before (pull requests #171 to #270): merge commits per pull request 0.96 (93 of 97), queue entries by the orchestrator 77 of 77, conflict removals 15, relayed "landed" and "released" messages about two per pull request. After, over the next 100 pull requests, by the same GraphQL query (kept in the description of the pull request that adds this section): merge commits per pull request at most half of before; queue entries by the orchestrator only after a veto; time from "auto-merge enabled" to "on hosts" from `watch-landing.sh` output, median and worst case, against the 15-minute target.

## Alternatives Considered

* **Keep the serial merge script.** Rejected by author: every pull request waits for CI twice, and the queue gives the same "tested on top of `main`" guarantee with one run.
* **"Require branches to be up to date" without a queue.** Rejected by author: every pull request has to be updated and rerun after each merge, the cost the queue removes ([GitHub](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue)).
* **Date-free version `2.<count>.0`.** Rejected by author: needs the version regex in `go/update/update.go`, `go/coordinator/release.go` and `release.yml` to change in one release, and a major bump. The date trap came from a human typing the tag; the pipeline computing the UTC date removes it with no code change.
* **Tag pushed by the workflow to trigger `release.yml`.** Rejected: a tag pushed with `GITHUB_TOKEN` starts no workflow; a personal token would be a new secret.
* **Rerun failed jobs on flake.** Rejected by author: it turns a red check green on the same run and hides the flake.
* **Release on a schedule.** Rejected by author: adds up to one interval of delay; the concurrency group already batches.
* **Workflow notifies the coordinator after publish.** Rejected by author: needs the `fleet-machines` Access token as an Actions secret in a public repository to save about a minute.
* **Coordinator polls GitHub releases.** Rejected by author: the coordinator cannot install itself, and executors already get the target through the poll reply.
* **The migration check against the version running on PDS18 and the oldest host** (requested by the orchestrator). Rejected by author: CI would need a credential to read host state. Checking the last 5 releases covers PDS18 in normal running, and a needs-you question covers the rest.
* **PDS18 installs every intermediate release in sequence.** Rejected by author: needs a release list beyond `latest.json`, stalls on a refused release, and slows catch-up.
* **Smoke and rollback from a GitHub runner.** Rejected: runners cannot reach the Mac or WSL, and rollback already lives in `fleet-update`.
* **`.gitattributes` `merge=union` for shared indexes** (section 8). Rejected by author: GitHub's merge queue ignores merge drivers, and a union of JSON or Go lines produces invalid files.
* **Migration numbers assigned in the merge queue.** Rejected: it needs a step that rewrites the merged tree inside the queue, and a branch's own CI would run another number than the one that lands. Timestamp-named migration files (adopted 2026-11-26, after three number clashes in one hour) need no step: `schema_migration` is keyed by the text id, its `version` column is generated from numeric ids, so the 5 previous releases' coordinators still read it on rollback.
* **Files owned by lanes.** Rejected by author: it limits which lane can take which task, a hidden concurrency limit.
* **Fewer, larger pull requests.** Rejected by author: a larger change conflicts more and waits longer in review.
* **Generated Go files not committed.** Rejected by author for now: every build path (`go:build`, `deploy/build-release.sh`, `go vet` in CI, editors) would need `go generate` first; regenerating on a conflict removes the hand-merge for one rule.
* **Allow stacked pull requests under the rules of section 2.** Superseded by section 8: 10 of the last 100 pull requests merged a sibling branch and then `main` again; waiting for the first change to merge costs one queue run.

## Risks

* **One flaky test drops a whole group.** Groups behind a failing pull request are rebuilt and rerun. With "only merge non-failing pull requests" on, a flake costs one extra queue run per group behind it. The flaky rule keeps flakes visible so they get fixed.
* **A worker enqueues an unreviewed head.** The status is per commit, so a push after the review blocks the queue; a false status is caught only by the orchestrator reading the description. The veto removes such a pull request.
* **Watching costs tokens.** A worker that waits on `watch-landing.sh` in the background spends nothing while it sleeps; the 45-minute stop bounds a stuck wait.
* **The queue changes the docs report base.** If `merge_group.base_sha` is wrong, the docs report is warning only and blocks nothing.
* **A bad release reaches executors quickly.** Executors move only after PDS18 marked the coordinator side healthy; an executor that fails its own health rolls back alone, as today.
* **PDS18 down longer than 5 releases.** The install goes ahead with a needs-you question; a rollback in that window is manual.

## Detailed Implementation Plan

One pull request per item, in this order. Items 1 and 2 wait for #191: the ruleset names its jobs and the release gate checks them.

1. **Queue** (after #191 merges):
    * `.github/workflows/ci.yml`: add `merge_group:`; pass `merge_group.base_sha` to the docs report; `go test -json` failure summary.
    * `scripts/docs-report.ts`: accept the base from `ci.yml` on a merge group.
    * Ruleset and auto-delete setting by `gh api` (no file); the exact JSON goes in the pull request description.
    * `fleet.md` Verification: drop "Not built" from the merge-queue line.
    * Coordinator merge script: deleted (it lives outside this repository).
    * Check: two pull requests enabled together land after one queue run each, with no manual update.
2. **Release**:
    * `.github/workflows/release.yml`: trigger, dispatch `sha` input, concurrency, pause variable, gate with `sha` output, computed version, the migration check in `build` over 5 releases with `checkedWith`, tag by `deploy/publish-release.sh`.
    * Environment `release`: deployment branch `main`, no reviewers (settings, no file).
    * `.github/workflows/ci.yml`: the header comment about tag releases.
    * `docs/designs/fleet-self-update.md`: CUJ 1 rewritten as "a merge releases"; section 1 points here.
    * `deploy/README.md` Releases and Rollback drill: no hand tag; the drill sets `FLEET_RELEASE_PAUSE`, merges the breakage, dispatches its `sha` as D, merges the revert, dispatches the revert as E after the drill, then deletes the variable.
    * Check: two merges inside one release run produce one release that contains both.
3. **Hosts** (`fleet-update` code):
    * `go/update/update.go`: ETag fetch, `checkedWith` check and its needs-you question, dump retention, poll smoke step.
    * `deploy/infrastructure/fleet-update.timer`: 10 seconds, `AccuracySec=1s`.
    * `go/update/trigger.go`: the `.path` unit and the watch LaunchAgent, written by each executor-mode run.
    * `deploy/README.md` first-install steps: no trigger to copy; `fleet-update` writes it.
    * Check: a release reaches all hosts within 15 minutes; a release whose coordinator refuses the executor poll rolls back on PDS18 and opens the needs-you question.
4. **Workers merge** (section 8), one pull request per step, each from `main`:
    * This section, `fleet.md` Stack and Verification, `AGENTS.md` lifecycle, `scripts/watch-landing.sh`.
    * Same pull request: `.github/workflows/review-gate.yml`, the merge-commit step in the `go` job of `.github/workflows/ci.yml`. After it lands, the ruleset change of Rollout.
    * `docs/designs/fleet-stack.md` split; inbound links updated.
    * `contracts/ops.schema.json` split by domain; `go/gen/main.go`, `contracts/gen-ts.mjs`, `go/contract/contract.go`, `contracts/README.md`, imports of `contracts/generated/ops.ts`.
    * Check: the next pull request after step 2 is enqueued by its worker, and the queue refuses one without the status.

## Checks per change

Owner decision of 2026-11-25. It replaces the selected-gate qualification (test selection, PDS18 routing of test jobs, `-race` runs and the test suites of the time).

**Why.** Measured on the merge queue from 2026-11-16 to 2026-11-25: 500 runs, 148 failed (30 %); a run took 6 to 9 minutes (median about 6). The critical path was `go-tests (unit)` (about 300 s: the coordinator package 177 s and the executor 94 s under `-race`), the outside tests (278 tests, about 800 s of test time, 250 to 630 s per run) and the browser checks (about 170 s). The most frequent failures were flaky tests, not bugs (`TestDownloadEmptyFileProviderStalls` 8 times, the scaled definition-usage test 6, `TestAgentToolRowOvertakesOtherExecutionsEvents` 6, `TestForgeReviewRoute` 5). Every small change paid minutes of tests, and the suites filled the hosts with test clusters and caches.

**What runs on every pull request and merge group.** Two jobs in parallel, each about a minute, no selection and no path filter. Their names are the required checks `go` and `app`; with `code-review` they are the whole ruleset.

* `go`: `go generate ./...` must leave no diff, `go vet ./...` (and a windows/amd64 vet of `go/coordinator`), the four binaries built with `CGO_ENABLED=0`, `go test ./...` (what is left: the design rules, pure logic and the two schema drift checks `TestMigrationSqlcSchema` and `TestMigrationSchemaFiles`, which also regenerate `db/schema.sql` and `schema/*.sql`; no sleeps), and the smoke: `node tests/trial/run.ts`, the real coordinator binary on a fresh database of the runner's PostgreSQL, 11 checks over HTTP and SSE (browser and host credentials, a session and its execution, the host poll, receipts, events, the stream with resume, row-level visibility, concurrent writes).
* `app`: contract lint, no merge commits in the branch, the documentation checks, `pnpm install`, typecheck and bundle, the pure app unit tests that are left. On a merge group and a push it uploads the bundle as `fleet-ui` for the UI-only release.

**What no longer runs.** The race build (`-race`): a race in production shows in Bugsink and gets its own fix. The other PostgreSQL Go tests, every Go test over 0.3 s, the outside tests in `tests/go/`, the browser checks in `tests/ui/*.check.ts` and the executor replay tests are deleted; the pull request that did it lists each with the reason. The warning-only reports (design report, docs report, govulncheck, use-case drift) still run and never gate.

**Release.** The merge queue no longer builds the release. `release.yml` builds the archives, runs the Linux selftests and the migration check after the merge (its existing build path, on the PDS18 runner). A failure there publishes nothing; the change is fixed forward on `main`. `fleet-update` health and rollback on each host stay the deploy gate.

**How a change proves itself.** By the build and the smoke above, then on the deployed site: a behavior change names its check in the task (`Landing: wait — live check: <what>`, [section 8](#8-workers-merge-and-watch-their-own-pull-requests)) and the worker runs it on `https://fleet.ufobe.com` after the release lands.

**When a test may be added.** Only when the task asks for one. Without such a task a worker adds no test of any kind: no test of documents, of source text or of a fixed bug. A requested test runs in the `go` job within its minute: a design rule, pure logic, or one more smoke step in `tests/trial/slice.test.ts`.

**A failing test.** A test that fails once and passes on the same head is deleted in the next change. The check itself runs once per CI attempt. A failure of the smoke or of a build step is a real failure.

**Commands.** The same commands run locally and in CI:

* `pnpm run build`: typecheck, bundle and the four Go binaries.
* `pnpm run check`: everything the two jobs check (`scripts/check.sh`).

## Independent web UI publication

The first installation of this mechanism is a normal native release. After that bootstrap, PDS18's coordinator service sets `FLEET_UI_ROOT=/home/fleet/ui`; the running coordinator must contain the independent static handler before activation. The service candidate records this setting but does not install itself. Activate the same setting in the installed user unit or its drop-in, reload that user's systemd manager and restart the coordinator once. Read back the effective unit and health before qualifying subsequent UI-only delivery.

The existing release workflow classifies cumulative changes since the latest native release. Only paths under `app/src/`, `packages/ui/src/` and `packages/ui/icons.json` use independent publication. Other paths, mixed backend changes and unknown boundaries retain native publication. This classification does not narrow merge-candidate tests: the existing selector and full fallback still apply.

The `app` job of the qualifying merge queue run (or of the `push` run) uploads the UI tree it built as `fleet-ui`; the gate takes it from the `ci` run whose `app` check passed for the exact commit. The `go` job of the same run built the coordinator from the same Go sources and ran the smoke against it; a UI-only diff since the latest native release leaves those sources unchanged. A failed run blocks publication. The release does not build the UI again.

The PDS18 updater discovers UI releases for its exact running backend under the existing update lock. It verifies signed checksums, digest, archive paths and manifest compatibility before installation. UI and native staging/state are separate. Public UI directories are explicitly `0755` and files `0444`, independent of the root updater's umask. Hashed assets are retained in the UI root and conflicting bytes under the same asset name are rejected. This preserves lazy asset requests from open tabs across publication and rollback; retained assets are not pruned by native release cleanup.

Installation atomically selects the UI tree without restarting native services or migrating the database. `/ui-release.json` reports the selected UI identity; native health and host versions continue to identify native binaries. Health qualification must observe the selected UI version and backend pair. Interrupted installs resume qualification. Failed UI installs restore the previous tree, or bundled UI on first-install failure, and record a refusal. Backend rollback selects bundled UI automatically when the independent UI manifest names another backend. Screen membership is read from the same selected tree as its entry document, after registered routes and outside API dispatch.

Qualification distinguishes the one-time native bootstrap, subsequent UI archive publication, site observation and native host uptake. A UI publication does not mean native hosts upgraded. Report actual queue, setup, build, checks, merge, publication and site observation separately; missing timestamps and USD costs remain unknown.


## UI landing observations

The landing watcher recognizes native and backend-scoped UI tags ordered by their trailing numeric release version. Native releases retain hosts_list uptake and `on hosts <tag>`. UI releases require exact deployed kind/version/backendVersion/source commit plus healthy exact backend, then report `on site <UI tag>`; native host versions do not advance. Missing or mismatched metadata remains waiting, never success.

## Delivery health

**Current state (the Delivery page).** `delivery_status` reads each connected repository every minute with a key (every 6 minutes keyless, fleet only): open and recently closed pull requests, active workflow runs, releases, and through the key only the latest finished `pull_request` runs (tool `runs_completed_pr`, granted with `connections_set_tools`; without the grant failed checks stay unseen and nothing else changes) and the latest finished `merge_group` runs (tool `runs_completed_mq`, the same way). A pull request whose latest finished run on its head failed, with no run active, is `pr_failed`. An open pull request not in the merge queue and not set to enter it, whose latest finished merge-group run of some workflow failed or timed out after its head was first checked (no new commit since), is `mq_failed` (Queue failed: it left the queue and waits on a person); a cancelled run does not count, since the queue cancels a group when an entry ahead fails and starts it again. A pass that changes a project's rows writes `delivery-state:<project>`, which the change stream carries as kind `delivery`. A merged pull request's release is the first `v*` or (fleet repository only) `ui-<backend>-v<UI version>` release whose target is merged no earlier (both share one version sequence). A native release shows as installed when every counted live host meets the [executor requirement under release pacing](fleet-host-updates.md#delivery-evidence). A UI-only release installs on no host: it is installed when the site serves that UI version or a newer one (the coordinator's selected UI, the same identity `/ui-release.json` reports and `watch-landing.sh` reads as `on site`), and Installing until then. GitHub lists releases not by date (native tags newest first, a `ui-*` tag behind its day's: `ui-1.261117.26-v1.261117.27` came 33rd, after `v1.261117.0`), so the pass adds to the 30 releases it reads the UI-only releases the history recorded from its 100, those published since the oldest one read. A new one is seen once the next history pass (10 minutes) recorded it.

Delivery is measured, compared with a baseline, and a worsening reaches the orchestrator without anyone looking (`go/coordinator/delivery_history.go`).

**What is measured.** The coordinator reads the fleet repository through the `github` connection every 10 minutes; its first pass backfills from the repository start (2026-11-08). The connection grants the read tools `pulls_closed_page`, `releases_page`, `workflow_runs_day`, `workflow_runs_sha` and `run_jobs` (`connections_set_tools`); these reads never fall back to the keyless API, and a refused one shows as the problem in `delivery_history`. The backfill stores runs and PR CI lookups as it reads, so after a deploy restarts the coordinator it lists the pages again but does not repeat those reads. It keeps one record per merged pull request, per release and per merge-queue run of the `ci` workflow:

| Record | Fields |
|---|---|
| Pull request | opened, PR CI done (first green `pull_request` run on the merged head), enqueued (first merge-queue run), queue CI done (green run on the merge commit), merged, release published (first `v*` or `ui-*` release whose target is merged no earlier), installed per host and on the PDS18 coordinator, UI on site; queue runs, removals (failed runs) and flakes (removals the pull request landed after without a new commit) |
| Merge-queue run | wall time, each job's duration, runner wait (created to started) and runner (GitHub-hosted or the PDS18 runner) |
| Release | published, target commit, installed per host, UI on site |

Stage durations are `pr_ci` (opened to PR CI done), `enqueue`, `queue`, `merge`, `release`, `install` (publication to a healthy exact-version PDS18 coordinator observation), `ui`, `lead` (opened to merged) and `total` (opened to that exact observation). A pull request delivered by a UI-only release has no `install`, `availability`, `time_to_available` or `ui`: its `ui_install` is publication to the site first serving that UI or a newer one, and `total` is opened to that moment. The two keys keep two populations apart: a native release's `ui` is mostly its coordinator install and restart. The UI on site is the first time the coordinator saw the site serve the version: the minute `delivery_status` pass notes it, and the 10-minute history pass records it. Every pass derives these from the stored records again, so records stored before a rule changes get it on the next pass. `availability` measures publication to a healthy same-or-newer version; `time_to_available` starts at PR opening. Pending availability waits use `availability_wait`, never successful install samples; a UI-only release not on the site yet adds its wait so far as `ui_wait`, compared with the `ui_install` baseline. A new UI-only release triggers the check like a native one. Original installed timestamps remain unchanged; without recorded matching version evidence their exact-install basis is unknown. Version-specific updater observations retain actual state, time and reason, without inferring refusal for skipped releases. GitHub keeps no install record, so host installs and the UI on site are recorded from the first pass on (`installsFrom`).

**Baseline.** New snapshots use metric basis `delivered-v2` (total includes UI-only releases, to the UI on site). An `exact-install-v1` snapshot still compares `install` but not `total` or `ui`. A snapshot without a basis (older) compares neither `install`, `total` nor `ui`; other stage metrics remain usable under any basis. P50 and P90 (nearest rank) per stage, per merge-queue run and per job over the last 7 days, never before 2026-11-12 (the days before had manual releases and no merge queue; they stay in the history and the trend), and per UTC day for the trend. A named snapshot stores the rolling baseline under a name (the first: `2026-11-13 after delivery speed-up`) so a later window can be compared with it.

**Check.** After each new release and at least daily, the last 24 hours are compared with the baseline: the 7 days before them (from 2026-11-12 at the earliest, so fewer in the first week), or the snapshot named in the settings. A stage or job whose P50 or P90 exceeds the baseline by the ratio (default 1.5) and by at least the minimum difference (default 1 minute), with at least 5 values in both windows, opens one problem `problem:delivery:<key>:<metric>`. The problem names the numbers, the first pull request over the threshold and the one merged before it; the compare and run links are its data (`links`), never its words (OR-38). It closes itself after two clean checks, like every rule cause (`problemsSync`); a check run by hand (`delivery_regressions` with `check: true`) counts as one.

**Who acts.** The problem's owner is the system duty bot ([problems](fleet-system-management.md#problems-anomalies-with-a-bot-owner)): it reads `delivery_history` for the range, finds the change that made the stage slower, and has the fix made or closes the problem as a duplicate with the cause named. It reaches Needs you only through `problem_escalate`. Agents use the MCP tools `delivery_history`, `delivery_baseline` (rolling, snapshots, trend, settings), `delivery_regressions` (the last check; `check: true` runs one) and `delivery_baseline_set` (snapshot, remove, settings; owner or full access). People see the trend on the Delivery screen.
