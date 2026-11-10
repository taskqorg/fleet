# Fleet Lab

* **Author:** Alex Kirs
* **Date:** 2026-11-10
* **Revision:** round 2. It applies the owner's answers to the round-1 open questions, adds Pipe (including moving its provider keys into Fleet), and adds the use-case registry.

## Overview

Fleet Lab is where the owner tests how Fleet's agents work. The thing under test is a **pipeline**: a chain of steps. Each step is one of three kinds:

* an **agent step**: any harness, profile and skill set;
* an **API step**: a call to an outside provider through a Fleet connection, which today means Pipe;
* a **script step**.

Lab runs pipelines in three kinds of experiment:

1. **Single run.** One pipeline over a set of examples.
2. **A/B.** Several variants on the same examples, compared.
3. **Improvement loop.** Run, evaluate, change the configuration with a chosen strategy, and run again until a stop rule fires.

Evaluators are scripts, judge agents, or the owner's ratings. Examples come from a **use-case registry** built from the use cases already written across the org's projects. Synthetic examples come first; examples taken from real Fleet tasks come later.

Pipelines are multi-step from the start. API steps go through **Pipe**, the prototype service that holds provider API keys and records each call. Agents reach Pipe through a thin MCP client that offers `web_search` and `web_extract`. This brief also plans how Pipe fits into Fleet, including moving Pipe's provider keys into Fleet as Fleet-managed connection secrets with rotation. The data foundation already says Fleet centralizes API connections (Q9 in `fleet-data-foundation.md`).

The old lab (`packages/coordinator/lab.ts`, `lab-ui.ts`, `/legacy/lab`) and its data are deleted, and `/lab` becomes the new screen. This brief fills the "laboratory ladder logic" non-goal in `fleet-data-foundation.md` and the "Laboratory view" future item in `fleet-operator-app.md`.

### Owner answers to the round-1 questions

| # | Question | Answer |
|---|---|---|
| 1 | One agent step, or multi-step from the start? | Multi-step from the start, with agent, API and script steps. API steps go through Pipe. |
| 2 | Where experiments and scores are stored | Undecided. This brief gives three options and a recommendation, under "Alternatives Weighed". |
| 3 | Where examples come from | A use-case registry built from the org's existing use cases. Synthetic examples first. |
| 4 | Improvement loop | Several strategies, configured flexibly. |
| 5 | Human rating | Start with a few basic forms. |
| 6 | Budget | Flexible, set per experiment. |
| 7 | Lab runs at `background` priority | Yes. |
| 8 | Old lab data | Deleted together with the legacy code. `/lab` becomes the new screen. |
| 9 | Where Claude runs | On both Mac and Windows. |
| 10 | Repeats per example | Several, configurable. |

### Glossary (one meaning per term)

| Term | Meaning |
|---|---|
| Pipeline | A Fleet definition of an ordered chain of steps, kept in `config/pipelines/*.json`. A run of it is a `pipeline_run` execution. |
| Step | One link in a pipeline: an agent step, an API step or a script step. Its run is a `pipeline_step` execution. "Step" always means a Fleet step, never Pipe's internal steps. |
| API step | A step that calls an outside provider through a Fleet connection, today always Pipe. The screen labels it "Pipe call: `web_search`". It never holds a key. |
| Pipe tool call | A `web_search` or `web_extract` call made from inside an agent step through the MCP client. Its cost counts toward that agent step. |
| Pipe | The service that talks to outside providers. A Pipe method is always written "Pipe method X@n". "Pipe" is never shortened to "pipe(line)". |
| Connection | Fleet's record of one outside provider: endpoint, grants, key pool and unit of cost. Example: the `pipe-read` connection. |
| Provider | The vendor behind a connection, such as Tavily or Meshy. |
| Key | A provider API key in a connection's key pool. Keys never appear on Lab screens. |
| Account | A harness subscription account (Codex, Claude). It never means an API key. |
| Use case | One registry entry: a kind of work taken from an org project's documents, such as `fleet:UC1` or `taskq:UC-07`. |
| Example / example set | One runnable Lab input drawn from a use case / a pinned selection of examples. Round 1 called these "case" and "case set"; both are renamed to avoid "case" vs "use case". An example is never a Fleet task. |
| Experiment (`E-…`) | One Lab study. Its kind is single, A/B or loop. |
| Variant | A pipeline plus overrides (profile, harness, model, skills, prompt, Pipe method, script parameters). It is named by its change, such as "baseline + skill X". |
| Baseline | The reference variant. |
| Trial | One variant on one example, repeated once: a `pipeline_run` execution with `experiment_id` and `variant` set, and `task_id` null. |
| Repeat | The same trial run again, to measure run-to-run spread. |
| Iteration | One round of a loop. |
| Strategy | The rule that makes the next variants. It has two parts, a generator and an allocator (defined under Decision 8). |
| Stop rule | One condition that ends an experiment. A strategy can only propose stopping. |
| Evaluator | Whatever produces a score: a script, a judge agent, or a human. |
| Rating form | The form a human rating takes: pass/fail, pick-best or scale. A rating question is one asked instance of a form. |
| Score | One result from one evaluator, for one trial or one group of trials. It records its form, and results of different forms are never averaged together. |
| Budget | A Lab limit, shown as scope + unit + "spent / limit (basis)". |
| Pipe limit / quota | Not budgets. A Pipe limit is Pipe's own admission refusal. Quota is a subscription window on an account. Both show up as reasons. |
| Promote | An explicit owner action that turns a winning variant into a new revision of a catalogue method (a recommendation). |
| Revision / `config_ref` / attempt | Revision: a definition changed. `config_ref`: the variant's hash. Attempt: a Lab retry of a failed trial. |

