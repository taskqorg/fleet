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
4.  **History collection.** Transcripts, events and measurements, gathered from every host, with secrets removed, and readable by people and agents within their permissions.

The laboratory ladder, spending control, account routing and result viewers will be built on this foundation. They are out of scope here.

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
    *   deliveries (merge, publication);
    *   laboratory trials;
    *   delegations, handoffs and forks;
    *   child agents that a harness reports.

    Each execution has one kind, one state set (`preparing`, `running`, `waiting`, `succeeded`, `failed`, `cancelled`, from `fleet.md`) and optional links:
    *   project, task, experiment and parent execution;
    *   session, account, host and environment;
    *   the configuration revision it ran with.
*   **G2. A task table owned by Fleet.**
    *   Tasks are flat and linked by dependencies.
    *   They use the task states from `fleet.md`, and questions and decisions attach to them.
    *   A task's progress, cost and latest result are derived from its linked executions, not stored twice.
*   **G3. An artifact store for any result type and size.**
    *   Artifacts are immutable objects keyed by their content hash.
    *   Each one links to the execution that produced it.
    *   Agents write through a Fleet tool and never receive storage credentials.
    *   An execution that declares a required artifact cannot reach `succeeded` until that artifact is stored.
*   **G4. Complete history, collected centrally.**
    *   Harness transcripts and events from every host are copied to central storage, live where the harness allows and at the latest when the run ends.
    *   Secrets known to Fleet are removed before writing.
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

*   **Q1. Garage features: answered 2026-11-09.** Tested on a local single-node Garage 2.5.0 (Homebrew, native on macOS arm64), using `@aws-sdk/client-s3` v3. All of these passed:
    *   a presigned PUT with `x-amz-checksum-sha256` (200), and a wrong SHA-256 is rejected with 400;
    *   `HEAD` with `ChecksumMode` returns the stored SHA-256, which the commit step can check without downloading;
    *   a presigned GET returns the exact bytes;
    *   a multipart upload through presigned `UploadPart` URLs (11 MiB in 5 MiB parts);
    *   a server-side `CopyObject` between buckets;
    *   `PutBucketLifecycleConfiguration` and `GetBucketLifecycleConfiguration`.

    One gotcha: the SDK client must set `requestChecksumCalculation: 'WHEN_REQUIRED'`. Otherwise the presigner embeds a CRC32 of an empty body in part URLs, and Garage rejects the part with `InvalidDigest`. Garage keeps an independent single-node store on each host, because its layout is cluster-wide.
*   **Q1b. Garage on macOS: answered.** Homebrew ships a native `garage` 2.5.0 bottle for arm64, so the Mac store runs natively under launchd.
*   **Q2. S3 store on Windows.** Garage officially targets Linux. Decide whether the Windows store is Garage under WSL2 or another S3 server with a native Windows build. Placement does not depend on this choice, because each store is independent.
*   **Q9. Connection gateway.** How agents call third-party APIs without seeing keys, and how each call's cost reaches its execution. The gateway is a separate design, and it must use the `connection_cost` record defined here.
*   **Q8. tight D7.** tight D7 says "Do not create a generic S3 service". That decision belongs to the AssetQ prototype. Update tight to allow Fleet's per-host stores, as a Podman Quadlet of `fleet` on PDS18.
*   **Q4. Native OpenTelemetry export: answered 2026-11-09.** The checks read the strings in the installed binaries; no harness was run.
    *   Claude Code 2.1.319 has OTel export (`CLAUDE_CODE_ENABLE_TELEMETRY`, `OTEL_EXPORTER_OTLP_*`).
    *   Codex 0.167.0 has OTel export (`otlp-http`, `OTEL_EXPORTER_OTLP_*`, `log_user_prompt`).
    *   Antigravity 1.5.3 has none.
    *   Under D4, an adapter may point a harness's OTLP exporter at a loopback endpoint of the executor. That makes native OTel one more input to the adapter, not a separate path. Still open: whether the OTel data contains anything the native stream does not.
*   **Q5. Rate-limit signals: answered 2026-11-09.**
    *   Codex rollout files carry `rate_limits` with:
        *   `primary` and `secondary` windows, each with `used_percent`, `window_minutes` and `resets_at`;
        *   `credits` (`has_credits`, `unlimited`, `balance`);
        *   `plan_type` and `rate_limit_reached_type`.

        This is a measured value (`basis = measured`) for each window.
    *   Claude Code `stream-json` emits `rate_limit_event` with `rate_limit_info` (`status`, for example `allowed` or `rejected`, reset time, overage and fallback flags). This is a measured status, not a percentage.
    *   Antigravity has no CLI command for quota: `agy models` lists models only. Its binary contains a local `LanguageServerService/RetrieveUserQuotaSummary` handler and a `RetrieveUserQuota` response with buckets (`remaining_fraction`, `remaining_amount`, `reset_time`). Next live check: call that handler on the language server that an ACP session starts. Calling Google's API directly with the account's OAuth token is not acceptable, because it would act outside the harness.
    *   The executor drops all of these today (see item 35).
*   **Q6. Claude transcript cleanup: answered 2026-11-09.**
    *   The default `cleanupPeriodDays` is 30 days.
    *   Claude 2.1.319 skips the retention sweep when `--setting-sources` excludes user settings and no other enabled source provides `cleanupPeriodDays`. Fleet launches Claude with `--setting-sources ""` (`crates/executor/src/harness.rs:226`), so Fleet-launched transcripts are not deleted by Claude.
    *   The risk remains for sessions launched without that flag, and for any future change to that behaviour. Copying transcripts when a run ends (G4) is still required, but it is not urgent.
*   **Q7. Backup and durability.** Is one storage node on PDS18 with backups enough, or is replication needed? What is the backup target?

## Critical User Journeys (CUJs)

### CUJ 1: Operator sees what a task cost and where it stands

