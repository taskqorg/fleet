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
*   **Executor.** The Rust service on each host (`crates/executor`). It launches harnesses, collects their output and reports to the coordinator.
*   **Outbox.** The executor's local queue of rows for the coordinator, sent in batches over HTTP, each row with a sequence number `seq`.
*   **Harness.** An agent program that Fleet launches: Codex, Claude Code, or Antigravity (`agy`, its CLI). An **adapter** is the executor code that reads one harness's output.
*   **ACP.** The Agent Client Protocol, the session protocol Antigravity speaks.
*   **Session.** One continuous conversation with a harness. An execution may run inside a session.
*   **Profile.** A named agent setup in `config/agents/*.json`: harness, model, instructions, skills and sandbox settings.
*   **Environment.** A registered place where an execution runs on a host: a working area plus the settings it needs.
*   **Project grant.** The record that says which projects a person or agent may read or change. Every query is filtered by it.
*   **Experiment and variant.** An experiment is a lab study, stored as the existing `experiment` document. A variant is one way of working that the experiment compares. Each lab execution records `experiment_id` and `variant`.
*   **Hosts.** PDS18 is the central Linux server. The other hosts are the owner's Mac and Windows machines. A host ID is the ID its executor registers with.
*   **tight.** The owner's infrastructure rules for PDS18, in `~/Projects/tight/tight.md`. Its decisions are written `tight-D4`, `tight-D6` and so on. This design's own decisions are `FD1` to `FD6`.
*   **AssetQ and Pipe.** Earlier prototypes on PDS18: AssetQ stores files with Postgres metadata; Pipe holds provider keys and records each API call.
*   **t3code and Orca.** Two existing agent IDEs whose tool shapes and parsing code Fleet copies. Revisions are named where they are used.
*   **EGM.** The design-review method used for this document.

## The Problem

Today the coordinator keeps all state in one SQLite table, `records(kind, id, data)` in `packages/coordinator/app.ts:19`, as JSON documents of about 30 kinds. Several problems follow:

*   **No single definition of a run.** The same idea appears as `execution`, `delegation`, `pipeline-run`, `delivery`, lab `trial` and `session-fork`. Each kind has its own states and its own way of linking to a project. Questions such as "what did project X spend this week" or "how did variant A compare to B across all its runs" need custom code for each kind.
*   **No task object.** `fleet.md` describes tasks and their states, but no record exists. Operators have nothing to track progress on or accept. In earlier iterations public GitHub issues filled this role: everyone could see them, and GitHub rate limits slowed the work.
*   **Artifacts cannot hold real results.** Artifacts are stored as base64 inside records, with a 16 MB limit (`FLEET_MAX_ARTIFACT_BYTES`). The executor rejects glb, mp4 and pdf files. A run whose required artifact upload failed with a 4xx is still marked successful. So 3D models, video and large reports cannot be results, and a "successful" run can be missing its result.
*   **History and measurements are incomplete.**
    *   The executor drops harness rate-limit events.
    *   Account quota is stored as the string `'unknown'` (`packages/coordinator/accounts.ts:18`).
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
    *   A task's progress, cost and latest result are derived from its linked executions, not stored twice. The stored `state` changes only through the transitions in Technical Plan §2.
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

*   **Q2. S3 store on Windows.** Garage officially targets Linux. Decide whether the Windows store is Garage under WSL2 or another S3 server with a native Windows build. Placement does not depend on this choice, because each store is independent.
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
    *   Claude 2.1.319 skips the retention sweep when `--setting-sources` excludes user settings and no other enabled source provides `cleanupPeriodDays`. Fleet launches Claude with `--setting-sources ""` (`crates/executor/src/harness.rs:226`), so Fleet-launched transcripts are not deleted by Claude.
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
    *   It fits the Rust executor. It does not fit the TypeScript coordinator, which uses a `current` symlink.
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
| FD2 | Object store and placement | An independent single-node S3 store on each host (Garage; on Windows, Garage under WSL2 or another S3 server, see Q2). Each store is published through Cloudflare Tunnel behind Cloudflare Access, as Fleet and Dash are. Fleet manages placement: each project lists the locations that must hold its artifacts, and Fleet copies objects between stores and checks their hashes |
| FD3 | Events, spans and measurements | ClickHouse next to Postgres, as a rootless Podman Quadlet unit of the `fleet` user (tight-D4 and tight-D5). Separate from the ClickHouse that belongs to Langfuse |
| FD4 | Getting history off the hosts | Extend the executor outbox. Raw transcripts and artifacts upload directly to the store with presigned URLs. No collector process on the hosts |
| FD5 | Isolation boundary | One OS user per harness account, with the harness sandbox inside. Project keys are issued for each execution and never stored with the account user. On Linux, each execution runs in its own systemd scope, started through the root-owned `fleet-launch@.service` template |
| FD6 | Running the services on PDS18 | Follow tight: one Unix user `fleet`, user systemd units with linger, no Docker and no sudo except the one launch helper in Q8, containers only as rootless Podman Quadlet. Garage on PDS18 also runs as a Podman Quadlet of `fleet`. Hosts other than PDS18 follow the same pattern with their native service manager |

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
*   **Kinds.** `agent_run`, `pipeline_run`, `pipeline_step`, `child_agent`, `tool_run` (a Fleet tool or connection call that is billed or long-running on its own). A lab trial is an `agent_run` or `pipeline_run` linked to an experiment. It is not a separate kind. A delivery is not an execution kind: it is a task, and its steps are executions of that task (§2).
*   **States.** `preparing`, `running`, `waiting`, `succeeded`, `failed`, `cancelled`. Observation confidence is a separate field (`confirmed` or `unconfirmed`, plus `last_seen_at`). Lost contact leaves the last known state and marks it unconfirmed, as `fleet.md` requires.
*   **Unplaceable executions.** An execution bound to a host, account or environment that is not registered is shown as a problem with that reason. It does not wait silently. Example from live state: `6b00f0fb` has waited forever for the host ID `pds18`, which no executor has registered.
    *   **Problem rule.** A problem explains why an *open* execution cannot run. `detectProblems` sets and clears it only on open executions. Reaching `succeeded` clears it. `failed` and `cancelled` keep the last problem as the explanation of the failure. Migration 7 clears problems left on succeeded executions.
*   **Mapping from the prototype.** The full mapping per legacy kind is in §7. Each migrated row keeps `legacy_kind` and `legacy_id`.
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
*   **Derived, not stored.** Progress, cost so far and the latest result come from the view `task_view` over the task's executions, artifacts and questions. Only `state` and `wait_reason` are stored, and they change only through these transitions, all written by `tasks.ts`:

    | From | Trigger | To |
    |---|---|---|
    | (new) | `tasks.create` | `queued` |
    | `queued` | the first execution of the task starts and all dependencies are `completed` | `in_progress` |
    | `queued` | an open question of kind `operator_decision` blocks the task before its first execution, or a dependency is not `completed` | `waiting`, with `wait_reason` |
    | `in_progress` | an open question of kind `operator_decision` or `agent_question` blocks the task, or a dependency is not `completed` | `waiting`, with `wait_reason` |
    | `waiting` | the blocking question is answered, or the dependency completes | `in_progress` |
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

### 3. Artifact store

