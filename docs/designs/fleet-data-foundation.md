# Fleet data foundation: executions, tasks, artifacts and history

*   **Author:** Alex Kirs
*   **Status:** Proposed
*   **Date:** 2026-11-09

--------------------------------------------------------------------------------

## Overview

Fleet runs agents, tools and pipelines on the owner's Mac, Windows and Linux hosts. It exists so the team can measure ways of working, improve them, and run production work with the best one. Measuring and improving need one thing first: every run leaves a complete, linked and queryable record.

This design defines that data foundation. It has four parts:

1.  **Execution record.** One record shape for every run, whatever started it.
2.  **Task table.** Production work items that operators track and accept.
3.  **Artifact store.** Results of any type and size, kept outside the database.
4.  **History collection.** Transcripts, events and measurements, gathered from every host, with known secrets masked, and readable by people and agents within their permissions.

The laboratory ladder, spending control, account routing and result viewers will be built on this foundation. They are out of scope here.

### Terms

*   **Coordinator.** The central TypeScript service on PDS18 (`packages/coordinator`). It owns all state and serves the operator UI and agent tools.
*   **Executor.** The Go service on each host (`go/executor`). It launches harnesses, collects their output and reports to the coordinator.
*   **Outbox.** The executor's local queue of rows for the coordinator, sent in batches over HTTP, each row with a sequence number `seq`.
*   **Harness.** An agent program that Fleet launches: Codex, Claude Code, or Antigravity (`agy`, its CLI). An **adapter** is the executor code that reads one harness's output.
*   **ACP.** The Agent Client Protocol, the session protocol Antigravity speaks.
*   **Session.** One continuous conversation with a harness. An execution may run inside a session.
*   **Profile.** A named agent setup in `config/agents/*.json`: harness, model, instructions, skills and sandbox settings.
*   **Environment.** A registered place where an execution runs on a host: a working area plus the settings it needs.
*   **Project grant.** The record that says which projects a person or agent may read or change. Every query is filtered by it.
*   **Experiment and variant.** An experiment is a lab study, stored in the `experiment` table (migration 11; [fleet-lab.md](fleet-lab.md)). A variant is one way of working that the experiment compares. Each lab execution records `experiment_id` and `variant`.
*   **Hosts.** PDS18 is the central Linux server. The other hosts are the owner's Mac and Windows machines. A host ID is the ID its executor registers with.
*   **tight.** The owner's infrastructure rules for PDS18, in `~/Projects/tight/tight.md`. Its decisions are written `tight-D4`, `tight-D6` and so on. This design's own decisions are `FD1` to `FD6`.
*   **AssetQ and Pipe.** Earlier prototypes on PDS18: AssetQ stores files with Postgres metadata; Pipe holds provider keys and records each API call.
*   **t3code and Orca.** Two existing agent IDEs whose tool shapes and parsing code Fleet copies. Revisions are named where they are used.
*   **EGM.** The design-review method used for this document.

## The Problem

Before this design the coordinator kept all state in one SQLite table, `records(kind, id, data)`, as JSON documents of about 30 kinds. Several problems followed:

*   **No single definition of a run.** The same idea appears as `execution`, `delegation`, `pipeline-run`, `delivery`, lab `trial` and `session-fork`. Each kind has its own states and its own way of linking to a project. Questions such as "what did project X spend this week" or "how did variant A compare to B across all its runs" need custom code for each kind.
*   **No task object.** `fleet.md` describes tasks and their states, but no record exists. Operators have nothing to track progress on or accept. In earlier iterations public GitHub issues filled this role: everyone could see them, and GitHub rate limits slowed the work.
*   **Artifacts cannot hold real results.** Artifacts are stored as base64 inside records, with a 16 MB limit (`FLEET_MAX_ARTIFACT_BYTES`). The executor rejects glb, mp4 and pdf files. A run whose required artifact upload failed with a 4xx is still marked successful. So 3D models, video and large reports cannot be results, and a "successful" run can be missing its result.
*   **History and measurements are incomplete.**
    *   The executor drops harness rate-limit events.
    *   Account quota is stored as the string `'unknown'`.
    *   CPU is measured only for the leader process.
    *   Codex child agents run against the same subscription without full records.
    *   Native transcripts stay on the host that produced them, and Claude may delete them under `cleanupPeriodDays`.
*   **Secrets can leak into history.** A diagnostic once printed the machine bearer token. Nothing removes secrets before data is written.
*   **One file on one host does not scale.** Analytical queries over millions of events run against the same file that serves live operations.

Without this foundation, the laboratory cannot compare variants honestly. The operator cannot see cost by project or task, and agents cannot analyse past work.

## Goals

*   **G1. One execution record for every run.** This covers:
    *   agent turns;
    *   pipeline runs and their steps;
    *   delivery steps (merge, publication), which belong to a delivery task;
    *   laboratory trials;
    *   delegations, handoffs and forks;
    *   child agents that a harness reports.

    Each execution has one kind, one state set (`preparing`, `running`, `waiting`, `succeeded`, `failed`, `cancelled`, from `fleet.md`) and optional links:
    *   project, task, experiment and parent execution;
    *   session, account, host and environment;
    *   the configuration revision it ran with.
*   **G2. A task table owned by Fleet.**
    *   Tasks are flat and linked by dependencies.
    *   They use the task states from `fleet.md`. Questions, including operator decisions and acceptance, attach to them.
    *   A task's progress, cost and latest result are derived from its linked executions, not stored twice. The list values (current agent, last host, first start, open questions) are stored once, in `task_derived`, kept by triggers and checked for drift ([fleet-view-urls-and-list-path.md §3](fleet-view-urls-and-list-path.md)); no code writes them. The stored `state` changes only through the transitions in Technical Plan §2.
    *   A delivery is a task. Its merge and publication steps are executions of that task.
*   **G3. An artifact store for any result type and size.**
    *   Artifacts are immutable objects keyed by their content hash.
    *   Each one links to the execution that produced it.
    *   Agents write through a Fleet tool and never receive storage credentials.
    *   An execution that declares a required artifact cannot reach `succeeded` until that artifact is stored.
*   **G4. Complete history, collected centrally.**
    *   Harness transcripts and events from every host are copied to central storage, live where the harness allows and at the latest when the run ends.
    *   Secrets known to Fleet, and values that match common token patterns, are masked before writing.
    *   Nothing expires by default. Moving or deleting data is an optional project rule, and a deletion leaves a tombstone record.
*   **G5. Measurements with stated coverage.** Each execution records what is available:
    *   tokens;
    *   subscription rate-limit signals, forwarded rather than dropped;
    *   money spent on API connections;
    *   wall-clock and active time;
    *   CPU and memory.

    Each value says whether it is measured, estimated or unavailable. It is never shown as zero when it is unknown.
*   **G6. Queries for people and agents through the same operations.**
    *   People and agents filter executions and tasks by project, task, experiment, account, host, kind, state and time range.
    *   Totals do not count nested usage twice.
    *   Agents get summaries and pointers by default and raw transcript parts on request, all within the requester's permissions. History is marked as data, never as instructions.
*   **G7. Migration without loss.**
    *   Every existing record kind maps to the new model.
    *   Existing operations and screens keep working during the move.
    *   Rollback stays possible until the old store is retired.

*   **G8. An isolation boundary around every agent.**
    *   An agent sees only what it needs: the keys and connections of its own account and project, its own `HOME`, and its own working area.
    *   Inside that boundary the agent keeps full capabilities, and the boundary adds no caps.
    *   The boundary is also what makes three other things possible:
        *   CPU and memory are measured for the whole process tree;
        *   lab runs start from a clean, comparable environment;
        *   a person can log in and watch the agent.
    *   Facts this goal must cover:
        *   today one OS user can read every project key;
        *   `gitNetwork` gives a writable shared `.git` and full network access;
        *   Antigravity allows one account per OS user;
        *   computer use allows one GUI per OS user;
        *   Claude needs a real `HOME` to reach the Keychain.
*   **G9. A small agent-facing tool set for results and history.**
    *   Agents save results by writing files to a per-execution output folder, and the executor collects them.
    *   Explicit tools exist for large files, for saving immediately, and for reading results and history.
    *   Tools have real parameter schemas.
    *   Their instructions load on demand, and a test enforces a byte budget on them.
    *   The shapes are copied from t3code and Orca.

*   **G10. People and access.**
    *   Fleet keeps the list of people who use it: the owner, Alina and Simon to start.
    *   Any email can be given access to a project, a task or a single artifact.
    *   Cloudflare Access stays the outer wall, so an email Fleet does not know never reaches Fleet.
    *   Fleet checks access again on every operation.
    *   Fleet shows the state of access and onboarding: who has access, to what, since when, and whether Cloudflare matches Fleet's list.

*   **G11. Hosts, environments and stores are visible and managed through Fleet.**
    *   One list shows every connected host with its environments, account users, stores, harness versions, reachability, and disk and store fill.
    *   Store capacity and service resource bounds are Fleet settings. An operator changes them through Fleet operations, and Fleet applies them on the host.
    *   Low free space is a visible problem before it stops work.

## Non-Goals

*   The laboratory ladder logic: N variants, interleaved trials, judges, assessments and promotion. This foundation only stores what the ladder needs.
*   Spending control, account routing, reserve accounts and handoff on quota exhaustion. This design records the signals; acting on them is separate.
*   Porting the executor to Windows.
*   Result viewers: interactive 3D viewing, video streaming players, and side-by-side comparison. Basic previews (image thumbnails and a poster frame for video) are in scope; see Technical Plan §3.
*   The connection gateway for third-party APIs such as Meshy. This design fixes only how a call's cost is recorded: a `measurement` row with `kind = connection_cost`, `connection_id`, `amount`, `currency` or `credits`, and `basis` (`estimated`, then `measured`), linked to the execution. The gateway itself is the next design (Q9).
*   Event triggers such as "an error report starts a fix-plan agent".
*   Export to GitHub, GitLab or other boards.
*   Task hierarchy (subtasks, epics).
*   Interface customization per operator.
*   Concurrency or admission caps of any kind.

## Open Questions

Open questions come first; answered ones follow with their evidence.

*   **Q2. S3 store on Windows (answered 2026-11-09).** Garage under WSL2, as a systemd user unit, published through its own tunnel; it runs only while the user is logged in. See Proof results below and `docs/infrastructure-build.md`.
*   **Q7. Backup and durability.** Is one storage node on PDS18 with backups enough, or is replication needed? What is the backup target?
*   **Q9. Connection gateway.** How agents call third-party APIs without seeing keys, and how each call's cost reaches its execution. The gateway is a separate design, and it must use the `connection_cost` record defined here.
*   **Q8. tight amendments.** Two tight rules must change before Phase 0 and Phase 5:
    *   tight-D7 says "Do not create a generic S3 service". That rule was written for the AssetQ prototype. Amend it to allow Fleet's per-host stores, as a Podman Quadlet of `fleet` on PDS18.
    *   tight-D4 says no service user has sudo. Amend it with one exception: the root-owned system unit template `fleet-launch@.service` (Technical Plan §9) lets the `fleet` user start processes only as `fleet-*` account users. It grants nothing else.
