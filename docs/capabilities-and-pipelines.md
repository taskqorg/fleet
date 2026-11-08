# Capabilities and pipelines

The [catalogue](../fleet.md#tools-and-pipelines) describes what callers can obtain and the methods available to produce it.

## Outcome, implementation and invocation

| Concept | Meaning | Example |
|---|---|---|
| Capability | Expected outcome with an input and result contract. | Create a 3D model from a description and optional references. |
| Method | A way to provide that capability. | Direct generation API, an agent using Blender, or generation with review. |
| Tool | The callable interface used to request the capability. | An agent operation accepting a description and returning model files and check results. |

These distinctions do not require separate services. Alternative methods satisfy the advertised contract, including file formats, textures, accessible artifacts and check results where required. Follow the existing explicit-choice and recommendation rules when selecting a method.

## Pipeline execution

A pipeline is a process executed by the system. Its caller supplies inputs and receives results without coordinating its internal steps.

Steps may call APIs through tools, execute programs, launch agents or invoke other pipelines. An agent can call a pipeline that launches another agent; results return through the calling chain.

An agent step receives a work assignment and chooses actions within it. The definition need not enumerate every internal agent action; expose significant stages when they need explicit coordination, comparison or repetition.

A single-step pipeline is valid. A simple API call need not be wrapped in a pipeline; direct tools and pipelines share invocation and observation conventions.

MCP exposes tools to agents. A tool's implementation may call an API directly or execute a pipeline. Provider configuration, accounts and key pools belong to connections, not to the calling agent's instructions.

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
