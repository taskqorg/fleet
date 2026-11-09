# Specification guide

[Fleet](../fleet.md) owns the target architecture. Supporting documents below provide canonical detail unless explicitly labelled proposal or review. Accepted design is not shipped behavior. The [coverage review](spec-coverage-review.md) identifies current gaps and proposed next work.

The [architecture baseline and first working slice](delivery-strategy.md#architecture-baseline) define the current design boundary, completion evidence and next technical-design stage. Earlier review suggestions do not override this delivery order.

## Current specification

| Area | Detail |
|---|---|
| Configuration and project context | [Configuration](configuration.md), [project context](project-context.md), [capabilities and pipelines](capabilities-and-pipelines.md) |
| Work and communication | [Operations](system-operations.md), [execution contract](execution-contract.md), [orchestration](orchestration.md), [state ownership](state-ownership.md) |
| Resources and runtime | [Deployment](deployment.md), [allocation](work-allocation.md), [connections](connections.md), [continuity](execution-continuity.md), [runtime boundary](runtime-integration.md) |
| Evaluation | [Experiments](experiments.md), [work analysis](work-analysis.md) |
| Product experience | [Interfaces](interfaces.md), [web-first delivery](delivery-strategy.md), [use cases](use-cases.md) |

## Proposals and coverage studies

- [Distributed contracts](distributed-contracts-proposal.md): detailed semantics remain labelled proposal; do not infer adoption from a push.
- [Work lifecycle](work-lifecycle-proposal.md): not adopted; coverage study, not mandatory stages.
- [Architecture map](architecture-map.md): synthesis; its next package and first vertical slice are proposals.
- [Current coverage review](spec-coverage-review.md): findings and proposed method, not new product rules.
- [Website example](website-example.md): selected first design exercise; illustrative instructions and exchanges, not executed work or final API contracts.
- [Hello World agent kit](../examples/hello-world/README.md): concrete role instructions, specification and assignment; runtime bindings remain unverified.

## Evidence and historical context

- [Component reuse](component-reuse-review.md) and [runtime boundary review](runtime-boundary-review.md): source evidence, not native qualification or adoption decisions.
- [Prototype gap review](prototype-gap-review.md) and [simplification review](architecture-review.md): dated observations; canonical detail takes precedence over their snapshots.
- [Session handoff](session-handoff.md): historical conversation context, not current work instructions.
- [Character storyboard](prototypes/csgo-workflow.html): unapproved synthetic navigation study; not the accepted interaction design.

- [Connected projects, catalogue and pipeline prototype](project-pipeline-prototype.md)
- [Architecture coverage and evidence](architecture-coverage.md)
