# Experiments from recorded work

Use [retained execution evidence](../fleet.md#observation-and-evaluation) to investigate real problems, compare changes and improve project results. Laboratory execution uses the shared run mechanism.

## Source and variants

Select a recorded step with its inputs, instructions, effective configuration, results and errors. Link each experiment to that source.

Retain accessible input materials needed for reproduction, not only names or mutable links. Preserve them before cleaning the source working area. Record unavailable inputs and differences in external state; reproducible conditions do not guarantee identical model output.

Define each variant's changes explicitly: skills, tools, model, settings or workflow. Keep unchanged conditions tied to the recorded source rather than silently resolving them to newer defaults. Show the difference from the source.

## Execution and comparison

Run the selected step afresh, with repeated trials when useful. Reuse unchanged preparation results where valid; run required dependencies when inputs or prerequisites change. Do not substitute a cached result for the step being evaluated.

Retain actual inputs, configurations, outputs, failures and resource use for each trial. Compare variants against stated criteria and link assessments to the evidence.

A laboratory run does not modify the product. Separate experimental generation and evaluation from publication or other changes to live project state.

## Applying an improvement

Updating defaults or recommendations for future work and applying improvements to existing work are separate choices.

For existing work, select the steps to redo and include affected dependants. New results pass the project's normal verification, acceptance and publication requirements before reaching the product. Preserve prior results and the links between source work, experiment and replacement results.

## Scenario: improve an asset generation step

1. An architect finds a project run with poor assets or excessive tool calls and opens the responsible step's evidence.
2. The architect creates variants using its saved inputs and changes the skill or tool configuration.
3. Laboratory runs generate fresh assets; the architect compares quality, time, cost and failures and records an assessment.
4. The architect chooses whether to update the recommendation for future work and whether to apply the improvement to selected existing assets.
5. The selected generation steps and affected assembly steps run again. Their results pass project checks and publication rules; the original product results remain in history.
