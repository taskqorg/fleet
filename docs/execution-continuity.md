# Execution continuity

Requirements supporting [agent execution and environments](../fleet.md#agent-execution). Preserve completed work and make continuation practical when execution conditions change.

## Working areas

Pipeline steps declare required capabilities rather than machine identities. Architects configure suitable environments and preferences; callers may explicitly select an environment. Different steps may use different environments, while compatible steps may reuse one working area.

Execution prepares the selected environment and working area and makes required inputs accessible. A change of agent need not recreate the area; a change of environment requires access to the necessary materials. Show preparation progress, resource use and failures alongside execution.

An environment provides compute, software and access. A working area provides files for particular work: a worktree, a separate directory or an attached project directory.

Track each area's associated work, active users, origin (system-created or user-provided), retained materials and release conditions.

Prepare, use, preserve required results and history, then release. A pause or handoff may retain the area; one agent finishing does not authorize its deletion.

Automatically clean system-created temporary areas when no longer needed. Preserve existing user and project files. Check unfinished changes and uncertain executor state before cleanup; show retained or abandoned areas, why they remain and available cleanup actions.

Cleanup is an ordinary system operation using shared execution, retry and telemetry mechanisms. Trigger it from the area's release conditions after success, failure or cancellation, not solely as the pipeline's final step. Failed cleanup leaves occupied space and the failure visible; it does not invalidate a completed task result.

Measure preparation and cleanup duration, disk consumption and failures. Include their CPU and memory use in resource accounting. Worktree creation is visible work, not unaccounted setup.

Account for aggregate resource use across simultaneous preparation, execution and laboratory runs. Expose capacity, consumption and contention so concurrency respects configured resource limits.

## Session project selection

An ordinary session may explicitly select a granted project with `session_transition`. An omitted project may resolve only a single focused project; observation focus alone never changes execution authority. Multiple focused projects require explicit selection. A task-bound session cannot select a different project.

A selection during an execution is retained for the next execution, after the current execution ends. Preserve running and already queued execution snapshots. At application, recheck grants, prepare normal project checkout and rules, and carry the conversation through a handoff when project or working directory changes; incompatible native state is not resumed. Existing explicit holds remain in force.

Release old owned areas through cleanup using their original execution, project settings and directory. Retain dirty or unknown areas with a visible cleanup outcome. Workspace identity includes the project, host and workspace generation. Start and teardown carry the same immutable `workspaceKey`; absent keys preserve legacy directories. Each project change allocates an isolated directory and branch, so cleanup can run without racing the new checkout. Returning to a project uses a fresh generation and transcript handoff, while retained old work remains visible through its cleanup command and outcome. An active selection queues one internal continuation so the user request proceeds without another human message.

## Continuation and handoff

Distinguish exhausted account limits, lost connectivity, process failure and incompatibility. Show the cause, preserved state and available continuation actions.

Human and agent tools support waiting for recovery, using another authorized account, handing work to another agent and moving execution to another environment. Offer only actions supported by the target's capabilities and permissions.

Continue automatically within established rules; route substantive choices to the responsible person. Preserve the task and prior execution evidence across changes of executor.

A handoff carries the goal, accepted decisions, completed work, remaining steps, files, results and unresolved external operations. Provide sufficient working context and references to retained history; reading the entire history is not mandatory.

The system builds the handoff from retained data without requiring a response from the previous agent. That agent may supplement it when available. Identify missing or unconfirmed information rather than presenting an incomplete handoff as complete.

Coordinate access so handoff does not create conflicting writers to the same files. When the previous executor is unreachable, expose the uncertainty and establish safe access before continuing. Follow the [external-effect recovery rule](../fleet.md#system-requirements).

## Evidence for improvement

Apply [system-wide observation](../fleet.md#observation-and-evaluation) to preparation, execution, cleanup and recovery, including operations that fail before an agent starts. Retain their history for later queries and comparison.

Architects and assisting agents can inspect where resources went, which tools were called most often, and where failures or repeated manual work occurred. Use this evidence to assess changes to project settings, pipelines, skills, tools and infrastructure; assessment alone does not change production recommendations.
