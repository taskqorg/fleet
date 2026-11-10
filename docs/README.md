# Specification guide

[Fleet](../fleet.md) owns the target architecture. Supporting documents below provide canonical detail unless labelled evidence or review. Accepted design is not shipped behavior; check the code. Technical designs live in [designs/](designs/README.md); UI rules live in [design-system/](design-system/README.md).

## Current specification

| Area | Detail |
|---|---|
| Configuration and project context | [Configuration](configuration.md), [project context](project-context.md), [capabilities and pipelines](capabilities-and-pipelines.md), [connected project prototype](project-pipeline-prototype.md) |
| Work and communication | [Operations](system-operations.md), [execution contract](execution-contract.md), [orchestration](orchestration.md), [state ownership](state-ownership.md) |
| Resources and runtime | [Deployment](deployment.md), [allocation](work-allocation.md), [connections](connections.md), [continuity](execution-continuity.md), [runtime boundary](runtime-integration.md) |
| Evaluation | [Lab](lab.md), [work analysis](work-analysis.md) |
| Product experience | [Interfaces](interfaces.md), [web-first delivery](delivery-strategy.md), [use cases](use-cases.md), [website example](website-example.md) |

## Build and operation

- [Coordinator build](coordinator-build.md): local build, test and run.
- [Executor build](executor-build.md): Mac executor build and checks.
- [Infrastructure](infrastructure-build.md): PDS-18 installation and release path.
- [PDS Codex environment](pds-codex-environment.md): second native environment on PDS-18.

## Prototype plan and its research

- [Prototype implementation plan](prototype-implementation-plan.md): T1–T16, cited by executor and coordinator code.
- [Architecture breadth plan](architecture-breadth-plan.md), [project setup and access](project-setup-and-access.md), [project settings matrix](project-settings-matrix.md), [project settings decisions](project-settings-decisions.md), [real projects survey](real-projects-survey.md): inputs of that plan.
- [Resources and auth model](resources-and-auth-model.md), [multi-account same env](multi-account-same-env.md), [layered isolation](layered-isolation-model.md): account and isolation research cited by the data foundation design.
- [Test results T1–T16](test-results-T1-T16.md): measurements cited by the data foundation design.

## Evidence and reviews

- [Coverage review](spec-coverage-review.md): findings, not product rules.
- [Component reuse](component-reuse-review.md) and [runtime boundary review](runtime-boundary-review.md): source evidence, not adoption decisions.
- [Multi-harness native evidence](multiharness-native-evidence.md), [Codex resume repair](multiharness-resume-repair.md), [repaired resume proof](multiharness-repaired-native-evidence.md): native qualification records cited by `evidence/`.
- [Conversations no-reply RCA](conversations-no-reply-rca.md): cited by the conversations design.
- [Toolchain handoff](handoffs/toolchain-choice-2026-11-10.md): input of the stack plan.
- [Character storyboard](prototypes/csgo-workflow.html): unapproved synthetic navigation study.

## Implementation references

Use [AgentQ, Orca and T3 Code](harness-session-comparison.md) as recurring implementation references; [AgentQ reuse boundary](agentq-reuse-boundary.md) and [agent tools inventory](agent-tools-inventory.md) hold the source findings. Fleet owns its architecture; assess reusable mechanisms and policy coupling before importing code or dependencies. AgentQ remains a prototype, not an adopted runtime dependency.

Local source checkouts: `~/Projects/agentq`, `~/Projects/orca`, `~/Projects/t3code`. Record the inspected revision in each assessment. Check upstream changes deliberately; do not overwrite local work.