## Analysis

Seven reviewers looked at the concept in each of two rounds: design, codebase details, zoom-out, comprehension, implementation readiness, structure, and inventive catalyst. Round 2 covered the new scope.

| Reviewer | Round 1 | Round 2 |
|---|---|---|
| `egm-design` | completed | completed |
| `egm-codebase-details` | completed | completed |
| `egm-zoom-out` | completed | completed |
| `egm-comprehension` | completed | completed |
| `egm-implementation-readiness` | completed | completed |
| `egm-structural` | completed | completed |
| `egm-inventive-catalyst` | completed | completed |

### Still true from round 1

* Executions already carry `experiment_id`, `variant`, `parent_execution_id`, `harness`, `harness_version`, `model` and `config_ref` (`packages/coordinator/db/schema.sql:80-90`).
* Artifacts get lab scope when an experiment is set (`artifacts.ts:45`).
* The data foundation's rule A3 says lab runs are not tasks.

These round-1 fixes still apply:

* Add `experimentId`, `variant` and `accountId` to `createExecution` (`executions.ts:108`).
* Add a `rating` question kind (`schema.sql:38`).
* Add a `skills` field to profiles. Skills are off in every harness today (`native.rs:83`, `harness.rs:216`, `lab.rs:191`).
* Give each trial a new worktree at a pinned commit, with no shared-path links (`worktree.rs:58-92`).
* Make the loop a reconcile function that survives a restart (compare commit 8daff24).
* Show confounders such as host and harness version, but never block on them.
* Drop the old lab's invented limits.

### New in round 2: blockers

1. **Fleet agents cannot reach Pipe today** (design, codebase details, readiness).
   * Claude starts with `--strict-mcp-config`, and its MCP allowlist covers only `design-rules` and `fleet` (`crates/executor/src/harness.rs:225`, `:291-315`).
   * Codex gets `mcp_servers={}` (`native.rs:87`).
   * Both run with a Fleet-owned HOME, so `mcp.py install` (`mcp.py:170-176`) is never seen.
   * The only web route an agent has is the coordinator's single-step `web_search` (`harness.rs:307`). `web_extract` cannot be reached at all.
   * Claude's built-in web tools stay on, so a Codex-vs-Claude comparison measures different tools.
   * **Fix.** A profile grants the `pipe` connection. For each execution, the executor:
     * adds the `mcp.py serve` command to the harness's MCP config and allowlists `mcp__pipe__web_search` and `mcp__pipe__web_extract`;
     * passes the label `PIPE_TASK=<execution id>`;
     * reads the Pipe token from a host-only file at run time, never from the profile.
   * Every trial records its web route (pipe, built-in or none) as a confounder.

2. **Pipe's call records do not join to Fleet executions** (codebase details, design, readiness).
   * Fleet sends the pipeline run's UUID as `PIPE_TASK` (`pipe_search.py:27`, `projects.ts:14`). Pipe accepts only a number as a task. Any other label gives `task=null`, and the raw label goes into `source` (Pipe `lane/src/main.rs:444-462`).
   * Fleet stores the returned usage only inside the JSON `data` field (`projects.ts:65`), and `writeTotals` writes only `wall_ms` (`executions.ts:126`).
   * **Fix.** The label is the step's own execution ID, and the join key is `source`. A UUID fits Pipe's label rule of 64 characters from `A-Za-z0-9._:-`. Each API step writes a `connection_cost` measurement from `usage.credits`, with basis `measured` for coordinator-side calls and `reported` for calls made by an agent. Until Fleet matches these against Pipe's own records, the screen shows "Pipe record not matched".

3. **Deleting the old lab breaks unrelated code** (readiness).
   * `lab.ts` exports the profile functions `canonical`, `readTrustedProfile`, `profileDigest`, `listProfiles` and `validNativeProfile`. They are used by `delivery.ts:7`, `recovery.ts:5`, `agent-tools.ts:10`, `delegation.ts:3`, `app.ts:11` and tests.
   * `crates/executor/src/lab.rs` validates every profile (`main.rs:282`, `:310`, `:330`, `:382`).
   * The data foundation's §7 and its items 13 and 19 refer to lab trials.
   * **Fix.** Delete in three steps:
     1. Move the profile code into `profiles.ts` and `profile.rs` with no change in behavior.
     2. Remove `registerLab`, `lab-ui.ts`, the `/api/experiments*` routes, the `/legacy/lab` redirect and the lab tests.
     3. Edit the data foundation, `docs/experiments.md` and `docs/lab-coordinator-build.md` to match.
   * Old `experiment` documents are deleted. The new store must not reuse the document kind `experiment`.

