# Fleet: architecture assessment and open gaps

* **Author:** Alex Kirs
* **Date:** 2026-11-09
* **Status:** Working draft before Goldfish review. W4 is merged. The review starts only on the owner's confirmation.

## Overview

Fleet runs on the owner's own infrastructure: a Mac, a Windows machine and a Linux server (PDS18). It builds agents from a harness, a model, instructions and skills. It runs those agents in a laboratory against a set of tasks and measures each run: tokens, time, CPU, cost and quality. It then moves the best variant into production work, where operators handle dozens of tasks a day and accept results. Results can be text, code, images, 3D models or video.

The system exists to help people work, not to restrict them. Two rules follow from that:

* A harness limitation is a recorded fact about that harness, not a blocker. One example: Codex sub-agents cannot be switched off. Fleet records the limitation in the harness profile. A person or an agent takes it into account, and work continues.
* Fleet controls all of its hosts. Agents get the strongest setup available, and a person can log in to any host and watch any agent.

Fleet collects every run's metrics, its full history and its materials. It keeps them in reliable storage that can grow, built on existing products rather than custom ones. Agents can query this history. A person can open a session with an agent to discuss spending, inspect how an agent reasoned and which tools it called, propose a new agent configuration, and start a laboratory test.

## Analysis

### Assessment of the approach so far

The order of work is sound: the team tested the hard runtime problems against the real world before building on top of them. The weak point is balance. Most of the effort so far has gone into the runtime: harness binding, accounts, handoff and delivery. The parts that justify building Fleet at all have little or no evidence yet:

* the laboratory "ladder", meaning repeated improvement against a score;
* honest resource accounting;
* account balancing;
* the task object.

The main risk is that the runtime keeps getting polished while the core hypothesis stays unproven. The core hypothesis is that tests find better instructions and that the winning instructions carry over to production work.

### Proven live (from `docs/` and `evidence/`)

* Mac and PDS18 executors run Codex (2 accounts), Claude and Antigravity.
* A session was handed across harnesses: Codex → Claude → Antigravity → Codex.
* Native fork works in Codex and in Claude.
* Ten Codex turns ran in parallel without a 429 error.
* Four deliveries ran end to end.
* Account gates refuse launches with 409.
* The laboratory has one retained comparison of two variants.

### Real-world constraints found (record as facts, not blockers)

| Constraint | Consequence for the architecture |
|---|---|
| Claude reaches the macOS Keychain only through the real `HOME`, and the personal `~/.claude` stays reachable. Antigravity allows one account per OS user. The provider prompt cache is shared across accounts. | The real boundary between accounts is an OS user or a container, not a config directory. `layered-isolation-model.md` needs this. |
| `crates/executor/src/harness.rs` runs Claude with `--permission-mode dontAsk` and a bare `Bash` tool. Antigravity can receive `--sandbox=false`. | Requirement: an autonomous agent with shell access runs inside an isolation boundary limited to its working area. The owner's full control of the hosts makes this cheap to provide. |
| Codex `spawn_agent` cannot be disabled. Child agents use the same subscription and are invisible to the queue. CPU is measured for the leader process only. | Measurements must state their coverage. A partial number is shown as partial, never as exact. |
| The Linux restriction on Claude is Fleet's own rule, not a harness limit: `packages/coordinator/accounts.ts:18` returns 403 "Claude account is restricted to Mac and Windows; never PDS". Windows has never been set up as a host, and the executor uses Unix-only APIs. | Keep a compatibility matrix per harness and OS, as `fleet.md` already requires under compatibility facts. Review the Claude rule as a policy decision. Porting the executor to Windows is real work. |
| The scale bottleneck was the executor's serial loop and an outbox `seq` collision, not the vendor. One recovery needed a manual SQL fix. | The reliability of Fleet's own core is not yet proven. |

### Goldfish review findings (full report: `goldfish_report.md`)

All seven lenses agree that the direction is sound. They also agree on the main weakness: the brief designs for code that does not exist yet, and some of its facts were wrong or out of date. The report has 6 blockers, 14 major findings and 10 minor ones. The blockers are below. Spot-checked items are marked "verified".

1. **The cycle laboratory → selection → production → acceptance does not run in today's code.**
   * Lab runs carry no account, so Claude and Antigravity fail in the lab.
   * An experiment accepts exactly two variants, and its trials run one at a time. Verified: `packages/coordinator/app.ts:106` orders the trials.
   * The lab pins the model. Verified: `crates/executor/src/lab.rs:71` sets `gpt-6.1-sol`.
   * Nothing defines when the cycle counts as proven.
