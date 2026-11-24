# Library

**Status.** Target, rail section Library ([information architecture](../../designs/fleet-ia-and-journeys.md#1-closed-information-architecture)). Until it is built, the same content is the one page behind `/agents`, `/pipelines` and `/catalogue`, plus `/bots` and `/changes`. This spec is the section frame; the content of each root keeps its own spec: [Methods](catalogue.md), [Agents and Skills](agents.md), [Pipelines](pipelines.md), [Changes](changes.md).

**Purpose.** What methods, pipelines, agents, bots and skills we have, which are recommended, and what has not reached the store or the hosts ([principles](../owner-rules.md#principles) 3: details on demand).

**Routes:**

| Route | Opens |
|---|---|
| `/library` | Idle: library summary |
| `/library/methods/<id>` | A capability and its methods |
| `/library/pipelines/<id>` | A pipeline: definition, runs, usage |
| `/library/agents/<id>` | An agent profile |
| `/library/bots/<name>`, `/library/bots/new` | A bot definition: instructions, schedule, automations, runs; a new bot |
| `/library/skills/<id>` | A skill and its rollout |
| `/library/changes` | Repository and store drift |
| `/catalogue…`, `/agents…`, `/pipelines…`, `/changes`, `/bots…` | Redirect to the matching Library route |

## Layout

Three columns ([OR-24](../owner-rules.md)). Shell header: title, New (`plus`, a short menu: agent, pipeline, bot), focus chip, search.

* **Control column** (`CatalogueTree`, [OR-48](../owner-rules.md)): one tree with the roots **Methods, Pipelines, Agents, Bots, Skills, Changes**, no mode switch ([OR-46](../owner-rules.md)). Above it the list header with search (filter words `category:`, `harness:`, `skill:`, `state:`, [OR-36](../owner-rules.md)) and saved views. A leaf: state icon (recommended, experimental, deprecated, waits for the repository), name and id, and on the right 30-day runs. Lab variants fold under their base agent. Changes shows its drift count as a counter on the root ([OR-29](../owner-rules.md)).
* **Work area, idle:** what waits first: definitions waiting for a repository version, with Take all as one [problem row](../controls/problem-row.md); recommendations that changed this week (J3); then totals by root (agents active in 30 days, pipelines that ran, bots awake). Never a second copy of the tree.
* **Work area, a definition open** ([definition editor](../patterns/definition-editor.md)):
  1. Head: kind icon, name, id, state, revision, and the actions as icons (Compare in Lab, Recommend, Take repository version).
  2. Only the fields that differ from the base default, then "Inherited defaults (N)" folded ([OR-45](../owner-rules.md), UR-65); no `override` words or badges.
  3. A tab bar for the facets of that one definition ([OR-46](../owner-rules.md)): Configuration · Effective · Diff · Usage · History; a pipeline adds Runs, a bot adds Automations and Runs.
* **Bots.** An active bot is a session on [Home](home.md); its definition lives here. "Talk to it" in the bot's head opens its session ([OR-51](../owner-rules.md)).

## States

* **Repository version waits:** the leaf's state icon in the attention tone; the head offers Take repository version with a preview first.
* **Store only:** the leaf is marked; the head offers Save to the repository (export patch).
* **Phone (390 px):** the tree; an opened definition shows the differences first, the tabs as a scrollable tab bar.

## Inventory

| Inventory item | Shown by |
|---|---|
| Agent configurations; purpose, category, tags; active or unused | Agents root; agent head and differences |
| Lab variants folded under their agent | Agents root |
| Skills, their files and carrying agents; rollout | Skills root; skill detail |
| Pipelines with steps, where each lives, 30-day runs and price | Pipelines root; Configuration and Runs |
| Catalogue entries with recommendations and evidence | Methods root ([catalogue](catalogue.md)) |
| Bot definitions, schedule, automations, runs | Bots root |
| Agents waiting for a repository version; definitions made in Fleet | Idle problem row; Changes root |

## Data

As each root's spec says: `catalogue`, `definition_get`, `definition_usage`, `pipeline_get`, `bot_list`, `bot_get`, `definition_take_repository`, `definition_export`. The tree reads one list per root and stays mounted while a definition loads ([OR-47](../owner-rules.md)).

## Review of the current build

Checked against the [product map](../product-map.md) rows for Library and Bots.
* **Keep:** everything the root specs describe ([catalogue](catalogue.md), [agents](agents.md), [pipelines](pipelines.md), [changes](changes.md)); bot status, runs with causes, automations (trigger, state, next time, last outcome), settings, New bot, enable, pause and Run now; tree search, pins, persisted folding and filter words.
* **Change:** Agents and Skills tabs, the `/pipelines` and `/catalogue` pages and `/bots` become roots of one tree; a bot's open questions show on Home, not on its page.
* **Drop:** the Library Summary breakdowns by harness and by category (counts per root are enough); the Pipelines link in the Agents header (the tree has the root); the bot Analytics tab (bot cost is a purpose on System › Spend, [OR-54](../owner-rules.md)).

## Open

* Bot definitions have no human code; `/library/bots/<name>` uses the bot's unique name.

## Blocks

* [Navigation rail](../controls/nav-rail.md), [shell header](../patterns/app-shell.md).
* [Filter bar](../controls/filter-bar.md#board), [catalogue row and list group](../controls/data-list.md#board), [detail head](../patterns/list-detail.md#board); [tab bar](../controls/tab-bar.md) for definition facets; [setting field](../controls/setting-field.md#board) for edits.
* [State dot and word](../controls/state-badge.md#dot-and-word), [button](../controls/button.md).
* [Diff row](../patterns/definition-editor.md#diff-row), [problem row](../controls/problem-row.md#board) and, for pipelines, [step row](../controls/step-list.md#board).

## Design board

Canvas [screens/library.bl](../board/screens/library.bl): idle, an agent open with differences and inherited defaults folded, a bot open, phone.