4. **Asset creation through Pipe cannot run today** (design, codebase details, inventive catalyst).
   * Pipe refuses general provider dispatch (`assetq/README.md:9`).
   * Pipe's image worker refuses launches until it is qualified (`pipe.md:109`).
   * Meshy needs a job bound to an operator, refuses inside a TaskQ worker, and is asynchronous: submit, then poll (`assetq/src/meshy.rs:30-112`).
   * **Fix.**
     * Image making by an agent is an agent step on a Fleet host, not a call to Pipe's image worker.
     * Meshy is an API step of the asynchronous kind: submit, keep a durable job ID, poll, and on an uncertain outcome mark it "unknown, never replay".
     * Until the method is qualified, a trial that needs it ends with "method not qualified", and the experiment keeps going.
     * The first runnable pipelines use `web_search` and `web_extract`.

5. **Claude on Windows needs WSL work** (codebase details, readiness).
   * `harness.rs:140` allows Claude only when `cfg!(target_os="macos")`, so a WSL build, which targets Linux, is rejected.
   * `delivery_worker.py:98` checks `sys.platform` against `darwin` and `win32`, but WSL reports `linux`.
   * The executor uses Unix-only APIs (`harness.rs:4-7`, `:84`, `:201`), so a native Windows build does not compile. WSL is therefore the only path.
   * The Claude login code assumes the macOS keychain (`project-pipeline-prototype.md:15-21`).
   * **Fix.**
     * Replace the compile-time check with a per-host setting, `claude_placement: mac | windows-wsl`, which the readiness check verifies.
     * Detect WSL from `/proc/sys/kernel/osrelease`.
     * Store the WSL login as a file under Fleet's own config directory.
     * Use the same rule in `delivery_worker.py`.
     * Confirm with one real login-status check.
   * PDS18 (Linux) stays ineligible.

### New in round 2: major findings

6. **Pipe already contains a second Lab** (comprehension, inventive catalyst, structure). `pipe.md` and `assetq/docs/method-evaluation.md` define datasets, evaluators, comparisons, repeats, decision rules, credit budgets, recommendations and a benchmark site (ASSETQ-R9). Without a boundary, the owner gets two places to compare methods and two sources of recommendations. Recommendation: Fleet Lab owns experiments, and the Fleet catalogue owns recommendations. A Pipe method version is one override inside a Lab variant. Lab's example shape is a superset of assetq's dataset case (`id, text, attachments`), so Pipe datasets import without loss. See open question 1.

7. **Script steps have no input/output contract** (codebase details). `worktree::script` runs `sh -c`, cannot be stopped, sets only three environment variables, and keeps only the tail of the output (`worktree.rs:97-103`). The input and output folders are set only for agents (`main.rs:364-369`). Fix: add a `script` command kind. It runs in a process group that the stop command can kill, gets `FLEET_INPUT_DIR` and `FLEET_OUTPUT_DIR`, and has its output artifacts collected like an agent's. A timeout is optional and set per step by the owner. Lab sets no default.

8. **The pipeline runner accepts exactly one Pipe step** (codebase details, readiness).
   * It requires one `web_search` step on `pipe-read` (`projects.ts:54`) and hard-codes `steps[0]`.
   * It sorts errors by matching text (`projects.ts:64`), so a full Pipe queue and "no usable key" both become `unavailable`.
   * It writes legacy `pipeline-run` documents (`projects.ts:55`).
   * Fix:
     * Add pipeline format `version: 2`, with steps `{id, kind: agent|api|script, ...}`. An API step is `{connection, tool, arguments, mode: sync|job}`.
     * Keep reading `version: 1`.
     * Read the error code from Pipe's JSON `{error, reason}` body.
     * Write typed `pipeline_run` and `pipeline_step` rows.
     * Pin the client's SHA-256 in the connection record, since `pipe_search.py:19-26` loads it from a configured path without checking it.
     * Keep what works: request-ID idempotency, and "unknown, never replay".

9. **When Pipe fails, the variant must not look worse** (design, comprehension, readiness).
   * `queue-full`, 429, provider quota replies and `no-usable-key` are infrastructure failures. The trial gets a null score and a reason, and it is left out of comparisons.
   * An `unknown` outcome is never retried automatically, because a retry would pay twice.
   * A check before launch warns but never refuses.
   * Lab work can use a lot of Pipe credit. For example, 50 examples × 4 variants × 3 repeats × 10 searches is 6,000 calls. Cells that hit a quota reply are labelled "quota-affected".

