# Experiments

Use [retained execution evidence](../fleet.md#observation-and-evaluation) and new assignments to compare ways of working. Laboratory execution uses ordinary agents, tools, pipelines and environments; it adds variants and comparison, not a separate executor.

## Source and variants

Start from a recorded step, task or project assignment, or provide a new assignment and its inputs. Link recorded sources where available. A prior run and a predefined pipeline are not required.

Retain accessible input materials needed for reproduction, not only names or mutable links. Preserve them before cleaning the source working area. Record unavailable inputs and differences in external state; reproducible conditions do not guarantee identical model output.

Define each variant's changes explicitly: instructions, skills, tools, model, settings or workflow. For recorded sources, keep unchanged conditions tied to that source rather than silently resolving them to newer defaults. Show differences and unavailable conditions.

Development methodologies such as SDD and role behaviors such as PM planning are configurable subjects of evaluation, not built-in requirements. Discuss findings in ordinary sessions and change the smallest relevant definition.

## Execution and comparison

Run the selected scope afresh, with repeated trials when useful. Reuse unchanged preparation results where valid; run required dependencies when inputs or prerequisites change. Do not substitute a cached result for the work being evaluated.

Retain actual inputs, configurations, outputs, failures and resource use for each trial. Compare variants against stated criteria and link assessments to the evidence.

Criteria may cover result quality, instruction adherence, human intervention, repeated work, time, tokens, cost and infrastructure use. Human comments and automated checks may both support an assessment; numeric scoring is not mandatory. Unknown measurements remain unknown; one successful trial does not establish consistent performance.

A laboratory run does not modify the product. Separate experimental generation and evaluation from publication or other changes to live project state.

## Numeric evaluation

A trial may return one or several named numeric measurements alongside checks and comments. Record each measurement's unit, source, evaluation procedure and relevant conditions. Define which values to minimize, maximize or keep within requirements; a single combined score is optional.

Compare candidates under comparable evaluation conditions. Retain individual measurements when trials repeat, not only their aggregate. Distinguish measured values, estimates and missing data. Changing the evaluation procedure or requirements is an explicit experiment change, not evidence that the candidate improved.

## Iterative improvement

An assignment may ask an agent to propose a change, run a trial, inspect measurements and choose the next change. Instructions may describe the search strategy in ordinary text; agents may invoke programs, pipelines or further agent sessions. No dedicated optimizer or fixed algorithm catalogue is required.

Configure the objective, allowed changes, evaluation criteria and budget for the assignment. Iteration may continue until the budget is reached, a target or other configured stopping condition is met, or the operator stops it; a fixed attempt count is not required. Preserve candidates, measurements, decisions and the best qualifying results so the work can resume.

Account for all nested work against the assignment's budget, including agents, evaluators, tools and infrastructure. Show spent and committed resources and remaining budget where known. Admission of further work and handling of active operations at the limit follow configured resource rules; delayed or estimated charges remain explicit. Child sessions do not receive an independent budget by default.

Optimization must preserve the assignment's correctness and quality requirements. A faster incorrect compiler is not an improvement. Record failed candidates and trade-offs rather than silently replacing the best result with the latest attempt.

## Scenario: improve a compiler

Given a compiler assignment, correctness checks, benchmark programs and a budget, an agent builds a candidate and measures compilation time and resource use. It proposes a change, tests another candidate and compares the measurements under comparable conditions. It may delegate implementation or analysis to another session within the same budget.

Retain valid candidates and their measurements across iterations. At the stopping condition, return the best qualifying results, changes, spent resources and unresolved limitations. Selection for product use follows the application rules below.

## Applying an improvement

Updating defaults or recommendations for future work and applying improvements to existing work are separate choices.

For existing work, select the steps to redo and include affected dependants. New results pass the project's normal verification, acceptance and publication requirements before reaching the product. Preserve prior results and the links between source work, experiment and replacement results.

## Scenario: improve an asset generation step

1. An architect finds a project run with poor assets or excessive tool calls and opens the responsible step's evidence.
2. The architect creates variants using its saved inputs and changes the skill or tool configuration.
3. Laboratory runs generate fresh assets; the architect compares quality, time, cost and failures and records an assessment.
4. The architect chooses whether to update the recommendation for future work and whether to apply the improvement to selected existing assets.
5. The selected generation steps and affected assembly steps run again. Their results pass project checks and publication rules; the original product results remain in history.
