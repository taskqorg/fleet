# V1 coordinator assessment

Proposed implementation package, not an accepted stack decision. Fleet's accepted requirements remain in fleet.md and the first-version design, plan and delivery strategy. This review used source only; no runtime, service, installation, deployment or paid experiment was run.

## Recommendation

Build a new TypeScript coordinator on Node.js LTS, with Fastify, a SQLite binding, and a thin stdio MCP adapter. Keep a Rust host executor behind a versioned HTTPS JSON exchange. Use one coordinator database and a local executor SQLite journal; store retained files on coordinator-owned disk. Do not introduce a broker, workflow engine, shared network database or required prototype service.

This proposed split separates the frequently edited coordinator/tool surface from compiled process control; neither it nor SQLite is an evidence-based performance winner. It costs a second language and protocol compatibility work. An all-Rust coordinator would reduce language count but couple application iteration to compilation; choose it only if the first measured edit/build cycle contradicts this proposal. PostgreSQL is unnecessary for the proposed single coordinator writer; revisit for demonstrated multiple active writers or availability needs. These are bounded design judgments, not benchmark results or claims about current library versions.

Use supported pinned dependency versions selected during implementation; this review does not qualify a runtime or SQLite binding. No time-sensitive external capability claim is needed for this proposal.

## Source evidence and adoption decisions

Paths below are relative to /Users/kirs/Projects/<repository>. Revisions identify inspected HEAD; local uncommitted material may exist and is not qualified. No modules were copied. Source observations do not prove runtime reliability.

| Repository / revision | Exact evidence | Fleet decision |
|---|---|---|
| TaskQ `21d09b91dbeed82983135f6a4f19e40a9e555c2b` | `src/runtime.rs:2778-2780` requires reconciliation of uncertain input; `:3762-3785` preserves unknown Claude launch outcomes; `:5094-5109` refuses replaced/changed native history. | **New** Fleet command/state ownership; **extract** these failure scenarios into executor qualification cases. Do not adopt issue authority, runtime orchestration policy or role prompts. |
| PilotQ `400f0f5552cc74660bbff8124527902abc1e5518` | `src/journal/schema.rs:5-20` refuses incompatible schema and starts WAL/FULL transaction; `:34-49` fingerprints instructions and creates temporary previews. `src/journal/requests.rs:204-236` avoids external replay and classifies interrupted work as uncertain; `:281-283` fences old runs. | **New** Fleet journal schema; **extract** transaction, fence and uncertain-outcome patterns. Do not copy Telegram admission, grants, conversation rebuild policy or TaskQ operation identities. Its temporary text preview is not durable website delivery. |
| DashQ `6916f30a1ab7cbcd1c09de937fff1d632e0882b9` | `src/store.rs:18-25` separates receipts, history and outbox; `:36-42` stores operations, telemetry and unique observations; `:601-613` opens WAL/FULL storage. | **Extract** receipt/outbox and bounded-storage patterns; **new** Fleet tables and authorization. Dashboard snapshots are observations, not task authority; do not depend on DashQ as coordinator. |
| AssetQ `fd0c79e95f48978f05f619fcdfe4fab03e1c3196` | `src/store.rs:1-6` describes content-addressed, fsynced immutable objects; `:65-84` validates SHA and exposes bounds. `src/api.rs:246-279` checks retained preview content and integrity. `src/model.rs:263,319,350-353` handles duplicate events and reconciliation restrictions. | **Extract**, after dependency review, immutable upload/hash/verification mechanism into the Rust executor; **new** coordinator blob receipt and retention implementation. Do not inherit provider jobs, submission policy or image-only previews. |
| ServerQ `dc85cc2077e114d94436c6e5accd969a5c39666d` | `src/telemetry.rs:139` exercises history import in a fixture. Targeted source search found no website artifact/preview store in `src/*.rs`. | **New** Fleet observations and preview path. No ServerQ service dependency; this limited search does not establish that all repository facilities are unsuitable. |

No reviewed mechanism qualifies for reuse unchanged. Extract means a bounded implementation candidate with its own dependency/license review and focused checks, not permission to copy a product wholesale. Changes in prototype owners are unnecessary for V1.

