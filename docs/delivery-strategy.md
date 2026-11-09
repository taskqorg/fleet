# Delivery strategy

Build both operator and architect experiences on the web first. Share UI components and system operations. Desktop packaging follows later for Windows, macOS and Linux; it must not introduce a second business-logic implementation.

## Independent change cycles

| Area | Priority |
|---|---|
| Web interface | Fast local feedback, preview, focused checks and deployment. UI-only edits do not rebuild native executors. |
| Coordination and application logic | Rapid iteration with persistent work state, observable outcomes and recovery across updates. Logic lives behind shared operations, not only in browser code. |
| Execution hosts and harness integration | Reliable process control, resource observation and platform integration. Reuse suitable compiled components without forcing their build cycle on every UI change. |

Technology boundaries follow responsibilities, not a requirement that every component use the same language. A small initial service/package count remains preferable where responsibilities can coexist cleanly.

Choose technologies by measured edit-to-preview and edit-to-deploy time, available libraries, maintainability, deployment effort and behavior under real workloads. Web delivery may still include bundling and checks; it does not promise zero build time or replace verification.

Version interfaces between independently updated components sufficiently to expose incompatibility. Updating the web interface must not cancel remote work or require restarting every executor. Coordination updates follow the existing recovery contract.

## Scope

This decision sets delivery order and change-cycle boundaries. It does not select a frontend framework, backend language, database, deployment tool or migration strategy. Validate a representative session/task view and a real remote execution route before adopting a stack for the full product.

## Architecture baseline

The [system specification](../fleet.md) and canonical detail in the [specification guide](README.md) are sufficient to begin technical design of the first working slice. Use them to assess implementation choices and results. Proposals, reviews and illustrative examples remain distinct from adopted requirements.

This baseline is a reference, not a freeze. Reopen a decision when a concrete conflict, missing capability or execution result warrants it; update its owning specification. Do not repeat settled conceptual design before each implementation step.

## First working slice

Configure an agent through system operations, assign the [Hello World exercise](website-example.md), execute it in a managed environment and retrieve its result, history and resource observations. Agent tools and future web interfaces use the same operations and state.

| Part | First-slice responsibility |
|---|---|
| Configuration | Select harness/model, instructions, skills, tools and environment requirements; retain actual prepared content. |
| Operations | Configure, start, observe, retrieve history/results, send input and stop execution. |
| Coordinator | Own the assignment and execution associations, dispatch to an eligible executor and retain observable outcomes. |
| Executor | Prepare the working area, launch/control the harness and report process state, events and resource observations. |
| Storage and observation | Retain configuration, session/tool history, results and available usage/resource measurements, with explicit gaps. |
| Delivery | Return website sources, browser-check evidence and an operator-accessible preview. |

Coordinator and executor remain separate responsibilities even when the first run uses one machine. The distributed target remains PDS-18 with intermittently available Mac and Windows executors. A local run does not prove remote execution or all-platform support.

Full operator/architect panels, production website deployment, advanced balancing and laboratory optimization are outside this first slice. Their architectural requirements remain; deferral does not remove them. The prepared instruction files are test inputs, not a completed runtime integration.

## Completion evidence

- A real assignment reaches source files, actual browser checks and an accessible preview through the integrated path.
- The selected configuration, execution state, tool activity, results and available resource measurements are inspectable through system operations. Missing evidence is identified.
- Closing the client does not stop accepted background work; reopening retrieves its retained history and outcome.
- A controlled stop and an execution failure produce observable outcomes and preserve available history and partial work. A stop request alone does not prove process termination.

Record demonstrated behavior and remaining gaps against this baseline. Completing the slice establishes a working path, not qualification of every architecture requirement or supported harness.

## Next stage: technical design

Produce one bounded technical design for this slice before implementation:

1. Recheck the [reuse candidates](component-reuse-review.md) at current source revisions. For each needed mechanism choose reuse, bounded adaptation or replacement with a reason; assign its code owner.
2. Specify process placement, authoritative data, working-area/artifact ownership and communication between coordinator and executor. Include the first local topology and its remote extension.
3. Define the concrete operation inputs, outputs, identities and observable failure outcomes. Keep native file, shell and browser tools where sufficient.
4. Select the minimum stack and dependencies based on those responsibilities, iteration speed and existing qualified components. Compiled execution components are candidates, not a blanket language requirement.
5. Order implementation into connected increments and map each completion criterion above to a reproducible check. Identify access, preview and runtime prerequisites without assuming them available.

The design is ready for implementation when component ownership, interfaces, storage, technology choices and verification are concrete enough to build this path. Unresolved issues outside the slice remain explicit follow-up work. This stage does not authorize native agent runs, deployment or changes to existing production systems.

After the first run, choose the next iteration from observed gaps and cost: remote environments, recovery, allocation, interface work or richer tasks. Do not expand the first slice to resolve every future scenario.
