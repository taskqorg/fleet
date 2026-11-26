# Project apps

Fleet develops and tunes a pipeline for a project in Lab; then the project's own application uses it. A **project app** is that application (DAM's site at revilio.com, for example) calling the project's pipelines from its own data processing: it starts a run with files it already has, gets the result back, and every run and its cost show in Fleet under the project and the app. One app belongs to one project and may only run that project's pipelines. Agents, people and apps call the same operations ([shared core](../../fleet.md#principles)); an app adds no operation of its own.

Not built: everything below. Today an outside program can call `pipeline_run` only by posing as a person (a Cloudflare Access service token mapped in `people.json`), which makes it an operator of the project with rights far beyond running pipelines.

## Terms

- **App:** `{id, project, name, pipelines, callbackOrigins, limits}`, one project's caller. `id` is a name such as `dam-site`, unique in Fleet.
- **App key:** the secret the app sends as `Authorization: Bearer <key>`. Fleet stores its SHA-256 only. An app has at most two live keys (the current one and, during rotation, the previous one).
- **Edge token:** the Cloudflare Access service token `fleet-apps` that lets requests to `/api/apps/*` through Cloudflare, as `fleet-machines` does for hosts. It is the same for every app and authorizes nothing in Fleet on its own.
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

## Calling Fleet

- **Route.** An app calls `POST https://fleet.ufobe.com/api/apps/ops/<operation>` and `PUT https://fleet.ufobe.com/api/apps/uploads/<id>` with three headers: `CF-Access-Client-Id` and `CF-Access-Client-Secret` of the edge token, and `Authorization: Bearer <app key>`. Caddy routes `/api/apps/*` like the host routes, past the Access guard, so no `X-Fleet-Person` is ever set. A request to `/api/apps/*` that carries `X-Fleet-Person`, a browser Bearer or a host secret is 403.
- **Identity.** The coordinator finds the key by its hash; an unknown, retired or revoked key is 401. The request runs as the app: scope is the app's project only, `person` is nil, and the operation's auth is checked against the app instead of a person.
- **Operations an app may call.** Only these; anything else is 403 `An app may only run its pipelines`:
  - `pipeline_run` with `projectId` equal to the app's project and a method whose pipeline is in `pipelines`;
  - `pipeline_get` and `pipeline_history`, for runs the app started;
  - `artifact_upload_begin` (into the app's project; the answered `url` is under `/api/apps/uploads/`) and the upload parts;
  - `artifact_get` and the artifact download, for the result artifacts of runs the app started and the app's own uploads.
- **Inputs.** `inputs[]` takes either `{path, artifactId}` (an upload, as today) or `{path, ref: {connection, path}}`. `connection` must be a ready files grant connection of the app's project (`dam-files`); `path` is the server's own path under a granted folder. The app never sends the bytes of a file Fleet can already read.
- **Result.** As for every caller: `wait: true` (the default) answers when the run ends, `wait: false` answers the started run at once; `pipeline_get {runId}` reads its state, `canonicalResult` and `resultArtifact`. An app should use `wait: false` with a callback or polling: one request held for the whole run passes Cloudflare and can be cut.
- **Callback.** `pipeline_run` takes `notify: {url}` when the app has callback origins and the URL's origin is one of them; otherwise 400. When the run ends, Fleet posts `{runId, requestId, state, finishedAt}` to the URL with `X-Fleet-Signature: sha256=<HMAC of the body with the app key>` and `X-Fleet-App: <id>`. The body carries no result: the app reads it with `pipeline_get`, so personal data never travels in a callback. Delivery is background work on the work kit (`attempts`, `next_attempt_at`, `last_error`); a 2xx ends it, anything else retries with backoff until the run's record is cleaned up. Each attempt is in the run's history.
- **Idempotence.** `requestId` stays the app's key for a run: a retried `pipeline_run` with the same `requestId` answers the same run.

## Inputs by reference

At `pipeline_run`, before the run is planned, the coordinator stages each `ref` input: it calls the gateway's `files_get` on the named connection with a read token minted for the run's project, the way the coordinator's own reads are made ([system activity](fleet-analytics.md#system-activity)), and stores the bytes as an artifact of the project, deduplicated by SHA-256 (a file read before is not stored again). The staged artifact then goes to the run as an ordinary input. Each read is a `tool_run` child of the pipeline run, with the path in the gateway spool like every other files call. A path outside the grant, a missing file or a grant that is not ready fails the run before any step with `input:<path>: <reason>`.

## Limits

Settings on the app, set with `project_app_ask` or `project_app_set`; an empty value means no limit, and every limit is empty by default.

- `runsPerDay`: runs the app may start per UTC day. Over it, `pipeline_run` answers 429 `limit:runs-per-day`.
- `spendPerMonth`: `{amount, currency}` of measured and estimated spend of the app's runs in the UTC month. Over it, `pipeline_run` answers 429 `limit:spend-per-month`; a run in flight finishes.
- `concurrentRuns`: runs of the app in flight at once. Over it, `pipeline_run` answers 429 `limit:concurrent-runs`.

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
3. Take the key (`project_app_key_take`) and the edge token's location from the owner's onboarding, store both in the app's secret store, never in a repository or chat.
4. In the app: upload or reference files, call `pipeline_run` with `wait: false` and `notify`, verify the callback's signature, read the result with `pipeline_get`.
5. Check: `project_apps` shows the app's last use and its first run; `analytics_query {dimensions: ["app", "pipeline"], measures: ["executions", "cost"]}` shows the run and its cost.

## Rejected alternatives

- **A service-token person per app** (works today): an operator of the project, able to start sessions and change settings; no app identity in attribution; every key change is a Cloudflare and `people.json` edit.
- **A Cloudflare service token per app as the only credential:** revoking or rotating needs Cloudflare each time, and Fleet would still need its own identity for scope and attribution. One shared edge token plus a Fleet app key keeps Cloudflare a single, unchanging gate and puts revoke and rotate in Fleet.
- **Uploads only:** DAM's documents are already readable through `dam-files`; uploading them again would copy personal files through one more network path and store them twice.
- **Result in the callback body:** the result holds personal fields; a signed notice plus `pipeline_get` keeps them inside Fleet's authenticated API.

## Verification

- Contract tests: an app key reaches only the listed operations and only its project (other project 404, other operation 403, other pipeline 403, a run of another caller 404 on `pipeline_get`); a retired key 401; `X-Fleet-Person` on `/api/apps/*` 403.
- A `ref` input outside the grant fails the run before any step; a ref read appears as a `tool_run` child with its path in the spool.
- Callback: signature verifies with the key; a failing receiver is retried; the body has no result fields.
- Limits: each 429 at its bound; empty limits never refuse.
- Pricing: an `api` step of each of the three providers has an `estimated` cost on the run.
- Live: DAM's app (or a test client with its key) runs `doc-recognize-agent` on two `dam-files` references with a callback; Projects › DAM › Pipelines shows "Used by: DAM site"; `analytics_query` by `app` shows the run and its cost.
