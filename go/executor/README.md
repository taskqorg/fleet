# Executor

## Overview

`executor` is the host-side Fleet binary. It polls and journals coordinator commands, starts and reconciles native harness processes, exposes scoped MCP services, manages project worktrees and artifacts, and reports receipts, events, readings, and spans.

## Interface

- The executable dispatches `mcp`, `qualify-mcp`, `mcp-guardian`, `reproject`, and `selftest`; otherwise it runs the host executor loop or an owned MCP dependency command.
- Command handling covers native harness sessions, scripts, project setup/teardown, account inspection and limit probes, artifact transfer, and host readiness.
- `resolveAttemptContext` verifies and stages declared immutable instruction sources from local Git objects; `prepareInstructions` gives the resolved payload to the selected harness.
- The executor records command and process state in its SQLite journal and communicates with the coordinator through typed `contract` requests and events.

## Invariants

- Command trace context and run attribution are optional for mixed-version hosts. [`spans.go`](spans.go) restores the journaled command parent for a distinct terminal execution span; absent or invalid context starts a root with `missing.parent`. Harness/provider span ingestion remains a separate capability.

- Host secrets, account registries, credential homes, workspaces, Git metadata, and staged instruction sources are ownership-, path-, and symlink-checked before use ([executor.go: `hostSecret`](executor.go), [accounts.go: `runnable`](accounts.go), [attempt_context.go: `resolveAttemptContext`](attempt_context.go)).
- A launch is journaled before its process exists; owned process identities are journaled before reconciliation. After a restart, surviving recorded processes are fenced and cleaned up rather than reattached ([executor.go: `launch`, `own`, `reconcileProcesses`](executor.go)).
- A held coordinator poll (up to 25 s) does not pause the 100 ms process loop for live executions: exits, output, script deadlines and samples are handled while it waits. A process-loop failure cancels the poll and stops the executor ([executor.go: `heldPoll`](executor.go)).
- Outbox rows of one execution are one lane, delivered in rowid order (a row without an execution is in its route's lane); lanes are delivered concurrently, so an agent tool, delegation or project request waits only behind its own execution's rows, and each execution's consecutive queued event rows go in one events request (up to 500 events; row by row after a timeout or a rejected batch). Rows without an execution are not ordered against execution rows. A scan every 100 ms starts lanes that are not already delivering; answers to requests sent before a route's edge state last changed do not change it again ([outbox.go: `startFlush`, `observeSent`](outbox.go)).
- Outbox rows are delivered with the held-poll deadline (`deliveryClient`, 35 s), not the 5 s heartbeat deadline: a coordinator ingest up to that bound is not cancelled and resent whole while the terminal receipt waits behind it. Beyond the bound the row is still cut and resent ([outbox.go: `flush`](outbox.go)).
- A 2xx answer whose body is cut while it is read (a deadline, or a heartbeat poll cancelled when its exchange ends) is a transport retry; other statuses keep their class. A cancelled heartbeat poll writes no update signal. Only a complete 2xx answer that is not JSON is an edge condition; its log line names the status, content type, length and first 120 bytes ([outbox.go: `classify`](outbox.go)).
- A process or provider action with uncertain external outcome stays `unknown`; it is not silently retried as a known failure ([executor.go: `reconcile`](executor.go), [mcp_connection.go: `connectionCall`](mcp_connection.go)).
- Source resolution does not fetch, read working-tree rules, or execute Git hooks or filters. It requires the declared commit, exact blobs, digests, origin identity, and allowed owned storage ([attempt_context.go: `resolveAttemptContext`](attempt_context.go)).
- Worktree cleanup keeps work when configured reasons such as uncommitted changes, unpushed commits, or teardown failure apply ([worktree.go: `teardown`](worktree.go)).
- A start with `branch: false` gets a worktree detached at its base, with no session branch ([worktree.go: `worktree`](worktree.go)); a start with `readOnly: true` runs codex in its read-only sandbox, claude with Read, Glob and Grep only, and ACP agents in plan mode; the executor reports `start-access-v1`, so the coordinator places such starts only here ([harness_codex.go](harness_codex.go), [harness_command.go: `readOnlyTools`](harness_command.go), [harness_acp.go](harness_acp.go)).

- Update drain retains the activity lease for live ownership, unfinished commands, queued transport, retryable archived status 401 and active transfers; nonretryable archived rejection history remains preserved without blocking idle replacement ([update_drain.go](update_drain.go), [outbox.go](outbox.go)).

## Side effects

- Starts and stops native processes, creates and removes worktrees and MCP-owner directories, writes session files and a SQLite journal, and uploads artifacts ([executor.go: `run`, `release`](executor.go), [worktree.go: `teardown`](worktree.go), [upload.go: `sendParts`, `spawnTransfers`](upload.go)).
- Samples owned process trees on a 100 ms loop via `observe()`, records harness tool/turn metrics, and emits `ExecutionResourceSummary` on `process.closed` without periodic `resource.observation` events ([executor.go: `tickOne`, `buildExecutionResourceSummary`](executor.go), [harness_blocks.go: `runMetrics`](harness_blocks.go)).
- Runs approved local Git, harness, project-script, browser, and account-limit commands; it queues coordinator receipts and events ([executor.go: `event`, `receipt`](executor.go), [accounts.go: `readLimits`](accounts.go)).
- Readiness and account observation read host configuration and native CLI state without sending tokens or credential contents ([readiness.go: `readiness`](readiness.go), [accounts.go: `accountInspect`](accounts.go)).

## Verification

Run from `go/`:

```sh
go test ./executor
CGO_ENABLED=0 go build -o bin/fleet-executor ./executor
```