*   **Table `store`.** One row per host store: `id`, `kind` (`s3`), `endpoint` (the store's Cloudflare Tunnel hostname), `bucket`, `host_id`, `state` (`reachable`, `unreachable` or `full`), `capacity_bytes`, `used_bytes`, and the credentials reference. The credentials themselves stay in the coordinator's and executor's secret configuration, never in rows.
*   **Project setting `artifact_locations`.** A list of hosts that must hold the project's artifacts, for example `[windows]`, `[mac]` or `[windows, mac]`. The default is the PDS18 host ID. Optionally, a separate list can be set for lab output and for production output. The setting changes only placement. It never limits who may read the artifacts, which follows project grants.
*   **Table `artifact_copy`.** `artifact_id`, `store_id`, `state` (`pending`, `copying`, `verified`, `failed` or `deleted`), `verified_at`, `last_error`. An artifact is `stored` once one copy is `verified`. It is `placed` once every location in the project's list holds a verified copy. The task and result views show both states.
*   **Store connectivity.** Each host store listens on loopback and is published through Cloudflare Tunnel (`cloudflared`) under its own hostname, behind Cloudflare Access, the same way Fleet and Dash are published. Executors and the coordinator pass Access with a service token. Operators pass Access with their normal login. Presigned URLs are signed for the tunnel hostname.
*   **Transfers.** Every upload and copy uses S3 multipart with 32 MiB parts, below the Cloudflare request-body limit. A file of 32 MiB or less is one part. Each part is retried on its own. A pending upload, with its upload ID and finished parts, is kept on disk, so it resumes after a restart.
*   **Placement worker.** It runs in the coordinator. For each artifact with missing copies, it queues a `copy` command to the executor of the target host. That executor downloads from a verified store and uploads to its local store, both through the tunnels, then the coordinator checks the hash and marks the copy `verified`. The copy never passes through an operator's machine.
    *   A store that is offline (a sleeping Mac, a Windows reboot) leaves its copies `pending`. They resume when its host reports the store reachable again.
    *   Objects are immutable and keyed by hash, so a repeated copy is safe and there are no version conflicts.
    *   Changing a project's locations schedules new copies. Removing a location deletes copies there only after the other locations hold verified copies.
*   **Shared objects.** Several `artifact` rows in one project can point to the same object, because the key is the content hash. A store copy of an object is deleted only when no live (not deleted) `artifact` row with that `content_hash` in that project needs that store. Deleting one row otherwise only writes its tombstone.
*   **Forced purge.** `artifacts.purge(content_hash, reason)` removes an object that contains a leaked secret from every store at once, whatever rows still point to it. It marks every such row `deleted` with `deleted_by = purge:<reason>`. Only the operator may run it, and it is logged as an event.
*   **Local first.** A worker writes to the store on its own host when that host is in the project's list. Otherwise it writes to the first store in the list that is `reachable`. Files that must stay on one machine therefore never leave it (CUJ 5).
*   **Previews.**
    *   After an image or video artifact is committed, the executor that uploaded it builds previews with `ffmpeg`, if `ffmpeg` is present on that host:
        *   a 512 px `thumbnail` for an image;
        *   a `poster` frame plus a 512 px `thumbnail` for a video.
    *   Previews are stored as artifacts with `derived_from_artifact_id`, follow the project's placement, and can be deleted and rebuilt at any time.
    *   Preview work runs at the lowest CPU priority (`nice 19` on macOS and Linux, idle priority on Windows), so it does not slow agents on the same host.
    *   A host without `ffmpeg` reports this in its readiness, and the preview shows as `unavailable`.
    *   3D models get no preview in this design.
*   **Retention rules (optional, set per project).** A project may add rules to `artifact_rules`, for example:
    *   `{scope: lab, older_than_days: 30, action: move, to: [nas]}`;
    *   `{scope: lab, older_than_days: 180, action: delete}`.

    How the rules run:
    *   The placement worker carries out a rule as ordinary copies and deletions, under the same safety rules: delete only after the target holds verified copies, and only when no live row still needs the object.
    *   Deletion keeps the `artifact` row as a tombstone (`state = deleted`, `deleted_at`, `deleted_by = rule:<id>`).
    *   A new or changed rule runs only after the operator reviews a preview. The preview lists the count, total size and a sample of the affected artifacts, and the operator confirms it.
    *   Without rules, nothing moves or expires. History in ClickHouse is not affected by artifact rules.
*   **Reading.** `artifacts.get` returns a presigned link to a verified copy on its store's tunnel hostname, preferring the reader's own host. When no listed store is reachable, the artifact is shown as unavailable, not lost.
*   **Table `artifact`.**
    *   `id`, `content_hash` (sha256), `size`, `media_type`, `name`;
    *   `store_id`, `object_key`;
    *   `execution_id`, `project_id`, `task_id`;
    *   `required`, `previous_artifact_id`;
    *   `derived_from_artifact_id` and `role` (`original`, `thumbnail`, `poster` or `web`). A preview is an ordinary artifact that points to its original;
    *   `state`: `pending`, `stored`, `failed` or `deleted`;
    *   `deleted_at` and `deleted_by`, which form the tombstone.
*   **Object keys.** `<project>/<lab|prod>/<sha256>` in each store. The same content in the same project is stored once per store (see Shared objects).
*   **Write protocol.**
    1.  The executor asks the coordinator to reserve an artifact, sending name, size, type and hash.
    2.  The coordinator starts a multipart upload on the chosen store in the project's list and returns presigned part URLs.
    3.  The executor uploads the 32 MiB parts directly to the store through its tunnel hostname.
    4.  The executor confirms through the outbox.
    5.  The coordinator checks the object's size and hash on the store, then marks it `stored`.
*   **Success rule.** An execution with a `required` artifact that is not `stored` cannot move to `succeeded`. It stays `running` while retries continue, for up to the operator setting `artifact_retry_window` (default 24 hours), then becomes `failed` with the upload error. This replaces today's behaviour, where a run succeeds after a 4xx.
*   **Raw transcripts.** These are artifacts with `media_type` set to the harness log format. They are uploaded when the run ends, and in chunks during long runs. This protects them against local cleanup (CUJ 6).
*   **No size limit in Fleet.** The base64 path and `FLEET_MAX_ARTIFACT_BYTES` are removed, in the coordinator and in the executor. Limits are the store's disk, and they show as store capacity.

### 4. ClickHouse: events, spans, measurements

*   **Tables.**
    *   `event`: execution state changes, questions, errors and harness lifecycle.
    *   `span`: model calls, tool calls and shell commands, with tokens (input, output, cache read, cache write), duration and status.
    *   `measurement`: CPU, memory, rate-limit signals and connection cost, each with its `basis`. Connection cost rows carry `connection_id`, `amount`, and `currency` or `credits`.
    *   `host_metric`: host health, disk, harness versions and reachability.
*   **Columns copied from Postgres at ingest.** `project_id`, `task_id`, `experiment_id`, `account_id`, `host_id` and `execution_id`. Every query can then filter by permission and group without a join.
*   **Engine and keys.** `ReplacingMergeTree`, partitioned by month and `ORDER BY (project_id, execution_id, seq)`. The sort key is the deduplication key, so a retried outbox batch with the same `seq` collapses to one row. `ts` is a plain column.
*   **Reading without double counts.** Duplicates disappear only when ClickHouse merges parts in the background. Every query that sums therefore reads with `FINAL`, or takes one value per `seq` with `argMax`. No TTL by default. A project retention rule can add one later.

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
*   **Host metrics.** The executor adds periodic `host_metric` reports to the existing host observation: disk, load, harness versions and reachable stores.

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
    | lab trials inside `experiment` | `execution` with `experiment_id` and `variant` | the `experiment` document stays in `document` |
    | `session-fork`, `handoff` | `execution`, kind `agent_run` | `parent_execution_id` = the source execution |
    | `project-question`, `question` | `question` | kind `operator_decision` or `agent_question`; `task_id` filled from the delivery or from `execution.task_id` |
    | `artifact` | `artifact` and `artifact_copy` | base64 decoded, see below |

    Tasks are backfilled only from deliveries. Other legacy executions get no task.
*   **Out of scope.** Every other record kind moves unchanged into a Postgres table `document(kind, id, data jsonb)`, with the same behaviour as today. Those kinds get their own tables only when a later design needs them.
*   **Base64 artifacts.** They are decoded, hashed and uploaded to the PDS18 store. The base64 documents stay readable until `delivery.ts` reads results through `artifacts.get`, which happens in the same phase.
*   **Readers move with writers.** Every module that reads run documents or events moves to the new tables in the same phase as the writers (item 19).
*   **Row shape during the transition (Phase 2).** A typed row (`execution`, `task`, `question`) keeps its typed columns and, in `data`, the record the coordinator API returns today (the legacy document). `executions.ts` and `tasks.ts` write both in one statement. The store serves the legacy kinds (`execution`, `delegation`, `pipeline-run`, `delivery`, `question`, `project-question`) from these tables, so every route answers exactly as before while new code reads the typed columns. A delegation lives on its child execution's row (`execution.delegation`); a pipeline run's steps are their own `pipeline_step` rows. Migration 6 moves the existing documents in insertion order and keeps their `seq`.
*   **Row-level security scope.** Every table has `FORCE ROW LEVEL SECURITY`. The coordinator sets `fleet.project_ids = '*'` on each connection (operator scope), and `''` in a list makes rows without a project visible. Narrow per-operation scopes (`SET LOCAL`) come with the agent-facing tools of Phase 6, the first callers that act for one project.
*   **Cut-over.** A one-time migration runs during a short planned stop. The SQLite file stays read-only as the rollback source. Rolling back means pointing the coordinator back at SQLite. Any work done after the cut-over is then re-exported by the reverse script, which maps `document` rows and the typed tables (`execution`, `task`, `task_dependency`, `question`, `artifact`) back to legacy documents.

### 8. Agent-facing tools for results and history

The shapes are copied from t3code (`apps/server/src/mcp/toolkits/attachment/tools.ts`, `agentContextBudget.json`; revision `d0f1f7200d`) and Orca (`ORCA_CHAT_SCRATCH_DIR`, `orca skills get`; revision `0b93cef4450`).

*   **Output folder, the default path.**
    *   Each execution gets `FLEET_OUTPUT_DIR`, a folder inside its working area.
    *   Anything the agent writes there is collected by the executor when the run ends, and in chunks during long runs. It is stored as artifacts of that execution.
    *   This extends the worktree output collection added in commit `23c5418`.
    *   The agent needs no tool call and no knowledge of storage.
*   **Explicit tools.** MCP tools on the existing `/api/tools` surface:
    *   `artifact_prepare_upload({name, media_type, size, sha256, required?})` returns `{artifact_id, upload: {url | parts[]}, expires_at}`. Use it for files larger than a single request, or to store a file now rather than at the end.
    *   `artifact_commit({artifact_id})` returns `{artifact_id, state}`. The coordinator checks size and hash on the store.
    *   `artifact_discard({artifact_id})`.
    *   `artifact_list({execution_id? | task_id?, media_type?})` returns metadata only.
    *   `artifact_get({artifact_id})` returns metadata plus a local path when the file is already on this host, or a short-lived URL otherwise. It never returns the file content inline.
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
    *   The access guard (`deploy/infrastructure/origin_guard.py`) still verifies the Access JWT, but passes the email to Fleet instead of matching one owner.
    *   Fleet maps the email to a `person` or a live `share`, and then applies the grants inside each operation. An email Fleet does not know gets 403 even if Cloudflare let it through.
*   **Access state and onboarding are Fleet work.**
    *   `access.status` lists people, grants, shares and expiry dates.
    *   It compares the `fleet-people` group with Fleet's list and reports every difference as a problem with an action: add, remove or ignore.
    *   `access.status` also lists the hand edits still needed, for example a person in `people.json` who is missing from the group.
*   **Agents.** An agent acts with its execution's permissions, never with a person's. Sharing with a new email is an operator action. An agent can only propose it as an `operator_decision` question.

### 11. Hosts, environments and stores

*   **One list, `hosts.list`.** For each registered host it returns:
    *   platform, last contact and observation state;
    *   environments and account users;
    *   harness versions and readiness;
    *   stores, with capacity, used and free bytes, and tunnel state;
    *   free disk;
    *   the running Fleet services, with their resource settings and current use.

    The data comes from the existing host observation plus `host_metric`. Nothing is entered by hand.
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

Phase 1 moved handlers written for synchronous SQLite onto Postgres. On SQLite one writer ran at a time, and every handler was one atomic, isolated step. The handlers rely on that implicitly. Many of them read several documents, decide, and write others: "no active execution in this session, so start the queued message"; "this receipt does not regress the command"; "this event sequence is not taken". Running them under SERIALIZABLE gave the right answers but failed constantly. Live PDS18 logged 3553 serialization failures in about 4 minutes. A one-table store makes SSI predicate locks coarse (kind ranges, shared index pages), so unrelated documents conflict. Retries piled up until executor requests passed the executor's 5 s HTTP timeout (`crates/executor/src/outbox.rs`).

**Model.** Two transaction kinds and two lock scopes, in `packages/coordinator/store.ts`:

*   **Read** (`store.read`): `READ ONLY, REPEATABLE READ`. One consistent snapshot. It never blocks writers and never fails to serialize. A write inside it fails with 25006, so a handler classified as read-only by mistake fails loudly instead of corrupting data.
*   **Write** (`store.tx` and `store.atomic`): `READ COMMITTED` inside a **scope lock** held for the whole transaction. Each statement sees the latest committed data. Writers in the same scope run one at a time, so a handler's read, decide and write is as atomic and isolated as it was on SQLite.
    *   **GLOBAL** is the default scope.
    *   **Execution scope** `execution:<id>` is for host ingest of one execution.
    *   Inside the process, a queue per key orders waiters before they take a pool connection, so waiters hold no connection. The transaction then takes `pg_advisory_xact_lock` on the key, which also orders writers in other processes (the migration tool, a second coordinator during a deploy).
    *   Lock order is GLOBAL, then an execution scope. An execution-scope writer never takes GLOBAL, so the two scopes cannot deadlock.
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
| Receipts `/api/hosts/receipts` | Command status never regresses. Native identity is set once. The execution state follows the command. `onReceipt`, `promote` and `advance` see the new state. | GLOBAL write. Account observations and native project tools are GLOBAL too; pipeline runs open their own short GLOBAL transactions. |
| Events `/api/hosts/events` | A batch is validated whole. An event id is immutable and a sequence number is used once per execution. A colliding frame becomes one transport conflict. | Execution scope. Only ingest and `transport_authorize` write `event`, `transport-conflict` and `transport-audit`; `transport_authorize` runs GLOBAL and also takes the execution scope. |
| Artifacts `/api/hosts/artifacts` | An artifact id never changes content. | Execution scope. Only this route writes `artifact`. |
| Agent tools `/api/agent-tools`, `/api/hosts/agent-tools` | Session invariants (`message_send` queue order, `session_fork`, `worker_done` accepted once, and so on). | Each tool call is its own GLOBAL write, or a read when the operation is in `READ_OPERATIONS`. |
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

**Whole-kind reads on write paths.**

*   `delegates.reconcile` now reads open delegations through a partial index (migration 5) instead of all delegations.
*   `delegates.ready` no longer scans every artifact when the parent has no source run.
*   Filters with `id`, `executionId` or `sessionId` use btree indexes (migration 4). Other equality filters use the GIN index.
*   Delivery `advance` still reads every delivery. It recomputes each delivery's state from its step executions before it decides to skip, so no stored field predicts which deliveries it touches. The kind holds one record per delivery request.

**Measured** on local PostgreSQL 17 with the live-volume fixture (`tests/coordinator/load.test.ts`: 4 pollers, two executor outboxes replaying 100 events and 10 receipts):

| Load | Before (main f22ea6b) | After (this model) |
|---|---|---|
| Live shape | 11–17 retries per run; max 153–198 ms; the owner-mac poller finished 1 poll | 0 retries; p95 19–22 ms; max 22–25 ms |
| Single-event rows, pollers without pause (150 requests) | 36–41 retries; max 168–265 ms | 0 retries; p95 12–13 ms; max 24–28 ms |
| 81 concurrent host requests (`concurrency.test.ts`) | 125–127 retries; 300 ms | 0 retries; 112–115 ms |

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

*   **ClickHouse returns duplicates before a merge.** Mitigation: every summing query uses `FINAL` or `argMax` by `seq` (§4); a test feeds a duplicate batch and checks totals.
*   **A cross-host transfer stops halfway.** Mitigation: 32 MiB multipart parts, each retried on its own, with the upload state kept on disk (§3).
*   **A store host is offline.** Mitigation: copies stay `pending` and resume; the artifact shows as unavailable, not lost.
*   **A deleted row removes bytes another row needs.** Mitigation: a store copy is deleted only when no live row needs it (§3).
*   **A secret reaches history.** Mitigation: two masking passes, and the forced purge for anything found later (§3, §5).
*   **The launch helper grants too much.** Mitigation: it starts only `fleet-launch@` instances for `fleet-*` users, and the isolation probe checks it (item 54).
*   **Migration breaks a reader.** Mitigation: readers move in the same phase as writers, and the reverse export allows rollback until Phase 7.

## Detailed Implementation Plan

Phases ship in order. Each phase leaves the system working and is verified live before the next phase starts.

*   **Phase 0. Infrastructure on PDS18:** the Postgres role, ClickHouse and Garage.
*   **Phase 1. Async data access and a `document` table on Postgres.** Behaviour stays the same, and the old data is migrated.
*   **Phase 2. Typed `execution`, `task` and `task_dependency` tables.**
*   **Phase 3. The artifact store, placement and the output folder.**
*   **Phase 4. Ingest to ClickHouse, harness adapters and redaction.**
*   **Phase 5. The isolation boundary.**
*   **Phase 6. Agent tools and query operations.**
*   **Phase 7. Documentation and retiring SQLite.**

Library choices. Each is the official or most widely used client, so there is one defensible answer:
*   `pg` (node-postgres) for Postgres;
*   `@clickhouse/client` for ClickHouse;
*   `@aws-sdk/client-s3` with `@aws-sdk/s3-request-presigner` for S3.

The executor uploads to presigned URLs with its existing `ureq` and `sha2` crates, so it needs no S3 SDK.

Unit tests use `@electric-sql/pglite`, an embedded Postgres that needs no server. Integration tests use the real PostgreSQL 18 cluster.

### Target Files to Create/Modify

#### Phase 0: infrastructure

##### 1. `deploy/infrastructure/fleet-clickhouse.container` (Create)

*   **Type / Scope**: Podman Quadlet unit for the `fleet` user.
*   **Proposed Changes**:
    *   Image pinned by digest.
    *   Listens on loopback port 9480 (HTTP) only.
    *   Data under `/home/fleet/data/clickhouse`.
    *   Memory and CPU set through the user unit, as an operator setting.
*   **Why**: FD3 and FD6. It follows tight-D5: rootless Podman Quadlet, no Docker.

##### 2. `deploy/infrastructure/fleet-garage.container` and `deploy/infrastructure/garage.toml.candidate` (Create)

*   **Type / Scope**: Podman Quadlet unit and Garage configuration.
*   **Proposed Changes**:
    *   A single-node Garage with `replication_factor = 1`.
    *   The S3 API on loopback port 9490, and the admin API on loopback.
    *   Data under `/home/fleet/data/garage`.
    *   One bucket `fleet`.
    *   An ingress hostname for the store in PDS18's existing `cloudflared` configuration, behind a Cloudflare Access policy that admits the operator and a service token for executors and the coordinator.
*   **Why**: FD2. PDS18's own store.

##### 3. `deploy/infrastructure/fleet-postgres.sql` (Create)

*   **Type / Scope**: SQL run once by root through the existing route.
*   **Proposed Changes**:
    *   Creates role `fleet` (no superuser, no `BYPASSRLS`) and database `fleet`, owned by `fleet`. `fleet` owns the tables and runs migrations.
    *   Creates role `fleet_app` (`LOGIN`, no superuser, no `BYPASSRLS`, not an owner) with `SELECT`, `INSERT`, `UPDATE` and `DELETE` on the tables. The coordinator connects as `fleet_app`.
    *   Peer authentication stays on the existing local socket, with a map from the Unix user `fleet` to both roles.
*   **Why**: FD1 and tight-D6, one role and one database per service.

##### 4. `deploy/infrastructure/fleet-backup.sh` and `fleet-backup.timer` (Create)

*   **Type / Scope**: User timer script.
*   **Proposed Changes**:
    *   A nightly ClickHouse `BACKUP` to a local disk target, and a Garage bucket mirror, both written to `/backup/fleet/YYYY-MM-DD/`.
    *   Keeps the same 5 days as tight-D6.
    *   No service stops.
*   **Why**: Durability. The existing `pg_dump` already covers Postgres.

##### 5. `deploy/infrastructure/fleet.service.candidate` and `deploy/infrastructure/coordinator.env` (Modify)

*   **Type / Scope**: Coordinator user unit and the environment file it loads. `deploy/infrastructure/fleet.service.candidate` is the unit in use; `deploy/fleet.service.candidate` is an older copy and is deleted.
*   **Proposed Changes**:
    *   In the unit, add `After=` and `Wants=` for the ClickHouse and Garage units.
    *   In `coordinator.env`, add `FLEET_DATABASE_URL` (Unix socket, peer authentication, role `fleet_app`), `FLEET_CLICKHOUSE_URL` and `FLEET_STORES_FILE`.
    *   `FLEET_MAX_ARTIFACT_BYTES` is removed in Phase 3 (item 26), when the base64 path goes.
*   **Why**: The coordinator needs the new stores.

##### 6. `deploy/hosts/macos/fleet-garage.plist`, `deploy/hosts/windows/fleet-garage.md` (Create)

*   **Type / Scope**: Store on the other hosts.
*   **Proposed Changes**:
    *   macOS: a launchd agent running Garage single-node under the executor service user, listening on loopback.
    *   Windows: an install note that stays open until Q2 is answered (WSL2 or a native S3 server).
    *   Each host's store gets an ingress hostname in that host's `cloudflared` configuration, behind the same Cloudflare Access policy as item 2.
*   **Why**: FD2, local stores.

##### 7. `docs/infrastructure-build.md` (Modify)

*   **Type / Scope**: Documentation.
*   **Proposed Changes**: Record the Phase 0 install and readback steps, following the existing style: SHA256 or digest verification, readback of listeners and units.
*   **Why**: The install must be repeatable.

##### 8. `~/Projects/tight/tight.md` (Modify, separate repository)

*   **Type / Scope**: Infrastructure decisions.
*   **Proposed Changes**:
    *   Add `fleet` to the tight-D4 user table.
    *   Note the Fleet ClickHouse and Garage under tight-D5 and tight-D7.
    *   Replace "Do not create a generic S3 service" with a scoped rule: Fleet's per-host store is allowed as a Podman Quadlet of `fleet` (Q8).
    *   Add the tight-D4 exception for `fleet-launch@.service` and its polkit rule (Q8).
*   **Why**: tight is the single source for host decisions (tight-D13).

#### Phase 1: async data access on Postgres

##### 9. `package.json` (Modify)

*   **Type / Scope**: Dependencies.
*   **Proposed Changes**:
    *   Add `pg`, `@types/pg`, `@clickhouse/client`, `@aws-sdk/client-s3`, `@aws-sdk/s3-request-presigner`, and `@electric-sql/pglite` (dev).
    *   Keep `better-sqlite3` and `@types/better-sqlite3` until Phase 7, for the migration tool only.
*   **Why**: The new stores.

##### 10. `packages/coordinator/db/schema.sql` (Create)

*   **Type / Scope**: Postgres schema with numbered migrations.
*   **Proposed Changes**:
    *   `schema_migration(version, applied_at)`.
    *   `document(kind text, id text primary key, data jsonb, project_id text null, seq bigserial, updated_at)`, holding every legacy kind. `seq` keeps insertion order: `forward` fills it from SQLite `rowid`, and `all()` orders by it, so callers that take `.at(-1)` as the latest record keep working.
    *   Phase 2 and Phase 3 tables are added by later migrations in the same file.
*   **Why**: G7. Phase 1 moves storage without changing the model.

##### 11. `packages/coordinator/store.ts` (Create)

*   **Type / Scope**: TypeScript data-access module.
*   **Proposed Changes**:
    *   `createStore(url)` returns `{get(id, kind), put(kind, doc), all(kind, filter?), tx(fn), query(sql, params)}`.
    *   All of them are async. `tx` runs `fn` inside one Postgres transaction at `SERIALIZABLE` isolation and retries on serialization failure.
    *   `fn` must be safe to run again: IDs (`randomUUID()`), file reads and calls to other operations happen before `tx`, and their results are passed in. Async operations such as `delivery_create`, which calls `question_ask`, run their steps as separate transactions.
    *   `put` keeps today's behaviour: writing an id that already exists under a different kind does nothing.
    *   It replaces the synchronous `get`, `put` and `all` helpers defined in `app.ts`.
*   **Why**: FD1 requires async access. A single module keeps the change in one place.

##### 12. `packages/coordinator/app.ts` (Modify)

*   **Type / Scope**: Coordinator HTTP application.
*   **Proposed Changes**:
    *   Remove `better-sqlite3` and the `records` table creation at line 19.
    *   Accept a `store` from `createStore`.
    *   Make every handler `await` the store.
    *   Turn `db.transaction(() => …)` blocks, such as `/api/hosts/poll` at line 114, into `store.tx(async t => …)`.
    *   Behaviour and routes stay the same.
*   **Why**: Phase 1.

##### 13. `packages/coordinator/accounts.ts`, `delegation.ts`, `delivery.ts`, `lab.ts`, `projects.ts`, `recovery.ts`, `agent-tools.ts`, `account-ui.ts`, `project-ui.ts`, `lab-ui.ts`, `ui.ts` (Modify)

*   **Type / Scope**: Coordinator modules.
*   **Proposed Changes**: Convert every store call to async through `store.ts`. No change in behaviour.
*   **Why**: Phase 1. Every module calls `get`, `put` or `all`.

##### 14. `packages/coordinator/server.ts` (Modify)

*   **Type / Scope**: Entry point.
*   **Proposed Changes**:
    *   Read `FLEET_DATABASE_URL`, `FLEET_CLICKHOUSE_URL` and `FLEET_STORES_FILE`.
    *   Drop `FLEET_MAX_ARTIFACT_BYTES` in Phase 3. Keep `FLEET_DATABASE` (the SQLite path) for `migrate-sqlite` until Phase 7.
    *   Run the schema migrations on start.
*   **Why**: Configuration for the new stores.

##### 15. `packages/coordinator/migrate-sqlite.ts` (Create)

*   **Type / Scope**: One-time migration tool.
*   **Proposed Changes**:
    *   `forward`: read every `records` row and insert it into `document`, keeping kind, id and data.
    *   `verify`: compare row counts and data hashes per kind.
    *   `reverse`: export `document` rows changed after the cut-over back into a copy of the SQLite file.
    *   Phase 2 extends `forward` and `reverse` with the typed mapping in Technical Plan §7, in both directions. Phase 3 extends them with extraction of base64 artifacts.
*   **Why**: G7, migration without loss and with a rollback path.

#### Phase 2: executions and tasks

##### 16. `packages/coordinator/db/schema.sql` (Modify)

*   **Type / Scope**: Migration 2.
*   **Proposed Changes**:
    *   `execution`:
        *   `id`, `kind` (enum), `state` (enum);
        *   `observation` (`confirmed` or `unconfirmed`), `last_seen_at`;
        *   `project_id`, `task_id`, `experiment_id`, `variant`, `parent_execution_id`, `session_id`, `account_id`, `host_id`, `environment_id`;
        *   `harness`, `harness_version`, `model`, `config_ref jsonb`;
        *   `problem text null`, `started_at`, `ended_at`;
        *   `legacy_kind`, `legacy_id`, `data jsonb`.
    *   `execution_totals`: `execution_id`; `tokens_input`, `tokens_output`, `tokens_cache_read`, `tokens_cache_write`; `rate_limit_last jsonb`; `connection_cost jsonb` (amount per connection and currency); `wall_ms`, `active_ms`, `cpu_ms`, `peak_memory_bytes`; each with a `_basis` column; plus `history_incomplete integer` and `computed_at`.
    *   `task`: the columns listed in Technical Plan §2, plus `delivery_method jsonb` and `legacy_id`.
    *   `task_dependency`.
    *   `question`: the columns listed in Technical Plan §2, with indexes on `task_id` and `execution_id`.
    *   View `task_view`: per task, the count of executions by state, the sum of `execution_totals` over distinct executions, the latest `stored` artifact, and open questions.
    *   Indexes on (`project_id`, `state`, `started_at`), `task_id`, `experiment_id`, `parent_execution_id` and `account_id`.
    *   Row-level security on every table, with `FORCE ROW LEVEL SECURITY`. Policy: `project_id = ANY (string_to_array(current_setting('fleet.project_ids', true), ','))`. When the setting is unset the expression is null and no row is visible. Each operation sets `fleet.project_ids` with `SET LOCAL`.
*   **Why**: G1, G2 and G6.

##### 17. `packages/coordinator/executions.ts` (Create)

*   **Type / Scope**: Execution model.
*   **Proposed Changes**:
    *   `createExecution`, `transition(id, state)`, and `markObservation`.
    *   `detectProblems()` marks executions bound to an unregistered host, account or environment, filling `problem`, as with `6b00f0fb`.
    *   `writeTotals(id)` reads ClickHouse after Phase 4. Before that it writes wall-clock time from `started_at` and `ended_at` with basis `measured`, and every other measure with basis `unavailable`. It runs when an execution ends, and again when ingest receives events for an execution that has already ended.
    *   The legacy state mapping from Technical Plan §1.
*   **Why**: G1.

##### 18. `packages/coordinator/tasks.ts` (Create)

*   **Type / Scope**: Task operations.
*   **Proposed Changes**:
    *   `tasks.create`, `tasks.update`, `tasks.get` and `tasks.list`.
    *   `tasks.get` returns the derived progress, cost and latest result from `task_view`.
    *   The state transitions from Technical Plan §2, and nowhere else.
    *   `questions.ask` and `questions.answer` on the `question` table. Answering an `acceptance` question records who accepted and the artifact IDs, and moves the task.
*   **Why**: G2.

##### 19. `packages/coordinator/delegation.ts`, `delivery.ts`, `lab.ts`, `projects.ts`, `agent-tools.ts`, `recovery.ts`, `ui.ts`, `lab-ui.ts`, `account-ui.ts`, `app.ts` (Modify)

*   **Type / Scope**: Writers and readers of run records and questions.
*   **Proposed Changes**:
    *   Writers: write `execution` rows through `executions.ts` instead of the `execution`, `delegation` and `pipeline-run` documents, with kinds `agent_run`, `pipeline_run`, `pipeline_step` and `child_agent` (from the child-agent recording added in commit `d3802cc`).
    *   `delivery.ts`: `delivery_create` creates a `task` through `tasks.ts`, and each step creates an execution with that `task_id`. `reconcile`, `delivery_get` and `delivery_history` read the task, its executions and `task_view` instead of `all('delivery')`.
    *   Questions: `projects.ts` (`project-question`, lines 65-67), `delivery.ts` (lines 21 and 25) and `agent-tools.ts` (`question`, lines 128-136, read at 169, 172 and 197) use `questions.ask` and `questions.answer`.
    *   Readers: `agent-tools.ts` (lines 169 and 172), `recovery.ts`, `ui.ts`, `lab-ui.ts`, `account-ui.ts` and the routes in `app.ts` read executions through `executions.ts` instead of `all('execution')`.
    *   Lab trials set `experiment_id` and `variant`.
    *   Handoff and fork set `parent_execution_id`.
*   **Why**: G1, with one record shape.

##### 20. `packages/coordinator/project-ui.ts` (Modify)

*   **Type / Scope**: Operator view.
*   **Proposed Changes**: A task list and a task detail page that read `tasks.list` and `tasks.get`. Loading new data does not move the scroll position. The page shows a block for each open question, and answers it by text or by option through `questions.answer`. Acceptance questions show the result artifacts with accept and reject buttons.
*   **Why**: G2 and the owner's view requirements. It has only these two pages.

#### Phase 3: artifacts and placement

##### 21. `packages/coordinator/db/schema.sql` (Modify)

*   **Type / Scope**: Migration 3.
*   **Proposed Changes**: The `store`, `artifact` and `artifact_copy` tables from Technical Plan §3, with indexes on `content_hash`, `execution_id`, `task_id` and (`store_id`, `state`).
*   **Why**: G3 and FD2.

##### 22. `config/stores.example.json` (Create)

*   **Type / Scope**: Store registry example.
*   **Proposed Changes**: One entry per host: `{id, host_id, endpoint, bucket, credentials_ref}`. Real values stay in the host-only file named by `FLEET_STORES_FILE`.
*   **Why**: Keeps credentials out of the repository.

##### 23. `config/projects/*/project.json` and `packages/coordinator/projects.ts` (Modify)

*   **Type / Scope**: Project settings.
*   **Proposed Changes**:
    *   Add an optional `artifact_locations: string[]` (host IDs), with optional `lab` and `production` overrides. The default is `["pds18"]`.
    *   Add an optional `artifact_rules` list (`scope`, `older_than_days`, `action: move|delete`, `to?`). It is empty by default.
    *   `projects.ts` checks that each host has a registered store.
*   **Why**: Placement per project.

##### 24. `packages/coordinator/artifacts.ts` (Create)

*   **Type / Scope**: Artifact operations.
*   **Proposed Changes**:
    *   The S3 client sets `requestChecksumCalculation: 'WHEN_REQUIRED'` (see Q1). Single-part uploads are presigned with `x-amz-checksum-sha256`, so the store itself rejects a wrong hash.
    *   `reserve({execution_id, name, media_type, size, sha256, required})` picks the store: the writer's host if it is in the list, otherwise the first host in the list whose store is `reachable`. It starts a multipart upload and returns presigned URLs for 32 MiB parts.
    *   `commit(id)` completes the multipart upload, checks the object's size and hash on the store with `HEAD` plus a stored checksum, then marks the artifact `stored` and its first `artifact_copy` `verified`.
    *   `purge(content_hash, reason)`: the forced purge from Technical Plan §3, operator only.
    *   `discard(id)`, `list(filter)` and `get(id)`. `get` returns metadata and a presigned GET for a verified copy, preferring the reader's host.
    *   The success rule: `executions.transition(id, 'succeeded')` refuses while any `required` artifact is not `stored`.
*   **Why**: G3.

##### 25. `packages/coordinator/placement.ts` (Create)

*   **Type / Scope**: Placement worker and audit.
*   **Proposed Changes**:
    *   `runPlacement()` repeats every 60 seconds (an operator setting). A store is `reachable` when its host's last observation is under 2 minutes old and reports the store reachable. For each artifact missing a verified copy at a reachable listed location, it queues a `copy` command to the target host's executor, then verifies the hash.
    *   `runAudit()` re-hashes 100 copies per store every day (an operator setting) and marks missing or corrupt copies `failed`, which schedules a new copy.
    *   Removing a location deletes copies there only after the remaining locations hold verified copies.
    *   A store copy is deleted only when no live `artifact` row with that `content_hash` in the project needs that store.
*   **Why**: FD2. The audit detects a false `placed` state.
*   **Retention rules**: `previewRule(projectId, rule)` returns the count, the bytes and a sample of affected artifacts. `confirmRule` stores the confirmation. `runRules()` applies only confirmed rules, through the same copy-then-delete path.

##### 26. `packages/coordinator/app.ts` (Modify)

*   **Type / Scope**: Routes.
*   **Proposed Changes**:
    *   Replace `POST /api/hosts/artifacts` (base64 at line 118) with `POST /api/hosts/artifacts/reserve` and `POST /api/hosts/artifacts/commit`.
    *   Remove `maxArtifactBytes`, the body limit derived from it, and `FLEET_MAX_ARTIFACT_BYTES`.
    *   `GET /api/results/:id` redirects to a presigned GET.

##### 26b. `packages/coordinator/delivery.ts` (Modify)

*   **Type / Scope**: Delivery result reading.
*   **Proposed Changes**: `reconcile` (line 11) reads `delivery-result.json` through `artifacts.get` instead of decoding `contentBase64`. The base64 documents are removed only after this change ships.
*   **Why**: Phase 3 must not break delivery.
*   **Why**: G3, no size limit inside Fleet.

##### 27. `crates/executor/src/upload.rs` (Create)

*   **Type / Scope**: Rust module.
*   **Proposed Changes**:
    *   Hash a file with streaming SHA256.
    *   Call reserve, then PUT each 32 MiB part to its presigned URL on the store's tunnel hostname with `ureq`, then commit.
    *   Retry each part on its own, with backoff.
    *   Keep the pending upload (upload ID and finished parts) in the existing `journal.sqlite`, so it resumes after an executor restart.
    *   Send the Cloudflare Access service token headers on every store request.
    *   It also serves the `copy` command from `placement.ts`: it downloads from the source presigned GET and uploads to its local store in the same parts.
*   **Why**: G3 and FD4. Files go directly to the store.

##### 28. `crates/executor/src/main.rs` (Modify)

*   **Type / Scope**: Executor core.
*   **Proposed Changes**:
    *   Replace the base64 artifact queue at line 812 with `upload.rs`.
    *   Remove the executor's own `FLEET_MAX_ARTIFACT_BYTES` check (lines 792-810, 16 MiB default).
    *   Create `FLEET_OUTPUT_DIR` inside each execution's working area, and collect it when the run ends and in chunks during long runs, extending `23c5418`.
    *   Upload the redacted raw transcript as an artifact.
    *   Remove `use base64`.
*   **Why**: G3, G4 and G9.

##### 28b. `crates/executor/src/preview.rs` (Create)

*   **Type / Scope**: Rust module.
*   **Proposed Changes**:
    *   `build_previews(artifact)` runs on image and video media types after commit:
        *   it calls `ffmpeg` at the lowest CPU priority;
        *   it writes the thumbnail or poster into a temporary folder;
        *   it uploads each one through `upload.rs` with `derived_from_artifact_id` and `role`.
    *   Readiness reports whether `ffmpeg` is present and its version.
    *   A failure is logged as a preview error. It never fails the execution.
*   **Why**: Basic previews (owner decision). There is no new crate dependency, because `ffmpeg` is an external program on the host.

##### 29. `crates/executor/Cargo.toml` (Modify)

*   **Type / Scope**: Dependencies.
*   **Proposed Changes**: Remove `base64` if nothing else uses it.
*   **Why**: Dead dependency.

#### Phase 4: ingest, adapters, redaction

##### 30. `packages/coordinator/db/clickhouse.sql` (Create)

*   **Type / Scope**: ClickHouse schema.
*   **Proposed Changes**:
    *   Tables `event`, `span`, `measurement` and `host_metric`, as in Technical Plan §4.
    *   Each table carries the link columns, uses `ReplacingMergeTree`, is partitioned by month, and has `ORDER BY (project_id, execution_id, seq)`. `ts` is a plain column.
    *   No TTL.
*   **Why**: G4 and G5.

##### 31. `packages/coordinator/ingest.ts` (Create)

*   **Type / Scope**: Outbox ingest.
*   **Proposed Changes**:
    *   `ingestBatch(hostId, rows)` splits each batch: state changes go to Postgres in one transaction, telemetry rows go to ClickHouse in batches.
    *   It copies the link columns from the execution row.
    *   It runs the second-pass redaction scan.
    *   It counts rows that a 4xx sets aside on the execution (`execution_totals.history_incomplete`).
    *   When rows arrive for an execution that has already ended, it calls `writeTotals` again.
    *   It replaces the event handling at `/api/hosts/events`, which today is `transportRecovery.ingest` in `recovery.ts` (`app.ts:116-117`).
*   **Why**: FD4, with nothing written around Fleet's own layer.

##### 32. `packages/coordinator/redaction.ts` (Create)

*   **Type / Scope**: Pattern redaction.
*   **Proposed Changes**:
    *   Patterns for common token formats: bearer tokens, `sk-` keys, GitHub tokens, private key blocks, AWS keys.
    *   A match is replaced and logged as a `redaction_miss` event with the pattern name only.
*   **Why**: G4, the second pass. The match is masked before the row is stored.

##### 33. `packages/coordinator/accounts.ts` (Modify)

*   **Type / Scope**: Accounts.
*   **Proposed Changes**: Replace `quota:'unknown'` at line 18 with the latest `rate_limit` measurement for the account, with its basis, or `unavailable`.
*   **Why**: G5. The signal is recorded; acting on it is out of scope.

##### 34. `crates/executor/src/outbox.rs` (Modify)

*   **Type / Scope**: Outbox.
*   **Proposed Changes**:
    *   New row classes: `model_usage`, `tool_call`, `rate_limit`, `child_agent`, `error`, `harness_version` and `host_metric`.
    *   Batching and `seq` stay as they are.
    *   Rows rejected with a 4xx are counted and reported, not only set aside.
*   **Why**: FD4 and G5.

##### 35. `crates/executor/src/harness.rs`, `codex.rs`, `antigravity.rs`, `native.rs` (Modify)

*   **Type / Scope**: Harness adapters.
*   **Proposed Changes**:
    *   Each adapter maps its native stream to the normalized events from Technical Plan §5.
    *   Codex: forward `rate_limits` from rollout and app-server events instead of dropping them (Q5).
    *   Claude: forward `stream-json` usage and rate-limit events (Q5).
    *   Record `harness_version` at the start of every run.
    *   Parsing follows t3code's `CodexAdapterV2.ts` and `ClaudeAdapterV2.ts` (revision `d0f1f7200d`).
*   **Why**: G4 and G5, Fleet's own harness layer.

##### 36. `crates/executor/src/redact.rs` (Create)

*   **Type / Scope**: Exact-value redaction.
*   **Proposed Changes**:
    *   A per-execution set of injected secret values: connection credentials, key material, provider tokens, the machine token.
    *   `redact(&str) -> String` replaces each value with `«redacted:<name>»` before an outbox write or an upload.
*   **Why**: G4, the first pass.

##### 37. `crates/executor/src/main.rs` (Modify)

*   **Type / Scope**: Resource and host reporting.
*   **Proposed Changes**:
    *   `resources()` at line 935 reports CPU and memory for the whole process tree, by pid-tree sampling, with basis `estimated`. On Linux it reads the execution's cgroup instead, with basis `measured`, after Phase 5.
    *   Add periodic `host_metric` rows: disk, load, harness versions and store reachability.
*   **Why**: G5 and the host telemetry the owner asked for.

#### Phase 5: isolation boundary

##### 38. `crates/executor/src/isolation.rs` (Create)

*   **Type / Scope**: Rust module.
*   **Proposed Changes**:
    *   `launch_as(account_user, cmd)` per OS:
        *   macOS: `launchctl asuser`, or `sudo -u` through one narrow sudoers rule that allows only `fleet-*` users;
        *   Linux: `systemctl start fleet-launch@<user>-<execution>.service` through the polkit rule from item 41;
        *   Windows: `CreateProcessWithLogonW`, active when the Windows executor exists.
    *   `issue_keys(execution)` writes the needed key to `<working area>/.fleet/keys/` with mode 0600, owned by the account user, or starts a per-execution `ssh-agent` socket. It deletes the key when the run ends.
    *   `cgroup_usage(scope)` on Linux.
*   **Why**: G8 and FD5.

##### 39. `crates/executor/src/git_access.rs` (Modify)

*   **Type / Scope**: Git credentials.
*   **Proposed Changes**: `GIT_SSH_COMMAND` (line 37) points at the per-execution key or `ssh-agent` socket from `isolation.rs`, never at the shared key path under `~/.config/fleet/connections`.
*   **Why**: G8. Today the shared key path is visible to the agent.

##### 40. `crates/executor/src/worktree.rs` (Modify)

*   **Type / Scope**: Working areas.
*   **Proposed Changes**:
    *   The working area is owned by the account user.
    *   The shared `.git` (config, hooks, objects and refs) is read-only to account users, and writes go through the delivery path. The `writableRoots` grant of the whole `.git` under `gitNetwork` in `main.rs` is removed.
    *   If a harness must write, the write is limited to `.git/worktrees/<session>`, and never reaches `config` or `hooks`.
    *   Every git command that the executor or delivery runs in a checkout uses `-c core.hooksPath=/dev/null -c core.fsmonitor=` and `GIT_CONFIG_NOSYSTEM=1`, so a file an agent wrote cannot run code on the host.
    *   `gitNetwork` becomes opt-in per profile, and is off by default. When it is on, sandbox network access is limited to the git remote's host instead of full network access.
*   **Why**: G8.

##### 41. `deploy/isolation/create-account-user.sh` and `create-account-user.ps1` (Create)

*   **Type / Scope**: Provisioning scripts run by root or an admin.
*   **Proposed Changes**:
    *   Create `fleet-<harness>-<n>` with a private `HOME`, no admin groups, and on Linux linger enabled.
    *   On macOS, document the one-time native login per user and the locked-Keychain file fallback.
    *   Add the executor's narrow launch rule. On Linux: install the system unit template `/etc/systemd/system/fleet-launch@.service` (runs the command for the execution as the instance's user, in its own scope) and a polkit rule in `/etc/polkit-1/rules.d/` that lets the `fleet` user start and stop only `fleet-launch@` units whose user is a `fleet-*` user. On macOS: the sudoers rule from item 38.
