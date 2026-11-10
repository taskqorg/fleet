# Component reuse review

Source-based assessment against the distributed contracts, 2026-11-09. Candidates below are not adopted dependencies, deployment instructions or proven end-to-end capabilities.

## Evidence and candidates

Read local product contracts, source module declarations and selected execution/resource paths. Revisions pin the inspected evidence. Local unrelated changes and untracked work exist in several checkouts; inspected contract and source files were not changed by this review. Unmerged work and live deployments were not qualified.

| Component | Source revision and useful mechanisms | Boundary requiring qualification or change |
|---|---|---|
| AgentQ | [`37498ef`](https://github.com/taskqorg/agentq/blob/37498efab19dcf91b5014952d28b0b389a392cea/runtime/src/agent/adapter.rs): native launch, input, history, observation and controls; see [runtime review](runtime-boundary-review.md). | Does not establish Fleet dispatch, cross-host ownership, artifact transfer or total assignment accounting. Qualify actual harness/mode behavior. |
| CoreQ | [`e9f440b`](https://github.com/taskqorg/coreq/blob/e9f440b38c14a821b35590c1fb3fad7d0c8f2246/src/lib.rs): library modules for services, processes, enrollment, resources, updates and telemetry. | Infrastructure primitives, not Fleet's task model or coordinator. Reuse must preserve the consuming product's authority. |
| TaskQ | [`21d09b9`](https://github.com/taskqorg/taskq/blob/21d09b91dbeed82983135f6a4f19e40a9e555c2b/src/balance.rs): eligible-runtime selection using weights and load. [`resources.rs`](https://github.com/taskqorg/taskq/blob/21d09b91dbeed82983135f6a4f19e40a9e555c2b/src/resources.rs) contains host resource control exchange and receipts; module declarations include dispatch, recovery, conversations and environments' related host functions. | Its [contract](https://github.com/taskqorg/taskq/blob/21d09b91dbeed82983135f6a4f19e40a9e555c2b/taskq.md) makes boards the sole task state and fixes worker/reviewer/PM behavior. Fleet requires arbitrary assignments, configurable roles and shared project/laboratory coordination. Existing balancing is useful evidence, not proof of the combined host/configuration/quota scheduler. |
| Pipe / AssetQ | [`fdfeb8c`](https://github.com/taskqorg/assetq/blob/fdfeb8cb56f366cec64dd4e19f296fea31aa0042/src/exec.rs): durable job transitions, provider execution/polling and result import; store, API, registry and telemetry modules are present. | Its [contract](https://github.com/taskqorg/assetq/blob/fdfeb8cb56f366cec64dd4e19f296fea31aa0042/pipe.md) treats every external call as a pipeline and allocates product ownership differently. Fleet permits direct tools and general nested agent work. Do not create independently authoritative copies of the same execution. |
| DashQ | [`805b42d`](https://github.com/taskqorg/dashq/blob/805b42d4269851af3cf4a6b176316892a34b9c7d/src/main.rs): archive, accounting, host/resource views, questions and selected control modules. [`fleet.rs`](https://github.com/taskqorg/dashq/blob/805b42d4269851af3cf4a6b176316892a34b9c7d/src/fleet.rs) explicitly projects observed metadata. | Its [contract](https://github.com/taskqorg/dashq/blob/805b42d4269851af3cf4a6b176316892a34b9c7d/dashq.md) is read-only by default with specific exceptions. It is not an established general architect editor or Fleet coordination authority. |
| ServerQ | [`0cae86f`](https://github.com/taskqorg/serverq/blob/0cae86f5d229dd122ecbafcda03c164cb3523747/serverq.md): host/workload/disk inventory and checks; source declares inventory, discovery and telemetry modules. | Explicitly excludes command execution and repair. Inventory can inform environment discovery; it cannot be assumed to be the execution host. |
| PilotQ | [`c7f363e`](https://github.com/taskqorg/pilotq/blob/c7f363e8fe789723e3e04bb3a4e27cc53900d76d/src/main.rs): conversation, journal, manager, files, image results and messaging modules. | Its [contract](https://github.com/taskqorg/pilotq/blob/c7f363e8fe789723e3e04bb3a4e27cc53900d76d/pilotq.md) integrates CLI/Telegram and TaskQ. Reusable conversation/delivery mechanisms do not require Fleet sessions to inherit those channel or task-storage choices. |

## Findings

1. Native execution and host infrastructure have identifiable reuse candidates; a clean-sheet runtime rewrite is not justified by this review.
2. Scheduling and remote controls already have implementation material. The gap is their composition under Fleet's assignment, authority, quota and configuration semantics, not simply the absence of a balancing module.
3. Existing product contracts disagree with Fleet on task authority, fixed roles, mandatory pipelines and interface ownership. Wrapping every current service unchanged would preserve those conflicts.
4. The unified path from assignment through remote execution, durable questions, accessible results and shared accounting is not established by these source reads.
5. Registration and observation are not execution authority. ServerQ and DashQ cannot silently acquire host-command responsibilities through integration.

## Proposed implementation approach

Start with one Fleet-facing coordination path and reuse qualified runtime, process, enrollment, artifact and observation mechanisms behind it. Each accepted execution has one state owner; other products reference or project that state rather than independently completing the same work.

For each mechanism choose among direct reuse, a bounded adaptation in its owner, or replacement after a demonstrated mismatch. Compare extending TaskQ with a new coordinator only after isolating board-specific state and policy dependencies; this review does not choose a rewrite, migration or repository merger.

Validate three connected parts before broad UI work: registered execution host and native control; durable assignment dispatch and question/result return; eligible placement with shared resource accounting. Use the same path for a normal task and a laboratory trial.

## Follow-up decision

[State ownership](state-ownership.md) now makes Fleet authoritative for work and external boards optional. The [delivery order](designs/fleet-stack.md#delivery-order-and-change-cycles) prioritizes web interfaces with independent native execution updates. Estimate concrete reuse changes against these contracts before assigning repository ownership; this does not authorize changing existing TaskQ work.

## First-version inventory refresh

Local checkout review on 2026-11-09 for the first-version plan. Read current manifests, product rules, AgentQ adapter declarations, CoreQ exports and selected executor/module declarations. These are local source revisions, not deployed versions or claims about the latest remote branches. Unrelated local modifications were preserved.

| Component | Inspected HEAD | First-version assessment |
|---|---|---|
| AgentQ | `7c48127cd952d7b001207575f7c2ce7889c1ef51` | Strong reuse candidate: its runtime library separates native execution from product scheduling. Qualify configuration preparation, history, input and stop on the chosen harness. |
| CoreQ | `bed3ab4e3682cd00483ef798d96628d04dc85dc9` | Selective library reuse: process, service, resource, enrollment and telemetry exports. Do not import board-token assumptions or make it another running service. |
| TaskQ | `21d09b91dbeed82983135f6a4f19e40a9e555c2b` | Reference for balancing, host control and failure cases. Its board authority and fixed roles conflict with Fleet; do not adopt its whole coordinator. |
| Pipe / AssetQ | `fd0c79e95f48978f05f619fcdfe4fab03e1c3196` | Reference for durable execution and output retention. Its mandatory pipeline treatment of external calls is not Fleet policy. Not required for Hello World. |
| DashQ | `6916f30a1ab7cbcd1c09de937fff1d632e0882b9` | Reference for archive/accounting/resource views. Existing read-only projections do not supply Fleet's configuration or work authority. Not a required PoC control plane. |
| ServerQ | `dc85cc2077e114d94436c6e5accd969a5c39666d` | Optional inventory source later. Its explicit absence of a command channel makes it unsuitable as the execution host itself. |
| PilotQ | `400f0f5552cc74660bbff8124527902abc1e5518` | Reference for conversation/journal behavior. Telegram and TaskQ integration are not prerequisites for Fleet sessions. |

Current manifests already share AgentQ/CoreQ dependencies, but not all pin identical releases. A dependency declaration is not integration evidence. Select compatible revisions during technical design and verify the exact combined build later.

The recommended boundary is a Fleet-owned coordination design with selectively reused runtime/infrastructure mechanisms. New architecture does not imply rewriting every native adapter, importing all existing services or migrating their data in the first version.