2. **The current store cannot hold media.**
   * One SQLite table holds everything, and artifacts are stored inside records as base64.
   * The executor rejects glb, mp4 and pdf.
   * A run whose artifact upload was rejected with a 4xx is still marked successful.
   * The brief has no migration path for existing data.
3. **The brief contradicts `fleet.md` in two places.**
   * Retention: `fleet.md` keeps history "without default expiry", while this brief has "delete by rule".
   * Compatibility: in `fleet.md` a confirmed incompatibility blocks launch, while this brief says harness limits never block.
   * The pinned Antigravity version `1.5.3` also blocks use and contradicts `fleet.md`.
4. **Secrets are reachable by agents.**
   * The SSH key path is passed into the agent environment through `GIT_SSH_COMMAND`. Verified: `crates/executor/src/git_access.rs:37`.
   * Claude runs as the real user with a full shell. It can read the Keychain, the personal `~/.claude` and the Fleet token.
5. **The execution is called the universal unit, but it has no single definition.**
   * The same thing is called a run, a trial, a pipeline run, a delivery and a delegation.
   * Each of these has its own set of states.
   * No rule says how fine-grained an execution is.
   * Links to project and task sit in different fields.
6. **Spending control rests on a wrong premise.**
   * Codex and Claude probably do emit rate-limit events, but this is not verified on the installed versions.
   * The executor drops these events.
   * The coordinator stores quota as `'unknown'`. Verified: `packages/coordinator/accounts.ts:18`.

Major gaps in real-world operation the brief did not cover:
* harness self-updates and silent model substitution;
* hangs that never exit;
* expired logins or a locked Keychain;
* orphaned child processes;
* Mac sleep and Windows reboots;
* Claude deleting its own transcripts through `cleanupPeriodDays`;
* the provider terms of service for automated use of consumer subscriptions.

Other major findings:
* "Cost" mixes money with use of the subscription window, and these need separate measures.
* Lab comparisons are confounded by a prompt cache shared across accounts, personal settings in `HOME`, and version changes during an experiment.
* Transcripts are untrusted data and a prompt-injection risk.

Proposed resolutions for item 3. These are the main context's proposals, open for the owner:
* **Retention.** The default stays "no expiry", as in `fleet.md`. Moving and deleting data become optional project rules that the operator turns on, and they keep a tombstone record.
* **Compatibility.** A recorded harness limitation is a fact that people or agents work around. A confirmed incompatibility of one specific combination blocks launch of that combination only, and the reason is shown. A pinned harness version is a setting, not a gate.

### Directions from the review

1. Correct the facts first: the table above and the two contradictions with `fleet.md`.
2. Add a "current implementation" section, so the engineering session starts from the real store, executor and records.
3. Define the execution once:
   * its kind;
   * its optional links;
   * one state set, with a mapping from the prototype's states;
   * its granularity: calls are spans, sub-agents are child executions.
4. Prove the ladder with one thin end-to-end slice:
   * one project and one kind of work;
   * three or more variants, each with several interleaved trials;
   * one objective check and one LLM judge;
   * the recommendation promoted as a git change;
   * one production task that stores a non-text artifact in an object store;
   * operator acceptance recorded as a human score.

   Run the slice on two hosts with one simulated rate-limit event. Defer tiers, previews, triggers, export to boards and scale targets.
5. Build spending control from data Fleet already receives: forwarded rate-limit events, token counts, and limit errors with reset times. Combine them into a labelled estimate. A reservation is an operator setting on the existing account switch.
6. Make Pipe the single gateway for connections. Add a cost record (estimated, then actual), protection against double billing on retry, immediate copying of results that come back as short-lived links, and git and `gh` credentials.
7. Make the environment the isolation boundary: one OS user or container per account or environment. This also provides CPU and memory accounting for the whole process tree (cgroup on Linux, Job Object on Windows), a clean `HOME` for comparable lab runs, and a login target for observation.
8. Collect transcripts to central storage when a run ends, and redact secrets at ingest. Give agents summaries and pointers by default, and raw parts on request, within the execution's permissions. Mark history as data, never as instructions.
9. Every production acceptance doubles as a free human score. It measures carry-over from lab to production and calibrates the LLM judges.

**Ideal Final Result:** Fleet is a thin ledger over existing products. It writes only execution records, scores, task rows and the links between them. The rest comes from things Fleet already has:
* the isolation boundary measures resources;
* the connection gateway knows which secrets to remove;
* production work keeps evaluating the current best configuration;
* the stores enforce tiers and permissions themselves.

