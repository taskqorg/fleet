# Project apps

Fleet develops and tunes a pipeline for a project in Lab; then the project's own application uses it. A **project app** is that application (DAM's site at revilio.com, for example) calling the project's pipelines from its own data processing: it starts a run with files it already has, gets the result back, and every run and its cost show in Fleet under the project and the app. One app belongs to one project and may only run that project's pipelines. Agents, people and apps call the same operations ([shared core](../../fleet.md#principles)); an app adds no operation of its own.

Approved by the owner 2026-11-26 (board `drafts/project-apps`, frames PA1-PA4). Built: apps, keys, the app route, the Cloudflare edge, `requestedBy {kind: app}`, the `app` dimension, inputs by reference, callbacks and limits, and the screens Setup › Apps (with `use30d`) and the pipeline page's Used by lines (`use`). Not built: API-step pricing, the Pipelines facet's 30-day runs per row, and a Used by line opening Runs filtered to its caller. Today an outside program can call `pipeline_run` only by posing as a person (a Cloudflare Access service token mapped in `people.json`), which makes it an operator of the project with rights far beyond running pipelines.

## Terms

- **App:** `{id, project, name, pipelines, callbackOrigins, limits}`, one project's caller. `id` is a name such as `dam-site`, unique in Fleet.
- **App key:** the secret the app sends as `X-Fleet-App-Key: <key>`. Fleet stores its SHA-256 only. An app has at most two live keys (the current one and, during rotation, the previous one).
- **Edge token:** the Cloudflare Access service token `fleet-apps` that a policy of the site's Access application admits, so the app's requests pass Cloudflare and the Access guard. It is the same for every app and authorizes nothing in Fleet on its own. Fleet creates it through the Cloudflare API (below); nobody makes it by hand.
- **Input by reference:** a `pipeline_run` input that names a file on one of the project's [files grants](fleet-project-access.md) instead of an uploaded artifact.

## Model

- `project_app`: `id`, `project_id`, `name`, `pipelines` (pipeline ids the app may run; `["*"]` is every pipeline of the project, the default), `callback_origins` (origins a callback may go to; empty means no callbacks), `limits` (below), `state` (`asked`, `active`, `declined`, `revoked`), `request_id`, `asked_by`, `question_id`, `last_used_at`, audit through `audit()`.
- `project_app_key`: `id`, `app_id`, `sha256`, `created_at`, `retired_at` (set when a rotation's grace ends or on revoke), `last_used_at`, `taken_at` (when the value was handed out). The value itself is never stored.
- A run an app starts records `requestedBy {kind: "app", app: <id>, projectId}`. `directedBy` stays the person who approved the app, so existing person-based gates and budgets keep one owner of the work.
- `execution` gets the typed grouping column `app_id`, written once at creation from the run's `requestedBy`, inherited by every step and leaf execution of the run.

### App states

| From | To |
|---|---|
| `asked` | `active` (approved), `declined`, `asked` (withdrawn and asked again with a changed request) |
| `active` | `revoked` |
| `declined`, `revoked` | none |

## Operations

- `project_app_ask {projectId, requestId, id, name, pipelines?, callbackOrigins?, limits?, reason}` (person or agent tool, project grant): creates the app `asked` and an approval card (an `operator_decision` question with `data.approval` action `app`, owner-only like `access`). The card states the project, the pipelines, the callback origins, the limits and the reason. Idempotent by `requestId`; a changed request withdraws the old card. Approve makes the app `active` and mints its first key; Not now declines it.
- `project_app_key_take {appId, requestId}` (the person or agent that asked, or the owner): answers the newest untaken key value once and sets `taken_at`; a second call answers 409 with the time it was taken. The asking agent stores it in the app's own secret store (a GitLab CI variable, for example) without printing it.
- `project_app_rotate {appId}` (owner or full access): mints a new key; the previous key keeps working until the new key's first use, then gets `retired_at`. Take the new value with `project_app_key_take`.
- `project_app_revoke {appId}` (owner or full access): retires every key at once and makes the app `revoked`. Runs already started finish; their records stay.
- `project_app_set {appId, pipelines?, callbackOrigins?, limits?}` (owner or full access): changes the settings of an active app; every change is a `setting-change`.
- `project_apps {projectId}` (person or agent tool): the project's apps with state, settings, key ages, last use, and runs, failures and cost over the last 30 days. Never a key value. Every write to `project_app` and `project_app_key` is a change (kind `project-app`).

## Edge setup

Fleet sets up Cloudflare for apps itself, in the onboarding style of [access status](fleet-connections.md#11-access-status-and-onboarding); the owner only approves a card.

- `apps_edge_status` (person or agent tool): `ready`, `missing` or `failed`, with the Access application and service token ids, the token's expiry, and the error of the last attempt.
- `apps_edge_setup {requestId}` (person or agent tool): asks the owner with one approval card ("Fleet creates the Cloudflare service token fleet-apps and an Access application for fleet.ufobe.com/api/apps/* that admits only it"). Approve queues the work; it runs with the coordinator's Cloudflare token (`FLEET_CLOUDFLARE_ACCESS_API_TOKEN_FILE`, the one Humans uses):
  1. create the service token `fleet-apps` (`POST /accounts/<id>/access/service_tokens`) and write its client id and secret to `<FLEET_SECRET_DIR>/apps/edge.env` (0600); the secret is never in the database;
  2. add one policy `fleet-apps` (`non_identity`, include that service token) to the site's own Access application (`fleet.ufobe.com`). A separate application on `/api/apps` is not used: the Access guard verifies the site application's audience, and Caddy strips `Authorization` on every path but the host and gateway routes, so the app's request goes through the guard like an agent's (the guard names the token's client id as `X-Fleet-Person`) and needs no Caddy change;
  3. probe: a request to `/api/apps/ops/apps_ping` with the edge token and no app key must answer Fleet's 401 (it passed Cloudflare), and one without the edge token must be stopped by Cloudflare.
  The edge token admits nothing else: on any other path its client id is not a person (403 `Unknown person`).
  Each step is idempotent (an existing token or application named so is reused); the state and every step are in `audit()`.
- **Permissions.** The Cloudflare token needs, on the Fleet account, `Access: Service Tokens Edit` and `Access: Apps and Policies Edit` besides Humans' `Access: Groups Edit`. A refusal names the missing permission in `apps_edge_status`, and the card says what to add to the token.
- **Rotation.** `apps_edge_setup` with `rotate: true` refreshes the service token's secret (`POST .../service_tokens/<id>/rotate`); apps get the new pair with their next key (`project_app_rotate`, then `project_app_key_take`). A token near its expiry shows in `apps_edge_status`.
- `project_app_key_take` answers the edge token's client id and secret and the route's base URL with the app key, so the app gets everything it needs in one answer.

## Calling Fleet

- **Route.** An app calls `POST https://fleet.ufobe.com/api/apps/ops/<operation>` and `PUT https://fleet.ufobe.com/api/apps/uploads/<id>` with three headers: `CF-Access-Client-Id` and `CF-Access-Client-Secret` of the edge token, and `X-Fleet-App-Key: <app key>`.
- **Identity.** The coordinator requires the guard's browser Bearer and the edge token's client id as the person header (anything else on `/api/apps/*` is 403), then finds the key by its hash; a missing, unknown, retired or revoked key is 401. The request runs as the app: scope is the app's project only, `person` is nil, and the operation's auth is checked against the app instead of a person.
- **Operations an app may call.** Only these; anything else is 403 `An app may only run its pipelines`:
  - `pipeline_run` with `projectId` equal to the app's project and a method whose pipeline is in `pipelines`;
  - `pipeline_get`, for runs the app started, and `pipeline_history`, the project's runs;
  - `artifact_upload_begin` (into the app's project; the answered `url` is under `/api/apps/uploads/`) and the upload parts;
  - `artifact_get`, for artifacts of the app's project (the result itself is `pipeline_get`'s `canonicalResult`);
  - `apps_ping`: the app and its project, the edge probe.
- **Inputs.** `inputs[]` takes either `{path, artifactId}` (an upload, as today) or `{path, ref: {connection, path}}`. `connection` must be a files grant connection of the app's project (`dam-files`); `path` is the server's own path under a granted folder. The file is read after the run is answered ([inputs by reference](#inputs-by-reference)). The app never sends the bytes of a file Fleet can already read.
- **Result.** As for every caller: `wait: true` (the default) answers when the run ends, `wait: false` answers the started run at once; `pipeline_get {runId}` reads its state, `canonicalResult` and `resultArtifact`. An app should use `wait: false` with a callback or polling: one request held for the whole run passes Cloudflare and can be cut.
- **Callback.** `pipeline_run` takes `notify: {url}` only from an app caller whose callback origins hold the URL's `https` origin; otherwise 400. When the run ends, Fleet posts `{runId, requestId, state, finishedAt}` (`finishedAt` in epoch ms, as `pipeline_get`) to the URL with `X-Fleet-Signature: sha256=<hex HMAC-SHA256 of the body>` and `X-Fleet-App: <id>`. The HMAC key is the app's **callback secret**, not the app key (Fleet keeps only the key's hash): the app's first key (approval) makes it in `<FLEET_SECRET_DIR>/apps/callback/<app id>` (0600), and a rotation keeps it, so callbacks verify while the app still uses its previous key (an app approved before callbacks gets one with its next rotation), and `project_app_key_take` answers it as `callbackSecret`. The body carries no result: the app reads it with `pipeline_get`, so personal data never travels in a callback. Delivery is the work kit's `app-callback` queue over table `app_callback` (`attempts`, `next_attempt_at`, `last_error`, `delivered_at`; a row waits until its run ends): a 2xx ends it, anything else retries with backoff until the run's record is cleaned up (the row goes with it). The post follows no redirect and times out after 15 s.
- **Idempotence.** `requestId` stays the app's key for a run: a retried `pipeline_run` with the same `requestId` answers the same run.

## Inputs by reference

`pipeline_run` reads no file before it answers. It checks what needs no network: a person to store the files for, a path, and a connection that is a files grant of the run's project (an unknown connection, a connection of another project or of another kind answers 400 `input:<path>: <reason>`, as does an input with both `artifactId` and `ref`). It then creates the run in state `preparing`, whose record names each ref as asked (`inputRefs: [{path, connection, file}]`), and answers it, with `wait: false` at once.

Each ref waits as one `pipeline_input_ref` row (`attempts`, `next_attempt_at`, `last_error`) on the work kit's `input-refs` queue (`pipeline_refs.go`). Its step leaves the read as its call, outside any transaction: the gateway's `files_get` on the named connection with a read token minted for the run's project, the way the coordinator's own reads are made ([system activity](fleet-analytics.md#system-activity)), into a private temporary folder, then the operator-upload path as the run's director, deduplicated by SHA-256 (a stored artifact of the project with the same content is reused). Each read is a system `tool_run` of the read token, with the path in the gateway spool like every other files call. Several refs of one run are read in parallel. The step after the call records on the run's `inputRefs` entry the artifact, its `bytes` and `stageMs` (read and store), so a slow server shows in `pipeline_get`. Once every ref is stored, the run goes to `running` and its steps start with the files as ordinary inputs.

A ref that cannot be read (a grant that is not ready, a path outside the grant, a missing file, a failed read) fails the run before any step: `state: failed`, `result.error: {code: "input_unavailable", message: "input:<path>: <reason>"}`, and the reason on its `inputRefs` entry. Any other failure (the object store, the database) is retried with the kit's backoff. A run that ends (failed or cancelled) drops its refs still waiting, and an app's callback fires as for any end. `wait: true` waits through `preparing` as through `running`. Every caller (agent, person, app) may use refs; a native agent's `pipeline_run` inside its receipt's transaction reads nothing either. A retried `requestId` answers its run without reading its refs again: the signature names a ref as asked (connection and path), not the bytes it read. A rerun of a run that ended before every ref was stored reads its refs again.

## Limits

Settings on the app, set with `project_app_ask` or `project_app_set`; an empty value means no limit, and every limit is empty by default.

- `runsPerDay`: runs the app may start per UTC day. Over it, `pipeline_run` answers 429 `limit:runs-per-day`.
- `spendPerMonth`: `{amount, currency}` of measured and estimated spend of the app's runs in the UTC month. Over it, `pipeline_run` answers 429 `limit:spend-per-month`; a run in flight finishes.
- `concurrentRuns`: runs of the app in flight at once. Over it, `pipeline_run` answers 429 `limit:concurrent-runs`.

A limit refuses at its bound (the next run would be one too many, or the month's spend has reached the amount). Spend counts each leaf's `execution_spend` cost in the limit's currency with basis `measured` or `estimated`. The limits are checked before the run is created and again in the run's creating transaction; a retried `requestId` answers its run whatever the limits.

The project's own budgets ([people access](fleet-people-access.md)) apply to app runs as to any other work of the project.

## Attribution and cost

- `analytics_query` gets the dimension `app` (the column `app_id`; `unknown` for work no app started), and `spend_summary` takes `by: app`. Nothing new is stored for cost: an app run's leaf executions carry their usage as any run's do.
- **API-step pricing.** Today an `api` step's `tool_run` is unpriced: the catalogue reads one `usageField` (`usage.total_tokens`), while price entries are per model and token kind. Each model tool in the catalogue declares `usageFields` instead: the reply paths of input, output, cache-read and reasoning tokens and of the model name (`usage.input_tokens`, `usage.output_tokens`, `model` for the OpenAI Responses API; `usageMetadata.promptTokenCount`, `usageMetadata.candidatesTokenCount`, `modelVersion` for Gemini; `usage.input_tokens`, `usage.output_tokens`, `model` for Anthropic). The gateway writes the split and the model on the spool line, ingest records them on the `tool_run`'s totals, and the price lookup matches by provider, model and token kind, so the cost is `estimated` from the price table instead of `unavailable`. A reply without these fields stays `unavailable` with its reason.

## Screens

Board frames: `drafts/project-apps.bl` (dark, 1440 and 390).

- **Projects › <project> › Setup › Apps** (after Access): one row per app: name and id, state, pipelines (`all pipelines` or their ids), last use, runs and cost over 30 days, limits in words ("no limits", "500 runs a day"). A row opens its detail: callback origins, keys (created, last use, taken), the approval question, with **Rotate key** and **Revoke** for the owner, each confirming inline. "No apps yet." when empty, with one line on how an app is asked for (an agent or a person calls `project_app_ask`; the approval is a Needs-you card).
- **Approval card** (Needs you): "<project> app <name> asks to run <pipelines>", the callback origins, the limits, the reason, Approve and Not now.
- **Pipeline page, Overview**: under the description and In · Out, one line per caller over the last 30 days: "Used by: DAM site · 412 runs · 3 failed · $4.10 · 30 d", then "Agents and people · 19 runs · $1.76" when there are such runs; Lab runs are left out (the ranking shows them). The line opens Runs filtered to that caller. `project_pipeline_view` answers this as `use: [{caller: {kind, id, name}, runs, failed, cost}]`.
- **Pipelines facet**: each pipeline row gets the 30-day real-use runs next to its latest run's age.

## How an agent sets up an app

1. Ask for the app: `project_app_ask {projectId: "dam", requestId, id: "dam-site", name: "DAM site", pipelines: ["doc-recognize-agent"], callbackOrigins: ["https://revilio.com"], reason}`.
2. Wait for the owner's answer on the card (`project_apps` shows the state).
3. Once `apps_edge_status` is `ready` (else `apps_edge_setup` and the owner's card), take the key and the edge token (`project_app_key_take`) and store them in the app's secret store, never in a repository or chat.
4. In the app: upload or reference files, call `pipeline_run` with `wait: false` and `notify`, verify the callback's signature, read the result with `pipeline_get`.
5. Check: `project_apps` shows the app's last use and its first run; `analytics_query {dimensions: ["app", "pipeline"], measures: ["executions", "cost"]}` shows the run and its cost.

## Rejected alternatives

- **A service-token person per app** (works today): an operator of the project, able to start sessions and change settings; no app identity in attribution; every key change is a Cloudflare and `people.json` edit.
- **A Cloudflare service token per app as the only credential:** revoking or rotating needs Cloudflare each time, and Fleet would still need its own identity for scope and attribution. One shared edge token plus a Fleet app key keeps Cloudflare a single, unchanging gate and puts revoke and rotate in Fleet.
- **The owner makes the edge token by hand:** the owner decided (2026-11-26) that Fleet does it through the Cloudflare API and the owner only approves.
- **Uploads only:** DAM's documents are already readable through `dam-files`; uploading them again would copy personal files through one more network path and store them twice.
- **Result in the callback body:** the result holds personal fields; a signed notice plus `pipeline_get` keeps them inside Fleet's authenticated API.

## Verification

- Contract tests: an app key reaches only the listed operations and only its project (other project 404, other operation 403, other pipeline 403, a run of another caller 404 on `pipeline_get`); a retired key 401; a request on `/api/apps/*` without the edge token's client id 403.
- A `ref` input outside the grant fails the run before any step; a ref read appears as a `tool_run` child with its path in the spool.
- Callback: signature verifies with the callback secret; a failing receiver is retried; the body has no result fields.
- Limits: each 429 at its bound; empty limits never refuse.
- Pricing: an `api` step of each of the three providers has an `estimated` cost on the run.
- Live: DAM's app (or a test client with its key) runs `doc-recognize-agent` on two `dam-files` references with a callback; Projects › DAM › Pipelines shows "Used by: DAM site"; `analytics_query` by `app` shows the run and its cost.
