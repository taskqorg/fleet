# supervise

## Overview

The one way the executor starts a child process ([Fleet standard](../../../docs/designs/fleet-standard.md) S7): process group, deadline, kill and reap.

## Interfaces and boundaries

- `Spawn(Spec{Cmd, SameGroup, Deadline, KillLeftovers, Started})` starts `Cmd` (configured by the caller) and reaps it on an `errtrack.Go` goroutine. The child gets its own process group unless `SameGroup` (a guardian's child, which the guardian's own group kill ends).
- `Proc.Kill`, `Signal`, `Wait`, `Done`, `TimedOut`; `Cmd.ProcessState` is set once `Done` closes.
- `WaitExited(pid)`: waitid with `WNOWAIT` (Linux; macOS through purego).

## Invariants

- A kill reaches the child's group only while its exited leader is not reaped: the reaper first waits for the exit without reaping, then marks the child reaped, so no kill can reach a reused group id. Without waitid kills reach only the leader.
- `Deadline` kills the group once it passes unless the leader exited first; only then `TimedOut` is true.
- `Started(pid)` runs after the start and before the reaper begins, so a child that exits at once is still unreaped (its identity readable).
- `KillLeftovers` kills what the leader left in its group at its exit, before the reap.

## Side Effects

- Starts, signals and reaps child processes.

## Verification

```sh
go build ./executor/...
```
