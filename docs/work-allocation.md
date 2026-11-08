# Work allocation

Distribute project and laboratory work across registered hosts, environments and eligible agent configurations. Automatic balancing and operator steering are core capabilities, using the same operations from interfaces and agent sessions.

## Registered execution resources

Register a host's identity and connection, available environments, OS, software, harnesses and configured account references. Observe availability, last contact, active work, CPU, memory, disk and other relevant capacity. Separate configured capabilities, observed readiness and unknown state.

A host may initially expose one default environment. Additional environments on the same host share its physical capacity; registering them does not create additional resources. Account quotas may likewise be shared across several hosts.

## Placement and selection

1. Resolve task requirements, explicit choices, project rules, access and active allocation controls.
2. Find eligible environments and agent configurations. Preserve required software, OS, harness, skills and tools; an alternate harness must use a suitable configuration, not merely an available executable.
3. Select among eligible candidates using project recommendations, operator preferences, observed load, capacity and remaining quotas. Account for preparation and data transfer where known.
4. Launch within allocated resources and reevaluate queued work as availability, load or policy changes. Explain the selection or the reason no candidate can start.

A task may permit several harnesses or require a specific one. Changing a preference does not relax a requirement. If the only required resource is unavailable or prohibited, retain the work with the reason visible rather than silently substituting it.

## Operator controls

Controls may target hosts, environments, projects, harnesses or accounts. Support project/work placement rules, preferences, concurrency limits, stopping new assignments to a resource and restoring eligibility.

Harness policy distinguishes preferred, eligible only when required, and prohibited. Reducing concurrency or adjusting a preference can conserve quota without prohibiting all use. A prohibition still applies to tasks requiring that harness; a requirement does not override it.

Examples of ordinary commands:

- Keep general work off the Mac; retain explicitly permitted Mac work.
- Route eligible work away from Windows while it has problems; restore it when ready.
- Run at most two concurrent assignments on this host; use more of another host's available capacity.
- Prefer eligible Codex configurations; reserve Claude for tasks explicitly requiring it.
- Reduce Antigravity usage as its quota falls, or prohibit new Antigravity launches.

An authorized PM or assisting agent applies the same controls as the interface. Record the scope and effective change, show affected queued work and distinguish configured policy from observed execution. Retain previous settings so a temporary intervention can be explicitly reversed. Do not require manual per-task reassignment for routine balancing.

## Running work and disconnection

Routing changes govern new assignments, including new steps and trials. They do not imply that active processes moved, stopped or changed harness. Moving active work requires an explicit supported interruption and continuation or handoff; keep the outcome visible.

Use connected eligible resources for new work. An offline host's current execution remains unconfirmed until reconciled, not automatically eligible for duplicate execution elsewhere. Follow [disconnection rules](deployment.md#disconnection-behavior).

Reserve and account for work already admitted across shared host capacity and account quota pools, including laboratory children. Apply configured quota and capacity policies to automatic balancing; estimated or missing measurements stay explicit. Newly available capacity does not authorize unlimited work.

## Explainability

Show eligible resources, active restrictions, load, quota information and placement reasons. Human and agent queries expose the same state. Balancing may start with understandable selection rules; a separate learning optimizer is not required. Algorithm and policy tuning can use retained work evidence and experiments.
