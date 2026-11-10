# Fleet Lab

*   **Author:** Alex Kirs
*   **Status:** In Review (revision 2, after EGM review in [`reviews/fleet-lab/`](./reviews/fleet-lab/))
*   **Date:** 2026-11-10
*   **Input:** [`lab-brief.md`](./lab-brief.md). Its section "Owner Decisions, Round 3 (2026-11-10)" is binding and wins over earlier brief text. The decisions are restated in the table below so this doc stands alone.

--------------------------------------------------------------------------------

## Overview

Fleet Lab is the `/lab` screen plus coordinator code where the owner tests how Fleet's agents work. The thing under test is a **pipeline**: an ordered chain of agent, API and script steps. Lab runs a pipeline over **examples** in an **experiment** (single run, A/B, or improvement loop), scores the results with scripts, judge agents or owner ratings, and lets the owner promote a winning variant into the project's recommendation.

Lab adds no new run machinery. A trial is a `pipeline_run` execution with `experiment_id` and `variant` set and `task_id` null. Its steps are `pipeline_step` executions; each provider call is a `tool_run`. Outputs are artifacts with lab scope. Owner ratings are questions. Lab adds two tables (`experiment`, `score`), one column (`execution.request_id`), pipeline format version 2, a use-case registry in `config/lab/`, and one reconcile function that drives experiments.

The old lab and Pipe's own method evaluation are deleted in one change. `/lab` becomes the new screen.

### Glossary

| Term | Meaning |
|---|---|
| Coordinator | Fleet's TypeScript server (`packages/coordinator`). Owns Postgres, routes, reconcile. |
| Executor | Fleet's Rust process on each host (`crates/executor`). Runs harnesses, scripts, worktrees. |
| Host | A machine with an executor: the Mac, the Windows PC (WSL), PDS18 (Linux server, runs Pipe; not eligible for Claude). |
| Harness | An agent CLI the executor drives: Codex, Claude, Antigravity. |
| Profile | A trusted JSON file naming harness, model, skills, granted connections. |
| Pipe | Separate service (other repo, `pipe.md`) that holds provider API keys and serves `web_search` / `web_extract` over HTTP. Records every call with a caller-supplied label in its `source` field. |
| Connection | Fleet's record of one outside provider endpoint: endpoints per host, grants, cost unit, pinned client hash. Example: `pipe-read`. |
| Catalogue / recommendation | `config/catalogue.json` lists methods per project; `recommendation_set` (`projects.ts:70`) records the chosen method as a `project-recommendation`. |
| Use case | A registry entry pointing at a use case written in some org repo (Fleet, taskq, pilotq, agentq, dashq, assetq). |
| Example | One runnable Lab input drawn from a use case. Never a task. |
| Experiment | One whole comparison: examples × variants × repeats. Kind `single`, `ab`, `loop`. |
| Variant | A label plus overrides of the pipeline (profile, harness, model, skills, prompt, web route, script params); hashed into `config_ref`. |
| Trial | One variant on one example, one repeat. |
| Attempt | Retry counter of a trial cell. Starts at 0; raised only by the owner's "retry cell" action. |
| Generator | Strategy part that proposes next variants. |
| Allocator | Strategy part that picks which (example, variant) cells run this iteration. |

### Owner decisions (round 3)

| # | Decision |
|---|---|
| R3.1 | No staged removal. Old lab, legacy pages, Pipe's method evaluation go in one cut; no shims, no read-only period. |
| R3.2 | Web search is an ordinary API connection with the same accounting as every call. Callers reach connections via Pipe HTTP or MCP servers served from Fleet infrastructure. No special lane. |
| R3.3 | Pipe calls link to runs by a step-id label now; per-run tokens issued by Fleet later. |
| R3.4 | Claude built-in web tools off by default; turning on is an explicit variant setting. |
| R3.5 | "Experiment" = whole comparison; "example" = one input item. |
| R3.6 | An agent proposes examples; owner approves each with a pass/fail rating. |
| R3.7 | Budget action configurable: "pause and ask" and "stop" both supported. |
| R3.8 | Claude on Windows is WSL only; Claude runs on Mac and Windows. |
| R3.9 | Provider keys in coordinator 0600 secret directory with encrypted backup; Postgres metadata only (covered by the separate `connections` design). |
| R3.10 | First pipelines: web research; Codex vs Claude page with/without skill; Meshy asset pipeline once qualified. |

## The Problem

*   The old lab runs one agent step, carries invented limits, and stores each experiment as one growing JSON document.
*   Pipe has a second evaluation system (datasets, comparisons, recommendations): two places claim which method is best.
*   Agents cannot reach provider connections from inside a step (Claude runs with `--strict-mcp-config`, Codex gets `mcp_servers={}`, both under a Fleet-owned HOME), so Codex-vs-Claude comparisons measure different web tools.
*   Pipe calls do not join to Fleet executions, and their cost is not a typed measurement.
*   Use cases exist as prose in six repositories; nothing turns them into runnable inputs.

## Goals

