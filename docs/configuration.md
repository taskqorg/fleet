# Configuration

Organization of existing settings, not new services, mandatory forms or a universal inheritance tree. Complements [project context](project-context.md), [allocation](work-allocation.md) and the [runtime boundary](runtime-integration.md).

## Canonical homes

| Home | Contains | References rather than copies |
|---|---|---|
| Shared catalogue | Agent configurations, role instructions, skills, tools, methods and pipelines. | Canonical definition files and their dependencies. |
| Project | Purpose, requirements, workflow overrides, recommended methods, resource bindings and delivery rules. | Shared definitions, project-specific definitions and project materials. |
| Agent configuration | Harness/model, behavior, assigned skills/tools and environment requirements. | Role and skill definitions, tool capabilities. |
| Method or pipeline | Input/output expectations, implementation choices, step instructions and transitions where applicable. | Agent configurations, tools and required capabilities. |
| Host/environment | Available software and runtime facilities, working-area preparation and placement/capacity controls. | Connection identities and shared host capacity. |
| Connection | Service access, accounts, known quota pools and permitted use. | Protected credentials; never copies into prompts or definition files. |
| Assignment | Specific outcome, inputs, explicit choices, requirements and applicable budget. | Project context and selected definitions. |

The catalogue is an index of definitions, not another independently editable copy. Shared defaults reduce per-project setup. A project can reference a shared developer and supply its own requirements without cloning the developer configuration.

## Consistent preparation

Prepare the selected agent definition and skill content from canonical files on every execution environment. The same selected configuration uses the same content across hosts; independently maintained machine copies are not configuration sources. Adapt local paths and supported harness/tool wiring without silently changing instructions.

Record the actual prepared content and tool availability. Later launches use current definitions unless another version or draft is explicitly selected. Do not assume edits are injected into an already running session.

Instructions guide the agent's actions, including whether and how to call pipelines. Review run history, tool calls, results and checks to assess adherence and improve the definition. Supplying an instruction does not prove it was followed, and identical content does not guarantee identical model behavior.

## Different kinds of settings

| Kind | Resolution |
|---|---|
| Defaults and preferences | Apply existing agent/role, project and task override rules; explicit selections override defaults. |
| Requirements | Combine applicable task, project and method requirements. Incompatible requirements need resolution, not silent replacement. |
| Authority and operational controls | Apply grants, budgets, eligibility and resource restrictions; task prose does not widen them. |
| Observations | Show current availability, measured load, reported quota and uncertainty separately from configured policy. |

Method parameters describe the selected method. They do not become another universal precedence layer: a method's required Blender capability is a placement requirement; its default quality setting may be an overridable parameter. Defaults and requirements must be distinguishable in their owning definitions.

Free-form instructions remain text. Give Fleet structured information only where it must select, route, invoke, account or recover. Do not promise to mechanically reconcile all semantic conflicts in arbitrary instructions.

## Effective configuration

For planned work, resolve an inspectable preview from current definitions, project differences, explicit assignment choices and allocation rules. At launch, retain the actual selected configuration and conditions under existing execution rules.

Show what was selected, why, and the source of relevant settings. Mark choices not yet resolved and prerequisites not yet observed. The preview is not a guarantee of future host availability, quota or runtime support.

This view is derived, not a new editable master configuration. Edit an item's source or explicitly change the assignment. Human interfaces and agent tools expose the same explanation.

## Changes and scope

Ordinary sessions may request configuration changes. Resolve the intended target and scope: this assignment, a project's defaults, a shared definition or current resource routing. Ask only when ambiguity materially changes the effect; do not require a confirmation form for every change.

Show the applied scope and source diff where applicable. Definition changes affect subsequent launches under existing source rules; live control is a separate operation. Temporary allocation controls retain their previous settings for explicit restoration.

Architects manage shared definitions and grants. Operators manage project work and granted controls; authorized agents use the same operations. A conversation's role label does not grant additional authority.

## Illustrative CSGO configuration

This is a design example, not the current project's measured configuration.

- A shared developer configuration provides chosen development instructions and tools. The csgo project references it and supplies game requirements and checks.
- A project character method references common generation/optimization tools and csgo-specific rig adaptation. Its steps declare the needed capabilities without embedding machine names.
- A particular character assignment supplies selected references and any explicit method choice. A Mac-only check requires a suitable Mac environment; general work follows eligible placement preferences.
- Reserving Claude for required work changes allocation policy, not every developer definition. A Codex alternative must still satisfy the selected role, skills, tools and assignment requirements.

The person can inspect one explanation of the planned or actual work without browsing every source. Detailed configuration remains available when needed.
