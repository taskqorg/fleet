# Hosts, environments and stores (architect)

**Mockup routes:** `#/hosts` in [mockups.html](mockups.html). Every page renders [app-shell](../patterns/app-shell.md) with the [nav rail](../controls/nav-rail.md).

**Purpose.** See host health, worker load and store space, and change host settings.

**Layout (D19).** [App shell](../patterns/app-shell.md), then [list and detail](../patterns/list-detail.md).
* **Control column:** the page head with "updated 40s"; the low-space [problem row](../controls/problem-row.md) with its three actions; the hosts catalogue; in the app, the subscription limits below it.
* **Catalogue row:** state, name and platform; on the next lines workers running of slots, workers by harness, queued commands when there are any, and the store fill; last seen on the right.
* **Work area:** the opened host as stacked [detail sections](../controls/detail-section.md) with its [setting fields](../controls/setting-field.md).

There is no stat strip: it repeated the rows (OR-17).

Not built in the app, shown in the mockup: slots and the store fill in the row, the low-space problem row, the setting fields. The app's page head shows the offline marker, not "updated 40s".

| Inventory item | Shown by |
|---|---|
| Hosts: platform, last contact, observation state, harness versions, readiness | Catalogue row: name (the platform shows only when the name does not already say it, OR-17), [state icon](../controls/state-badge.md) (unconfirmed overlay when offline), last contact [age chip](../controls/number.md) (attention past 15m) |
| Environments and account users per host | Host detail: stacked [detail sections](../controls/detail-section.md) (C3) |
| Stores: capacity, used, free, tunnel state, fill per project | [Fill bar](../controls/fill-bar.md) per host with project segments, free space and warning mark |
| Store capacity, memory and CPU bounds, free-space warning | Setting fields with applied, pending or apply error |
| Low space: raise capacity, change locations, add retention rule | Problem row actions |
| Workers running per host (owner, D9, D11) | Catalogue row: running/slots plus [harness counts](../controls/harness-counts.md) |

**Open.** None from stage 3.
