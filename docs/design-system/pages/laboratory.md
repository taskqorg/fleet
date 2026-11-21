# Laboratory (architect)

**App routes:** `/lab`, `/lab/<experiment id>` and `/lab/new` in the operator app (`app/src/views/lab.tsx`, `app/src/views/lab-create.tsx`, checked by `tests/ui/app.check.ts` and `tests/ui/lab-create.check.ts`); mockup `#/lab` in [mockups.html](mockups.html) shows the earlier one-page layout. Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md). Built in Lab Stage 3 ([D18](../decisions.md), [fleet-lab.md](../../designs/fleet-lab.md#lab-screen)).

**Purpose.** Compare variants of a pipeline over examples, inspect trials, rate outputs, decide and promote.

**Columns (D19).** [List and detail](../patterns/list-detail.md): the control column holds "Waiting for you" and the experiments catalogue; the work area holds the opened experiment, or the use-case registry while none is open. The lists below keep their content and move to these places. Not built: the `#/lab` mockup still has the earlier one-page layout, and at 1100 px and below the registry cannot be reached while no experiment is open.

**Questions view** ([requirements](../../designs/fleet-lab.md#lab-screen)). The experiments catalogue lists series as questions: the newest goal as the title, the series name and its count of experiments, the answer or how many wait for one; groups waiting for a decision, answered, stopped. The opened experiment reads question, steps (question, design, runs, scores, decision), answer card (decision, reason, who and when, or confirm and override), a comparison table with one column per variant, a confounders warning, then the matrix and spend below.

**Layout of `/lab`.**
* Withdrawn ratings stay visible as history with their reason and no answer controls; they never count as pending or as owner scores.
* "Waiting for you": open ratings count; drafted examples awaiting approval as [decision blocks](../controls/decision-block.md) (pass or fail).
* Experiments: [data list](../controls/data-list.md), state first, title = pipeline · kind · variants, short ID, age.
* Use-case registry: organized by source/collection — `LLM Code Benchmarks` (`bench:*` or `llm-code-benchmarks` tag), `Real Work` (`real:*` or `real-work` tag), `Fleet & Projects` (active `fleet:*`, `cs:*`, and other non-archived entries), plus a folded `Archived / Obsolete` disclosure (`status === 'obsolete' || status === 'duplicate'`). Each row shows drift state first ([state badge](../controls/state-badge.md) family `source`), goal with ID, fit, maturity.

**Layout of `/lab/<id>`.**
* Head: experiment state, title, ID, Launch / Resume / Stop.
* Spend: [totals](../controls/number.md) for trials, wall time, Pipe calls and credits, each with its basis and the budget limit and action.
* Matrix: rows are examples, columns variants. A cell shows a summary per evaluator (never combined across forms) and one line per repeat: trial state (reason when left out of comparison), select for rating, repeat, time, credits, scores, run link, cancel or retry. Confounders (host, harness and version, model, web route) on the repeat's tooltip.
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

New experiment in the catalogue's head opens `/lab/new` in the work area. Searchable project, published pipeline and approved-example choices use the control column; selection returns focus to the invoking field. The form and each column retain one scroller. Back/Close preserve the actor-scoped local draft; discarding it does not cancel a request already sent.

Use [text fields](../controls/text-field.md), Choice, CodeEditor for advanced configuration objects, and the existing list/selection controls. All existing single/A-B/loop kinds, variant/evaluator/strategy options and optional budgets follow the [Lab contract](../../designs/fleet-lab.md); no new algorithm, authority, numeric cap or automatic launch is added. Goal/hypothesis remain launch prerequisites rather than new create-time restrictions. Show unset budgets and unavailable costs honestly.

Create draft calls `lab_experiment_create` once, then reads `lab_experiment_get` for the same id/project/pipeline and draft state before opening the existing detail. Creation starts no trials or provider calls. Launch remains a separate existing action. A lost/malformed reply or commit-uncertain error retains an unconfirmed outcome and cannot automatically POST again, including after reload. A confirmed reply followed by unavailable details retains its id and permits only a read retry. Ordinary server grants/validation remain authoritative; errors retain edits and never substitute another project, pipeline or example.

Actor/grant changes and sign-out clear local drafts through the existing cache-clear lifecycle. Restoring a pending request marks it unconfirmed, never absent. No offline writes are queued. Empty choices name the missing prerequisite without API/curl instructions.

CI checks actual draft creation/readback with zero trial/native/provider work, validation and uncertain-response recovery, and captures representative images. Native/owner visual acceptance remains unqualified until actual permitted evidence is reviewed. Local browser and nested design-check wrappers remain prohibited.

**Open.** The variant configuration diff.

## Access changes and authored input

Quarantine locally authored draft input under its original validated identity. Restore only after the same identity freshly authorizes its project; never restore another actor's text. Server-derived receipt/result fields are cleared on access invalidation and require authorized readback. Unknown creation outcomes retain uncertainty and never automatically resend. Captured generation guards every storage/readback/success callback, so late replies cannot repopulate or clear a new actor's draft.

## Trial plan inspection

“View trial plan” opens a read-only detail within the experiment and fetches only that trial through authorized `pipeline_get`. It uses the shared StepList to distinguish pending, selected execution, not selected and blocked. Unavailable resources stay unavailable; reused and unconfirmed observations retain their labels. The canonical result and project assessment artifacts have separate links. Current identity/grant fences apply to late reads; unavailable plans show an explicit empty/error state, without a write or automatic retry of work.
