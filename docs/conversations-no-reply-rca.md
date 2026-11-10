# Conversations: messages got no visible reply (2026-11-10)

> Historical record. It was written against the TypeScript coordinator and the Rust and Python executor, which the Go port deleted; file names in it are not current code. Current code: `go/coordinator`, `go/executor`, `go/gateway` ([stack](designs/fleet-stack.md)).

The owner sent messages in Conversations on fleet.ufobe.com and saw no answer. Two links were broken.

## 1. The app never rendered replies (all sessions)

Send, queueing, host pickup and the harness all worked. Example: message `5bda3290` ("hi") in session `c2c11096` ran as execution `6a55ab8b` on owner-mac (codex, succeeded 21:53:41 UTC); its event 11 holds the reply "Hi! What would you like to work on?". Session `efcca254` (claude) has replies such as "Shalom! How can I help?" in execution `127d6fd7`.

`entries()` in the Conversations view read `ev.item` at the top level of an event. The executor stores the native record under `ev.data` (`data.item`, `data.fleetNormalized.messages`), so no reply ever matched; Claude replies (`type: assistant`) would not have matched even with the right path. The UI fixture in `tests/ui/app.check.ts` used the wrong shape, so the check passed.

Fix: `app/src/transcript.ts` takes reply text from `data.fleetNormalized.messages` (harness-neutral, the same source `agent-tools.ts` and `delegation.ts` use) and tool calls from `data.item`. Test: `tests/ui/transcript.test.ts`.

## 2. A message queued forever behind a lost receipt

Queued message `f9d05aaf` ("hi", 21:53:15) in PM session `05e696a1` waits behind execution `d89cc199`, which is `running` in the coordinator although its process closed at 16:36:07 (`process.closed`, exit 0). At 17:26 the coordinator answered owner-mac's outbox with `401` for about 20 seconds; the outbox treated 401 as a bad row and set 19 receipts and 37 event rows aside (`outbox_failed`, status 401), so `d89cc199` never got its terminal receipt.

Fix: `crates/executor/src/outbox.rs` classifies 401 as an edge condition (row stays queued, backoff, host problem reported), and on start moves earlier 401 rows from `outbox_failed` back to the queue.

Not in scope: tool run `6b00f0fb` (program-worker, `preparing` on pds18) is unrelated to Conversations.