*   G1. Run a linear multi-step pipeline (agent, API, script) as one trial, with each step's cost and outputs visible.
*   G2. One experiment model: examples × variants × repeats; kinds `single`, `ab`, `loop`.
*   G3. Restart-safe: no duplicate launches, no double-counted spend.
*   G4. Scores from scripts, judges and three owner rating forms; forms never averaged together.
*   G5. Budgets in measurable units with configurable action; the system warns or pauses, never refuses a launch on its own.
*   G6. Use-case registry with source pointers and drift warning; agent-drafted examples approved by the owner.
*   G7. Every provider call, coordinator-side or agent-side, uses one accounting path.
*   G8. Claude trials on the Mac and on Windows through WSL.
*   G9. One-cut removal of the old lab and Pipe's evaluation.

## Non-Goals

*   Lab-only execution kinds or pipeline format; examples as `task` rows.
*   Concurrency caps, default timeouts, or other limits Lab invents. Provider limits stay in the connection.
*   Branching or fan-out inside a pipeline.
*   Combined "cost score", Elo ladders, rubric builders.
*   Deploying LiteLLM, Portkey or OpenRouter.
*   Automatic import of use cases from other repositories.
*   Native (non-WSL) Windows.
*   Keeping or migrating old lab or Pipe evaluation data.
*   The provider-key move into Fleet (separate `connections` design). Lab needs only a labelled call that returns units.
*   Automatic rating sampling (order-swap disagreement, near-decision). Owner opens the rating queue. Added later with agreement data.

## Open Questions

Defaults taken so work can start; owner can overturn.

*   **Q1. Storage.** Two Postgres tables (option B). A and C under Alternatives. *Default: B.*
*   **Q2. Pipe datasets.** *Default: convert assetq dataset cases into Lab examples in Stage 1; the Pipe-side removal PR lands after that conversion.*
*   **Q3. Decision-gate threshold** (judge-to-owner agreement before running loops unattended). *Default: no coded threshold; screen shows agreement; owner decides.*
*   **Q4. Scope split.** Zoom-out review suggests moving the loop (Stage 4) and Claude-on-WSL into separate docs. *Default: keep one doc, ship by stage; Stage 4 starts only after Stage 3 A/B results exist.*

## Critical User Journeys (CUJs)

### CUJ 1: A/B two agent setups on web research

*   **Given**: approved examples for `fleet:UC1`; pipeline `web-research` v2; Codex and Claude hosts ready.
*   **When**: the owner creates an `ab` experiment, variants "baseline" and "baseline + skill X", 3 repeats, budget `pipe_credits` with action `pause`, and launches.
*   **Then**: each cell (example × variant) shows state, score with form, spend with basis, time, confounders (host, harness version, web route). Baseline spread across repeats is visible. Trials failed on infrastructure show their reason and are left out of comparison. At the budget, new launches wait and the owner gets a question.

### CUJ 2: Restart mid-iteration

*   **Given**: a running experiment.
*   **When**: the coordinator restarts.
*   **Then**: reconcile inserts trials by `request_id` with on-conflict-do-nothing; only missing trials launch; spend is derived from rows, so counted once.

### CUJ 3: Owner rating

*   **Given**: finished trials.
*   **When**: the owner opens the rating queue and requests `pick_best` for a set of cells.
*   **Then**: outputs show blind in a seeded shuffle; keyboard answers write `score` rows with form `pick_best`. Results show "rated k of n".

### CUJ 4: Registry drift

*   **Given**: an entry `taskq:UC-07` with stored source hash.
*   **When**: the source changes upstream.
*   **Then**: the drift check marks `source_status: changed`; launch warns, not refuses.

### CUJ 5: Promote

*   **Given**: a winning variant.
*   **When**: the owner clicks Promote.
*   **Then**: Lab produces a catalogue method entry (pipeline + overrides + `config_ref` + source experiment) as a JSON patch for `config/catalogue.json` shown to the owner to commit, and, once the method exists, calls `recommendation_set`. Nothing promotes automatically; the coordinator never writes tracked config.

### CUJ 6: Stuck trial

*   **Given**: one trial hangs (no timeout set).
*   **When**: the owner cancels that cell.
*   **Then**: trial ends `cancelled`, counts as finished for iteration progress, is excluded from comparison; owner can "retry cell" (`attempt + 1`).

## Technical Plan (Architecture)

### Entities

| Lab term | Stored as |
|---|---|
| Trial | `execution` kind `pipeline_run`, `experiment_id`, `variant`, `config_ref`, `request_id`, `task_id` null |
| Step run | `execution` kind `pipeline_step`, parent = trial |
| Provider call (any caller) | `execution` kind `tool_run`, parent = step, `connection_cost` measurement |
| Judge, strategy, drafting run | `execution` kind `agent_run` or `pipeline_run`, `experiment_id` set, `variant` null |
| Infrastructure failure | `execution.problem` with code prefix `infra:` |
| Outputs, transcripts, approved examples | `artifact`, scope `lab` |
| Owner rating | `question` kind `rating` |
| Experiment | table `experiment` |
| Score | table `score` |
| Pipeline | `config/pipelines/*.json`, version 2 |
| Use case | `config/lab/use-cases/<project>.json` (repo file) |

```text
experiment ─┬─< execution(pipeline_run, variant)  = trial
            │      └─< execution(pipeline_step) ─< execution(tool_run, connection_cost)
            │      └─< artifact(scope=lab)
            ├─< execution(agent_run|pipeline_run, variant=null)  judge / strategy / drafting
            ├─< score (trial_ids[])
            └─< question(kind=rating) ── answer ──> score
```

### Data contracts

