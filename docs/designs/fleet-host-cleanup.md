# Fleet host cleanup: every run leaves nothing behind, and telemetry names what uses a host

*   **Author:** Alex Kirs
*   **Status:** Accepted (owner decisions 2026-11-25)
*   **Date:** 2026-11-25
*   **Linked design:** [telemetry](fleet-telemetry.md) owns spans, host readings and the rules pass; this document adds the cleanup mechanism, the readings it needs and the `host-saturation` extension. [Executor](../../go/executor/README.md) owns the process journal this builds on.

--------------------------------------------------------------------------------

## Overview

Each Fleet run carries one marker, its execution id, in the environment of every process it starts. It also gets its own temporary directory. When the run ends, and when the executor starts, the executor kills every process that carries the marker of a run that is no longer live, and removes the run's temporary directory. Each cleanup pass records what it removed. Host readings gain swap, compressed memory, `/tmp` use and the top memory consumers, and the existing `host-saturation` rule names those consumers.

Tests use one long-lived PostgreSQL cluster per machine and one template database per schema version, so a test run never creates or leaves a cluster.

## The Problem

The audit of 2026-11-25 found every host's swap full:

*   **owner-mac** (swap 37 of 38 GB): a six-day-old `docker run` from a prototype workspace held 20 GB in the Docker Desktop VM. Twelve test PostgreSQL clusters from test runs were still running (`~/.cache/fleet-trial/pg-*`, `$TMPDIR/fleet-coord-pg-*`). Their orphaned SysV shared memory segments had earlier exhausted the 32-segment limit. Prototype processes six days old were still running, and each Claude Code session kept four `fleet-executor mcp connection` processes (137 in all).
*   **pds18** (swap 8 of 8 GB): `/tmp` is tmpfs and held 9.6 GB of per-task Go and pnpm caches that agents had made (`t-flt-52-gocache`, `fleet-t-flt-54-check`). A `fleet-executor mcp connection` from a release that no longer existed was still running.
*   **windows (WSL)**: 2.5 GB of per-task leftovers in `/tmp`.

Fleet's telemetry said only "owner-mac saturated". `HostLoad` has CPU count, load, free memory and total memory. It has no swap. On macOS, free memory ignores the compressor. On Linux, `MemAvailable` ignores tmpfs. `host-saturation` watches load and free memory only. The coordinator reads `attrs['fleet.orphans']`, but the executor never writes it.

Process ownership has gaps. The executor tracks a run's descendants by parent link, so a double-forked or `setsid` child (`pg_ctl`, `nohup`) reparents to pid 1 and escapes. Files in `/tmp` and SysV shared memory have no owner. `mcp connection` processes have no owner and survive release changes.

## Goals

1.  A process started by a run does not outlive the run, whatever it forks.
2.  A run's temporary files go with the run.
3.  On a host where the executor has its own user, nothing that user leaves in `/tmp` survives a day.
4.  An `mcp connection` process exits when its release is gone or its parent is gone.
5.  Host readings show swap, compressed memory (macOS), `/tmp` tmpfs use and the top five memory consumers, each flagged as Fleet's or not.
6.  `host-saturation` fires on swap pressure too, and its finding names the top consumers.
7.  Each cleanup pass records what it removed (`host.cleanup` span); each execution span carries `fleet.orphans`.
8.  Tests never run `initdb` per run and never leave a cluster behind.

## Non-Goals

*   Killing Docker containers. A container cannot carry the run marker reliably, and the owner's own containers on owner-mac may be needed. Containers are shown in readings through the Docker VM process, not killed.
*   Sweeping `/tmp` on a host where the executor shares the owner's user (owner-mac, and WSL, where the executor runs as the owner's `alexk`). There only the per-run directory is removed.
*   One shared `mcp connection` server per host. That is a separate task.
*   Session directory retention and old release removal. Those leftovers were removed by hand once; retention is a separate task.
*   Moving tests onto one shared database isolated by ids. That rewrite (about 100 Go call sites and 50 TypeScript files that expect an empty database) is a separate task; this design takes the first step.

## Technical Plan

### 1. Run marker

The executor sets `FLEET_EXECUTION_ID=<execution>` in the environment of every harness and script process it starts. Children inherit it, including those that double-fork or call `setsid`.

A cleanup pass lists the processes of the executor's user and reads each one's environment: `/proc/<pid>/environ` on Linux, `sysctl kern.procargs2` on macOS. A process whose marker names an execution that is not live and has no journaled launch is frozen and killed. The pass runs:

