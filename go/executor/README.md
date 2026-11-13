# Executor

## Overview

`executor` is the host-side Fleet binary. It polls and journals coordinator commands, starts and reconciles native harness processes, exposes scoped MCP services, manages project worktrees and artifacts, and reports receipts, events, readings, and spans.

## Interface

- The executable dispatches `mcp`, `qualify-mcp`, `mcp-guardian`, `reproject`, and `selftest`; otherwise it runs the host executor loop or an owned MCP dependency command.
- Command handling covers native harness sessions, scripts, project setup/teardown, account inspection and limit probes, artifact transfer, and host readiness.
- `resolveAttemptContext` verifies and stages declared immutable instruction sources from local Git objects; `prepareInstructions` gives the resolved payload to the selected harness.
- The executor records command and process state in its SQLite journal and communicates with the coordinator through typed `contract` requests and events.

## Invariants

- Host secrets, account registries, credential homes, workspaces, Git metadata, and staged instruction sources are ownership-, path-, and symlink-checked before use ([executor.go: `hostSecret`](executor.go), [accounts.go: `runnable`](accounts.go), [attempt_context.go: `resolveAttemptContext`](attempt_context.go)).
- A launch is journaled before its process exists; owned process identities are journaled before reconciliation. After a restart, surviving recorded processes are fenced and cleaned up rather than reattached ([executor.go: `launch`, `own`, `reconcileProcesses`](executor.go)).
- A process or provider action with uncertain external outcome stays `unknown`; it is not silently retried as a known failure ([executor.go: `reconcile`](executor.go), [mcp_connection.go: `connectionCall`](mcp_connection.go)).
- Source resolution does not fetch, read working-tree rules, or execute Git hooks or filters. It requires the declared commit, exact blobs, digests, origin identity, and allowed owned storage ([attempt_context.go: `resolveAttemptContext`](attempt_context.go)).
- Worktree cleanup keeps work when configured reasons such as uncommitted changes, unpushed commits, or teardown failure apply ([worktree.go: `teardown`](worktree.go)).

## Side effects

- Starts and stops native processes, creates and removes worktrees and MCP-owner directories, writes session files and a SQLite journal, and uploads artifacts ([executor.go: `run`, `release`](executor.go), [worktree.go: `teardown`](worktree.go), [upload.go: `sendParts`, `spawnTransfers`](upload.go)).
- Runs approved local Git, harness, project-script, browser, and account-limit commands; it queues coordinator receipts and events ([executor.go: `event`, `receipt`](executor.go), [accounts.go: `readLimits`](accounts.go)).
- Readiness and account observation read host configuration and native CLI state without sending tokens or credential contents ([readiness.go: `readiness`](readiness.go), [accounts.go: `accountInspect`](accounts.go)).

## Verification

Run from `go/`:

```sh
go test ./executor
CGO_ENABLED=0 go build -o bin/fleet-executor ./executor
```
