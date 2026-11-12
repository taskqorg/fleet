# Fleet delivery pipeline

* **Author:** Alex Kirs
* **Date:** 2026-11-11
* **Status:** the merge queue (section 1) and the automatic release (section 4) run on `main`; section 8 is being built. Amends [fleet-self-update.md](fleet-self-update.md): Goals, the version decision, CUJ 1, section 1 (release trigger and gate), 3.4 (health) and 3.5 (units). Section 8 (2026-11-11, owner decision) moves enabling auto-merge from the orchestrator to the worker that owns the pull request.

**Terms.** *Orchestrator*: the one agent session that coordinates the lanes and holds the dequeue veto for the project ([one orchestrator](../../fleet.md)); it is not the coordinator service. *Coordinator*: the Fleet service on PDS18 ([fleet-self-update.md](fleet-self-update.md#terms)). *Batch*: the commits that land on `main` while one release run is busy.

## Overview

A pull request travels: review, merge to `main`, release, install on every host. Today each step waits on a person or a timer. This design hands merging to GitHub's merge queue and releases every green `main` batch without a tag. PDS18 learns of a release within a minute instead of five; executors start their update as soon as their poll brings a new target. The `fleet-update` health probe stays the only deploy gate and keeps its rollback.

Targets, measured from "the worker enables auto-merge on its reviewed, green pull request":

* On `main` after one queue CI run (about 4 minutes when the queue is empty).
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
* A worker enables auto-merge on its own pull request after `/code-review`, and only then; GitHub merges. The orchestrator can take any pull request out of the queue ([section 8](#8-workers-merge-and-watch-their-own-pull-requests)).
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
worker: /code-review, code-review status ──► gh pr merge --auto <n>   (orchestrator: dequeue veto)
                                             │
GitHub merge queue ── merge_group run of ci (go, app, browser) on main+ahead+PR
        │ green: main moves to the tested commit   red: PR removed, its worker fixes
        ▼
push to main ──► release.yml (on: push main; concurrency: release, no cancel)
        gate: FLEET_RELEASE_PAUSE unset; sha = head of main; ci green for sha
        version 1.<UTC yymmdd of the run>.<next M>; build sha + migrate-check against the last 5 releases
        (one job on the PDS18 runner); selftest-macos; publish (signed SHA256SUMS, latest.json, tag by API)
        ▼
PDS18 fleet-update (timer 1 min, conditional GET of latest.json) ── install, health, smoke,
        3 starts ─► rollback; healthy ─► coordinator healthyTarget = new version
        ▼
executor poll reply carries target ─► executor rewrites update.json
        ─► systemd .path / launchd WatchPaths starts fleet-update executor at once
```

### 1. Merge: ruleset and merge queue

* **Ruleset** on `main` (repository ruleset, created with `gh api repos/taskqorg/fleet/rulesets`), enforcement active:
    * Require a pull request (no required approvals: review is the coordinator's `/code-review`, not a GitHub approval).
    * Require status checks `go`, `app`, `browser` and `code-review` ([section 8](#8-workers-merge-and-watch-their-own-pull-requests)). `go`, `app` and `browser` are the `ci` jobs on `main` after [#191](https://github.com/taskqorg/fleet/pull/191), which run on every pull request and, with this change, every merge group. `comment` and `docs-order` are never required: they run only on `pull_request` or `push`. If #191 lands with other job names, the ruleset uses those.
    * Require merge queue, set explicitly (ruleset rule `merge_queue`): `merge_method: MERGE` (as set on the live ruleset; a pull request's commits land as they are, so its branch must hold no merge commits, section 8), `max_entries_to_build: 5`, `min_entries_to_merge: 1`, `max_entries_to_merge: 5`, `min_entries_to_merge_wait_minutes: 0`, `check_response_timeout_minutes: 30`, `grouping_strategy: ALLGREEN` ("only merge non-failing pull requests"). 5 matches the 4 to 5 lanes; a wait of 0 merges a lone pull request at once. These are settings, not limits on lanes.
    * Block force pushes and deletion of `main`.
    * Bypass: repository admins only, for the rollback drill in [deploy/README.md](../../deploy/README.md#rollback-drill).
* **Repository setting** "Automatically delete head branches" on. GitHub then retargets stacked children after the parent merges.
* **`ci.yml`** adds `merge_group:` to `on:`. Every required job runs on all three events with no path filter. The docs report (`scripts/docs-report.ts`) picks its base itself (merge base on a pull request, `HEAD^` on a push); on a merge group `ci.yml` passes `github.event.merge_group.base_sha` as its base. The `push` run on `main` stays: it feeds `docs-order` and covers an admin bypass.
* **Worker**, after its `/code-review` and the `code-review` status: `gh pr merge <n> --auto` (the ruleset sets the method). The serial merge script is deleted. Nobody merges without the queue.
* **A removed pull request** (red queue run, conflict, timeout) shows the reason on its timeline. The worker that owns it reads the reason (`scripts/watch-landing.sh` prints it) and fixes the conflict or applies the flaky rule below.

### 2. Stacked pull requests

* Workers do not stack. A lane that needs one change on top of another waits for the first to merge, then branches from `main`.
* A stack that already exists: the worker enables auto-merge on the child only after its parent merged and the child's base is `main`. With auto-delete on, GitHub retargets the child itself. If the child shows conflicts, its lane rebases it on `main`. No new stacks ([section 8](#8-workers-merge-and-watch-their-own-pull-requests)).
* Nobody deletes a branch by hand while a pull request is based on it.

### 3. Flaky tests

* Nothing reruns a failed job to make it green. GitHub's "re-run failed jobs" is not used on queue runs.
* `go test` runs with `-json`. On failure, a step appends the failed test names to the job summary.
* When the queue removes a pull request for a failed check the first time, its worker enqueues it again once (`gh pr merge --auto` again), with no judgement about which test failed. The new queue run is a fresh full pass.
* If that pass is green on the same pull request head, the failed test is flaky: the worker adds the test name, both run links and the date to the open issue labelled `flaky` (one issue, created if missing). The red run stays red in history.
* A test recorded twice in that issue within 7 days is fixed next, by the lane that last changed the test's file. A fix that cannot land the same day may skip the test with `t.Skip("flaky: <issue link>")`; the fix removes the skip.
* A second red run of the same pull request is a real failure; the pull request goes back to its lane.

### 4. Automatic release

Replaces [fleet-self-update.md section 1](fleet-self-update.md#1-release-workflow) trigger and gate. `selftest-macos`, signing and publishing stay as they are. `build` runs on the self-hosted PDS18 runner (`deploy/infrastructure/pds18-ci`), whose disk is capped at 20 MB/s, so the job avoids writes: it restores no GitHub pnpm or Go cache (the runner keeps its own stores between runs; the hosted fallback still restores them), and it runs the migration check of section 5 in the same job on the tarball it just built instead of a second job with its own checkout and artifact download. The run took 3.4 minutes before this (2026-11-12, `build` 1.5 and `migrate-check` 1.2 of them, the check waiting about 40 seconds on throttled writeback).

* **Trigger**: `on: push: branches: [main]`, plus `workflow_dispatch` with an optional `sha` input (a commit on `main` newer than the latest release; empty means the head of `main`).
* **Batching**: `concurrency: {group: release, cancel-in-progress: false}`. GitHub keeps at most one running and one pending run in a group; a newer pending run replaces the older pending one. While a release runs (about 2 minutes), every merge that lands is collected into the next release. No quiet-window logic.
* **Pause**: repository variable `FLEET_RELEASE_PAUSE`. Set to any value, a `push` run's `gate` ends successfully without releasing and writes "paused" to the job summary; a `workflow_dispatch` run ignores it, so a paused orchestrator can still release one chosen commit. Merges that land during a pause go out in the first release after it: after deleting the variable, the orchestrator runs `gh workflow run release` once. The orchestrator owns the variable (`gh variable set FLEET_RELEASE_PAUSE --body on`, `gh variable delete FLEET_RELEASE_PAUSE`). It stops new releases; it does not touch hosts. The per-host `autoUpdate` hold stays the way to stop one host from installing.
* **Gate**: the commit (`sha`) is the dispatch input or the head of `main` when the gate starts; an older pending commit is superseded. The gate outputs `sha`; `build` builds it (not `$GITHUB_SHA`) and `publish` tags it. CI lookup: `gh run list --workflow ci --commit <sha> --event merge_group`, else `--event push`; every required job must have concluded `success`. The wait ends after 30 minutes (`timeout-minutes` on the gate, today 360) and fails the run.
* **Version**: computed by the gate, never typed. `1.<yymmdd>.<M>`, where `yymmdd` is the UTC date of the gate run (`date -u +%y%m%d`) and `M` is one more than the highest existing tag `v1.<yymmdd>.*` (`git ls-remote --tags`), or 0 when there is none, so a deleted release or orphan tag never reuses a number. The `release` concurrency group means two runs never compute the same `M`. The shape is unchanged, so `fleet-update`, the coordinator's `olderVersion` and `healthyTarget` need no code change, and the next release compares newer than every release hosts run today.
* **Tag**: `gh release create v<ver> --target <sha> --latest` creates the tag by API. The tag-name and tagger-date checks of today's gate are removed: there is no human tag to check. A human-pushed `v*` tag no longer starts anything.
* **Environment `release`** (holds `FLEET_RELEASE_KEY`, used by `publish`): no required reviewers, deployment branches limited to `main`. A tag-only branch policy would block every run.

### 5. Compatibility across skipped versions

Today `migrate-check` proves only "release N's migrations, then release N-1's coordinator on that database". With a release per batch, PDS18 can be several releases behind when it installs (it was paused or down), and a rollback returns to that older version.

* **`migrate-check` covers the last 5 published releases.** The newest published release builds the database first, as PDS18's was built; N then applies only its new migrations on top (2026-11-11: a fresh database alone hid a new migration that relied on an edited, already released one), and each of the 5 newest published releases' coordinators then starts on it in turn; each must answer `/api/health` 200. A released migration is never edited. `latest.json` records them as `checkedWith`. Each start takes seconds. The check is a step of `build` (section 4): the recent releases' coordinators (`fleet-coordinator` and `config/`) stay extracted under `~/fleet-releases/v/<version>` on the runner, so a release is downloaded once, when it first enters the window, and entries that left the window are removed; the throwaway PostgreSQL runs with `fsync` off (`initdb --no-sync`), which changes durability only, not what the migrations do. Why 5: PDS18 checks every minute and a release takes at least 3 minutes from merge, so in normal running PDS18 is at most one release behind; 5 covers an outage of about 15 minutes at the peak rate. It is a setting, not a limit on releases.
* **PDS18 beyond the checked window.** When PDS18 runs a version not in the candidate's `checkedWith`, `fleet-update pds18` still installs (fix forward; the pre-install dump is taken as always) and opens one needs-you question: "pds18: <running> to <candidate> rollback pair unchecked". Rollback, if needed, is then a manual decision with the dump.
* **A refused release** is skipped as today (`cycle` skips refused candidates). PDS18 installs the next release once one is published; its `checkedWith` includes the version PDS18 still runs, as long as that is within the last 5.
* **Executors** do not touch the database. The existing rule stands: the coordinator serves N-1 and N, older executors show `version-behind` and keep working, and every host protocol change is expand-then-contract. New here: the contract step (removing a field) merges only when `hosts_list` shows no host older than the release that added the replacement. The worker checks that before enabling auto-merge on such a pull request.
* **Dumps**: `fleet-update` keeps pre-install dumps of `current`, `previous` and the 5 highest refused versions (ordered by `newer()`), not of every refused version (today `update.go` keeps all refused ones).

### 6. Faster pickup on hosts

* **PDS18**: `fleet-update.timer` `OnUnitActiveSec` 1 minute (was 5). The fetch of `latest.json` sends `If-None-Match` with the last ETag, so an unchanged file is a 304 with no body.
* **Executors**: the poll reply already carries `target` (`go/coordinator/host_poll.go`), and the executor rewrites `update.json` (temp file, then rename) only when a value changes. A new trigger starts `fleet-update executor` when that file changes:
    * Each `fleet-update executor` run writes the trigger for the real data dir (`FLEET_DATA_DIR` or the default) and reloads it only when its content changed (`go/update/trigger.go`); no host edits a path.
    * Linux and WSL: `~/.config/systemd/user/fleet-update.path`, `PathChanged=<data dir>/update/update.json`, `Unit=fleet-update.service`, enabled with `systemctl --user enable --now`.
    * Mac: `~/Library/LaunchAgents/com.ufobe.fleet-update-watch.plist`, `WatchPaths` on `<data dir>/update/update.json`, running `launchctl kickstart gui/<uid>/com.ufobe.fleet-update`. A separate job, so the update job never boots itself out.
    * Not on PDS18: its executor is updated by `fleet-update pds18`, not by an executor-mode run.
    * The 5-minute timer stays as the fallback.
* No coordinator change and no new credential. The coordinator offers a target only after PDS18 marked it healthy, as today.

Worst case from enable-auto-merge to the last executor, PDS18 up: queue CI 4 min + waiting behind a running release 2 min + release 2 min + PDS18 timer 1 min + PDS18 settle up to 1.5 min + executor poll and settle under 1 min, about 12 minutes. An executor that was several releases behind jumps straight to the current target; it does not restart at each version in between.

### 7. Smoke and rollback on the host

* The health probe of [fleet-self-update.md section 3.4](fleet-self-update.md#34-health-and-rollback) gains a smoke step on PDS18. After the new coordinator and gateway answer health, `fleet-update pds18` waits up to 30 seconds for the still-running PDS18 executor to complete a poll against the new coordinator: its `update.json` (under `/home/fleet-agent/.local/state/fleet/executor/update/`) shows `pollOk: true` with `at` later than the restart. That proves host authentication, the database and the poll route end to end with no new credential and no new route. A missing poll counts as an unhealthy start, so three failures roll back and raise the existing needs-you question.
* An ops-route smoke (`/api/ops/...`) is not used: those routes need the browser secret, a named person and an Origin (`go/coordinator/main.go`, `authorize`), which `fleet-update` does not hold.
* The Access-path check `deploy/infrastructure/agent-access-smoke.sh` stays a manual check for agents. It is no longer a deploy step and nobody waits on a host version to run it.
* The deploy rule stands in this form: hosts install only releases published by `release.yml`; nobody installs a branch build or CI artifact on a host.

### 8. Workers merge and watch their own pull requests

Amendment of 2026-11-11, decided by the owner. Data from pull requests #171 to #270: the orchestrator enqueued all 77 queue entries by hand; 93 of 290 commits were merges of `main` or of a sibling branch (#207 merged `main` 8 times); the queue removed pull requests 15 times for a conflict and once for a failed check; CI failed 8 of 146 pull request runs. The time went into hand-offs and conflicts, not into CI. The orchestrator also relayed "X landed, merge main" and "released, run the live test" to each lane, and workers sat idle until it did.

**Review gate.**

* A worker runs `/code-review` on its final head. When the review has no open finding, the worker posts the commit status `code-review` = `success` on that head: `gh api repos/taskqorg/fleet/statuses/<sha> -f state=success -f context=code-review -f description="<review level, findings fixed>"`.
* The ruleset requires `code-review` next to `go`, `app` and `browser`, so the queue refuses a pull request without it. A status belongs to one commit: any new push drops it, and the worker posts it again after reviewing the new head. A worker rebases only to resolve a conflict (below), so a rebased head always holds new hand-made lines and is reviewed again.
* In the queue, `.github/workflows/review-gate.yml` (only `on: merge_group`) reports a job named `code-review` on the group commit. It does not review anything: the pull request could only enter the queue with the status on its head. It runs on no other event, so no skipped `code-review` check ever satisfies the rule on a pull request.
* The status is a statement by the worker, not a security control: every lane uses the owner's GitHub login and could post it, and an admin can bypass the ruleset (`gh pr merge --admin`). Workers never use `--admin`. The status is still worth one command: the queue now refuses a pull request whose head was pushed after the review, which a rule in prose cannot do.

**Merge.**

* Then the worker runs `gh pr merge <n> --auto`. It does this only for its own pull request.
* The orchestrator keeps the veto: `gh pr merge <n> --disable-auto` takes a pull request out of the queue, and the worker does not enable it again until the orchestrator says so.

**Branches.**

* A worker brings its branch up to date by `git rebase origin/main` and `git push --force-with-lease`, never by merging `main` or a sibling branch into it. It rebases only when GitHub reports a conflict or the queue removed the pull request for one; an out-of-date branch without a conflict needs nothing, because the queue tests it on top of `main`.
* On `pull_request` only, the `go` job fails when the pull request's commits include a merge commit (`gh api repos/{repo}/pulls/<n>/commits`, any commit with two parents) and prints the rebase command. A merge group has no pull request number and its own commit is a merge, so the step does not run there.
* No stacks. A change that needs another one waits for it to merge, then branches from `main`. Section 2 still covers a stack that already exists.
* Migration numbers in `go/coordinator/schema.sql` increase; `go/coordinator/migrations_test.go` fails otherwise. A repeated number is otherwise silent: a database that already applied the first section skips the second.
* Generated files (`go/contract/*_gen.go`) are never merged by hand. On a conflict in one, take either side, run `cd go && go generate ./contract`, add the result and continue the rebase. Their schemas in `contracts/` are merged by hand, as source.

**Watch to the hosts.**

* After enabling auto-merge the worker runs `scripts/watch-landing.sh <n>` and reports from its output. The script waits for the merge, then for the first release tag that contains the merge commit (`git tag --contains`; a release can carry several merges, so a run ID is never used), then for every observed host with auto-update on to run that version or a newer one (`hosts_list` through the deployed app as `site-agents`).
* It stops after 45 minutes (three times the 15-minute target of this design; a setting of the script) and prints the stage it reached. A timeout is a finding the worker reports to the orchestrator, not a reason to wait longer.
* "Done" for a worker is the line `on hosts v<version>`, then the live check its task names. If the pull request leaves the queue, the script prints the reason and the worker fixes it.
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
* **`migrate-check` against the version running on PDS18 and the oldest host** (requested by the orchestrator). Rejected by author: CI would need a credential to read host state. Checking the last 5 releases covers PDS18 in normal running, and a needs-you question covers the rest.
* **PDS18 installs every intermediate release in sequence.** Rejected by author: needs a release list beyond `latest.json`, stalls on a refused release, and slows catch-up.
* **Smoke and rollback from a GitHub runner.** Rejected: runners cannot reach the Mac or WSL, and rollback already lives in `fleet-update`.
* **`.gitattributes` `merge=union` for shared indexes** (section 8). Rejected by author: GitHub's merge queue ignores merge drivers, and a union of JSON or Go lines produces invalid files.
* **Migration numbers by timestamp.** Rejected by author: `schema_migration.version` is an `int`, and numbers out of order break the 5-release rollback check; a collision is caught by `go/coordinator/migrations_test.go` (`TestMigrationNumbersIncrease`) and, for two pull requests in flight, by the queue run that holds both.
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
    * `.github/workflows/release.yml`: trigger, dispatch `sha` input, concurrency, pause variable, gate with `sha` output, computed version, `migrate-check` over 5 releases with `checkedWith`, tag by `gh release create --target`.
    * Environment `release`: deployment branch `main`, no reviewers (settings, no file).
    * `.github/workflows/ci.yml`: the header comment about tag releases.
    * `docs/designs/fleet-self-update.md`: CUJ 1 rewritten as "a merge releases"; section 1 points here.
    * `deploy/README.md` Releases and Rollback drill: no hand tag; the drill sets `FLEET_RELEASE_PAUSE`, merges the breakage, dispatches its `sha` as D, merges the revert, dispatches the revert as E after the drill, then deletes the variable.
    * Check: two merges inside one release run produce one release that contains both.
3. **Hosts** (`fleet-update` code):
    * `go/update/update.go`: ETag fetch, `checkedWith` check and its needs-you question, dump retention, poll smoke step.
    * `deploy/infrastructure/fleet-update.timer`: 1 minute.
    * `go/update/trigger.go`: the `.path` unit and the watch LaunchAgent, written by each executor-mode run.
    * `deploy/README.md` first-install steps: no trigger to copy; `fleet-update` writes it.
    * Check: a release reaches all hosts within 15 minutes; a release whose coordinator refuses the executor poll rolls back on PDS18 and opens the needs-you question.
4. **Workers merge** (section 8), one pull request per step, each from `main`:
    * This section, `fleet.md` Stack and Verification, `AGENTS.md` lifecycle, `scripts/watch-landing.sh`.
    * Same pull request: `.github/workflows/review-gate.yml`, the merge-commit step in the `go` job of `.github/workflows/ci.yml`. After it lands, the ruleset change of Rollout.
    * `docs/designs/fleet-stack.md` split; inbound links updated.
    * `contracts/ops.schema.json` split by domain; `go/gen/main.go`, `contracts/gen-ts.mjs`, `go/contract/contract.go`, `contracts/README.md`, imports of `contracts/generated/ops.ts`.
    * Check: the next pull request after step 2 is enqueued by its worker, and the queue refuses one without the status.

## Selected-gate qualification

With successful exact-base evidence, prose under `docs/` and root README uses the deterministic core. UI source and shared controls execute app and every browser check, including real DB/HTTP flows, keyboard focus, access/error handling and draft preservation. Executor, gateway and updater paths execute go plus app, retaining clean Linux/Darwin archives and Linux selftests before merge. Coordinator, shared contracts, observability, build/config/dependency inputs, new tests, selector changes and unknown paths retain full execution. Mixed known changes take the union; unknown consumers broaden. Failure, incomplete base evidence or FLEET_TEST_FULL restores full execution.

The core is contract lint, test-rule metadata/self-check, selector fixtures and documentation checks. Candidates with app execution also run the pure view-URL check. Metadata is not behavioral evidence. No suite is deleted. Diagnostic reports keep their warning semantics; moving them off the critical path can increase total compute through repeated setup. Existing full commands and the diagnostic budget remain the fallback; no new numeric speed target applies.

A comparison uses the pull request's event base and tested checkout candidate, the complete merge group's event base/head, or push before/after. Group impact covers every member. Both rename paths and deleted paths contribute. Missing or zero base, candidate mismatch, unavailable history, malformed/empty comparison or unknown paths run full. Runtime definitions, harnesses, dependencies, shared contracts/codegen and selector changes use full protection. All coordinator changes retain fresh database/HTTP and race tests; UI/shared URL changes retain their consumers. New test paths are unknown and therefore full.

Selection reads the most recent other CI run on the same branch. A failed or unfinished run forces full execution, keeping previously failing checks. A successful push CI for the exact base is always required, even when a previous branch run is green; missing state or an API error also forces full. This uses existing GitHub run records, not a failure ledger. Only successful prior evidence permits the narrow path. Nonempty repository variable `FLEET_TEST_FULL` restores full execution immediately on subsequent candidates, including after a missed failure; clearing it requires resolved protection and exact-candidate evidence.

Plans retain event/base/candidate, selector hash, changed paths, history confidence, proposed and actual families, fallback reasons and full-override state. Existing required go/app/browser names remain. App validates the plan against actual family results: selected families must succeed and omitted families must be skipped. Required go/browser wrappers succeed only after that validation. The verifier rechecks the candidate's actual Git diff, so a corrupted omission manifest fails. Missing/failed evidence blocks publication. Release uses the candidate's own verifier and exact-SHA immutable qualification artifact; it waits for required conclusions rather than diagnostic workflow completion. Main bypass candidates use the same evidence requirements.

The rollout follows shadow execution and independent source review. Further selection changes require inspecting named retained checks and comparing proposed omissions with actual full-run results before widening the qualified boundary. A missing relevant scenario remains a protection gap. Scheduled reports alone enforce no release veto: known material failures use existing release pause and prohibit manual publication until exact-candidate proof or an explicit owner protection decision. Native archive startup, migration compatibility, signing, provenance and rollback remain prepublication requirements. No scheduler, dependency service or general failure ledger is added.

## Independent web UI publication

The first installation of this mechanism is a normal native release. After that bootstrap, PDS18's coordinator service sets `FLEET_UI_ROOT=/home/fleet/ui`; the running coordinator must contain the independent static handler before activation. The service candidate records this setting but does not install itself. Activate the same setting in the installed user unit or its drop-in, reload that user's systemd manager and restart the coordinator once. Read back the effective unit and health before qualifying subsequent UI-only delivery.

The existing release workflow classifies cumulative changes since the latest native release. Only paths under `app/src/`, `packages/ui/src/` and `packages/ui/icons.json` use independent publication. Other paths, mixed backend changes and unknown boundaries retain native publication. This classification does not narrow merge-candidate tests: the existing selector and full fallback still apply.

A UI archive is built once from the exact qualified source commit and contains `release.json` with kind `ui`, UI version, source commit and exact backend version. Before signing, the workflow extracts that archive and runs the existing browser/API checks against the downloaded released backend binary, a fresh database and the candidate assets. A failed pair blocks publication. Protected signing and GitHub provenance cover the UI archive using the existing release authority. UI releases use `ui-<backend-version>-v<ui-version>` with `--latest=false`; native discovery at `latest/download/latest.json` is unchanged. Migration rollback checks select the last five native releases only. UI/native versions share the allocator, and an already published UI source is not republished for the same backend.

The PDS18 updater discovers UI releases for its exact running backend under the existing update lock. It verifies signed checksums, digest, archive paths and manifest compatibility before installation. UI and native staging/state are separate. Public UI directories are explicitly `0755` and files `0444`, independent of the root updater's umask. Hashed assets are retained in the UI root and conflicting bytes under the same asset name are rejected. This preserves lazy asset requests from open tabs across publication and rollback; retained assets are not pruned by native release cleanup.

Installation atomically selects the UI tree without restarting native services or migrating the database. `/ui-release.json` reports the selected UI identity; native health and host versions continue to identify native binaries. Health qualification must observe the selected UI version and backend pair. Interrupted installs resume qualification. Failed UI installs restore the previous tree, or bundled UI on first-install failure, and record a refusal. Backend rollback selects bundled UI automatically when the independent UI manifest names another backend. Screen membership is read from the same selected tree as its entry document, after registered routes and outside API dispatch.

Qualification distinguishes the one-time native bootstrap, subsequent UI archive publication, site observation and native host uptake. A UI publication does not mean native hosts upgraded. Report actual queue, setup, build, checks, merge, publication and site observation separately; missing timestamps and USD costs remain unknown.


## UI landing observations

The landing watcher recognizes native and backend-scoped UI tags ordered by their trailing numeric release version. Native releases retain hosts_list uptake and `on hosts <tag>`. UI releases require exact deployed kind/version/backendVersion/source commit plus healthy exact backend, then report `on site <UI tag>`; native host versions do not advance. Missing or mismatched metadata remains waiting, never success.
