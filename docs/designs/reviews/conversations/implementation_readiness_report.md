# Stage 9: Implementation Readiness Test

Target: `docs/designs/fleet-conversations.md`, Detailed Implementation Plan. Reviewers: `egm-implementation-readiness`, `egm-codebase-details`, `egm-style` (implementation-readiness standard). Date: 2026-11-10. The plan section was rewritten to resolve the findings below.

| # | Severity | Source | Finding | Resolution |
|---|---|---|---|---|
| R1 | Blocker | readiness | Other readers of `fleetNormalized.messages` (`delegation.ts`, `agent-tools.ts`, `delivery.ts`, tests, fixture) break when it is removed. | Fixed: shared `replyText()` in `threads.ts`; readers, tests and fixture listed (items 8, 14, 21). |
| R2 | Blocker | codebase | `DecisionBlock` is in `controls.tsx:239`, not `patterns.tsx`. | Fixed: merged into item 19 (`QuestionRow`). |
| R3 | Blocker | codebase | Storing deltas as `event-delta` breaks `ingest` replay and collision checks, which read only `event`. | Fixed: lookups read both kinds; tests added. |
| R4 | Blocker | codebase | `main()` parses no arguments and takes the lock and journal first. | Fixed: `reproject` checked first and runs before any env read, lock or journal open. |
| R5 | Major | readiness | No operation to fetch fed rows. | Fixed: `thread_blocks({ key, seqs })`. |
| R6 | Major | readiness, codebase | `feedCursor` computation and snapshot primitive unnamed; export streaming vs snapshot. | Fixed: `store.read` snapshot (read operations) and `store.changes.since(null).cursor`; export streams page by page, each in its own read. |
| R7 | Major | readiness | Block-key index for coordinator-made blocks and `execution.ended` collision. | Fixed: index 0; `execution.ended` has empty `blocks`. |
| R8 | Major | readiness | No SQL for membership. | Fixed: CTE with indexes named. |
| R9 | Major | readiness, codebase | No per-harness mapping; `deltaOf` does not exist (today `"delta": true`). | Fixed: mapping table (to confirm against fixtures); `deltaOf` defined and replaces `delta`. |
| R10 | Major | readiness | `execution.ended` function and repeat handling. | Fixed: `transition()`; fixed id `<executionId>:ended`. |
| R11 | Major | readiness | Reproject inputs vague. | Fixed: `data.data`, `data.executionId`, missing harness case. |
| R12 | Major | readiness, codebase | Missing rows, orphaned `transcript.test.ts`, missing unit tests. | Fixed: `QuestionRow`, `ResultRow`; test deleted; tests for window state, selection, format. |
| R13 | Major | readiness | Result block delivery depends on Stage 4. | Fixed: result blocks carry no delivery chip until Stage 4. |
| R14 | Major | codebase | Wrong cargo package name. | Fixed: `fleet-executor`. |
| R15 | Major | codebase, style | `answer` line refs and the no-row path, hook side effects. | Fixed: line 77/84/85; conditions stated. |
| R16 | Major | codebase | Script and job naming. | Fixed: `check:conversations` with build; job `conversations-check`. |
| R17 | Major | style | "entry" vs "block"; unclear "its"; `execution.harness` reads as an event field. | Fixed. |
| R18 | Nit | codebase | Artifacts are reached through executions, not sessions. | Fixed in item 4 and the Technical Plan. |
| R19 | Nit | codebase | Fixture files not named; "the book" not a path. | Fixed. |
| R20 | Nit | readiness | `VirtualList` signatures; CI job; URL format and redirect. | Fixed; `scrollToKey` reuses `scrollToIndex`, prepend reuses the `anchor` ref. |
| R21 | Nit | style | Stage 4 parallel wording; `messages` gate; `--check` writes nothing; cross-references. | Fixed. |

Re-audit: not run (cap of one; findings resolved directly).