*   **Why**: FD5. The owner runs these once per account.

##### 42. `docs/layered-isolation-model.md` and `docs/multi-account-same-env.md` (Modify)

*   **Type / Scope**: Documentation.
*   **Proposed Changes**: State the FD5 boundary, the accepted gaps, the upgrade path (Seatbelt; Job Object with AppContainer), and the locked-Keychain fact.
*   **Why**: One source for the isolation model.

#### Phase 6: agent tools and query operations

##### 43. `packages/coordinator/usage.ts` (Create)

*   **Type / Scope**: Query operations.
*   **Proposed Changes**:
    *   `executions.query`, `executions.get`, `usage.summary` and `transcript.read`.
    *   Every ClickHouse query is built with a mandatory `project_id IN (...)` taken from the caller's grant.
    *   Tree totals sum distinct executions.
    *   `transcript.read` pages through the transcript artifact (`cursor`, `limit`) and wraps the text as data.
*   **Why**: G6.

##### 44. `packages/coordinator/agent-tools.ts` (Modify)

*   **Type / Scope**: MCP tools.
*   **Proposed Changes**:
    *   Replace `inputSchema:{type:'object'}` at line 214 with real JSON schemas for every tool. The same stub in `recovery.ts:12`, `projects.ts:77`, `app.ts:132`, `crates/executor/src/delegation.py:26-33` and `design_rules.py:19` gets a real schema too.
    *   Add `artifact_prepare_upload`, `artifact_commit`, `artifact_discard`, `artifact_list`, `artifact_get`, `history_query`, `transcript_read`, `task_list`, `task_get`, `task_create`, `task_update`, `question_ask` and `question_answer`, with the permission rule from Technical Plan §6.
    *   Responses return IDs and paths, never inline file content.