`experiment` holds: kind, pipeline id, pinned examples (`[{id, artifact_id, hash, split}]`), variants (`[{label, overrides, config_ref}]`), repeats, evaluators (`[{kind: script|judge|human, ref, form}]`), primary evaluator (required for `loop`), strategy (loop only), budgets, state, stop reason. Iteration is derived from trials.

`score` holds: experiment, `trial_ids[]` (one for a trial score, N for `pick_best`), evaluator + version hash, form (`metric|pass_fail|pick_best|scale`), value, source execution or question, rationale, `replaces_score_id` (a correction is a new row).

Experiment states: `draft → running ⇄ paused → stopped | done`. Variants and examples are editable only in `draft`; the loop appends variants while `running`.

### Pipeline format version 2

```json
{"version":2,"id":"web-research","steps":[
  {"id":"search","kind":"api","connection":"pipe-read","tool":"web_search","arguments":{"query":"${example.prompt}"}},
  {"id":"read","kind":"api","connection":"pipe-read","tool":"web_extract","arguments":{"urls":"${steps.search.output.urls}"}},
  {"id":"summary","kind":"agent","profile":"codex-default","prompt":"Summarize the files in $FLEET_INPUT_DIR."},
  {"id":"check","kind":"script","command":"scripts/check.sh"}]}
```

*   Every step writes an artifact `output.json` (structured result) plus any files. The next step gets all previous steps' artifacts in `FLEET_INPUT_DIR/<step id>/`.
*   References: `${example.<field>}` and `${steps.<id>.output.<json path>}`, resolved by the coordinator before the step starts, from the example and from earlier `output.json`. A missing reference fails the step with `problem: ref:<path>`; no default.
*   `api` step: `{connection, tool, arguments, mode: sync|job}` (default `sync`). `job`: submit, store job ID in the step's `execution.data.jobId`, poll on each reconcile of the experiment; after a restart, polling resumes from that field. Uncertain outcome is "unknown, never replay".
*   `agent` step: profile + variant overrides.
*   `script` step: new executor command kind `script`: own process group (stop kills the group), `FLEET_INPUT_DIR`, `FLEET_OUTPUT_DIR`, outputs collected as artifacts, optional `timeout_ms`.
*   Failed step stops later steps; trial keeps its reason.
*   Version-1 files are rewritten to v2; no v1 reader stays (R3.1).

### Connections: one accounting path (R3.2)

Fleet ships one connection client, `connection_call.py` (today's `pipe_search.py`, generalized), with two entry points:

*   **CLI** (coordinator-side `api` step): `connection_call.py call <connection> <label>`, body on stdin.
*   **MCP** (agent-side): `connection_call.py mcp <connection>`. The executor adds it to the harness MCP config for each connection the profile grants and allowlists `mcp__<connection>__<tool>`. This is the "MCP served from Fleet infrastructure" of R3.2.

Both entry points send label = the current step's execution ID (UUID; fits Pipe's 64-char `A-Za-z0-9._:-` rule), and both append one line per call to `$FLEET_OUTPUT_DIR/.connection-calls.jsonl`: `{tool, request_id, outcome, error_code, usage}`. The coordinator (API steps) or executor upload (agent steps) turns each line into a `tool_run` child with `connection_cost` from `usage.credits`, basis `measured`. No pull-back from Pipe, no "unmatched" state. Spend lands at step completion, before the step's state change triggers reconcile.

*   The client checks the SHA-256 of Pipe's client library against `clientSha256` in the connection record before loading it.
*   Errors come from Pipe's JSON `{error, reason}` body. `queue-full`, 429, provider quota, `no-usable-key` → `execution.problem = infra:<code>`; trial excluded from comparison. `unknown` is never retried automatically.
*   Claude built-in web is off unless the variant sets `web_route: builtin` (R3.4). Each trial records its web route.
*   The Pipe token is read from a host-only file at run time. An agent on the same host could read it and send calls with any label; acceptable for the prototype, closed by per-run tokens (R3.3, `connections` design).

### Use-case registry and examples

*   Use cases are repo files: `config/lab/use-cases/<project>.json`, entries `{id: "<project>:<source id>", source {repo, path, anchor, commit, sha256}, source_status, actor, goal, expected_outcome, checks[], lab_fit: work|behavior|none, tags, maturity}`. Hand-indexed. IDs never renumber.
    *   `anchor` names one section of the source file: an explicit `<a id="…">` line or a heading with that GitHub slug (the section runs to the next heading of the same or a higher level), or else one table row whose first cell starts with the anchor (pilotq `L01`, agentq `A1`, dashq `DQ-01`). `sha256` is the hash of that section's text at `commit`, so an edit elsewhere in the file is not drift.
    *   `source_status`: `current | changed | missing` (file or anchor gone). An org repo that is not checked out reports `unavailable` and leaves the stored status alone.
    *   `lab_fit`: `work` = an agent produces a checkable result; `behavior` = an agent's conduct is what is judged (PM, review); `none` = product behavior Lab does not exercise.
    *   `checks[]`: `{kind: script|judge|human, ref, metric}`; `maturity`: `indexed | example drafted | synthetic ready | calibrated | linked to a real task`.
