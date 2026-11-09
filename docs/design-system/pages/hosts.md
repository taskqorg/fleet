# Hosts, environments and stores (architect)

**Purpose.** See host health, worker load and store space, and change host settings.

**Layout.** [App shell](../patterns/app-shell.md). The page head shows "updated 40s". Then:
* a [stat strip](../controls/stat-strip.md) with workers per host;
* the low-space [problem row](../controls/problem-row.md) with its three actions;
* the hosts and stores table;
* the selected host's [setting fields](../controls/setting-field.md).

| Inventory item | Shown by |
|---|---|
| Hosts: platform, last contact, observation state, harness versions, readiness | Table: name, platform, state icon (unconfirmed overlay when offline), last contact age chip (attention past 15m) |
| Environments and account users per host | Not in the mockup; planned as a host detail tab |
| Stores: capacity, used, free, tunnel state, fill per project | Fill bar per host with project segments, free space and warning mark |
| Store capacity, memory and CPU bounds, free-space warning | Setting fields with applied, pending or apply error |
| Low space: raise capacity, change locations, add retention rule | Problem row actions |
| Workers running per host (owner, D9) | Stat strip and the Workers column (running/slots, queue) |

**Open.** The host detail tabs (environments, account users, harness versions) and the tunnel state column.
