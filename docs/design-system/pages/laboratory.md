# Laboratory (architect)

## Target

**Status.** Target, rail section Lab ([information architecture](../../designs/fleet-ia-and-journeys.md#1-closed-information-architecture)). It keeps the built Lab's content and changes its frame; the sections after **Current build** stay true for the content.

**Purpose.** Which model, harness or pipeline is best for each use case right now, and how do I test a new one ([J1–J3](../../designs/fleet-ia-and-journeys.md#3-core-user-journeys-j1j5)).

**Routes:** `/lab` (idle: leaderboard), `/lab/use-cases/<id>`, `/lab/suites/<key>`, `/lab/E-FLT-7` (one experiment, [D17](../decisions.md#2026-11-10-d17-human-entity-codes)), `/lab/new?useCase=<id>`. Old `/lab/<uuid>`, `/lab/use-case:<id>`, `/lab/suite:<key>` and `/personal-lab` redirect (`/personal-lab` to `/lab` with the saved view "Mine").

### Layout

Three columns ([OR-24](../owner-rules.md)). Shell header: title, New experiment (`plus`), focus chip, search.

* **Control column** (`CatalogueTree`, [OR-48](../owner-rules.md)): two roots, **Use cases** (LLM Code Benchmarks, Real Work, Fleet & Projects, folded Archived) and **Runs** (Active, Drafts, Completed, folded Stopped); one tree, no mode switch ([OR-46](../owner-rules.md)). Above it the list header with search and saved views (Mine, Waiting for a decision). A leaf: state icon, name, code, best score on the right.
* **Work area, idle** (J1, under 5 s): what waits for the person first (ratings and undecided series as question cards, [OR-53](../owner-rules.md)); then the comparison of the selected collection: **Model × Pipeline** with score, median wall time and failure rate in each cell ([owner choice](#owner-choices-target)); cost and tokens fold into the cell's Details ([OR-54](../owner-rules.md), [OR-59](../owner-rules.md)); a winning cell has Promote (J3).
* **Work area, use case open:** goal and expected outcome, approved examples, the comparison scoped to this use case, and **Run in Lab** (J2).
* **Work area, experiment open:** head (state, title, code, Launch, Stop, Promote), the matrix (examples × variants, cells with state and score, live step `2/3 · review`), variants as differences from the baseline ([OR-45](../owner-rules.md)), the decision gate, ratings.
* **Work area, new:** the create form with EntityPicker choices; Launch in at most 3 clicks from a use case (J2).

### States

* **A run working:** cells update in place with fixed-width figures ([OR-6](../owner-rules.md)).
* **A trial failed:** the cell is in the danger tone with its `problem` cause inline; one click opens the trial (UR-63).
* **Phone (390 px):** the tree; an opened use case shows the ranked list of variants instead of the matrix.

### Owner choices (target)

1. **Idle comparison.** *Recommended:* a Model × Pipeline matrix, the form J1 names. *Other:* a ranked list of variants (score, time, failure rate), one row each, easier on a narrow screen. Both are drawn on [screens/lab.bl](../board/screens/lab.bl).

### Blocks

* [Navigation rail](../controls/nav-rail.md), [shell header](../patterns/app-shell.md).
* Catalogue rows (tree), list header, saved views, filter search, detail pane frame and form fields (block list, PR C).
* [Question card](../controls/decision-block.md#card) for ratings and undecided series; [state dot and word](../controls/state-badge.md#dot-and-word); [button](../controls/button.md) for Promote and Run in Lab.
* Table row and matrix cell (stand-ins until masters exist).

### Design board

Canvas [screens/lab.bl](../board/screens/lab.bl): idle with the comparison, an experiment open, both comparison forms, phone.

## Current build

**App routes:** `/lab`, `/lab/<experiment id>` and `/lab/new` in the operator app (`app/src/views/lab.tsx`, `app/src/views/lab-create.tsx`, checked by `tests/ui/app.check.ts` and `tests/ui/lab-create.check.ts`); mockup `#/lab` in [mockups.html](mockups.html) shows the earlier one-page layout. Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Built in Lab Stage 3 ([D18](../decisions.md), [fleet-lab.md](../../designs/fleet-lab.md#lab-screen)).

**Purpose.** Compare variants of a pipeline over examples, inspect trials, rate outputs, decide and promote.

**Columns (D19).** [List and detail](../patterns/list-detail.md): the control column holds a `Use cases | Runs` mode switch, query `SearchField`, "New experiment" action, "Waiting for you", and a [catalogue tree](../controls/catalogue-tree.md) (`OR-12` pinning, persisted collapse per person, `OR-22` state badges); the work area holds the opened experiment, or the suite leaderboard, use-case card and questions view while none is open.

**Layout of `/lab`.**
* Control column (`Use cases` default on `/lab`; `Runs` auto-selected on `/lab/<id>`):
  * `Use cases` tree: `LLM Code Benchmarks` (`bench:*` or `llm-code-benchmarks` tag), `Real Work` (`real:*` or `real-work` tag), `Fleet & Projects` (active `fleet:*`, `cs:*`, and other non-archived entries), plus a folded `Archived / Obsolete` folder (`status === 'obsolete' || status === 'duplicate'`). Leaves show drift state first ([state badge](../controls/state-badge.md) family `source`), goal, ID, and best observed score.
  * `Runs` tree: `Active`, `Drafts`, `Completed`, and folded `Stopped` folders (max 3 levels), with experiment state, series/goal, kind/variants, undecided marker, and age.
* Work area (`Use cases` mode):
  * Undecided finished experiments warning above the workspace when experiments wait for a decision.
  * **Summary strip** (`<Totals />`, when no specific use case is open): `Active use cases`, `Approved examples`, `Leaderboard entries`, and `Undecided series` — replacing the duplicate registry table so the `Use cases` `CatalogueTree` remains the single suite navigator.
  * **Use-case Card** (when a use case is selected): `id`, `goal`, `tags`, `status`, `lab_fit`, `maturity`, approved example count/readiness (matched by `useCaseId` first, falling back to slug and token-set matching), source reference, and a 1-click `"Run in Lab"` CTA opening `/lab/new?useCase=<id>`.
  * **Suite Leaderboard** (`lab_leaderboard` scoped to `{ useCaseId }`, `{ collection }`, or `{}`): compact 7-column sortable semantic table designed to fit inside the 1440 px work area without horizontal clipping, with two-line cells for `Variant` (model/profile subline + use-case link when unscoped), `Pipeline` (evaluator subline), `Score` (mean with min–max spread), `Runs` (`succeededTrials / trials`), `Median` wall time, `Tokens / Cost` (tokens and USD cost + basis `measured` / `subscription` / `estimated` / `unavailable`), and `Actions` (`EntityCode`, open link, age, and 1-click `Promote` via `lab_promote` on winning rows).
* Work area (`Runs` mode): [Questions view](../../designs/fleet-lab.md#lab-screen) grouping series by `labGroup` (newest goal, hypothesis, experiments table, and decision).
* Withdrawn ratings stay visible as history with their reason and no answer controls; they never count as pending or as owner scores.
* "Waiting for you": open ratings count; drafted examples awaiting approval as [decision blocks](../controls/decision-block.md) (pass or fail).

**Layout of `/lab/<id>`.**
* Head: experiment state, title, ID, Launch / Resume / Stop, and 1-click `Promote` for the winning or adopted variant when `done`.
* Spend: [totals](../controls/number.md) for trials, wall time, Pipe calls and credits, each with its basis and the budget limit and action.
* Matrix: rows are examples, columns variants. A cell shows a summary per evaluator (never combined across forms) and one line per repeat: trial state (reason when failed or left out of comparison, both in the state tooltip and concisely inline below the trial line), select for rating, repeat, live step progress (`step k/n` from `planSummary` while running/preparing), time, credits, tokens (`spend.tokens`), USD cost with cost basis (`cost`), scores, run link, cancel or retry. Confounders (host, harness and version, model, web route) on the repeat's tooltip.
* Loop strategies: retained iteration, state, output reason and execution ID; explicit stop reason in the experiment state. Proposal labels remain beside generated variant IDs.
* Variants: spread across repeats per evaluator; Promote shows the catalogue patch inline or the recommendation it set.
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