*   Examples are artifacts (one source of truth): scope `lab`, `role: example`, name `examples/<id>@<version>.json`, JSON `{id, use_case_id, version, origin: synthetic|task:<id>, fixture: {repo, commit}|null, prompt, inputs[], references[], checks[], split: tune|held_out}`, hash = artifact content hash.
    *   `inputs[]` and `references[]` are files `{path, text}` (relative path). `inputs` are what the trial starts with; `references` are one known-good result.
    *   `checks[]` are `{id, run}`: a shell command run in the trial's output directory with `FLEET_OUTPUT_DIR` set; exit 0 = pass. Stage 3 script evaluators run the same commands.
    *   An `id@version` never changes content: importing other content under the same `id@version` is refused (409); a change is a new version. The coordinator writes the example artifact itself (no execution), stored and verified like a backfilled artifact.
    *   Approval is `artifact.approved_at` (migration 12): set on approval, null while proposed.
    *   "Checks pass with no agent" means: on inputs plus references every check passes, and on inputs alone at least one check fails (so a check that passes untouched input is caught). `scripts/lab-examples.ts check` runs this; the test suite runs it on every seed.
*   Owner-written examples (Stage 1) start as seed files `config/lab/examples/*.json`, reviewed in the PR (the review is the approval), and are imported with `scripts/lab-examples.ts import` into approved example artifacts. After import the artifact is the source of truth; a seed edit needs a new `version` and a new import.
*   Drafting (R3.6, Stage 2): pipeline `config/pipelines/draft-examples.json` (agent step) writes proposed example artifacts; each gets a `rating` question with form `pass_fail`; a pass marks the artifact approved. Experiments pin approved example artifacts by ID and hash.
*   Drift check `scripts/lab-drift.ts` recomputes source hashes from each repo's `HEAD` and prints changed or missing entries; `--write` stores `source_status`; `--pin <id…>` accepts the current source after review. Warns in CI and (Stage 3) on launch; never blocks.

### Experiment reconcile

`reconcileExperiment(id)` runs on coordinator start (all `running` experiments), after every Lab execution reaches a final state, and after each owner action. It runs inside `pg_advisory_xact_lock(hash(experiment id))`, so calls for one experiment are serial.

1.  Load experiment, its executions, scores, spend (sum of `connection_cost` and totals over executions with this `experiment_id`, including judge, strategy and drafting runs).
2.  Stop rules, first match wins and is recorded: (a) owner stop; (b) budget with action `stop`; (c) `max_iterations` reached; (d) `no_gain`; (e) strategy returned `stop`.
3.  Budget with action `pause` reached → `paused`, one owner question (resume / stop). Running trials finish. `warn` → banner only.
4.  For each needed cell (examples × variants × repeats of the current iteration, filtered by the allocator) insert the trial with `request_id = sha256(experiment, iteration, example, variant, repeat, attempt)` using `ON CONFLICT (request_id) DO NOTHING`; launch only inserted rows. Priority `background` (no effect until the scheduler exists; screen says so).
5.  Iteration complete = every cell's trial is final (`completed`, `failed`, `cancelled`) and every automatic evaluator has a score or a failed run for each completed trial. Then for `loop`: run the strategy step, append its variants.

`no_gain`: `{rule: "no_gain", k}`. Gain = best variant's mean of the primary evaluator on tune examples minus the baseline mean. Stop when gain ≤ (max − min of baseline repeats) for k consecutive iterations. `k` is set by the owner per experiment; no default.

### Strategies (loop only, Stage 4)

A strategy is a pipeline step (script or agent). Input artifact: experiment snapshot (variants, scores, spend, budgets). Output artifact `strategy.json`: `{action: next|stop, variants:[{label, overrides}], reason}`. Config `{generator:{kind, params}, allocator:{kind, params}}`.

*   Generators: `list` (fixed variants, covers A/B), `grid` (all combinations of listed overrides), `propose` (agent reads scores and losing transcripts, writes overrides with a reason; `keep_best: true` starts each round from the current best).
*   Allocator: `full` (every cell runs). Adaptive allocators (`halving`, later) apply to tune examples only; finalists (the top variants an allocator keeps) always run on every held-out example.
*   A strategy never changes a budget.

### Budgets

`{unit, limit, action: warn|pause|stop}` at experiment scope. Units now: `trials`, `wall_ms`, `pipe_calls`, `pipe_credits`. Default action when unset: `pause` (R3.7). `trials` counts trial executions only; the other units count every Lab execution of the experiment. Projected overrun warns. Token and money units, and per-iteration or per-trial scope, come later (tokens once totals are filled; money once API cost is centralized, data foundation Q9).

### Ratings

`question.kind = 'rating'`, `options = {form, experiment_id, trial_ids, seed}`. Forms: `pass_fail` (with reason tags), `pick_best` (N outputs blind, order = shuffle seeded by `seed`; pairwise is `pick_best` with N = 2; value = chosen trial ID), `scale` (1–5 plus note). The owner creates rating requests from the matrix. Never blocking. Agreement shown = percent of trials where judge and owner `pass_fail` (or `pick_best` choice) match.

### `/lab` screen

From `packages/ui` only. Views: experiments list; matrix (rows examples, columns variants; cell state, score with form, spend with basis, running time, confounders, link to execution; actions cancel cell, retry cell); rating queue; registry with drift and maturity; examples awaiting approval; Promote.

### Claude on WSL (R3.8)

Per-host setting `claude_placement: mac | windows-wsl` in the host config replaces the compile-time macOS check. Readiness verifies it (WSL detected from the kernel release string) and checks a login file under Fleet's config directory. `delivery_worker.py` uses the same rule. PDS18 stays ineligible.

### Removal in one cut (R3.1)

