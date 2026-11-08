# Target model and prototype gaps

Review of the target Fleet model against taskqorg repositories, captured on 2026-11-09. This is evidence and a design proposal, not an allocation of Fleet responsibilities to existing products.

## Scope and evidence

Read the default-branch README and product contract of every other repository listed by the organization, following AssetQ's contract redirect to Pipe. Inspect selected architecture documents and source entrypoints for the major execution and interface paths. Revisions below pin the observations.

Repository documentation states intent and reported status; source inspection establishes code presence only. No service, native agent, UI, deployment or end-to-end path was executed. Unmerged branches and external infrastructure were not inventoried. Fleet is the local design under discussion, including its accepted but uncommitted result-viewing additions.

## Repository observations

| Repository and pinned evidence | What exists in the inspected material | What Fleet must decide independently |
|---|---|---|
| [TaskQ](https://github.com/taskqorg/taskq/blob/21d09b91dbeed82983135f6a4f19e40a9e555c2b/taskq.md) | Board-owned task state, workers/reviewers, PM, scheduling, workspaces and operator UI. [Source entrypoint](https://github.com/taskqorg/taskq/blob/21d09b91dbeed82983135f6a4f19e40a9e555c2b/src/main.rs) declares those functional modules. | Whether board-backed task storage fits arbitrary projects; no assumption that every project is a repository or every task is software development. |
| [AgentQ](https://github.com/taskqorg/agentq/blob/986f2b009dc6aca8d11bbc7196598302ef865384/docs/spec/RUNTIME.md) | Native-agent preparation, launch/resume, observation, control and history contracts. [Runtime library](https://github.com/taskqorg/agentq/blob/986f2b009dc6aca8d11bbc7196598302ef865384/runtime/src/lib.rs) exposes agent, archive, CLI and installation modules. | Agent configurations, skills and method catalogues are broader than a native runtime registry. Existing runtime coverage is not proof of the full Fleet agent lifecycle. |
| [AssetQ / Pipe](https://github.com/taskqorg/assetq/blob/fdfeb8cb56f366cec64dd4e19f296fea31aa0042/pipe.md) | Pipeline jobs, methods, assets, comparisons, recommendations, storage and a management site. [API source](https://github.com/taskqorg/assetq/blob/fdfeb8cb56f366cec64dd4e19f296fea31aa0042/src/api.rs) contains jobs, objects, methods, recommendation and comparison routes. README reports provider/deployment qualification limits. | Capability versus method versus tool; direct tools versus pipelines; general agent composition; integration into role-based interfaces. |
| [DashQ](https://github.com/taskqorg/dashq/blob/ebab2423a2c91aaa20d1d788cdb848249cad1ebe/dashq.md) | Product/host observation, resource views, guarded session archive and selected controls. [Source](https://github.com/taskqorg/dashq/blob/ebab2423a2c91aaa20d1d788cdb848249cad1ebe/src/main.rs) includes archive, enrollment and limits modules. | A general architect workspace cannot be assumed to follow a dashboard whose contract is read-only by default and excludes editing and scheduling. |
| [PilotQ](https://github.com/taskqorg/pilotq/blob/c7f363e8fe789723e3e04bb3a4e27cc53900d76d/pilotq.md) | Telegram/CLI conversation, authorized actions, delivery and TaskQ management; README reports a Rust service and test-bot trials without production cutover. | PM sessions and communications must not require Telegram or inherit this product's request and route model automatically. |
| [ServerQ](https://github.com/taskqorg/serverq/blob/0cae86f5d229dd122ecbafcda03c164cb3523747/serverq.md) | Read-only host, workload and disk inventory and checks. | Inventory is useful evidence, but does not define working-area allocation, environment preparation or cleanup. |
| [CoreQ](https://github.com/taskqorg/coreq/blob/687997c9bd497f0e9db4216cae5173131cd1002d/coreq.md) | Shared service, process, telemetry, release and connection primitives; [library modules](https://github.com/taskqorg/coreq/blob/687997c9bd497f0e9db4216cae5173131cd1002d/src/lib.rs) corroborate those areas. | A reusable library is not itself Fleet's product model, coordinator or management interface. |
| [SpecQ](https://github.com/taskqorg/specq/blob/d178c809b607d37696fe93b7572d85ed62f883a9/specq.md) | Shared engineering, documentation and design rules. | Existing consumer-specific organization is not a substitute for deciding Fleet's target product structure. |

## Concrete differences

1. **Project versus repository.** TaskQ R1 makes boards authoritative for task state. Fleet explicitly supports projects with several repositories or no code. Reuse must not turn an optional integration into the definition of a project.
2. **Tool versus pipeline.** Pipe declares every external service call a pipeline. Fleet permits a direct tool and treats a pipeline as one implementation method. This is a product-model difference, not a naming change.
3. **Role workflow versus product website.** Pipe owns its canonical asset/method workspace and DashQ links to it; Fleet requires convenient results in both operator and architect workflows. Existing website boundaries cannot decide the new navigation or viewer boundaries.
4. **Native runtime versus configured agent.** AgentQ's runtime registry describes native clients and support. Fleet also needs authored agent configurations, instructions, skills, tool assignments and environment requirements. These objects must be understandable together regardless of repository ownership.
5. **Scattered administration versus one architect workflow.** Configuration, accounts, resource controls, methods and evaluations are distributed among the inspected products. Fleet needs a connected route from observing a problem to changing a configuration, experimenting and applying the improvement.
6. **Telemetry versus analysis experience.** CoreQ observations, AgentQ histories, DashQ views and Pipe comparisons are useful ingredients. Their presence does not establish a unified path from a costly worktree or tool call to its source step and a reproducible experiment.

These differences do not establish that any repository must be retained, replaced or merged. Reuse decisions follow the target contracts and qualification of the candidate implementation.

## What the target description currently provides

Fleet defines roles, major responsibilities, execution scenarios, shared operations, result types, continuity and improvement workflows. This is enough to discuss functional scope and sketch candidate navigation.

It is not yet a complete information architecture for the manager interface. We have not agreed which objects have their own catalogues, what belongs on each object's page, which relationships are navigable, or where global and project-specific views meet. Interface workflows describe activities, not a finished set of sections.

## Candidate architect workspaces

The following is a proposal derived from Fleet workflows, not from existing repository names. Entries may be sections, linked catalogues or tabs; they do not prescribe services or databases.

| Workspace | Objects and work it brings together |
|---|---|
| Agents | Agent configurations, native harness/model choices, instructions, skills and assigned tools. |
| Capabilities and methods | Expected outcomes, alternative methods, tool interfaces, pipeline definitions and current recommendations. |
| Environments and resources | Execution environments, working areas, preparation/cleanup, capacity, consumption and retained space. |
| Connections and access | Service connections, accounts, available limits and project access. |
| History and analysis | Executions, results and viewers, tool calls, costs, resource use, failures and paths to source work. |
| Laboratory | Source steps, experiment variants, comparisons, assessments and explicit application of improvements. |

Examples of required navigation: a method opens its pipeline or implementation; an agent configuration opens assigned skills and tools; an execution opens the method, configuration, working area and outputs actually used; a problematic execution starts an experiment; an accepted improvement identifies the projects or work it will affect.

Project views should present the selected shared objects and project-specific differences without creating separately maintained copies. Exact editing and navigation boundaries remain to be discussed.

## Next design package

Agree on the object catalogues and navigation above, then describe one representative object from each workspace: what the person sees, changes, invokes and opens next. Use concrete search, image or 3D methods and a real agent configuration as examples without inheriting their current implementation restrictions.

Only then derive concrete method contracts and evaluate which existing components satisfy them. Physical service boundaries, technology choices and repository migrations remain later decisions. The current prototype contracts and the historical session handoff do not settle those decisions.
