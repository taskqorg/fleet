# Goldfish Brainstorm Report: Fleet concept brief

**Mode:** BRAINSTORM
**Primary target:** `/Users/kirs/orca/workspaces/fleet/shad/concepts_brief.md`
**Also read:** `fleet.md`, `docs/` (state-ownership, capabilities-and-pipelines, work-analysis, experiments, work-allocation, connections, layered-isolation-model, multi-account-same-env, interfaces, resources-and-auth-model, README, and others named below), plus code in `crates/executor`, `packages/coordinator` and `evidence`.
**Date:** 2026-11-09

## Reviewer status

| Reviewer | Role | Status |
| --- | --- | --- |
| `egm-design` | Skeptical Architect Brainstormer | completed |
| `egm-codebase-details` | Codebase Reality Brainstormer | completed |
| `egm-zoom-out` | Simplicity & Scope Brainstormer | completed |
| `egm-comprehension` | Naive Reader Brainstormer | completed |
| `egm-implementation-readiness` | Implementation Readiness Brainstormer | completed |
| `egm-structural` | Document Structure Brainstormer | completed |
| `egm-inventive-catalyst` | Inventive Catalyst Brainstormer | completed |

All seven lenses completed on their first run. After deduplication: **6 Blockers, 14 Major, 10 Nits.**

Every entity or storage idea below is a candidate, not a decision. No finding proposes a built-in cap. Where a limit, budget or reservation comes up, it is an operator-configured setting with no default.

---

## Critique first: overall assessment

The direction is right, and all seven lenses agree on that. Making the execution the unit of analysis, keeping tasks optional, flat and in Fleet's own tables, building on existing storage products, and recording harness limitations as facts are the simple, Occam-minimal choices. The brief is also honest that the core hypothesis is unproven: lab selection finds better configurations, and the winners carry over to production.

There are four main weaknesses.

1. **The brief designs against a codebase that does not exist yet, and against facts that are partly wrong.**
   - The current store is one SQLite table of JSON records, with artifacts kept inline as base64.
   - The executor rejects 3D and video files.
   - The lab cannot run Claude or Antigravity variants.
   - Production delivery is pinned to one Codex profile.

   So the lab, selection, production and acceptance cycle cannot run today. On facts, several items in the "real-world constraints" table are Fleet's own policy or out of date, not harness facts:
   - Claude on Linux is a Fleet rule.
   - The Codex sub-agent switch exists.
   - Quota signals probably exist.

2. **The brief contradicts canonical `fleet.md` without saying so.**
   - Retention: `fleet.md` keeps everything with no default expiry, while the brief deletes "by rule".
   - Compatibility: `fleet.md` says a confirmed incompatibility blocks launch, while the brief says limitations never block.

3. **Vocabulary and entities are multiplying.**
   - Run, execution, trial, pipeline run, delivery, delegation and step are all "something executed".
   - "Task" means both a production work item and a lab test case.
   - "Connection" and "subscription" are defined differently in the brief and in `connections.md`.

   The central decision, execution as the universal unit, has no single owning definition: no state set, no granularity rule, no list of links.

4. **The ladder has no exit test.** No criterion says what "one full cycle proven" means. The lab is not connected back to production outcomes. And the lab shares subscription windows with production, so a "separate lab budget" can only be an operator reservation.

The real cost and load are not in query rate. They are in subscription usage windows, human judging time, media and transcript volume on a single storage host, and secret exposure on hosts where agents have full shells.

Suggested overall direction: build one thin vertical slice that proves the ladder end to end, using records Fleet already has, and borrow everything else from existing products.

---

## Prioritized findings

### Blockers

#### B1. The lab-to-production cycle cannot run in today's code, and its success test is undefined
- **Lenses:** codebase-details (code facts), design, zoom-out, structural, inventive-catalyst, comprehension, implementation-readiness
- **Scope:** brief "Assessment", goal "One full cycle proven" (line ~126), "The ladder"; `packages/coordinator/lab.ts:19-21`; `harness.rs:41-46,88-90`; `delivery.ts:26,44`; `app.ts:106`
- **Problem:**
  - Experiment commands carry no account. Lab Codex runs fall back to the ambient `FLEET_CODEX_HOME`, which is outside managed accounts, so the account enable/disable switch has no effect on them. Claude and Antigravity profiles fail with "named account required".
  - `/api/experiments` requires exactly two profiles, runs trials one at a time, and always uses the coordinator's own host.
  - `delivery_generate` refuses any profile other than Codex `gpt-6.1-sol` at `low` effort, with HTML/CSS output only, 1 MiB at most. A lab winner therefore cannot reach production.
  - The spec has the same gap: no pass/fail criterion, and production acceptance does not feed back into the lab.
  - The brief never uses `fleet.md`'s word "recommendation", which is the actual promotion mechanism. Readers cannot tell who picks the winner, what is promoted, at what scope, or whether promotion is automatic.