## Durable coordination and reconciliation

The coordinator transaction inserts the caller request, execution association and dispatch obligation before acknowledgement. Key requests by caller identity plus request ID; retain action, target and canonical payload digest. An exact retry returns the original receipt; the same key with another payload is a conflict. A command ID remains stable through dispatch, restart and reconnect. Keep task, execution, session, command and native session identities distinct.

Executor polling uses outbound authenticated HTTPS, avoiding inbound executor ports. Propose `POST /v1/hosts/poll`, `/receipts`, `/events` and `/artifacts`; polling returns commands and current protocol/capability information. Begin with bounded ordinary polling, not WebSockets. This adds latency and polling overhead but makes reconnect and durable acknowledgement explicit. Protocol major mismatch refuses dispatch; additive fields are tolerated only where declared.

Executor commits the received command and execution attempt before native launch. It retains host identity, native session/process ownership evidence, command digest, fence and result. A repeated command reads the existing attempt. If launch occurred between durable intent and confirmed native identity, classify it as uncertain and reconcile original process/history evidence; never launch a replacement merely because an acknowledgement is missing. Persisted intent alone cannot establish whether an external effect occurred.

Serialize input/control per native session, not globally. Fence stale attempts and reject late commands targeting a retired execution generation. Do not reassign a disconnected running execution without a resolved ownership outcome. Contact loss, execution health and accepted task completion remain independent facts. Stop has requested, delivered and observed-terminal outcomes; cancellation prevents new steps without claiming rollback or immediate termination.

The executor uploads stable event IDs and monotonically increasing source positions. Coordinator receipt and event insertion commit together before acknowledgement. Duplicates with identical content are harmless; conflicting content is an integrity error. Retain gaps and source occurrence/receipt times; do not fabricate cross-host event order. Executor removes acknowledged spool copies only after durable coordinator retention is confirmed; full spool blocks new launches and reports missing capacity rather than dropping evidence silently.

## Storage, records and tool surface

Propose SQLite WAL, FULL synchronization for acknowledged coordinator/executor writes, one bounded writer, busy timeout and explicit schema migrations. Keep the files local to their owning process. Backups must include a consistent database and referenced blobs; restore needs a focused proof. This follows specq engineering/data.md rather than adopting a prototype database schema.

Conceptual records: projects/source references; prepared configurations/content hashes; tasks; sessions/native links; executions/attempts; requests/commands/dispatch obligations; event receipts/events; questions/answers; results/blob references; working areas; measurements. These do not prescribe one table per record or a fixed broad schema; implement only the records needed by each connected increment in one database and service. Source files remain configuration authority; retain the exact prepared content and separately record what the native harness demonstrably loaded.

Raw native events/history and large prepared inputs live as immutable blobs with metadata references; normalized event rows index them. Each history page has a stable cursor and explicit coverage. Usage measurements carry native source identity, scope, unit and whether cumulative or incremental; aggregate only a known accounting basis. Preserve unknown prices, child coverage and missing measurements. CPU, memory and disk observations include sample time and execution/host scope. Instrument operations and transport from the first increment without requiring another observability service.

Implement Fleet operations once in coordinator application handlers; HTTP and stdio MCP are adapters. Expose the design's context.read, agent.list/prepare, environment.list, execution.start/read/stop, session.send, command.read and result.read. Configuration editing remains authorized native file work. Do not wrap ordinary shell/file/browser tools. Tool schemas return stable IDs, authority failures, pending/unknown outcomes and history cursors rather than success-shaped prose. Scope the MCP credential to its caller/project; an agent-supplied project field cannot grant access.

The stdio adapter calls authenticated coordinator HTTP and contains no scheduler or durable state. It can exit without cancelling accepted work. The executor supplies the chosen harness's supported tool configuration; actual MCP loading remains a native qualification prerequisite, not an assumed universal runtime capability.

## Artifacts and preview lifetime

Upload website sources, browser evidence and selected static build output to coordinator disk before declaring result availability. Stream to bounded temporary files, verify hash/size, fsync and atomically publish, then commit the result reference. A crash before metadata commit leaves a reclaimable orphan, not a visible partial result. Hash match alone does not establish safe executable content.