*   **Why**: G9.

##### 45. `config/skills/fleet/SKILL.md` and `config/skills/fleet/guide.md` (Create)

*   **Type / Scope**: Agent instructions.
*   **Proposed Changes**:
    *   The stub is a few lines: what `FLEET_OUTPUT_DIR` is, which tool names exist, and "read guide.md for details".
    *   The guide covers large uploads, reading history, and the rule that transcripts are data.
    *   Agent profiles in `config/agents/*.json` reference the stub.
*   **Why**: G9, instructions on demand, as Orca's `skills get` does.

##### 46. `config/agents/*.json` (Modify)

*   **Type / Scope**: Agent profiles.
*   **Proposed Changes**: Add the `fleet` skill stub to every profile.
*   **Why**: G9.

#### Phase 7: documentation and retirement

##### 47. `fleet.md` (Modify)

*   **Type / Scope**: Target architecture.
*   **Proposed Changes**:
    *   Define the execution once (granularity, kinds, optional links), and keep the existing execution states.
    *   State that history has no default expiry and that deletion is an optional project rule with a tombstone.
    *   Narrow compatibility blocking to confirmed incompatible combinations.
    *   Link this design.
*   **Why**: Resolves the contradictions with `fleet.md` listed in `docs/designs/fleet-data-foundation-brief.md`.