10. **The registry will drift from its sources** (structure, inventive catalyst).
    * A survey found about 112 use cases spread over six repositories:
      * Fleet: UC1–UC11
      * taskq: UC-01–UC-17
      * pilotq: about 67 acceptance rows
      * agentq: A1–A7
      * dashq: DQ-01–DQ-10
      * assetq: datasets in `method-evaluation.md`
    * specq defines how use cases should be written. serverq and coreq have none.
    * Most of them describe product behavior ("show why it waits"), not agent work ("make a page").
    * Fix: each entry points to its source as `repo@commit#anchor` and stores that source's hash. It carries a `lab_fit` field (`work`, `behavior` or `none`). A drift check flags entries whose source changed and never blocks a launch.
    * IDs are `<project>:<source id>`, because Fleet UC1 and taskq UC-01 would otherwise clash. IDs are never renumbered.
    * Skip the copies of specq inside `dashq/.taskq/specq/`.

11. **Several strategies could become several engines** (inventive catalyst, zoom-out, structure, readiness). Fix: one interface. A strategy is a script or agent step. Its input is a snapshot of the experiment: variants, scores, spend and budget. Its output is `strategy.json`: `{action: next|stop, variants[{label, overrides}], reason}`. The coordinator's reconcile function runs it, and it never extends a budget. Adding a strategy means adding one step file. Decision 8 gives the menu.

12. **Budget units that Fleet can measure now** (all lenses).
    * Measurable today: trial count, wall-clock time, Pipe call count, and Tavily credits when Pipe reports them.
    * Tokens: Fleet parses them but does not total them (`harness.rs:367-385`), and some token counts cannot be added together (`harness.rs:603-613`).
    * Money: unverifiable until Q9.
    * A budget in a unit Fleet cannot measure shows as "unverifiable". It is never treated as zero.

13. **Rating forms cannot share one numeric score** (comprehension, inventive catalyst, readiness). A pick-best answer is a preference, not a number for one trial. Fix: store `form` on every score, never average across forms, and record a pairwise result against the pair or group of trials it compared.

14. **Copied designs carry made-up limits** (inventive catalyst, design). Pipe's evaluation sets 1–16 examples, 1–8 repeats, `max_jobs` 1–32 and `limit` 500 (`method-evaluation.md`). Copy the shapes, not the limits. Tavily's `max_results` of 1–20 is the provider's own rule, so it stays in the connection, not in the runner.

### Inventive tensions and how they resolve

| Contradiction | Resolution |
|---|---|
| Keys held centrally, yet agents call providers mid-step. | Agents hold only a Pipe token, and Pipe holds the provider keys. Fleet becomes the source of truth for those keys and pushes them to Pipe. Later, a scoped token per trial (LiteLLM virtual keys) lets Pipe stamp the trial itself, instead of trusting the label the agent sends. |
| Fleet should own the keys, but Pipe must keep serving when the control side is down (ASSETQ-R10, `pipe.md:52-63`). | Push, never pull. Fleet writes the encrypted key file that Pipe already restores from, and triggers a reload. Serving a request never calls Fleet. |
| Several flexible strategies, but no optimizer engine. | Split a strategy into a generator (what to try next) and an allocator (where the trials go), as in Optuna's sampler/pruner split. Each part is a small step behind one interface. |
| Several rating forms, but limited owner time. | One question kind with three forms, asked only where it pays off: where the judge disagrees with itself after an order swap, where a result is close to a decision, and on a small random sample to measure agreement. Results say "rated k of n", never "incomplete". |
| Flexible budgets, but the system never blocks. | Provider limits belong to the connection and are real outside limits. Lab budgets have an action: warn, pause launches (the default) and ask, or cancel the trial. Cancelling is opt-in. A projected overrun only warns. |
| A structured registry, but the prose documents keep changing. | The prose stays the source of truth. Entries hold pointers plus hashes, and a drift check warns. |
| Two benchmark places (Lab and Pipe). | One owner for experiments (Lab) and one for recommendations (the Fleet catalogue). Pipe comparisons are imported read-only and not run a second time. |

## Pipe in Fleet

### What exists

**Pipe's read lane** runs on PDS18, behind Cloudflare.

* It serves `web_search` and `web_extract` through Tavily.
* It loads provider keys through systemd `LoadCredential` (`pipe.md:54`).
* When a key gets 401, 402 or 403, it disables that key, keeps it disabled across restarts and raises an alert.
* It switches to the next key only when a call gets no HTTP answer. Quota replies and 429 pass through unchanged.
* A full queue refuses at once.
* It records every call: product, operation, duration, outcome, step, route, a 12-character key fingerprint, the source label, runtime, units and host.
* It authorizes project tokens through a published grant snapshot that stores only hashes (`pipe.md:55`).
* An encrypted SOPS/age copy of the keys is kept off-host for restore (`pipe.md:69`).

**Fleet** calls the lane from the coordinator through `pipe_search.py`, using one `web_search` step.

### What moves into Fleet, and what stays separate