*   Profile code (`canonical`, `readTrustedProfile`, `profileDigest`, `listProfiles`, `validNativeProfile`) moves from `lab.ts` to `profiles.ts`; `lab.rs` (`validate`, `validate_antigravity_settings`, `prepare`, `apply`, `mcp_servers`) moves to `profile.rs`. Behavior unchanged.
*   Old lab features used elsewhere become Lab experiments:
    *   paired delegation (`POST /api/experiments` with `profileIds`) → `ab` experiment with one variant per profile;
    *   delivery comparison (`POST /api/experiments/from-runs`) → `single` experiment built from existing executions;
    *   `/api/lab/executions`, `/api/lab/compare`, `/api/lab/profiles` → `GET /api/lab/experiments/:id` (trials included) and `GET /api/profiles`.
*   Everything else of the old lab is deleted; old `experiment` documents are deleted by migration.
*   Pipe's method evaluation is removed in a paired Pipe-repo PR after the assetq conversion (Q2).

## Alternatives Considered

### Storage A: JSON documents with scores inside

*   **Pros**: no schema work.
*   **Cons**: documents grow without bound in a loop; every write takes the global write lock; scores do not join with spend; corrections rewrite the document.
*   **Reason for Rejection**: Rejected by author: fails the loop and correction cases.

### Storage C: control in Postgres, scores in ClickHouse

*   **Pros**: fast cross-experiment analytics.
*   **Cons**: corrections and ratings awkward append-only; waits on Phase 6 of the data foundation; reconcile reads two stores.
*   **Reason for Rejection**: Rejected by author; later copy scores into ClickHouse for reports.

### Staged removal with shims

*   **Pros**: smaller PRs; old screens keep working during migration.
*   **Cons**: shim code and a legacy period in an early prototype with one user.
*   **Reason for Rejection**: Rejected by author (R3.1).

### Agent-side cost pulled back from Pipe records

*   **Pros**: no client-side recording.
*   **Cons**: second accounting path, late spend, an "unmatched" failure state.
*   **Reason for Rejection**: Rejected by author: one client writes the same call record on both paths (R3.2).

### Web search as a special coordinator lane

*   **Pros**: exists today (`direct_search`, `projects.ts`).
*   **Cons**: `web_extract` unreachable; agents and API steps measured differently.
*   **Reason for Rejection**: Rejected by author (R3.2).

### Lab and Pipe evaluation coexist

*   **Pros**: no Pipe change.
*   **Cons**: two sources of recommendations.
*   **Reason for Rejection**: Rejected by author (R3.1).

### Promote writes `config/catalogue.json` from the coordinator

*   **Pros**: one click.
*   **Cons**: runtime writes to a git-tracked file; conflicts with worktrees and review.
*   **Reason for Rejection**: Rejected by author: Lab produces the patch, the owner commits it.

### Gateway (LiteLLM etc.)

*   **Pros**: ready key pools and budgets.
*   **Cons**: agent steps use subscription harnesses; second call ledger.
*   **Reason for Rejection**: Rejected by author: copy shapes.

### Strategy engine with built-in algorithms

*   **Pros**: fewer files per strategy.
*   **Cons**: one engine to grow for every algorithm.
*   **Reason for Rejection**: Rejected by author: one step interface.

### Examples as tasks

*   **Reason for Rejection**: Rejected by author; data foundation rule A3 (lab runs are not tasks).

--------------------------------------------------------------------------------

## Non-Functional Requirements

*   **Security**: provider keys never reach executors, agents, profiles or screens. Client library hash pinned. Pipe token readable by same-host agents until per-run tokens (noted above).
*   **Cost**: 50 examples × 4 variants × 3 repeats × 10 searches = 6,000 calls. Budgets and a pre-launch estimate warn; quota-hit cells are labelled "quota-affected".
*   **Restart safety**: unique `request_id`, per-experiment advisory lock, spend derived from rows.
*   **Rollback**: revert the PR and drop the two tables and column. Old lab data is not restorable by design.

--------------------------------------------------------------------------------

### Additional Resources

*   [`lab-brief.md`](./lab-brief.md), [`fleet-data-foundation.md`](./fleet-data-foundation.md), [`fleet-operator-app.md`](./fleet-operator-app.md)
*   Pipe repo: `pipe.md`, `assetq/docs/method-evaluation.md`

### Future Improvements

*   Allocator `halving`; generators `reflect`, `evolve`, `thompson`.
*   Automatic rating sampling once agreement data exists.
*   Per-run tokens (R3.3); token and money budgets; per-iteration/per-trial budget scope.
*   Examples snapshotted from real tasks.
*   Meshy asset pipeline as `job` step once qualified.

--------------------------------------------------------------------------------

## Detailed Implementation Plan

Stages: 0 → 1 → 2 → 3 → 4, one PR each. Each stage leaves a building, tested tree.

### Stage 0: one cut and groundwork

#### 1. `packages/coordinator/profiles.ts` (Create)

*   Move the five profile functions from `lab.ts` unchanged. Add optional profile fields `skills: string[]`, `connections: string[]` (validated, unused until Stage 2).

#### 2. `packages/coordinator/lab.ts`, `packages/coordinator/lab-ui.ts` (Delete)

#### 3. `packages/coordinator/app.ts` (Modify)

*   Import from `./profiles.js`; remove `registerLab`, `/legacy/lab` from `LEGACY_ROUTES` (line 27); add `GET /api/profiles`.

