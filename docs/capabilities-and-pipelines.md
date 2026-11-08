# Capabilities and pipelines

The [catalogue](../fleet.md#tools-and-pipelines) describes what callers can obtain and the methods available to produce it.

## Outcome, implementation and invocation

| Concept | Meaning | Example |
|---|---|---|
| Capability | Expected outcome with an input and result contract. | Create a 3D model from a description and optional references. |
| Method | A way to provide that capability. | Direct generation API, an agent using Blender, or generation with review. |
| Tool | The callable interface used to request the capability. | An agent operation accepting a description and returning model files and check results. |

These distinctions do not require separate services. Alternative methods satisfy the advertised contract, including file formats, textures, accessible artifacts and check results where required. Follow the existing explicit-choice and recommendation rules when selecting a method.

## Catalogue scope

Catalogue entries may be shared or project-specific. Project pipelines may compose shared methods with project requirements, tools and checks. Extract shared methods when reuse warrants it.

People and agents discover available shared entries and entries for their project context. Other projects' entries are not offered by default. Availability determines usable methods; recommendations guide selection among them.

## Pipeline development

Develop pipelines through ordinary agent sessions: discuss requirements, edit definitions, run trials and compare results. Use the [laboratory workflow](interfaces.md#architect-workflow) to assess methods and select project or system recommendations for subsequent tasks.

Follow the [project context](project-context.md) baseline for one-off work and when to formalize reusable methods.

## Pipeline execution

A pipeline is a process executed by the system. Its caller supplies inputs and receives results without coordinating its internal steps.

Steps may call APIs through tools, execute programs, launch agents or invoke other pipelines. An agent can call a pipeline that launches another agent; results return through the calling chain.

An agent step receives a work assignment and chooses actions within it. The definition need not enumerate every internal agent action; expose significant stages when they need explicit coordination, comparison or repetition.

Start with coarse steps. Split a step when its result, alternatives, reruns or resource use need independent control. A known procedure may run as a program without an agent.

Agents may adapt their approach within an assignment. Improving the current result does not automatically change the pipeline definition for future runs; changing that definition is a separate action within the assigned scope.

A single-step pipeline is valid. A simple API call need not be wrapped in a pipeline; direct tools and pipelines share invocation and observation conventions.

MCP exposes tools to agents. A tool's implementation may call an API directly or execute a pipeline. Provider configuration, accounts and key pools belong to connections, not to the calling agent's instructions.

## Results and transfer

A capability declares named required and optional outputs. An output may contain data or reference accessible material in its source system, with its type and description. Fleet need not duplicate materials already held elsewhere.

Downstream steps bind their inputs to these outputs. The system makes the material accessible in the receiving environment within permissions; callers need not know the producing machine's file layout.

Humans and agents use the same result through suitable representations. Material availability and verification outcome are separate: retain the artifact and its check report even when the artifact needs rework.

Code results may reference repository changes and provide a diff. Both human interfaces provide [result viewers](interfaces.md#result-viewing); storage and transport choices do not define the result contract.

## Alternative methods

| Capability | Method examples |
|---|---|
| Web search | Direct search API; search followed by page retrieval and processing. |
| Image creation | API provider A; API provider B; an agent using its native image capabilities. Each saves and returns the resulting image. |
| 3D creation | Direct generation API; an agent using generation tools and Blender; generation followed by review and rework. |

## Discovery, selection and fallback

Agents discover capabilities by the required outcome. Return relevant input and result contracts, usage conditions, a recommended method and alternatives. Describe each method's purpose, limitations, requirements and known quality, time and cost; identify unknown information.

Honor explicit task choices. Otherwise, use project recommendations, falling back to system recommendations. Routine calls need not ask the operator to select a provider.

A capability may configure permitted fallback methods. If the primary method is unavailable, use an eligible authorized fallback and report the substitution. Without an eligible fallback, offer available alternatives. Do not substitute an explicitly user-selected method without permission.

Fallback follows the shared external-effect recovery rules: an unconfirmed original outcome does not authorize another attempt through a different method.

Retain the methods actually selected, substitutions, effective settings, internal executions and results. A common callable interface does not hide implementation choices from history or analysis.

## Review and rework

A definition may explicitly branch on a review result and repeat rework up to a stated iteration limit. The system coordinates these transitions; the caller need not perform them manually.

A review requesting changes is a workflow outcome, not necessarily an execution failure. Operational failures follow the shared recovery rules.

When the iteration limit is reached, return the actual artifact, if available, and its review outcome. Exhausting attempts does not establish quality or task acceptance; project completion requirements still apply.
