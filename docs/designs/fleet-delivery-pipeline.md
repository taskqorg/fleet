# Fleet delivery pipeline

* **Author:** Alex Kirs
* **Date:** 2026-11-11
* **Status:** design, not built. Amends [fleet-self-update.md](fleet-self-update.md): Goals, the version decision, CUJ 1, section 1 (release trigger and gate), 3.4 (health) and 3.5 (units).

**Terms.** *Orchestrator*: the one agent session that reviews and merges for the project ([one orchestrator](../../fleet.md)); it is not the coordinator service. *Coordinator*: the Fleet service on PDS18 ([fleet-self-update.md](fleet-self-update.md#terms)). *Batch*: the commits that land on `main` while one release run is busy.

## Overview

A pull request travels: review, merge to `main`, release, install on every host. Today each step waits on a person or a timer. This design hands merging to GitHub's merge queue and releases every green `main` batch without a tag. PDS18 learns of a release within a minute instead of five; executors start their update as soon as their poll brings a new target. The `fleet-update` health probe stays the only deploy gate and keeps its rollback.

Targets, measured from "the orchestrator enables auto-merge on a reviewed, green pull request":

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
* Only the orchestrator merges. It does that by enabling auto-merge; workers never merge or enable auto-merge.
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
orchestrator: /code-review reported ──► gh pr merge --auto <n>
                                             │
GitHub merge queue ── merge_group run of ci (go, app, browser) on main+ahead+PR
        │ green: main moves to the tested commit   red: PR removed, orchestrator decides
        ▼
push to main ──► release.yml (on: push main; concurrency: release, no cancel)
        gate: FLEET_RELEASE_PAUSE unset; sha = head of main; ci green for sha
        version 1.<UTC yymmdd of the run>.<next M>; build sha; selftest-macos;
        migrate-check against the last 5 releases; publish (signed SHA256SUMS, latest.json, tag by API)
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
    * Require status checks `go`, `app` and `browser`: the `ci` jobs on `main` after [#191](https://github.com/taskqorg/fleet/pull/191), which run on every pull request and, with this change, every merge group. `comment` and `docs-order` are never required: they run only on `pull_request` or `push`. If #191 lands with other job names, the ruleset uses those.
    * Require merge queue, set explicitly (ruleset rule `merge_queue`): `merge_method: SQUASH`, `max_entries_to_build: 5`, `min_entries_to_merge: 1`, `max_entries_to_merge: 5`, `min_entries_to_merge_wait_minutes: 0`, `check_response_timeout_minutes: 30`, `grouping_strategy: ALLGREEN` ("only merge non-failing pull requests"). 5 matches the 4 to 5 lanes; a wait of 0 merges a lone pull request at once. These are settings, not limits on lanes.
    * Block force pushes and deletion of `main`.
    * Bypass: repository admins only, for the rollback drill in [deploy/README.md](../../deploy/README.md#rollback-drill).
* **Repository setting** "Automatically delete head branches" on. GitHub then retargets stacked children after the parent merges.
* **`ci.yml`** adds `merge_group:` to `on:`. Every required job runs on all three events with no path filter. The docs report (`scripts/docs-report.ts`) picks its base itself (merge base on a pull request, `HEAD^` on a push); on a merge group `ci.yml` passes `github.event.merge_group.base_sha` as its base. The `push` run on `main` stays: it feeds `docs-order` and covers an admin bypass.
* **Orchestrator**, after the review is reported: `gh pr merge <n> --auto` (the ruleset sets the method). That is its whole merge action. The serial merge script is deleted. Workers never run `gh pr merge`.
* **A removed pull request** (red queue run, conflict, timeout) shows the reason on its timeline. The orchestrator reads it and either sends the pull request back to its lane or applies the flaky rule below.

### 2. Stacked pull requests

* Workers do not stack. A lane that needs one change on top of another waits for the first to merge, then branches from `main`.
* When a stack is unavoidable, the orchestrator enables auto-merge on a child only after its parent merged and the child's base is `main`. With auto-delete on, GitHub retargets the child itself. If the child shows conflicts after a squash merge of the parent, its lane rebases it on `main`.
* Nobody deletes a branch by hand while a pull request is based on it.

### 3. Flaky tests

* Nothing reruns a failed job to make it green. GitHub's "re-run failed jobs" is not used on queue runs.
* `go test` runs with `-json`. On failure, a step appends the failed test names to the job summary.
* When the queue removes a pull request for a failed check the first time, the orchestrator enqueues it again once (`gh pr merge --auto` again), with no judgement about which test failed. The new queue run is a fresh full pass.
* If that pass is green on the same pull request head, the failed test is flaky: the orchestrator adds the test name, both run links and the date to the open issue labelled `flaky` (one issue, created if missing). The red run stays red in history.
* A test recorded twice in that issue within 7 days is fixed next, by the lane that last changed the test's file. A fix that cannot land the same day may skip the test with `t.Skip("flaky: <issue link>")`; the fix removes the skip.
* A second red run of the same pull request is a real failure; the pull request goes back to its lane.

### 4. Automatic release

Replaces [fleet-self-update.md section 1](fleet-self-update.md#1-release-workflow) trigger and gate. Build, `selftest-macos`, signing and publishing stay as they are.

* **Trigger**: `on: push: branches: [main]`, plus `workflow_dispatch` with an optional `sha` input (a commit on `main` newer than the latest release; empty means the head of `main`).
* **Batching**: `concurrency: {group: release, cancel-in-progress: false}`. GitHub keeps at most one running and one pending run in a group; a newer pending run replaces the older pending one. While a release runs (about 2 minutes), every merge that lands is collected into the next release. No quiet-window logic.
* **Pause**: repository variable `FLEET_RELEASE_PAUSE`. Set to any value, a `push` run's `gate` ends successfully without releasing and writes "paused" to the job summary; a `workflow_dispatch` run ignores it, so a paused orchestrator can still release one chosen commit. Merges that land during a pause go out in the first release after it: after deleting the variable, the orchestrator runs `gh workflow run release` once. The orchestrator owns the variable (`gh variable set FLEET_RELEASE_PAUSE --body on`, `gh variable delete FLEET_RELEASE_PAUSE`). It stops new releases; it does not touch hosts. The per-host `autoUpdate` hold stays the way to stop one host from installing.
* **Gate**: the commit (`sha`) is the dispatch input or the head of `main` when the gate starts; an older pending commit is superseded. The gate outputs `sha`; `build` builds it (not `$GITHUB_SHA`) and `publish` tags it. CI lookup: `gh run list --workflow ci --commit <sha> --event merge_group`, else `--event push`; every required job must have concluded `success`. The wait ends after 30 minutes (`timeout-minutes` on the gate, today 360) and fails the run.
* **Version**: computed by the gate, never typed. `1.<yymmdd>.<M>`, where `yymmdd` is the UTC date of the gate run (`date -u +%y%m%d`) and `M` is one more than the highest existing tag `v1.<yymmdd>.*` (`git ls-remote --tags`), or 0 when there is none, so a deleted release or orphan tag never reuses a number. The `release` concurrency group means two runs never compute the same `M`. The shape is unchanged, so `fleet-update`, the coordinator's `olderVersion` and `healthyTarget` need no code change, and the next release compares newer than every release hosts run today.
* **Tag**: `gh release create v<ver> --target <sha> --latest` creates the tag by API. The tag-name and tagger-date checks of today's gate are removed: there is no human tag to check. A human-pushed `v*` tag no longer starts anything.
* **Environment `release`** (holds `FLEET_RELEASE_KEY`, used by `publish`): no required reviewers, deployment branches limited to `main`. A tag-only branch policy would block every run.

### 5. Compatibility across skipped versions

Today `migrate-check` proves only "release N's migrations, then release N-1's coordinator on that database". With a release per batch, PDS18 can be several releases behind when it installs (it was paused or down), and a rollback returns to that older version.

* **`migrate-check` covers the last 5 published releases.** It migrates a database with N, then starts each of the 5 newest published releases' coordinators on it in turn and requires `/api/health` 200 from each. `release.json` records them as `checkedWith`. Each start takes seconds, so the job stays near its current minute. Why 5: PDS18 checks every minute and a release takes at least 3 minutes from merge, so in normal running PDS18 is at most one release behind; 5 covers an outage of about 15 minutes at the peak rate. It is a setting, not a limit on releases.
* **PDS18 beyond the checked window.** When PDS18 runs a version not in the candidate's `checkedWith`, `fleet-update pds18` still installs (fix forward; the pre-install dump is taken as always) and opens one needs-you question: "pds18: <running> to <candidate> rollback pair unchecked". Rollback, if needed, is then a manual decision with the dump.
* **A refused release** is skipped as today (`cycle` skips refused candidates). PDS18 installs the next release once one is published; its `checkedWith` includes the version PDS18 still runs, as long as that is within the last 5.
* **Executors** do not touch the database. The existing rule stands: the coordinator serves N-1 and N, older executors show `version-behind` and keep working, and every host protocol change is expand-then-contract. New here: the contract step (removing a field) merges only when `hosts_list` shows no host older than the release that added the replacement. The orchestrator checks that before enabling auto-merge on such a pull request.
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
* **Ban stacked pull requests outright.** Rejected by author: rare stacks are cheaper than the wait; the rules in section 2 make them safe.

## Risks

* **One flaky test drops a whole group.** Groups behind a failing pull request are rebuilt and rerun. With "only merge non-failing pull requests" on, a flake costs one extra queue run per group behind it. The flaky rule keeps flakes visible so they get fixed.
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