#### 4. `packages/coordinator/delegation.ts`, `delivery.ts`, `agent-tools.ts`, `recovery.ts` (Modify)

*   Import from `./profiles.js`.

#### 5. `packages/coordinator/ui.ts`, `project-ui.ts`, `packages/coordinator/README.md` (Modify)

*   Remove old lab links and mentions; `/lab` shows "Lab is being rebuilt" until Stage 3.

#### 6. `crates/executor/src/profile.rs` (Create), `crates/executor/src/lab.rs` (Delete)

*   Move `validate`, `validate_antigravity_settings`, `prepare`, `apply`, `mcp_servers`.

#### 7. `crates/executor/src/main.rs`, `crates/executor/src/harness.rs` (Modify)

*   Call `profile::*` (`main.rs:282,310,330,382`; `harness.rs:59,264`). Replace `cfg!(target_os="macos")` at `harness.rs:140` with the `claude_placement` setting; update the test at `harness.rs:513` to cover both values.

#### 8. `crates/executor/src/readiness.rs` (Modify)

*   Verify `claude_placement`; WSL detection; login file present at `<fleet config dir>/claude/.credentials.json` (the file Claude writes when `CLAUDE_CONFIG_DIR` is `<fleet config dir>/claude`).

#### 9. Host config schema: `docs/configuration.md` (Modify)

*   Document `claude_placement` and the WSL login file path.

#### 10. `packages/coordinator/delivery_worker.py` (Modify)

*   Platform check (line 98) reads `claude_placement`; WSL `linux` counts as Windows.

#### 11. `crates/executor/src/worktree.rs` (Modify)

*   `script` command kind: process group, group kill on stop, `FLEET_INPUT_DIR` / `FLEET_OUTPUT_DIR`, full output as artifacts, optional `timeout_ms`. New worktree per trial at a pinned commit, no shared-path links.

#### 12. `packages/coordinator/db/schema.sql` (Modify)

*   `-- migration 11`:
    *   `ALTER TYPE question_kind ADD VALUE 'rating'` (own statement; no use of `rating` in the same migration).
    *   `ALTER TABLE execution ADD COLUMN request_id text`; `CREATE UNIQUE INDEX execution_request ON execution(request_id) WHERE request_id IS NOT NULL`.
    *   `CREATE TABLE experiment(id text PRIMARY KEY, project_id text NOT NULL, kind text NOT NULL CHECK (kind IN ('single','ab','loop')), pipeline_id text NOT NULL, examples jsonb NOT NULL DEFAULT '[]', variants jsonb NOT NULL DEFAULT '[]', repeats int NOT NULL DEFAULT 1, evaluators jsonb NOT NULL DEFAULT '[]', primary_evaluator text NULL, strategy jsonb NULL, budgets jsonb NOT NULL DEFAULT '[]', state text NOT NULL DEFAULT 'draft' CHECK (state IN ('draft','running','paused','stopped','done')), stop_reason text NULL, created_at timestamptz NOT NULL DEFAULT now(), seq bigint NOT NULL DEFAULT nextval(<the sequence execution.seq uses>))`.
    *   `CREATE TABLE score(id text PRIMARY KEY, experiment_id text NOT NULL REFERENCES experiment(id) ON DELETE CASCADE, project_id text NOT NULL, trial_ids text[] NOT NULL, evaluator text NOT NULL, evaluator_version text NOT NULL, form text NOT NULL CHECK (form IN ('metric','pass_fail','pick_best','scale')), value jsonb NULL, source_execution_id text NULL REFERENCES execution(id), question_id text NULL REFERENCES question(id), rationale text NULL, replaces_score_id text NULL REFERENCES score(id), created_at timestamptz NOT NULL DEFAULT now(), seq bigint NOT NULL DEFAULT nextval(<same sequence>))`; `CREATE INDEX score_experiment ON score(experiment_id)`; GIN index on `trial_ids`.
    *   Row-level security on both tables with the same `fleet.project_ids` policy as `task`.
    *   Triggers `experiment_change` and `score_change` calling `fleet_change()`; extend `fleet_change` with kinds `experiment` and `score`.
    *   `DELETE FROM document WHERE kind = 'experiment'`.

#### 13. `packages/coordinator/executions.ts` (Modify)

*   `createExecution` (line 108) adds `experimentId`, `variant`, `accountId`, `requestId` to its INSERT (`configRef` already exists). Add `insertIfAbsent` returning whether a row was inserted.

#### 14. Tests (Modify/Delete/Create)

*   Delete `tests/coordinator/lab.test.ts`, `lab-browser.ts`, `lab-packaged.ts`.
*   Create `tests/coordinator/profiles.test.ts`: coverage of `canonical`, `profileDigest`, `validNativeProfile` moved from `lab.test.ts`.
*   Modify `tests/coordinator/multiharness.test.ts:1` (import path), `stream.test.ts:77` (`/lab` no longer redirects to `/legacy/lab`), `tests/ui/sw.test.ts:11` (drop `/legacy/lab`).
*   Modify `tests/coordinator/delegation.test.ts:12` and `delivery.test.ts:30-31`: mark the pairing and comparison cases skipped with a pointer to Stage 3, where they are rewritten against `/api/lab/experiments`. (The delegation and delivery behavior itself does not depend on these routes; only the tests use them.)
*   `cargo test`: `profile.rs` keeps `lab.rs`'s tests; `worktree.rs` adds tests for group kill and timeout.