*   **Q1. Garage features: answered 2026-11-09.** Tested on a local single-node Garage 2.5.0 (Homebrew, native on macOS arm64), using `@aws-sdk/client-s3` v3. All of these passed:
    *   a presigned PUT with `x-amz-checksum-sha256` (200), and a wrong SHA-256 is rejected with 400;
    *   `HEAD` with `ChecksumMode` returns the stored SHA-256, which the commit step can check without downloading;
    *   a presigned GET returns the exact bytes;
    *   a multipart upload through presigned `UploadPart` URLs (11 MiB in 5 MiB parts);
    *   a server-side `CopyObject` between buckets;
    *   `PutBucketLifecycleConfiguration` and `GetBucketLifecycleConfiguration`.

    One gotcha: the SDK client must set `requestChecksumCalculation: 'WHEN_REQUIRED'`. Otherwise the presigner embeds a CRC32 of an empty body in part URLs, and Garage rejects the part with `InvalidDigest`. Garage keeps an independent single-node store on each host, because its layout is cluster-wide.
*   **Q3. Garage on macOS: answered 2026-11-09.** Homebrew ships a native `garage` 2.5.0 bottle for arm64, so the Mac store runs natively under launchd.
*   **Q4. Native OpenTelemetry export: answered 2026-11-09.** The checks read the strings in the installed binaries; no harness was run.
    *   Claude Code 2.1.319 has OTel export (`CLAUDE_CODE_ENABLE_TELEMETRY`, `OTEL_EXPORTER_OTLP_*`).
    *   Codex 0.167.0 has OTel export (`otlp-http`, `OTEL_EXPORTER_OTLP_*`, `log_user_prompt`).
    *   Antigravity 1.5.3 has none.
    *   Under FD4, an adapter may point a harness's OTLP exporter at a loopback endpoint of the executor. That makes native OTel one more input to the adapter, not a separate path. Still open: whether the OTel data contains anything the native stream does not.
*   **Q5. Rate-limit signals: answered 2026-11-09.**
    *   Codex rollout files carry `rate_limits` with:
        *   `primary` and `secondary` windows, each with `used_percent`, `window_minutes` and `resets_at`;
        *   `credits` (`has_credits`, `unlimited`, `balance`);
        *   `plan_type` and `rate_limit_reached_type`.

        This is a measured value (`basis = measured`) for each window.
    *   Claude Code `stream-json` emits `rate_limit_event` with `rate_limit_info` (`status`, for example `allowed` or `rejected`, reset time, overage and fallback flags). This is a measured status, not a percentage.
    *   Antigravity has no CLI command for quota: `agy models` lists models only. Its binary contains a local `LanguageServerService/RetrieveUserQuotaSummary` handler and a `RetrieveUserQuota` response with buckets (`remaining_fraction`, `remaining_amount`, `reset_time`). Next live check: call that handler on the language server that an ACP session starts. Calling Google's API directly with the account's OAuth token is not acceptable, because it would act outside the harness.
    *   The executor drops all of these today (see Detailed Implementation Plan, item 35).
    *   Evidence: Codex field names were read from local rollout files written by Codex 0.167.0; Claude and Antigravity names were read from the strings in the installed Claude Code 2.1.319 and Antigravity 1.5.3 binaries, as in Q4.
*   **Q6. Claude transcript cleanup: answered 2026-11-09.**
    *   The default `cleanupPeriodDays` is 30 days.
    *   Claude 2.1.319 skips the retention sweep when `--setting-sources` excludes user settings and no other enabled source provides `cleanupPeriodDays`. Fleet launches Claude with `--setting-sources ""` (`go/executor/harness_command.go`), so Fleet-launched transcripts are not deleted by Claude.
    *   Evidence: read from the strings in the installed Claude Code 2.1.319 binary, as in Q4.
    *   The risk remains for sessions launched without that flag, and for any future change to that behaviour. Copying transcripts when a run ends (G4) is still required, but it is not urgent.

*   **Proof results (2026-11-09).** Q2 is answered: the Windows store is Garage under WSL2. A cross-host proof passed: an 80 MiB multipart upload to the Windows store went through its tunnel, the Mac executor copied it to its own store, and PDS18 read it back with a matching SHA-256. Details and findings are in `docs/infrastructure-build.md`. The main finding: Access headers are added after S3 signing, because Cloudflare strips them before the origin.

*   **Q10. Executor lifecycle through CoreQ (candidate, not decided).** CoreQ at `2f141a7` is the shared Rust library already used by taskq, DashQ and ServerQ. It ships, with tests on macOS, Linux and Windows, three things that Fleet now does by hand:
    *   Service units: `service::host_unit` writes a launchd agent, a systemd user unit or a Windows Scheduled Task.
    *   A launcher with crash rollback: `launcher::launch` rolls back after three unhealthy starts.
    *   Atomic self-update: `update::prepare_install` and `activate_install` gate a release on SHA256SUMS and a `selftest` run, then switch `current` and `previous`.

    It also provides `write_durable` (fsync before success) and a `status.json` freshness check.

    Costs and limits:
    *   Releases must be published as GitHub releases in CoreQ's naming (`latest.json`, `SHA256SUMS`).
    *   It does not fit Fleet's releases, which switch a `current` symlink.
    *   Its telemetry shipper and enrollment speak DashQ's wire protocol and allow only fixed codes, so they do not replace Fleet's outbox.

    Decide when executor packaging is designed (Windows executor, auto-update).

## Critical User Journeys (CUJs)

### CUJ 1: Operator sees what a task cost and where it stands

*   **Given**: A task in project `dashq` has had three executions: a worker run on Codex, a reviewer run on Claude, and a merge step.
*   **When**: The operator opens the task.
*   **Then**: The operator sees:
    *   the task state and its waiting reason, if any;
    *   the latest result;
    *   tokens per account, money spent on API calls, and total time.

    Each total says whether it is complete or partial. The operator can open any execution and read its transcript.

### CUJ 2: Lab trials stay out of production views but stay analysable

*   **Given**: An experiment ran 300 trials across 4 variants.
*   **When**: The operator lists the project's work, and later the architect asks an agent to compare the variants.
*   **Then**: The operator's list shows tasks only, with no trials. The agent queries executions linked to the experiment, grouped by `variant`, and gets per-variant totals with pointers to individual runs. Scores stay in the `experiment` document and are joined by execution ID.

### CUJ 3: A 3D model is a result

*   **Given**: A worker generates a 400 MB `glb` file through the Meshy connection.
*   **When**: The worker stores it with the Fleet artifact tool.
*   **Then**:
    *   The file goes to the object store under its content hash, linked to the execution and the task.
    *   Once the connection gateway exists (Q9), the Meshy charge is recorded on the same execution as a `connection_cost` measurement. Until then the cost shows as `unavailable`.
    *   If the upload fails, the execution does not reach `succeeded` and the failure is visible.

### CUJ 4: An agent analyses spending

*   **Given**: Two weeks of executions across Codex, Claude and Antigravity on three hosts.
*   **When**: The operator asks an agent "where did our Claude window go this week and what can we cut?"
*   **Then**: The agent queries totals by project, task, kind and configuration revision, and drills into the most expensive executions. It reads transcript parts on demand. Credentials that Fleet injected, and values that match token patterns, were masked at ingest, so the agent does not see them. Its answer cites execution IDs.

### CUJ 5: A project keeps its heavy assets on one host

*   **Given**: A game project has `artifact_locations: [windows]`, and a design project has `[windows, mac]`.
*   **When**: A worker on the Windows host saves a 2 GB asset bundle.
*   **Then**:
    *   The game bundle goes to the Windows store and is never copied anywhere else. Its metadata row in Postgres records which store holds it.
    *   A design file from the other project is written on Windows and then copied to the Mac. It shows `placed` once both copies are verified.
    *   Operators on other machines open artifacts through a presigned link to the holding store's Cloudflare Tunnel hostname, behind Cloudflare Access, while a holding host is online. While none is online, the artifact is shown as unavailable, not lost.

### CUJ 6: History survives the host

*   **Given**: A Claude run finished on the Mac, and its transcript is due for local cleanup.
*   **When**: Claude's cleanup deletes the local file, or the Mac is wiped.
*   **Then**: The transcript and events are already in central storage, and the execution's history is complete.

## Technical Plan (Architecture)

### Decisions taken

| # | Question | Decision |
|---|---|---|
| FD1 | Core database | Postgres: role and database `fleet` in the existing system PostgreSQL 18 cluster on PDS18 (tight-D6), with local peer authentication and the existing nightly `pg_dump` |
| FD2 | Object store and placement | An independent single-node S3 store on each host (Garage; on Windows, Garage under WSL2, Q2). Each store is published through Cloudflare Tunnel behind Cloudflare Access, as Fleet and Dash are. Fleet manages placement: each project lists the locations that must hold its artifacts, and Fleet copies objects between stores and checks their hashes |
| FD3 | Events, spans and measurements | ClickHouse next to Postgres, as a rootless Podman Quadlet unit of the `fleet` user (tight-D4 and tight-D5). Separate from the ClickHouse that belongs to Langfuse |
| FD4 | Getting history off the hosts | Extend the executor outbox. Raw transcripts and artifacts upload directly to the store with presigned URLs. No collector process on the hosts |
| FD5 | Isolation boundary | One OS user per harness account, with the harness sandbox inside. Project keys are issued for each execution and never stored with the account user. On Linux, each execution runs in its own systemd scope, started through the root-owned `fleet-launch@.service` template |
| FD6 | Running the services on PDS18 | Follow tight: one Unix user `fleet`, user systemd units with linger, no Docker and no sudo except the one launch helper in Q8, containers only as rootless Podman Quadlet. Garage on PDS18 also runs as a Podman Quadlet of `fleet`. Hosts other than PDS18 follow the same pattern with their native service manager |
| P3-D1 | Whole-file hash check of a multipart upload or copy | The store host's executor re-hashes the staging object over loopback and reports the digest; the coordinator promotes on a match (owner, 2026-11-10). |
| P3-D2 | Execution state while required uploads continue after the agent exits | `waiting` with reason `artifacts`; the agent slot is released and no active time accrues (owner, 2026-11-10). |
| P3-D3 | Where required artifacts are declared | Both: the task's `completion_requirements.artifacts` sets the minimum, and an agent may only add required artifacts through `artifact_save` (owner, 2026-11-10). |
| P3-D4 | Previews and retention rules | Both in Phase 3. `ffmpeg` runs sandboxed as the account user without network; its time and memory bounds are operator settings, not built-in limits (owner, 2026-11-10). |

