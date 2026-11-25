# Hosts, environments and stores (architect)

**Target home.** [System](system.md) › Hosts (`/system/hosts/<name>`) and › Accounts & limits (`/system/accounts`); the live picture of hosts is [Live](live.md). `/hosts` and `/hosts/limits` redirect. The "updated 40s" page head is dropped there ([system](system.md#review-of-the-current-build)).

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
| Poll failure: the coordinator answers the host's polls with an error, so it gets no commands and no update target while its last contact still moves (`pollError` of `hosts_list`) | Catalogue row: state icon Polls failing (danger), before online and offline. Host detail, State: a failure [problem row](../controls/problem-row.md) with the reason code, the status, the first failure as an age chip and the message. After 2 minutes Needs you has the problem too (host id and reason code); both go with the next successful poll |
| Managed CLI and host tool versions | Host detail, Agent CLIs and Host tools (frame 2A): one wrapping line each of `name version`; a version off its pin in attention with `pinned <version>` beside it; `—` when observation is unavailable or stale. Host tools are the versions the executor reports for node, pnpm, gh and glab (the pinned executables while tool pins are on, else those on its PATH) |
| What each update did on this host | Host detail, Updates ([canvas](../board/screens/host-updates.bl), frame 2A; `host_update_history`): one [live row](../controls/live-row.md) per change of the reported update result, newest first: state icon, release, the components it brought (`claude 2.1.331`) or "no component change", the state word and reason (installing, refused, rolled back), time on the right. An install that ended shows only its end; consecutive switches without a component change fold into `×N releases, no component change · <oldest> to <newest>`. Older entries load on scroll, one request per page (OR-65) |
| Environments and account users per host | Host detail: stacked [detail sections](../controls/detail-section.md) (C3) |
| Stores: capacity, used, free, tunnel state, fill per project | [Fill bar](../controls/fill-bar.md) per host with project segments, free space and warning mark |
| Store capacity, memory and CPU bounds, free-space warning | Setting fields with applied, pending or apply error |
| Low space: raise capacity, change locations, add retention rule | Problem row actions |
| Workers running per host (owner, D9, D11) | Catalogue row: running/slots plus [harness counts](../controls/harness-counts.md) |
| Host and process readings (Daemon health): load, memory, disk, CPU and memory per Fleet process | Strictly separated from active execution consumption: catalogue row shows host load and memory free; host detail separates Daemon Readings and Processes sections (`telemetry_summary`) from Active Executions (linking to `/runners` and `/tasks`) |
| Run resources (Active Executions): CPU seconds and peak memory per execution | Work area with no host open: dedicated Executions / Recent runs section (`telemetry_summary.runs`) kept strictly separate from host daemon health; links into Tasks and Runners |
| Services: calls, errors, latency; findings of the rules pass | Work area with no host open: Services and Findings (`telemetry_summary`, `telemetry_findings`) |
| Remove a host, purge removed hosts | Host detail: Remove; work area: Purge (`hosts_remove`, `system_purge`, preview first) |

**Open.** None from stage 3.