| Moves into Fleet | Stays in Pipe (a separate service) |
|---|---|
| The **connection registry**: `pipe-read` and later `meshy`, with each host's endpoint (loopback on PDS18, the Cloudflare entry elsewhere), grants, cost unit and the pinned client hash. | The **read lane as a running gateway**. It is hardened, built to keep serving when the control side is down, and moving it into the coordinator would make the coordinator a single point of failure for every agent's web reads. |
| The **provider key inventory and rotation policy** (the key move below). Fleet is the source of truth. | Holding the keys **at run time** (`LoadCredential`), plus its failover and disable-on-401/402/403 behavior. |
| **Call records as executions**: each API step is a `tool_run` with a `connection_cost` measurement, and calls made from agents are stored as child `tool_run` rows pulled back from Pipe's records. | Pipe's own call record, which feeds CoreQ. Fleet reads it; it is not a second record Fleet writes. |
| **Experiments, comparisons and recommendations** (Lab and the catalogue). | Pipe's asset storage and asset site, already rejected as Fleet's artifact store (data foundation A8d). Pipe's generation worker, until it is qualified. |
| **Coordinator-side API steps** for providers that do not need the read lane (Meshy), with keys held in the coordinator. | None for these. |

### The key move: Pipe's provider keys become Fleet connection secrets

The owner allows moving Pipe's provider keys into Fleet. This section describes the move only. No key or token values were read during the review, and none appear here.

1. **Where keys live in Fleet.**
   * Every provider key is a secret of its connection's key pool.
   * It is stored in the coordinator's private secret directory (mode 0600, owned by the coordinator user), in the same pattern the data foundation uses for store keys.
   * Postgres holds only metadata: key ID, 12-character fingerprint, connection, state (`active`, `draining`, `disabled`, `retired`), added date and disable reason. It never holds the value.
   * The encrypted SOPS/age backup becomes Fleet's, kept for restore.
   * Executors, agents and profiles never receive provider keys.
2. **How keys reach Pipe.**
   * Fleet writes the read lane's credential file, the same file Pipe restores from today, and triggers a reload.
   * Pipe keeps loading keys through `LoadCredential`, so it still never calls Fleet while serving.
   * After a push, Fleet reads back the set of fingerprints Pipe has loaded and shows any mismatch.
3. **Rotating a provider key** (no downtime):
   1. Add the new key to the pool and push. Both keys are now served.
   2. Mark the old key `draining`, so Pipe stops choosing it for new calls.
   3. After the owner confirms, or once the old key has had no calls for an observed period, mark it `retired` and push again.
   * "Rotation" never means round-robin across keys. Pipe's failover (switching only when a call gets no HTTP answer) and its disable-on-401/402/403 stay exactly as they are.
   * A key that Pipe disabled shows up in Fleet with its reason. Re-enabling it is an owner action in Fleet.