The principle behind FD4 is that the layer between Fleet and the harnesses belongs to Fleet. That layer covers how each harness is configured, how its output is read, and how Fleet interacts with it. Fleet has to write it in any case, so history passes through that layer and nothing writes into Fleet's stores around it. Native OpenTelemetry output from a harness, where it exists, becomes one more input to the same executor adapter. It is not a separate path.

### Data flow

```text
 host (Mac / Windows / PDS18)                          PDS18 (central)
 ┌──────────────────────────────────┐                  ┌────────────────────────────────┐
 │ harness (Codex/Claude/agy)       │                  │ coordinator                    │
 │   │ native stream / logs / OTel  │                  │  ingest ─┬─> Postgres (state)  │
 │   v                              │  outbox (HTTP,   │          │   execution, task,    │
 │ executor harness adapter         │  batched, seq)   │          │   artifact, store ... │
 │   normalize -> redact            ├─────────────────>│          └─> ClickHouse          │
 │   │                              │                  │              event, span,       │
 │   │ request presigned PUT        │<─────────────────┤              measurement        │
 │   v                              │                  │  operations (UI + agents)      │
 │ upload raw transcript/artifact ──┼──> Garage store  │   query with project filter    │
 │   (multipart, 32 MiB parts)      │   (per host, via │                                │
 │   confirm hash+size via outbox   │   Cloudflare     │                                │
 └──────────────────────────────────┘   Tunnel+Access) └────────────────────────────────┘
```

### 1. Execution: one record shape

*   **Granularity rule.** An execution is a unit that Fleet launches, or that a harness reports as a separate agent. That covers:
    *   an agent turn or session run;
    *   a pipeline run and each of its steps;
    *   a delivery step, such as a merge or a publication;
    *   a laboratory trial;
    *   a delegated or handed-off run;
    *   a reported child agent.

    Calls inside an execution are spans in ClickHouse, not executions. This includes model calls, tool calls and shell commands.
*   **Kinds.** `agent_run`, `pipeline_run`, `pipeline_step`, `child_agent`, `tool_run` (a Fleet tool or connection call that is billed or long-running on its own), `host_job` (a host command that is not an agent turn: artifact copy or verify, account inspection, integration, a pipeline script step). A lab trial is an `agent_run` or `pipeline_run` linked to an experiment. It is not a separate kind. A delivery is not an execution kind: it is a task, and its steps are executions of that task (§2).
*   **States.** `preparing`, `running`, `waiting`, `succeeded`, `failed`, `cancelled`. Observation confidence is a separate field (`confirmed` or `unconfirmed`, plus `last_seen_at`). Lost contact leaves the last known state and marks it unconfirmed, as `fleet.md` requires.
*   **Unplaceable executions.** An execution bound to a host, account or environment that is not registered is shown as a problem with that reason. It does not wait silently. Example from live state: `6b00f0fb` has waited forever for the host ID `pds18`, which no executor has registered.
    *   **Problem rule.** A problem explains why an *open* execution cannot run. `detectProblems` sets and clears it only on open executions. Reaching `succeeded` clears it. `failed` and `cancelled` keep the last problem as the explanation of the failure. Migration 7 clears problems left on succeeded executions.
*   **Mapping from the prototype.** The full mapping per legacy kind is in §7. Migration 6 kept `legacy_kind` and `legacy_id` on each migrated row; migrations 23, 24 and 32 dropped them once every reader used the typed columns.
*   **Links.** All are optional foreign keys:
    *   `project_id`, `task_id`, `experiment_id`, `parent_execution_id`;
    *   `session_id`, `account_id`, `host_id`, `environment_id`;
    *   `variant`, set on lab executions.

    Lab versus production is not a flag: it follows from the presence of `experiment_id`.
*   **Parent rule.** `parent_execution_id` is, in order: the delegating execution (a delegated child), the continued execution, the pipeline run (for a pipeline step or the host worker of a pipe search), the source turn of a handoff, the agent execution that called `session_start` (every execution of the session it started), or the fork source for a fork's first execution. Tree totals and drill-down follow this link. Migration 7 backfills the `session_start` links of rows written before the rule existed: the creator session's latest execution at the time the session was created.
*   **Effective configuration.** `harness`, `harness_version`, `model`, and `config_ref`. `config_ref` holds the repository, revision and paths of the instructions, skills and profile, plus a hash of any draft content. The harness version is recorded on every run, so a self-update inside an experiment is visible.
*   **Totals and coverage.** When an execution ends, the coordinator writes a totals row from ClickHouse to Postgres. If events for an execution that has already ended arrive later, the coordinator recomputes that row. It covers tokens by type, subscription-window signals, money by connection, wall-clock and active time, CPU and peak memory. Each value carries `basis`: `measured`, `estimated` or `unavailable`. The row also carries `history_incomplete`, the count of outbox rows the coordinator rejected for this execution. Totals cover the execution's own spans only. A tree total is the sum over the distinct executions in the tree, so nested usage is never counted twice.

### 2. Task: production work item

