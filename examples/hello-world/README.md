# Hello World agent kit

Concrete instruction files for the first [website exercise](../../docs/website-example.md). Prepared for review; not installed or executed. This kit contains no website implementation.

## Files

| File | Use |
|---|---|
| [pm.md](pm.md) | PM role instructions: discuss, specify and delegate. |
| [developer.md](developer.md) | Developer role instructions: implement, check and report. |
| [hello-world.md](hello-world.md) | Project specification supplied to both agents. |
| [assignment.md](assignment.md) | Implementation request supplied to the developer. |

Role instructions are separate from project requirements. They can be reused with another specification. No pipeline or additional skill is needed for this exercise.

## Preparation

Select the harness/model and an eligible environment. Supply the chosen role file as instructions, together with the project specification, applicable project rules, working-directory path and actual tool descriptions. Supply the assignment when implementation is requested.

Resolve file references against this kit before preparing another workspace. The website working directory is a separate execution input; do not infer it from the location of these instruction files. Record the content supplied and any additional instructions loaded by the harness.

Use the same source files on each host. Harness-specific installation and tool bindings remain to be selected and verified; this directory is not an executable Fleet configuration.

## Tool bindings needed

These are capability contracts, not invented callable tool names. Use native harness tools where they already provide the capability.

| Capability | Input | Returned information |
|---|---|---|
| Read/write project files | Working-area path and requested read or edit. | File content or applied changes/errors. |
| Delegate to a developer | Assignment, specification, working area and selected agent configuration. | Execution reference and observed admission state. |
| Read execution | Execution reference and observation position. | Messages, questions, tool activity, results and current state. |
| Ask/answer | Question and relevant context, or a response to an existing question. | Linked response or pending state. |
| Run commands | Command and working directory. | Output, exit status or running-process reference. |
| Inspect browser | Preview address and viewport. | Rendered page, screenshot and available browser errors. |
| Provide preview | Website working area and configured preview facility. | Operator-accessible address, availability and lifetime, or failure. |

PM needs files, delegation, execution reads and questions. The developer needs files, commands, browser, preview and questions. Both return results through their session. A preview may use existing process/browser facilities; a new service is not required by this contract.

## First run

The initial PM message can be: “Help me specify a one-page Hello World website.” Use the supplied specification as the agreed target for comparison. When the operator requests implementation, pass the saved specification and assignment to the developer.

Inspect the resulting source diff, actual browser checks and preview access. Retain session history and available duration, token, cost and resource measurements. Missing measurements remain unknown.

Before a real run, bind the selected harness, working area, delegation and preview tools. Check that the role content was loaded and tools are callable. Then assess behavior from the run; preparation alone does not establish adherence.