- **Recommendation:**
  - Name these as the first gaps.
  - In code: route lab runs through `enqueue` and `managed.resolve` as production does (`app.ts:78-89`). Remove the two-variant limit and the serial trials. Remove the delivery profile pin and reuse `project-recommendation` (`projects.ts:64`) together with the method's `agentProfile` (`delivery.ts:18`).
  - In the spec, write the cycle as named records: experiment → trials (executions) → evaluator scores → assessment → architect changes the recommendation for (kind of work, scope) → production executions resolve it → acceptance. Promotion is never automatic unless that authority is delegated (`fleet.md:188`).
  - Add a falsifiable exam. Candidate: the challenger beats the champion in paired lab trials, and its production acceptance and rework rates over N tasks are no worse. N is a per-experiment setting.

#### B2. Artifact storage starts from a store that cannot hold media, and no migration path is stated
- **Lenses:** codebase-details, zoom-out, implementation-readiness
- **Scope:** brief Gap 1 and Gap 2 candidates; `packages/coordinator/app.ts:19-23,57,110`; `projects.ts:54`; `delivery.ts:11,48`; `crates/executor/src/main.rs:680-688,768-787,802`; `outbox.rs:59-83`
- **Problem:**
  - All state sits in `records(kind,id,data)`. Artifacts are stored inline as `contentBase64`, with a default limit of 16 MiB. `all(kind)` parses every row of a kind, and the Pipe wait loop calls `all('artifact')` every 200 ms.
  - The executor uploads only an allow-list of types (html, css, js, json, svg, png, jpg, webp, ico, txt, woff2). Any other file under `output/` (glb, gltf, mp4, pdf, md, zip) fails the whole execution. Upload happens only once, at process exit.
  - If the coordinator rejects an artifact with a 4xx, the outbox sets it aside, and the later `completed` receipt still arrives. The run shows success with its artifact missing. The size limit is also configured twice, in two settings that can disagree.
  - The store has no schema version and no migrations. The brief never says whether existing executions, artifacts and the ids cited in evidence survive the move.
- **Recommendation:**
  - Add a "current implementation" section to the brief.
  - First storage step: move content out of `records` (metadata row, content hash, blob key). Keep the `artifact:<executionId>:<path>` identity and the 409 conflict rule as the basis for immutability.
  - Replace the type allow-list with type detection. Stream large files and allow uploads during the run. Keep the `output/` drop folder: agents already never see storage credentials there.
  - Decide whether prototype data is disposable or migrated. If migrated, keep ids so evidence links still resolve, map the existing sha256 values onto content keys, and keep the old store read-only until the new one has been checked against it.

#### B3. The brief contradicts canonical `fleet.md` on retention and on compatibility blocking
- **Lenses:** comprehension, structural, design, zoom-out, codebase-details, inventive-catalyst, implementation-readiness
- **Scope:** brief Gap 1 req. 8 (line ~73, "deleted by rule"), lines 13 and 148 ("never blockers"); `fleet.md:180` ("retain every execution in full, without default expiry") and `fleet.md:182` ("Confirmed incompatibility blocks launch"); `crates/executor/src/antigravity.rs:75`
- **Problem:**
  - A reader cannot tell which document wins.
  - Some brief "facts" are really self-imposed blockers:
    - "Claude refuses to run on Linux" is Fleet's own 403 account policy (`harness.rs:140`, `accounts.ts:6,18,19`, `resources-and-auth-model.md`). The harness itself runs on Linux.
    - `harness.rs:140` also rejects Windows, although `accounts.ts` allows `win32`.
    - The executor refuses any Antigravity runtime other than exactly `1.5.3`, treating an unchecked version as incompatible. That contradicts `fleet.md`'s own "unchecked is visible, not incompatible".
- **Recommendation:**
  - Ask the owner to settle both points, and write each one into `fleet.md` as an explicit change.
  - Retention candidate: full retention by default; a project may configure tiering or deletion; metadata survives deletion. State whether this covers artifacts, history or both.
  - Define three categories once in `fleet.md`:
    - **Limitation:** recorded; work launches anyway.
    - **Incompatibility:** the combination physically cannot run; it is filtered out of placement with the reason shown, and work stays queued or routes elsewhere.
    - **Operator or owner policy:** configured, visible and reversible. Account gates and Claude-on-Linux go here.

    Compatibility facts then feed eligibility in work allocation, with an operator override. There is no separate launch gate.

#### B4. Secrets can reach agents that run with full capabilities
- **Lenses:** inventive-catalyst, codebase-details
- **Scope:** brief Gap 1 req. 1 and req. 9; constraints table (real `HOME`, `--permission-mode dontAsk` with a bare `Bash`); `git_access.rs:26-28,37`; `main.rs:347-348`; `delivery_worker.py:103,117,123-132`; `fleet.md:139`
- **Problem:**
  - W4 puts the SSH key path into the agent's own environment through `GIT_SSH_COMMAND`. The agent runs as the same OS user, so it can read the key.
  - The delivery worker uses ambient git and `gh` credentials.
  - Claude runs as the real user with an unrestricted shell, so it can reach the Keychain, the personal `~/.claude`, other accounts' files and Fleet's bearer token. The past token leak is the precedent.
  - Under maximal capabilities, "agents never receive credentials" cannot hold, so redaction is the only defence.