##### 48. `docs/state-ownership.md`, `docs/work-analysis.md`, `docs/capabilities-and-pipelines.md` (Modify)

*   **Type / Scope**: Documentation.
*   **Proposed Changes**: Reference the task table, the execution as the unit of analysis, and artifact placement. Remove claims that this design supersedes.
*   **Why**: Each rule keeps one canonical source.

##### 49. `docs/designs/fleet-data-foundation.md` and `docs/README.md` (Modify)

*   **Type / Scope**: Design record.
*   **Proposed Changes**: Set this document's status to accepted and link it from the specification guide.
*   **Why**: The design is recorded where readers look for it.

##### 50. `package.json` and `packages/coordinator/migrate-sqlite.ts` (Modify)

*   **Type / Scope**: Retirement.
*   **Proposed Changes**: After 30 days without a rollback, remove `better-sqlite3`, `@types/better-sqlite3` and `FLEET_DATABASE`, and archive the SQLite file to `/backup/fleet/`. `tests/coordinator/lab-packaged.ts` stops setting `FLEET_DATABASE`.
*   **Why**: G7. The old store is retired.

#### Tests

##### 51. `tests/coordinator/store.test.ts`, `executions.test.ts`, `tasks.test.ts`, `artifacts.test.ts`, `placement.test.ts`, `ingest.test.ts`, `usage.test.ts`, `migrate.test.ts` (Create)