#### 15. Docs (Delete/Create/Modify)

*   Delete `docs/experiments.md`, `docs/lab-coordinator-build.md`, `docs/lab-first-experiment.md`, `docs/lab-live-evidence.md`, `docs/lab-runtime-build.md`.
*   Create `docs/lab.md`: one paragraph pointing at this design.
*   Modify `docs/designs/fleet-data-foundation.md`: remove lab-trial row in §7 and lab parts of items 13 and 19; log migration 11.
*   Modify `docs/designs/README.md`: list `lab-brief.md`, `fleet-lab.md`, `reviews/fleet-lab/`.

Stage 0 exit: a trial inserted by SQL fixture (experiment row + `pipeline_run` with `experiment_id`) is queryable, `task_id` null, has lab artifacts, not in Tasks; `/legacy/lab` gone; delivery, recovery, delegation tests pass; Claude readiness passes on WSL.

### Stage 1: registry and examples

#### 16. `config/lab/use-cases/{fleet,taskq,pilotq,agentq,dashq,assetq}.json` (Create)

*   Hand-indexed entries with `lab_fit`. Skip `dashq/.taskq/specq/` copies. Sources: Fleet `docs/use-cases.md` UC1–UC11; taskq `docs/use-cases/README.md` UC-01–UC-17; pilotq `docs/acceptance.md` L01–L20 (the brief's "about 67 rows" counted the same L rows repeated in later qualification tables); agentq `docs/spec/VERIFICATION.md` A1–A7; dashq `docs/design/operator-redesign/scenarios-and-data.md` DQ-01–DQ-10; assetq one entry `assetq:datasets` (`docs/method-evaluation.md`; the datasets themselves live in Pipe's database, not in a file). 66 entries.

#### 17. `scripts/lab-drift.ts` (Create), `.github/workflows/ci.yml` (Modify)

*   Recompute hashes, print changed entries; CI step `continue-on-error: true`. CI checks out Fleet only, so CI checks Fleet's own entries; other repos are checked where they are checked out (`FLEET_LAB_REPOS`, default the directory above the Fleet checkout).

#### 18. `scripts/lab-import-assetq.ts` (Create)

*   Convert assetq dataset cases (`id, text, attachments`) to examples (`origin: synthetic`, approved), use case `assetq:datasets`. Input is the `GET /v1/datasets` reply (`assetq datasets`), exported where the Pipe CLI and its credential already are; the case text is the prompt and the whole case (dataset identity, operation, attachment object IDs) is the input file `assetq-case.json`, so nothing is lost. Pipe scored these cases by owner judgment only, so they carry no script checks. The output goes to `scripts/lab-examples.ts import`.

#### 19. `docs/use-cases.md` (Modify)

*   Stable anchors (`<a id="ucN">`), Source and Status lines (Status `accepted`; Source names the registry entry).

#### 19a. Examples (Create): `packages/coordinator/lab/examples.ts`, `scripts/lab-examples.ts`, `config/lab/examples/*.json`; `app.ts`, `artifacts.ts`, `db/schema.sql` (Modify)

*   Added while building Stage 1: the exit needs stored examples, and the code had no way for the coordinator to write one (artifact `role` allowed only `original|thumbnail|poster`, every write went through an executor upload, and there was no approval field).
*   `-- migration 12`: `role` adds `example`; `artifact.approved_at timestamptz NULL`; unique index on `(coalesce(project_id, ''), name) WHERE role = 'example'`.
*   `POST /api/lab/examples` `{examples[], projectId?, approved?}` → `{examples: [{artifactId, id, version, hash, created}]}` (400 invalid example or unknown use case, 409 other content under an existing `id@version` or no reachable store); `GET /api/lab/examples?state=proposed|approved`; `GET /api/lab/examples/:artifactId` → `{artifactId, state, example}`. Operator credential, like every `/api` route.
*   `artifacts.ts` `writeObject` (shared with backfill) writes an object the coordinator hashed itself. An import refuses purged content (as `reserve` does) and checks every example before writing any, so a refused batch stores nothing.
*   Agents never see examples: the host artifact routes (`artifact_list`, `artifact_get`) skip role `example`, because references are known-good answers. Stage 2 hands a trial its example inputs only.
*   `config/lab` ships in `dist/config/lab`; the coordinator reads the registry from there (`FLEET_LAB_DIRECTORY` overrides).
*   `scripts/lab-examples.ts check|import`. `import` sends `$FLEET_ORIGIN` (else `$FLEET_BROWSER_ORIGIN`) as Origin when set (needed behind `FLEET_BROWSER_PROXY_AUTH`); `check` appends the directory of the node running it to PATH. Registry loaders skip dotfiles (macOS `._*` copies).
*   Five owner-written `work` seeds: `fleet:UC1` page, `fleet:UC5` web research (for the `web-research` pipeline, the API-step example), `fleet:UC9` faster code, `taskq:UC-07` plan to tasks, `taskq:UC-16` tray icon.

#### 19b. `tests/coordinator/lab-registry.test.ts` (Create)

*   Section rules; drift flags a changed section and leaves others current; registry shape; every seed passes its checks with no agent and a vacuous check is caught; assetq conversion is lossless; example import is idempotent, refuses changed content under the same version, validates use case and paths.