*   **Given**: A task in project `dashq` has had three executions: a worker run on Codex, a reviewer run on Claude, and a delivery.
*   **When**: The operator opens the task.
*   **Then**: The operator sees:
    *   the task state and its waiting reason, if any;
    *   the latest result;
    *   tokens per account, money spent on API calls, and total time.

    Each total says whether it is complete or partial. The operator can open any execution and read its transcript.

### CUJ 2: Lab trials stay out of production views but stay analysable

*   **Given**: An experiment ran 300 trials across 4 variants.
*   **When**: The operator lists the project's work, and later the architect asks an agent to compare the variants.
*   **Then**: The operator's list shows tasks only, with no trials. The agent queries executions linked to the experiment and gets per-variant totals and scores, with pointers to individual runs.

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
*   **Then**: The agent queries totals by project, task, kind and configuration revision, and drills into the most expensive executions. It reads transcript parts on demand. It never sees credentials, because they were removed at ingest. Its answer cites execution IDs.

### CUJ 5: A project keeps its heavy assets on one host

*   **Given**: A game project has `artifact_locations: [windows]`, and a design project has `[windows, mac]`.
*   **When**: A worker on the Windows host saves a 2 GB asset bundle.
*   **Then**:
    *   The game bundle goes to the Windows store and is never copied anywhere else. Its metadata row in Postgres records which store holds it.
    *   A design file from the other project is written on Windows and then copied to the Mac. It shows `placed` once both copies are verified.
    *   Operators on other machines open artifacts through a presigned link while a holding host is online. While none is online, the artifact is shown as unavailable, not lost.

### CUJ 6: History survives the host

*   **Given**: A Claude run finished on the Mac, and its transcript is due for local cleanup.
*   **When**: Claude's cleanup deletes the local file, or the Mac is wiped.
*   **Then**: The transcript and events are already in central storage, and the execution's history is complete.

## Technical Plan (Architecture)

### Decisions taken

| # | Question | Decision |
|---|---|---|
| D1 | Core database | Postgres: role and database `fleet` in the existing system PostgreSQL 18 cluster on PDS18 (tight D6), with local peer authentication and the existing nightly `pg_dump` |
| D2 | Object store and placement | An independent single-node S3 store on each host (Garage; on Windows, Garage under WSL2 or another S3 server, see Q2). Fleet manages placement: each project lists the locations that must hold its artifacts, and Fleet copies objects between stores and checks their hashes |
| D3 | Events, spans and measurements | ClickHouse next to Postgres, as a rootless Podman Quadlet unit of the `fleet` user (tight D4/D5). Separate from the ClickHouse that belongs to Langfuse |
| D4 | Getting history off the hosts | Extend the executor outbox. Raw transcripts and artifacts upload directly to the store with presigned URLs. No collector process on the hosts |
| D5 | Isolation boundary | One OS user per harness account, with the harness sandbox inside. Project keys are issued for each execution and never stored with the account user. On Linux, each execution runs in a `systemd-run --scope` |
| D6 | Running the services on PDS18 | Follow tight: one Unix user `fleet`, user systemd units with linger, no Docker and no sudo, containers only as rootless Podman Quadlet. Garage on PDS18 also runs as a Podman Quadlet of `fleet`. Hosts other than PDS18 follow the same pattern with their native service manager |

The principle behind D4 is that the layer between Fleet and the harnesses belongs to Fleet. That layer covers how each harness is configured, how its output is read, and how Fleet interacts with it. Fleet has to write it in any case, so history passes through that layer and nothing writes into Fleet's stores around it. Native OpenTelemetry output from a harness, where it exists, becomes one more input to the same executor adapter. It is not a separate path.

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
 │   confirm hash+size via outbox   │   (central or    │                                │
 └──────────────────────────────────┘    per-location) └────────────────────────────────┘
