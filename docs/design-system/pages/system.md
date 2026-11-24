# System

**Status.** Target, rail section System ([information architecture](../../designs/fleet-ia-and-journeys.md#1-closed-information-architecture)). Until it is built, the same content is spread over `/hosts`, `/hosts/limits`, `/keys`, `/runners`, `/spend` and, for the owner, `/humans`. This spec is the section frame; the content of each root keeps its own spec: [Hosts and limits](hosts.md), [Runners](runners.md), [Spend](spend.md).

**Purpose.** Are hosts, accounts, connections and spend healthy, and who has access? [Live](live.md) answers "what runs now"; System is where the person configures and repairs.

**Routes:**

| Route | Opens |
|---|---|
| `/system` | Idle: readiness and spend summary |
| `/system/hosts/<name>` | One host: state, workers, readings, CLIs, store, Remove |
| `/system/accounts`, `/system/accounts/<id>` | Accounts & limits; one subscription |
| `/system/connections/<id>` | One connection: keys, access, intake, balancing, limits |
| `/system/runners` | CI runners and agent runs per host |
| `/system/spend` | Spend: period, total, breakdown, what it bought |
| `/system/people/<id>`, `/system/people/new` | One person and their access (owner only) |
| `/hosts…`, `/hosts/limits`, `/keys…`, `/runners`, `/spend`, `/humans…` | Redirect to the matching System route |

## Layout

Three columns ([OR-24](../owner-rules.md)). Shell header: title, the section's action for the open root as an icon (Add host, Add connection, Invite person), focus chip, search.

* **Control column** (`CatalogueTree`, [OR-48](../owner-rules.md)): one tree with the roots **Hosts, Accounts & limits, Connections & keys, Runners, Spend, People** (People only for the owner), no mode switch ([OR-46](../owner-rules.md)). Runners and Spend are roots without children: a click opens their page. A host leaf: state icon (red only for a dead or failing host, [OR-56](../owner-rules.md)), name, and on the right running of slots and last seen ([OR-22](../owner-rules.md)). An account leaf: harness mark, name, left of capacity as a small gauge.
* **Work area, idle:** readiness first: hosts with a problem, accounts near their limit, connections with failing keys, each as one [problem row](../controls/problem-row.md) with its fix action, repeats as `×N` ([OR-58](../owner-rules.md)); then today's and the month's spend as figures with the link to Spend; then services with errors in the last hour. A healthy System says so in one line.
* **Work area, a host open:** the host's stacked [detail sections](../controls/detail-section.md) as [Hosts](hosts.md) specifies, with "Show on Live" in the head.
* **Work area, Spend:** the [Spend](spend.md) page; cost is shown here because it is the question ([OR-54](../owner-rules.md)).

## States

* **Unconfirmed host:** "running · unconfirmed, last seen 4m", never failed (UR-32).
* **Risky action** (remove a host, rotate a key, change money): starts only from a confirmation card naming its scope and the way back ([OR-61](../owner-rules.md)).
* **Phone (390 px):** the tree; an opened item fills the screen.

## Inventory

As each root's spec says ([hosts](hosts.md), [runners](runners.md), [spend](spend.md)); plus connections and keys (`/keys` today) and people and access (`/humans` today).

## Review of the current build

Checked against the [product map](../product-map.md) rows for System.
* **Keep:** everything the root specs describe for one host, limits, connections and keys (`/keys`), people and access (`/humans`, with the Cloudflare Access state), CI runners and Spend.
* **Change:** release and harness-pin drift shows on each host instead of on Changes; the idle screen leads with problems to fix.
* **Drop:** the spend table on Runners (Spend is the one place for cost, [OR-54](../owner-rules.md)); the "updated 40s" page head on Hosts (the offline marker covers it); services without errors on the idle screen (only services with errors in the last hour show).

## Data

`hosts_list`, `telemetry_summary`, `telemetry_findings`, `account_list`, `subscription_limits`, `connections_list`, `executions_runners`, `executions_ci`, `spend_summary`, `people_list`. The idle summary is one read over the same operations, so agents see the same readiness (fleet.md R2).

## Blocks

* [Navigation rail](../controls/nav-rail.md), [shell header](../patterns/app-shell.md).
* [Filter bar](../controls/filter-bar.md#board), [catalogue row and list group](../controls/data-list.md#board), [detail head](../patterns/list-detail.md#board); [setting field](../controls/setting-field.md#board) for host settings.
* [Gauge](../controls/fill-bar.md#gauge), [figure tile](../controls/figure-tile.md), [run strip](../controls/time-strip.md#run-strip), [worker slot](../controls/worker-slot.md) for a host; [question card](../controls/decision-block.md#card) for confirmations.
* [Problem row](../controls/problem-row.md#board), [table row](../controls/data-list.md#table-row) and [share bar](../controls/share-bar.md#board).

## Design board

Canvas [screens/system.bl](../board/screens/system.bl): idle readiness, a host open, Spend, phone.
