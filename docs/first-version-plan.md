# First-version plan

Proposed technical work package for the accepted [first working slice](delivery-strategy.md#first-working-slice). Fleet's specification defines the architecture; existing products supply evidence and candidate mechanisms. This plan does not adopt a stack or authorize production migration.

## Target composition

Start with two runtime responsibilities: a Fleet coordinator and a host executor. They may initially share a machine. Agent-facing tools call the coordinator's operations; future web views use the same state and operations. Libraries are not additional services.

| Part | Prepare | Candidate source |
|---|---|---|
| Fleet coordinator | Work/session associations, configuration resolution, dispatch, control outcomes and result retrieval. | New design against Fleet; TaskQ supplies selected failure cases and mechanisms. |
| Host executor | Working-area preparation, runtime calls, event delivery, resource observation and cleanup. | Assess AgentQ/CoreQ mechanisms against Fleet requirements before choosing reuse, adaptation, extraction or new code. |
| Agent-facing tools | A small callable surface over coordinator operations, with actual request/response examples. | Existing native tools for files, commands and browser; Fleet tools for Fleet state and actions. |
| Persistence | Coordinator records, retained history and artifact locations; executor records pending delivery. | Assess existing mechanisms without inheriting their product state models. |
| Preview | Serve the result with an operator-accessible address and explicit lifetime. | Select an existing suitable facility or the smallest isolated implementation. |

No full existing product is assumed to fit unchanged. The [refreshed inventory](component-reuse-review.md#first-version-inventory-refresh) records the evidence and limits.

Design component boundaries from Fleet requirements before selecting prototype code. Existing repository names do not determine Fleet's packages or services. The [first-version technical design](first-version-design.md) proposes the complete execution path and the assessment needed before adopting dependencies.

## Package to prepare before implementation

| Deliverable | Required decision | Complete when |
|---|---|---|
| Component and repository map | Ownership of coordinator, executor and adapters; reused library boundaries. | Each first-slice responsibility has one owner and an explicit reuse/adaptation/new-code choice. |
| Agent preparation example | Definition references, project overrides, actual loaded instructions, tools and dependencies. | One complete configuration maps to the chosen runtime without an invented unsupported field or hidden host dependency. |
| Operation contract | Configure/read configuration; start; observe/history/results; send input/answer; stop. | Each operation has inputs, outputs, authority, identities and observable error outcomes; retrying an uncertain request does not silently start another execution. |
| Data and transport design | Durable ownership, event ordering, result storage, host authentication and reconnect behavior. | A launch and its observations can be traced across both processes; pending versus confirmed outcomes are explicit. |
| Stack decision | Coordinator language, executor integration, storage, transport and agent-tool binding. | Choices match the previous deliverables and have a small build/deployment path; alternatives and material tradeoffs are recorded. |
| Qualification recipe | First harness/model, execution host, test workspace, browser and preview access. | Prerequisites and concrete checks map to the accepted completion evidence; untested platforms remain explicit. |

Keep these in one technical design with linked examples, not a new document per operation. Select technology after examining the native integration and data requirements. Do not add a broker, workflow engine or separate observability service without a demonstrated first-slice need.

## Build order

1. **Configuration to native execution.** Resolve the example definition and run through the selected adapter in an isolated working area. Inspect loaded inputs, native events and process outcome. Establish one reliable harness path before adding alternatives.
2. **Durable coordination.** Accept an assignment through Fleet operations, dispatch to the executor, retain state/history and retrieve the outcome after client reconnection. Distinguish acceptance, actual start and completion.
3. **Interaction and control.** Deliver another message or answer to the intended session, observe its delivery, stop owned work and report an execution failure. Retain useful history and files.
4. **Website delivery.** Execute the Hello World assignment, inspect the actual browser result and return source changes, evidence and accessible preview through the same result path.
5. **Operational proof and gap review.** Inspect correlated timings, CPU/memory/disk observations and available native token/cost data. Check working-area retention/cleanup and recorded failures. Compare the evidence with the architecture baseline and prioritize the next iteration.

Each increment adds to the same running path. Instrument operations as they are implemented; the final review verifies coverage rather than adding observation afterward. A thin agent-tool adapter is sufficient initially; full operator and architect panels remain later work.

## Test boundary

The first qualification can use one host and one harness. Keep coordinator and executor boundaries explicit so the subsequent remote check uses the same operations. PDS-18, Mac and Windows remain the target topology, not an assertion of first-version platform coverage.

Check client close/reopen, controlled stop and execution failure under the [completion evidence](delivery-strategy.md#completion-evidence). Before claiming distributed reliability, additionally exercise executor disconnection, coordinator restart and reconciliation. Record these as outstanding when not demonstrated.

Native usage may not expose price or every child event. Preserve available measurements and coverage gaps; do not infer zero usage or complete history. A running web server is not proof that the operator can open the preview.

## Scope and next decision

Do not migrate existing TaskQ work, replace production services or require all prototype products for the PoC. Full pipeline authoring, laboratory optimization, advanced quota balancing and complete web panels remain outside this slice under the delivery strategy.

Review the proposed technical design as one package, then resolve its implementation choices through targeted prototype assessment. This inventory does not establish implementation estimates: integration gaps and exact runtime qualification remain unresolved.

The assessments are now consolidated in [implementation decisions](v1-implementation.md), including the first build batch and remaining bounded choices. Use that sequence for implementation; earlier assessment proposals are supporting evidence.
