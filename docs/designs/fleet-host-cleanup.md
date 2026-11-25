# Fleet host cleanup: every run leaves nothing behind, and telemetry names what uses a host

*   **Author:** Alex Kirs
*   **Status:** Accepted (owner decisions 2026-11-25; retention, section 9, the same day)
*   **Date:** 2026-11-25
*   **Linked design:** [telemetry](fleet-telemetry.md) owns spans, host readings and the rules pass; this document adds the cleanup mechanism, the readings it needs and the `host-saturation` extension. [Executor](../../go/executor/README.md) owns the process journal this builds on.

--------------------------------------------------------------------------------

## Overview

Each Fleet run carries one marker, its execution id, in the environment of every process it starts. It also gets its own temporary directory. When the run ends, and when the executor starts, the executor kills every process that carries the marker of a run that is no longer live, and removes the run's temporary directory. Every hour it removes session directories nothing needs any more; `fleet-update` keeps only the releases rollback needs. Each cleanup pass records what it removed. Host readings gain swap, compressed memory, `/tmp` use and the top memory consumers, and the existing `host-saturation` rule names those consumers.

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
9.  A host keeps only the releases rollback needs, and a session directory goes once it holds no worktree and nothing uses it.

## Non-Goals

*   Killing Docker containers. A container cannot carry the run marker reliably, and the owner's own containers on owner-mac may be needed. Containers are shown in readings through the Docker VM process, not killed.
*   Sweeping `/tmp` on a host where the executor shares the owner's user (owner-mac, and WSL, where the executor runs as the owner's `alexk`). There only the per-run directory is removed.
*   One shared `mcp connection` server per host. That is a separate task.
*   Moving tests onto one shared database isolated by ids. That rewrite (about 100 Go call sites and 50 TypeScript files that expect an empty database) is a separate task; this design takes the first step.

## Technical Plan

### 1. Run marker

The executor sets `FLEET_EXECUTION_ID=<execution>` and `FLEET_EXECUTION_OWNER=<its data directory>` in the environment of every harness and script process it starts. The owner part keeps one executor (a test executor inside a run, say) from killing another executor's runs. Children inherit it, including those that double-fork or call `setsid`.

A cleanup pass lists the processes of the executor's user and reads each one's environment: `/proc/<pid>/environ` on Linux, `sysctl kern.procargs2` on macOS. A process whose marker names this executor and an execution that is not live and has no journaled launch is frozen and killed. The pass runs:

*   at the end of each run, after the existing owned-tree cleanup (`live.cleanup`);
*   at executor start, after `reconcileProcesses`.

macOS hides the environment of Apple's own programs (`sleep`, `tail`, `perl`) from `kern.procargs2`, so on macOS those are found only through the owned tree and the process group; every other program (Homebrew, Node, Go, PostgreSQL, Docker CLI) carries a readable marker. Linux reads every process of the executor's user.

The count killed for one run goes into that run's execution span as `attrs['fleet.orphans']`, which the coordinator already reads.

### 2. Run temporary directory

Each run gets `TMPDIR=<executor data dir>/run-tmp/<execution>`, mode 0700, created before spawn. The executor removes the directory at run end, and at start removes every such directory of a run that is not unresolved. Codex's workspace-write sandbox already allows `$TMPDIR`.

### 3. `/tmp` sweep on a dedicated user

With `FLEET_TMP_SWEEP=owned` in `executor.env`, the executor removes top-level entries of `/tmp` that its user owns and that were last modified more than `FLEET_TMP_SWEEP_AGE_HOURS` (default 24) ago. It runs this sweep at start and every hour, in the same pass as the session sweep (section 9). Only pds18 sets it: there the executor runs as the dedicated user `fleet-agent`, so everything that user owns is Fleet's.

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

The existing rule also fires when swap use is above `ruleSwapUsedShare` (default 0.8) of swap total. Dispatch placement (`hostOverloadReason`) still refuses a host only on load or low free memory, because macOS keeps swap long after pressure passes; its reason names swap and the top consumers. The finding's subject stays the host. Its text names the top three consumers, for example `owner-mac: swap 37/38 GB; top: com.apple.Virtualization.VirtualMachine 20 GB, postgres 2 GB (fleet), ...`. No new rule is added.

