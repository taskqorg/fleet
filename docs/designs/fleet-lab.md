# Fleet Lab

Fleet Lab is where the owner tests how Fleet's agents work, on `/lab` and through the `lab_*` operations. The thing under test is a **pipeline**: an ordered chain of agent, API and script steps. Lab runs a pipeline over **examples** in an **experiment**, scores the results with scripts, judge agents or owner ratings, and lets the owner promote a winning variant into the project's recommendation. Lab also owns work analysis: the same retained history, queried to find what to improve.

Lab adds no run machinery of its own. A trial is a `pipeline_run` execution with `experiment_id` and `variant` set and `task_id` null; its steps are `pipeline_step` executions; each provider call is a `tool_run`. Outputs are lab-scope artifacts. Owner ratings are questions. Lab's own storage is three tables (`experiment`, `score`, `experiment_note`), two columns (`execution.request_id`, `execution.reuse_key`), the use-case registry and example seeds in `config/lab/`. The work query and the price table are in [fleet-analytics.md](fleet-analytics.md).

## Glossary

| Term | Meaning |
|---|---|
| Coordinator | Fleet's TypeScript server (`packages/coordinator`). Owns Postgres, routes, reconcile. |
| Executor | Fleet's Go process on each host (`go/executor`). Runs harnesses, scripts, worktrees. |
| Harness | An agent CLI the executor drives: Codex, Claude, Antigravity. |
| Profile | A trusted JSON file in `config/agents` naming harness, model, skills and granted connections (`listProfiles` in `go/coordinator/projects.go`; one validator, `contract.ParseProfile` in `go/contract/profile.go`, schema `contracts/profile.schema.json`). |
| Connection | Fleet's record of one outside provider endpoint, owned by [fleet-connections.md](fleet-connections.md). Example: `tavily`. |
| Catalogue / recommendation | `config/catalogue.json` lists methods per project; `recommendation_set` records the chosen method as a `project-recommendation`. |
| Use case | A registry entry pointing at a use case written in an org repo. Current sources: Fleet, csgo, desiign; replaced prototypes (taskq, pilotq, agentq, dashq, assetq) keep their entries marked with `status` and `maps_to` ([asset pipelines](fleet-asset-pipelines.md#3-registry-s3)). |
| Example | One runnable Lab input drawn from a use case. Never a task. |
| Experiment | One whole comparison: examples × variants × repeats. Kind `single`, `ab` or `loop`. |
| Variant | A label plus overrides of the pipeline; hashed into `config_ref`. |
| Trial | One variant on one example, one repeat. |
| Cell | `<example id@version>|<variant>|<repeat>`; its trials differ by attempt. |
| Attempt | Retry counter of a cell. Starts at 0; raised only by the owner's "retry cell". |
| Lab group | The set of experiments that test one step of one use case; the comparison screen groups by it. Not the `fleet.md` end-to-end use case. |
| Suite | A sorted set of `example id@hash`. Two results compare only on the same suite and evaluator version. |
| Experiment record | Goal, hypothesis, decision rule and decision of an experiment. Not the system audit journal. |
| Production reference | Numbers a project reports for its current step, outside Fleet. Context only, never a comparison row. |
| Baseline variant | The variant that runs the current method inside Fleet; every other variant is compared with it. |
| Noise floor | The spread of the baseline variant over its repeats. |
| Coverage | confident answers / all answers. Escalation share = 1 − coverage. |
| Selective accuracy | correct confident answers / confident answers. |
| Confidence band | Thresholds in the variant config (so hashed) that split a typed answer into confident yes, confident no, and the middle band that abstains or escalates. |

## Owner decisions

| # | Decision |
|---|---|
| R3.1 | No staged removal: the old lab, legacy pages and Pipe's method evaluation went in one cut; no shims, no read-only period, no old data kept. |
| R3.2 | Web search is an ordinary API connection with the same accounting as every call. No special lane. |
| R3.3 | Provider calls link to runs by a step-ID label now; per-run tokens issued by Fleet later. |
| R3.4 | Claude built-in web tools are off by default; turning them on is an explicit variant setting. |
| R3.5 | "Experiment" is the whole comparison; "example" is one input item. |
| R3.6 | An agent proposes examples; the owner approves each with a pass/fail rating. |
| R3.7 | Budget action is configurable: "pause and ask" and "stop" are both supported. |
| R3.8 | Claude on Windows is WSL only; Claude runs on the Mac and on Windows. |
| R3.9 | Provider keys live in the coordinator's 0600 secret directory with encrypted backup; Postgres holds metadata only ([fleet-connections.md](fleet-connections.md)). |
| R3.10 | First pipelines: web research; Codex vs Claude page with and without a skill; Meshy asset pipeline once qualified. Asset pipelines, judges per use case, `loop` and the character pipeline: [fleet-asset-pipelines.md](fleet-asset-pipelines.md). |
| Q1 | Storage is Postgres tables, not JSON documents and not ClickHouse: `experiment` (with its [record](#experiment-record) columns), `score`, and the append-only `experiment_note`. Amended 2026-11-11: the record needs typed columns and notes; a journal artifact would be a legacy stopgap. |
| Q2 | Closed with nothing to convert: Pipe's assetq database held 0 dataset revisions (checked at the Lab Stage 1 deploy), so the importer was deleted with Pipe ([connections](fleet-connections.md) C4). |
| Q3 | No coded judge-to-owner agreement threshold; the screen shows agreement and the owner decides when loops run unattended. |
| — | Lab runs at `background` priority. Repeats per example are configurable. Rating starts with a few basic forms. |

Not adopted: Lab-only execution kinds or pipeline formats; examples as `task` rows; concurrency caps, default timeouts or other limits Lab invents (provider limits stay in the connection); branching or fan-out inside a pipeline; a combined "cost score", Elo ladders or rubric builders; a gateway (LiteLLM, Portkey, OpenRouter); automatic import of use cases; native (non-WSL) Windows; the coordinator writing `config/catalogue.json`; automatic rating sampling.

## Entities

| Lab term | Stored as |
|---|---|
| Trial | `execution` kind `pipeline_run`, `experiment_id`, `variant`, `config_ref`, `request_id`, `task_id` null |
| Step run | `execution` kind `pipeline_step`, parent = trial |
| Provider call | `execution` kind `tool_run`, parent = step, `connection_cost` measurement |
| Judge or drafting run | `execution` kind `pipeline_run`, `experiment_id` set, `variant` null |
| Infrastructure failure | `execution.problem` with prefix `infra:` |
| Outputs, transcripts, examples | `artifact`, scope `lab` |
| Owner rating | `question` kind `rating` |
| Experiment, score | tables `experiment`, `score` (`go/coordinator/schema.sql`, migration 11) |
| Pipeline | `config/pipelines/*.json`, version 2 |
| Use case | `config/lab/use-cases/<project>.json` |

```text
experiment ─┬─< execution(pipeline_run, variant)  = trial
            │      └─< execution(pipeline_step) ─< execution(tool_run, connection_cost)
            │      └─< artifact(scope=lab)
            ├─< execution(pipeline_run, variant=null)  judge / drafting
            ├─< score (trial_ids[])
            └─< question(kind=rating) ── answer ──> score
```

## Data contracts

`experiment`: project, kind (`single|ab|loop`), pipeline ID, pinned examples (`[{id, artifact_id, hash, split}]`), variants (`[{label, overrides, config_ref}]`), repeats, evaluators, primary evaluator (required for `loop`), strategy (loop only), budgets, state, stop reason. Iteration is derived from trials. Row-level security uses the same `fleet.project_ids` policy as `task`; changes reach the change feed as kind `experiment`.

`score`: experiment, `trial_ids[]` (one for a trial score, N for `pick_best`), evaluator name and version hash, form (`metric|pass_fail|pick_best|scale`), value, source execution or question, rationale, `replaces_score_id` (a correction is a new row). Change-feed kind `score`.

States: `draft → running ⇄ paused → stopped | done`. Variants and examples are fixed at create. Not built: editing them in `draft`. Retrying a cell of a `done` experiment returns it to `running`.

Variant `config_ref` = SHA-256 of canonical `{pipeline, overrides}`. Trial `request_id` = `lab:` + SHA-256 of `[experiment, iteration, example, variant, repeat, attempt]`, unique (`execution_request`). Neither identifies the work across experiments; that is the [reuse key](#reuse-key).

## Experiment record

The record turns an experiment into a decision. Columns on `experiment`: `goal`, `hypothesis`, `primary_evaluator`, `decision_rule`, `lab_group`, `decision` (`adopt <variant> | keep baseline | inconclusive | pending`), `decision_reason`, `decided_by`, `decided_at`.

- **Frozen at launch.** Launch is refused (400) without `goal` and `hypothesis` (`lab_experiment_create` takes them with `decisionRule` and `labGroup`; `primary_evaluator` and `decision_rule` become required at launch with the statistics function, stage 4). From launch on (`launched_at`), these fields, the variants and the suite are fixed.
- **Amendments are visible.** A change after launch is an `experiment_note` row `{experiment_id, kind: amendment | note, field, old, new, text, author, at}`, append-only (a trigger refuses update and delete), written by `lab_experiment_note` and listed as `notes` by `lab_experiment_get`; the experiment page shows it with its time relative to the first trial result (not built). Notes are never edited or deleted.
- **Decision.** At close the coordinator applies the frozen `decision_rule` to the [statistics](#statistics) and writes `decision` with `decided_by` null as the proposal (until stage 4 it writes `pending`). An agent may write `decision_reason`. A person confirms or overrides with `lab_experiment_decide {experimentId, decision: adopt <variant> | keep baseline | inconclusive, reason?}` once the experiment is stopped or done; an override of a decision already made needs a reason and is an amendment; the same decision again changes nothing. A trigger on `experiment` sets `pending` on every stop or completion and stamps `launched_at`.
- An automatic stop (budget `stop`, owner stop) leaves `decision: pending`.
- Scores outlive their experiment: `score.experiment_id` has no foreign key (migration 26), so a deleted experiment cannot erase evidence a decision cited. Deleting an experiment with a decision is refused (409); no delete operation exists yet.
- Record kinds stay distinct: a trial is a run; a score is an assessment; Promote writes the recommendation. The decision is the experiment's own field and is what the [model-selection skill](fleet-pm-agent.md#9-pm-profile) cites.

## Statistics

One Go function computes every comparison: the experiment matrix, the Lab group comparison, `analytics_query` consumers and later gates. No second implementation. It is `go/coordinator/statistics.go` (`compare`, `typedStats`, `bandAnswer`); `lab_experiment_get` returns `comparison`, each non-baseline variant against the first on the primary evaluator, over `held_out` examples when the suite has any. The interval is a t interval over the per-example differences; `FLEET_LAB_MIN_EXAMPLES` (default 2) is the "too few examples" threshold.

- Input: two variants on one suite, one evaluator version. Output: per-example paired difference of the primary evaluator, its mean, a 95% interval clustered by example, and `n` (examples), always printed.
- When `n` is too small for the interval to mean anything, the output says "too few examples" and no interval is drawn. The threshold is a setting with a permissive default.
- Repeats of a near-deterministic variant (repeat spread 0 on the noise floor check) count as one.
- Typed-answer variants also report coverage, selective accuracy and escalation share, per example tag (for example language).
- Bands are tuned on `tune` examples; results are reported on `held_out` only. Bands are the variant override `bands: {yes, no}` (0 <= no <= yes <= 1, part of `config_ref`); without them every answer is confident at 0.5. The typed evaluator `{kind: typed, ref: labels, form: typed}` scores a trial from its output `p` through its variant's bands against the example's `references/expected.json` `{expected: yes|no, tag}` (score value `{answer: yes|no|abstain, label, p, tag}`; 1 when the answer equals the label, else 0, in comparisons); a trial it cannot score (no `p` in 0..1 in the last step's output, no yes/no label) gets `{answer: unscored, reason}`, visible and never counted. `lab_experiment_get` returns `typed`: per variant and split (`tune` to tune bands, `held_out` to report), coverage, selective accuracy and escalation share per tag and an aggregate row (`aggregate: true`; a tag may be any string). A direct pipeline run refuses `bands`, and Promote leaves them out of the method with a note (a method run applies no scoring setting); a typed score cannot be corrected (it follows from p, bands and label). Each band setting is its own variant, so its own trials (the bands are part of the variant config); the reuse key (stage 7) does not merge them.
- Rows from different suites or evaluator versions are never averaged; the screen shows them side by side with the reason.

## Reuse key

`execution.reuse_key` = SHA-256 of canonical `[pipeline content hash, overrides, resolved model ID per step, harness version, example content hash, evaluator version, repeat index]`. A resolved model ID is the one the provider answered, never an alias. Pipeline content is hashed, not its file name.

- Before inserting a cell, reconcile looks for a `completed` trial with the same key in the same project and reuses it by reference instead of running it again. Any change to the inputs gives a new key, so fresh trials run.
- An experiment may re-run a sample of reused trials (`reuse_check: {share}`, off by default). When a re-run differs by more than the noise floor, every trial with that key is marked `stale` and no longer reused.
- The cross-experiment comparison reads trials by reuse key and suite; it is built after the key.

## Project data in examples

A project's raw data (names, texts, translations) is stored in Fleet as ordinary `inline` example artifacts (owner answer 2026-11-11). Preparation comes first: examples, labels, the project's export and Lab experiments on stored data; no test task runs on the project itself until the owner says so.

Trial outputs of typed decisions are `{answer, p, answered_by, tokens}`. A project export follows a versioned schema: step ID, row ID, input, label, tag (for example language), production cost and basis, split. Version 1 is [`contracts/lab-export.schema.json`](../../contracts/lab-export.schema.json) (frozen; a change is a new version). `scripts/lab-examples.ts from-export FILE --use-case ID` turns each row into an inline example (origin `export:<project>`, prompt = input, references = a certain `output.json` and `expected.json` with the label, tag and production cost, one check that the trial's `p` is on the label's side of 0.5); `import` then stores them. Its production numbers become the production reference, never a comparison row.

Not adopted: fetching example text at run time or importing externally run trials; neither earns its place while project data may be stored.

## Pipeline format version 2

```json
{"version":2,"id":"web-research","steps":[
  {"id":"search","kind":"api","connection":"tavily","tool":"web_search","arguments":{"query":"${example.prompt}"}},
  {"id":"read","kind":"api","connection":"tavily","tool":"web_extract","arguments":{"urls":"${steps.search.output.urls}"}},
  {"id":"summary","kind":"agent","profile":"codex-researcher","prompt":"Summarize the files in $FLEET_INPUT_DIR."},
  {"id":"check","kind":"script","command":"scripts/check.sh"}]}
```

The runner is `go/coordinator/pipelines.go`; no version-1 reader exists.

*   Every step writes `output.json` plus any files. An agent or script step without `output.json` gets `{files: [...]}` from the coordinator. Later steps receive all earlier steps' artifacts in `FLEET_INPUT_DIR/<step id>/`.
*   References `${example.<field>}` and `${steps.<id>.output.<json path>}` are resolved by the coordinator before the step starts. A missing reference fails the step with `problem: ref:<path>`; there is no default.
*   `api` step: `{connection, tool, arguments, mode: sync|job, output?}`, default `sync`. `output` (sync only) maps the provider reply to the step output with `${reply.<json path>}` references (a missing one fails the step with `ref:reply.<path>`); without it the output is the reply. The run's step view lists the provider calls made with the step's own token (api steps; an agent step's calls belong to its agent run) as `calls: [{id, tool, connection, cost, basis}]`. A failed mapping keeps the reply on the step as `reply`. In `job` mode the submit output carries `jobId`, kept in the step's `execution.data.jobId`; the coordinator polls tool `job_status {jobId}` on the same connection (answer `{state: running|succeeded|failed, output, usage}`) on every pipeline tick (every 5 s and on every receipt) and resumes polling after a restart. An uncertain outcome is never replayed: a sync call cut by a restart ends `unknown:interrupted`.
*   `agent` step: `{profile, prompt}` plus variant overrides. Its run is an ordinary agent execution, parent = the step, linked by `pipelineStepId`, on the host its account binding picks. Override `host` pins it to that host with no fallback; a host that cannot run it fails the step with the reason.
*   `script` step (Blender steps pin the Mac, [asset pipelines §4](fleet-asset-pipelines.md#4-asset-pipelines-s4)): `{command, timeout_ms?, host?}`, executor command kind `script` (`go/executor/executor.go`): `sh -c` in its own process group (stop kills the group), with `FLEET_INPUT_DIR` and `FLEET_OUTPUT_DIR`; outputs become artifacts. Host: the step's or override's `host`, else the coordinator's default host. Every substituted value is shell-quoted. Not built: the coordinator sending the example fixture's `repo`/`commit`, so a script runs in the trial's own worktree at that commit (the executor supports it; today scripts run in a scratch directory).
*   Variant overrides: `profile`, `model`, `skills`, `prompt`, `web_route`, `account` (agent steps), `host`, `bands` (typed decisions, [statistics](#statistics)), `arguments` (`api` steps: `{<step id>: {<argument>: value}}`, merged over the step's arguments before references resolve and hashed into `config_ref`, [asset pipelines §4](fleet-asset-pipelines.md#4-asset-pipelines-s4)).
*   Inputs reach agent and script steps as start-command `inputs` `[{artifactId, path}]`; the executor downloads them into `FLEET_INPUT_DIR` before the step starts, and the host may read exactly those artifacts. Example `inputs` are staged under `example/`; an example's `references` never enter a run.
*   A failed step stops later steps; the trial keeps its reason.
*   Profile skills (`config/skills/<id>/SKILL.md`, sent in the start command): Claude gets them as a local plugin (`--plugin-dir` with the `Skill` tool; `--disable-slash-commands` stays only when no skill is granted); Codex gets them in the developer instructions.
*   Direct start: `POST /api/pipelines/:id/runs` `{requestId, projectId?, exampleArtifactId? | example?, overrides?}`; the same `requestId` returns the same run; an example artifact must be approved. `GET /api/pipeline-runs/:id` reads a run. The `direct_search` and `pipeline_run` project operations use the same runner.

Pipelines in `config/pipelines/`: `web-research`, `hello-search`, `search-basic`, `draft-examples`, `judge-pass-fail`, `judge-scale`, `typed-decision` (one `api` step on the `typesafe` connection's `system_one`, output `{answer, p, answered_by, tokens}`; analytics stage 1), and for the Cyprus.BZ language pilot (use case `cs:source-audit-language`) `lang-check-jev` (Jev with the project audit's language rules) and `lang-check-claude` (the same rules as the project's Claude audit prompt, an agent step writing `output.json` with `p`); the System One decomposition test adds `lang-decomp-jev` (one call, three questions: alphabet choice, transliteration, language; a script step combines them as `min(P(expected or mixed alphabet), 1 - P(transliteration), P(language))`), `lang-decomp-jev-rev` (the same with the choice options in reverse order), `lang-script-only` (no model: 1 when most letters are of the expected alphabet) and `lang-script-jev` (the alphabet check, then the single Jev question).

## Connections: one accounting path (R3.2)

Every provider call goes through fleet-gateway, from two entry points; the connection record, its hash pin and tokens are specified in [fleet-connections.md](fleet-connections.md).

*   **In-process** (coordinator-side `api` step): one gateway call with the step's run token; the answer is the call line with `output`.
*   **MCP** (agent-side): the executor adds `fleet-executor mcp connection …` to the harness MCP config for each connection the profile grants, and allowlists `mcp__<connection>__<tool>` for the record's `tools`.
*   Both send label = the current step's execution ID and append one line per call to `<calls dir>/.connection-calls.jsonl` (for an agent, `$FLEET_OUTPUT_DIR`): `{tool, request_id, label, outcome: ok|error|unknown, error_code, usage}`. On step completion the coordinator turns each line into a `tool_run` child of the step with `connection_cost {unit: credits, credits, connection}`, basis `measured`, or `unavailable` when the provider reported no usage. API steps read the client's file directly; agent steps' file arrives with the uploaded outputs. Spend lands before the step's state change triggers reconcile.
*   Error classes: `queue-full`, `http-429`/`432`/`433`, `no-usable-key`, `key-disabled-*`, `provider-unreachable`, `unauthenticated`, an unreachable endpoint, a missing record or a hash mismatch give `infra:<code>`, and the trial is excluded from comparison. `provider-body`, `provider-timeout`, a client timeout or an unreadable reply give outcome `unknown`, problem `unknown:<code>`, never retried. Other failures are `step:<code>`.
*   Built-in web (R3.4): Claude runs with a `--tools` allowlist from the profile that never includes `WebSearch` or `WebFetch`; `web_route: builtin` adds both. Codex gets `web_search="disabled"`, or `"live"` with `builtin`. Each trial records its web route.

## Use-case registry and examples

*   Use cases are repo files `config/lab/use-cases/<project>.json`, hand-indexed. Entry: `{id: "<project>:<source id>", source {repo, path, anchor, commit, sha256}, source_status, status?, maps_to?, actor, goal, expected_outcome, checks[], lab_fit: work|behavior|none, tags, maturity}`. IDs never renumber.
    *   `status`: `current | obsolete | duplicate | to-design`, absent = current; `maps_to`: the `fleet:` use cases that cover an obsolete or duplicate entry. Every prototype is replaced by Fleet, so its entries carry `lab_fit: none` and a status. A use case with several sources has one entry per source (`fleet:UC2#csgo-437`). Schema: [asset pipelines §3](fleet-asset-pipelines.md#3-registry-s3). Not built.
    *   `anchor` names one section of the source: an explicit `<a id="…">` or a heading with that GitHub slug (to the next heading of the same or higher level), else one table row whose first cell starts with the anchor. `sha256` hashes that section at `commit`, so an edit elsewhere is not drift.
    *   `source_status`: `current | changed | missing`. A repo that is not checked out reports `unavailable` and leaves the stored status alone.
    *   `lab_fit`: `work` = an agent produces a checkable result; `behavior` = an agent's conduct is judged; `none` = not exercised by Lab.
    *   `maturity`: `indexed | example drafted | synthetic ready | calibrated | linked to a real task`.
*   Drift check `scripts/lab-drift.ts` recomputes hashes from each repo's `HEAD` (`FLEET_LAB_REPOS`, default the directory above the Fleet checkout) and prints changed or missing entries; `--write` stores `source_status`; `--pin <id…>` accepts the current source. CI runs it on Fleet's own entries as a warning; it never blocks.
*   Examples are artifacts: scope `lab`, `role: example`, name `examples/<id>@<version>.json`, hash = content hash. JSON `{id, use_case_id, version, origin: synthetic|task:<id>|export:<project>, fixture: {repo, commit}|null, prompt, inputs[], references[], checks[], split: tune|held_out}`. `inputs[]` and `references[]` are files `{path, text}`, or binary `{path, sha256, media_type}` read from `config/lab/examples/<id>/<path>`; import checks the hash and stores the bytes as a lab artifact with role `example-file`, and staging and checks read them by hash ([asset pipelines §4](fleet-asset-pipelines.md#4-asset-pipelines-s4)); `references` are one known-good result. `checks[]` are `{id, run}`: a shell command run in the trial's output directory with `FLEET_OUTPUT_DIR` set; exit 0 passes. The example's `references` are there too for script checks only (never for a judge or an agent), under `references/<path>`, so a check can compare the output with the known-good result.
    *   An `id@version` never changes content; other content under it is refused (409). The coordinator writes the artifact itself, stored and verified like a backfilled artifact; a refused batch stores nothing.
    *   Approval is `artifact.approved_at`, null while proposed.
    *   "Checks pass with no agent": on inputs plus references every check passes; on inputs alone at least one fails. `scripts/lab-examples.ts check` runs this; the test suite runs it on every seed.
    *   Agents never see examples: host artifact routes skip role `example`. A trial receives its example's inputs only.
*   Owner seeds `config/lab/examples/*.json` are approved by PR review and imported with `scripts/lab-examples.ts import` (sends `$FLEET_ORIGIN`, else `$FLEET_BROWSER_ORIGIN`, as Origin). After import the artifact is the source of truth; an edit needs a new version. Seeds: `fleet:UC1`, `fleet:UC5` (two: web research, and `fleet-typed-decision-smoke` for `typed-decision`, whose check reads `references/expected.json`), `fleet:UC9`, `taskq:UC-07`, `taskq:UC-16`.
*   Routes: `POST /api/lab/examples` `{examples[], projectId?, approved?}` → `{examples: [{artifactId, id, version, hash, created}]}` (400 invalid or unknown use case, 409 conflict or no store); `GET /api/lab/examples?state=proposed|approved`; `GET /api/lab/examples/:artifactId`.
*   Drafting (R3.6): a completed `draft-examples` run's `examples/*.json` files become proposed example artifacts, each with a `rating` question `{form: pass_fail, example_artifact_id}` whose text lists every check command; a pass sets `approved_at`. Completed draft runs are imported again on start.
*   `config/lab` ships in `dist/config/lab`; `FLEET_LAB_DIRECTORY` overrides. Loaders skip dotfiles.

## Experiment reconcile

`labReconcile` (`go/coordinator/lab.go`) runs on coordinator start, after every Lab execution reaches a final state, after each owner action and answer, and on the pipeline tick for running experiments. It runs under the store scope lock `experiment:<id>` (`pg_advisory_xact_lock`). Script checks run before the transaction, never under the lock.

1.  Load the experiment, its executions, scores and spend.
2.  Stop rules, first match wins and is recorded: owner stop; budget with action `stop`.
3.  Budget with action `pause` reached: state `paused`, one `operator_decision` question (`resume|stop`, no task). Running trials finish. `warn` shows a banner only.
4.  For each needed cell, insert the trial with its `request_id` (`ON CONFLICT DO NOTHING`) in the reconcile transaction; only inserted rows launch, started by the pipeline runner after commit. Priority `background` (no effect until the scheduler exists; the screen says so).
5.  Iteration complete = every cell's trial is final and every automatic evaluator has a score or a failed run for each completed trial.

Final trial states are execution states: `succeeded`, `failed`, `cancelled`; a trial whose outcome nobody can prove is `failed` with `observation = unconfirmed` (shown as Unknown). Only a confirmed `succeeded` trial is comparable. Cancel cell ends the run `cancelled` and stops its running step. Retry cell inserts attempt + 1.

Loop experiments use the strategy and stop rules under Iterative improvement. Each strategy is an ordinary retained pipeline run; failed or unconfirmed strategy work stops autonomous continuation with its reason.

## Local-suffix retry

`lab_cell_action` action `retry_suffix` (also `POST …/:id/cells/:cell/retry_suffix`) creates a new attempt of the same cell, reusing the unchanged confirmed succeeded prefix and rerunning a nonempty script-only suffix. Ordinary project grants apply; audit evidence grants no authority. The source must be latest, unscored and not confirmed successful, in a launched non-stopped experiment. Freeze-check definition digest/current content, variant, pinned example and actual input/output artifact bytes. Read bytes outside the write lock; under lock recheck immutable IDs/hashes/size/state/ownership/path before mutation. Missing, corrupt or changed inputs reject with HTTP 409, never provider fallback.

For each started suffix execution require its unique original script command, matching project/host/session, a canonical command-bound `process.reconciled` event (ownedProcesses > 0, resolved true, launchWindowUnresolved false, survived false), and that command's closing unknown receipt. Reject missing/ambiguous/remapped proof and additional launch-capable commands. This uses the trusted host journal's exclusive lifetime ownership and terminal same-command replay guard; output presence never proves exit. Native/API/agent suffixes and unknown prefix outcomes remain blocked.

Serialize source run, GLOBAL, experiment and execution scopes in that order. Recheck latest/unscored and frozen/proof facts under lock. Atomically reserve the new attempt with existing UNIQUE execution.request_id, initialize normal Lab linkage once, and cancel only the proven-stopped old parent. No synthetic child success or speculative stop. Conflict rollback cannot cancel the source. Matching authorized replay occurs before latest/scored/content checks, never initializes/notifies/dispatches again. Ordinary full retry also rechecks latest under its write lock.

For `retry_suffix`, `experimentId`, `cell`, `sourceRunId` and `requestId` are required nonempty strings. `cell` is the existing canonical cell key; `sourceRunId` is the source trial execution ID. Whitespace-only strings fail with HTTP 400. Preserve exact accepted requestId bytes in the key/signature; do not trim an accepted value into a different identity. Other actions retain existing parsing and unknown-field policy. No unknown output/reuse/proof field acquires meaning.

The action reply remains the existing open experiment view. Only a successful suffix action adds the required result `recovery: {"sourceRunId":"old-run", "runId":"new-run"}`. Both fields are nonempty string execution IDs. New creation and an identical replay return the same pair. The surrounding experiment view is current at response time and may change after scoring/latest changes; the recovery pair does not. Ordinary cancel/retry replies omit `recovery`.

Each recovered trial in the existing `trials` projection adds `recovery: {"sourceRunId":"old-run", "reusedStepIds":["decide"]}`. `sourceRunId` is the immediate source run, and `reusedStepIds` is a nonempty array of string step IDs in definition order. Non-recovered trials omit this property. It does not alter existing `id`, `cell`, `spend`, `cost` or score fields. The row links this source run beside incremental spend; historical spend remains on that source/history.

The new attempt's execution data stores this exact object:

```json
{
  "recovery": {
    "identity": {
      "experimentId": "experiment",
      "requestId": "request",
      "cell": "existing-canonical-cell-key",
      "sourceRunId": "old-run"
    },
    "reusedStepIds": ["decide"],
    "audit": {
      "proofs": [{"executionId":"old-script", "commandId":"script-command", "eventId":"canonical-reconciliation-event"}],
      "artifacts": [{"artifactId":"original-artifact", "sha256":"lowercase-64-hex"}]
    }
  }
}
```

All shown properties are required for a suffix-recovery record. IDs are nonempty strings. `proofs` contains one exact command/event binding per started suffix execution in definition order; `artifacts` contains every validated prefix-output/example-input artifact once, sorted by artifactId, with the computed actual-byte SHA-256. The arrays have no caller-supplied members. These are audit references, not authority or accepted output bytes. Existing artifact records and source runs retain full ownership/path/size/state/provenance.

Only the four `identity` strings participate in replay equality, after ordinary grants and authorized row lookup; compare them exactly and compare the stored typed request key. Audit arrays and current latest/scored/artifact availability are not replay-identity inputs. Malformed stored identity fails closed with a generic HTTP 409 response. The UNIQUE request key still uses only canonical `[experimentId, requestId]`, so another cell/source under the same key conflicts. A matching replay never reinitializes, cancels, notifies or dispatches.

The immediate source is explicit in trial recovery, run `rerunOf`, and the existing step `reusedFrom` convention. For transitive reuse, original executionId/outputArtifactId and artifact bytes/hash/owner remain unchanged; do not replace them with the intermediate run's IDs or create copied provider outputs. Original content provenance follows those immutable references, while immediate lineage follows sourceRunId/reusedFrom. Public recovery objects do not claim that the immediate source performed the provider work. The ordinary transitive rerun test and suffix HTTP assertions verify this distinction.

Schema comments, Lab trial projection and HTTP expectations use these exact shapes; keep `LabCellActionReply` an open map so other experiment-view fields survive generation.


The request key is `lab-suffix:` plus SHA-256 of canonical `[experimentId,requestId]` in the new execution.request_id. Original calls/usage/cost remain in source history and experiment accounting once; the new trial shows incremental work and an immediate-source link. Reused original artifact/execution references remain immutable, including transitive reuse. Normal new script receipts, output storage and scoring establish the result.

## Iterative improvement

Target for kind `loop`. A strategy is a pipeline step (script or agent). Input: an experiment snapshot (variants, scores, spend, budgets). Output `strategy.json`: `{action: next|stop, variants:[{label, overrides}], reason}`. Config `{generator:{kind, params}, allocator:{kind, params}}`. At iteration end reconcile runs the strategy and appends its variants.

*   Generators: `list` (fixed variants), `grid` (all combinations of listed overrides), `propose` (an agent reads scores and losing transcripts and writes overrides with a reason; `keep_best: true` starts each round from the current best).
*   Allocator: `full` (every cell runs). Adaptive allocators apply to tune examples only; finalists always run on every held-out example.
*   `no_gain {k}`: gain = best variant's mean of the primary evaluator on tune examples minus the baseline mean; stop when gain ≤ (max − min of baseline repeats) for k consecutive iterations. The owner sets `k`; no default.
*   A strategy never changes a budget.

Loop configuration is `strategy: {generator:{kind,params},allocator:{kind:"full",params:{}},max_iterations?:N,no_gain?:{k}}`. Both stop settings are optional; there is no coded iteration ceiling or default k. `primaryEvaluator` is required and must have a scalar form. `list` takes `params.variants`; `grid` takes `params.overrides` (override keys mapped to value arrays); `propose` takes an agent strategy and may set `keep_best`. Generator `params.step_overrides` selects the strategy run’s pipeline overrides. Ordinary owner-only override authority is preserved from experiment creation.

List/grid use `lab-strategy`; propose uses `lab-strategy-propose`. The runner stages a retained `snapshot.json` with variants, tune scores, spend, budgets and generator/allocator configuration. Held-out results stay in history but do not steer strategies. Propose runs also receive the retained execution-event transcripts of losing tune trials; unavailable history is marked unknown. The last step writes matching `strategy.json` and `output.json`. The coordinator accepts only `{action,variants,reason}`, so a strategy cannot replace budgets. Successful outputs append uniquely identified variants with their proposal labels and iteration. Strategy identity includes experiment and iteration, and consumption is transactional. Missing primary-score evidence stops with `primary-score-unavailable`, never a false `no_gain`.

## Budgets

`{unit, limit, action: warn|pause|stop}` at experiment scope; default action `pause` (R3.7). Units: `trials` (trial rows), `wall_ms` (pipeline runs, trials and judges), `pipe_calls` and `pipe_credits` (`tool_run` rows of the experiment; basis `partial` when a call had no usage). Each budget keeps `firedAt`, so its action fires once and a resumed experiment does not pause again on it. A `trials` budget with `pause` or `stop` also caps launches in a pass. A budget fires only while cells remain to launch. Judges launch only while `running`. Owner Resume or Stop withdraws the open budget question. Budgets warn or pause; Lab never refuses a launch on its own.

Not built: token and money units, per-iteration or per-trial scope, projected-overrun warning.

## Ratings

`question.kind = 'rating'`, options `{form, experiment_id, trial_ids, seed}`, created by the owner from the matrix (`POST /api/lab/ratings {experimentId, form, trialIds}`); never blocking.

*   Forms: `pass_fail` (answer `pass|fail[: tags]`, one trial), `scale` (`1..5[: note]`, one trial), `pick_best` (the chosen trial ID, two or more trials shown blind as A, B, … in an order shuffled by `seed`).
*   The answer hook (`runAnswerChecks`, `go/coordinator/pipelines.go`) checks and applies Lab answers in the answering transaction; reconcile follows.
*   Evaluators, named `<kind>:<ref>:<form>`: `script:example.checks:pass_fail|metric` (the coordinator runs the example's checks over the trial's files: example inputs, then each step's files at the top level, later steps winning); `judge:<pipeline id>:pass_fail|scale|pick_best` (`pick_best` runs twice per example/repeat group, in deterministic shuffled and reverse orders, and scores only agreeing valid trial IDs; other forms use one pipeline run per evaluator and trial; its example is the original prompt plus the trial's files under `trial/`; its last `output.json` is `{value, rationale}`; profile `codex-judge`); `human:owner:<form>`. A failed judge run or invalid value counts as evaluated with no score. Forms are never averaged together.
*   Agreement = owner `pass_fail` vs judge `pass_fail` on the same trial, plus owner `pick_best` vs judge `pick_best` over the same trial set (falling back to the single best `scale` score). Report agreement separately per evaluator/rubric version. Judge evaluator `overrides` use pipeline override keys; a prompt containing the rubric and output instructions pins its content into the evaluator version.
*   Correction: `POST /api/lab/scores/:id/correct {value, rationale}` writes a new score that replaces the old.

Order-swapped `pick_best` judgments retain both runs; disagreeing answers produce no score and the view reports `order-dependent` ([asset pipelines §5](fleet-asset-pipelines.md#5-judges-s5)).

## `/lab` screen

`app/src/views/lab.tsx`, from `packages/ui` only; page rules in [laboratory.md](../design-system/pages/laboratory.md). `/lab`: experiments list, use-case registry with drift and maturity, examples awaiting approval. `/lab/<experiment id>`: matrix (rows examples, columns variants; cell state, score with form, spend with basis, running time, confounders, link to execution; cancel and retry cell), spend against budgets, agreement, rating queue, Promote. Workspace `lab`. Experiment creation is an API call; the screen covers everything after it.

Analytics view on the same screen, dark theme first: the experiment list in plain words (goal, hypothesis, decision, when) and, per Lab group, the variant comparison from [statistics](#statistics): primary evaluator difference with interval and `n`, coverage and escalation share where typed, cost with basis and price source, p50 time, per tag. A row is identified by variant label plus `config_ref`; the kind of variant (model override, other pipeline, `api` step) is shown as text. No combined score, no ranking.

Routes (each also an operation `lab_*`, `go/coordinator/lab.go`): `POST|GET /api/lab/experiments`, `GET /api/lab/experiments/:id` (trials, scores, spend), `POST …/:id/{launch,stop,resume}`, `POST …/:id/cells/:cell/{cancel,retry,retry_suffix}`, `POST …/:id/promote {variant}`, `POST|GET /api/lab/ratings`, `GET /api/lab/ratings/:id`, `POST /api/lab/scores/:id/correct`, `GET /api/lab/registry`. Errors: 400 invalid body, 404 unknown ID, 409 wrong state.

Promote saves the variant as a catalogue method (pipeline, overrides, `config_ref`, source experiment) in a new catalogue revision with source `lab` ([definition editors](fleet-definition-editors.md)), unless a method already carries the variant's `configRef`, and sets the project's recommendation to it, in one transaction. It returns `{method, catalogue, recommendation}` (`cataloguePatch` is null). Nothing promotes automatically.

## Claude on WSL (R3.8)

Host setting `FLEET_CLAUDE_PLACEMENT` (`mac | windows-wsl`, [configuration.md](../configuration.md)) decides where Claude may run; readiness verifies it (WSL detected from the kernel release) and checks the login file under Fleet's config directory. The delivery worker (not built) applies the same rule.

## Removal in one cut (R3.1)

The old lab (its pages, its `/api/lab/*` and `/api/experiments` routes and stored documents) is gone. Profile code lives in `go/contract/profile.go` (the one validator, used by coordinator and executor), `go/coordinator/projects.go` and `go/executor/profile.go`; profiles are listed by `GET /api/profiles`. Paired delegation is an `ab` experiment; delivery comparison is removed.

AssetQ was deleted in one cut on 2026-11-11 (service, database, route, Access app, repository archived); only the `fleet:UC2` crate seed `fleet-uc2-crate` remains ([asset pipelines §2](fleet-asset-pipelines.md#2-deleting-assetq-s2-after-s1)).

## Work analysis

Human interfaces and agents query the same retained work history within their permissions: project, task, execution, step, method, agent, environment, connection and time-range views, with drill-down from aggregates to source records.

*   Coverage: the full work cycle (queueing, preparation, agent and tool execution, questions and waits, retries, review, merge, CI, publication, cleanup), including failed and cancelled work and infrastructure operations without an agent.
*   Measures: elapsed and active time, waiting reasons, invocation and retry counts, tokens, money, CPU, memory, disk; the method, instructions, skills and configuration used.
*   Elapsed task duration differs from summed execution time across parallel work. Nested usage is not counted again in parent totals. Estimates and unavailable data are never exact values or zero.
*   An operator or architect may discuss history with an agent, find repeated or expensive work, and test a change as an experiment; findings are not limited to predefined categories. Production and Lab measurements stay linked to their executions in one history store.

Not built: work-analysis views and queries beyond the experiment page and execution records; the coverage and measures above are the target for them. The query contract is [`analytics_query`](fleet-analytics.md#analytics_query).

Production signals (acceptance, rework) find examples worth mining and calibrate judges; they are never shown as a quality score. Grading runs in tiers: script checks, then a typed-decision pre-filter, then an LLM judge for the middle band only, then a person for disagreements only.

## Analytics staging

| Stage | Content | Done when |
|---|---|---|
| 0 | Register the project; freeze export schema v1; import examples with labels. | Schema file exists; examples approved. Built: the Cyprus.BZ project is `config/projects/cs`; the schema and the converter exist; no project rows imported yet |
| 1 | Smoke test: one `single` experiment with one `api` step on a typed-decision connection; trial output `{answer, p, answered_by, tokens}`; a check that reads `references`. | A `tool_run` row with cost and basis |
| 2 | Experiment record, frozen at launch, decision at close. | Launch fails without goal and hypothesis; amendments visible |
| 3 | Price entries and estimated cost per trial. | Matrix shows cost with basis |
| 4 | (Use case: `cs:translate_taxonomy-language` in `config/lab/use-cases/cs.json`, the language detection of Cyprus.BZ's `translate_taxonomy` task.) First project step: baseline variant vs typed decision with bands, tuned on `tune`, reported on `held_out` per tag, with the statistics function. | A decided experiment |
| 5 | The `model-selection` skill with its playbook section. | A PM agent answers "which model for step X" citing the experiment |
| 6 | The analytics view on `/lab`, reading trials through the Lab routes; it switches to `analytics_query` in stage 7 with no second statistics path. | Operator sees record history and comparison |
| 7 | Grouping columns, `analytics_query`, the reuse key, the cross-experiment comparison. | A PM agent answers production questions from the query |
| 8 | Cascade experiments, after [script-step gateway access](fleet-connections.md#4-per-run-tokens). | Decisions per step |

Not in this round: ClickHouse spans for Lab, regression gates, `loop`, a combined score or Elo, a money budget unit, judge sampling of production (a setting, off by default), automatic application of merge decisions.