*   **Type / Scope**: Unit tests on PGlite, with a fake S3 and a fake ClickHouse client.
*   **Proposed Changes**:
    *   **store:** transaction retry; `all()` keeps insertion order; `put` under a different kind does nothing; row-level security returns no rows when `fleet.project_ids` is unset.
    *   **executions:** legacy state mapping; detection of an unregistered host; observation `unconfirmed`; totals are recomputed for late events.
    *   **tasks:** derived progress and cost; dependencies; every state transition in Technical Plan §2; `completed` only after an accepted `acceptance` question; agent permission rule.
    *   **questions:** both legacy kinds migrate with `task_id`; one answer path for operators and agents.
    *   **artifacts:** reserve and commit; hash mismatch is refused; a required artifact blocks `succeeded`; a copy shared by two rows survives deleting one row; purge removes the object from every store.
    *   **placement:** an `[windows]` project is never copied elsewhere; a `[windows, mac]` project reaches `placed`; a store offline leaves copies `pending`; removing a location is safe; the audit detects a missing copy; an unconfirmed rule never runs; a delete rule leaves tombstones and removes nothing while the target lacks verified copies.
    *   **ingest:** a duplicate `seq` is counted once in summed queries; the 4xx count is reported; the redaction scan masks a match.
    *   **usage:** no double counting in tree totals; the project filter is always present.
    *   **migrate:** forward, verify and reverse round-trip, including the typed tables; a legacy delivery becomes a task with step executions; legacy readers in item 19 return the same data.

