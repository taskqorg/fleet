# Fleet host updates: one procedure for everything a host runs

* **Author:** Alex Kirs
* **Date:** 2026-11-26
* **Status:** approved by the owner on 2026-11-26 with options A1, B1 and C1 (section 4); being built in the order of section 5. Release pacing (section 7) approved by the owner on 2026-11-26. Section 1 lists what existed before. It amends [self-update](fleet-self-update.md#managed-agent-clis), the [delivery pipeline](fleet-delivery-pipeline.md) and the [Changes screen](fleet-system-management.md#changes-screen) (section 6).

A host runs more than Fleet's own binaries. It runs agent CLIs, the tools agent work needs, later project environments, and the operating system under them. Today only the Fleet release updates without a person. The agent CLI pins get a daily proposal that waits on a person. Host tools have no version, no owner and no update path. This design gives every updatable thing on a host one procedure with the same steps. A new kind adds rows to it, not a new mechanism.

## 1. What exists

### Decided (in the specs)

| Topic | Decision | Where |
|---|---|---|
| Release | Every green `main` batch releases by itself. The pipeline computes the version. `FLEET_RELEASE_PAUSE` pauses releasing. Hosts pace what they install (section 7). | [delivery pipeline §4](fleet-delivery-pipeline.md#4-automatic-release) |
| Order | PDS18 installs first. The coordinator offers executors a target only after PDS18 marked it healthy. | [self-update §3](fleet-self-update.md#3-fleet-update), [delivery §6](fleet-delivery-pipeline.md#6-faster-pickup-on-hosts) |
| Host verification | Signature, digest and `selftest` run before the switch. Health must answer within 30 s. Three unhealthy starts roll back to `previous` and refuse the version. | [self-update §3.2, §3.4](fleet-self-update.md#34-health-and-rollback) |
| Smoke | PDS18's health check waits for its executor's poll against the new coordinator. No smoke or rollback runs from a GitHub runner. | [delivery §7](fleet-delivery-pipeline.md#7-smoke-and-rollback-on-the-host), Non-Goals |
| Agent CLIs | `config/harness-pins.json` ships in the release. `fleet-update` verifies and caches the payloads, and the executor runs its release's own pins. Switching `current` switches Fleet and the CLIs together, including on rollback. | [self-update, Managed agent CLIs](fleet-self-update.md#managed-agent-clis) |
| Hold | Per host, `autoUpdate` is on, off, or held until a time. The holder or the owner sets it. | [self-update, Decisions](fleet-self-update.md#decisions-defaults-the-owner-can-overturn) |
| Lag | The coordinator serves N-1 and N executors. `version-behind` shows from N-2. Protocol changes are expand, then contract. | same |
| Owner signal | A refused or failed update opens one Needs you question per host and version (`update:<host>:<version>`), addressed to the holder. | `go/coordinator/release.go` |
| Out of scope | Release channels, staged rollouts, automatic database restore, an Access token in GitHub secrets. | [self-update](fleet-self-update.md#non-goals), [delivery](fleet-delivery-pipeline.md#non-goals) |
| Compatibility | Compatibility is "checked combinations of harnesses, models, skills, tools and environments, stored as versioned facts". Confirmed incompatibility blocks launch. An unchecked combination is visible, not incompatible. | [fleet.md, Observation and evaluation](../../fleet.md#observation-and-evaluation) |
| Visibility | The Changes screen has a row for the release and a row for the harness pins, each with a cell per host. The rail version block turns danger only for a failed update or an open poll problem. | [system management](fleet-system-management.md#changes-screen), [nav rail](../design-system/controls/nav-rail.md) |

### Implicit (code only)

* **Harness pin proposal.** `.github/workflows/harness-pins.yml` opens `automation/harness-pins` daily with the `GITHUB_TOKEN` and asks a person to qualify it. It exits while that pull request is open, so taskqorg/fleet#669 has stayed at the 2026-11-18 versions. Its CI waits for approval (`action_required`), because events made with that token start no workflow.
* **Known incompatibility is code.** `harnessrelease.ModelError` refuses Codex older than 0.162.0 with `gpt-6.2-sol`. It is not a stored fact.
* **Account probe.** `probeModels` (`go/executor/accounts.go`) already reads the models a login offers, without using quota: Codex `model/list`, Claude Code's stream-json `initialize`, Antigravity's `session/new`.
* **Host tools.** Capabilities come from three sources:
  * `tool:node` and `tool:pnpm` from `exec.LookPath`;
  * `tool:browser` from the Playwright binding files;
  * `ffmpeg:<version>` from `ffmpeg -version`, read once per executor process.

  Readiness runs `node --version` and `pnpm --version` with a clean PATH and the account HOME. No version of node, pnpm, Playwright, Chromium, gh or glab is recorded. Nothing installs, pins or updates them.
* **Project environments.** Container images for project work do not exist. Podman runs only Fleet's own ClickHouse and Garage on PDS18, with images pinned by digest.
* **OS packages.** No spec mentions them. Each holder updates them however they set up the machine.
* **Update records.** Each host keeps only its last update result (`update-state.json` `last`). The coordinator's delivery history records merged pull requests, releases and queue runs, not what each host installed when.

### Conflicts

1. **A person qualifies CLI pins.** The self-update goal says no host needs a human after its first install, yet the pin pull request waits on a person. It is the only update step that does.
2. **Compatibility as facts, or as code.** fleet.md asks for stored facts; `ModelError` is code. Qualification results are not stored anywhere Fleet can read.
3. **Capability PATH and readiness PATH differ.** Dispatch trusts `tool:node` found on the executor's PATH, but readiness runs node with a clean PATH. One can pass while the other fails.
4. **ffmpeg.** [Data foundation](fleet-data-foundation.md) P3-D4 says a host without ffmpeg "reports it". In code, a missing ffmpeg is only an absent capability.
5. **Staged rollouts.** They are a non-goal, yet one stage already exists: PDS18 installs first, then the rest. Nothing says what that first stage must prove for anything other than the Fleet binaries.
6. **Hand-installed CLIs.** [docs/pds-codex-environment.md](../pds-codex-environment.md) describes CLIs installed by hand into `~/.local/share/fleet/runtime/<cli>-<ver>`. The managed pins replaced that path.

## 2. One shape for every kind

Every updatable thing is a **component**, named by kind and name (`cli/codex`, `tool/node`). Each component has a **pin** checked into the repository: a version, an official URL and a digest per target. Pins ship inside the Fleet release. So one release identifies everything a host runs, and one rollback returns all of it.

Every component goes through the same ten steps:

| Step | What happens | Who acts |
|---|---|---|
| 1. Discover | A daily job reads the vendor's official release metadata. It writes the newest stable version that all of a component's targets can take. | GitHub workflow |
| 2. Propose | One pull request per kind (`automation/<kind>-pins`, `.github/workflows/pins.yml` and `pins-kind.yml`). It is rebuilt from the current `main` whenever the proposal changes, or when its `ci.yml` lags `main` (the dispatched `ci` runs the head's own copy): one commit, never a merge. The body lists each component's old and new version and the vendor changelog. | GitHub workflow |
| 3. Qualify offline | `deploy/qualify-pins.mjs` on a GitHub runner per target (linux-amd64, darwin-arm64) installs the current and the proposed pins the way `fleet-update` does. It runs each component as Fleet runs it, without an account. A check that passes on the current pins and fails on the proposed ones is a regression. | GitHub workflow |
| 4. Qualify on hosts | The coordinator sees the proposal and runs the kind's Lab qualification on each host of the target, with real accounts and the configured models or projects (section 3). The result is stored as compatibility facts: component, version, host, model or project, check and outcome. For agent CLIs (`go/coordinator/pins_qualify.go`): when the head's `qualify-offline` passed, the Lab experiment `pins:cli:<sha>` (pipeline `pins-qualify`, example `pins-qualify-cli`, profiles `pins-qualify-<cli>`) runs one trial per changed CLI, host with a ready account binding, and configured model, requested in the owner's name. Each trial's override `candidate` makes the executor stage the proposed payload into its candidate cache (`fleet-update stage-candidate`: vendor origin, digest, unpack, `--version`) and run it. The trials are the compatibility facts; the example's check reads the shell call's output and the Fleet tool's answer from the run's result file. **Coverage is per platform:** a trial proves its CLI and model for every host of its pin target (linux-amd64 covers PDS18 and WSL; darwin-arm64 the Mac). A host that is offline, or whose executor cannot run a candidate and whose update is held, refused or failed, is skipped by name (in the experiment's goal and the `qualify-hosts` text), but only when another host of its target runs the same CLI and model; a combination only a skipped host could run makes the qualification wait, and an executor that is merely updating makes it wait too. A skipped host still verifies each new CLI itself before it switches (step 8: digest and `--version`). A host without a ready account for a CLI runs no agent on it, so it adds no trial and no coverage need; it too only verifies the binary in step 8. Host tools have no host trial yet, so their proposals wait at this step. Turning the tool pins on is a change of its own: the installed `fleet-update` prepares a new release's payloads and refuses component names it does not know, so the release that adds components ships them with `enabled: false`, and they are turned on once every host runs it. PDS18 needs `deploy/infrastructure/fleet-browser-chrome.apparmor` loaded before the pinned Chromium can start its sandbox (loaded 2026-11-26). | Coordinator and executors (Lab) |
| 5. Merge | Step 3 posts the status `qualify-offline` on the proposal's head and dispatches `ci` there (the workflow token's push starts no workflow). A dispatched run's check runs belong to no pull request and the ruleset does not count them, so that run posts `go` and `app` as commit statuses on the commit it tested (job `dispatched`). When step 4 passes, the coordinator posts `qualify-hosts` and `code-review` through the connection `github-pins`. Its key is a fine-grained token for taskqorg/fleet only, with Commit statuses and Pull requests read and write and nothing else, and only the coordinator's own read token may use it. The provider grants two tools (`commit_status` for the access check, `status_create`), and `pinsPublish` (`go/coordinator/pins_publish.go`) refuses every branch but `automation/<kind>-pins` to `main`. `code-review` stands for the coordinator's mechanical review of the diff (only the kind's pin file; vendor origins and digests) together with the host trials. Enabling auto-merge needs Contents write, which this key must not hold: a repository admin's token with it could push to `main` past the ruleset. So the status event of that `code-review` starts `.github/workflows/pins-merge.yml`. It trusts nothing from the event, checks out and runs no pull request code, and reads only through the API: the proposal is open, its head is `automation/<kind>-pins` in taskqorg/fleet itself (not a fork) at this commit, its base is `main`, and its diff modifies only that kind's pin file; on the current head `qualify-offline` is success by the workflow's bot, and `qualify-hosts` and `code-review` are success by the `github-pins` key's user. Then it turns on auto-merge with the workflow token, and dispatches `ci` and `review-gate` on the queue's group branch when that token's enqueue starts no `merge_group` run (`review-gate` passes a dispatch only on a queue branch). It passes the merge queue like any pull request. | Workflow and coordinator |
| 6. Release | The normal automatic release, with nothing kind-specific. | Release workflow |
| 7. Roll out | PDS18 first, then every executor host with auto-update on, paced by section 7. | `fleet-update` |
| 8. Verify on the host | Before the switch: digest, unpack and executable, then one run of the component as the executor user (`--version` for a CLI or tool, the image digest for an environment). A failure refuses the release on that host, and the host stays on its current release. After the switch: the existing health check. | `fleet-update` |
| 9. Watch | For 24 hours after a version first runs on a host, the coordinator compares that host's failure share of agent runs on the version with its share on the other versions over the week before (`go/coordinator/pins_watch.go`, every 10 minutes from the delivery pass; `execution.harness_version`). It flags a version with at least 5 runs and 3 failures there whose share is 25 points or more above the other versions'. It then marks the merged proposal that pinned the version with the failed status `pins-watch` (`revert <component> <version>: ...`). | Coordinator |
| 10. Recover | A refused or rolled-back release is handled as today. A component that passed qualification but fails in step 9 gets a revert proposal: the next pins run (`deploy/pins-revert.mjs`) puts its pin back to the marked proposal's base version and adds the version to the pin file's `skip`, which discovery honours, so step 1 does not propose it again. The revert then goes through steps 3 to 7 like any proposal. | Coordinator and workflow |

### When the owner is asked

The owner is asked only on a failure, with one question per cause:

* **Step 3 or 4 failed:** `pins:<kind>:<head sha>`. It names each failing component, target, host, model or project, with the changelog. It is withdrawn when the proposal merges, closes or qualifies.
* **Step 8 refused or failed:** the existing `update:<host>:<version>`.
* **Step 9 raised a revert:** `pins:<kind>:revert:<component>@<version>`. It says the revert is already in flight, so the owner only reads it.

Nothing waits for an answer. The next vendor release is proposed and qualified by itself. A failed proposal never blocks other pull requests or releases.

### What the owner sees

* **Changes screen:** one row per component, with the pinned version and a cell per host, plus the open proposal and its qualification state.
* **Rail version block:** danger only for a failed host update or a revert in flight.
* **Live:** nothing, unless a qualification run is in progress. It shows there as an ordinary Lab run.

### What is recorded

* the pull request, its statuses and its body, on GitHub;
* compatibility facts for each qualification, as Lab assessments that are never rewritten and that agents can read;
* per host, the history of every update attempt with its version, state and reason, not only the last one (new);
* an audit journal entry for each host's switch (new: today the switch is not journalled);
* the release's delivery history.

## 3. Per kind

| | Fleet release | Agent CLIs | Host tools | Project environments | OS packages |
|---|---|---|---|---|---|
| Components | coordinator, gateway, executor, `fleet-update`, UI | `cli/codex`, `cli/claude`, `cli/antigravity` | `tool/node` (with npm and npx), `tool/pnpm`, `tool/gh`, `tool/glab`, `tool/jq`, `tool/rg`, `tool/python` (python-build-standalone, with `python`, `python3` and `pip`), `tool/playwright` (playwright-core), `tool/chromium` (chrome-headless-shell) | `env/<project>`: a container image by digest | the host's OS |
| Pin | the release itself | `config/harness-pins.json` (exists) | `config/tool-pins.json` (on, same shape) | `execution.environment.image` in the project file (new) | none |
| 1 Discover | merge to `main` | vendor metadata (exists: `deploy/refresh-harness-pins.mjs`) | node LTS index; pnpm, gh, jq, ripgrep and python-build-standalone releases; glab releases; playwright-core on npm; Chrome for Testing Stable, whose digest is the SHA-256 of the download because Google publishes none | the project's own registry build | holder |
| 3 Offline | `go`, `app` and the merge queue (exist) | for each configured model: `--version`, protocol handshake with Fleet's flags, a session with a required MCP server, the structured output stream (built in taskqorg/fleet#1033, not merged) | `--version` and one real use each (`deploy/qualify-pins.mjs`): node runs a script and npm, jq filters, rg lists files, python runs a script and finds `python3` and `pip`, the Playwright module loads, Chromium renders a page | the image pulls and starts with the project's check command | none |
| 4 On hosts | none beyond step 8 | per host and account: the probe offers each configured model; one short run per configured model, with one tool call and one Fleet MCP call, through the executor's own adapter and record parser | per host: readiness with the pinned tool, on each project that requires it (`execution.tools`) | per host: readiness and the project's check in the image | none |
| 7 Order | PDS18, then executors | with the release | with the release | with the release | holder's schedule |
| 8 Host verify | signature, digest, `selftest`, health, PDS18 smoke (exist) | digest and `--version` (built in taskqorg/fleet#1033) | digest and `--version` | digest; `podman image inspect` | none |
| 9 Signals | health, poll failures (exist) | per harness version: runs failing in the native protocol, account probe errors | per tool version: readiness failures, tool check failures | per image: readiness and check failures | the OS version in capabilities, for comparison only |
| 10 Rollback | `fleet-update` (exists) | release rollback; revert proposal | release rollback; revert proposal | release rollback; revert proposal | holder |

Host tools become managed the way CLIs did. `fleet-update` caches the pinned payloads under the releases root. The executor puts the release's tool directories first on the PATH of every run and readiness check, which removes conflict 3. Playwright and Chromium stay off PATH: the executor points `FLEET_PLAYWRIGHT_MODULE` (playwright-core's `index.js`) and `FLEET_BROWSER_EXECUTABLE` at them for the project browser. A tool's `--version` in step 8 runs with the tools before it first on PATH, so Playwright finds the pinned node. A host that has not yet taken a release with tool pins keeps today's host tools, as `enabled: false` did for the CLIs.

ffmpeg, Podman itself and OS packages stay with the host's holder. They are system packages, need root, and are shared with whatever else the machine runs. Fleet reads their versions into capabilities and compatibility facts. A capability the executor cannot find is shown as missing, with its reason (conflict 4).

## 4. Options

### A. Where model-level qualification of agent CLIs runs (step 4): A1 chosen

* **A1. Before merge, on hosts, through the Lab (recommended).**
  * How it works: the coordinator reads the open proposal and sends each host a qualification command with the candidate pins. The executor downloads and verifies the candidate payloads into a separate qualification cache, with the same verification code as `fleet-update`. It runs the checks of section 3 with its own accounts and reports the results as Lab trials. The coordinator posts `harness-qualify` on the proposal's head through the `github` connection.
  * Gains: a bad CLI never merges and never blocks a release. The Mac and WSL produce their own evidence.
  * Costs: a new host command, the qualification cache, and the `statuses` write grant on the `github` connection.
* **A2. After release, as part of PDS18's health check.**
  * How it works: the release carries the new pins. `fleet-update pds18` switches, and the PDS18 executor runs the same checks. A failure counts as an unhealthy start, so it rolls back and refuses the release for every host.
  * Gains: nothing new on hosts or on GitHub.
  * Costs: a bad CLI blocks every other change in that release until the revert proposal lands (minutes, with no person). Only PDS18's platform and accounts are checked; the Mac's are not.

Recommendation: A1. It fits "never block other pull requests" and "compatibility as facts". A2 is the smaller change and reuses the existing gate.

### B. Whether host tools are pinned by Fleet: B1 chosen

* **B1. Pinned by Fleet, like the CLIs (recommended for node, pnpm, Playwright with Chromium, gh and glab; jq, ripgrep and Python added on 2026-11-26, because agent shells need them).** These are userland downloads with digests. Every host gets one version, a rollback includes the tools, and readiness and dispatch see the same binary.
* **B2. Left to the holder; Fleet only reports versions and readiness.** Nothing new to install. But hosts drift, and a broken tool shows only as failed runs.

### C. Rollout order (step 7): C1 chosen

* **C1. Keep today's order (recommended).** PDS18 goes first, then every executor at once. Step 4 already ran on every platform before the merge. Per-host refusal (step 8) and the watch (step 9) cover what is left. Staged rollouts stay a non-goal.
* **C2. Add a canary executor.** After PDS18, one named executor host takes the release. The other hosts take it only after that host finishes one qualification run on the new release, or after 30 minutes healthy. The coordinator keeps this hold per release, and the Mac or WSL waits for it.

## 5. Build order after approval

1. The CLI proposal, with offline qualification and automatic merge, renamed to this procedure's names and statuses (taskqorg/fleet#1033 holds a draft).
2. A1: the host qualification command, the Lab trials, and the status on the proposal.
3. The update history and journal entries for host switches, and the Changes rows per component (built: `host_update` rows written by the poll, `host_update_history`, the `components` of `system_changes`; [Changes](../design-system/pages/changes.md), [Hosts](../design-system/pages/hosts.md)).
4. The step 9 watch and the revert proposal.
5. Host tools (B1), the same way. Built first: every executor reports each tool's version on its poll (`toolVersions`: the pin and the managed executable's version, or, while tool pins are off, the version on its PATH), shown on Changes and a host's detail. Then the pins themselves: jq, ripgrep, Python, Playwright and Chromium added, with offline checks for each (`config/tool-pins.json` on since 2026-11-26, one release later). Not built: the host trial of step 4 for tools.
6. Project environments, once the environment work defines `execution.environment`.

## 6. Drift

| Spec | Before | With this design |
|---|---|---|
| [self-update, Managed agent CLIs](fleet-self-update.md#managed-agent-clis) | The pin pull request "uses the normal review and release path", so a person qualifies it. | It follows this procedure, and the section points here. |
| [self-update, Non-Goals](fleet-self-update.md#non-goals) | Staged rollouts are out of scope. | Unchanged with C1. With C2, the canary hold becomes a goal. |
| [delivery pipeline §7](fleet-delivery-pipeline.md#7-smoke-and-rollback-on-the-host) | The smoke proves the Fleet binaries only. | With A2, PDS18's health check also runs the changed components' checks. |
| [system management, Changes screen](fleet-system-management.md#changes-screen) | Rows for the release and the harness pins. | One row per component, with the open proposal's qualification state, and a per-host update history. |
| [configuration, Readiness](../configuration.md#readiness) | "Not built: … toolchain versions". | Readiness reports the pinned tool versions it ran. |
| [fleet.md, Observation and evaluation](../../fleet.md#observation-and-evaluation) | Compatibility as versioned facts, with no implementation. | Qualification writes those facts, and `harnessrelease.ModelError` becomes one of them. |
| [data foundation P3-D4](fleet-data-foundation.md) | A host without ffmpeg "reports it". | A missing system tool is a capability shown as missing, with its reason. |
| [pds-codex-environment](../pds-codex-environment.md) | Hand-installed CLI runtime directories. | Removed; the managed pins replace them. |

## 7. Release pacing

Owner decision of 2026-11-26. Releases are still built per merge batch, every few minutes. The pacing is at host install: a host installs a release only when the release changes what that host runs, and then at a time its urgency class allows. No model decides; every step below is a fixed rule. (Before: every executor installed every native release, drained for releases that changed only the web app or the coordinator, and the orchestrator paused releases by hand with `FLEET_RELEASE_PAUSE` so a busy host could catch up.)

### Parts by content digest

Each native release carries one digest per part. `deploy/release-parts.sh <commit>` computes them in the release `build` job: a part's digest is the SHA-256 of the sorted lines `<git blob id> <path>` of its files at the commit, without `*_test.go`, `*.md` and `testdata/`.

| Part | Files | Installed by |
|---|---|---|
| `executor` | every file of each package of the module that `fleet-executor` or `fleet-update` imports (`go list -deps`, for linux and darwin; embedded files included), `go/go.mod`, `go/go.sum`, `config/executor-defaults.json`, `deploy/hosts/`, `scripts/lab/` | every executor host |
| `cliPins` | `config/harness-pins.json` | every executor host |
| `toolPins` | `config/tool-pins.json` | every executor host |
| `coordinator` | the packages of `fleet-coordinator` and `fleet-gateway`, `go/go.mod`, `go/go.sum`, `config/`, `deploy/` | PDS18, coordinator side |
| `web` | `app/`, `packages/`, `package.json`, `pnpm-lock.yaml` | PDS18, coordinator side (UI-only changes keep their own UI release, [delivery pipeline](fleet-delivery-pipeline.md#independent-web-ui-publication)) |

The **host part** is `executor`, `cliPins` and `toolPins`: everything an executor host runs. `release.json` in the tarball and the release's `latest.json` carry `parts` (the five digests) and `executor`:

* `changedIn`: the newest release whose host part differs from its predecessor's. It is this release's version when the host part differs from the previous native release's, or when that release carries no digests; otherwise the previous release's `changedIn`.
* `urgentIn`: the newest release whose host part change was immediate. It is this release's version when the host part changed and the batch is immediate (below); otherwise the previous release's `urgentIn`, or empty.

A host that runs version `v` therefore runs the current host part when `v` is not older than `changedIn`, and it misses an immediate change when `v` is older than `urgentIn`, however many releases it skipped.

### Urgency classes

The release `gate` job classifies the batch (`deploy/release-urgency.sh`): all commits from the previous native release to the released commit.

* **Immediate** when any of these holds:
  * a pull request merged in the batch has the label `release:now` (a fix for broken work, or anything the author wants out now);
  * a pull request merged in the batch has the label `security`;
  * `contracts/host-protocol.schema.json` or `contracts/host-files.schema.json` changed (a host protocol change).
* **Normal** otherwise.

The class matters only for the host part. The coordinator side on PDS18 installs every native release at once, as before: its restart takes seconds and drains nothing. Later, a model may suggest the `release:now` label from the pull request text when these rules are unsure; it never decides ship or hold.

### When a host is offered a release

The coordinator offers a target in each poll reply, as before ([self-update section 4](fleet-self-update.md#4-coordinator-and-executor-changes)). It now decides per host and per poll, for host `H` running `v`, with `R` the healthy release it would offer (`go/coordinator/update_pacing.go`):

1. No `R`, or `v` is not older than `R`: offer `R` (nothing to install).
2. `R` carries no `executor.changedIn` (built before pacing): offer `R`.
3. `v` is not older than `R`'s `changedIn`: offer nothing. `H` runs the current host part; this release does not touch it. State `current`.
4. `v` is older than `urgentIn`: offer `R`. Class `immediate`, state `offered`.
5. `H` reports a drain episode in its poll: keep offering `R`, so an episode is never withdrawn half way. State `offered`.
6. `H` is idle: its poll lists no active execution. Offer `R`. State `offered`.
7. `H`'s local time is inside the daily window: offer `R`, even while `H` is busy. State `offered`.
8. Otherwise offer nothing. Class `normal`, state `deferred`, until `H` is idle or the window opens.

The daily window is `03:00-05:00` in the host's local time. The coordinator setting `FLEET_UPDATE_WINDOW` (`HH:MM-HH:MM`) changes it for every host. The executor sends its UTC offset in each poll (`utcOffsetMinutes`); an executor that sends none is treated as UTC.

Once offered, the release installs as before: the executor drains, `fleet-update` installs once the executor is drained, and the bounded drain (10 minutes fenced, then 30 minutes open, [self-update section 3.3](fleet-self-update.md#33-restarts)) caps how long a busy host refuses new starts. The per-host `autoUpdate` hold still wins over every rule above.

`fleet-update` installs only what it is offered:

* The PDS18 executor follows the coordinator's offer to it (the target in its `update.json`), not the coordinator side's version. A web or coordinator release leaves it running.
* For an executor part, `fleet-update` takes the activity lease before it downloads. While the executor is not drained, nothing is downloaded and the result is `pending`. (Before, PDS18's updater downloaded the release every 10 seconds while its executor drained.)

### What is shown

* `hosts_list` adds `pacing` to each host row: `release` (the coordinator's release), `state` (`current`, `offered`, `deferred`), `class` (`immediate`, `normal`) and `reason` in words, such as "executor unchanged since 1.261126.4" or "waits for idle or 03:00-05:00 local".
* `version-behind` is raised only when the host is older than `previous` and also older than `changedIn`: a host that skipped web and coordinator releases is not behind.
* The landing watcher (`scripts/watch-landing.sh`) counts a host whose pacing state is `current` as landed: the release does not change it. A `deferred` host is still waited for, so a worker whose live check needs its executor change on every host now labels the pull request `release:now`.

### Compatibility

* `utcOffsetMinutes` is an optional poll field; `pacing` an added `hosts_list` field. An older executor sends no offset (UTC window) and ignores nothing new.
* An older `fleet-update pds18` still follows the coordinator side's version. While it is not offered a target it records `pending` and installs nothing; the first healthy PDS18 install of this release replaces it (`/opt/fleet/update/fleet-update`).
* An older executor that reports no drain is fenced only when it is offered a release (`hostDrain`), so a deferred host takes work.

### What this removes

* Executors draining for releases that change only the web app, the coordinator or documents.
* The updater chasing releases: a busy host with a normal change is not offered each new release; it installs once, when idle or in the window.
* `FLEET_RELEASE_PAUSE` for pacing. The variable stays to stop all releasing in an emergency.
