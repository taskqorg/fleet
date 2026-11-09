# Hosts, environments and stores (architect)

**Mockup routes:** `#/hosts` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** See host health, worker load and store space, and change host settings.

**Layout.** [App shell](../patterns/app-shell.md). The page head shows "updated 40s". Then:
* a [stat strip](../controls/stat-strip.md) with workers per host;
* the low-space [problem row](../controls/problem-row.md) with its three actions;
* the hosts and stores table;
* the selected host's [setting fields](../controls/setting-field.md).

| Inventory item | Shown by |
|---|---|
| Hosts: platform, last contact, observation state, harness versions, readiness | Table: name (the platform shows only when the name does not already say it, OR-17), [state icon](../controls/state-badge.md) (unconfirmed overlay when offline), last contact [age chip](../controls/number.md) (attention past 15m) |
| Environments and account users per host | Host detail: stacked [detail sections](../controls/detail-section.md) (C3) |
| Stores: capacity, used, free, tunnel state, fill per project | [Fill bar](../controls/fill-bar.md) per host with project segments, free space and warning mark |
| Store capacity, memory and CPU bounds, free-space warning | Setting fields with applied, pending or apply error |
| Low space: raise capacity, change locations, add retention rule | Problem row actions |
| Workers running per host (owner, D9, D11) | Stat strip and the Workers column: running/slots plus [harness counts](../controls/harness-counts.md) |

**Open.** None from stage 3.