Stage 1 exit: drift check flags a changed source; at least five `work` examples exist; their script checks pass with no agent.

### Stage 2: pipelines v2 and connections

#### 20. `packages/coordinator/pipelines.ts` (Create), `packages/coordinator/projects.ts` (Modify)

*   v2 runner: ordered steps, `output.json` + artifacts, reference resolution, `sync` and `job` modes, typed `pipeline_run` / `pipeline_step` / `tool_run` rows, `infra:` problems from Pipe's JSON body. `projects.ts` `direct_search` and `pipeline_run` operations call it; no v1 path.

#### 21. `packages/coordinator/connection_call.py` (Create), `pipe_search.py` (Delete)

*   CLI and MCP entry points, tool name parameter, label, hash pin check, `.connection-calls.jsonl`.

#### 22. `crates/executor/src/harness.rs`, `native.rs`, `upload.rs` (Modify)

*   Per-execution MCP config adds `connection_call.py mcp <connection>` for each granted connection; allowlist its tools; Claude built-in web off unless `web_route: builtin`. Install profile `skills`: Claude via `--add-dir <skills dir>` plus enabling skills in its settings; Codex via `AGENTS.md` include in the worktree. Upload `.connection-calls.jsonl` lines as `tool_run` children.

#### 23. `config/pipelines/hello-search.json`, `search-basic.json` (Modify); `web-research.json`, `draft-examples.json` (Create)

#### 24. `config/connections.example.json` (Modify)

*   Add `pipe-read`: endpoints per host, grants, cost unit `credits`, `clientSha256`.

#### 25. Tests (Create)

*   `tests/coordinator/pipelines.test.ts`: failed step stops later steps; missing reference fails with `ref:`; job resumes after restart; infra codes classified (`queue-full`, 429, `no-usable-key`, `unknown` not retried).
*   `tests/coordinator/connection_call.test.ts`: hash mismatch refuses to load; both entry points write identical call lines.

Stage 2 exit: `web-research` completes on Mac and WSL; every Pipe call of an API step and of an agent step appears as a `tool_run` with credits.

### Stage 3: experiments and screen

#### 26. `packages/coordinator/lab/experiments.ts` (Create)

*   CRUD with state rules, `reconcileExperiment`, budgets, stop rules (a–d), cancel cell, retry cell, Promote (catalogue patch + `recommendation_set`).

#### 27. `packages/coordinator/lab/scores.ts` (Create)

*   Script and judge evaluators, rating questions and answers → scores, corrections, agreement.

#### 28. `packages/coordinator/tasks.ts` (Modify)

*   `answer` handles `rating` questions by delegating to `lab/scores.ts`.

#### 29. `packages/coordinator/lab/routes.ts` (Create), `app.ts` (Modify: register)

*   `POST /api/lab/experiments` `{kind, pipelineId, examples, variants, repeats, evaluators, budgets, primaryEvaluator?, strategy?}` → experiment.
*   `GET /api/lab/experiments`, `GET /api/lab/experiments/:id` (with trials, scores, spend).
*   `POST /api/lab/experiments/:id/{launch,stop,resume}` → experiment; `POST .../cells/:cell/{cancel,retry}`; `POST .../promote` `{variant}` → `{cataloguePatch}`.
*   `POST /api/lab/ratings` `{experimentId, form, trialIds}` → question.
*   `GET /api/lab/registry`, `GET /api/lab/examples?state=proposed`.
*   Errors: 400 invalid body, 404 unknown id, 409 wrong state.

#### 30. `packages/coordinator/lab/ui.ts` (Create)

*   `/lab` views from `packages/ui`; run `npm run check:design`.

#### 31. Tests (Create/Modify)

*   `tests/coordinator/lab-experiments.test.ts`: two concurrent reconciles launch one trial per cell; restart launches only missing trials; spend counted once; each budget action fires once; infra-failed and cancelled trials excluded; forms not averaged; correction replaces score; seeded shuffle stable.
*   Rewrite the skipped cases in `delegation.test.ts` and `delivery.test.ts` against `/api/lab/experiments`.

Stage 3 exit: A/B with repeated baseline shows spread; each budget action fires once; judge-to-owner agreement shown.

### Stage 4: strategies

#### 32. `config/lab/strategies/list.json`, `grid.json`, `propose.json` + `scripts/lab-strategy-list.ts`, `scripts/lab-strategy-grid.ts` (Create)

*   `propose` is an agent step; `list` and `grid` are scripts.

#### 33. `packages/coordinator/lab/experiments.ts` (Modify)

*   Stop rules (c)–(e), strategy step at iteration end.

#### 34. `tests/coordinator/lab-strategies.test.ts` (Create)

*   `strategy.json` parsed; `stop` ends loop with reason; `no_gain` fires after k flat iterations; finalists run held-out.

Stage 4 exit: a loop improves the primary score on held-out examples; restart neither relaunches nor double-counts.

### External (Pipe repo)

*   After Stage 1 item 18 has imported the live datasets: remove method-evaluation endpoints, recommendations and benchmark site; `pipe.md` notes `source` = Fleet step execution ID.

### Verification Plan

*   **Build**: `npm run build`; `cargo build -p executor` on Mac and WSL.
*   **Tests**: `npm test`; `cargo test`.
*   **Design**: `npm run check:design`.
*   **Stage exits** as listed under each stage.

--------------------------------------------------------------------------------

*Drafted with EGM*
