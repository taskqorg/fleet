# Hosts, environments and stores (architect)

**Mockup routes:** `#/hosts`, `#/hosts/limits` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** See host health, worker load and store space, and change host settings.

**Layout (D19).** [App shell](../patterns/app-shell.md), then [list and detail](../patterns/list-detail.md).
* **Control column:** the page head with "updated 40s"; the low-space [problem row](../controls/problem-row.md) with its three actions; the hosts catalogue.
* **Catalogue row:** state, name and platform; on the next lines workers running of slots, workers by harness, queued commands when there are any, and the store fill; last seen on the right.
* **Work area:** the opened host as stacked [detail sections](../controls/detail-section.md) with its [setting fields](../controls/setting-field.md): state, workers, readings (heartbeat age, load of cores, memory and disk free), Fleet processes (CPU per beat, memory), problems, capabilities, store, and Remove (preview counts first, then the danger button).
* **Work area with no host open:** the system ([telemetry §8](../../designs/fleet-telemetry.md#8-system-screen-read-half), [system management](../../designs/fleet-system-management.md#system-screen)): findings of the rules pass (stale marker), services with calls, errors and p95 for the last hour, spans dropped, recent runs with CPU and peak memory, and purge of removed hosts (preview first).

There is no stat strip: it repeated the rows (OR-17).

**Limits (`/hosts/limits`, opened from the page head's Limits link, people access §8 and §12, D24).** The page head has the burn window choice (1h, 3h, 6h; default 3h), the Left and Burn choice, Earlier, Today and Later (one day each; the range is one week back and one week ahead of its centre) and Refresh all. Then one card per harness and period: left of capacity in percent, next reset with its restore in weighted points, first empty, pool empty, burn per hour over the window, members without a reading, and an [area chart](../controls/area-chart.md): the recorded pool line to now and the projected line after it with a step at each member's reset (Left), or burn per hour (Burn). Under the cards one table per subscription: each period (per-model weekly periods are their own columns) shows the left as a [fill bar](../controls/fill-bar.md) with the Low threshold as its mark, the reset countdown, pace (ahead, on, under), a Low mark, the reading age and its source (execution or probe); the row ends with Refresh. No combined score. It replaces the "Subscription limits" panel of the control column; the idle work area shows the system. A refresh answer (sent, or why a subscription was skipped) shows in a line under the head.

Not built in the app, shown in the mockup: slots and the store fill in the row, the low-space problem row, the setting fields. The app's page head shows the offline marker, not "updated 40s".

| Inventory item | Shown by |
|---|---|
| Hosts: platform, last contact, observation state, harness versions, readiness | Catalogue row: name (the platform shows only when the name does not already say it, OR-17), [state icon](../controls/state-badge.md) (unconfirmed overlay when offline), last contact [age chip](../controls/number.md) (attention past 15m) |
| Environments and account users per host | Host detail: stacked [detail sections](../controls/detail-section.md) (C3) |
| Stores: capacity, used, free, tunnel state, fill per project | [Fill bar](../controls/fill-bar.md) per host with project segments, free space and warning mark |
| Store capacity, memory and CPU bounds, free-space warning | Setting fields with applied, pending or apply error |
| Low space: raise capacity, change locations, add retention rule | Problem row actions |
| Workers running per host (owner, D9, D11) | Catalogue row: running/slots plus [harness counts](../controls/harness-counts.md) |
| Host and process readings: load, memory, disk, CPU and memory per Fleet process | Catalogue row: load and memory free; host detail: Readings and Processes sections (`telemetry_summary`) |
| Run resources: CPU seconds and peak memory per execution | Work area with no host open: Recent runs (`telemetry_summary.runs`) |
| Services: calls, errors, latency; findings of the rules pass | Work area with no host open: Services and Findings (`telemetry_summary`, `telemetry_findings`) |
| Remove a host, purge removed hosts | Host detail: Remove; work area: Purge (`hosts_remove`, `system_purge`, preview first) |

**Open.** None from stage 3.