##### 52. `tests/coordinator/*.test.ts` (Modify)

*   **Type / Scope**: Existing coordinator tests.
*   **Proposed Changes**: Run them against `createStore` on PGlite instead of SQLite, with async calls. Expected behaviour is unchanged.

##### 53. `tests/coordinator/agent-tools.test.ts` (Modify)

*   **Type / Scope**: Tool tests.
*   **Proposed Changes**: Every tool has a non-empty schema. The new artifact and history tools are covered. A byte budget is set on the `tools/list` JSON and on the `fleet` skill stub, starting from measured size plus 10%, as t3code's `AgentContextBudget.test.ts` does.

##### 54. `tests/executor/upload_protocol.py`, `redaction.py`, `isolation_probe.py` (Create), and Rust unit tests in `upload.rs`, `redact.rs`, `isolation.rs`

*   **Type / Scope**: Executor tests.
*   **Proposed Changes**:
    *   **upload:** a multipart upload resumes after an executor restart; a failed part is retried alone; a file over 16 MiB is accepted.
    *   **redaction:** every injected secret is removed from the outbox rows and from the uploaded transcript.
    *   **preview:** a PNG and an MP4 fixture produce a thumbnail and a poster linked by `derived_from_artifact_id`; without `ffmpeg`, the result is `unavailable` and the execution still succeeds.
    *   **isolation:** an agent cannot read another account's `HOME`, the shared key directory, or the machine token; a hook or `core.fsmonitor` that an agent planted in a checkout does not run during host-side git; the key file is gone after the run; the Linux cgroup reports usage; the `fleet` user cannot start `fleet-launch@` for a user outside `fleet-*`.

