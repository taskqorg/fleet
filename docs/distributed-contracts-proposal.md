# Distributed contracts proposal

Proposed implementation-facing semantics for the five boundaries in the [architecture map](architecture-map.md). Field names below describe information, not wire schemas. Use existing task, execution, environment and result identities; no new workflow entities are required.

## 1. Host registration and observation

Registration establishes a stable host identity, authorized connection and initial environments. Store the operator's name and allocation policy separately from observed OS, software, harness support, capacity, running executions and last contact.

Reconnect updates the existing host rather than registering another copy. Removing eligibility for new work does not remove history or stop active processes. Registration does not authorize installation of missing software; environment preparation follows its existing permissions.

The host runs an execution component while available. It starts and observes local work, captures pending history and exposes supported controls. This is a responsibility, not a new mandatory daemon or product: reuse an existing suitable runtime or host component where qualified.

## 2. Assignment delivery and recovery

| Direction | Required information |
|---|---|
| Coordinator → host | Execution identity, command identity, selected environment and agent/tool configuration, assignment inputs, working-area request, applicable authority and resource allocation. |
| Host → coordinator | Command receipt or refusal; preparation progress; confirmed native start with its process/session association; subsequent observations and actual outcome. |

Store accepted command identity before starting its side effect. Delivery retries query or return that command's known outcome rather than create another execution. A deliberate retry of work has its own execution identity linked to the original.

Receipt, preparation, actual start and outcome remain distinct. After interrupted delivery or host restart, reconcile local records and observed runtime state. An ambiguous start remains unconfirmed; command identity alone cannot guarantee that an arbitrary external side effect occurred exactly once.

## 3. Work materials and results

Inputs identify repository state, explicitly selected drafts and required materials. The host resolves them into its working area and records the actual inputs used. Paths are local execution details, not the cross-host result contract.

Material access may reuse an accessible local copy or fetch from the configured repository or storage. Obtain task-required inputs, not a mandatory full copy of every project resource. Detect inaccessible or changed inputs and show their effect on the assignment.

Return typed result references, descriptions, availability and associated checks. Material already available in project storage need not be copied again. Locally retained output stays visibly local until accessible at the required destination. Cleanup follows [working-area rules](execution-continuity.md#working-areas), not merely process exit.

## 4. Native interaction and observation delivery

The adapter accepts supported input and controls and emits native-derived messages, tool activity, questions, usage and outcomes under the [execution contract](execution-contract.md). Fleet questions created through its tools need not depend on a harness-specific native question format.

Retain execution identity, event identity or position, source, observed time and available native associations. Reconnect resumes delivery from acknowledged history without repeating already recorded observations or counting usage twice. Preserve source order within an execution; cross-host timestamps do not establish a total causal order.

Answers identify their pending question; native permission replies retain native request identity and offered options. Delivery state is observable. Controls unsupported by the selected harness or mode return an explicit reason rather than a simulated success.

## 5. Allocation and accounting

The coordinator selects an eligible host/environment/configuration under the [allocation contract](work-allocation.md), then records its resource allocation before dispatch. The host rechecks local readiness; a changed condition can refuse preparation with a reason. This does not allow the host to silently substitute the selected method.

One host's environments share physical capacity. Configured account references identify shared quota pools across hosts without exposing credentials. Parent and child executions share assignment budgets; include outstanding allocations before admitting more work.

Start with explicit placement preferences, concurrency limits and available load/quota observations. Estimates remain estimates. Operator changes reevaluate queued work and are reported with scope and effective state; stopping active work is a separate control.

## Common end-to-end acceptance

1. Register Mac and Windows environments with persistent coordination on PDS-18; confirm identity, observed capabilities and eligibility.
2. Submit a controlled assignment; observe receipt, preparation and actual start on the selected host.
3. Deliver a question and answer through the same work record; retrieve an accessible output and its checks.
4. Disconnect during delivery or execution, reconnect and reconcile without a second logical assignment or duplicate usage records.
5. Change host concurrency and harness preference; verify the next eligible assignment's actual placement and explain work that must wait.
6. Run parent/child work under a shared budget; inspect costs, outstanding work and configured limit behavior.

These checks can use inert executors first. Native interactive behavior and provider usage require separate bounded qualification; synthetic checks cannot establish them. No hosts, grants or live tasks have been changed by this proposal.

## Decisions intentionally left open

Transport (including possible SSH use), host installation mechanism, storage technology, observation batching and artifact transfer implementation. Choose implementations against these contracts and existing component evidence, not by adding services for each row.
