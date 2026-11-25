# System

**Status.** Built, rail section System ([information architecture](../../designs/fleet-ia-and-journeys.md#1-closed-information-architecture)): one view (`app/src/views/hosts.tsx`) renders every System route; the earlier paths `/hosts`, `/hosts/limits`, `/keys`, `/runners`, `/spend` and `/humans` redirect. This spec is the section frame; the content of each root keeps its own spec: [Hosts and limits](hosts.md), [Runners](runners.md), [Spend](spend.md).

**Purpose.** Are hosts, accounts, connections and spend healthy, and who has access? [Live](live.md) answers "what runs now"; System is where the person configures and repairs.

**Routes:**

| Route | Opens |
|---|---|
| `/system` | Idle: readiness and spend summary |
| `/system/hosts/<name>` | One host: state, workers, readings, CLIs, accounts and models, store, Remove |
| `/system/hosts/new` | Add host (`hosts_register`) |
| `/system/accounts`, `/system/accounts/<id>` | Accounts & limits; one subscription |
| `/system/connections/<id>` | One connection: keys, access, intake, balancing, limits |
| `/system/connections/new` | Add connection (`connections_create`) |
| `/system/access` | Access: every subscription, model key and service key with its state and next step; the add flows |
| `/system/runners` | CI runners and agent runs per host |
| `/system/spend` | Spend: period, total, breakdown, what it bought |
| `/system/people/<id>`, `/system/people/new` | One person and their access (owner only) |
| `/hosts…`, `/hosts/limits`, `/keys…`, `/runners`, `/spend`, `/humans…` | Redirect to the matching System route |

## Layout

Three columns ([OR-24](../owner-rules.md)). Shell header: title, the section's action for the open root as an icon (Add host, Add connection, Invite person), focus chip, search.

* **Control column** (`CatalogueTree`, [OR-48](../owner-rules.md)): one tree with the roots **Access, Hosts, Accounts & limits, Connections & keys, Runners, Spend, People** (People only for the owner), no mode switch ([OR-46](../owner-rules.md)). Runners and Spend are roots without children: a click opens their page. A host leaf: state icon (red only for a dead or failing host, [OR-56](../owner-rules.md)), name, and on the right running of slots and last seen ([OR-22](../owner-rules.md)). An account leaf: harness mark, name, left of capacity as a small gauge.
* **Work area, idle:** readiness first: hosts with a problem, accounts near their limit, connections with failing keys, each as one [problem row](../controls/problem-row.md) with its fix action, repeats as `×N` ([OR-58](../owner-rules.md)); then today's and the month's spend as figures with the link to Spend; then services with errors in the last hour. A healthy System says so in one line.
* **Work area, a host open:** the host's stacked [detail sections](../controls/detail-section.md) as [Hosts](hosts.md) specifies, with "Show on Live" in the head.
* **J5 compatibility** ([journey J5](../../designs/fleet-ia-and-journeys.md#3-core-user-journeys-j1j5)): an account inspection (`account_connect`) probes the models the host's vendor CLI offers that binding (codex `model/list`, Claude Code `initialize`); a run that fails because the vendor does not know its model marks the model rejected on that binding until the next probe. The router skips a binding for a model it is known not to serve (cause `subscription:<id>:model:<model>`); a binding whose list is unknown is never skipped. The host page lists **Accounts and models** with the probe's age and a problem row per profile no account there can serve; the account page shows the models per host in **Runs on**, **Profiles on this account** one row per model, and **Router skips, 24 h** (`route_decisions` with `subscriptionId`); idle readiness has one row per profile no ready binding anywhere serves.
* **Spend figures** carry a [sparkline](../controls/figure-tile.md), one bar per UTC day from `spend_summary` with `by: day`: the last 12 days on the idle screen (Spend today, Unpriced runs), the chosen period on Spend (Total, Runs, Unpriced usage). Cost per task and Tasks done have no daily read and no sparkline.
* **Add host** (owner or full access) and **Add connection** are the shell header's plus on their root; each opens its form in the work area (OR-14). Add host sends the SHA-256 printed by `scripts/host-secret.sh` on the host; without it Fleet mints a secret, shown once on that page and kept nowhere in the browser. Add connection opens the new connection's page, where its key is added.
* **Not built: held by Balancing.** The board draws an Access row in the state "held by Balancing". Balancing is not built and waits for the owner's decision; the row state comes with it.
* **Work area, Access** (approved 2026-11-25, board frame "System › Access: status and onboarding (approved)"; `app/src/views/access.tsx`): the head counts what works, what needs the owner and what is not added, with **Add access** and **Check all**. Three groups (subscriptions · login on a host, model API keys · credits, services), one row each: name and kind, a state badge (family `access`) with its sentence, limit or credits, where it serves and how many profiles use it, the last check's age (red when it failed) and one next-step button (Sign in on a host, Paste a key, Add a provider, Check billing, Check). Then the browser-only Ultra items as links. **Add an API key:** provider and connection name, then the numbered steps with the key and billing pages, a password field (sent once to `connections_key_add`, never shown again) and **Add and check**, which shows the check's state and, on a failure, the billing link. **Sign in:** host, then **Sign in on <host>** starts the vendor login on that host (`access_onboard` kind login `start`); the link and device code the host printed appear here within seconds, the host waits up to 15 minutes, then **Check** reads who is signed in. **Limit:** amount, unit and month for a connection (`access_onboard` kind limit). Checks are read-only and never start a login. Data: `access_status`, `access_check`, `access_onboard`.
* **Work area, Spend:** the [Spend](spend.md) page; cost is shown here because it is the question ([OR-54](../owner-rules.md)).

## States

* **Unconfirmed host:** "running · unconfirmed, last seen 4m", never failed (UR-32).
* **Risky action** (remove a host, rotate a key, change money): starts only from a confirmation card naming its scope and the way back ([OR-61](../owner-rules.md)).
* **Phone (390 px):** the tree; an opened item fills the screen.
* **Person, Telegram** ([canvas](../board/screens/people.bl)): the person card has a Telegram section (`TelegramLink`, `app/src/views/humans.tsx`). Not linked: the Telegram users who wrote to the bot without a link (`telegram_people.unknown`), each with Link, then a typed user id, the language (English, Русский) and Link; the bot confirms the link in that chat. Linked: the state, the @username and id, the language as a choice that saves at once, when it was linked (time last, [OR-64](../owner-rules.md)) and Unlink. The People list adds the @username after the email. Machine identities have no Telegram.

## Inventory

As each root's spec says ([hosts](hosts.md), [runners](runners.md), [spend](spend.md)); plus connections and keys (`/keys` today) and people and access (`/humans` today).

## Review of the current build

Checked against the [product map](../product-map.md) rows for System.
* **Keep:** everything the root specs describe for one host, limits, connections and keys (`/keys`), people and access (`/humans`, with the Cloudflare Access state), CI runners and Spend.
* **Change:** release and harness-pin drift shows on each host instead of on Changes; the idle screen leads with problems to fix.
* **Drop:** the spend table on Runners (Spend is the one place for cost, [OR-54](../owner-rules.md)); the "updated 40s" page head on Hosts (the offline marker covers it); services without errors on the idle screen (only services with errors in the last hour show).

## Data

`hosts_list`, `telemetry_summary`, `telemetry_findings`, `account_list` (bindings carry `models`, `modelsObservedAt`, `rejectedModels`), `profiles_list` (architects), `route_decisions` (`subscriptionId`), `subscription_limits`, `connections_list`, `executions_runners`, `executions_ci`, `spend_summary` (also `by: day`), `people_list`, `telegram_people`; writes `hosts_register`, `connections_create`, `telegram_person_set`. The idle summary is one read over the same operations, so agents see the same readiness (fleet.md R2).

## Blocks

* [Navigation rail](../controls/nav-rail.md), [shell header](../patterns/app-shell.md).
* [Filter bar](../controls/filter-bar.md#board), [catalogue row and list group](../controls/data-list.md#board), [detail head](../patterns/list-detail.md#board); [setting field](../controls/setting-field.md#board) for host settings.
* [Gauge](../controls/fill-bar.md#gauge), [figure tile](../controls/figure-tile.md), [run strip](../controls/time-strip.md#run-strip), [worker slot](../controls/worker-slot.md) for a host; [question card](../controls/decision-block.md#card) for confirmations.
* [Problem row](../controls/problem-row.md#board), [table row](../controls/data-list.md#table-row) and [share bar](../controls/share-bar.md#board).

## Design board

Canvas [screens/system.bl](../board/screens/system.bl): idle readiness, a host open, Spend, phone. Canvas [screens/people.bl](../board/screens/people.bl): a person's Telegram, not linked and linked, at 1440 and 390.