Claims to verify before relying on them:
* Codex and Claude rate-limit events, and Claude `/usage`;
* the default of Claude `cleanupPeriodDays`;
* the status of the MinIO community edition;
* native OpenTelemetry export in Claude Code and Codex, which, if present, is preferred over parsing logs;
* whether self-hosted Langfuse fits on PDS18;
* provider terms of service for automated use of subscriptions.

### Documentation hygiene

* 11 documents and 3 evidence folders that other documents cite are missing from `main`. Among them: `multiharness-*-evidence`, `lab-live-evidence`, `antigravity-*`, `second-codex-routing`, `first-version-design`.
* Documents contradict each other on several points:
  * Concurrency caps: some documents require them, others withdraw them. The owner's rule is no invented caps.
  * Claude `--resume`: stage3 D3 forbids it, but the code uses it.
  * Execution states differ between `fleet.md` and the prototype.
  * `session-handoff.md` is stale.

### Gap 1: artifact storage

What `fleet.md` covers today: results may be held in a project's own systems, and Fleet "need not store every artifact itself". What it does not cover is where results go when no external system exists. That case applies to most images, 3D models, videos, reports and laboratory outputs.

Requirements:

1. Agents write artifacts through a Fleet tool and never receive storage credentials.
2. Each artifact links to the execution that produced it, its project and its task where one exists. An artifact without that link cannot be traced.
3. Artifacts are immutable. A new version is a new object that links to the previous one. Reviews and comparisons depend on this.
4. Each project sets its own rules: location, visibility, retention, and who can read.
5. Laboratory output is separated from production output and has its own retention, so thousands of test runs do not crowd production views.
6. Any host can write, including hosts that connect only from time to time. An upload that fails is retried, and the run is not marked successful until its required artifacts are stored.
7. Viewers need previews: thumbnails for images, streaming for video, an interactive viewer for 3D. Previews are derived from the stored object and can be rebuilt.
8. Storage has tiers: hot (recent, indexed), cold (cheap), and deleted by rule. Moving or deleting data keeps the metadata record and marks the content as removed.
9. Secrets are redacted before anything is written to history or artifacts. There is precedent: a diagnostic once printed the machine bearer token.

Candidate, not decided: an S3-compatible object store that Fleet hosts itself (for example MinIO or Garage on PDS18), or Cloudflare R2. Objects are stored under keys made from their content hash. Metadata lives in Fleet's core database. A custom file server is rejected (see Alternatives).

### Gap 2: the task object

What `fleet.md` covers today: the fields and states of a task. `state-ownership.md` also already decides that Fleet owns task state, and that GitHub or GitLab issues are optional links, not the source of truth. That decision answers the public-issues problem, but nothing implements it.

The point most often confused: **not every run needs a task, but every run needs analysis.** This means the unit of analysis must be the execution, not the task.

Requirements:

1. Every agent, tool or pipeline run produces one execution record with the same shape, whatever the reason for the run.
2. An execution carries optional owner links: project, task, experiment, and the parent execution or pipeline run. Analysis groups executions by any of these links. A laboratory run links to an experiment, not to a task.
3. A task is a production work item an operator cares about: an expected outcome, its state, its questions and its acceptance. It does not record the work's steps. Steps are the executions linked to it.
4. Tasks start flat and are linked by dependencies. Hierarchy is added later, and only if real work shows a need.
5. A person sees progress as a short summary: state, waiting reason, latest result, cost so far. They can drill down into executions. Agents read and change tasks through the same operations as people.
6. Tasks are private to the project by default. Mirroring a task to a public GitHub issue is a choice made per project and per task, and runs in one direction.
7. External tracker rate limits must never slow Fleet's internal work, because the external tracker is not on the critical path.
8. Queries by project, state, assignee and time stay fast as the history grows into millions of executions and events.

Candidate, not decided: tasks and executions as tables in the core Postgres database. Events and metrics go to an analytics store, with ClickHouse or Langfuse as candidates. The schema for traces and spans follows the OpenTelemetry GenAI conventions. Raw transcripts are collected from the harnesses' own logs (Claude Code session JSONL, Codex rollout files) rather than recreated.

### Owner answers on evaluation, accounts and operators

* **Evaluators are configurable, not fixed.** Objective checks (tests, CI, build, technical checks on assets) and judge scoring (an LLM judge or a human) both exist. Each project or experiment configures which ones it uses and how much weight each one gets. Requirement: the system records every judge's score next to the objective results, so that how far a judge agrees with people can be measured over time.
* **The laboratory runs whatever variants are configured.** The first cycle is not a fixed list of tasks. The architect selects tasks, variants and evaluators in configuration and runs them. Requirement: setting up an experiment takes only configuration, not code.
* **Accounts are mixed.**
  * Agent work runs on subscriptions: Codex, Claude and Antigravity. The system must control what they consume, even though no subscription reports its remaining quota.
  * Other services use API keys, for example Meshy for 3D, and more are expected. Fleet centralizes these keys as connections.
