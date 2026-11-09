# Website example

First representative design exercise for Fleet. All dialogue, identifiers and operation examples below are illustrative, not executed calls or selected API schemas. No website, preview or deployment has been created.

## Scenario ladder

| Scenario | What it exercises |
|---|---|
| 1. Discuss → specify → implement → preview | Agent configuration, context, delegation, ordinary tools and observable results. |
| 2. Deploy the accepted page | Project delivery tools, authority and verification of the published result. |
| 3. Revise through conversation | Updated requirements, diffs, continuation and repeated checks. |
| 4. Diagnose and fix a bug | Reproduction, investigation, correction and evidence. |
| 5. Add more complex pages | Proportionate architecture discussion, specification changes and optional delegation. |
| 6. Compare approaches | Alternative instructions or agents, quality, human effort, time and cost. |

First follow the ordinary path. Then exercise another host/harness, lost connectivity, a question and quota exhaustion without simultaneously increasing product complexity. CSGO remains a later check for large assets, specialist software and domain viewers.

## Scenario 1: Hello World

### Starting context

A small project has a repository and working-area access. Fleet knows an eligible development environment, configured PM/developer agents and a preview facility accessible to the operator. Missing infrastructure is recorded as a prerequisite, not silently assumed available.

The example uses plain HTML/CSS because the requested page needs no application framework. This is an example choice, not a restriction on Fleet or later projects.

### Conversation and specification

**Operator:** “Let's make a small Hello World website. Help me write a short specification.”

**PM:** “One page, no backend. I suggest plain HTML/CSS, readable on a phone and desktop. The result will be source files, a browser check and a preview you can open.”

The PM discusses material choices and saves the agreed requirements using ordinary file tools. It does not ask the operator to populate a technical form or invent an architecture document for this page.

The [example project specification](../examples/hello-world/hello-world.md) records this scope. The [agent kit](../examples/hello-world/README.md) supplies concrete PM/developer instructions and an implementation assignment for review; no run has occurred.

**Operator:** “Implement this specification and show me the preview.”

This authorizes the assignment in the example; a second confirmation of the same work is not a mandatory stage. If implementation was already requested with the initial message, discussion and execution may proceed under that scope.

### Agent instructions and available tools

| Agent | Relevant instructions | Needed capabilities |
|---|---|---|
| PM | Read project context; clarify material gaps; keep the specification proportional; preserve the requested outcome; delegate when asked. | Project/context queries, ordinary file editing, assignment and agent invocation, work/result queries, questions and answers. |
| Developer | Read the supplied specification and project rules; implement the smallest matching result; inspect the actual browser rendering; return files, preview and checks; report unresolved limitations. | Files, shell/process execution, browser inspection, project preview operation, result reporting and questions. |

These are example instruction excerpts, not new fixed agent types. Configurations reference shared files; project context supplies differences. Fleet records the actual prepared content. No mandatory SDD skill, reviewer, pipeline or architecture ceremony is introduced.

### Representative operation exchanges

Labels are semantic operations, not final tool names. File edits, shell commands and browser work use existing harness tools where supported.

| Operation | Illustrative input | Expected response |
|---|---|---|
| Read project context | Project `hello-site`; purpose: implement its page. | Specification reference, applicable instructions, available methods and preview capability. |
| Find a developer | Development role; required file, shell and browser capabilities; project `hello-site`. | Eligible configurations and placement conditions; no execution starts from this query. |
| Assign implementation | Project, saved specification, expected sources/checks/preview, selected developer or permitted alternatives. | Task/execution references and actual admission state; starting remains separately observable. |
| Observe work | Execution reference and last observed position. | New messages, tool activity, questions, state and available usage; no second assignment. |
| Prepare a preview | Working-area/result reference and project preview settings. | Operator-accessible address, running state and its lifetime/availability information, or a concrete failure. |
| Return and retrieve results | Execution reference, source changes, preview reference and checks. | Linked materials and evidence usable from both conversation and task views. |

The developer chooses the actual editing and checking actions. The sequence above describes one possible run, not a system-enforced pipeline.

### Preview and checks

The developer opens the rendered page and checks the specified heading and layout at phone and desktop widths. Retain browser evidence and errors where present. Process exit or generated source alone does not prove the page renders correctly.

A remote host's `localhost` address is not an operator-accessible preview. The configured preview facility must provide actual access and report unavailability or expiration. A new public deployment is not implied by a preview request. Transport and hosting implementation remain open.

### What the operator sees

The same conversation contains the saved specification, linked assignment, progress and final result: “Page created”, accessible preview, source diff and concise check findings. A task view summarizes the same work; opening it is optional. Results and checks are actual observations, not prefilled success text.

### What the architect can inspect

- Actual instructions, skills, tools, harness/model and environment used.
- Whether the specification was read, which tools/methods were invoked and what they returned.
- Result files, browser checks, preview availability, questions and recovery where applicable.
- Observed duration, tokens, cost and host resources; missing values remain unknown.

Available content, native loading and behavioral adherence are distinct evidence. No claim that an agent read every instruction follows merely from including it in the prepared configuration.

### Completion and feedback

The assignment completes when its specified sources, checks and accessible preview are delivered under project rules. Production deployment is not required here. A visual correction continues through ordinary conversation with linked rework; it does not automatically modify shared developer instructions.

If repeated runs reveal a behavioral problem, discuss the evidence, change the appropriate instruction/tool/method and compare another run through the laboratory mechanism.

## What this exercise should resolve next

1. Bind the prepared agent kit to selected harnesses and verify how they load its content.
2. Turn the semantic exchanges above into a minimal callable contract, retaining ordinary harness tools where sufficient.
3. Define the preview facility's ownership and lifetime. Do not equate a running server with operator access.
4. Qualify the scenario on real executors as a separately scoped step. This document does not establish runtime support or authorize infrastructure changes.
