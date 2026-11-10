# Specification guide

[Fleet](../fleet.md) is the contract. Each topic below has one target file: the current rule for that topic. Read and edit the target. Files in the last column hold older or partial text on the same topic; the [docs rework](designs/fleet-docs-governance.md#rework-plan) merges them into the target and deletes them. Accepted design is not shipped behaviour; check the code. Document kinds and what an agent may add: [docs governance](designs/fleet-docs-governance.md).

## Topics

| Topic | Target file | Summary | Merges into the target |
|---|---|---|---|
| Lab | [designs/fleet-lab.md](designs/fleet-lab.md) | Experiments over example sets, variants, evaluators, Promote, the `/lab` screen; build stages. | [lab.md](lab.md), [work-analysis.md](work-analysis.md) |
| Operator app | [designs/fleet-operator-app.md](designs/fleet-operator-app.md) | The operator web app, its sections and live change feed. | [interfaces.md](interfaces.md), [designs/legacy-inventory.md](designs/legacy-inventory.md) |
| View URLs and list path | [designs/fleet-view-urls-and-list-path.md](designs/fleet-view-urls-and-list-path.md) | How a view URL is built and parsed; the filtered list path and its check. | none |
| Conversations | [designs/fleet-conversations.md](designs/fleet-conversations.md) | One thread per task, paging, typed transcript blocks, question and result cards, the Results page. | [conversations-no-reply-rca.md](conversations-no-reply-rca.md) |
| People and access | [designs/fleet-people-access.md](designs/fleet-people-access.md) | People, identity, per-host secrets, holders, subscriptions, take, dispatch, intake, budgets. | [resources-and-auth-model.md](resources-and-auth-model.md), [multi-account-same-env.md](multi-account-same-env.md), [layered-isolation-model.md](layered-isolation-model.md) |
| Data foundation | [designs/fleet-data-foundation.md](designs/fleet-data-foundation.md) | Postgres store, typed tasks and executions, artifact store, ingest, isolation; to split into store, ingest and isolation. | [state-ownership.md](state-ownership.md), [designs/fleet-data-foundation-review/phase3/live-findings.md](designs/fleet-data-foundation-review/phase3/live-findings.md) |
| Connections | [designs/fleet-connections.md](designs/fleet-connections.md) | API key connections, the Fleet gateway, per-run tokens, call methods. | [connections.md](connections.md) |
| Stack | [designs/fleet-stack.md](designs/fleet-stack.md) | Stack plan: rule delivery, JSON Schema contracts, MCP servers as executor subcommands, Go and Rust trial. | [delivery-strategy.md](delivery-strategy.md) |
| Contracts | [designs/fleet-l3-contracts.md](designs/fleet-l3-contracts.md) | One JSON Schema for event payloads and the host protocol (stack L3, stage 1). | none |
| Toolchain | [designs/fleet-toolchain.md](designs/fleet-toolchain.md) | Build, bundle and release toolchain. | none |
| Executor delivery | [designs/fleet-executor-delivery.md](designs/fleet-executor-delivery.md) | Executor release files built in CI and how they are fetched. | none |
| PM agent | [designs/fleet-pm-agent.md](designs/fleet-pm-agent.md) | Director-scoped agent tools, task tools, Held tasks, focus, watch mode. | [system-operations.md](system-operations.md), [execution-contract.md](execution-contract.md) |
| Orchestration | [orchestration.md](orchestration.md) | Coordination semantics over executions. | none |
| Allocation | [work-allocation.md](work-allocation.md) | Which host, environment and agent configuration runs a task; balancing and operator steering. | none |
| Configuration | [configuration.md](configuration.md) | Project settings, their layers and defaults. | [project-context.md](project-context.md), [capabilities-and-pipelines.md](capabilities-and-pipelines.md), [project-settings-matrix.md](project-settings-matrix.md), [project-settings-decisions.md](project-settings-decisions.md), [project-setup-and-access.md](project-setup-and-access.md), [real-projects-survey.md](real-projects-survey.md) |
| Deployment | [deployment.md](deployment.md) | Physical responsibility boundaries and hosts. | none |
| Runtime integration | [runtime-integration.md](runtime-integration.md) | Responsibility boundaries for agent execution. | [runtime-boundary-review.md](runtime-boundary-review.md), [harness-session-comparison.md](harness-session-comparison.md), [agentq-reuse-boundary.md](agentq-reuse-boundary.md), [agent-tools-inventory.md](agent-tools-inventory.md), [component-reuse-review.md](component-reuse-review.md) |
| Execution continuity | [execution-continuity.md](execution-continuity.md) | Preserving work and continuing after an execution stops. | none |
| Coordinator build | [coordinator-build.md](coordinator-build.md) | Local build, test and run of the coordinator. | none |
| Executor build | [executor-build.md](executor-build.md) | Executor build and checks. | none |
| Infrastructure | [infrastructure-build.md](infrastructure-build.md) | PDS-18 installation and release path. | none |
| PDS Codex environment | [pds-codex-environment.md](pds-codex-environment.md) | Second native environment on PDS-18. | none |
| Prototype plan | [prototype-implementation-plan.md](prototype-implementation-plan.md) | T1–T16, cited by executor and coordinator code; deleted when code stops citing it. | [architecture-breadth-plan.md](architecture-breadth-plan.md), [project-pipeline-prototype.md](project-pipeline-prototype.md), [spec-coverage-review.md](spec-coverage-review.md); to `evidence/` if still cited: [test-results-T1-T16.md](test-results-T1-T16.md), [multiharness-native-evidence.md](multiharness-native-evidence.md), [multiharness-resume-repair.md](multiharness-resume-repair.md), [multiharness-repaired-native-evidence.md](multiharness-repaired-native-evidence.md) |
| Use cases | [use-cases.md](use-cases.md) | Accepted scenarios for the system. | none |
| Website example | [website-example.md](website-example.md) | First representative design exercise. | none |
| Docs governance | [designs/fleet-docs-governance.md](designs/fleet-docs-governance.md) | Document kinds, what an agent may add, the docs report and `docs-order` issue, the rework plan. | none |
| Design system | [design-system/README.md](design-system/README.md) | How design work is done; start here for any UI work. | [design-system/methodology-brief.md](design-system/methodology-brief.md) |
| Owner rules | [design-system/owner-rules.md](design-system/owner-rules.md) | The owner's design rules; read first in every design or UI session. | [design-system/owner-rules-table.md](design-system/owner-rules-table.md) |
| Design principles | [design-system/principles.md](design-system/principles.md) | Usability rules every screen follows. | none |
| Tokens | [design-system/tokens.md](design-system/tokens.md) | Colour, type, spacing, radius, elevation, motion. | none |
| Interface inventory | [design-system/inventory.md](design-system/inventory.md) | What people must see, configure, choose and decide. | none |
| Design decisions | [design-system/decisions.md](design-system/decisions.md) | Owner design decisions with options and reasons. | none |
| Controls | [design-system/controls/README.md](design-system/controls/README.md) | One file per control, for example [button](design-system/controls/button.md). | none |
| Patterns | [design-system/patterns/README.md](design-system/patterns/README.md) | Compositions of controls for recurring jobs. | none |
| Pages | [design-system/pages/README.md](design-system/pages/README.md) | One file per page: purpose, layout, controls and patterns used. | none |

Briefs, review reports, handoffs, screenshots and prototypes still in `docs/` are working papers: the docs report lists them as `docs.paper`, and step 2 of the rework folds their surviving findings into the targets and deletes them. [designs/README.md](designs/README.md) lists the design folder.

## Citations from code

Code and configuration cite these files by path. A citation is one of two kinds:

- **Loaded path:** a program reads the path. The only ones are `source.path` and `source.anchor` in `config/lab/use-cases/*.json`, read and hashed by `scripts/lab-drift.ts`: today 11 entries in `fleet.json` load [use-cases.md](use-cases.md). A loaded path moves only together with its data and a new hash. An entry whose `source.repo` is not `fleet` names a file in that repository. The `docs/` mentions in `contracts/lab-config.json` are description text, so they are comments.
- **Comment:** every other citation (code comments, `ci.yml`, `schema.sql`, READMEs, book text). It is fixed in the same pull request that moves the file.

The docs report checks both (`docs.cite`) and lists them on each pull request (`docs.touched`).

## Implementation references

Use [AgentQ, Orca and T3 Code](harness-session-comparison.md) as recurring implementation references. Fleet owns its architecture; assess reusable mechanisms and policy coupling before importing code or dependencies. AgentQ remains a prototype, not an adopted runtime dependency.

Local source checkouts: `~/Projects/agentq`, `~/Projects/orca`, `~/Projects/t3code`. Record the inspected revision in each assessment. Check upstream changes deliberately; do not overwrite local work.
