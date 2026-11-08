# Placement and disconnection

Physical responsibility boundaries for the [shared core](../fleet.md#shared-core). Multiple execution hosts with intermittent availability are a target requirement. No database, transport, cloud provider or repository allocation is selected.

## Roles and placement

| Role | Owns | Placement |
|---|---|---|
| Human interface | Views, interaction and submission of commands. | Desktop application or browser. |
| Coordination | Accepted work, dependencies, questions, execution links, budget allocation and progression. | Local background process or server. |
| Execution host | Local runtime processes, working areas, tools, native history capture and resource observations. | Operator machine, dedicated computer or server. |

These roles may share a machine and package. A persistent server coordinates local and remote execution hosts; operator computers may disconnect while server work continues. An environment is an execution context, not necessarily a separate host. Register hosts and their environments under the [allocation contract](work-allocation.md).

Use one authoritative coordination state per installation as the initial model. Multiple interfaces and execution hosts share it. An independently writable offline replica and automatic multi-coordinator failover are not required by these scenarios.

## State and materials

Keep authored instructions and definitions in their canonical repositories. Coordination state retains accepted work, decisions and links to executions. Execution hosts retain local process evidence and pending observations until delivery is confirmed under the history retention rules.

Results may remain in project storage or execution areas under existing material and cleanup rules. Record their location and accessibility. A referenced file on an unreachable host is not yet available to a remote viewer; retain this distinction until transfer or reconnection succeeds.

Credentials stay in their configured connections or execution environments. Interfaces and coordination records reference access; placement does not require copying secrets into task history.

## Disconnection behavior

| Event | Expected behavior |
|---|---|
| Close the interface | Background coordination and execution continue where running. Reopening reads retained state. |
| Operator computer sleeps | Server work continues. Work and coordination hosted on that computer are unavailable until recovery; no seamless local execution is promised. |
| Execution host loses contact | Already accepted work may continue within its recorded authority and allocated resources. Retain local observations and results. Shared decisions or new allocations requiring coordination wait. |
| Coordinator stops | Do not start new centrally coordinated work. Existing host work follows the same accepted-work rules; recover state and reconcile before progressing. |
| Connection returns | Deliver pending observations and results, resolve command outcomes and reconcile running work. Reconnection does not create another assignment. |

Show last contact and unconfirmed state rather than inferring failure. Stop or pause requests aimed at a disconnected host remain pending until delivered and confirmed. Shared budget allocations must account for outstanding work; disconnection does not grant additional budget.

## Initial deployment target

The owner's current deployment scenario uses PDS-18 (Linux) as the persistent server for the laboratory and web interface, with Mac and Windows computers actively executing work when available. Place coordination on the persistent server. These are deployment targets based on the owner's description, not verified host state or hard-coded product identities.

SSH access is available in this scenario; Tailscale is not an architectural requirement. Select transport and enrollment mechanics later. Register another computer without changing project pipelines.

A single-machine installation remains useful for initial development and tests, not a substitute for distributed execution. Closing the window differs from stopping background processes or sleeping the machine.

Moving coordination between machines requires an explicit transfer of ownership and retained state, not two active writers. Migration mechanics remain a later design decision.

## Validation needed

Demonstrate close/reopen, executor disconnection, coordinator restart, pending answers, output transfer and budget accounting with outstanding executions. These are target behaviors, not observed implementation capabilities.