Serve static Hello World previews from a separate origin without coordinator credentials; authorize access before issuing a scoped preview route. Never serve arbitrary executor paths. Dynamic applications require a separately owned preview process and are outside this initial static delivery proof. Return preview URL, access policy, any configured temporary access lifetime and availability observation. Browser reachability from the operator's actual route is required.

Retain every execution in full without default expiry, including session history, output, actions, tool calls, questions, results and verification materials, as fleet.md requires. Temporary preview access may end and working areas may be released only without deleting retained evidence. Preserve required files and partial work before working-area cleanup; ending preview access does not expire its retained source or verification materials. Set upload, storage and spool capacities through measured deployment configuration, not arbitrary thresholds in this assessment. At capacity, report the constraint and refuse further admission where needed rather than silently dropping evidence. No evidence-expiry or tombstone mechanism is required for the first slice.

## Repository and deployment layout

Keep all Fleet-owned code in this repository:

- `packages/coordinator/`: operations, dispatch, SQLite migrations, history, blob/preview serving and HTTP API.
- `packages/tools/`: thin stdio MCP adapter and schemas; no second state model.
- `crates/executor/`: host journal, command reconciliation, observation spool, working areas and runtime adapter boundary.
- `contracts/`: versioned JSON schemas plus request/receipt/event fixtures shared across languages.
- `tests/scenarios/`: fake executor, crash/reconnect and result-retrieval checks.

First development topology: coordinator and executor on one machine with separate journals and HTTP exchange. Target topology: coordinator/database/blob disk on PDS-18; outbound Mac/Windows executors use the same protocol. No target installation or platform support is established by this review. Preview hosting may share coordinator placement, with separate origin/access enforcement.

## Implementable milestones and checks

1. **Package and contracts.** Add the layout, lockfiles, one fixture per operation and a fake executor. Proposed commands after files exist: `npm ci`, `npm run build`, `cargo build --manifest-path crates/executor/Cargo.toml`. Check both languages parse the same IDs/errors and reject incompatible versions. These commands are not currently established repository commands.
2. **One qualified native path.** Integrate the selected harness behind the Rust boundary, record prepared and actual loaded inputs, identity and available observations. Check one isolated launch/history path after separate native-run authorization. Runtime adapter selection depends on the companion runtime assessment, not on adopting AgentQ by name.
3. **Durable dispatch.** Implement transactions, deduplication and executor receipts. With a fake executor, kill each side around acceptance, receipt and launch confirmation; assert one command identity, retained acknowledged writes and explicit uncertainty, not silent duplicate launch. Test same-key/different-payload refusal and preserved corrupt/newer stores.
4. **Interaction and recovery.** Implement session input, answers, stop, history pagination and reconnect. Check client close/reopen, stale fence, duplicate/reordered events, disconnected host and spool exhaustion. Native stop qualification must observe termination and retain partial history.
5. **Result delivery and first-slice proof.** Upload source/evidence/static preview; check interrupted upload, integrity mismatch, retrieval after producer exit and working-area cleanup, and retained evidence after temporary preview access ends. When authorized, run the Hello World path, inspect actual operator browser access and report usage/resource coverage, failure and controlled-stop evidence against delivery-strategy.md.
6. **Operational closure.** Restore a backup, restart the coordinator during disconnected execution, reconcile and measure representative edit/build time. Record measured limits and platform gaps; broaden deployment only after the evidence supports it.

## Decision blockers and remaining evidence

No conceptual storage or dispatch blocker prevents implementing the fake-executor path. True native proof prerequisites are the selected harness's supported launch/input/stop/tool-loading contract, an authorized test host/account and an operator-reachable preview origin with credentials/access configured. The coordinator can be built while these are resolved; do not claim the integrated slice complete without them.

Unproven: native process reconciliation after the intent/launch crash window, remote offline recovery, Windows cleanup, actual tool visibility, charge deduplication against captured native records, storage capacity sizing and browser access after producer exit. This assessment recommends mechanisms and checks; it does not establish runtime adoption or performance measurements.