* **Operators:** one or two more people are expected soon. This is assumed from the owner's answer "б". Access rights per project are needed early. Interface customization per operator stays out of scope.

### Other gaps that need requirements

* **The ladder:** the number of repeated trials needed to separate two variants, a token budget kept apart from production quota, and protection against overfitting to a small task set.
* **Spending control on subscriptions:** no subscription exposes an API that reports remaining quota.
  * Fleet estimates the remaining quota from its own usage records, rate-limit errors and response headers. The estimate is shown as an estimate.
  * An operator can mark an account "reserve for urgent work" or move load to another account, and the system follows that setting. These are operator settings, not built-in caps.
  * When an account runs out mid-task, the work continues on another account through handoff. The flow is designed but not yet tested (T7).
* **Centralized API services (Meshy and others):**
  * Each service is a connection with its own credentials, limits and money spent.
  * Agents call the service through a Fleet tool and never receive the key.
  * Every call is recorded against its execution, so cost per project and per task includes these API charges.
* **Triggers:** an incoming event, such as an error report, starts an agent that prepares a fix plan. No requirement exists for event sources, deduplication or routing.
* **Agent access to history:** agents read history within the requester's permissions. This needs a query tool that does not load raw transcripts into context by default.

## Goals vs. Non-Goals

* **In-Scope Goals:**
  * Requirements for artifact storage and for the task/execution split.
  * A map of real-world constraints recorded as facts about each harness.
  * A storage approach built from existing products.
  * One full cycle proven: laboratory → selection → production → acceptance.
* **Out-of-Scope Non-Goals:**
  * Interface customization per operator, until there is more than one operator.
  * Invented concurrency or admission caps.
  * A custom database, tracker or object store.
  * Choosing storage products before the engineering session.

## Alternatives Weighed

* **GitHub or GitLab issues as the task store.** Rejected by the author:
  * issues are public on a public repository;
  * GitHub applies API rate limits;
  * the tracker would become a second source of truth.
  
  Kept as an optional mirror.
* **A self-hosted tracker (Plane, Forgejo or GitLab issues) as the task store.** Rejected by author: Fleet keeps tasks in its own central tables and builds its own view. The view is not complex. It needs smooth scrolling that does not jump, standard decision blocks, and text questions where a person answers or picks an option. A project can later add export to a board in its settings.
* **Task hierarchy (subtasks, epics) from the start.** Rejected by author: start with flat tasks plus dependencies, and refactor if real work shows a need.
* **A custom artifact file server.** Rejected: S3-compatible stores already provide versioning, lifecycle rules and access policies.
* **A task for every run, including laboratory runs.** Rejected by the author: thousands of test runs would clutter the work views. The execution is the unit of analysis instead.

## Key Decisions & Staging for Next Steps

* Harness limitations are recorded facts. They never block use of the system.
* The execution is the universal unit of analysis. A task is optional and covers production work only.
* Fleet owns task state in its own central tables. External issues and boards are optional exports, configured per project.
* Tasks start flat, linked by dependencies. Hierarchy is added only when real work needs it.
* The task view stays simple: smooth scrolling without jumps, standard decision blocks, text questions with free answers or choices.
* Artifacts and history are stored on existing products, with project-level rules, tiers and redaction.
* Goldfish review starts only after the owner confirms it, once the remaining code is merged.
* Evaluators and laboratory runs are configured, not hard-coded. Objective checks and judge scores sit side by side.
* Agent work runs on subscriptions (Codex, Claude, Antigravity), and Fleet controls their consumption. Third-party APIs such as Meshy are centralized as connections that carry cost.
* W4 (per-execution git identity and credentials) is merged on `main`.
* Accepted by owner, retention: the default is no expiry, as in `fleet.md`. Moving and deleting data are optional project rules, and a deletion keeps a tombstone record.
* Accepted by owner, compatibility: a harness limitation is a recorded fact and never blocks work. Only a confirmed incompatibility of one specific combination blocks launch, and only for that combination, with the reason shown. A pinned harness version is a setting, not a gate.
* Accepted by owner, delivery approach: prove the ladder with one thin end-to-end slice (Directions from the review, item 4) before widening scope.
* Accepted by owner, next steps (Directions from the review):
  * correct the facts;
  * add a "current implementation" section;
  * define the execution once;
  * make the environment the isolation boundary;
  * make Pipe the connection gateway;
  * verify the quota signals and the provider terms before designing spending control.