*   at the end of each run, after the existing owned-tree cleanup (`live.cleanup`);
*   at executor start, after `reconcileProcesses`.

The count killed for one run goes into that run's execution span as `attrs['fleet.orphans']`, which the coordinator already reads.

### 2. Run temporary directory

Each run gets `TMPDIR=<session dir>/tmp-<execution>`, mode 0700, created before spawn. The executor removes the directory when it releases the run. Codex's workspace-write sandbox already allows `$TMPDIR`.

### 3. `/tmp` sweep on a dedicated user

With `FLEET_TMP_SWEEP=owned` in `executor.env`, the executor removes top-level entries of `/tmp` that its user owns and that were last modified more than `FLEET_TMP_SWEEP_AGE_HOURS` (default 24) ago. It runs this sweep at start and every hour. Only pds18 sets it: there the executor runs as the dedicated user `fleet-agent`, so everything that user owns is Fleet's.

### 4. `mcp connection` lifetime

An `mcp connection` server checks once a minute whether its executable still exists, and whether its parent pid has changed since start (its owner exited and it was reparented). If either holds, it exits. A release change removes the old release directory, so old servers end within a minute.

### 5. Readings

`contract.HostLoad` gains:

| Field | Linux | macOS |
|---|---|---|
| `swapUsed`, `swapTotal` | `/proc/meminfo` `SwapTotal - SwapFree` | `sysctl vm.swapusage` |
| `memCompressed` | absent | `vm_stat` "Pages occupied by compressor" |
| `tmpUsed`, `tmpTotal` | `statfs /tmp` when it is tmpfs | absent |
| `top` | five processes by resident plus swapped memory (`/proc/<pid>/status` `VmSwap`) | five by resident memory |

Each `top` entry is `{pid, user, command, memBytes, ageSeconds, fleet}`. `command` is the program base name only: no arguments or paths, under the telemetry no-content rule. `fleet` is true when the process carries a run marker or is a Fleet program from `fleetProcesses`. On macOS the Docker Desktop VM shows as its own process with its memory.

The poll's `load` and the `host.heartbeat` span carry the same readings. The heartbeat keeps them in `fleet.swap_used`, `fleet.swap_total`, `fleet.mem_compressed`, `fleet.tmp_used` and `fleet.top` (a short `command:memMB` list).

### 6. `host-saturation`

The existing rule, and `hostOverloadReason` in dispatch placement, also fire when swap use is above `ruleSwapUsedShare` (default 0.5) of swap total and free memory is below `ruleMemFreeShare`. The finding's subject stays the host. Its text names the top three consumers, for example `owner-mac: swap 37/38 GB; top: com.apple.Virtualization.VirtualMachine 20 GB, postgres 2 GB (fleet), ...`. No new rule is added.

### 7. Cleanup ledger

Each pass (run end, start, hourly sweep) that removed something writes one `check` span `host.cleanup` with `fleet.cleanup.trigger` (`run-end`, `start`, `sweep`), `fleet.cleanup.processes`, `fleet.cleanup.dirs` and `fleet.cleanup.bytes`. A pass that removed nothing writes no span.

### 8. Test PostgreSQL

*   **One cluster per machine.** Go tests and `tests/harness.ts` use one data directory, `~/.cache/fleet-test/pg` (`FLEET_TEST_PGDATA`), started on first use and left running. `initdb` runs only when the directory is empty. Go tests stop making `$TMPDIR/fleet-coord-pg-*` clusters. `tests/go/run.sh` slots stop making clusters of their own: each slot uses its own database on the one cluster.
*   **One template per schema version.** The first test that needs a database migrates `fleet_tmpl_<hash of schema.sql>` once. Each Go test gets `CREATE DATABASE ... TEMPLATE` (a file copy taking milliseconds) and drops it at test end. At start a test process drops test databases older than a day, so one killed run does not leave databases behind.
*   **Later:** tests isolated by their own ids in one shared database, in a separate task.

## Alternatives Considered

*   **Kill by process group or cgroup only.** `setsid` leaves the group. Cgroups exist on Linux only. The environment marker works on both platforms and survives every fork.
*   **A new rule for swap.** Rejected: one host problem is one finding. `host-saturation` already owns host pressure.
*   **Kill containers started during a run.** Rejected: no reliable link from a container to a run on a shared Docker host.

## Done when

*   A run that starts `nohup sleep 1000 &` and `setsid sleep 1000` leaves no `sleep` after it ends (executor test).
*   A cleanup pass on the three hosts finds no Fleet leftovers.
*   `hosts_list` on prod shows swap and top consumers for every host.