- **Recommendation (candidate):**
  - Separate in space: one OS user or container per account or environment. It is the credential boundary and the working-area boundary at once. The agent keeps every tool inside it, and humans observe by logging in as that user.
  - Record one exception as a fact: a harness subscription token has to live inside the boundary. Every other secret (API connections, Fleet's token, git and `gh` credentials) stays in a broker outside it.
  - Fleet issues every secret it hands out, so it can register their exact values and scrub them before writing; pattern-based redaction stays as a backstop.
  - Bring git and `gh` credentials into the same connection model.

#### B5. The execution is the universal unit but has no single definition
- **Lenses:** implementation-readiness, design, zoom-out, comprehension, structural, codebase-details
- **Scope:** brief Gap 2 (lines ~84-95), "Documentation hygiene"; `fleet.md:104,114,186-188`; `experiments.md`; `capabilities-and-pipelines.md`; code at `app.ts:85`, `lab.ts:20-21`, `delivery.ts:47,57`, `projects.ts:52,56`, `delegation.ts:22`, `agent-tools.ts:169`
- **Problem:**
  - **Duplicate names:** run (`fleet.md:186`), execution, trial, pipeline run, step, delivery, delegation child and session overlap. Line 86 says a pipeline run is an execution; line 87 says "parent execution or pipeline run".
  - **No agreed states:** `fleet.md` has preparing, running, waiting, succeeded, failed and cancelled. The prototype has queued, completed, failed and stopped. This becomes a stored, queried contract with no mapping for legacy records.
  - **No granularity rule:** if every tool or Meshy call becomes an execution, row counts explode.
  - **Scattered owner links:** in code, `projectId` sits on program-worker executions but otherwise only on the session. Experiments, pipeline runs, deliveries and delegations each use their own field. Pipe calls made in-process create no execution at all.
  - **No home doc:** task states get a table; execution states get one sentence.
- **Recommendation (candidate):**
  - One execution record with a `kind` and optional links: parent, project, task, experiment, variant, config revision. Then:
    - a trial is an execution linked to an experiment and a variant;
    - a pipeline run or delivery is a parent execution, and its steps are child executions;
    - tool and API calls are events or spans inside an execution;
    - Codex child agents are child executions discovered from rollout files and marked "observed, not launched".
  - Decide the state set and its legacy mapping.
  - Put an execution-states table and the link list in `fleet.md` § Agent execution.
  - Frame Gap 2 req. 1 as "unify the existing fields", not "design from scratch".

#### B6. Spending control has no acceptance criteria, and its premise ("no quota signal") is probably wrong
- **Lenses:** implementation-readiness (Blocker), design, zoom-out, codebase-details, inventive-catalyst (Major)
- **Scope:** brief "Spending control on subscriptions" (lines ~108-112); `accounts.ts:15,18`; `harness.rs:367-396,380,463`; `codex.rs:192-197,212`; `main.rs:925-952`; `projects.ts:58`
- **Problem:**
  - Reviewers believe two signals exist, neither verified on the installed versions:
    - Codex rollout and `token_count` and app-server events carry `rate_limits` (percent used, window length, reset time).
    - Recent Claude Code emits rate-limit events and `/usage`.
  - The executor captures neither. `codex.rs:212` drops unmapped app-server messages, so they survive only in host-local `*.native.jsonl`.
  - Quota is the hard-coded string `'unknown'`. `usage.observation` events exist, with a coverage label, but nothing sums them per account.
  - The only limit detection is a regex on 429/432/433 or the word "quota", and only for Pipe.
  - "Response headers" are mentioned, but harness CLIs do not expose HTTP headers.
  - The quotas are rolling windows that depend on the model, so one "remaining balance" figure is the wrong model.
  - "Reserve for urgent work" depends on an urgency attribute that is never defined.
- **Recommendation:**
  - Build a per-harness signal inventory: what output means "limit hit", where the reset time comes from, and which numbers are estimates.
  - Forward and store rate-limit messages as observations with their source.
  - Make the estimate a view over existing data (token counts, harness percentages, limit errors with reset times), shown per window and per model and labelled "estimate". No new entity is needed.
  - Define account availability: available, limited until T, unknown.
  - Acceptance tests:
    - an injected limit error marks the account limited until T instead of failing the execution;
    - queued work follows the operator's routing setting;
    - after T the account is eligible again with no manual step;
    - handoff (T7) is shown working once.
  - Reuse the account enable/disable switch and its audit trail (`accounts.ts:16`) as the basis for reservations.

### Major

#### M1. Uncovered real-world harness operation problems
- **Lenses:** design, zoom-out, implementation-readiness, inventive-catalyst
- **Scope:** brief "Real-world constraints" table and gaps; `fleet.md` execution states; `readiness.rs:52`; `multi-account-same-env.md`
- **Problem:** the brief does not cover:
  - **Version and model drift:** Claude Code updates itself, Codex and agy update through package managers, and model aliases move. This changes prompts, tools and log formats mid-experiment and breaks parsers.
  - **Stalls:** a live process that waits on a trust or permission prompt, a "usage limit reached, resets at X" message or a login expiry. "Lost contact" does not cover this.
  - **Auth:** expiry mid-run, refresh races on one account, a locked macOS Keychain under SSH or launchd. Only a pre-launch `login-absent` check exists.
  - **Crashes:** no taxonomy for a harness or executor killed mid-turn or a host reboot. One recovery already needed a manual SQL fix, and the outbox stalls on one bad row.
  - **Process trees:** orphaned `spawn_agent` children, MCP servers and dev servers. Windows needs Job Objects for kill and CPU totals.
  - **Host power:** Mac sleep and Windows Update reboots.
  - **Disk:** worktree and disk growth; worktree removal is not implemented.
  - **Transcript lifetime:** Claude Code may prune its own transcripts after `cleanupPeriodDays` (reviewers believe the default is 30). Fleet runs with `--setting-sources ""`, so that setting may not be changeable. Antigravity's transcript location and lifetime are undocumented.
  - **Prompt injection** from repository content and history, given `dontAsk` plus `Bash`.
  - **Terms-of-service risk:** using consumer subscriptions for automated parallel agents may break provider terms and get accounts banned. This threatens the subscription premise itself.
- **Recommendation:** add one compatibility-matrix row per item, as recorded facts.
  - Pin harness versions per environment; an update is an explicit operator action, with an automatic requalification run. An unqualified version still runs, with a visible "unqualified" fact.
  - Record the resolved model ID, the harness version and the parser version on every execution.
  - Make "no progress since T" a visible observation. Any timeout is an operator or project setting.
  - Turn auth, Keychain, dialog and disk checks into readiness facts that placement routes around.
  - Define failure classes (auth, rate limit, harness crash, executor crash, lost contact, unknown), each with one acceptance test: no manual DB edits, no repeated external action without a confirmed outcome, and the outbox survives one bad row.
  - Copy harness transcripts into Fleet storage at execution end, with a checksum.
  - Rerun the failure-class tests as a qualification suite on every harness upgrade.

#### M2. Windows is a declared host, but the executor cannot be built for it
- **Lenses:** codebase-details, implementation-readiness
- **Scope:** brief Overview and constraints; `main.rs:15,422,788,811,818,927`; `native.rs:2,119-141`; `codex.rs:4`; `worktree.rs`, `lab.rs`
- **Problem:**
  - The executor uses Unix-only APIs: `std::os::unix`, `flock`, signals, `/bin/ps`, `/usr/bin/du`, `proc_pidinfo` and `pidfd`. That is about 29 uses.
  - `env_clear` on Windows removes `SystemRoot`.
  - Not examined: Windows credential storage, path-length limits, Codex sandboxing on Windows, and whether WSL counts as Linux (which matters for the Claude policy).
- **Recommendation:**
  - Add "port the executor to Windows" as an explicit item.
  - Decide native or WSL for each harness.
  - Acceptance on the first Windows host: the executor builds and registers, each permitted harness completes one turn, account isolation is checked, and the compatibility matrix is filled in.

#### M3. In code, agents do not get maximal capabilities, and code-level caps exist
- **Lenses:** codebase-details; isolation-versus-capability wording from comprehension, structural and zoom-out
- **Scope:** `native.rs:95-111`; `codex.rs:112`; `harness.rs:231-247`; `lab.ts:9,11,21`; `delegation.ts:15,18`; `app.ts:86,106`; brief line 14 against line 46
- **Problem:**
  - Restricted capabilities:
    - Codex runs with network off and the `workspace-write` sandbox, and with image_generation, browser_use, computer_use, apps and plugins disabled.
    - Claude may use only Read, Edit, Write, Bash, Glob and Grep.
    - Profiles accept only the `design-rules` MCP server.
  - Fixed process rules in code: trials run serially; a delegation allows one child (409) and a child cannot delegate (403); an experiment must have exactly two variants; the default instructions say "Do not spawn subagents."
  - The brief says both "strongest setup" (line 14) and "isolation boundary limited to its working area" (line 46). A newcomer reads that as a contradiction.
- **Recommendation:**
  - List the restrictions as current state to lift through profile settings.
  - List the code-level caps next to the doc-level ones in the hygiene section.
  - State once that isolation protects other accounts and the owner's personal data and takes no tools away. Make that a numbered requirement owned by `layered-isolation-model.md`.

#### M4. Artifact store design: dedupe, purge, redaction point, redundancy and upload path
- **Lenses:** design, inventive-catalyst, zoom-out, implementation-readiness, structural
- **Scope:** brief Gap 1, requirements 3-9 and the candidate paragraph
- **Problem:**
  - **Dedupe conflicts with project rules:** global content-hash dedupe conflicts with per-project visibility and retention, since one object can belong to projects with different rules.
  - **Immutability conflicts with removal:** an "immutable" object that is later found to contain a secret must still be removable.
  - **Redaction point:** redaction cannot happen at the source, because harnesses write raw transcripts to the host first.
  - **Redundancy and backup:**
    - A single-node MinIO or Garage on PDS18 has no redundancy.
    - Reviewers report that MinIO's community edition has been cut back; this needs checking.
    - Postgres has no backup requirement.
  - **Upload path:** 100-500 MB video and 3D files cannot pass through MCP JSON-RPC or the event outbox.
  - **Success tied to storage:** "not successful until stored" (req. 6) ties execution success to storage reachability, against `fleet.md:192`. It also blocks hosts that connect only intermittently.
  - **Premature requirements:** previews and hot/cold tiers (req. 7-8) come before there is evidence they are needed.
- **Recommendation (candidate):**
  - **Keys and dedupe:** prefix keys by project and by lab or production, for example `project/{p}/{lab|prod}/sha256/...`, and dedupe within a project only. The store's own lifecycle rules then handle tiers and lab retention, with no Fleet code.
  - **Purge:** delete the content and keep a tombstone in the metadata store.
  - **Redaction:** redact at ingest, by exact value (see B4), and record a retention rule for raw files left on hosts.
  - **Uploads:** agents drop files into `output/`, or a tool takes a local path. The executor uploads with resumable multipart uploads or short-lived scoped presigned URLs, from a local spool. State that a presigned URL is not the agent-credential exception.
  - **State:** "produced, upload pending" is a separate result status, not a condition on execution success.
  - **Redundancy:** add backup, restore-drill and off-site requirements, for example Postgres WAL archiving and R2 or B2 as an off-site tier. Compare R2 with self-hosting on these criteria, not only on cost.
  - **Acceptance binding:** acceptance binds to one immutable artifact version, and rework creates a linked new version.
  - **Previews:** browser-native first (image thumbnails, HTTP range requests for video, `<model-viewer>` or three.js for glTF). Treat preview generation as ordinary observable work.
  - **Code results:** these stay in Git.
  - **Sizing, to be measured:** a 3D model with textures is about 10-50 MB, so a 400-trial 3D experiment is on the order of 4-20 GB.

#### M5. Centralized third-party API connections: reuse Pipe, and handle async, delayed and uncertain charges
- **Lenses:** zoom-out, codebase-details, design, implementation-readiness, inventive-catalyst
- **Scope:** brief "Centralized API services" (lines ~113-116); `projects.ts:11-13,48-59`; `pipe_search.py`; `connections.md`; `capabilities-and-pipelines.md:41`
- **Problem:**
  - Pipe already matches the Meshy model: the key stays on the host, agents call a Fleet operation, and the call records `requestedBy`. It has no cost or limit fields.
  - Meshy-style APIs work as submit, poll, then download:
    - the charge is known only at the end, or only partly for failed jobs;
    - download URLs expire;
    - a retry after a timeout can bill twice;
    - prices change over time.
  - Wrapper tools always lag the provider's API, which conflicts with maximal capabilities.
  - It is undefined which host serves each connection: if it is only PDS18, every agent depends on PDS18 being reachable.
- **Recommendation (candidate):**
  - Extend Pipe rather than build a new key store. For LLM APIs, consider an existing gateway such as the LiteLLM proxy.
  - Record the intended call against the execution before sending it. Afterwards record the outcome and the cost, as estimated then actual, with its unit and a versioned price. Use idempotency keys; an uncertain outcome does not authorize a repeat.
  - Copy results into artifact storage immediately.
  - Consider a forwarding proxy that injects credentials, so agents call the real provider API through a local endpoint with no key, and every charge lands on an execution.
  - Record which host serves each connection as a fact.
  - Spending limits on paid connections are owner-configured settings.

#### M6. "Cost" mixes real money with subscription consumption
- **Lenses:** design, implementation-readiness
- **Scope:** brief Gap 2 req. 5 ("cost so far"); `work-analysis.md`
- **Problem:** subscription work costs window capacity, not marginal money. Adding subscription usage to Meshy money in one figure makes the ladder optimise the wrong thing.
- **Recommendation:** report three measures:
  - consumption: tokens and share of window, per account;
  - money spent on paid connections;
  - optionally, an API-equivalent price that is clearly labelled as imputed and says how it was computed.

#### M7. Lab validity: confounders, judge bias and the human-judging bottleneck
- **Lenses:** design, implementation-readiness, inventive-catalyst, zoom-out
- **Scope:** brief "The ladder" (line ~108); `experiments.md:9-11,17,21-29,35-37`
- **Problem:**
  - **Confounders:**
    - A prompt cache shared across accounts lets a later variant inherit a warm cache.
    - Personal `CLAUDE.md`, plugins, hooks and MCP servers in the real `HOME` silently join every variant.
    - Harness and model versions can change mid-experiment.
  - **Judging:** judges from the same model family as the workers are biased. With one to three operators, human judging is the real limit.
  - **No evidence rules:** nothing says what "insufficient evidence" looks like, and there is no held-out set.
  - **Shared quota:** the lab uses the same subscription windows as production.
- **Recommendation:** keep every threshold per-experiment and configurable, but require:
  - interleaved or randomized trial order;
  - cached and uncached tokens recorded separately;
  - a snapshot of the effective configuration, plugins and harness version on each trial;
  - per-trial values, their spread, and an explicit "insufficient evidence" verdict;
  - held-out cases, plus a rolling holdout refreshed from recently accepted production executions;
  - the judge's configuration version recorded, with its agreement with humans measured where humans scored;
  - blind pairwise human review on a sample;
  - a pre-run size estimate (cases x variants x trials, plus judge cost) shown against the window estimate.

  Cheaper options (inventive):
  - run lab work on window capacity that would otherwise expire at reset, as an operator priority;
  - use recorded champion production runs as the baseline arm;
  - use sequential stopping;
  - run an operator-configured canary on selected production tasks, opted in per project.

#### M8. Task vs execution: overlapping task-like objects and derived state stored twice
- **Lenses:** codebase-details, zoom-out, comprehension, structural, inventive-catalyst
- **Scope:** brief Gap 2; `fleet.md` task states (lines ~72-82); `delivery.ts:14,22`; `projects.ts:65-66`; `agent-tools.ts:121-130`; `experiments.md:7`
- **Problem:**
  - `delivery` is already task-like: it has a project, task text, state, a question, steps linked to executions, an account and a method. There are also two question systems, `project-question` and the agent `question`. Adding a new task table without saying how it relates to these creates three overlapping objects.
  - The task states Queued, In progress and Waiting can be derived from the task's executions and open questions. Storing them twice is where the documented state mismatch comes from.
  - "Task" also names the lab test case (lines 9, 100 and 108), which undercuts the key decision that tasks are production-only.
  - "Production" is never defined.
  - Acceptance of results that have no task, such as an image made in a PM session, is undefined.
- **Recommendation (candidate):**
  - Decide whether the task replaces delivery, wraps it, or merges with it, and merge the two question systems.
  - Store only operator intent and the outcome on the task (open, paused, cancelled, accepted, failed). Compute the rest.
  - Call the lab unit an "assignment" or "case": a versioned file in git with its inputs stored as artifacts, never a task row. A replay links through the source execution, which carries the task link.
  - Define production as, for example, "an execution with no experiment link", and use that rule everywhere.
  - Decide whether a result with no task can be accepted, or whether accepting it creates a task.
  - Settle the TaskQ board transition: board work stays where it is until migrated, new work becomes Fleet tasks, and mirror failures are recorded facts.

#### M9. Evaluators, acceptance, questions and recommendations can collapse into fewer mechanisms
- **Lenses:** zoom-out, inventive-catalyst, comprehension
- **Scope:** brief "Owner answers on evaluation" (line ~99); `fleet.md:186-188`, Questions and acceptance; `experiments.md:21-29`; `capabilities-and-pipelines.md`
- **Problem:** objective checks, LLM judges, human judges, operator acceptance, questions, assessments and recommendation records are each separate concepts. "Evaluator" is not connected to `fleet.md`'s "assessment" or "criteria".
- **Recommendation (candidate):**
  - An evaluator is a method: a program, a judge agent, or a question to a human.
  - Its output is a score attached to an execution: name, value, source, procedure version.
  - Operator acceptance is a human score on the production execution, which gives judge-versus-human agreement and a held-out signal for free.
  - Task acceptance is derived from the latest accepted execution.
  - The assessment stays a conclusion over trials. Weights are optional.
  - The production default can be a project setting in git, with promotion as a commit or PR that links the assessment. Git then provides the audit trail, diffs and rollback.
  - Keep Capability, Method and Tool as vocabulary until a second method exists for some capability.

#### M10. Agent access to full history: untrusted content, permissions and storage shape
- **Lenses:** design, implementation-readiness, codebase-details, inventive-catalyst, comprehension, zoom-out
- **Scope:** brief "Agent access to history" (line ~118), Gap 2 candidate (line ~95); `fleet.md:197-198`; `work-analysis.md`; `main.rs:417-428,648`; `.gitignore:16-19`; `harness.rs:317-366,463`; `agent-tools.ts:197-212`; `lab.ts:19`
- **Problem:**
  - **Untrusted content:** transcripts contain web pages, third-party output and earlier agent text. An analyst agent with full host access that reads them can be steered by injected instructions, and reading raw history burns quota.
  - **Whose permissions:** "requester's permissions" means nothing for triggered or autonomous work, and lab visibility is undefined.
  - **Where history lives today:**
    - Raw streams stay in each host's session folder and are gitignored, which explains part of the missing evidence.
    - The coordinator stores each native event twice (the event plus `fleetNormalized.native`).
    - Ingest does no redaction.
    - Agent tools are scoped to the agent's own session, and `/api/lab/executions` returns production executions too.
  - **Analytics stack:** Langfuse v3 brings ClickHouse, Redis and S3. Storing streamed token deltas would overload the outbox. Log formats and the OpenTelemetry GenAI conventions are unstable.
- **Recommendation (candidate):**
  - Give agents read-only aggregate and query tools over views filtered by permission; Postgres row-level security fits.
  - Return summaries and pointers by default, with raw excerpts only on request, and mark history content as data in tool output.
  - Run every analysis under the execution's effective permissions (`fleet.md:197`).
  - Store transcripts unchanged in the object store, with one import adapter per harness version so they can be re-parsed.
  - Do not store streamed token deltas.
  - Use OpenTelemetry GenAI as an export mapping, not as the storage schema.
  - Start from the existing normalized tool-call view.

#### M11. Fleet's own observability is thin
- **Lenses:** codebase-details
- **Scope:** `app.ts:25,58,106`; `main.rs:661-668,853,873,939`; `outbox.rs:104`
- **Problem:**
  - Fastify runs with no logger, failures print generic `eprintln` messages, and no trace ID exists.
  - `du -sk` runs over the whole data folder every 2 s for each live execution, and the result is not per execution.
  - There is no outbox depth or loop-lag health data.
- **Recommendation:** put `commandId`, `executionId` and `sessionId` into every log line and outbox row. Report outbox depth, the age of the oldest row and the `outbox_failed` count in the host heartbeat.

#### M12. No per-person identity, while operator access is a near-term need
- **Lenses:** codebase-details, inventive-catalyst, zoom-out
- **Scope:** `app.ts:15,34`; `projects.ts:66`; `accounts.ts:11`; brief "Operators" (line ~104); `fleet.md:198`
- **Problem:**
  - All browsers share one secret, and `answeredBy` is always `'operator'`.
  - Account preference is hard-coded to two hello-world projects.
  - "Observe any agent" through host login would show what project grants hide.
- **Recommendation:**
  - Add real user identity before per-project grants.
  - Host login and "observe any agent" belong to the owner and architect role.
  - Operators watch through Fleet's stream, under the same project grant as history.
  - Use the project-grant intersection already in `fleet.md`, with two roles. No separate role-based access framework.

#### M13. Scope is wider than the core hypothesis needs
- **Lenses:** zoom-out, implementation-readiness
- **Scope:** brief Gap 1 items 7-8; "Other gaps" (triggers, ladder extras); Gap 2 item 6
- **Problem:** previews, tiers, public GitHub mirroring, trigger routing and dedup, and "millions of executions" all get attention before a single cycle exists.
- **Recommendation:**
  - Mark these as "later, when evidence demands".
  - For triggers, one inbound webhook that calls the existing create-task operation is enough. Add one acceptance example: the same error report arriving twice produces one plan.
  - Postgres-only, or even indexed SQLite, is the Occam baseline to rule out before adding ClickHouse or Langfuse (see Divergences).

#### M14. Document structure: the brief cannot be merged as it stands
- **Lenses:** structural, comprehension
- **Scope:** whole brief; `docs/README.md`; `layered-isolation-model.md`; hygiene section (lines ~53-58)
- **Problem:**
  - The brief has no Risks & Mitigations section and no success measures, cost or timeline (Heilmeier).
  - Decisions sit in three places ("Owner answers", "Alternatives", "Key Decisions"), mixed with status notes.
  - Only Gaps 1-2 have requirement lists. Spending control, connections, the ladder, triggers and history get one bullet each.
  - The hygiene section lists contradictions without locations. Reviewers found them:
    - still proposing caps or admission limits: `recovery-milestone.md:20`, `onboarding-handoff.md:43`, `distributed-contracts-proposal.md:46`;
    - withdrawing them: `stage3-architecture-findings.md:47`;
    - operator-set concurrency settings, which are allowed: `work-allocation.md:22,30`.
  - `docs/README.md` leaves out `layered-isolation-model.md`, `multi-account-same-env.md`, `agent-tools-inventory.md` and `stage3-architecture-findings.md`. The first of these mixes canonical model, evidence and an implementation log.
  - About 11 cited docs are missing.
  - Account, binding, connection and environment are defined in four docs, and no doc owns them.
  - The candidates are never compared: MinIO, Garage or R2; ClickHouse, Langfuse or Postgres-only.
- **Recommendation:** split the brief by destination rather than merging it whole:
  - decisions go to `fleet.md`;
  - each gap's requirements go to the doc that owns that concept;
  - evidence and the assessment go to a dated review under "Evidence";
  - candidates and open questions go to one decision register, each tagged Decided, Candidate or Withdrawn.

  Also:
  - Add a glossary that maps each term to its `fleet.md` term.
  - Add a list of every place the brief changes `fleet.md`.
  - Add one worked example: an asset experiment changes a recommendation, then a production task uses it, calls Meshy, stores a 3D artifact and is accepted.
  - Give every gap the same template: covered today, requirements, candidate, owning doc.
  - New owning docs as candidates: `docs/artifacts-and-history.md` for storage, tiers, redaction, previews and history access. Spending, cost and connections go to `connections.md` and `work-allocation.md`. Evaluators and production validation go to `experiments.md`.
  - State once in `fleet.md`: operators may set limits; the system has no built-in caps.

### Nits

| # | Item | Lens |
| --- | --- | --- |
| N1 | The owner answer "б" in the Operators bullet is untranslated; state the decision itself. | design, comprehension, structural, readiness |
| N2 | Insider shorthand without a gloss: W3/W4, T7, stage3 D3, PDS18, "account gates", outbox `seq`, "ladder" (used two ways), "standard decision blocks". | comprehension |
| N3 | Line 104 expects more operators soon, yet line 128 keeps per-operator customization out of scope "until there is more than one operator". Make them agree. | comprehension |
| N4 | Define "watch any agent" for headless `codex app-server`, `claude -p` and `agy acp`: a live event view plus OS-user access; stepping in is a message through the harness protocol. | design |
| N5 | A cross-account handoff is a new session with a cold cache. Show "wait until reset at T" next to "hand off now", and measure the loss in T7. | design |
| N6 | Label `work-allocation.md:30` ("at most two concurrent assignments") as an example operator setting. | design, readiness, structural |
| N7 | "see Alternatives" does not match the heading "Alternatives Weighed"; `session-handoff.md` is called stale with no replacement link; the "Proven live" status belongs outside `fleet.md`-bound content. | structural, comprehension |
| N8 | The OpenTelemetry GenAI conventions are experimental; if adopted, record the version used. | readiness, design |
| N9 | Smaller code items: host capabilities are a fixed list, not observed; event pages count array positions, not `seq`; the 1800 s PR-check and 50 s Pipe timeouts are hard-coded. Make them settings. | codebase-details |
| N10 | Gap 2 restates `state-ownership.md`'s decision that Fleet owns task state. Present it as background. | structural |

---

## Divergences between lenses

- **Analytics store.** Zoom-out suggests self-hosted Langfuse on PDS18: Fleet's core tables can share its Postgres, and it brings traces, scores, datasets and annotation queues. Design and structural prefer Postgres plus the object store first, adding ClickHouse only when query load proves it. Their reasons are Langfuse's heavy stack and Occam's razor. Both are candidates; compare them in the decision register.
- **Codex sub-agents.** Inventive treats them as invisible. Design points to a recorded `collaborationTools:false` profile switch (`layered-isolation-model.md`) and to child rollout files in the same `CODEX_HOME`. Codebase found spawned children recorded on the parent (`codex.rs:182-184`). The brief's "cannot be disabled, invisible" row needs updating from the evidence.

## Claims to verify before relying on them

The reviewers could not confirm these against installed versions:
- Codex `rate_limits` in rollout and app-server events, and Claude Code rate-limit events and `/usage`.
- Claude Code's `cleanupPeriodDays` default, and whether it applies under `--setting-sources ""`.
- MinIO community edition status.
- Native OpenTelemetry export in Claude Code and Codex. If it exists, prefer it over parsing logs.
- Whether Langfuse self-hosted fits on PDS18.
- Provider terms of service for automated use of consumer subscriptions.

---

## Directions to consider

1. **Correct the facts first.** Fix the constraints table:
   - Claude on Linux is a policy.
   - The Codex sub-agent switch and child visibility are known.
   - The quota signals need checking.
   - The Antigravity version pin is a blocker that contradicts `fleet.md`.

   Then settle the two contradictions with `fleet.md` (B3).
2. **Write a "current implementation" section,** so engineering starts from the real store, executor and records (B1, B2, B5, M3, M8).
3. **Define the execution once:** kind, optional links, state set with legacy mapping, and granularity (spans for calls, child executions for sub-agents).
4. **Prove the ladder with one thin vertical slice:**
   - one project and one kind of work;
   - three or more variants with several interleaved trials each;
   - one objective check and one LLM judge;
   - an assessment, then promotion of the recommendation as a git change;
   - one production task that uses it and stores a non-text artifact in the object store;
   - operator acceptance recorded as a human score.

   Run it on two hosts, with one simulated rate-limit event. Defer tiers, previews, triggers, mirroring and scale targets.
5. **Build spending control from data Fleet already has:** forwarded rate-limit events, token counts and limit errors with reset times, combined into a labelled estimate and account availability states. Reservations are operator settings on the existing account switch.
6. **Make Pipe the single connection gateway.** Add a cost record (estimated, then actual), idempotency, immediate result copying and per-host service placement. Bring git and `gh` credentials into it.
7. **Make the environment the boundary.** One OS user or container per account or environment gives isolation, CPU and memory accounting for whole process trees (cgroup or Job Object), a clean `HOME` for lab comparability, and a login target for observation.
8. **Treat history as untrusted data behind scoped queries.** Collect transcripts to central storage when a run ends, redact by exact value at ingest, and give agents summaries and pointers under the execution's effective permissions.
9. **Split the brief by destination** following the merge plan in M14. Add a glossary, a Risks & Mitigations section, success measures and a decision register.

---

## Ideal Final Result

**For the whole concept:** Fleet is a thin ledger over existing products. It writes only execution records, scores, task rows and the links between them. Everything else is a side effect of something Fleet already has to do:
- the isolation boundary measures resources;
- the connection broker knows which secrets to scrub;
- production work is a continuous evaluation of the current champion;
- the object store and analytics store enforce tiers and permissions themselves.

The ideal for each focus area:

- **Real-world harness operation:** problems appear as readiness facts before launch, and as recorded confounders at the start of each execution. Nothing hangs silently; nothing is blocked by a quirk; every harness upgrade reruns the qualification suite on its own.
- **Artifact storage:** agents drop files into a folder or a local path. The executor streams them to an object store, with keys prefixed by project and by lab or production. The store's lifecycle rules handle tiers. Fleet keeps one metadata row per object and purges with a tombstone. Acceptance binds to an immutable version.
- **Task vs execution:** one execution record with optional links covers every run, trial, step and delivery. A task is operator intent plus an outcome, and the rest of its state is a view over its executions and their scores.
- **Subscription spending control:** quota is a measured fact where the harness reports it, and a labelled estimate where it does not. Limit errors are calibration points that trigger operator-configured routing or handoff. The lab consumes window capacity that would otherwise expire.
- **Centralized API connections:** agents use the provider's full API through a local endpoint that injects credentials. They never see a key, and every charge, estimated then actual, lands on an execution, with no double billing on retry.
- **Lab-to-production ladder:** lab, then an optional operator-configured canary, then promotion as a git change. Every production acceptance is a free human score, so carry-over is measured all the time and LLM judges are calibrated automatically.
- **Agent access to full history:** one project grant controls live view, history and agent queries, enforced by the store's row policies. Agents get summaries and pointers by default and fetch raw spans on demand, with history marked as data and never as instructions.