*   **Table `task`.**
    *   `id`, `project_id`, `title`, `expected_outcome`;
    *   `state`: `queued`, `in_progress`, `waiting`, `paused`, `completed`, `failed` or `cancelled`;
    *   `wait_reason`, `priority`;
    *   `priority` is a queue class (owner decision, 2026-11-10), not a number:
        *   `urgent` runs first. It may use accounts the operator marked "reserve for urgent". It shows a red chip.
        *   `normal` is the default. Tasks run first in, first out within the class, and the operator can move a task up or down by hand. Rows show no chip.
        *   `background` covers tests, lab work and experiments. It runs only when no urgent or normal work is waiting for the same resource. It shows a grey chip.
        *   The API takes the names and also accepts `p0`, `p1` and `p2` as synonyms (TaskQ vocabulary).
        *   What background work may use, for example a share of an account window or nights only, is an operator setting with no built-in cap.
        *   Old integers migrate to `normal` (migration 8).
        *   The scheduler behaviour and the per-class queue view are planned work; see [fleet-operator-app.md](fleet-operator-app.md#future-improvements).
    *   `completion_requirements` (JSONB), `result_destination`;
    *   `created_by`, timestamps.
*   **Table `task_dependency`.** `task_id`, `depends_on_task_id` and `kind`. Tasks are flat; there is no hierarchy. Only `blocks` dependencies gate transitions. `revision_of` records a delivery that revises an earlier one (legacy `previousDeliveryId`) without waiting for it, as the legacy flow never waited for acceptance.
*   **Derived, not stored.** Progress, cost so far and the latest result come from the view `task_view` over the task's executions, artifacts and questions. Only `state` and `wait_reason` are stored, and they change only through these transitions, all written by `go/coordinator/tasks.go`:

    | From | Trigger | To |
    |---|---|---|
    | (new) | `tasks.create` | `queued` |
    | `queued` | the first execution of the task starts and all dependencies are `completed` | `in_progress` |
    | `queued` | an open question of kind `operator_decision` blocks the task before its first execution, or a dependency is not `completed` | `waiting`, with `wait_reason` |
    | `in_progress` | an open question of kind `operator_decision` or `agent_question` blocks the task, or a dependency is not `completed` | `waiting`, with `wait_reason` |
    | `waiting` | the blocking question is answered, or the dependency completes | `in_progress` |
    | `in_progress` | its last open execution succeeds, it has no delivery method and no open question | `in_progress`; Fleet asks one `acceptance` question, so a finished task waits on the operator, not silently |
    | `in_progress` | the completion requirements are met and an `acceptance` question is answered `accepted` | `completed` |
    | `in_progress` | an `acceptance` question is answered `rejected` | `in_progress`, with a new execution expected |
    | `in_progress` or `waiting` | the operator stops the task, or its last execution fails and the operator marks it failed | `failed` |
    | any open state | the operator pauses or resumes it | `paused`, or back to the state before |
    | any open state | the operator cancels it | `cancelled` |

*   **Acceptance.** A task reaches `completed` only through an answered `acceptance` question. The answer records who accepted, when, and the accepted artifact IDs. A result with no task (a lab run, an ad hoc agent run) is not accepted; it stays a result of its execution.
*   **Table `question`.** One typed table replaces the legacy `project-question` and `question` documents:
    *   `id`, `kind` (`operator_decision`, `agent_question` or `acceptance`), `project_id`;
    *   `task_id` and `execution_id`, both nullable and indexed. `task_id` is set when the question is created: from the task for operator decisions and acceptance, and from `execution.task_id` for agent questions;
    *   `session_id`, `text`, `options jsonb`, `asked_by`, `asked_at`;
    *   `answer`, `answered_by`, `answered_at`, `state` (`open`, `answered` or `withdrawn`).

    Operators and agents answer through one operation, `questions.answer`. There is no separate `decision` record.
*   **Delivery as a task.** A legacy `delivery` document becomes one `task` row. Each entry in its `steps[]` becomes an execution with that `task_id`:

    | Legacy `delivery` field | New place |
    |---|---|
    | `id` | `task.id` (kept, and `task.legacy_id`) |
    | `task` (the text) | `task.title` and `task.expected_outcome` |
    | `requestId`, `signature` | `task.completion_requirements` |
    | `methodId`, `profileId`, `account` | `task.delivery_method` (jsonb); each step execution records its own `account_id` and `config_ref` |
    | `questionId` | a `question` row of kind `operator_decision` with this `task_id` |
    | `previousDeliveryId` | a `task_dependency` row of kind `revision_of` |
    | `steps[i].executionId` | an execution with `task_id`, kind `pipeline_step` or `agent_run` |
    | state `waiting` | task `waiting`, `wait_reason = question` |
    | state `pr-open` | task `in_progress` |
    | state `merged`, `published` | task `in_progress`; an `acceptance` question (options `accepted`, `rejected`, with the step artifacts) is asked once per state; `completed` after `accepted` |
| states `researching`, `generating`, `integrating`, `publishing`, `blocked` | task `in_progress` |
| states `quota`, `unavailable` | task `failed` |
    | state `failed` | task `failed` |
    | state `unknown` | task `in_progress`; its latest execution is `unconfirmed` |

*   **External boards.** None. A future export reads from this table.

*   **Human codes.** Tasks, questions, experiments and spec rules carry a human code next to the UUID. The UUID stays the key; the code is for reading and recognition only. Brief: [entity-ids-brief.md](entity-ids-brief.md).
    *   Format: `T-FLT-42` (type, project code, number) for `T` task, `Q` question, `E` experiment. Spec rules carry a mandatory section letter: `S-FLT-D3`. Hyphens, uppercase, no leading zeros, case-insensitive lookup. Regex: `\b(?:([TQE])-([A-Z][A-Z0-9]{1,4})-([1-9][0-9]*)|S-([A-Z][A-Z0-9]{1,4})-([A-DF-PRU-Z])([1-9][0-9]*))\b`.
    *   Scope `ALL` is reserved for general rules and rows with no project (`S-ALL-C4`). Project codes are 2 to 5 characters, start with a letter, are not hex-only, avoid I, O, S, Z, B, and are never reused. Any uppercase letter may be a section letter; positions are fixed, so `S-FLT-S5` is unambiguous.
    *   Column `code text unique` on `task`, `question`, the experiment row and the spec rule row. Executions, artifacts, hosts and projects get no code.
    *   Table `code_counter(scope, type, section, next)`: one row per (project, type) for `T`, `Q`, `E`, and per (scope, section) for `S`. Only the coordinator issues codes, with `UPDATE … RETURNING` in the same transaction as the insert. Gaps are allowed; numbers are never reused. Counters are per project so gaps reveal nothing across RLS boundaries.
    *   Table `entity_code(code primary key, entity_id, type, issued_at, superseded_by)`. A code never changes. A moved entity, a renamed project or a changed type gets a new code; the old code stays as an alias that resolves to the entity. No move operation is built.
    *   Ops `entity.resolve(code)` returns `{id, type, current_code, superseded}`; `entity.resolve_many(text)` finds every code in free text. Unknown and forbidden codes both return `not_found`.
    *   Backfill: existing rows get codes in `created_at` order, filling only empty values.

*   **Migration 11** (Lab Stage 0, [fleet-lab.md](fleet-lab.md)): `experiment` and `score` tables with row-level security and change-feed triggers, `execution.request_id` (unique when set), question kind `rating`; the old lab's `experiment` documents are deleted and their executions unlinked. Lab trials set `experiment_id` and `variant` through `executions.createExecution`.
*   **Migration 12** (Lab Stage 1, [fleet-lab.md](fleet-lab.md)): artifact `role` adds `example` (a Lab example, written by the coordinator), `artifact.approved_at` (null while proposed), and a unique index on `(coalesce(project_id, ''), name)` for example artifacts.
*   **Table `task_derived`** (migration 10): `task_id` (primary key, references `task`), `project_id` (copied from the task, for row-level security), `agent` (harness of the latest `preparing` or `running` execution), `host` (host of the latest execution), `started_at` (earliest execution start) and `open_questions`. Triggers on `execution`, `question` and `task` keep it current; the operator list (`task_page`) reads it instead of computing these per task. `task_derived_check` reports drift.

### 3. Artifact store

The rules below were revised after the Phase 3 review (`docs/designs/fleet-data-foundation-review/phase3/`) and the cross-host store proof (`docs/infrastructure-build.md`, 2026-11-09). Decisions **P3-D1** to **P3-D4** were taken by the owner on 2026-11-10 (Decisions taken).

*   **Table `artifact`.**
    *   `id`, `project_id`, `scope` (`lab` or `prod`, from whether the execution has an `experiment_id`), `content_hash` (SHA-256 of the whole file), `size`, `media_type`, `name`;
    *   `execution_id`, `task_id`;
    *   `required`, `previous_artifact_id`;
    *   `derived_from_artifact_id` and `role` (`original`, `thumbnail` or `poster`). A preview is an ordinary artifact that points to its original;
    *   `state`: `pending`, `stored`, `failed` or `deleted` (who sets each: Write protocol and Forced purge below);
    *   `lease_expires_at` while `pending`;
    *   `deleted_at` and `deleted_by`, which form the tombstone;
    *   `legacy_id` for artifacts migrated from base64 documents (`artifact:<execution>:<path>`), so existing links and acceptance answers keep resolving.

    Which store holds the bytes is not a column of `artifact`; it is the set of `artifact_copy` rows.
*   **Object keys.** `<project>/<scope>/<sha256>` in each store. Uploads go first to `<project>/<scope>/.pending/<artifact_id>` and are promoted to the hash key only after verification (Write protocol).
*   **Table `store`.** One row per host store: `id`, `kind` (`s3`), `host_id`, `zone`, `bucket`, `endpoint` (the store's Cloudflare Tunnel hostname), `local_endpoint` (the loopback URL that only the store's own host can use), `state` (`reachable`, `unreachable` or `full`), `capacity_bytes`, `used_bytes`, `requires_logon` (true for the Windows store), `registered_at`. Credentials are never in rows.
*   **Store registration.** The executor on a store's host registers its store through its outbox: endpoints, bucket, zone, the coordinator role's S3 key, `capacity_bytes` and `used_bytes`. The coordinator writes the key to its own secret directory (`/home/fleet/.config/fleet/stores/<store>.key`, mode 0600) and upserts the row. Only the authenticated executor of that host may register or replace its store. Every endpoint must be on the operator's allowlist `FLEET_STORE_ENDPOINT_HOSTS` (exact hosts or `.suffix`); a local endpoint must be loopback; Access headers go only to allowlisted tunnel hosts. All hosts share one machine secret today, so the allowlist is what keeps a registering host from pointing the coordinator elsewhere; per-host credentials come with the isolation boundary (§9). The key thus reaches the coordinator directly from the store's host and is never relayed through another server. Executors hold no S3 keys: they use presigned URLs plus the Cloudflare Access service token.
*   **Capacity and availability.** `capacity_bytes` is the smaller of the store's configured capacity and the free space of the disk behind it. For the Windows store that disk is the system drive holding the WSL image. Each poll reports the store's reachability and the remaining capacity (the smaller of the two values) as `capacityBytes`. Every store host runs an executor: it registers the store, verifies uploads and copies (P3-D1). The Windows store therefore needs an executor in WSL. `full` is set when free capacity falls below the `store.free_space_warning` host setting (§11); a `full` store accepts no new uploads and is skipped by store selection. A store whose host has not reported it reachable for 2 minutes is `unreachable`. The Windows store runs only while its user is logged in, because WSL is started by a logon task; readiness shows `requires logon` for it.
*   **Table `artifact_copy`.** `artifact_id`, `store_id`, `state` (`pending`, `copying`, `verified`, `failed`, `purge_pending` or `deleted`), `verified_at`, `last_error`; one row per (artifact, store). An artifact is `stored` once one copy is `verified`. It is `placed` when every location in the project's list holds a verified copy; `placed` is computed, not stored. The task and result views show both.
*   **Store protocol rules.**
    *   Control-plane calls to a store (create, complete or abort a multipart upload, `HEAD`, copy, delete) use its tunnel `endpoint`. The coordinator makes them.
    *   Presigned part URLs and presigned GETs target the store's `local_endpoint` when the host that will use them is the store's host, and the tunnel `endpoint` otherwise.
    *   Requests to a tunnel `endpoint` carry the Cloudflare Access service-token headers. They are added after SigV4 signing and are never signed headers; otherwise Garage answers `signed header cf-access-client-id is not present`. Loopback requests carry no Access headers.
    *   S3 clients set `requestChecksumCalculation` and `responseChecksumValidation` to `WHEN_REQUIRED`.
*   **Transfers.** Uploads and copies use S3 multipart in parts of 32 MiB, under Cloudflare's 100 MB request-body limit; a file of 32 MiB or less is one part. Part URLs are presigned on demand, a batch at a time with a 15-minute expiry, so a long or resumed upload never holds expired URLs. Each part is retried on its own. A pending upload keeps only its upload ID and the finished parts' ETags on disk, so it resumes after a restart.
*   **Write protocol.**
    1.  The executor hashes the file (streaming SHA-256) and asks the coordinator to reserve an artifact with name, size, media type and hash.
    2.  The coordinator selects the store (Store selection). If that store already holds a verified copy of the same project, scope and hash, it records a new `artifact` row as `stored` at once and returns `{state: stored}`: nothing is uploaded. Otherwise it starts a multipart upload on the staging key and records the artifact as `pending` with a lease (`lease_expires_at`, 1 hour, renewed by each part request).
    3.  The executor requests part URLs and uploads the parts.
    4.  The executor commits through a synchronous route; its transfer worker retries the commit until it gets an answer. The completion receipt of a run lists the names it collected, so a required file still on its way counts as pending, not missing.
    5.  The coordinator completes the upload, checks the size with `HEAD`, and has the hash verified (**P3-D1**, default: the store host's executor re-hashes the staging object over loopback and reports the digest; when the writer is that host, it does so at once). On a match it copies the staging object to the hash key if that key is absent, deletes the staging key, and marks the artifact `stored` and its copy `verified`. On a mismatch it deletes the staging key and marks the artifact `failed`. A verified object is never overwritten.
    6.  An artifact whose lease expires before commit is aborted on the store and marked `failed`.
*   **Store selection (the coordinator decides).** The writer's own host when it is in the project's list. Otherwise the first host in the list whose store is `reachable` and not `full`. If none qualifies, the upload waits and retries (Success rule); Fleet never writes outside the project's list.
*   **Success rule.** An execution cannot reach `succeeded` while an artifact it requires is not `stored`. Required artifacts are declared by the task (`task.completion_requirements.artifacts`: names or patterns in the output folder), which sets the minimum; an agent may only add required artifacts, through `artifact_save` with `required: true` (**P3-D3**). After the agent process ends with uploads still pending, the execution is `waiting` with reason `artifacts` (**P3-D2**): its account slot is released and no active time accrues while the executor's outbox keeps uploading. After the operator setting `artifact_retry_window` (default 24 hours, §11) it becomes `failed` with the upload error. This replaces today's behaviour, where a run succeeds after a 4xx.
*   **Reading.** `artifacts.get` returns metadata and a presigned GET for a verified copy, choosing the reader's own host's store when it holds one (then on its `local_endpoint`). When no store holding a verified copy is reachable, the artifact is shown as `unavailable`, not lost. Coordinator code that needs the bytes (delivery results, website sources, pipe responses, differences) reads them through `artifacts.read(id)`, which streams from such a copy, with a byte bound per caller checked before reading. `/api/results/:id` streams the same way with the sandbox headers it always had. A host reads an artifact only for its own command in progress that names it (`payload.artifactId` of a copy or verify command, `payload.files[].artifactId` of a delivery step) or for its own execution in the artifact's project. §6 and §8 use this rule.
*   **Project setting `artifact_locations`.** A list of hosts that must hold the project's artifacts, for example `[windows]`, `[mac]` or `[windows, mac]`. The default is the PDS18 host ID. Every listed host must have a registered store; a project naming one without it is rejected when the project loads. The setting changes only placement; it never limits who may read the artifacts, which follows project grants. A project whose list holds one host keeps its files on that host (CUJ 5).
*   **Placement worker.** It runs in the coordinator every `placement_interval_seconds` (default 60). For each stored artifact missing a verified copy at a reachable listed location, it queues a `copy` command to the executor of the target host. That executor downloads from a store holding a verified copy (through that store's tunnel, or over loopback when it is its own) and uploads to its own store over loopback, through the same staging, verification and promotion steps. The copy never passes through an operator's machine.
    *   A store that is offline (a sleeping Mac; Windows while no user is logged in) leaves its copies `pending`. They resume when its host reports the store reachable again.
    *   Changing a project's locations schedules new copies. Removing a location deletes copies there only after the remaining locations hold verified copies, and only after a `HEAD` shows a surviving copy on a reachable store matches its size. The last copy of an artifact is never deleted outside a purge.
*   **Shared objects.** Several `artifact` rows of one project and scope can point to the same object, because the key is the content hash. A store copy of an object is deleted only when no `artifact` row with that project, scope and `content_hash` in state `stored` still needs that store (its store's host is in the row's project list). Deleting one row otherwise only writes its tombstone.
*   **Forced purge.** `artifacts.purge(content_hash, reason)` removes an object that contains a leaked secret from every store, whatever rows still point to it. It marks every such row `deleted` with `deleted_by = purge:<reason>` and adds the hash to a purge list, which reserve and placement refuse. Copies on unreachable stores become `purge_pending` and are deleted when the store is reachable again, before that store serves any read or copy. Only the operator may run it, and it is logged as an event.
*   **Previews (P3-D4).**
    *   After an image or video artifact is stored, the executor that uploaded it builds previews with `ffmpeg`, if present on that host:
        *   a 512 px `thumbnail` for an image;
        *   a `poster` frame plus a 512 px `thumbnail` for a video.
    *   `ffmpeg` decodes untrusted agent output, so it runs as the execution's account user (§9; until Phase 5 adds account users, as the user that runs the agents on that host), with no network access, under the time and memory bounds of the operator settings `preview_timeout_seconds` and `preview_memory_bytes` (defaults 120 s and 1 GiB), at the lowest CPU priority (`nice -n 19` on macOS and Linux, idle priority on Windows); agents on the same host get the CPU first.
    *   Previews are stored as artifacts with `derived_from_artifact_id`, `required = false` and the source's `execution_id`; they follow the project's placement and can be deleted and rebuilt at any time.
    *   A host without `ffmpeg` reports it in its capabilities, and the preview shows as `unavailable`. 3D models get no preview in this design.
*   **Retention rules (optional, set per project; P3-D4).** A project may add rules to `artifact_rules`, for example `{scope: lab, older_than_days: 30, action: move, to: [windows]}` or `{scope: lab, older_than_days: 180, action: delete}`. `to` names hosts with registered stores, is required for `move` and forbidden for `delete`.
    *   The placement worker carries out a rule as ordinary copies and deletions, under the same safety rules.
    *   Deletion keeps the `artifact` row as a tombstone (`state = deleted`, `deleted_at`, `deleted_by = rule:<id>`).
    *   A new or changed rule runs only after the operator confirms a preview listing the count, total size and a sample of the affected artifacts; editing a rule clears its confirmation.
    *   Without rules, nothing moves or expires. History in ClickHouse is not affected by artifact rules.
*   **Output and transcript collection.** In Phase 3 the executor collects `FLEET_OUTPUT_DIR` and the harness transcript once, when the run ends; a later run of the same path creates a new artifact with `previous_artifact_id`. Collection during long runs, with a rule for files still being written, comes with the CUJ 6 work. A transcript is uploaded after the executor's exact-value masking (§5); pattern masking in the coordinator applies when it is read.
*   **No size limit in Fleet.** The base64 path and `FLEET_MAX_ARTIFACT_BYTES` are removed, in the coordinator and in the executor. The limit is the free space behind each store, shown as store capacity.

### 4. ClickHouse: events, spans, measurements

*   **Tables.**
    *   `event`: execution state changes, questions, errors and harness lifecycle.
    *   `span`: all timed work of every service, not only inside executions: model calls, tool calls and shell commands with tokens, and also routes, passes, outbound calls and host and process readings. Shape, engine, transport and rules are in [telemetry](fleet-telemetry.md#1-record-model).
    *   `measurement`: CPU, memory, rate-limit signals and connection cost, each with its `basis`. Connection cost rows carry `connection_id`, `amount`, and `currency` or `credits`.
    *   `host_metric` is dropped: host and store readings are `check` spans in `span` ([telemetry](fleet-telemetry.md#2-what-is-recorded)).
*   **Columns copied from Postgres at ingest.** `project_id`, `task_id`, `experiment_id`, `account_id`, `host_id` and `execution_id`. Every query can then filter by permission and group without a join.
*   **Engine and keys (`event`, `measurement`).** `ReplacingMergeTree`, partitioned by month and `ORDER BY (project_id, execution_id, seq)`. The sort key is the deduplication key, so a retried outbox batch with the same `seq` collapses to one row. `ts` is a plain column. `span` uses plain `MergeTree` with insert tokens ([telemetry §4](fleet-telemetry.md#4-storage-transport-retention)).
*   **Reading without double counts.** Duplicates disappear only when ClickHouse merges parts in the background. Every query that sums `event` or `measurement` therefore reads with `FINAL`, or takes one value per `seq` with `argMax`. No TTL by default. A project retention rule can add one later.

### 5. Executor: harness adapters, outbox and redaction

*   **Adapter per harness.** Each adapter reads the harness's native output and emits normalized events:
    *   `turn_started` and `turn_ended`;
    *   `model_usage`;
    *   `tool_call`;
    *   `rate_limit`, which is forwarded and no longer dropped;
    *   `child_agent`;
    *   `error`;
    *   `harness_version`.

    The raw stream is kept verbatim, after redaction, as the transcript artifact. Parsing practices are taken from t3code and Orca, not invented.
*   **Outbox.** It keeps its existing batching, thread and `seq`. New event classes travel in the same batches. A batch row that the coordinator rejects with a 4xx is set aside, as today, and also counted, so the execution shows incomplete history instead of looking complete.
*   **Redaction.**
    *   First pass, in the executor: it replaces every secret value it injected (connection credentials, git key material, the Fleet machine token, provider tokens) with `«redacted:<name>»` before writing to the outbox or uploading.
    *   Second pass, in the coordinator: a pattern scan for common token formats. A match is masked before the row is stored, and logged as a `redaction_miss` event with the pattern name only.
    *   Coverage: the two passes catch values Fleet injected and values in the listed token formats. They do not catch a secret an agent reads from disk in an unknown format, or a harness login token that is not injected by Fleet. A secret found later is removed with the forced purge (§3).
*   **Host metrics.** The executor sends a `host.heartbeat` check span each minute with disk, load, harness versions, reachable stores and process readings ([telemetry](fleet-telemetry.md#2-what-is-recorded)).

### 6. Operations for people and agents

People and agents use the same coordinator operations under the same permissions:

*   `executions.query`: filters on links, kind, state and time.
*   `executions.get`: totals with coverage, plus the span tree.
*   `tasks.list`, `tasks.get`, `tasks.create`, `tasks.update`.
*   `questions.ask`, `questions.answer`.
*   `usage.summary`: group by project, task, experiment, account, host, kind or configuration, with totals that count nothing twice.
*   `transcript.read`: one execution, a range or a search, returning redacted text marked as data.
*   `artifacts.get`: metadata plus a short-lived presigned GET.

Permissions:
*   The project grant is applied inside each operation.
*   Agents may read tasks and questions of the projects in their grant, create tasks in those projects, update only tasks their own execution is linked to, and ask questions. Only operators answer `operator_decision` and `acceptance` questions.
*   Postgres row-level security on `project_id` is the second guard. The tables are owned by the role `fleet`; the coordinator connects as the non-owner role `fleet_app`, and every table has `FORCE ROW LEVEL SECURITY`. When `fleet.project_ids` is not set, a policy returns no rows.
*   ClickHouse is reachable only through these operations, and every query gets a mandatory project filter.

Agent replies return summaries and IDs by default. Raw transcript text is returned only by `transcript.read`.

### 7. Migration from `records`

*   **In scope.** These kinds map to the new tables:

    | Legacy kind | Target | Notes |
    |---|---|---|
    | `execution` | `execution`, kind `agent_run` | state `completed` → `succeeded`, `stopped` → `cancelled`, `unknown` → last known state with `observation = unconfirmed`; other states keep their name |
    | `delegation` | `execution`, kind `agent_run` | `parent_execution_id` = the delegating execution |
    | `pipeline-run` | `execution`, kind `pipeline_run`, plus one `pipeline_step` per step | steps link by `parent_execution_id` |
    | `delivery` | `task`, plus executions for its steps | field and state mapping in §2 |
    | `session-fork`, `handoff` | `execution`, kind `agent_run` | `parent_execution_id` = the source execution |
    | `project-question`, `question` | `question` | kind `operator_decision` or `agent_question`; `task_id` filled from the delivery or from `execution.task_id` |
    | `artifact` | `artifact` and `artifact_copy` | base64 decoded, see below |

    Tasks are backfilled only from deliveries. Other legacy executions get no task.
*   **Out of scope.** Every other record kind moves unchanged into a Postgres table `document(kind, id, data jsonb)`, with the same behaviour as today. Those kinds get their own tables only when a later design needs them.
*   **Base64 artifacts.** They are decoded, hashed and uploaded to the PDS18 store. The base64 documents stay readable until delivery reads results through `artifacts.get`, which happens in the same phase.
*   **Readers move with writers.** Every module that reads run documents or events moves to the new tables in the same phase as the writers (item 19).
*   **Row shape during the transition (Phase 2).** A typed row (`execution`, `task`, `question`) keeps its typed columns and, in `data`, the record the coordinator API returns today (the legacy document). The execution and task writers write both in one statement. The store serves the legacy kinds (`execution`, `delegation`, `pipeline-run`, `delivery`, `question`, `project-question`) from these tables, so every route answers exactly as before while new code reads the typed columns. As built (2026-11-11): an execution record (kinds `agent_run` and `host_job`, the copy, verify, account-inspection, integration and script jobs of a host) has one writer, `putExecution` in `go/coordinator/records.go`; its state and observation live only in their columns, in the typed vocabulary, and the record the API returns is `data` with those two columns over it. A receipt `unknown` keeps the state and sets `observation = unconfirmed`. Pipeline runs and steps follow the same rule (an unknown outcome is `failed`, unconfirmed); execution rows have no `legacy_kind` or `legacy_id`. Since migration 32 question and task rows have none either: an agent question (`kind = agent_question`) and a project question (`kind = operator_decision` with a `request_id`) are built from the question columns, and `data` keeps only fields without a column (an answer's delivery). A delegation lives on its child execution's row (`execution.delegation`); a pipeline run's steps are their own `pipeline_step` rows. Migration 6 moves the existing documents in insertion order and keeps their `seq`.
*   **Row-level security scope.** Every table has `FORCE ROW LEVEL SECURITY`. The coordinator sets `fleet.project_ids = '*'` on each connection (operator scope), and `''` in a list makes rows without a project visible. Narrow per-operation scopes (`SET LOCAL`) come with the agent-facing tools of Phase 6, the first callers that act for one project.
*   **Cut-over.** A one-time migration runs during a short planned stop. The SQLite file stays read-only as the rollback source. Rolling back means pointing the coordinator back at SQLite. Any work done after the cut-over is then re-exported by the reverse script, which maps `document` rows and the typed tables (`execution`, `task`, `task_dependency`, `question`, `artifact`) back to legacy documents.

### 8. Agent-facing tools for results and history

The shapes are copied from t3code (`apps/server/src/mcp/toolkits/attachment/tools.ts`, `agentContextBudget.json`; revision `d0f1f7200d`) and Orca (`ORCA_CHAT_SCRATCH_DIR`, `orca skills get`; revision `0b93cef4450`).

*   **Output folder, the default path.**
    *   Each execution gets `FLEET_OUTPUT_DIR`, a folder inside its working area.
    *   Anything the agent writes there is collected by the executor when the run ends (§3, Output and transcript collection) and stored as artifacts of that execution.
    *   This extends the worktree output collection added in commit `23c5418`.
    *   The agent needs no tool call and no knowledge of storage.
*   **Explicit tools.** MCP tools on the existing `/api/tools` surface:
    *   `artifact_save({path, name?, media_type?, required?})` stores a file of the agent's working area now, rather than at the end. The executor uploads it through its own transfer path (§3 Write protocol) and returns `{artifact_id, state}`. Agents never receive store URLs or the Access token: the store hostnames sit behind Cloudflare Access, whose service token is a credential (G3, G8). Reserve, part URLs, commit and discard are routes between executor and coordinator only.
    *   `artifact_list({execution_id? | task_id?, media_type?})` returns metadata only.
    *   `artifact_get({artifact_id})` returns metadata, and the executor downloads the file into the execution's input folder (`FLEET_INPUT_DIR`, inside the working area, readable by the account user) and returns that path. The copy lives only as long as the working area. It never returns the file content inline.
    *   An agent acts only on its own execution's artifacts for writes; reads follow the project grant (§6). The task sets the minimum of required artifacts (§3 Success rule); an agent may mark more as required through `artifact_save`, never fewer.
    *   `history_query({filters, group_by?})` maps to `usage.summary` and `executions.query`.
    *   `transcript_read({execution_id, range? | search?})` returns redacted text marked as data, with paging (`cursor`, `limit`).
    *   `task_list({project_id?, state?})`, `task_get({task_id})`, `task_create({project_id, title, expected_outcome, depends_on?})` and `task_update({task_id, ...})` map to the `tasks.*` operations.
    *   `question_ask({text, options?, task_id?})` and `question_answer({question_id, answer})` map to `questions.*`.
    *   Every tool applies the permission rule in §6.
*   **Files as paths, not content.** Inputs and earlier results reach the agent as paths in the prompt, as both t3code and Orca do. The agent reads what it needs.
*   **Real schemas.** Every tool gets a real `inputSchema` instead of today's `{type:'object'}`, wherever that stub appears.
*   **Instructions on demand.**
    *   A short `fleet` skill stub says what the output folder is and lists the tool names.
    *   The full guide is read on request.
    *   A test enforces a byte budget on the tool list and on the stub, as t3code does.

### 9. Isolation boundary

#### Mechanism (FD5)

*   **Boundary.** One OS user per harness account, for example `fleet-claude-1` or `fleet-codex-2`. The account user owns its `HOME` and its native login (Keychain or file).
*   **Launch.** The executor runs under its own service user and launches each agent as the account user:
    *   Linux (PDS18): through the root-owned system unit template `fleet-launch@.service`. A polkit rule lets the `fleet` user start this template only, and only for instance names that are `fleet-*` users. Each execution runs in its own scope, which gives its cgroup. This is the tight-D4 amendment in Q8.
    *   macOS: `launchctl asuser`, or `sudo -u` through one narrow sudoers rule for the executor's service user that allows only `fleet-*` users.
    *   Windows: `CreateProcessWithLogonW`.
*   **Keys for each execution.**
    *   Project keys and connection credentials live only with the executor's service user.
    *   For each execution, the executor places the key it needs into a temporary file, or behind a per-execution `ssh-agent` socket, inside the working area. It deletes the key when the run ends.
    *   The account user's `HOME` never holds project keys, so projects that share an account cannot read each other's keys.
*   **Inner layer.** A harness's own sandbox (Claude Code Seatbelt or bubblewrap, Codex sandbox) separates executions that run under the same account user. It is configured per profile.
*   **Measurement.**
    *   Linux: the execution's cgroup gives exact CPU and memory. Basis: `measured`.
    *   macOS and Windows: each execution's process tree is sampled by pid, inside the account user's processes. Basis: `estimated`.
*   **Accepted gaps.**
    *   On macOS and Windows, there is no OS boundary between projects that share one account, and CPU per execution is an estimate.
    *   A macOS user's login Keychain is locked while that user is not logged in, and Claude then falls back to file storage. This is a recorded harness fact.
*   **Upgrade path.** Each gap closes with the per-execution OS mechanism for that platform, adopted one platform at a time when needed: Seatbelt on macOS, a Job Object plus AppContainer on Windows.

#### Requirements

*   **Secrets.** An agent's environment holds only the connections granted to its execution. Key files cannot be read from other boundaries. The Fleet machine token is never readable by an agent.
*   **Working area.** Each execution writes to its own working area. The shared `.git` is read-only to agents, and writes go through the delivery path. Host-side git runs with hooks and fsmonitor disabled, so files an agent planted in a checkout never run on the host.
*   **Network.** Network access is a setting per project or profile, not a fixed rule.
*   **Observation.** A person can log in to the boundary, or attach to it, and watch the agent.

### 10. People and access

*   **People: a host-only file, not an operation.** There will be at most about 10 people, and adding one is rare.
    *   People and their grants live in a host-only file, `people.json`, with `email`, `name`, `roles` (`operator`, `architect`) and project grants. The file stays outside the repository. The coordinator loads it into table `person` on start and when the file changes.
    *   Adding a person means editing that file and adding the email to the Access group `fleet-people` by hand. There is no invite workflow.
    *   The first entries are the owner, Alina and Simon.
*   **Table `share`.**
    *   Columns: `email`, `object_kind` (`project`, `task` or `artifact`), `object_id`, `expires_at` (optional), `granted_by`, `revoked_at`.
    *   A share gives read access to that object only.
*   **Cloudflare Access is kept in sync by Fleet.**
    *   The Access policies for the Fleet site and the store tunnels allow one Access group, `fleet-people`.
    *   People are added to the group by hand, as above.
    *   Shares are kept in sync by Fleet. When a share is created, Fleet adds the email to the group through the Cloudflare API. When an email's last live share is revoked or expires, Fleet removes it, unless the email belongs to a person.
    *   The Cloudflare API token, limited to editing this group, is a connection of the coordinator. It is never visible to agents.
*   **Fleet authorizes too.**
    *   The access guard (`fleet-gateway guard`, `go/gateway/guard.go`) still verifies the Access JWT, but passes the email to Fleet instead of matching one owner.
    *   Fleet maps the email to a `person` or a live `share`, and then applies the grants inside each operation. An email Fleet does not know gets 403 even if Cloudflare let it through.
*   **Access state and onboarding are Fleet work.**
    *   `access.status` lists people, grants, shares and expiry dates.
    *   It compares the `fleet-people` group with Fleet's list and reports every difference as a problem with an action: add, remove or ignore.
    *   `access.status` also lists the hand edits still needed, for example a person in `people.json` who is missing from the group.
*   **Agents.** An agent acts with its director's grants (the execution's `directedBy`, [fleet-pm-agent.md](fleet-pm-agent.md) §1); the actor of its writes stays the execution, never the person. Sharing with a new email is an operator action. An agent can only propose it as an `operator_decision` question.

### 11. Hosts, environments and stores

*   **One list, `hosts.list`.** For each registered host it returns:
    *   platform, last contact and observation state;
    *   environments and account users;
    *   harness versions and readiness;
    *   stores, with capacity, used and free bytes, and tunnel state;
    *   free disk;
    *   the running Fleet services, with their resource settings and current use.

    The data comes from the existing host observation plus `host.heartbeat` spans. Nothing is entered by hand.
*   **Settings are Fleet data, applied on the host.** Table `host_setting(host_id, key, value, updated_by, updated_at, applied_at, apply_error)`. Settings in this design:
    *   `store.capacity_bytes`: the executor applies it to Garage through the Garage admin API, as a layout capacity change.
    *   `service.<name>.memory_max` and `service.<name>.cpu_quota`: the executor applies them with `systemctl --user set-property` to its own Fleet user units, such as ClickHouse and Garage.
    *   `store.free_space_warning`: the threshold at which the store shows a problem.

    How settings work:
    *   The operator changes a setting with `hosts.set_setting`.
    *   The executor applies the setting on its next poll. It reports `applied_at` or `apply_error`, and the list shows both.
    *   Nothing has a built-in cap: every value is the operator's.
*   **Fill and problems.**
    *   When free space drops below `store.free_space_warning`, `hosts.list` and the problem view show the store, its fill per project, and the available actions: raise capacity, change a project's `artifact_locations`, or add a retention rule.
    *   A full store stops new uploads with a visible reason. It never fails runs silently.
*   **Agents** read `hosts.list` within their permissions. Changing a setting is an operator action, which an agent can only propose as an `operator_decision` question.

### 12. Document store concurrency model

Phase 1 moved handlers written for synchronous SQLite onto Postgres. On SQLite one writer ran at a time, and every handler was one atomic, isolated step. The handlers rely on that implicitly. Many of them read several documents, decide, and write others: "no active execution in this session, so start the queued message"; "this receipt does not regress the command"; "this event sequence is not taken". Running them under SERIALIZABLE gave the right answers but failed constantly. Live PDS18 logged 3553 serialization failures in about 4 minutes. A one-table store makes SSI predicate locks coarse (kind ranges, shared index pages), so unrelated documents conflict. Retries piled up until executor requests passed the executor's 5 s HTTP timeout (the Rust outbox, since ported to `go/executor/outbox.go`).

**Model.** Two transaction kinds and two lock scopes, in `Store.Tx` (`go/coordinator/store.go`):

*   **Read** (`write` false): `READ ONLY, REPEATABLE READ`. One consistent snapshot. It never blocks writers and never fails to serialize. A write inside it fails with 25006, so a handler classified as read-only by mistake fails loudly instead of corrupting data.
*   **Write** (`write` true): `READ COMMITTED` inside a **scope lock** held for the whole transaction. Each statement sees the latest committed data. Writers in the same scope run one at a time, so a handler's read, decide and write is as atomic and isolated as it was on SQLite.
    *   **GLOBAL** is the default scope.
    *   **Execution scope** `execution:<id>` is for host ingest of one execution.
    *   Inside the process, a queue per key orders waiters before they take a pool connection, so waiters hold no connection. The transaction then takes `pg_advisory_xact_lock` on the key, which also orders writers in other processes (the migration tool, a second coordinator during a deploy).
    *   Lock order is GLOBAL, then an execution scope. An execution-scope writer never takes GLOBAL, so the two scopes cannot deadlock.
    *   An execution-scope writer never writes `task_id`, `state`, `host_id`, `harness`, `started_at` or `seq` of an execution, nor `task_id` or `state` of a question. Those columns fire the `task_derived` recompute (migration 10), so only GLOBAL writers, which run one at a time, recompute. The recompute also locks the task's `task_derived` row before reading (two task ids in ascending order) as a second guard if this rule is ever broken.
    *   A `put` outside a transaction is a one-statement GLOBAL write, so it never lands inside another writer's transaction.
    *   Deadlocks (40P01) are still retried with a short random backoff. A failure that handler code swallowed dooms the transaction, which is then rolled back and retried.

**Why not the alternatives.**

*   *SERIALIZABLE everywhere* (Phase 1 to PR #15): correct, but it generated the failure storm described above. Narrow indexes cut the conflicts by about 3 times; the rest are page-level false sharing between neighbouring ids.
*   *READ COMMITTED or REPEATABLE READ with row locks (`FOR UPDATE`) and per-session advisory locks*: row locks protect the documents a handler changes, not the conditions it checks. For example, "no other queued message of this session is earlier" or "no event of this execution has this sequence" are conditions over many rows. That needs a scope lock anyway. The hot write paths, poll and receipts, run `promote`, `delegates.reconcile` and delivery `advance`. These loops read across every session and every delegation and enqueue work into any of them, so their scope is every session, which is GLOBAL.
*   *SERIALIZABLE only for a few handlers*: under SSI, conflicts are detected only between SERIALIZABLE transactions. Mixing levels gives the SERIALIZABLE handlers no protection against the others.

Throughput is not the constraint. A write transaction takes a few milliseconds (poll: 21 statements). Live traffic is 4 pollers at about 1 per second plus executor outboxes, so one writer at a time is far from saturated. Execution-scope ingest runs in parallel with GLOBAL writers and with other executions.

**Handler groups.**

| Group | Invariant it needs | Mechanism |
|---|---|---|
| Poll `/api/hosts/poll` | One host observation per host. `delegates.reconcile` delivers each child result once. `promote` starts only the earliest queued message of an idle session. A pending command is returned only when its execution and delegation inputs are ready. | GLOBAL write (the loops span all sessions) |
| Receipts `/api/hosts/receipts` (status; `/api/hosts/project-operations`, `/api/hosts/delegations`, `/api/hosts/accounts/report` for the other envelopes) | Command status never regresses. Native identity is set once. The execution state follows the command. `onReceipt`, `promote` and `advance` see the new state. | GLOBAL write. Account observations and native project tools are GLOBAL too; pipeline runs open their own short GLOBAL transactions. |
| Events `/api/hosts/events` | A batch is validated whole. An event id is immutable and a sequence number is used once per execution. A colliding frame becomes one transport conflict. | Execution scope. Only ingest and `transport_authorize` write `event`, `transport-conflict` and `transport-audit`; `transport_authorize` runs GLOBAL and also takes the execution scope. |
| Artifacts `/api/hosts/artifacts` | An artifact id never changes content. | Execution scope. Only this route writes `artifact`. |
| Agent tools `/api/hosts/agent-tools` | Session invariants (`message_send` queue order, `session_fork`, `worker_done` accepted once, and so on). | Each tool call is its own GLOBAL write, or a read when the operation is in `READ_OPERATIONS`. |
| Ops `/api/ops/:name`, MCP `/api/tools`, project and recovery routes | Same as the operation's own group. | `READ_OPERATIONS` run as reads. `LONG_OPERATIONS` (pipeline and delivery generation) open short GLOBAL transactions around their synchronous steps. All others are one GLOBAL write. |
| UI reads (every GET and HEAD) | A consistent view. | Read snapshot. A static scan and the read-only transaction both confirm that none of them writes. |
| Startup recovery (`onReady`) | Runs once, before traffic. | GLOBAL write |

**Phase 2 additions.**

| Group | Invariant it needs | Mechanism |
|---|---|---|
| Task and question operations (`task_create`, `task_update`, `questions_answer`, legacy `question_answer`, `reply`) | Task state changes only through the transition table (§2); a question is answered once; acceptance completes its task once. | GLOBAL write |
| `task_get`, `task_list`, `questions_list`, `executions_list` | A consistent view of one task with its executions, questions and `task_view`. | Read snapshot |
| Typed writes inside receipts and poll (execution rows, task state from delivery, `detectProblems`) | Same as their handler. | GLOBAL write |
| Typed writes inside host ingest (observation and `last_seen_at`, `harness_version`, `child_agent` rows of the execution, `execution_totals`) | Columns that no GLOBAL writer sets. Totals are recomputed from the row, so a recomputation from either scope gives the same value. | Execution scope. A GLOBAL writer that updates the same execution row waits for its row lock. |
| Migration 6 (documents to typed tables) | Runs once, alone. | One GLOBAL write |

**Phase 3 additions.**

| Group | Invariant it needs | Mechanism |
|---|---|---|
| Artifact routes (`reserve`, `parts`, `commit`, `verified`) | One artifact row per reservation; a verified object is never overwritten; promotion happens once. | Execution scope for short transactions around store calls; the store calls themselves run outside any transaction. |
| `POST /api/hosts/stores` and store fields of poll | Only the store's own host registers or updates it. | GLOBAL write |
| Placement worker, lease expiry, purge reconciliation | Copy, delete and purge decisions see current rows; never delete a last copy. | GLOBAL write per decision; store calls between transactions |

**Whole-kind reads on write paths.**

*   `delegates.reconcile` now reads open delegations through a partial index (migration 5) instead of all delegations.
*   `delegates.ready` no longer scans every artifact when the parent has no source run.
*   Filters with `id`, `executionId` or `sessionId` use btree indexes (migration 4). Other equality filters use the GIN index.
*   Delivery `advance` still reads every delivery. It recomputes each delivery's state from its step executions before it decides to skip, so no stored field predicts which deliveries it touches. The kind holds one record per delivery request.

**Measured** on local PostgreSQL 17 with the live-volume fixture (the TypeScript coordinator's load fixture, since deleted: 4 pollers, two executor outboxes replaying 100 events and 10 receipts):

| Load | Before (main f22ea6b) | After (this model) |
|---|---|---|
| Live shape | 11–17 retries per run; max 153–198 ms; the owner-mac poller finished 1 poll | 0 retries; p95 19–22 ms; max 22–25 ms |
| Single-event rows, pollers without pause (150 requests) | 36–41 retries; max 168–265 ms | 0 retries; p95 12–13 ms; max 24–28 ms |
| 81 concurrent host requests | 125–127 retries; 300 ms | 0 retries; 112–115 ms |

## Alternatives Considered

### A1: GitHub or GitLab issues as the task store

*   **Pros**: A ready interface, notifications, and links to PRs.
*   **Cons**:
    *   Issues are public on a public repository.
    *   GitHub applies API rate limits that slow agents down.
    *   The tracker becomes a second source of truth next to Fleet's own state (`state-ownership.md`).
*   **Reason for Rejection**: Rejected by author: tasks must be private by default and fast for agents. An external board stays possible as an optional export from Fleet's own table.

### A2: A self-hosted tracker (Plane, Forgejo, GitLab issues) as the task store

*   **Pros**: A ready interface without GitHub's limits, and the data stays on the owner's infrastructure.
*   **Cons**: It adds a second state model and a second permission model. Agents would read and write through someone else's API, and Fleet's task states would have to be mapped onto it.
*   **Reason for Rejection**: Rejected by author: mapping Fleet's task states and project grants onto another tracker's model costs more than one task table and one view. Fleet keeps tasks in its own tables.

### A3: A task for every run, including laboratory trials

*   **Pros**: One object answers both "what was done" and "what did it cost".
*   **Cons**: Thousands of lab trials would flood the operator's work views. A task would then mean two different things.
*   **Reason for Rejection**: Rejected by author: the execution is the unit of analysis. A task is optional and covers production work only. Lab runs link to an experiment.

### A4: Task hierarchy from the start

*   **Pros**: It fits epics and large features.
*   **Cons**: It adds rules for how states and costs roll up before any real need for them is known.
*   **Reason for Rejection**: Rejected by author: start with flat tasks plus dependencies, and refactor if real work shows a need.

### A5: SQLite with a relational schema as the core database

*   **Pros**: No new service, the current stack, and a quick migration from `records`.
*   **Cons**:
    *   Only one process can write.
    *   Row-level permissions must be built in code.
    *   Heavy queries compete with live operations.
*   **Reason for Rejection**: Rejected by author: Postgres on PDS18 allows several writers, row-level security for project grants, and time partitions.

### A6: Keep artifacts in the database, with a larger size limit

*   **Pros**: No new store.
*   **Cons**: Base64 inflates size by about a third, and database backups grow with every video. Streaming and presigned access are not available.
*   **Reason for Rejection**: Rejected by author: real results include 3D models and video, so they need an object store.

### A7: A custom artifact file server

*   **Pros**: Full control of the protocol.
*   **Cons**: It reimplements uploads, multipart transfer, presigned access and lifecycle rules, all of which S3-compatible stores already provide.
*   **Reason for Rejection**: Rejected by author: a custom server must rebuild multipart, presigned access and lifecycle rules that any S3 server already has, and Fleet would maintain that code.

### A8: MinIO on each host with its built-in bucket replication

*   **Pros**: Replication rules per bucket are built in. It has the most complete S3 API and a native Windows binary.
*   **Cons**: The community edition is restricted and its future is unclear. Without a subscription, Fleet could be left on a frozen version, and replication features could move to the paid edition.
*   **Reason for Rejection**: Rejected by author: the licence and update risk is too high. Placement managed by Fleet over independent stores gives the same per-project control with any S3 server.

### A8b: One SeaweedFS cluster with placement rules per path

*   **Pros**: Placement is built into the store, through data center and replication settings per bucket prefix. Windows binaries exist. It is one cluster.
*   **Cons**: More components (master, volume and filer servers). The cluster spans hosts that sleep or reboot.
*   **Reason for Rejection**: Rejected by author: a sleeping Mac makes the cluster see a lost volume server. If the master on PDS18 is down, even local writes on Windows fail. Independent stores keep working on their own.

### A8c: One Garage cluster across all hosts

*   **Pros**: One store with built-in replication.
*   **Cons**: Garage's layout is cluster-wide. Copies spread across zones by one replication factor, so one project's files cannot be pinned to one host.
*   **Reason for Rejection**: Rejected by author: it cannot express "this project only on Windows". Garage stays as the single-node store on each host.

### A8d: AssetQ / Pipe as the artifact store

*   **Pros**: Already deployed on PDS18, with Postgres metadata, immutable file storage, scoped API and connection budgets.
*   **Cons**: It is a prototype from an earlier iteration of the same idea, its storage is local to PDS18, and its proposed limits are 50 GiB of objects.
*   **Reason for Rejection**: Rejected by author: AssetQ stores files only on PDS18, so it cannot keep a project's files on Windows only (CUJ 5).

### A9: Cloudflare R2 as the object store

*   **Pros**: Nothing to operate, no egress fees, and reachable from any host.
*   **Cons**: Data sits outside the owner's infrastructure. Storage is billed per GB-month, and video and 3D add up quickly.
*   **Reason for Rejection**: Rejected by author: the owner wants a space Fleet fully controls, with files that never leave a chosen host.

### A10: Postgres alone for events, spans and measurements

*   **Pros**: One database, one permission model, one schema.
*   **Cons**: At about 1 million events per day with no expiry, free-form analytical queries over the whole history become slow without many rollup tables, and they load the live database.
*   **Reason for Rejection**: Rejected by author: ClickHouse sits next to Postgres for analytics. Postgres keeps state and per-execution totals.

### A11: Self-hosted Langfuse for traces and analytics

*   **Pros**: A ready trace viewer, cost, scores and datasets.
*   **Cons**:
    *   Its trace, observation and score model is a second record of each execution next to Fleet's own.
    *   Its permissions do not match Fleet's project grants.
    *   It is a heavy stack, and its S3 needs are unverified against Garage.
*   **Reason for Rejection**: Rejected by author: two records of each run must be kept in step, and Langfuse's permissions cannot express project grants.

### A12: An OpenTelemetry Collector on every host, writing to ClickHouse

*   **Pros**: A standard ingest tool with a disk buffer and a ready host-metrics receiver.
*   **Cons**:
    *   Data reaches the stores around Fleet's own layer.
    *   It adds a process on every host, Windows included.
    *   Parsing configuration per harness is still needed.
*   **Reason for Rejection**: Rejected by author: the layer between Fleet and the harnesses is Fleet's responsibility. Everything passes through the executor adapters and the existing outbox, with no extra subsystem. Native OTel from a harness becomes an input to the adapter.

### A13: Hybrid ingest (outbox for state, a collector for raw logs)

*   **Pros**: Large volumes go through a standard tool, and data critical for accounting goes through the proven path.
*   **Cons**: Two channels need a check that every execution's history is complete.
*   **Reason for Rejection**: Rejected by author: the same reasons as A12. Raw transcripts are uploaded directly to the store with presigned URLs instead.

### A14: An OS boundary for each execution (Seatbelt, cgroup and namespaces, Job Object plus AppContainer)

*   **Pros**: Projects that share an account are separated by the OS, and CPU is exact for each execution on every platform.
*   **Cons**: Three platform mechanisms to build and keep up with harness updates. A harness may stop working inside Seatbelt or AppContainer, for example because it cannot reach the Keychain or the GUI.
*   **Reason for Rejection**: Rejected by author: three platform mechanisms cost more than the two gaps they close (project separation within one account on macOS and Windows, and exact CPU there). It stays as the upgrade path, adopted one platform at a time when needed. Linux already gets per-execution cgroups through `systemd-run --scope`.

### A15: One OS user per account and project pair

*   **Pros**: Projects are separated by the OS with a single, simple mechanism.
*   **Cons**:
    *   The number of OS users grows as accounts times projects.
    *   Each user needs its own native login to the same subscription.
    *   Token refreshes across those copies can race.
*   **Reason for Rejection**: Rejected by author: onboarding cost grows with every project. Issuing keys for each execution separates project keys without new OS users.

--------------------------------------------------------------------------------

## Non-Functional Requirements

*   **Security & Privacy.**
    *   Agents never receive storage, database or Fleet machine credentials.
    *   Project keys exist only for the length of an execution (FD5).
    *   Secrets are masked twice: by exact value in the executor, and by pattern in the coordinator. Coverage limits are in Technical Plan §5.
    *   History returned to agents is marked as data.
    *   Postgres row-level security on `project_id`, forced on every table and applied to the non-owner role `fleet_app`, backs up the permission checks in each operation.
*   **Performance.** These numbers are working targets for measurement, not caps.
    *   Task and execution list queries return in under 200 ms at 10 million executions.
    *   `usage.summary` over 30 days of one project returns in under 2 s.
    *   Outbox ingest keeps up with 10 parallel sessions. Ten parallel Codex turns were proven live on today's outbox; the new event classes are a target to measure in Phase 4.
*   **Durability.**
    *   Postgres is backed up by the existing nightly `pg_dump` (tight-D6). A backup counts only after a restore proof.
    *   ClickHouse and Garage backups go to `/backup/fleet/` for the provider pull. Their restore proof is part of acceptance.
    *   Projects with a single artifact location accept the risk of a single copy. The task view shows that risk.
*   **Rollout & Rollback.**
    *   Rollout is phased; each phase ships on its own.
    *   Until Phase 7 retires SQLite, rollback means pointing the coordinator back at the read-only SQLite file and running the reverse export, which covers the `document` table and the typed tables.

## Risks & Mitigations

*   **The Windows store is offline while no user is logged in.** Mitigation: copies stay `pending` and resume at logon; readiness shows `requires logon`; a `[windows]`-only project accepts this, and its uploads wait under `artifact_retry_window` (§3).
*   **One coordinator key opens every store.** The coordinator holds the read-write key of each store; a leaked coordinator secret allows writes and deletes everywhere. Mitigation: keys live only in the coordinator's 0600 secret directory, executors hold none, and a store's host can replace its key by registering again.
*   **A verified copy rots on disk.** The daily re-hash audit is deferred past Phase 3. Mitigation until then: copies verify at write and copy time, and reads stream through a verified copy whose size is checked.
*   **Abandoned multipart uploads keep space.** Mitigation: leases abort uploads the coordinator knows; a periodic `ListMultipartUploads` sweep for unknown ones is a follow-up.
*   **ClickHouse returns duplicates before a merge.** Mitigation: every summing query uses `FINAL` or `argMax` by `seq` (§4); a test feeds a duplicate batch and checks totals.
*   **A cross-host transfer stops halfway.** Mitigation: 32 MiB multipart parts, each retried on its own, with the upload state kept on disk (§3).
*   **A store host is offline.** Mitigation: copies stay `pending` and resume; the artifact shows as unavailable, not lost.
*   **A deleted row removes bytes another row needs.** Mitigation: a store copy is deleted only when no live row needs it (§3).
*   **A secret reaches history.** Mitigation: two masking passes, and the forced purge for anything found later (§3, §5).
*   **The launch helper grants too much.** Mitigation: it starts only `fleet-launch@` instances for `fleet-*` users, and the isolation probe checks it (item 54).
*   **Migration breaks a reader.** Mitigation: readers move in the same phase as writers, and the reverse export allows rollback until Phase 7.

## Implementation status

The phases below were built and verified live on PDS18 with the TypeScript coordinator and Rust executor, then carried unchanged into the Go port: `go/coordinator` (schema `go/coordinator/schema.sql`, store `store.go`, artifacts `artifacts.go`, placement `placement.go`, S3 client `d_s3.go`) and `go/executor` (upload `upload.go`, outbox `outbox.go`). Tests are the outside tests in `tests/go` and `tests/trial` and `go test ./...`.

*   Phase 0, infrastructure on PDS18 (Postgres role, ClickHouse, Garage): built ([infrastructure-build.md](../infrastructure-build.md)).
*   Phase 1, the `document` table on Postgres: built.
*   Phase 2, typed `execution`, `task` and `task_dependency` tables: built.
*   Phase 3, the artifact store, placement and the output folder: built.
*   Not built: Phase 4, ingest to ClickHouse, harness adapters and redaction.
*   Not built: Phase 5, the isolation boundary beyond step 1 (per-account OS users, git access and worktree checks).
*   Not built: Phase 6, usage aggregation and query operations for agents ([section 8](#8-agent-facing-tools-for-results-and-history)).
*   Not built: Phase 7, the documentation pass ([state ownership](../state-ownership.md)).

### Phase 5 step 1 on PDS18: the executor under its own user


Today `fleet-executor.service` and every agent turn it starts run as `fleet`, the coordinator's own uid, so a turn can read `coordinator.env` (operator Bearer, machine secret, database URLs) and every other coordinator file. Step 1 closes that, before per-account users (items 38 to 41), and changes nothing in daily work: same host id `pds18-native`, same accounts and profiles, no new prompts or approvals.

*   New user `fleet-agent` (uid >= 1000, own group, not in group `fleet`, home `/home/fleet-agent` 0750, linger). `/home/fleet` stays 0750 and `.config/fleet`, `releases` and `data` stay 0700 `fleet`, so `fleet-agent` cannot open any coordinator file.
*   The executor runs as a user unit of `fleet-agent`. These move (no copies kept; owner guidance 2026-11-10: nothing to preserve) to `/home/fleet-agent`: the executor data directory (with `native-home`), the Codex account home `accounts/codex-pds`, `native-accounts.pds18.json`, `store-pds18.json`, the git connections of the projects (`fleet-hello-world`, `fleet-hello-world-alt`, `known_hosts`), the checkouts `projects/`, and `executor-pds18.env`. Only the executor holds its machine credential; separating hosts from one another is fleet-people-access slice 2.
*   The executor, Codex and Antigravity binaries are copied out of `/home/fleet/runtime` (unreachable behind 0750) to `/opt/fleet-agent/runtime`, root-owned 0755, so an agent cannot replace them.
*   The project configs change their `pds18-native` `checkoutPath` from `/home/fleet/projects/<id>` to `/home/fleet-agent/projects/<id>`.
*   The program worker (delivery, publication) has no agent turns and stays `fleet`.
*   Check: as `fleet-agent`, reading `/home/fleet/.config/fleet/coordinator.env` fails with Permission denied; one real Codex turn on `pds18-native` completes, with its artifacts uploaded.
*   `/home/fleet` itself becomes 0700. The store config's `backingPath` (free-space reading) points at the mount point `/`, because `fleet-agent` cannot reach the Garage data directory.
*   The old `fleet-executor.service` of user `fleet` and `/home/fleet/runtime/{executor-*,codex-*,agy-*}` are deleted. Safety is the pre-deploy `pg_dump`.

### Verification

*   Build and unit tests: `cd go && go vet ./... && go test ./...`; the app with `pnpm run build`.
*   Outside tests: `tests/go/run.sh` after `pnpm run go:build`.
*   Live, per phase on PDS18 and the Mac: Phase 4, CUJ 4 and CUJ 6, and a seeded fake token never reaches ClickHouse or Garage; Phase 5, an agent turn cannot read another account's files; Phase 6, an agent answers the CUJ 4 question by citing execution IDs.
*   Backups: a restore proof of Postgres, ClickHouse and Garage from `/backup/fleet/` before the design counts as shipped.

*Drafted with EGM*
