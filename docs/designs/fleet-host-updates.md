# Fleet host updates: one procedure for everything a host runs

* **Author:** Alex Kirs
* **Date:** 2026-11-26
* **Status:** approved by the owner on 2026-11-26 with options A1, B1 and C1 (section 4); being built in the order of section 5. Section 1 lists what existed before. It amends [self-update](fleet-self-update.md#managed-agent-clis), the [delivery pipeline](fleet-delivery-pipeline.md) and the [Changes screen](fleet-system-management.md#changes-screen) (section 6).

A host runs more than Fleet's own binaries. It runs agent CLIs, the tools agent work needs, later project environments, and the operating system under them. Today only the Fleet release updates without a person. The agent CLI pins get a daily proposal that waits on a person. Host tools have no version, no owner and no update path. This design gives every updatable thing on a host one procedure with the same steps. A new kind adds rows to it, not a new mechanism.

## 1. What exists

### Decided (in the specs)

| Topic | Decision | Where |
|---|---|---|
| Release | Every green `main` batch releases by itself. The pipeline computes the version. `FLEET_RELEASE_PAUSE` pauses releasing. | [delivery pipeline §4](fleet-delivery-pipeline.md#4-automatic-release) |
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
| 2. Propose | One pull request per kind (`automation/<kind>-pins`). It is rebuilt from the current `main` whenever the proposal changes: one commit, never a merge. The body lists each component's old and new version and the vendor changelog. | GitHub workflow |
| 3. Qualify offline | A GitHub runner per target (linux-amd64, darwin-arm64) installs the current and the proposed pins the way `fleet-update` does. It runs each component as Fleet runs it, without an account. A check that passes on the current pins and fails on the proposed ones is a regression. | GitHub workflow |
| 4. Qualify on hosts | The coordinator sees the proposal and runs the kind's Lab qualification on each host of the target, with real accounts and the configured models or projects (section 3). The result is stored as compatibility facts: component, version, host, model or project, check and outcome. | Coordinator and executors (Lab) |
| 5. Merge | When steps 3 and 4 pass, the proposal gets the statuses `harness-qualify` (one per kind) and `code-review`, and auto-merge turns on. It passes the merge queue like any pull request. | Workflow and coordinator |
| 6. Release | The normal automatic release, with nothing kind-specific. | Release workflow |
| 7. Roll out | PDS18 first, then every executor host with auto-update on, as today. | `fleet-update` |
| 8. Verify on the host | Before the switch: digest, unpack and executable, then one run of the component as the executor user (`--version` for a CLI or tool, the image digest for an environment). A failure refuses the release on that host, and the host stays on its current release. After the switch: the existing health check. | `fleet-update` |
| 9. Watch | For 24 hours after a host switches, the coordinator compares the host's failure rate for the changed components with the week before. It counts runs that failed in the harness or the tool, and account probe errors. | Coordinator |
| 10. Recover | A refused or rolled-back release is handled as today. A component that passed qualification but fails in step 9 gets a revert proposal: step 2 again, with its pin back at the previous version. The revert marks that version as skipped, so step 1 does not propose it again. The revert then goes through steps 3 to 7 like any proposal. | Coordinator and workflow |

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
| Components | coordinator, gateway, executor, `fleet-update`, UI | `cli/codex`, `cli/claude`, `cli/antigravity` | `tool/node`, `tool/pnpm`, `tool/playwright` with its Chromium, `tool/gh`, `tool/glab` | `env/<project>`: a container image by digest | the host's OS |
| Pin | the release itself | `config/harness-pins.json` (exists) | `config/tool-pins.json` (new, same shape) | `execution.environment.image` in the project file (new) | none |
| 1 Discover | merge to `main` | vendor metadata (exists: `deploy/refresh-harness-pins.mjs`) | node LTS index; pnpm and Playwright on npm; gh and glab releases | the project's own registry build | holder |
| 3 Offline | `go`, `app` and the merge queue (exist) | for each configured model: `--version`, protocol handshake with Fleet's flags, a session with a required MCP server, the structured output stream (built in taskqorg/fleet#1033, not merged) | `--version`; `pnpm install` and `pnpm run build` of the Fleet repository; Playwright opens a local page | the image pulls and starts with the project's check command | none |
| 4 On hosts | none beyond step 8 | per host and account: the probe offers each configured model; one short run per configured model, with one tool call and one Fleet MCP call, through the executor's own adapter and record parser | per host: readiness with the pinned tool, on each project that requires it (`execution.tools`) | per host: readiness and the project's check in the image | none |
| 7 Order | PDS18, then executors | with the release | with the release | with the release | holder's schedule |
| 8 Host verify | signature, digest, `selftest`, health, PDS18 smoke (exist) | digest and `--version` (built in taskqorg/fleet#1033) | digest and `--version` | digest; `podman image inspect` | none |
| 9 Signals | health, poll failures (exist) | per harness version: runs failing in the native protocol, account probe errors | per tool version: readiness failures, tool check failures | per image: readiness and check failures | the OS version in capabilities, for comparison only |
| 10 Rollback | `fleet-update` (exists) | release rollback; revert proposal | release rollback; revert proposal | release rollback; revert proposal | holder |

Host tools become managed the way CLIs did. `fleet-update` caches the pinned payloads under the releases root. The executor puts the release's tool directory first on the PATH of every run and readiness check, which removes conflict 3. A host that has not yet taken a release with tool pins keeps today's host tools, as `enabled: false` did for the CLIs.

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

* **B1. Pinned by Fleet, like the CLIs (recommended for node, pnpm, Playwright with Chromium, gh and glab).** These are userland downloads with digests. Every host gets one version, a rollback includes the tools, and readiness and dispatch see the same binary.
* **B2. Left to the holder; Fleet only reports versions and readiness.** Nothing new to install. But hosts drift, and a broken tool shows only as failed runs.

### C. Rollout order (step 7): C1 chosen

* **C1. Keep today's order (recommended).** PDS18 goes first, then every executor at once. Step 4 already ran on every platform before the merge. Per-host refusal (step 8) and the watch (step 9) cover what is left. Staged rollouts stay a non-goal.
* **C2. Add a canary executor.** After PDS18, one named executor host takes the release. The other hosts take it only after that host finishes one qualification run on the new release, or after 30 minutes healthy. The coordinator keeps this hold per release, and the Mac or WSL waits for it.

## 5. Build order after approval

1. The CLI proposal, with offline qualification and automatic merge, renamed to this procedure's names and statuses (taskqorg/fleet#1033 holds a draft).
2. A1: the host qualification command, the Lab trials, and the status on the proposal.
3. The update history and journal entries for host switches, and the Changes rows per component.
4. The step 9 watch and the revert proposal.
5. Host tools (B1), the same way.
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
