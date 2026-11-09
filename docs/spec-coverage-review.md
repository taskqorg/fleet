# Specification coverage review

Review of the current working specification and supporting documents, 2026-11-09, including accepted local changes not yet pushed. This assesses design coverage, not shipped capability. Findings and the proposed next package do not change canonical rules.

## Conclusion

The principal responsibilities and scenarios are described. The specification is not yet an implementation-ready contract: agent preparation, concrete tool/data interactions and session/work associations need representative definitions. Adding more workflow stages would not close these gaps.

## Coverage map

| Area | Defined | Missing next contract or evidence |
|---|---|---|
| Agent configuration | Canonical files, shared skills, project differences, actual configuration history. | One complete developer/PM example, its dependencies, native loading and cross-host comparison. |
| Tools and data | Operation groups, scoped queries, references and shared authority. | Minimal caller-visible contracts with concrete inputs, outputs, asynchronous outcomes and example responses. |
| Sessions and work | Free conversation, independent assignments, delegation, durable questions. | Explicit links and context/access behavior for projectless and multi-project sessions; one representative conversation. |
| Distributed execution | Persistent PDS-18, intermittent Mac/Windows, recovery and working areas. | Confirm detailed distributed proposal status; qualify native start, reconnect, material access and cleanup. |
| Allocation | Host/configuration eligibility, preferences, shared quotas and operator controls. | Selection among equivalent agent configurations; measurable handling of outstanding work and quota uncertainty. |
| Pipelines and results | Optional composition, typed outputs, project scope and review/rework. | One small executable definition and invocation; identify hard-coded policy that should be default or configurable. |
| Laboratory and analysis | Full assignments, qualitative/numeric evaluation, shared budget, iterative improvement. | One concrete evaluator result and linked trial; demonstrate full child accounting and instruction-adherence evidence. |
| Human interfaces | Web first, conversation and background work, result viewing and configuration inspection. | Accepted representative session/configuration view; current storyboard is not that evidence. |
| State and delivery | Fleet-owned work, optional boards, file-backed definitions, independent update cycles. | Storage/recovery implementation, backup/restore proof and optional integration contracts, after representative behavior is settled. |

## Priority findings

### F1. Same files do not yet prove the same prepared agent

[Configuration](configuration.md#consistent-preparation) requires identical selected content across hosts. It does not yet show how a real skill's files, scripts and dependencies reach each harness, or how inherited native-home/project instructions are recorded or controlled. A matching Git revision alone cannot establish the effective instruction set.

**Next:** prepare one representative agent on two environments, inspect actual loaded inputs and tool availability, and report platform adaptations. Keep the outcome distinction: content available, harness loaded it, agent followed it.

### F2. Tool groups are not yet usable contracts

[Operations](system-operations.md#operation-groups) and [data access](system-operations.md#data-access) describe capabilities, but an implementer cannot yet derive a consistent callable surface. This is the most direct gap against the owner's goal of giving agents sufficient tools.

**Next:** write concrete examples for discovering a method, invoking it, observing a run, reading a result, asking/answering a question and comparing history. Reuse their identities and query shapes. Add other operations only when an accepted scenario cannot be expressed with them; exact MCP packaging can wait.

### F3. Session flexibility lacks an explicit context/access mapping

[Interfaces](interfaces.md#interaction-model) allow sessions spanning projects; the [root authority rules](../fleet.md#system-requirements) describe project-scoped execution/history. The exact association for a projectless architect conversation or a session discussing two projects remains unspecified. This is a gap, not evidence that cross-project access currently leaks.

**Next:** describe one session spanning two projects and one task continued by another agent: current context, action target, question recipient, history visibility and result links. Do not create a mandatory task or repository for every conversation.

### F4. Fixed configurations and harness substitution need one explicit relation

The [agent definition](../fleet.md#agent-execution) selects a harness/model; [allocation](work-allocation.md#placement-and-selection) can prefer another eligible configuration. The requirement to preserve skills/tools is stated, but how an assignment identifies acceptable alternatives is not concrete.

**Next:** show a developer assignment eligible for Codex and Claude and one explicitly pinned to Claude. Establish which choices are defaults and which are requirements without introducing a new hierarchy of agent types.

### F5. Some pipeline policy is more rigid than the central principle

The [root pipeline rules](../fleet.md#tools-and-pipelines) require other independent steps to finish before a failed run ends and phrase review iteration limits as definition requirements. [Experiments](experiments.md#iterative-improvement) allow budget/target-controlled iteration without a fixed attempt count. These can describe different scopes, but that distinction is not explicit enough for implementation.

**Next:** clarify pipeline defaults versus configurable failure/stopping behavior. Preserve dependency correctness and recorded outcomes; do not prescribe a universal lifecycle or a fixed iteration count for all agent work. This review has not changed these policies.

### F6. Full-history and budget promises exceed current qualification evidence

The [root observation contract](../fleet.md#observation-and-evaluation) requires full retention, while native evidence and usage may be missing. [Work analysis](work-analysis.md) correctly preserves unknowns. No test in this design session establishes complete native tool/child capture, image retention or delayed-cost handling across hosts.

**Next:** specify the observable minimum and expose capture gaps, then qualify it. Decide where durable history/results survive host loss and test restoration. Do not weaken retention, invent missing usage or equate passing document lint with runtime proof.

### F7. Document growth obscures authority and progress

Canonical detail, proposals, historical reviews and a rejected interaction study sit together. The root's short detail list is not a complete reading guide. The distributed proposal remains labelled proposed despite being pushed; publication alone does not settle its decision status. The lifecycle study is explicitly not adopted.

**Next:** use the [document index](README.md), retain one canonical rule owner and resolve proposal status explicitly. Do not count new files or repeated approvals as additional validated coverage.

## Method review

Keep scenario-led design, shared primitives, explicit uncertainties and experimental evaluation. Change the working method in three ways:

1. Work through a representative agent, its instructions and tool calls before adding more abstract workflow descriptions.
2. For each gap record only the missing capability, proposed owning rule and an observable acceptance example. A scenario is covered when the agent has sufficient inputs/tools and the system can observe the outcome, not when every internal action is prescribed.
3. Distinguish accepted intent, specified contract, source evidence and executed proof. Close a discussion batch with what became concrete and what remains open; avoid treating prose completeness as implementation readiness.

## Proposed next package

Use the [Hello World website exercise](website-example.md) first: one shared developer configuration, a short project specification, ordinary tools and an accessible preview. A pipeline is optional. Describe resolved inputs, representative tool interactions and a run history supporting adherence review. Then test alternate hosts/harnesses and context associations from F3/F4. CSGO follows later for domain complexity.

First produce inspectable definitions and example exchanges, not a universal schema or a product UI. Then perform a bounded runtime qualification as a separately authorized step. Successful coverage of this package would justify choosing implementation technologies and a first vertical slice; it would not qualify the whole distributed system.

## Deferred implementation choices

Frontend framework, backend language, database, broker, exact wire names, transport and repository migrations remain open. They are not missing product capabilities and need not be decided to resolve F1–F4 at the representative-contract level.