```

### 1. Execution: one record shape

*   **Granularity rule.** An execution is a unit that Fleet launches, or that a harness reports as a separate agent. That covers:
    *   an agent turn or session run;
    *   a pipeline run and each of its steps;
    *   a delivery;
    *   a laboratory trial;
    *   a delegated or handed-off run;
    *   a reported child agent.

    Calls inside an execution are spans in ClickHouse, not executions. This includes model calls, tool calls and shell commands.
*   **Kinds.** `agent_run`, `pipeline_run`, `pipeline_step`, `delivery`, `child_agent`, `tool_run` (a Fleet tool or connection call that is billed or long-running on its own). A lab trial is an `agent_run` or `pipeline_run` linked to an experiment. It is not a separate kind.
*   **States.** `preparing`, `running`, `waiting`, `succeeded`, `failed`, `cancelled`. Observation confidence is a separate field (`confirmed` or `unconfirmed`, plus `last_seen_at`). Lost contact leaves the last known state and marks it unconfirmed, as `fleet.md` requires.
*   **Unplaceable executions.** An execution bound to a host, account or environment that is not registered is shown as a problem with that reason. It does not wait silently. Example from live state: `6b00f0fb` has waited forever for the non-existent host `pds18`.
*   **Mapping from the prototype.** `completed` → `succeeded`; `stopped` → `cancelled`; `unknown` → last known state with `observation = unconfirmed`. Each migrated row keeps `legacy_kind` and `legacy_id`.
*   **Links.** All are optional foreign keys:
    *   `project_id`, `task_id`, `experiment_id`, `parent_execution_id`;
    *   `session_id`, `account_id`, `host_id`, `environment_id`.

    Lab versus production is not a flag: it follows from the presence of `experiment_id`.
*   **Effective configuration.** `harness`, `harness_version`, `model`, and `config_ref`. `config_ref` holds the repository, revision and paths of the instructions, skills and profile, plus a hash of any draft content. The harness version is recorded on every run, so a self-update inside an experiment is visible.
*   **Totals and coverage.** When an execution ends, the coordinator writes a totals row from ClickHouse to Postgres. It covers tokens by type, subscription-window signals, money by connection, wall-clock and active time, CPU and peak memory. Each value carries `basis`: `measured`, `estimated` or `unavailable`. Totals cover the execution's own spans only. A tree total is the sum over the distinct executions in the tree, so nested usage is never counted twice.

### 2. Task: production work item

*   **Table `task`.**
    *   `id`, `project_id`, `title`, `expected_outcome`;
    *   `state`: `queued`, `in_progress`, `waiting`, `paused`, `completed`, `failed` or `cancelled`;
    *   `wait_reason`, `priority`;
    *   `completion_requirements` (JSONB), `result_destination`;
    *   `created_by`, timestamps.
*   **Table `task_dependency`.** `task_id` and `depends_on_task_id`. Tasks are flat; there is no hierarchy.
*   **Derived, not stored.** Progress, cost so far and the latest result come from a view over the task's executions and artifacts. Questions and decisions keep their existing records and gain a `task_id` link.
*   **External boards.** None. A future export reads from this table.

### 3. Artifact store

*   **Table `store`.** One row per host store: `id`, `kind` (`s3`), `endpoint`, `bucket`, `host_id`, `state` (`reachable`, `unreachable` or `full`), `capacity_bytes`, `used_bytes`, and the credentials reference. The credentials themselves stay in the coordinator's and executor's secret configuration, never in rows.
*   **Project setting `artifact_locations`.** A list of hosts that must hold the project's artifacts, for example `[windows]`, `[mac]` or `[windows, mac]`. The default is `[pds18]`. Optionally, a separate list can be set for lab output and for production output. The setting changes only placement. It never limits who may read the artifacts, which follows project grants.
*   **Table `artifact_copy`.** `artifact_id`, `store_id`, `state` (`pending`, `copying`, `verified`, `failed` or `deleted`), `verified_at`, `last_error`. An artifact is `stored` once one copy is `verified`. It is `placed` once every location in the project's list holds a verified copy. The task and result views show both states.
*   **Placement worker.** It runs in the coordinator. For each artifact with missing copies, it copies the object from a verified store to the missing store, checks the hash, and marks the copy `verified`.
    *   The copy goes directly between stores, either by a server-side copy where both stores can reach each other, or through the executor of the target host. It never passes through an operator's machine.
    *   A store that is offline (a sleeping Mac, a Windows reboot) leaves its copies `pending`. They resume when its host reports the store reachable again.
    *   Objects are immutable and keyed by hash, so a repeated copy is safe and there are no version conflicts.
    *   Changing a project's locations schedules new copies. Removing a location deletes copies there only after the other locations hold verified copies.
*   **Local first.** A worker writes to the store on its own host when that host is in the project's list. Otherwise it writes to the nearest store in the list. Files that must stay on one machine therefore never leave it (CUJ 5).
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
    *   The placement worker carries out a rule as ordinary copies and deletions, under the same safety rule: delete only after the target holds verified copies.
    *   Deletion keeps the `artifact` row as a tombstone (`state = deleted`, `deleted_at`, `deleted_by = rule:<id>`).
    *   A new or changed rule runs only after the operator reviews a preview. The preview lists the count, total size and a sample of the affected artifacts, and the operator confirms it.
    *   Without rules, nothing moves or expires. History in ClickHouse is not affected by artifact rules.
*   **Reading.** `artifacts.get` returns a link to a verified copy, preferring the reader's own host. When no listed store is reachable, the artifact is shown as unavailable, not lost.
*   **Table `artifact`.**
    *   `id`, `content_hash` (sha256), `size`, `media_type`, `name`;
    *   `store_id`, `object_key`;
    *   `execution_id`, `project_id`, `task_id`;
    *   `required`, `previous_artifact_id`;
    *   `derived_from_artifact_id` and `role` (`original`, `thumbnail`, `poster` or `web`). A preview is an ordinary artifact that points to its original;
    *   `state`: `pending`, `stored`, `failed` or `deleted`;
    *   `deleted_at` and `deleted_by`, which form the tombstone.
*   **Object keys.** `<project>/<lab|prod>/<sha256>` in each store. The same content in the same project is stored once per store.
*   **Write protocol.**
    1.  The executor asks the coordinator to reserve an artifact, sending name, size, type and hash.
    2.  The coordinator returns a presigned PUT, or a multipart upload, for the chosen store in the project's list.
    3.  The executor uploads directly to the store.
    4.  The executor confirms through the outbox.
    5.  The coordinator checks the object's size and hash on the store, then marks it `stored`.
*   **Success rule.** An execution with a `required` artifact that is not `stored` cannot move to `succeeded`. It stays `running` while retries continue, then becomes `failed` with the upload error. This replaces today's behaviour, where a run succeeds after a 4xx.
*   **Raw transcripts.** These are artifacts with `media_type` set to the harness log format. They are uploaded when the run ends, and in chunks during long runs. This protects them against local cleanup (CUJ 6).
*   **No size limit in Fleet.** The base64 path and `FLEET_MAX_ARTIFACT_BYTES` are removed. Limits are the store's disk, and they show as store capacity.

### 4. ClickHouse: events, spans, measurements

*   **Tables.**
    *   `event`: execution state changes, questions, errors and harness lifecycle.
    *   `span`: model calls, tool calls and shell commands, with tokens (input, output, cache read, cache write), duration and status.
    *   `measurement`: CPU, memory, rate-limit signals and connection cost, each with its `basis`. Connection cost rows carry `connection_id`, `amount`, and `currency` or `credits`.
    *   `host_metric`: host health, disk, harness versions and reachability.
*   **Columns copied from Postgres at ingest.** `project_id`, `task_id`, `experiment_id`, `account_id`, `host_id` and `execution_id`. Every query can then filter by permission and group without a join.
*   **Engine and keys.** `ReplacingMergeTree` keyed by (`execution_id`, `seq`), so a retried outbox batch does not duplicate rows. Partitioned by month and ordered by (`project_id`, `execution_id`, `ts`). No TTL by default. A project retention rule can add one later.

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
    *   Second pass, in the coordinator: a pattern scan for common token formats. A match is logged as a redaction miss to fix.
*   **Host metrics.** The executor adds periodic `host_metric` reports to the existing host observation: disk, load, harness versions and reachable stores.

### 6. Operations for people and agents

People and agents use the same coordinator operations under the same permissions:

*   `executions.query`: filters on links, kind, state and time.
*   `executions.get`: totals with coverage, plus the span tree.
*   `tasks.list`, `tasks.get`, `tasks.create`, `tasks.update`.
*   `usage.summary`: group by project, task, experiment, account, host, kind or configuration, with totals that count nothing twice.
*   `transcript.read`: one execution, a range or a search, returning redacted text marked as data.
*   `artifacts.get`: metadata plus a short-lived presigned GET.

Permissions:
*   The project grant is applied inside each operation.
*   Postgres row-level security on `project_id` is the second guard.
*   ClickHouse is reachable only through these operations, and every query gets a mandatory project filter.

Agent replies return summaries and IDs by default. Raw transcript text is returned only by `transcript.read`.

### 7. Migration from `records`

*   **In scope.** These kinds map to the new tables: `execution`, `delegation`, `pipeline-run`, `delivery`, the lab trials inside `experiment`, `session-fork`, `handoff` and `artifact`.
*   **Out of scope.** Every other record kind moves unchanged into a Postgres table `document(kind, id, data jsonb)`, with the same behaviour as today. Those kinds get their own tables only when a later design needs them.
*   **Base64 artifacts.** They are decoded, hashed and uploaded to the central Garage store.
*   **Cut-over.** A one-time migration runs during a short planned stop. The SQLite file stays read-only as the rollback source. Rolling back means pointing the coordinator back at SQLite. Any work done after the cut-over must then be re-exported from Postgres by a reverse script, which is part of the migration tool.

### 8. Agent-facing tools for results and history

The shapes are copied from t3code (`apps/server/src/mcp/toolkits/attachment/tools.ts`, `agentContextBudget.json`; revision `d0f1f7200d`) and Orca (`ORCA_CHAT_SCRATCH_DIR`, `orca skills get`; revision `0b93cef4450`).

*   **Output folder, the default path.**
    *   Each execution gets `FLEET_OUTPUT_DIR`, a folder inside its working area.
    *   Anything the agent writes there is collected by the executor when the run ends, and in chunks during long runs. It is stored as artifacts of that execution.
    *   This extends the worktree output collection added in `23c5418`.
    *   The agent needs no tool call and no knowledge of storage.
*   **Explicit tools.** MCP tools on the existing `/api/tools` surface:
    *   `artifact_prepare_upload({name, media_type, size, sha256, required?})` returns `{artifact_id, upload: {url | parts[]}, expires_at}`. Use it for files larger than a single request, or to store a file now rather than at the end.
    *   `artifact_commit({artifact_id})` returns `{artifact_id, state}`. The coordinator checks size and hash on the store.
    *   `artifact_discard({artifact_id})`.
    *   `artifact_list({execution_id? | task_id?, media_type?})` returns metadata only.
    *   `artifact_get({artifact_id})` returns metadata plus a local path when the file is already on this host, or a short-lived URL otherwise. It never returns the file content inline.
    *   `history_query({filters, group_by?})` maps to `usage.summary` and `executions.query`.
    *   `transcript_read({execution_id, range? | search?})` returns redacted text marked as data, with paging (`cursor`, `limit`).
*   **Files as paths, not content.** Inputs and earlier results reach the agent as paths in the prompt, as both t3code and Orca do. The agent reads what it needs.
*   **Real schemas.** Each tool in `agent-tools.ts` gets a real `inputSchema` instead of today's `{type:'object'}`.
*   **Instructions on demand.**
    *   A short `fleet` skill stub says what the output folder is and lists the tool names.
    *   The full guide is read on request.
    *   A test enforces a byte budget on the tool list and on the stub, as t3code does.

### 9. Isolation boundary

**Mechanism (D5).**

*   **Boundary.** One OS user per harness account, for example `fleet-claude-1` or `fleet-codex-2`.
    *   The executor runs under its own service user and launches each agent as the account user:
        *   macOS: `launchd` or `sudo -u`;
        *   Linux: a systemd unit;
        *   Windows: `CreateProcessWithLogonW`.
    *   The account user owns its `HOME` and its native login.
*   **Keys for each execution.**
    *   Project keys and connection credentials live only with the executor's service user.
    *   For each execution, the executor places the key the execution needs into a temporary file, or behind a per-execution `ssh-agent` socket, inside the working area. It deletes the key when the run ends.
    *   The account user's `HOME` never holds project keys. Projects that share an account therefore cannot read each other's keys.
*   **Inner layer.** The harness sandbox separates executions that run under the same account user. It is configured per profile.
*   **Measurement.**
    *   On Linux (PDS18), each execution runs inside `systemd-run --scope`, and its cgroup gives exact CPU and memory for the execution. Basis: `measured`.
    *   On macOS and Windows, usage is measured per account user. Each execution's process tree is sampled by pid. Basis: `estimated`.
*   **Known gaps, accepted.**
    *   On macOS and Windows, there is no OS boundary between projects that share one account.
    *   On those two systems, CPU per execution is an estimate.
    *   The login Keychain of a macOS user is locked while that user is not logged in, and Claude then falls back to file storage. This was already observed; record it as a harness fact.
*   **Upgrade path.** Each gap closes with the per-execution OS mechanism for that platform, adopted one platform at a time when needed:
    *   Seatbelt on macOS;
    *   a Job Object plus AppContainer on Windows.

Requirements the mechanism meets:

*   **Secrets.**
    *   An agent's environment holds only the connections granted to its execution.
    *   Key files belong to the boundary's owner and cannot be read from other boundaries.
    *   The Fleet machine token is never readable by an agent.
*   **Native logins.** Each harness account keeps its native login (Keychain or file) inside its own boundary, with a real `HOME`.
*   **Working area.** Each execution writes to its own working area. The shared `.git` is read-only to agents, and writes go through the delivery path. Host-side git runs with hooks and fsmonitor disabled, so files an agent planted in a checkout never run on the host.
*   **Network.** Network access is a setting per project or profile, not a fixed rule.
*   **Measurement.** CPU, memory and disk are measured for everything inside the boundary, including children:
    *   cgroups on Linux;
    *   a Job Object on Windows;
    *   per-user process accounting on macOS.
*   **Observation.** A person can log in to the boundary, or attach to it, and watch the agent.
*   **Inner layer.** A harness's own sandbox (Claude Code Seatbelt/bubblewrap, Codex sandbox) may run inside the boundary for each execution. It is configured per profile.

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
*   **Reason for Rejection**: Rejected by author: Fleet keeps tasks in its own central tables and builds its own simple view: smooth scrolling, decision blocks, text answers and choices.

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
*   **Reason for Rejection**: Rejected by author: do not reinvent the wheel.

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
*   **Reason for Rejection**: Rejected by author: AssetQ and Pipe are prototypes, not commitments. Choose the option that fits per-project placement best.

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
    *   Its trace, observation and score model duplicates the execution, which breaks the "no extra entities" rule.
    *   Its permissions do not match Fleet's project grants.
    *   It is a heavy stack, and its S3 needs are unverified against Garage.
*   **Reason for Rejection**: Rejected by author: Fleet keeps one execution model in its own stores.

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
*   **Reason for Rejection**: Rejected by author: too much effort for the remaining 5% of requirements. It stays as the upgrade path, adopted one platform at a time when needed. Linux already gets per-execution cgroups through `systemd-run --scope`.

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
    *   Project keys exist only for the length of an execution (D5).
    *   Secrets are redacted twice: by exact value in the executor, and by pattern in the coordinator.
    *   History returned to agents is marked as data.
    *   Postgres row-level security on `project_id` backs up the permission checks in each operation.
*   **Performance.** These numbers are working targets for measurement, not caps.
    *   Task and execution list queries return in under 200 ms at 10 million executions.
    *   `usage.summary` over 30 days of one project returns in under 2 s.
    *   Outbox ingest keeps up with 10 parallel sessions, the load already proven live.
*   **Durability.**
    *   Postgres is backed up by the existing nightly `pg_dump` (tight D6). A backup counts only after a restore proof.
    *   ClickHouse and Garage backups go to `/backup/fleet/` for the provider pull. Their restore proof is part of acceptance.
    *   Projects with a single artifact location accept the risk of a single copy. The task view shows that risk.
*   **Rollout & Rollback.**
    *   Rollout is phased; each phase ships on its own.
    *   Until Phase 2 completes, rollback means pointing the coordinator back at the read-only SQLite file and running the reverse export.

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
*   **Why**: D3 and D6. It follows tight D5: rootless Podman Quadlet, no Docker.

##### 2. `deploy/infrastructure/fleet-garage.container` and `deploy/infrastructure/garage.toml.candidate` (Create)

*   **Type / Scope**: Podman Quadlet unit and Garage configuration.
*   **Proposed Changes**:
    *   A single-node Garage with `replication_factor = 1`.
    *   The S3 API on loopback port 9490, and the admin API on loopback.
    *   Data under `/home/fleet/data/garage`.
    *   One bucket `fleet`.
*   **Why**: D2. PDS18's own store.

##### 3. `deploy/infrastructure/fleet-postgres.sql` (Create)

*   **Type / Scope**: SQL run once by root through the existing route.
*   **Proposed Changes**: Creates role `fleet` (`LOGIN`, no superuser, no `BYPASSRLS`) and database `fleet`, owned by `fleet`. Peer authentication stays on the existing local socket.
*   **Why**: D1 and tight D6, one role and one database per service.

##### 4. `deploy/infrastructure/fleet-backup.sh` and `fleet-backup.timer` (Create)

*   **Type / Scope**: User timer script.
*   **Proposed Changes**:
    *   A nightly ClickHouse `BACKUP` to a local disk target, and a Garage bucket mirror, both written to `/backup/fleet/YYYY-MM-DD/`.
    *   Keeps the same 5 days as tight D6.
    *   No service stops.
*   **Why**: Durability. The existing `pg_dump` already covers Postgres.

##### 5. `deploy/infrastructure/fleet.service.candidate` (Modify)

*   **Type / Scope**: Coordinator user unit.
*   **Proposed Changes**:
    *   Add `After=` and `Wants=` for the ClickHouse and Garage units.
    *   Add environment names `FLEET_DATABASE_URL` (Unix socket, peer authentication), `FLEET_CLICKHOUSE_URL` and `FLEET_STORES_FILE`.
    *   Remove `FLEET_MAX_ARTIFACT_BYTES`.
*   **Why**: The coordinator needs the new stores.

##### 6. `deploy/hosts/macos/fleet-garage.plist`, `deploy/hosts/windows/fleet-garage.md` (Create)

*   **Type / Scope**: Store on the other hosts.
*   **Proposed Changes**:
    *   macOS: a launchd agent running Garage single-node under the executor service user, listening on loopback.
    *   Windows: an install note that stays open until Q2 is answered (WSL2 or a native S3 server).
    *   Each host's store is reachable from other hosts only through its executor.
*   **Why**: D2, local stores.

##### 7. `docs/infrastructure-build.md` (Modify)

*   **Type / Scope**: Documentation.
*   **Proposed Changes**: Record the Phase 0 install and readback steps, following the existing style: SHA256 or digest verification, readback of listeners and units.
*   **Why**: The install must be repeatable.

##### 8. `~/Projects/tight/tight.md` (Modify, separate repository)

*   **Type / Scope**: Infrastructure decisions.
*   **Proposed Changes**:
    *   Add `fleet` to the D4 user table.
    *   Note the Fleet ClickHouse and Garage under D5 and D7.
    *   Replace "Do not create a generic S3 service" with a scoped rule: Fleet's per-host store is allowed as a Podman Quadlet of `fleet` (Q8).
*   **Why**: tight is the single source for host decisions (tight D13).

#### Phase 1: async data access on Postgres

##### 9. `package.json` (Modify)

*   **Type / Scope**: Dependencies.
*   **Proposed Changes**:
    *   Add `pg`, `@types/pg`, `@clickhouse/client`, `@aws-sdk/client-s3`, `@aws-sdk/s3-request-presigner`, and `@electric-sql/pglite` (dev).
    *   Keep `better-sqlite3` until Phase 7, for the migration tool only.
*   **Why**: The new stores.

##### 10. `packages/coordinator/db/schema.sql` (Create)

*   **Type / Scope**: Postgres schema with numbered migrations.
*   **Proposed Changes**:
    *   `schema_migration(version, applied_at)`.
    *   `document(kind text, id text primary key, data jsonb, project_id text null, updated_at)`, holding every legacy kind.
    *   Phase 2 and Phase 3 tables are added by later migrations in the same file.
*   **Why**: G7. Phase 1 moves storage without changing the model.

##### 11. `packages/coordinator/store.ts` (Create)

*   **Type / Scope**: TypeScript data-access module.
*   **Proposed Changes**:
    *   `createStore(url)` returns `{get(id, kind), put(kind, doc), all(kind, filter?), tx(fn), query(sql, params)}`.
    *   All of them are async. `tx` runs `fn` inside one Postgres transaction at `SERIALIZABLE` isolation and retries on serialization failure.
    *   It replaces the synchronous `get`, `put` and `all` helpers defined in `app.ts`.
*   **Why**: D1 requires async access. A single module keeps the change in one place.

##### 12. `packages/coordinator/app.ts` (Modify)

*   **Type / Scope**: Coordinator HTTP application.
*   **Proposed Changes**:
    *   Remove `better-sqlite3` and the `records` table creation at line 19.
    *   Accept a `store` from `createStore`.
    *   Make every handler `await` the store.
    *   Turn `db.transaction(() => …)` blocks, such as `/api/hosts/poll` at line 106, into `store.tx(async t => …)`.
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
    *   Drop `FLEET_DATABASE` and `FLEET_MAX_ARTIFACT_BYTES` after Phase 3.
    *   Run the schema migrations on start.
*   **Why**: Configuration for the new stores.

##### 15. `packages/coordinator/migrate-sqlite.ts` (Create)

*   **Type / Scope**: One-time migration tool.
*   **Proposed Changes**:
    *   `forward`: read every `records` row and insert it into `document`, keeping kind, id and data.
    *   `verify`: compare row counts and data hashes per kind.
    *   `reverse`: export `document` rows changed after the cut-over back into a copy of the SQLite file.
    *   Phase 2 extends it with typed mapping, and Phase 3 with extraction of base64 artifacts.
*   **Why**: G7, migration without loss and with a rollback path.

#### Phase 2: executions and tasks

##### 16. `packages/coordinator/db/schema.sql` (Modify)

*   **Type / Scope**: Migration 2.
*   **Proposed Changes**:
    *   `execution`:
        *   `id`, `kind` (enum), `state` (enum);
        *   `observation` (`confirmed` or `unconfirmed`), `last_seen_at`;
        *   `project_id`, `task_id`, `experiment_id`, `parent_execution_id`, `session_id`, `account_id`, `host_id`, `environment_id`;
        *   `harness`, `harness_version`, `model`, `config_ref jsonb`;
        *   `problem text null`, `started_at`, `ended_at`;
        *   `legacy_kind`, `legacy_id`, `data jsonb`.
    *   `execution_totals`: `execution_id` plus one column per measure, each with a `_basis` column.
    *   `task`: the columns listed in Technical Plan §2.
    *   `task_dependency`.
    *   Indexes on (`project_id`, `state`, `started_at`), `task_id`, `experiment_id`, `parent_execution_id` and `account_id`.
    *   Row-level security policies on `project_id`, for a session variable `fleet.project_ids` that each operation sets.
*   **Why**: G1, G2 and G6.

##### 17. `packages/coordinator/executions.ts` (Create)

*   **Type / Scope**: Execution model.
*   **Proposed Changes**:
    *   `createExecution`, `transition(id, state)`, and `markObservation`.
    *   `detectProblems()` marks executions bound to an unregistered host, account or environment, filling `problem`, as with `6b00f0fb`.
    *   `writeTotals(id)` reads ClickHouse after Phase 4, and before that writes only the measures that are available.
    *   The legacy state mapping from Technical Plan §1.
*   **Why**: G1.

##### 18. `packages/coordinator/tasks.ts` (Create)

*   **Type / Scope**: Task operations.
*   **Proposed Changes**:
    *   `tasks.create`, `tasks.update`, `tasks.get` and `tasks.list`.
    *   `tasks.get` returns the derived progress, cost and latest result through a view over the task's executions and artifacts.
    *   Questions and decisions gain a `task_id` link in `document.data`.
*   **Why**: G2.

##### 19. `packages/coordinator/delegation.ts`, `delivery.ts`, `lab.ts`, `projects.ts`, `agent-tools.ts`, `app.ts` (Modify)

*   **Type / Scope**: Writers of run records.
*   **Proposed Changes**:
    *   Write `execution` rows through `executions.ts` instead of the `execution`, `delegation`, `pipeline-run` and `delivery` documents, with these kinds:
        *   `agent_run`;
        *   `pipeline_run` and `pipeline_step`;
        *   `delivery`;
        *   `child_agent` (from `d3802cc` recording).
    *   Lab trials set `experiment_id`.
    *   Handoff and fork set `parent_execution_id`.
*   **Why**: G1, with one record shape.

##### 20. `packages/coordinator/project-ui.ts` (Modify)

*   **Type / Scope**: Operator view.
*   **Proposed Changes**: A task list and a task detail page that read `tasks.list` and `tasks.get`. The page scrolls smoothly without jumping, shows decision blocks for open questions, and answers questions by text or by option.
*   **Why**: G2 and the owner's view requirements. This is the minimal view, not a full interface.

#### Phase 3: artifacts and placement

##### 21. `packages/coordinator/db/schema.sql` (Modify)

*   **Type / Scope**: Migration 3.
*   **Proposed Changes**: The `store`, `artifact` and `artifact_copy` tables from Technical Plan §3, with indexes on `content_hash`, `execution_id`, `task_id` and (`store_id`, `state`).
*   **Why**: G3 and D2.

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
    *   `reserve({execution_id, name, media_type, size, sha256, required})` picks the store: the writer's host if it is in the list, otherwise the nearest one. It returns a presigned PUT, or multipart part URLs for files over 64 MiB.
    *   `commit(id)` checks the object's size and hash on the store with `HEAD` plus a stored checksum, then marks the artifact and its first copy `verified`.
    *   `discard(id)`, `list(filter)` and `get(id)`. `get` returns metadata and a presigned GET for a verified copy, preferring the reader's host.
    *   The success rule: `executions.transition(id, 'succeeded')` refuses while any `required` artifact is not `stored`.
*   **Why**: G3.

##### 25. `packages/coordinator/placement.ts` (Create)

*   **Type / Scope**: Placement worker and audit.
*   **Proposed Changes**:
    *   `runPlacement()` repeats on a short interval. For each artifact missing a verified copy at a listed location, it copies with a server-side `CopyObject` where both stores are reachable from the coordinator, and otherwise queues a `copy` command to the target host's executor. It then verifies the hash.
    *   `runAudit()` re-hashes a sample of copies in each store every day and marks missing or corrupt copies `failed`, which schedules a new copy.
    *   Removing a location deletes copies there only after the remaining locations hold verified copies.
*   **Why**: D2. The audit prevents a false `placed` state.
*   **Retention rules**: `previewRule(projectId, rule)` returns the count, the bytes and a sample of affected artifacts. `confirmRule` stores the confirmation. `runRules()` applies only confirmed rules, through the same copy-then-delete path.

##### 26. `packages/coordinator/app.ts` (Modify)

*   **Type / Scope**: Routes.
*   **Proposed Changes**:
    *   Replace `POST /api/hosts/artifacts` (base64 at line 118) with `POST /api/hosts/artifacts/reserve` and `POST /api/hosts/artifacts/commit`.
    *   Remove `maxArtifactBytes` and the body limit derived from it.
    *   `GET /api/results/:id` redirects to a presigned GET.
*   **Why**: G3, no size limit inside Fleet.

##### 27. `crates/executor/src/upload.rs` (Create)

*   **Type / Scope**: Rust module.
*   **Proposed Changes**:
    *   Hash a file with streaming SHA256.
    *   Call reserve, then PUT the file (or its parts) to the presigned URLs with `ureq`, then commit.
    *   Retry with backoff.
    *   Keep a pending upload in the existing `journal.sqlite`, so it survives an executor restart.
    *   It also serves the `copy` command from `placement.ts`: it downloads from the source presigned GET and uploads to its local store.
*   **Why**: G3 and D4. Files go directly to the store.

##### 28. `crates/executor/src/main.rs` (Modify)

*   **Type / Scope**: Executor core.
*   **Proposed Changes**:
    *   Replace the base64 artifact queue at line 812 with `upload.rs`.
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
    *   Each table carries the link columns, uses `ReplacingMergeTree` keyed by (`execution_id`, `seq`), is partitioned by month, and is ordered by (`project_id`, `execution_id`, `ts`).
    *   No TTL.
*   **Why**: G4 and G5.

##### 31. `packages/coordinator/ingest.ts` (Create)

*   **Type / Scope**: Outbox ingest.
*   **Proposed Changes**:
    *   `ingestBatch(hostId, rows)` splits each batch: state changes go to Postgres in one transaction, telemetry rows go to ClickHouse in batches.
    *   It copies the link columns from the execution row.
    *   It runs the second-pass redaction scan.
    *   It counts rows that a 4xx sets aside on the execution (`history_incomplete`).
    *   It replaces the event handling at `/api/hosts/events`.
*   **Why**: D4, with nothing written around Fleet's own layer.

##### 32. `packages/coordinator/redaction.ts` (Create)

*   **Type / Scope**: Pattern redaction.
*   **Proposed Changes**:
    *   Patterns for common token formats: bearer tokens, `sk-` keys, GitHub tokens, private key blocks, AWS keys.
    *   A match is replaced and logged as a `redaction_miss` event with the pattern name only.
*   **Why**: G4, the second pass.

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
*   **Why**: D4 and G5.

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
    *   `resources()` at line 925 reports CPU and memory for the whole process tree, by pid-tree sampling, with basis `estimated`. On Linux it reads the execution's cgroup instead, with basis `measured`, after Phase 5.
    *   Add periodic `host_metric` rows: disk, load, harness versions and store reachability.
*   **Why**: G5 and the host telemetry the owner asked for.

#### Phase 5: isolation boundary

##### 38. `crates/executor/src/isolation.rs` (Create)

*   **Type / Scope**: Rust module.
*   **Proposed Changes**:
    *   `launch_as(account_user, cmd)` per OS:
        *   macOS: `launchctl asuser`, or `sudo -u` through one narrow sudoers rule for the executor's service user;
        *   Linux: `systemd-run --uid=<user> --scope`;
        *   Windows: `CreateProcessWithLogonW`, active when the Windows executor exists.
    *   `issue_keys(execution)` writes the needed key to `<working area>/.fleet/keys/` with mode 0600, owned by the account user, or starts a per-execution `ssh-agent` socket. It deletes the key when the run ends.
    *   `cgroup_usage(scope)` on Linux.
*   **Why**: G8 and D5.

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
    *   Add the executor's narrow launch rule.
*   **Why**: D5. The owner runs these once per account.

##### 42. `docs/layered-isolation-model.md` and `docs/multi-account-same-env.md` (Modify)

*   **Type / Scope**: Documentation.
*   **Proposed Changes**: State the D5 boundary, the accepted gaps, the upgrade path (Seatbelt; Job Object with AppContainer), and the locked-Keychain fact.
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
    *   Replace `inputSchema:{type:'object'}` at line 191 with real JSON schemas for every tool.
    *   Add `artifact_prepare_upload`, `artifact_commit`, `artifact_discard`, `artifact_list`, `artifact_get`, `history_query` and `transcript_read`.
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
*   **Why**: Resolves the brief's contradictions with `fleet.md`.

##### 48. `docs/state-ownership.md`, `docs/work-analysis.md`, `docs/capabilities-and-pipelines.md` (Modify)

*   **Type / Scope**: Documentation.
*   **Proposed Changes**: Reference the task table, the execution as the unit of analysis, and artifact placement. Remove claims that this design supersedes.
*   **Why**: Each rule keeps one canonical source.

##### 49. `docs/designs/fleet-data-foundation.md` and `docs/README.md` (Create / Modify)

*   **Type / Scope**: Design record.
*   **Proposed Changes**: The finalized version of this draft, linked from the specification guide.
*   **Why**: The EGM handoff.

##### 50. `package.json` and `packages/coordinator/migrate-sqlite.ts` (Modify)

*   **Type / Scope**: Retirement.
*   **Proposed Changes**: After 30 days without a rollback, remove `better-sqlite3` and archive the SQLite file to `/backup/fleet/`.
*   **Why**: G7. The old store is retired.

#### Tests

##### 51. `tests/coordinator/store.test.ts`, `executions.test.ts`, `tasks.test.ts`, `artifacts.test.ts`, `placement.test.ts`, `ingest.test.ts`, `usage.test.ts`, `migrate.test.ts` (Create)

*   **Type / Scope**: Unit tests on PGlite, with a fake S3 and a fake ClickHouse client.
*   **Proposed Changes**:
    *   **store:** transaction retry.
    *   **executions:** legacy state mapping; detection of an unregistered host; observation `unconfirmed`.
    *   **tasks:** derived progress and cost; dependencies.
    *   **artifacts:** reserve and commit; hash mismatch is refused; a required artifact blocks `succeeded`.
    *   **placement:** an `[windows]` project is never copied elsewhere; a `[windows, mac]` project reaches `placed`; a store offline leaves copies `pending`; removing a location is safe; the audit detects a missing copy; an unconfirmed rule never runs; a delete rule leaves tombstones and removes nothing while the target lacks verified copies.
    *   **ingest:** a duplicate `seq` is idempotent; the 4xx count is reported; the redaction scan works.
    *   **usage:** no double counting in tree totals; the project filter is always present.
    *   **migrate:** forward, verify and reverse round-trip.

##### 52. `tests/coordinator/*.test.ts` (Modify)

*   **Type / Scope**: Existing coordinator tests.
*   **Proposed Changes**: Run them against `createStore` on PGlite instead of SQLite, with async calls. Expected behaviour is unchanged.

##### 53. `tests/coordinator/agent-tools.test.ts` (Modify)

*   **Type / Scope**: Tool tests.
*   **Proposed Changes**: Every tool has a non-empty schema. The new artifact and history tools are covered. A byte budget is set on the `tools/list` JSON and on the `fleet` skill stub, starting from measured size plus 10%, as t3code's `AgentContextBudget.test.ts` does.

##### 54. `tests/executor/upload_protocol.py`, `redaction.py`, `isolation_probe.py` (Create), and Rust unit tests in `upload.rs`, `redact.rs`, `isolation.rs`

*   **Type / Scope**: Executor tests.
*   **Proposed Changes**:
    *   **upload:** a multipart upload resumes after an executor restart.
    *   **redaction:** every injected secret is removed from the outbox rows and from the uploaded transcript.
    *   **preview:** a PNG and an MP4 fixture produce a thumbnail and a poster linked by `derived_from_artifact_id`; without `ffmpeg`, the result is `unavailable` and the execution still succeeds.
    *   **isolation:** an agent cannot read another account's `HOME`, the shared key directory, or the machine token; a hook or `core.fsmonitor` that an agent planted in a checkout does not run during host-side git; the key file is gone after the run; the Linux cgroup reports usage.

### Verification Plan

*   **Compilation/Build**: `npm run build`, `cargo build --release -p executor`.
*   **Unit Tests**: `npm test`, `cargo test -p executor`, `python3 -I tests/executor/<file>.py` for each executor protocol test.
*   **Static Analysis & Linting**: `tsc --noEmit` (strict settings from `tsconfig.json`), `cargo clippy -- -D warnings`.
*   **Test Coverage Requirement**: Every branch named in item 51 has a test. No numeric coverage target.
*   **E2E/Integration**: After each phase, run live on PDS18 and the Mac:
    *   **Phase 1:** T1 and T14 from `docs/test-results-T1-T16.md` pass again on Postgres, and `migrate-sqlite verify` reports no difference.
    *   **Phase 2:** CUJ 1 and CUJ 2.
    *   **Phase 3:** CUJ 3 with a file of at least 400 MB, and CUJ 5 with a Mac and PDS18 pair (Windows once Q2 is answered).
    *   **Phase 4:** CUJ 4 and CUJ 6, and a seeded fake token never reaches ClickHouse or Garage.
    *   **Phase 5:** `isolation_probe.py` passes on the Mac and PDS18.
    *   **Phase 6:** an agent answers the CUJ 4 question by citing execution IDs.
*   **Backups**: A restore proof of Postgres, ClickHouse and Garage from `/backup/fleet/` before the design counts as shipped.

*Drafted with EGM*