### 7. Cleanup ledger

Each pass (run end, start, hourly sweep of sessions and `/tmp`) that removed something writes one `check` span `host.cleanup` with `fleet.cleanup.trigger` (`run-end`, `start`, `sweep`), `fleet.cleanup.processes`, `fleet.cleanup.dirs` and `fleet.cleanup.bytes`. A pass that removed nothing writes no span.

### 8. Test PostgreSQL

*   **One cluster per machine.** Go tests and `tests/harness.ts` use one data directory, `~/.cache/fleet-test/pg` (`FLEET_TEST_PGDATA`), started on first use and left running. `initdb` runs only when the directory is empty. Go tests stop making `$TMPDIR/fleet-coord-pg-*` clusters. `tests/go/run.sh` slots stop making clusters of their own: each slot uses its own database on the one cluster.
*   **One template per schema version.** The first test that needs a database migrates `fleet_tmpl_<hash of schema.sql>` once. Each Go test gets `CREATE DATABASE ... TEMPLATE` (a file copy taking milliseconds) and drops it at test end. At start a test process drops test databases older than a day, so one killed run does not leave databases behind.
*   **Later:** tests isolated by their own ids in one shared database, in a separate task.

### 9. Retention: releases and session directories

Audit of 2026-11-25: owner-mac `~/releases` 1.3 GB and its sessions root 1399 directories, 4.6 GB; WSL `~/releases` 1.6 GB; PDS18 `/opt/fleet-agent/releases` 1.5 GB.

*   **Releases.** `fleet-update` already prunes each releases root to `current` and `previous` and `harnesses/` to the CLIs those two name ([self-update](fleet-self-update.md)); that is most of each root, and rollback needs it. The leftovers were the `fleet-<sha12>-<os>-<arch>` directories of the layout before self-update (5 to 7 per root, 24 to 47 MB each, about 1 GB on the four roots), which nothing links to. The same prune now removes them.
*   **Session directories.** On owner-mac 1334 of the 1399 held no worktree (3.5 GB: per-session Go caches, raw native logs, inputs); 65 held one (1.3 GB). The hourly pass of section 3 runs on every host (not only with `FLEET_TMP_SWEEP`) and removes a directory under `FLEET_SESSIONS_ROOT` (and the legacy `<data>/sessions`) when all hold:
    *   no `worktree*` entry in it or under `workspaces/*`: a worktree, or a copy set aside by `setAsideStale`, stays until teardown on delivery or archive removes it under `keepWhen` ([system management](fleet-system-management.md));
    *   nothing in it changed within two levels for `FLEET_TMP_SWEEP_AGE_HOURS` (24), and it holds no socket;
    *   the journal holds no launch of that session (asked last, just before removal).

    What goes is rebuilt by the next turn: per-run logs were already sent, inputs are fetched per run, caches refill. Native transcripts live in the native home, outside the session directory. The pass is one more source in the `host.cleanup` span (`trigger: sweep`).

## Alternatives Considered

*   **Kill by process group or cgroup only.** `setsid` leaves the group. Cgroups exist on Linux only. The environment marker works on both platforms and survives every fork.
*   **A new rule for swap.** Rejected: one host problem is one finding. `host-saturation` already owns host pressure.
*   **Remove a session directory at teardown.** Rejected: a teardown comes only on delivery or archive, and most sessions without a worktree get neither, so their directories would never go, and a second removal path would be needed for them. The sweep covers both, and after teardown removes a worktree the next pass takes the directory.
*   **Keep N releases or N days of sessions.** Rejected: a count or a horizon is an invented limit; rollback needs `previous`, and a session directory is needed while it has a worktree or a run.
*   **Kill containers started during a run.** Rejected: no reliable link from a container to a run on a shared Docker host.

## Done when

*   A run that starts `nohup sleep 1000 &` and `setsid sleep 1000` leaves no `sleep` after it ends (executor test).
*   A cleanup pass on the three hosts finds no Fleet leftovers.
*   `hosts_list` on prod shows swap and top consumers for every host.
*   After a release with section 9, each releases root holds only `current`, `previous` and `harnesses/`, and each sessions root holds only directories with a worktree or changed within a day.
