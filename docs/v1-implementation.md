# V1 implementation decisions

Working technical choices for the [first working slice](delivery-strategy.md#first-working-slice), consolidated from the [runtime](v1-runtime-assessment.md) and [coordinator](v1-coordinator-assessment.md) assessments. These select the implementation starting point, not proven runtime behavior. Product requirements remain in Fleet's canonical specification.

## Selected starting point

| Area | Choice | Reason and limit |
|---|---|---|
| Product core | New Fleet coordinator and executor, in this repository. | Preserve Fleet's state ownership and configurable agents without importing board or fixed-role policy. |
| Coordinator | TypeScript on supported Node.js LTS; Fastify HTTP boundary. | Separate frequently edited application logic from compiled host control. Development speed remains to be measured. |
| Executor | Rust with one internal native-adapter boundary. | Host processes and local journals have a separate build/deployment cycle. No prototype crate is automatically adopted. |
| Persistence | Local SQLite per coordinator/executor, WAL and durable acknowledged writes; retained files outside the database. | One coordinator authority, outbound executor exchange. No shared SQLite file across hosts. |
| Host communication | Authenticated HTTPS JSON, outbound executor polling, stable command/event identities. | No inbound executor port or separate broker in the first slice. Measure interaction latency before choosing polling intervals or streaming extensions. |
| Agent interface | Thin stdio MCP adapter calling coordinator HTTP operations. | Agent and future web clients share application handlers. Native harness tool loading must still be qualified. |
| Results | Retained files with metadata and integrity checks; isolated static website preview. | First proof is a static page. Dynamic previews and full web panels come later. |

Configuration remains repository content. Launch records actual resolved inputs; it does not freeze future configuration edits. History and results have no default expiry. Temporary workspace cleanup and preview access lifetime do not delete retained evidence.

The SQLite choice requires local database files and a controlled write path; [SQLite's WAL documentation](https://sqlite.org/wal.html) explains its same-host constraint. The agent adapter follows the [MCP stdio transport](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports). Use the [Fastify documentation](https://fastify.dev/docs/latest/) when pinning and implementing its API boundary. Pin concrete compatible versions and the SQLite binding with the first build; none is qualified by this document.

## Prototype code policy

Implement Fleet configuration resolution, work state, dispatch journal and resource attribution against Fleet requirements. Prototype schemas and application services are references, not prerequisites.

Prefer bounded extraction of useful native launch/control/history and process-identity mechanisms only after tracing the complete dependency closure for the first harness. Compare that concrete cost with wrapping the existing runtime and with a new implementation. The current review does not authorize copying a module by filename or select an AgentQ dependency.

Keep native-specific behavior inside one adapter. Record origin revisions, retained behavior and relevant checks for extracted code. Preserve uncertainty handling and ownership checks; do not transfer unrelated onboarding, automatic repairs, board integration or fixed limits.

## First implementation batch

Build the smallest coordinator/executor path with a fake native adapter first. This establishes coordination behavior while the native dependency assessment is completed independently.

1. Add coordinator, Rust executor, shared protocol fixtures and a scenario test harness. Implement only start, command receipt and execution/history read needed by this path.
2. Persist a request and dispatch obligation before acknowledging it. Repeated requests return the same execution; changed payload under the same identity returns a conflict.
3. Have the executor receive and journal a command, produce ordered fake events and return the result. Retries preserve command identity and do not duplicate execution or events.
4. Close/reopen the client and restart each process at relevant journal boundaries. Retain accepted records, reconcile known outcomes and expose uncertain ones without silently relaunching.
5. Record preparation/dispatch/execution timings, command counts and failures from the first path. Do not fabricate native token or cost observations for the fake adapter.

The batch is complete when a reproducible local scenario and focused failure checks prove this path. It is not the Hello World PoC, native qualification or distributed-platform support.

## Subsequent connected batches

| Batch | Deliverable | Proof |
|---|---|---|
| Native preparation and execution | Choose one harness after inspecting instruction/skill/tool mapping; connect its adapter. | Actual loaded configuration, native identity, history and one granted tool invocation. |
| Interaction and control | Messages, linked answers and owned stop through the same operations. | Delivery state, observed termination, partial work retained and unrelated processes preserved. |
| Website and preview | Execute the Hello World assignment, retain source/evidence and expose preview. | Actual browser checks and operator access after producer exit. |
| Operational completion | Reconcile restart/disconnect, verify retention/cleanup and resource coverage. | Accepted slice criteria, measured gaps and prioritized second iteration. |

Keep deployment and real-account prerequisites explicit. No existing production workload is migrated by these choices. Client-independent execution, native recovery and Mac/Windows/Linux coverage are separate claims requiring evidence.

## Remaining bounded decisions

- First native harness and the exact extraction/wrapper/new-code choice, based on dependency closure and supported preparation.
- Concrete package versions, SQLite bindings and protocol schemas in the first implementation batch.
- Native test account/host and operator-accessible preview route before the integrated run.

These do not require reopening Fleet's principal architecture. Revise a technical choice when implementation evidence contradicts its assumptions, recording the reason in this document.
