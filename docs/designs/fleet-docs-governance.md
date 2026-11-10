# Fleet docs governance

Agents write every Fleet document and read them as the base of every task. This design defines document kinds, where each kind lives and for how long, what an agent may add, the report that shows disorder, how its findings become work, how the rule reaches agents, how success is measured, and the order of the full docs rework. The rework itself is a separate Task.

## Problem

Measured on `main` at `3026f49`:

- `docs/` holds 131 Markdown files, 1.28 MB, and 70 other files (screenshots, HTML studies). `docs/designs/` holds 21 Markdown files, about 700 KB; seven briefs alone hold 196 KB; `fleet-data-foundation.md` is 146 KB.
- Each EGM feature run leaves a brief, a design and review reports in `docs/designs/`. Of 81 pull requests merged to `main` since 2026-10-01, 54 added no file under `docs/`; six added 11 to 57 files each.
- 70 files outside `docs/` cite `docs/designs/` or `docs/handoffs/`, often by section (`§4`, `D4`, `Stage 1`): code comments, `schema.sql`, `ci.yml`, `contracts/lab-config.json`. 61 citation lines already point at 9 docs paths that no longer exist.
- Both indexes say a document is "not shipped behaviour; check the code". Pairs such as `connections.md` and `designs/fleet-connections.md`, or `lab.md` and `designs/fleet-lab.md`, give a fresh agent two candidate rules for one topic.
- specq budgets only the root contract ([size budgets](https://github.com/taskqorg/specq/blob/13772b779be68103d0581d61a5f8fe7af2b92dd1/engineering/contracts.md#size-budgets)). No shared rule covers designs, briefs, reviews, handoffs or evidence.
- A rule that no check or delivery step carries is ignored: 325 of 325 test cases ignore the warning-only requirement-ID rule.

## Goals and non-goals

Goals: one current file per topic, found from one index; working papers never accumulate in the tree; growth and decay visible on every pull request and owned by one open issue; the rule delivered to every agent by injection and configuration.

Non-goals: no merge-blocking docs check; no new size cap or threshold; no archive folder (git history and pull requests are the archive).

Terms: *EGM* is the brainstorm, draft and review plugin agents use for designs. *Attempt index* is the list of project files injected at each agent attempt ([rule delivery](https://github.com/taskqorg/specq/blob/13772b779be68103d0581d61a5f8fe7af2b92dd1/engineering/rule-delivery.md#attempt-context)). *Design-report comment* is the sticky pull request comment that the `ci.yml` comment job posts.

## Document kinds

Every file in the repository that is `fleet.md`, `AGENTS.md`, or under `docs/` or `evidence/` has exactly one kind. Rules are applied in this order; the first match decides:

1. `fleet.md`: **contract**.
2. `AGENTS.md`, `docs/README.md` and every `README.md` under `docs/`: **index**.
3. Outside `evidence/`: a name ending `-brief.md`, an ISO date in the path, or a path under `reviews/`, `handoffs/`, `archive/`, `explorations/` or `prototypes/`: **working paper**.
4. Under `evidence/`: **evidence record**.
5. A non-Markdown file under `docs/design-system/` that a specification or `book/` links, plus the generated `book/index.html` and `pages/mockups.html`: **specification asset**, part of the specification that links it.
6. Any other non-Markdown file under `docs/`: **working paper**.
7. Any other Markdown file under `docs/`: **specification**.

| Kind | Holds | Life |
|---|---|---|
| Contract | Decisions and mandatory rules | One file, rewritten in place, budgeted by specq contracts. |
| Index | One row per specification: topic, file, one-line summary | Rewritten in place with each added or removed specification. |
| Specification | Current target of one topic: an area, a design-system control, pattern or page, or a build or operation procedure | One file per topic. Rewritten in place; no history, no dated sections. A part the code has not reached says so in one line, `Not built: <what>`, removed when the code lands. |
| Evidence record | A measurement or source finding that a specification or code cites for its exact numbers | Unchanged after merge except link fixes. Deleted when nothing cites it. |
| Working paper | Brief, draft, review report, drift report, handoff, screenshot, rejected prototype, research notes | Never committed. Lives in the pull request description or comments, CI artifacts, a task, or local `.egm/` (git-ignored). |

- An accepted design is the specification of its topic. Where a topic has both an area page and a design, the rework merges them into one file.
- A decision's reason goes next to its rule in one sentence. The discussion stays in the pull request ([rule homes](https://github.com/taskqorg/specq/blob/13772b779be68103d0581d61a5f8fe7af2b92dd1/engineering/principles.md#rule-homes)).
- A conclusion goes into the specification. An evidence record stays only when a citation needs its exact numbers.

## What an agent may add

- One specification per topic means one index row per file. The attempt index (one line per file, as specq requires) is therefore also the topic table; no change to specq rule delivery is needed.
- Before writing, the agent finds the topic's row and edits that file.
- If no row fits, the agent adds one specification and its row in the same pull request, and the pull request description says why no row fits.
- An evidence record is added only together with the citation that needs it.
- A working paper is never committed. Its substance goes into the pull request description; a finding that must survive goes into the specification it changes.

## EGM outputs

- `AGENTS.md` gets `EGM artifact directory: .egm` and `.gitignore` gets `.egm/`. Brainstorm briefs, drafts and review reports then land outside any commit; the EGM skills read this line themselves.
- The accepted design is written once, in place, as the topic's specification.
- The pull request description carries the brief's rejected alternatives, the review verdict, each blocking finding with its resolution, and the drift table when one is due.
- EGM brainstorm "publish as RFC to `docs/designs/`" is not used. A draft for feedback is a draft pull request.

## Report

`scripts/docs-report.ts`: Node standard library only, no LLM, about one second. It writes `design-report/docs-report.txt` in the `check-design.mjs` line format (`path:line id message`) and ends with "This report never fails the build." CI runs it as a step with `if: always()` and `continue-on-error: true`, like the three existing report steps. The build job's first checkout gets `fetch-depth: 0` so the merge base and `HEAD^` exist. A file is linked when a Markdown link, `src` or `href` in another file resolves to it from that file's directory.

| ID | Finding |
|---|---|
| `docs.unlisted` | Markdown specification not linked from a row of `docs/README.md` or of a sub-index that a root row links. |
| `docs.link` | Relative link or `#anchor` in a `fleet.md`, `AGENTS.md` or `docs/**/*.md` file that does not resolve. Anchors use GitHub heading slugs (`slug()` from `scripts/lab-drift.ts`, which is import-safe); explicit `<a id>` anchors also count. |
| `docs.cite` | In tracked files outside `docs/` and `evidence/` (excluding `node_modules/`, lock files and binaries), a match of `docs/[A-Za-z0-9_./-]+\.(md|html)` whose path does not exist. If the same line also has `#slug`, a missing heading is reported too. Section labels such as `§4`, `D4` or `Stage 1` are not verified. |
| `docs.paper` | Working paper in the tree (kind rules 3 and 6). |
| `docs.evidence` | Evidence record whose path no tracked file cites. |
| `docs.touched` | On a pull request: each docs path cited by a changed non-docs file. Listed for the reviewer, no judgement. |
| `docs.drift` | On a pull request: a specification changed by more than a link fix while the pull request description has no `## Drift` heading. A change is a link fix when every changed line differs from its old version only inside `](...)` targets or `<a id>` anchors. The comment job reads the current description with `github.rest.pulls.get` and evaluates this one ID there, so a `## Drift` added after the last push counts on the next run; the `pull_request` trigger gains `types: [opened, synchronize, reopened, edited]`. |
| `docs.size` | Bytes of each specification, largest first, and its change against the merge base. A measurement, no threshold. |
| `docs.delta` | Files added and removed under `docs/` and `evidence/`, and byte change, against the merge base on a pull request or `HEAD^` on a push. |

The comment job adds a second fenced block, `Docs report`, under the design report, cut to 40 lines with a link to the artifact; the design report keeps its own 60 lines. These are display limits, not budgets.

Size gets no threshold. The specq budget covers the root contract only, and extending it to every specification would be a new limit; whether to do so is an [owner decision](#open-owner-decisions). `fleet.md` stays covered by the existing contract lint.

## From warning to work

The report copies the specq compaction mechanism: one open item carries the work, and a second never opens while the first is open.

- A new job `docs-order` runs with `needs: build-test-design` and `if: always() && github.event_name == 'push'`, with `permissions: issues: write`. It downloads the `design-report` artifact and uses `actions/github-script@v7` exactly as the design-report comment job does: it finds the open issue whose body contains `<!-- fleet-docs-order -->`, updates its body with the findings, or creates it with label `docs`, creating the label if missing. When the report has no finding except `docs.size` and `docs.delta`, the job closes the issue.
- `AGENTS.md` gets a second line: an agent that edits `docs/` reads the open `docs-order` issue first and fixes the findings in the files it touches. The docs report block in each pull request comment links the issue. Fleet has no GitHub-issue intake today; when the coordinator gains one, the issue becomes a Fleet task with no change here.
- Step 0 of the rework is done only when this job has opened the issue once on `main`.

Nothing blocks a merge.

## Drift between specification and code

- **A pull request changes code that cites a specification:** `docs.touched` lists the cited files; the reviewer confirms each still matches or edits it in the same pull request.
- **A pull request rewrites a specification:** a fresh reviewer subagent compares each rule with the code it names and answers `matches`, `differs` or `not built`, under `## Drift` in the description. `docs.drift` warns when the heading is missing. Each `differs` is fixed in the same pull request or filed as a task named in the table; neither blocks merge.
- Reviewer prompt: "For each rule in `<spec>`, find the code that implements it in this checkout. Answer one line per rule: rule heading, `matches`, `differs: <what>` or `not built`, and the code path."
- No periodic drift job; no committed drift report.

## Delivery

- The specq shared core is injected each attempt; the proposed shared rule is in `principles.md`, a core file, so every agent receives it.
- The attempt index lists every specification with its summary.
- `AGENTS.md` carries the EGM artifact line and the `docs-order` line.
- `fleet.md` R1 links this specification for Fleet's paths once step 0 lands.
- The report reaches agents through the design-report comment and the `docs-order` issue.

## Shared rule (specq)

Home: `engineering/principles.md`, new section `## Project documents` after `## Project docs cleanup`. Proposed text:

> ## Project documents
>
> Each project file has one kind, decided by its path. The project contract names or links the path rules.
>
> - Contract: the one root file of decisions ([contracts](https://github.com/taskqorg/specq/blob/13772b779be68103d0581d61a5f8fe7af2b92dd1/engineering/contracts.md)).
> - Index: one row per specification: topic, file, one-line summary. The attempt index lists the same files.
> - Specification: the current target of one topic. One file per topic, rewritten in place; no history and no dated sections. A part not yet built says so in one line.
> - Evidence record: a measurement that a specification or code cites for its exact numbers. Unchanged after merge except link fixes; deleted when nothing cites it.
> - Working paper: brief, draft, review, drift report, handoff, screenshot, rejected option. Never committed; it lives in the pull request, the task or ignored local scratch. A finding that must survive goes into the specification it changes.
>
> Before adding a file, find the topic's index row and edit that file. Add a specification only with its new row, and say in the pull request why no row fits. File names carry no dates.
>
> A project check reports, without failing the build: specifications the index does not list, links and cited paths that do not resolve, working papers and uncited evidence in the tree, and the size and file change of each pull request. On the main branch the findings keep one open work item, updated on each run and closed when none remain.

It names no project path, plugin or number. It goes through specq's change process ([improve shared rules](https://github.com/taskqorg/specq/blob/13772b779be68103d0581d61a5f8fe7af2b92dd1/engineering/spec-first.md#improve-shared-rules)); Fleet adopts it by moving its pinned commit.

Fleet-only: the kind rules and paths above, the EGM line, the report and its IDs, the CI steps and the `docs-order` issue.

## Measurement

Run before the rework (step 0), after it (step 6), and with each later report review.

1. **Findability.** A fresh agent gets the injected index and the tree, and names one file per question. Score: answers naming the one current specification. Questions and the expected owner topic: (1) which host runs a task: allocation; (2) how a view URL is built: view URLs; (3) how the executor is released: executor delivery; (4) how a task's transcript is shown: conversations; (5) where per-host secrets live: people and access; (6) how a Lab experiment is promoted: Lab; (7) how an agent reaches an external API key: connections; (8) which tables hold tasks and executions: data foundation; (9) how a project's settings are layered: configuration; (10) what a design-system button may look like: `controls/button.md`. Expected file per question, against the [index](../README.md):

   | Question | Expected file |
   |---|---|
   | 1 | `docs/work-allocation.md` |
   | 2 | `docs/designs/fleet-view-urls-and-list-path.md` |
   | 3 | `docs/designs/fleet-executor-delivery.md` |
   | 4 | `docs/designs/fleet-conversations.md` |
   | 5 | `docs/designs/fleet-people-access.md` |
   | 6 | `docs/designs/fleet-lab.md` |
   | 7 | `docs/designs/fleet-connections.md` |
   | 8 | `docs/designs/fleet-data-foundation.md` (its `fleet-data-store.md` part after the split) |
   | 9 | `docs/configuration.md` |
   | 10 | `docs/design-system/controls/button.md` |

   Agent prompt: "You get the Fleet docs index and the file tree. For the question, name the one file that holds the current rule. Answer with one repository path." The agent gets `docs/README.md` and `git ls-files docs`, no other file.
2. **Size.** Markdown files and bytes under `docs/`; other files under `docs/`. Baseline 131, 1.28 MB, 70.
3. **Growth.** `docs.delta` per merged pull request. Baseline 54 of 81 add zero, six add 11 to 57. Expected: EGM pull requests add no file except a specification for a new topic.
4. **Decay.** Count per report ID on `main`. Baseline from the first run; expected zero after the rework.

## Rework plan

One pull request per step or area, so one revert restores it. A file is deleted only after the file that replaces it is merged.

0. **Stop the inflow.** EGM and `docs-order` lines in `AGENTS.md`, `.gitignore`, report, CI step, `fetch-depth: 0`, comment block, `docs-order` job. Done when the issue has opened once.
1. **Index and citations.** Rewrite `docs/README.md` as the topic table over current files, marking each topic's chosen target file; record the findability expected files; run the baseline. Fix the 61 dead citations. Mark each citation as a comment or a loaded path; loaded paths (`contracts/lab-config.json`, `config/lab/use-cases/*.json`) move only with their reader.
2. **Areas,** in order of code citations. For each: merge into the target file, keep cited headings, rewrite to the current target with `Not built:` lines, fold surviving findings of its papers in, delete the papers and the merged files, update code citations and the row, add the `## Drift` table.

| Area | Merge into (target) | Merge and delete | Papers to delete after folding |
|---|---|---|---|
| Lab | `designs/fleet-lab.md` | `lab.md`, `work-analysis.md` | `designs/lab-brief.md` |
| Operator app and views | `designs/fleet-operator-app.md`, `designs/fleet-view-urls-and-list-path.md` | `interfaces.md`, `designs/legacy-inventory.md` | `designs/reviews/fleet-operator-app/`, `designs/web-stack-brief.md`, `designs/entity-ids-brief.md` (codes fold into data foundation) |
| Conversations | `designs/fleet-conversations.md` | none | `designs/conversations-brief.md`, `conversations-no-reply-rca.md`, `designs/reviews/drift-*.md` |
| People and access | `designs/fleet-people-access.md` | `resources-and-auth-model.md`, `multi-account-same-env.md`, `layered-isolation-model.md` | `designs/people-access-brief.md`, `designs/reviews/people-access/` |
| Data foundation | `designs/fleet-data-foundation.md`, split by subtopic (see below) | `state-ownership.md` | `designs/fleet-data-foundation-brief.md`, `designs/fleet-data-foundation-review/` |
| Connections | `designs/fleet-connections.md` | `connections.md` | none |
| Stack, toolchain, delivery | `designs/fleet-stack.md`, `designs/fleet-toolchain.md`, `designs/fleet-executor-delivery.md` | `delivery-strategy.md` | `designs/fleet-stack-brief.md`, `designs/reviews/fleet-stack/`, `handoffs/` |
| PM agent and work | `designs/fleet-pm-agent.md`, `orchestration.md`, `work-allocation.md` | `system-operations.md`, `execution-contract.md` | `spec-coverage-review.md` |
| Configuration | `configuration.md` | `project-context.md`, `capabilities-and-pipelines.md`, `project-settings-*.md`, `project-setup-and-access.md` | `real-projects-survey.md` |
| Runtime and build | `deployment.md`, `runtime-integration.md`, `execution-continuity.md`, `coordinator-build.md`, `executor-build.md`, `infrastructure-build.md`, `pds-codex-environment.md` | `runtime-boundary-review.md` | `harness-session-comparison.md`, `agentq-reuse-boundary.md`, `agent-tools-inventory.md`, `component-reuse-review.md` (conclusions fold in) |
| Prototype plan | `prototype-implementation-plan.md` until code stops citing T1-T16, then deleted | `architecture-breadth-plan.md`, `project-pipeline-prototype.md` | `test-results-T1-T16.md` and `multiharness-*.md` move to `evidence/` if still cited |
| Use cases | `use-cases.md`, `website-example.md` | none | none |
| Design system | its controls, patterns and pages files | `owner-rules-table.md` into `owner-rules.md`, `methodology-brief.md` into `README.md` | `reviews/2026-11-10/`, `explorations/`, `pages/shots/`; fix the 7 broken links |

Where a row lists several targets, each stays its own topic with its own index row; only the middle column merges. The data foundation splits into `designs/fleet-data-store.md`, `designs/fleet-data-ingest.md` and `designs/fleet-data-isolation.md`. The table is the starting map; step 1 confirms each target against code citations and may move a file between rows. A merge that loses a cited heading keeps it as a heading in the target.

3. **Evidence.** Move still-cited records into `evidence/`; delete uncited ones.
4. **Index.** Remove the "check the code" disclaimers; `Not built:` lines replace them. Every row points at a merged target.
5. **Contract link.** `fleet.md` R1 links this specification.
6. **Measure** and record the four numbers in the last rework pull request.

The existing EGM briefs and review reports are deleted in step 2 after their surviving findings are folded in; git history keeps them.

## Alternatives considered

- **Keep working papers while cited.** Rejected: anything stays by adding a citation; the index already cites seven briefs. Evidence keeps this rule because code needs exact numbers.
- **Archive folder or `docs/work/`.** Rejected: an archive in the tree is the growth this design stops.
- **Per-file size cap from the contract budget.** Not adopted without the owner: it would be a new limit, and growth today is file count. Kept as an owner decision.
- **Blocking CI.** Rejected: the system helps, never blocks.
- **Kind declared in each file.** Rejected: a second source of truth; the path decides.
- **Periodic LLM drift job.** Rejected for cost and noise.
- **One issue per finding ID.** Rejected: one issue carries all findings with less machinery.

## Open owner decisions

1. Review reports are not kept in the tree; the pull request description holds the verdict and blocking findings. Default: as written.
2. Apply the contract budget (60 KiB cap, 70% target) to each specification as a `COMPACT:` signal. Default: no; `docs.size` measures only.
3. The specq text goes to specq as a proposal, homed in `principles.md` (injected core). Default: as written.