#### People and access (with Phase 6)

##### 55. `packages/coordinator/db/schema.sql` and `packages/coordinator/people.ts` (Modify / Create)

*   **Type / Scope**: Schema migration and TypeScript operations.
*   **Proposed Changes**:
    *   Add the `person`, `person_grant` and `share` tables from Technical Plan §10.
    *   Load `person` and `person_grant` from the host-only `people.json`, so that no email is stored in the repository.
    *   Add the operations `shares.create`, `shares.revoke` and `access.status`. People have no operations: their file is edited by hand.
*   **Why**: G10.

##### 56. `packages/coordinator/access-sync.ts` (Create)

*   **Type / Scope**: Cloudflare Access group sync.
*   **Proposed Changes**:
    *   Add or remove a share's email in the `fleet-people` group through the Cloudflare API. A periodic reconcile reports every difference between the group and Fleet (people plus live shares) to `access.status`.
    *   The token comes from the coordinator's connection file.
*   **Why**: Cloudflare stays the outer wall, and Fleet's list stays the source of truth.

##### 57. `deploy/infrastructure/origin_guard.py` and `test_origin_guard.py` (Modify)

*   **Type / Scope**: Access guard.
*   **Proposed Changes**: Replace the single-owner email match with JWT verification only, and forward the verified email to the coordinator in an internal header. Tests cover a forged header, a missing JWT and an unknown email.
*   **Why**: Fleet authorizes by its own tables.

##### 58. `tests/coordinator/people.test.ts` (Create)

*   **Type / Scope**: Unit tests.
*   **Proposed Changes**: These tests cover:
    *   An unknown email gets 403.
    *   An artifact share grants that artifact only.
    *   An expired share removes the email from the group when no other share remains.
    *   A drift between the group and the table appears in `access.status`.

#### Hosts, environments and stores (with Phase 3)

##### 59. `packages/coordinator/hosts.ts` and `packages/coordinator/db/schema.sql` (Create / Modify)

*   **Type / Scope**: Operations and migration.
*   **Proposed Changes**: Add table `host_setting`, the operations `hosts.list`, `hosts.set_setting` and `hosts.get`, and the problem rule for low free space. Add the matching read-only MCP tools `hosts_list` and `hosts_get` to `agent-tools.ts`.
*   **Why**: G11.

##### 60. `crates/executor/src/settings.rs` (Create)

*   **Type / Scope**: Rust module.
*   **Proposed Changes**:
    *   Receive pending settings in the poll response.
    *   Apply `store.capacity_bytes` through the Garage admin API (layout assign, then apply).
    *   Apply service bounds with `systemctl --user set-property` on Linux, or by updating the launchd plist and reloading it on macOS.
    *   Report `applied_at` or the exact error.
    *   Report store capacity, used bytes and free disk in `host_metric`.
*   **Why**: G11. The settings are applied on the host that owns them.

##### 61. `tests/coordinator/hosts.test.ts` and `tests/executor/settings_protocol.py` (Create)

*   **Type / Scope**: Tests.
*   **Proposed Changes**: These tests cover:
    *   A setting change reaches the executor and is reported as applied.
    *   An apply error is shown.
    *   A store under its warning threshold appears as a problem with the three actions.
    *   A full store refuses `reserve` with a reason.

### Verification Plan

*   **Compilation/Build**: `npm run build`, `cargo build --release -p executor`.
*   **Unit Tests**: `npm test`, `cargo test -p executor`, `python3 -I tests/executor/<file>.py` for each executor protocol test.
*   **Static Analysis & Linting**: `tsc --noEmit` (strict settings from `tsconfig.json`), `cargo clippy -- -D warnings`.
*   **Test Coverage Requirement**: Every branch named in items 51, 53 and 54 has a test. No numeric coverage target.
*   **E2E/Integration**: After each phase, run live on PDS18 and the Mac:
    *   **Phase 1:** T1 and T14 from `docs/test-results-T1-T16.md` pass again on Postgres, and `migrate-sqlite verify` reports no difference.
    *   **Phase 2:** CUJ 1 and CUJ 2.
    *   **Phase 3:** CUJ 3 with a file of at least 400 MB, and CUJ 5 with a Mac and PDS18 pair (Windows once Q2 is answered).
    *   **Phase 4:** CUJ 4 and CUJ 6, and a seeded fake token never reaches ClickHouse or Garage.
    *   **Phase 5:** `isolation_probe.py` passes on the Mac and PDS18.
    *   **Phase 6:** an agent answers the CUJ 4 question by citing execution IDs.
*   **Backups**: A restore proof of Postgres, ClickHouse and Garage from `/backup/fleet/` before the design counts as shipped.

*Drafted with EGM*
