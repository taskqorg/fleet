# Laboratory (architect)

## Target

**Status.** Target, rail section Lab ([information architecture](../../designs/fleet-ia-and-journeys.md#1-closed-information-architecture)). It keeps the built Lab's content and changes its frame; the sections after **Current build** stay true for the content.

**Purpose.** Which model, harness or pipeline is best for each use case right now, and how do I test a new one ([J1–J3](../../designs/fleet-ia-and-journeys.md#3-core-user-journeys-j1j5)).

**Routes:** `/lab` (idle: leaderboard), `/lab/use-cases/<id>`, `/lab/suites/<key>`, `/lab/E-FLT-7` (one experiment, [D17](../decisions.md#2026-11-10-d17-human-entity-codes)), `/lab/new?useCase=<id>`. Old `/lab/<uuid>`, `/lab/use-case:<id>`, `/lab/suite:<key>` and `/personal-lab` redirect (`/personal-lab` to `/lab` with the saved view "Mine").

### Layout

Three columns ([OR-24](../owner-rules.md)). Shell header: title, New experiment (`plus`), focus chip, search.

* **Control column** (`CatalogueTree`, [OR-48](../owner-rules.md)): two roots, **Use cases** (LLM Code Benchmarks, Real Work, Fleet & Projects, folded Archived) and **Runs** (Active, Drafts, Completed, folded Stopped); one tree, no mode switch ([OR-46](../owner-rules.md)). Above it the list header with search and saved views (Mine, Waiting for a decision). A leaf: state icon, name, code, best score on the right.
* **Work area, idle** (J1, under 5 s): what waits for the person first (ratings and undecided series as question cards, [OR-53](../owner-rules.md)); then the comparison of the selected collection: **Model × Pipeline** with score, median wall time and failure rate in each cell ([owner decision](#owner-decision-target)); cost and tokens fold into the cell's Details ([OR-54](../owner-rules.md), [OR-59](../owner-rules.md)); a winning cell has Promote (J3).
* **Work area, use case open:** goal and expected outcome, approved examples, the comparison scoped to this use case, and **Run in Lab** (J2).
* **Work area, experiment open:** head (state, title, code, Launch, Stop, Promote), the matrix (examples × variants, cells with state and score, live step `2/3 · review`), variants as differences from the baseline ([OR-45](../owner-rules.md)), the decision gate, ratings.
* **Work area, new:** the create form with EntityPicker choices; Launch in at most 3 clicks from a use case (J2).

### States

* **A run working:** cells update in place with fixed-width figures ([OR-6](../owner-rules.md)).
* **A trial failed:** the cell is in the danger tone with its `problem` cause inline; one click opens the trial (UR-63).
* **Phone (390 px):** the tree; an opened use case shows the ranked list of variants instead of the matrix.

### Owner decision (target)

The idle Lab screen shows the Model × Pipeline matrix (owner, 2026-11-25). On a phone a use case shows its variants as a ranked table.

### Review of the current build

Checked against the [product map](../product-map.md) rows for Lab.
* **Keep:** the use-case card with Run in Lab, the leaderboard with Promote, the Runs questions view, ratings and example approvals, New experiment with its EntityPicker form and its uncertain-reply handling, the experiment page (spend, matrix, loops, variants, decision gate, ratings), trial plan inspection.
* **Change:** Personal Lab becomes the saved view "Mine"; undecided experiments and ratings become question cards; the leaderboard table becomes the Model × Pipeline matrix on the idle screen.
* **Drop:** the `Use cases | Runs` mode switch (two roots of one tree, [OR-46](../owner-rules.md)); the summary strip of counts (active use cases, approved examples, leaderboard entries, undecided series), which the matrix and the questions already answer ([OR-17](../owner-rules.md)); the "undecided experiments" warning banner (it is a question now).

### Blocks

* [Navigation rail](../controls/nav-rail.md), [shell header](../patterns/app-shell.md).
* [Filter bar](../controls/filter-bar.md#board), [catalogue row and list group](../controls/data-list.md#board), [detail head](../patterns/list-detail.md#board); [text field](../controls/text-field.md#board) for the create form.
* [Question card](../controls/decision-block.md#card) for ratings and undecided series; [state dot and word](../controls/state-badge.md#dot-and-word); [button](../controls/button.md) for Promote and Run in Lab.
* [Matrix cell](../controls/matrix-cell.md) for the matrix and the experiment; [table row](../controls/data-list.md#table-row) for the ranked list on a phone; [diff row](../patterns/definition-editor.md#diff-row) for variants.

### Design board

Canvas [screens/lab.bl](../board/screens/lab.bl): idle with the matrix, an experiment open, phone. Canvas [screens/lab-decisions.bl](../board/screens/lab-decisions.bl) (owner, 2026-11-27): idle with Waiting for you, With bots, History and Best per use case, a use case's ranking, at 1440 and 390 px; it supersedes the idle matrix of all use cases.

## Current build

**App routes:** `/lab`, `/lab/<experiment id>`, `/lab/use-case:<id>`, `/lab/suite:<key>` and `/lab/new` in the operator app (`app/src/views/lab.tsx`, `app/src/views/lab-matrix.tsx`, `app/src/views/lab-create.tsx`, checked by `tests/ui/app.check.ts`). Built to the Target frame in FLT-53, except the routes: `/lab/use-cases/<id>`, `/lab/suites/<key>` and `/lab/E-FLT-7` are not built yet. Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Data.** One read for the screen, `lab_view` ([data layer](../../designs/fleet-data-layer.md)): the experiments, the use-case registry, each use case's project (`useCaseProjects`) with the project names (`projectNames`), the open rating questions and the last 30 days' comparison across use cases. It honours the header focus as every cross-project screen does (`scopeIds`): the experiments, their ratings and the history pane of the focused projects, without ignored ones; and the comparison of the focused projects (`lab_leaderboard` `projectIds`, `excludeProjectIds`). The use-case registry stays whole: it is the shared catalogue. An opened use case or suite reads its own `lab_leaderboard`, an experiment `lab_experiment_get`. The change stream rereads `lab_view` on experiment, score, question and execution changes, at most once in 5 s.

**Layout of `/lab`.**
* Control column: search, the saved views All, Mine (runs the person requested, with a link to Personal Lab drafts) and Waiting for a decision, plus a person's own views (the search query), then one [catalogue tree](../controls/catalogue-tree.md) with the roots Use cases and Runs, grouped by project (owner, 2026-11-26: global Lab is the same pages, grouped by project). A use case's project is the project of its experiments; when they are in several, the project it is named after (`dam:…` in DAM), else the one with most experiments, else the newest. Use cases no experiment ran keep their collections (`LLM Code Benchmarks`, `Real Work`, `Fleet & Projects`); folded `Archived` holds obsolete ones. Under Runs a project's runs read active, drafts, finished, stopped. A run view shows the Runs root only. Shell header: New experiment.
* Work area, idle: Waiting for you (only the person's own finished runs with a choice between variants, `lab_view` `waiting`; an agent's decision or a gate is never theirs; undecided finished runs as [question cards](../controls/decision-block.md#card) with Adopt and Keep baseline, and under the card each variant's score, succeeded of trials, median time and cost per trial from the leaderboard groups of that run; a typed answer is recorded as `inconclusive` with the text as its reason; example approvals as decision blocks; open ratings); With bots (agents' open decisions and failed gates, `lab_view` `withBots`, each with its owner, passive, [OR-71](../owner-rules.md)); History; then **Best per use case** over 30 days: one row per use case with its own best variant, score, median and runs, no Promote, since scores of different use cases cannot be compared. A collection's comparison is the same table. Titles are the record's `title` ("antigravity 1.5.18 pin · PR #1175"), else its group or goal. `bench:*` use cases are under LLM Code Benchmarks and `real:*` under Real Work whatever project ran them.
* Comparison (`lab_leaderboard`), within one use case only: variants that are retired (profile or pipeline left the library), have no score, or whose every run failed are not ranked, and one line counts them with a link to History. The Model × Pipeline matrix of [matrix cells](../controls/matrix-cell.md) (rows by model, columns by pipeline, both ordered by best score; one score form, the one most trials were scored with), Promote best with its scope (the project of the winning cell's latest experiment; Promote sets that project's recommendation, there is no system scope yet), and Run in Lab on a use case. Below 600 px of width the same cells are a ranked [table](../controls/data-list.md#table-row): Variant, Score, Median, Failed.
* Use case open: [detail head](../patterns/list-detail.md#board) with state, maturity, Lab fit and Run in Lab; goal, expected outcome, approved examples; the comparison scoped to the use case.
* Launcher: Launch creates the draft, confirms it by readback and launches it in one press (Run in Lab, then Launch: 2 clicks from a use case); Create draft keeps the draft for edits.

**Layout of `/lab/<id>`.**
* Head ([detail head](../patterns/list-detail.md#board)): project (a link to its pipeline page, `/projects/<CODE>/pipelines/<id>`) and series, goal as title, the [state dot](../controls/state-badge.md#dot-and-word) (a finished undecided run waits for you), code, the live step of the furthest running trial (`step 2/3`), Launch / Resume / Stop, and 1-click `Promote` for the winning or adopted variant when `done`.
* Spend: [totals](../controls/number.md) for trials, wall time, Pipe calls and credits, each with its basis and the budget limit and action.
* Matrix: rows are examples, columns variants, each a [matrix cell](../controls/matrix-cell.md) (state, primary-evaluator score, `k/n` comparable repeats, live step, median time and cost, a failed cell's cause inline with Retry). A click on a cell opens its trials under the matrix: a summary per evaluator (never combined across forms) and one line per repeat: trial state (reason when failed or left out of comparison, both in the state tooltip and concisely inline below the trial line), select for rating, repeat, live step progress (`step k/n` from `planSummary` while running/preparing), time, credits, tokens (`spend.tokens`), USD cost with cost basis (`cost`), scores, run link, cancel or retry. Confounders (host, harness and version, model, web route) on the repeat's tooltip.
* Loop strategies: retained iteration, state, output reason and execution ID; explicit stop reason in the experiment state. Proposal labels remain beside generated variant IDs.
* Variants: [diff rows](../patterns/definition-editor.md#diff-row) against the baseline with `Inherited defaults (N)` folded; spread across repeats per evaluator; Promote shows the catalogue patch inline or the recommendation it set.
* Decision gate: judge-to-owner agreement, separately by evaluator/rubric version, and pairwise order checks (agree, order-dependent, invalid output or pending; both runs retained); Lab priority is background (no effect until the scheduler).
* Ratings: decision blocks with outputs labelled A, B, … in the seeded order, answered from the keyboard; "rated k of n".

| Inventory item | Shown by |
|---|---|
| Experiments: variants, evaluators, examples | Experiments list; experiment head and matrix |
| Trials per variant with scores, time and spend, each with its basis | Matrix cells |
| Comparison of variants, with drill-down to runs | Matrix summaries, variant spread, run links |
| Assessments and recommendations | Ratings, decision gate, Promote |

## Create experiment

New experiment in the catalogue's head opens `/lab/new` in the work area; `/lab/new?useCase=<id>` (or selecting a use case via the inline [EntityPicker](../controls/entity-picker.md)) prefills a fresh editing draft's group slug, `ab` kind with two baseline variant slots, default evaluator (`script:example.checks:metric` or use-case check) and primary evaluator, matching approved examples, and default `Goal` / `Hypothesis` / `Decision rule` folded behind a `Disclosure` so standard A/B comparisons launch in 2 clicks without manual JSON. Per-variant `EntityPicker` and quick A/B preset buttons select agent/profile overrides and pipeline pairs without raw JSON, while `CodeEditor` stays behind an overrides disclosure for advanced overrides. Searchable project, published pipeline and approved-example choices use the control column; selection returns focus to the invoking field. The form and each column retain one scroller. Back/Close preserve the actor-scoped local draft; discarding it does not cancel a request already sent.

Use [text fields](../controls/text-field.md), Choice, CodeEditor for advanced configuration objects, and the existing list/selection controls. All existing single/A-B/loop kinds, variant/evaluator/strategy options and optional budgets follow the [Lab contract](../../designs/fleet-lab.md); no new algorithm, authority, numeric cap or automatic launch is added. Goal/hypothesis remain launch prerequisites rather than new create-time restrictions. Show unset budgets and unavailable costs honestly.

Create draft calls `lab_experiment_create` once, then reads `lab_experiment_get` for the same id/project/pipeline and draft state before opening the existing detail. Creation starts no trials or provider calls. Launch remains a separate existing action. A lost/malformed reply or commit-uncertain error retains an unconfirmed outcome and cannot automatically POST again, including after reload. A confirmed reply followed by unavailable details retains its id and permits only a read retry. Ordinary server grants/validation remain authoritative; errors retain edits and never substitute another project, pipeline or example.

Actor/grant changes and sign-out clear local drafts through the existing cache-clear lifecycle. Restoring a pending request marks it unconfirmed, never absent. No offline writes are queued. Empty choices name the missing prerequisite without API/curl instructions.

CI checks actual draft creation/readback with zero trial/native/provider work, validation and uncertain-response recovery, and captures representative images. Native/owner visual acceptance remains unqualified until actual permitted evidence is reviewed. Local browser and nested design-check wrappers remain prohibited.

**Open.** The variant configuration diff.

## Access changes and authored input

Quarantine locally authored draft input under its original validated identity. Restore only after the same identity freshly authorizes its project; never restore another actor's text. Server-derived receipt/result fields are cleared on access invalidation and require authorized readback. Unknown creation outcomes retain uncertainty and never automatically resend. Captured generation guards every storage/readback/success callback, so late replies cannot repopulate or clear a new actor's draft.

## Trial plan inspection

“View trial plan” opens a read-only detail within the experiment, shows any trial `problem` reason at the top, and fetches only that trial through authorized `pipeline_get`. It uses the shared StepList to distinguish pending, selected execution, not selected and blocked. Unavailable resources stay unavailable; reused and unconfirmed observations retain their labels. The canonical result and project assessment artifacts have separate links. Current identity/grant fences apply to late reads; unavailable plans show an explicit empty/error state, without a write or automatic retry of work.