4. **Rotating a Pipe project token** (Fleet's credential for Pipe):
   * Issue the new token and publish a grant snapshot that accepts both token hashes.
   * Switch Fleet's host files over to the new token.
   * Revoke the old token by publishing again.
   * A revocation counts as done only when Fleet sees a snapshot newer than the revocation.
5. **Keys for coordinator-side connections** (Meshy, and later direct model APIs) are read by the coordinator at the moment it makes the call. The call is recorded as a `tool_run`. These connections do not go through the read lane.
6. **Contract change.** This changes Pipe's contract: Pipe's credentials are now Fleet-managed and pushed to it. It must be written into `pipe.md` as well as here.
7. **Rollback.** The last pushed credential file and the SOPS/age backup still work without Fleet. Pipe can be run by hand from them.

### Staging for the Pipe track

This track runs alongside Lab, and Lab does not wait for it.

* **P0: Lab calls Pipe as it is.**
  * The label becomes the step's execution ID.
  * `pipe_search.py` takes the tool name, so `web_extract` works.
  * Each API step records a `connection_cost` measurement.
  * Agents get the Pipe MCP client, as described in blocker 1.
  * Exit: one trial shows its Pipe calls and credits, both for API steps and for calls made from inside agents.
* **P1: the connection registry.**
  * Connection records live in Fleet: endpoints, grants, pinned client hash.
  * Error codes are read from Pipe's JSON body.
  * Exit: `/connections` shows `pipe-read` with its state and spend.
* **P2: the key move.**
  * The key inventory and the SOPS/age backup move into Fleet.
  * Fleet pushes keys to Pipe and reads back what Pipe loaded.
  * Provider key rotation and Pipe token rotation work as described above.
  * Exit: one provider key and one Pipe token are rotated with no failed calls, and a key disabled by Pipe appears in Fleet.
* **P3: coordinator-side API steps** with Fleet-held keys (Meshy, as an asynchronous job), plus scoped tokens per trial or per experiment.
  * Exit: an asset pipeline runs Meshy through Fleet, and every Pipe call made by agents is attributed by token rather than by label.
* **P4 (optional): merge or retire the read lane.** Decide only after P3, from observed availability (see open question 2).

## Goals vs. Non-Goals

**In scope**

* Multi-step linear pipelines from the start, with agent, API (Pipe) and script steps. Steps pass data only through artifacts.
* One experiment model: examples × variants × repeats, with kinds single, A/B and loop.
* A use-case registry with pointers to its sources, and examples drawn from it. Synthetic examples first.
* Pluggable strategies behind one step interface, and several stop rules.
* Three rating forms, judge agents and script evaluators.
* Budgets set per experiment, in several units, each with an action.
* `background` priority for Lab runs.
* Deleting the old lab.
* Claude on Mac and on Windows through WSL.
* The Pipe track, including the key move.
* Promoting a winning variant into the catalogue.

**Out of scope**

* A Lab-only pipeline format or Lab-only execution kinds.
* Examples stored as `task` rows.
* Concurrency caps or limits invented by Lab.
* Branching or fan-out inside a pipeline.
* A combined "cost score".
* Deploying LiteLLM, Portkey or OpenRouter as components. Fleet copies their key-pool, virtual-key and budget shapes instead.
* Elo ladders and rubric builders.
* Automatic import of use cases from other repositories, until the hand-built registry proves useful.
* Native Windows (non-WSL) execution.
* Keeping or migrating old lab data.

## Alternatives Weighed

### Storage for experiments and scores (owner question 2)

* **A. Keep JSON documents and put scores inside them** (as the data foundation does today).
  * For: no schema work.
  * Against: a loop makes each document grow without bound. Every write takes the global write lock. Scores cannot be joined with spend or corrected without rewriting the whole document.
  * Fits only a throwaway first try.
* **B. Two small Postgres tables, `experiment` and `score`.** Trials stay executions; example sets and variant configurations are stored as hashed content. **(Recommended.)**
  * For: scores join with executions and spend. A correction is a new row that replaces the old one. The restart-safe reconcile is a plain query. Rollback is dropping the two tables.
  * Against: two migrations and one change-log entry.
* **C. Control in Postgres, scores as ClickHouse events.**
  * For: fast analytics across many experiments.
  * Against: corrections and human ratings are awkward in an append-only store. It waits on Phase 6. The loop would have to read two stores.
* **In plain words:** use B now. When ClickHouse arrives, copy scores into it for reports, behind the same API.
  * Fields of `experiment`: kind, pinned example-set hash, priority, budget, strategy, state, stop reason.
  * Fields of `score`: trial or group, evaluator and its version, form, value, source, a link to the judge's execution or the rating question, rationale, and which earlier score it replaces.

### Other choices

* **Move Pipe into Fleet before building Lab.** Rejected. Lab can call Pipe as it is, the contract Lab depends on (a labelled call that returns units) does not change, and the move becomes its own track.
* **Run the read lane inside the coordinator now.** Rejected for now, because the coordinator would become a single point of failure for agent web reads. Reconsider at P4.
* **Adopt LiteLLM or another gateway.** Rejected. Lab's agent steps use subscription harnesses, not API keys. The API steps named so far are Tavily and Meshy. A gateway would add a second ledger of every call. Copy its shapes instead.
* **A strategy engine with built-in algorithms.** Rejected in favor of a generator/allocator interface, where each strategy is a step.
* **A registry that copies the use-case text.** Rejected because the copies would drift. Entries hold pointers, hashes and only the fields Lab needs.
* **Lab replaces Pipe's evaluation at once.** Deferred to open question 1.
* **Examples as tasks**, and **external eval tools as dependencies**: rejected in round 1. Those reasons still hold.

## Key Decisions & Staging for Next Steps

### Decisions

1. Lab reuses the execution, artifact and question entities, plus the pipeline format (version 2). Examples are never tasks.
2. Pipelines are linear and multi-step from day one. Steps pass data only through artifacts. API steps go through Fleet connections and never hold keys.
3. A variant is a label plus an immutable `config_ref` hash. Every change makes a new variant. Promoting a variant is a separate, explicit owner action.
4. Storage follows option B: typed `experiment` and `score` tables.
5. The registry lives in the Fleet repo, one file per project, as `config/lab/use-cases/<project>.json`. One entry has:
   * `id` (`<project>:<source id>`) and `project`;
   * `source {repo, path, anchor, commit, sha256}` and `source_status`;
   * `actor`, `goal`, `initial_state`, input types, `expected_outcome` and exceptions;
   * `checks [{kind: script|judge|human, ref, metric}]`;
   * `lab_fit` (`work`, `behavior` or `none`);
   * `pipeline_hint`, `tags`;
   * `maturity` (indexed, example drafted, synthetic ready, calibrated, linked to a real task).
6. Examples are drawn from the registry.
   * One example has: `id`, `use_case_id`, `version`, `origin` (`synthetic` or `task:<id>`), fixture (a repo at a SHA, or none), prompt, input artifacts, optional reference artifacts, bound checks, `split` (`tune` or `held_out`) and a hash.
   * For each use case with `lab_fit` other than `none`, a drafting agent proposes 1–3 synthetic examples and the owner approves them. Examples:
     * `fleet:UC1`: make page X in a fixture repo at a pinned SHA.
     * `taskq:UC-07`: split a plan into tasks; a script checks for cycles and duplicates.
     * `pilotq:L08`: implement retry-after handling; its test is the check.
   * Examples are hashed into the experiment at launch.
7. Ratings use one `rating` question kind with three forms:
   * `pass_fail`, with reason tags;
   * `pick_best` over N outputs, shown blind and in random order. Pairwise is the case N = 2.
   * `scale` (1–5 with a note), for single-variant runs and for checking the judge.
   * Ratings never block. Questions are grouped by experiment, and rating works from the keyboard.
8. Strategies are configured as follows:
   * Config shape: `{generator: {kind, params}, allocator: {kind, params}, tune_examples, held_out_examples, stop: [...]}`.
   * Generators in the first loop stage:
     * `list` (also covers A/B);
     * `grid` (every combination of the listed changes);
     * `propose` (an agent reads scores and the transcripts of losing trials and writes the next overrides with a reason, OPRO/DSPy style). With `keep_best: true` it climbs from the current best variant.
   * Allocators: `full` first, `halving` (successive halving over examples) next.
   * Deferred: `reflect` (GEPA style), `evolve` and `thompson` (a bandit).
   * Adaptive allocators apply only to tuning examples. Finalists always run on every held-out example.
9. Stop rules are checked in this order, and the first one that matches is recorded as the stop reason:
   1. the owner stops the experiment;
   2. a budget is reached;
   3. the iteration count is reached;
   4. no gain beyond the baseline's spread for k iterations;
   5. the strategy proposes stopping.
10. Budgets are configured as follows:
    * Shape: `{unit: trials|wall_ms|pipe_calls|pipe_credits|tokens|money, scope: experiment|iteration|trial, limit, action: warn|pause_launch|cancel_trial}`.
    * The default action is `pause_launch` plus a question to the owner.
    * More than one budget can be set per experiment.
    * A unit Fleet cannot measure shows as "unverifiable".
11. Repeats are set per experiment. They are part of each launch's request ID, together with experiment, iteration, example, variant and attempt. The matrix shows the spread across repeats.
12. Lab runs carry `background` priority. Until the scheduler exists this has no effect, and the screen says so.
13. Claude runs on Mac and on Windows through WSL, using the per-host `claude_placement` setting.
14. Pipe's provider keys become Fleet connection secrets. Fleet pushes them to Pipe, and Pipe keeps serving on its own.

### Staging

The Lab stages build on each other. Stages 1 and 2 can run in parallel. The Pipe track (P0–P4, above) runs alongside, and P0 is needed by Stage 2.

* **Stage 0: groundwork.**
  * The round-1 fixes: `createExecution` fields, the `rating` question kind, the `experiment` and `score` tables, the `skills` field, worktrees for each trial.
  * Delete the old lab in three steps.
  * Replace the Claude platform check with `claude_placement`.
  * The `script` command kind.
  * Exit:
    * one hand-made trial can be queried by experiment and variant, has `task_id` null and lab artifacts, and does not appear in Tasks;
    * `/legacy/lab` is gone, and delivery and recovery still pass their tests;
    * Claude passes its readiness check on the WSL host.
* **Stage 1: registry and examples.**
  * Index all about 112 use cases with `lab_fit`.
  * Draft synthetic examples for at least five `work` entries. At least one of them uses an API step and one uses a Pipe dataset imported from assetq.
  * Add the drift check.
  * Exit: the drift check flags a changed source, and every drafted example runs its script checks with no agent involved.
* **Stage 2: multi-step pipelines.**
  * Pipeline format version 2: agent, API (Pipe, sync and job) and script steps, with artifacts passed between steps.
  * Typed `pipeline_run` and `pipeline_step` rows.
  * Pipe failures classified as infrastructure failures.
  * Exit:
    * a three-step example (`web_search` → agent summary → script check) completes on the Mac and on WSL;
    * a failed step stops the steps after it and keeps its reason;
    * Pipe calls join their trial.
* **Stage 3: experiments.**
  * N variants × examples × repeats.
  * The three rating forms, plus an optional blind judge with the order swapped.
  * Budgets in trials, time, Pipe calls and Pipe credits.
  * The `/lab` results matrix: rows are examples, columns are variants. Each cell shows state, score with its form, spend with its basis, time, confounders and a link to the execution.
  * Promote.
  * Exit:
    * an A/B with a repeated baseline shows the spread;
    * each budget action fires exactly once;
    * judge-to-owner agreement is shown.
* **Decision gate.** Is the judge close enough to the owner's ratings, and is the noise acceptable? The answer sets which generators are worth running unattended.
* **Stage 4: strategies.**
  * The generators `list`, `grid` and `propose` (with `keep_best`), and the allocators `full` and `halving`, all behind one interface.
  * Stop rules 1–5.
  * Exit:
    * one loop improves the score on held-out examples;
    * a coordinator restart neither launches trials again nor counts spend twice.
* **Stage 5: real tasks and full budgets.**
  * Examples snapshotted from finished tasks.
  * Token budgets once totals are filled.
  * Money budgets after Q9 and the Pipe track's P3.
  * The deferred strategies, as wanted.
  * Exit: a snapshot of a real task runs next to synthetic examples of the same use case, in a clean environment (Phase 5, G8).

### Follow-up documents

* A `fleet-lab.md` design covering:
  * the `experiment` and `score` schemas;
  * the example and registry formats;
  * pipeline format version 2;
  * the `strategy.json` and `score.json` contracts;
  * the Laboratory view.
* A `connections` design for the Pipe track, including the key move. Matching edits to `pipe.md`: key ownership, label semantics, push-based credentials.
* Edits to the data foundation: remove the lab-trial row from §7 and the lab parts of items 13 and 19, and add the two tables to the change log (operator app D5).
* Add stable anchors to `docs/use-cases.md`, plus Source and Status lines. Propose to specq the shared rule "use cases carry stable anchors".
* Add this brief to `docs/designs/README.md`.

## Open Questions for the Owner

Each question has a recommended answer.

1. **Does Fleet Lab replace Pipe's own method evaluation (datasets, evaluators, comparisons, recommendations, the benchmark site), or do the two coexist?**
   *Recommended:* Lab replaces it in stages. Pipe datasets import into the registry in Stage 1. New comparisons run in Lab from Stage 3. The Fleet catalogue becomes the only source of recommendations, and Pipe shows a read-only copy. Retire Pipe's comparison endpoints after that.

2. **What is the read lane's long-term home: a separate service fed by Fleet, or part of the coordinator?**
   *Recommended:* a separate service, with Fleet owning its keys and configuration. It stays up when the coordinator is down. Revisit at P4 with real availability data.

3. **How should Pipe calls made by agents be attributed: a label the agent sends, or a scoped token Fleet issues per trial?**
   *Recommended:* the label (the step's execution ID) now, because it costs almost nothing. Scoped tokens come at P3, when Fleet owns the keys and can issue tokens without republishing Pipe's grants for every trial.

4. **Should Claude's built-in web tools be off in Lab variants, so every variant goes through Pipe?**
   *Recommended:* off by default, so variants are comparable and every call is recorded. "Web route: built-in" stays available as an explicit override, so it can itself be A/B-tested.

5. **Rename "case" to "example" everywhere?**
   *Recommended:* yes. "Use case" is the registry entry, and "example" is the runnable input. This matches the "examples" wording already in UC2 and UC5.

6. **Who drafts examples from use cases?**
   *Recommended:* a drafting agent proposes 1–3 examples per use case with `lab_fit` set to `work` or `behavior`. The owner approves each one with a `pass_fail` rating. Approved examples become `synthetic ready`.

7. **What should a budget do by default when it is reached?**
   *Recommended:* `pause_launch` plus a question: running trials finish, new ones wait for the owner. `cancel_trial` is opt-in.

8. **Is "Claude on Windows" WSL only?**
   *Recommended:* yes. The executor does not compile for native Windows, and WSL matches the data foundation's other Windows decisions.

9. **Where do provider keys sit in Fleet: the coordinator's 0600 secret directory, or a secret manager such as Vault?**
   *Recommended:* the secret directory plus the SOPS/age backup Fleet takes over from Pipe, with metadata only in Postgres. Vault adds a service for one owner with no current need.

10. **Which pipelines run first?**
    *Recommended:*
    * web research (`web_search` → `web_extract` → agent summary → script check);
    * a page made by Codex vs Claude, with and without a skill;
    * an asset pipeline with Meshy once it is qualified. Until then it is shown as "method not qualified".

## Owner Decisions, Round 3 (2026-11-10)

These answers close the open questions above. Where they conflict with earlier text in this brief, these win.

1. **No gradual staging for removal.** Fleet is an early prototype. Remove the old lab, the legacy pages and the old Pipe method evaluation in one cut. No compatibility shims and no read-only legacy period. Code that delivery, recovery or delegation still imports from `lab.ts` or `lab.rs` moves to its new home in the same change.
2. **Web search is an ordinary API connection.** It gets the same accounting as every other API call. Callers can reach any connection in several ways: the Pipe HTTP endpoint, or MCP servers and tools served from Fleet infrastructure. Web search is not a special lane.
3. **Linking Pipe calls to runs:** accepted as recommended. Use a step-id label now, and per-run tokens issued by Fleet later.
4. **Claude built-in web:** accepted as recommended. It is off by default. Turning it on is an explicit setting that an A/B experiment can compare.
5. **Terms:** an "experiment" is the whole comparison. An "example" is one input item in the task set.
6. **Examples from use cases:** an agent proposes them and the owner approves each with a pass/fail rating.
7. **Budget action:** configurable. Both "pause and ask" and "stop" are supported.
8. **Claude on Windows:** WSL only. Claude runs on both the Mac and Windows.
9. **Provider keys:** accepted as recommended. They live in the coordinator's 0600 secret directory with an encrypted backup; Postgres holds metadata only.
10. **First pipelines:** accepted as recommended.
