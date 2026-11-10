# Consolidated Review: Fleet Conversations

Target: [fleet-conversations.md](../../fleet-conversations.md). Brief: [conversations-brief.md](../../conversations-brief.md). Mode: uninterrupted full flow (Stages 5-9), run by a dispatched worker; owner checkpoints were replaced by drafter defaults, listed below. Date: 2026-11-10.

Stage reports: [zoom_out_report.md](zoom_out_report.md), [comprehension_report.md](comprehension_report.md), [critic_report.md](critic_report.md), [intermediate_zoom_out_report.md](intermediate_zoom_out_report.md), [implementation_readiness_report.md](implementation_readiness_report.md).

## Totals

| Stage | Blocker | Major | Nit | Open after fixes |
|---|---|---|---|---|
| 5 Zoom Out + Inventive | 2 | 8 | 6 | 0 (Z7 kept as default) |
| 6 Comprehension | 0 | 6 | 6 | 0 |
| 7 Critic | 2 | 11 | 4 | 0 |
| 8 Intermediate Zoom Out | 0 | 3 | 5 | 0 |
| 9 Implementation Readiness | 4 | 13 | 4 | 0 |

## Changes that moved the design most

1. **Reuse over new parts** (Z1, Z3-Z6): artifact bytes from the existing `/api/results/:id` with a tighter CSP; `effort` from `config_ref`; idempotent answers from `tasks.answer`; delivery stages from `registry.ts`; `ResultViewer` extended. Removed: a new route, a column, a `requestId`, a second stage enum, a new viewer.
2. **Ordering without a new column** (I1, K1): the shared `seq` plus a snapshot read that returns the change-feed cursor, copied from the operator app. Late rows are placed by key under anchor keeping.
3. **Block key `(seq, index)`** (K2): one event can hold several blocks.
4. **Streams** (I2, K5, Y1): deltas stored as `event-delta`; the executor is the only producer of text blocks; `execution.ended` places result blocks.
5. **No invented limits** (Z2, K14, Y3): cost chip neutral; text read in `Range` pages; `limit` without a maximum.
6. **Brief reconciliation**: the brief's claim that artifacts have two sources was right; the draft first said otherwise, and now reads both through `artifacts.list`.

## Brief reconciliation (Step 6.1)

Contradictions with the brief: none remain. Additions that change interfaces:

*   New operations `thread_entries`, `thread_blocks`, `thread_search`, `thread_export` and their routes; `/api/sessions/:id/events` removed; `session_get` drops `events` and `results`.
*   New document kind `event-delta` and the `execution.ended` event.
*   Executor `reproject` subcommand and `pnpm run reproject`.
*   The brief's "effort has no column" is resolved by reading `execution.config_ref` instead of adding one.

## Drafter defaults that need the owner

| # | Question | Default |
|---|---|---|
| D1 | Must old history survive the block change? | Overturned by the owner (2026-11-10): no; old records are not re-projected. |
| D2 | Session with executions for two tasks: where does its early taskless history go? | The first task it got. |
| D3 | Steer while a question is open. | Steer goes to the execution; it does not answer. The open card stays above the composer. |
| D4 | Model label source. | Formatter in `format.ts` from the stored model id; no registry. |
| D5 | Cost chip attention colour. | None until Fleet has a cost rule. |
| D6 | Cut-off replies (stream ended without a final block) are not found by search. | Accepted trade-off. |
