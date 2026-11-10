# Fleet connections: API keys, the Fleet gateway and deleting Pipe

*   **Author:** Alex Kirs
*   **Status:** Proposed
*   **Date:** 2026-11-10
*   **Companion:** [fleet-people-access.md](fleet-people-access.md) (people, subscriptions, dispatch, budgets). This design answers data foundation Q9.

--------------------------------------------------------------------------------

## Overview

Fleet reaches outside APIs (Tavily web search and extract today; Meshy and model APIs next) through Pipe, a separate service in the assetq repository. Fleet's `connection_call.py` loaded Pipe's client library, checks its hash, reads a Pipe token from a host file and sends the Fleet step ID as a free-form label (`X-Pipe-Task`).

L4 leaves `connection_call.py` and its MCP mode in Python until C1; C1 replaces this path with Fleet's own connections, without a Rust bridge to Pipe.

The owner decided Fleet gets its own connections system and Pipe is deleted, with no bridge period. This design:

*   adds an **API key connection** list, separate from subscriptions (owner decision 1: two lists, one ownership model);
*   moves Pipe's proven read lane into Fleet as **`fleet-gateway`**, a separate process on PDS18 (decision 7);
*   replaces Pipe tokens and labels with **per-run tokens** minted by the coordinator;
*   keeps **one accounting record** per call: a `tool_run` execution with a `connection_cost` measurement;
*   ends with **deleting Pipe**.

### Glossary

*   **PDS18:** the Linux server that runs the coordinator, Postgres and the nightly backup.
*   **Pipe, assetq, read lane:** Pipe is the API service in the assetq repository; its read lane (`pipe-read`, Rust crate `lane/`, documented in assetq `docs/read-lane.md`) is the process that holds Tavily keys and serves `web_search` and `web_extract`.
*   **DashQ, `hermes`:** the owner's metrics service that receives Pipe telemetry, and the user that sends Pipe alerts. Both leave with Pipe.
*   **`fleet-machines` service token:** the Cloudflare Access service token executors use to reach PDS18.
*   **`tool_run`, `connection_cost`:** the execution kind for one API call and the measurement that holds its cost (data foundation, Non-Goals).
*   **Decisions 1, 7, 8:** the brief's round 2: two lists for subscriptions and API keys; one gateway in Fleet on PDS18; delete the read lane and archive assetq.
*   **Provider:** a service Fleet calls: Tavily, Meshy, OpenAI API, Anthropic API, Google AI.
*   **API key connection:** one named way to use a provider with keys Fleet holds, for example `tavily`. It has a holder, an access list, an intake mode and budgets. Called "API key" in the product.
*   **Key:** one secret in a connection. State: `active`, `draining`, `disabled`, `retired`.
*   **Gateway:** `fleet-gateway`, the only process that holds key values at run time.
*   **Snapshot:** the file the coordinator publishes for the gateway: token hashes, access lists, key states. No key values.
*   **Spool:** the gateway's local append-only call record file, ingested by the coordinator.
*   **Per-run token:** a bearer minted for one execution, valid until it ends.
*   **Tool:** one operation of a connection, for example `web_search`.

## The Problem

*   **Keys live outside Fleet.** Tavily keys live in Pipe's `/etc/pipe-read/tavily-keys`; Fleet cannot list, rotate or budget them.
*   **Calls are attributed by a label anyone can send.** The Pipe token is a host file. Any agent on the same host can read it and call with any label; it can also edit its own `.connection-calls.jsonl` (fleet-lab.md, Connections, last bullets).
*   **Two systems record one call.** Pipe keeps telemetry; Fleet rebuilds `tool_run` rows from the client's file.
*   **Every new provider needs Pipe work.** Meshy, model APIs and the asset pipeline all wait on a repository Fleet is replacing.
*   **Pipe hard-codes a queue bound** (`503 queue-full`), a number the owner did not choose.

## Goals

*   **G1.** A list of API key connections with holder, provider, tools, keys (fingerprint and state only), access list, intake, budgets and spend.
*   **G2.** Key values live only in the coordinator's secret directory and in the gateway's memory; they are backed up with SOPS/age, and a restore needs only that backup and the age identity.
*   **G3.** Rotation without downtime: add, drain, retire. A key refused with 401 or 402 is disabled with its reason and raises one needs-you question (cause `key:<id>:disabled`); only the owner re-enables it.
*   **G4.** `fleet-gateway` serves calls while the coordinator is down, from the last valid snapshot, and records every call in its spool.
*   **G5.** Three call methods on one gateway: HTTP, MCP and CLI.
*   **G6.** Every call is a `tool_run` with `connection_cost`, attributed through its per-run token to execution, project, director and experiment, with no caller label.
*   **G7.** Pipe deleted from Fleet, hosts, assetq, PDS18 and Cloudflare; the assetq repository archived (decision 8).

## Non-Goals

*   Subscription logins through the gateway. The gateway never holds or relays OAuth tokens or session credentials (people-access, Principle).
*   Key switching to get past a quota. Quota replies and 429 pass through unchanged.
*   A Fleet-invented queue bound or rate. An owner may set one per connection.
*   Pipe's asset library, generation worker and method registry (Lab and artifacts replace them).
*   A gateway per host (decision 7: one, on PDS18).
*   Model API keys as harness endpoints (see Future Improvements).

## Open Questions

*   **Q1. Meshy job semantics (decided).** Submit, then poll; the job ID is kept in `execution.data.jobId`. Meshy's API takes no idempotency key (checked in its Image to 3D reference), so a submit is never retried or failed over. On an unknown outcome or a 5xx, Fleet first reads the key's newest tasks (`recent_tasks`, `sort_by=-created_at`) and adopts the one task created since the call started; with none or several, the step ends `unknown:submit-unconfirmed`, visible, and nothing is sent again (owner decision).
*   **Q2. Reconcile source per provider.** Tavily exposes per-key usage; Meshy and model APIs differ. C2 ships reconcile for Tavily only.

## Critical User Journeys (CUJs)

### CUJ 1: An agent searches the web

*   **Given:** a pipeline agent step whose profile grants connection `tavily`.
*   **When:** the agent calls `mcp__tavily__web_search`.
*   **Then:** the Fleet MCP server sends the call to the gateway with the step's per-run token. The gateway checks the token hash in its snapshot, injects a key, forwards to Tavily and appends one spool line. The step later shows a `tool_run` child with `connection_cost {unit: credits, amount, basis: measured}`, directed by the step's director.

### CUJ 2: The coordinator restarts mid-run

*   **Given:** an agent is running web searches.
*   **When:** the coordinator stops for two minutes.
*   **Then:** searches keep working from the gateway's last snapshot. After restart, the coordinator ingests the spool; the loss counter is 0; every call appears as a `tool_run`.

### CUJ 3: Rotate a Tavily key

*   **Given:** connection `tavily` has key `k1` active.
*   **When:** the owner adds `k2`, then marks `k1` draining, then retires it.
*   **Then:** no call fails; new calls use `k2` once `k1` drains; `k1`'s fingerprint stays in history.

### CUJ 4: A key is revoked at the provider

*   **When:** Tavily answers 401 for `k2`.
*   **Then:** the gateway disables `k2` (persisted across restarts), the call returns `key-disabled-401`, the coordinator raises one needs-you question with cause `key:k2:disabled`, and only the owner can re-enable it.

### CUJ 5: Pipe is gone

*   **When:** C4 lands.
*   **Then:** the Pipe search in the Verification Plan finds nothing; no host has a Pipe MCP server; the old `pipe.ufobe.com` read route fails; a Lab web-research trial completes through the gateway.

## Technical Plan (Architecture)

```text
  agent (any host) ──MCP stdio──▶ fleet call mcp ─┐
  coordinator api step ──CLI──▶ fleet call ───────┼─HTTP + per-run token─▶ fleet-gateway (PDS18, loopback :8742)
  script ──HTTP──────────────────────────────────┘    (remote hosts: Cloudflare-guarded entry      │ keys from the secret dir
                                                        + fleet-machines service token)             │ snapshot.json (read each second)
                                                                                                    │ spool.jsonl (append)
  coordinator ── publishes snapshot.json ──────────────────────────────────────────────────────────▶│
  coordinator ◀── ingests spool → tool_run + connection_cost ──────────────────────────────────────┘
```

### 1. Model

*   **`provider`**: a code-side catalogue entry, not a table: `{id, baseUrl, tools: {<tool>: {method, path, unit, idempotent}}}` in `providers.ts`. Tavily (`web_search` → `/search`, `web_extract` → `/extract`, unit `credits`, idempotent) ships in C0; Meshy in C3. The coordinator copies the catalogue into the snapshot, so the Rust gateway has no second copy.
*   **`api-connection`** document kind (separate list from `managed-account`, decision 1): `{id, name, provider, holder, access: {people: [], projects: [], experiments: []}, intake: all | paused, tools: [], comment}`. **Use:** a run token gets the connection when the run's director, its project or its experiment is listed (any one is enough); `projects: ['*']` means every project; empty means only work directed by the holder. **Visibility** is separate: architects and the holder see the connection; the owner sees all.
*   **`connection-key`** document kind: `{id, connectionId, fingerprint (first 12 hex of SHA-256), state, addedAt, addedBy, disabledReason?, disabledAt?}`. No value. The key's id is its fingerprint (document id `key:<fingerprint>`), as in the lane, so the gateway's disabled-key file and the spool name keys the same way.
*   Both kinds carry `holder` (people-access §4), so ownership, visibility and audit are one model with subscriptions.

### 2. Keys

*   **Values.** `<FLEET_SECRET_DIR>/connections/<connection>/<fingerprint>` on PDS18, one value per file, mode 0600, coordinator user. Added through `connections_key_add` (owner only), which writes the file and the metadata row in one step and returns only the fingerprint. Screens, agents, executors and profiles never receive a value.
*   **Import and restore.** At start the coordinator registers every value file that has no row, if the file name matches the value's fingerprint; a directory named after a catalogue provider with no connection becomes that connection (holder owner, access `{projects: ['*']}`). `scripts/connections-import.sh` splits a one-key-per-line file (Pipe's `tavily-keys`) into such files, so import and restore need no database step.
*   **Gateway load.** `fleet-gateway` runs as a user unit of the coordinator user, which already owns the secret directory, so a separate service user would protect nothing. It reads a key's value file when a snapshot first names the key and checks it against the fingerprint; a key change takes effect at the next snapshot adoption, with no restart. Restarts drain in-flight calls while the socket stays open (socket activation).
*   **Backup.** The secret directory is encrypted with SOPS/age into the nightly backup (`fleet-backup.sh`, `secrets.sops.json`; recipient `FLEET_BACKUP_AGE_RECIPIENT` in `backup-age.env`, the age identity kept off the host). Restore is the read-lane restore copied: decrypt with the age identity, start the gateway, it serves from its retained snapshot without Postgres.
*   **Rotation:** add → mark old `draining` (no new calls) → `retired` after the owner confirms. No quiet-period timer (no invented number).
*   **Failover:** to the next active key only when a key got no HTTP answer **and** the tool is `idempotent`. A non-idempotent tool (Meshy submit) returns `outcome: unknown` instead, never replayed. 429 and quota replies pass through unchanged with `Retry-After`. 401 and 402 disable that key (persisted in the gateway's `disabled-keys` file and reported in the spool); a 403 passes through and is recorded on the key as a visible problem with the provider's message, never disabling it, so one bad request body cannot take out the keys one by one (owner decision); if none is left, `503 no-usable-key`.
*   **Reconcile** (Tavily first): every 10 minutes the coordinator asks the gateway's `GET /v1/usage` (loopback, not routed by Caddy, and bearer = the coordinator's per-process usage token whose hash is in the snapshot), which asks the provider per key (catalogue `usage: {path, field}`, Tavily `/usage` `key.usage`). The first answer is the key's baseline; later ones compare the provider's usage since the baseline with the credits recorded for that key's `tool_run` rows since then. A positive difference ("another client uses this key") shows on the key; a failed report shows as unknown, never as a match.

### 3. Gateway

*   **Code.** Copy Pipe's read lane (assetq `lane/`, Rust) into `crates/gateway`. It is the proven design: loopback socket, snapshot adoption every second with last-valid fallback, `LoadCredential`, disabled-key persistence, provider error mapping, spool and loss counter. Changes from the lane:
    *   route `POST /v1/call/<connection>/<tool>` instead of `/v1/read/<tool>`, generic over the provider catalogue;
    *   auth by per-run token hash from the snapshot instead of project tokens; no `X-Pipe-Task`;
    *   keys read from the secret directory by fingerprint instead of `LoadCredential`, so no restart on a key change;
    *   a re-enabled key comes back when the snapshot's `enabledAt` is later than the local disable;
    *   every reply carries `x-fleet-call-id`; a gateway refusal also carries `x-fleet-error` (a passed-through provider reply is `http-<status>` to the client);
    *   no fixed queue bound (the lane's `queue-full` is removed; a bound is added only when the owner asks for one);
    *   the lane's coreq telemetry store (`telemetry.db`, `coreq::enroll`, DashQ shipping) and the `coreq` dependency are removed; a new spool writer (below) replaces them;
    *   the alert path (`hermes`) is replaced by a needs-you question per cause (people-access §7), for example `key:<id>:disabled`;
    *   budget flags from the snapshot refuse calls with `budget-reached`;
*   **No database on the request path.** The gateway reads `snapshot.json` every second, and once more at once when a call carries a token hash it does not know, before refusing it with `unauthenticated` (this closes the gap between minting and adoption). At most one such reread runs at a time, so junk tokens cannot force a reread storm. The coordinator writes the file atomically on every change of tokens, access lists or key states. A missing or invalid file keeps the last valid one; `/healthz` reports its age. Schema:

    ```text
    {schema: 2, generated_ms, providers: {<id>: {baseUrl, tools}},
     connections: {<name>: {provider, tools, intake, keys: [{id, state, enabledAt?}], blocked: [<scope>]}},
     tokens: [{sha256, executionId, directedBy, projectId, experimentId, connections: [<name>]}]}
    ```
*   **Spool.** `<FLEET_GATEWAY_DIR>/state/spool/<day>.jsonl` (UTC day), append-only; a past day's file is deleted after the coordinator has ingested it. One JSON line per call: `{callId, tokenSha256, executionId, connection, tool, keyFingerprint, status, errorCode?, outcome: ok | error | unknown, usage: {unit, amount}?, disabled?: {key, status}, startedAt, ms}`. The coordinator ingests it every few seconds (and at step completion) into `tool_run` rows with `connection_cost`. Ingest is idempotent by `callId`; a persistent `lost` counter shows lines that could not be written.
*   **Reach.** Loopback `127.0.0.1:8742` on PDS18 (Pipe keeps `:8741` until C4). Other hosts call `https://fleet.ufobe.com/v1/call/...`: the Access app "Fleet gateway" (path `/v1/call`) admits only the `fleet-machines` service token, and Caddy sends the path to the gateway, which checks the run token.

### 4. Per-run tokens

*   When the start command is written (in `enqueue` from C1, in `dispatch` once people slice 6 lands), the coordinator mints a random token for the execution, stores its SHA-256 with `{executionId, directedBy, projectId, experimentId, connections}` and republishes the snapshot. The executor receives it in the start command (stored with the command row until the run ends) and writes it, with the gateway URL and the Access file path, to a private run file `<FLEET_DATA_DIR>/runs/<executionId>.env` (0600, in a 0700 directory), deleted when the run ends. Each connection MCP server gets that file's path as its last argument (`fleet-executor mcp connection <name> <spec> <run file>`) and reads `FLEET_RUN_TOKEN`, `FLEET_GATEWAY_URL` and `FLEET_CF_ACCESS_FILE` from it; the harness environment carries none of them.
*   **Threat model.** A token attributes calls per run. It does not isolate runs of one OS user from each other: under FD5 all runs of one harness account share an OS user, so one run can read another live run's environment (`/proc/<pid>/environ`) and call with its token until that run ends. On a held host, the holder can do the same. Both stay within that token's connections, and the holder already sees everything on their host. One OS user per run would close it (data foundation A15 rejected that for now).
*   The token's connections are the profile's granted connections filtered by each connection's `access` list and intake. `projects: ['*']` means all work, with or without a project (Lab runs have none).
*   The coordinator's connection tick (every 3 s) ingests the spool, then marks the tokens of ended executions revoked and republishes the snapshot, so recovery and every other ending path are covered; no separate expiry number. An `api` step's token is revoked right after its call. An execution that ends while the coordinator is down keeps its token until recovery settles it; the first snapshot after that removes it. A grant revocation removes tokens too (people-access §3). Child executions get their own tokens.
*   Coordinator-side `api` steps get a token for their step execution the same way.
*   **Owner connection tokens.** The owner's own interactive sessions (Claude Code, Codex, Hermes on any host) use a long-lived token from `connections_token_create {name, connections}` (owner only; the value is answered once, with the MCP spec). It lives in a 0600 file named by `FLEET_TOKEN_FILE`, never in a session config. The tick keeps it; `connections_token_revoke` ends it. Its calls are `tool_run` rows with no parent run and `data.tokenName`, directed by the owner.
*   This replaces Pipe project tokens, the grant snapshot, `pipeConfigPath` and the shared host token file.

### 5. Call methods

*   **HTTP:** `POST /v1/call/<connection>/<tool>`, body the tool's JSON, `Authorization: Bearer <run token>`. Replies copy the lane's table (`unauthenticated`, `bad-request`, `no-usable-key`, `key-disabled-<status>`, `provider-unreachable`, `provider-body`, `provider-timeout`; provider status passes through).
*   **CLI:** `python3 connection_call.py call <connection> <tool>`, body on stdin, one line `{tool, callId, outcome, error_code, usage, output}` on stdout. The file is `crates/executor/connection_call.py`, embedded in the executor by `profile.rs`; the Go coordinator makes pipeline calls in-process; Pipe is removed from it: no client library load, no hash pin, no label, token from `FLEET_RUN_TOKEN`. Error classification (`UNKNOWN` set, `infra:`/`unknown:`/`step:` problems in fleet-lab.md) stays.
*   **MCP:** `python3 connection_call.py mcp <connection> <spec>`, one stdio MCP server per connection; the start command's `connections: [{name, instructions, tools: [{name, description, inputSchema}]}]` gives the spec, so hosts need no connection files. The executor adds it to each run's MCP config and allowlists `mcp__<connection>__<tool>`, as it does for `connection_call.py mcp` today.
*   The local `.connection-calls.jsonl` file is deleted: the spool is the record, so an agent cannot edit its own costs.
*   **Built-in web** stays off unless the profile says `web_route: builtin` (fleet-lab.md R3.4).

### 6. Accounting and budgets

*   Every call: `tool_run` execution, parent = calling execution, `directed_by` and project copied from the token, plus a `connection_cost` measurement `{connection_id, amount, unit, basis}`. Basis `measured` when the provider returned usage, `estimated` when Fleet computed it from a price table, `unavailable` otherwise (same values as people-access §9).
*   Budgets use the shape in people-access §9 with `scope = connection` or with the connection named in a person, project or experiment budget. Spend reaches the coordinator through spool ingest, so a `pause` or `stop` budget can be overshot by the calls made during the ingest delay plus calls in flight, and without limit while the coordinator is down (the gateway keeps the last flags but cannot count). The coordinator computes reached budgets and writes a "blocked by budget" flag per (connection, scope) into the snapshot; the gateway refuses such calls with `budget-reached`, and keeps the last flags while the coordinator is down. Dispatch applies the same budgets to agent runs. `warn` never refuses.
*   Lab units `pipe_calls` and `pipe_credits` become `calls` and `credits`.
*   **As built (C3).** `budget_set` with `scope: connection` counts that connection's `tool_run` rows: unit `calls`, or the amount of the provider's own unit (`credits`), optionally per `period`. The action is a setting with default `warn` (owner rule outside Lab): `warn` and `pause` show on the budget and the person can ask for more; only a reached `stop` connection budget, set by the owner, becomes `blocked: ['connection']` in the snapshot on the next tick, and the gateway answers `budget-reached`. The gateway also refuses a run whose `person:`, `project:` or `experiment:` scope is listed; person, project and experiment budgets that name a connection are not built yet. `budget_ask {budgetId, limit, reason}` raises one question (`budget:<id>:ask`); only the owner's `approve` writes the new limit, audited with both people.
*   **Meshy (C3).** Catalogue tools `image_to_3d` (not idempotent), `job_status` (`GET .../{jobId}`: a `{field}` in a catalogue path takes that body field) and `recent_tasks`. The job step maps Meshy's task (`status`, `model_urls`, `consumed_credits`) to the job shape; the credits land as `connection_cost` when the task succeeds.

### 7. Deleting Pipe (stage C4)

One cut, no bridge:

*   **Fleet:** remove the Pipe parts of `connection_call.py`, the `pipe-read` record in `config/connections.example.json`, Pipe references in `pipelines.ts`, `projects.ts`, `executions.ts`, `lab/experiments.ts`, `lab/scores.ts`, `app.ts`, `artifacts.ts`, `store.ts`, `s3.ts`, `package-assets.mjs`, executor `profile.rs`, `harness.rs`, `main.rs`, `upload.rs`, `preview.rs`, `delegation.py`, and `packages/ui` registry and design check entries that name Pipe.
*   **Hosts:** remove the Pipe MCP server and `~/.config/pipe/read.json` on every host.
*   **PDS18 and Cloudflare:** stop and remove `pipe-read*` units, users and directories; remove the read-grant snapshot, project grants, the `pipe.ufobe.com` read route, its guard exemption and the read-lane probe; revoke Pipe project tokens.
*   **DashQ:** remove the shipper enrolment.
*   **assetq:** archive the repository (decision 8).

## Alternatives Considered

### A1: Gateway inside the coordinator

*   **Pros:** one process, direct DB access.
*   **Cons:** a coordinator restart stops every agent's web reads (brief M6).
*   **Reason for rejection:** a coordinator restart would stop every agent's web reads; the separate process with a snapshot is the lane's tested answer.

### A2: Keep Pipe as the gateway

*   **Pros:** works today.
*   **Cons:** two accounting paths (Pipe telemetry and Fleet `tool_run`), keys outside Fleet's list and budgets, a second repository to keep running, and the token-sharing hole.
*   **Reason for rejection:** the owner's scope extension in the brief (Overview, "Connections") and decision 8; the cons above are the technical reason.

### A3: LiteLLM or another off-the-shelf gateway

*   **Pros:** many providers.
*   **Cons:** model APIs only (no Tavily or Meshy shapes), its own DB and budgets, a second accounting path. fleet-lab.md already rejected it.
*   **Reason for rejection:** the lane already does the hard parts Fleet needs.

### A4: Caller labels (Pipe today)

*   **Reason for rejection:** labels are asserted by the caller; a per-run token is minted by the coordinator and names the execution, so attribution does not depend on the caller's honesty.

### A5: Round-robin keys

*   **Reason for rejection:** spreading load to stretch a quota is what provider terms forbid; Pipe's no-answer-only failover is proven.

### A6: One registry for subscriptions and API keys (brief's original option b)

*   **Reason for rejection:** brief, "Owner Decisions, Round 2", decision 1: separate lists and screens; the shared part is holder, access, intake and budgets.

## Risks & Mitigations

*   **Gateway or PDS18 down.** Every host loses API calls; agent runs continue without them and calls fail with `provider-unreachable` from the client. One gateway is the owner's decision 7; socket activation covers restarts. A second gateway is a future option.
*   **Stale snapshot.** The gateway serves the last valid one and `/healthz` shows its age; new tokens fail until the coordinator writes again, ended runs keep working tokens until then.
*   **Budget overshoot.** Bounded by ingest delay plus in-flight calls; unbounded during a coordinator outage (§6).
*   **Token borrowed by a same-user run.** See §4 threat model.
*   **Replay of a charged call.** Failover only for idempotent tools (§2).
*   **Spool loss.** A write failure increments the persistent `lost` counter, shown on the API keys screen.

## Non-Functional Requirements

*   **Security.** Key values only in the 0600 secret directory, the SOPS backup and gateway memory. Token hashes only in the snapshot. The run token is never written to the account user's `HOME` or to the run's output folder.
*   **Availability.** Gateway independent of coordinator and Postgres; socket activation keeps the port during restarts.
*   **Rollout.** C1 switches every Fleet call from Pipe to the gateway in one release; no call goes to Pipe after it. C4 removes what is left of Pipe outside Fleet's call path. No dual write, no bridge.

### Additional Resources

*   assetq `pipe.md`, `docs/read-lane.md`, `lane/`, `deploy/read-lane/`
*   [fleet-lab.md](fleet-lab.md) "Connections: one accounting path"
*   [fleet-data-foundation.md](fleet-data-foundation.md) Non-Goals (`connection_cost`), Q9

### Future Improvements

*   A person's own API key connection (holder ≠ owner).
*   Usage reconcile for Meshy and model APIs.
*   **Model API keys as harness endpoints.** The gateway as base URL for Claude Code (`ANTHROPIC_BASE_URL`, per-run token as bearer) and Codex (custom provider `base_url`), injecting the real key, so an API key with a budget can be a dispatch candidate. API keys only, never subscription OAuth. Needs a check that both harnesses accept it, and a dispatch change; a design of its own.

--------------------------------------------------------------------------------

## Detailed Implementation Plan

Stages run alongside the people slices.

| Stage | Content | Check | Needs |
| --- | --- | --- | --- |
| C0 Registry and keys | `api-connection`, `connection-key`, secret directory, SOPS/age backup, snapshot writer, API keys screen | the API keys list shows Tavily with holder and key states; restore from the backup alone works on a clean container | people slice 1 |
| C1 Gateway and tokens | `crates/gateway`, units, per-run tokens minted in `enqueue`, `connection_call.py` on the gateway (HTTP, CLI, MCP), spool ingest, `pipe-read` renamed `tavily` in config; every Fleet call leaves Pipe | coordinator stop does not stop web reads; spool ingested with loss 0; every call is a `tool_run` | people slice 4 |
| C2 Key lifecycle | rotation, disable on 401/402, 403 recorded on the key, owner re-enable, Tavily reconcile | rotation with no failed calls; a 401 key shows disabled with its reason | C1 |
| C3 Meshy and budgets | Meshy provider, `job` mode (submit then poll), connection budgets and requests | a Meshy charge lands as `connection_cost`; a `warn` budget shows and a request raises it | C1, people slice 6 |
| C4 Delete Pipe | §7 | the Pipe search in the Verification Plan is empty; old route fails; a Lab web trial completes | C1 |

### Target Files to Create/Modify

#### 1. `packages/coordinator/connections.ts` (Create), `packages/coordinator/app.ts` (Modify) — C0

*   `api-connection` and `connection-key` kinds; operations `connections_list`, `connections_create`, `connections_key_add`, `connections_key_state`, `connections_set_access`, `connections_set_intake` (architect operations). Key values and re-enabling a disabled key: owner only. Other changes: owner or holder.
*   `writeSnapshot()`: builds the §3 schema and writes `<FLEET_GATEWAY_DIR>/snapshot.json` atomically (temp file plus rename) after every committed change and at start. Without `FLEET_GATEWAY_DIR` no snapshot is written.
*   Seed: connection `tavily` (holder owner, access `{projects: ['*']}`) with today's Tavily keys moved from `/etc/pipe-read/tavily-keys` into the secret directory by `scripts/connections-import.sh` (Create); the coordinator registers them at start (§2).

#### 2. `packages/coordinator/providers.ts` (Create) — C0, C3

*   Provider catalogue `{id, baseUrl, tools: {<tool>: {method, path, unit, idempotent}}}`: Tavily in C0, Meshy in C3. Published to the gateway inside the snapshot.

#### 3. `crates/gateway/` (Create, copied from assetq `lane/`); root `Cargo.toml` (Modify) — C1

*   Copy `lane/src` and its tests; remove its own `[workspace]`; add `crates/gateway` to the root `members`.
*   Changes listed in Technical Plan §3: generic route, catalogue from the snapshot, token auth with coalesced reread, idempotent-only failover, budget flags, spool writer replacing the coreq telemetry store, no `queue-full`, coreq dependency removed.
*   Tests copied and adapted: snapshot fallback, unknown-token reread, disable on 401/402, 403 recorded without disabling, 429 pass-through, no failover for a non-idempotent tool, spool loss counter.

#### 4. `deploy/infrastructure/fleet-gateway.service`, `fleet-gateway.socket` (Create); `fleet.caddy.candidate`, `fleet-backup.sh` (Modify) — C0, C1

*   Units copied from `pipe-read.service` and `pipe-read.socket` (`LoadCredential`, `127.0.0.1:8742`).
*   Caddy route `/v1/call/*` for remote hosts behind the guard and the `fleet-machines` service token.
*   Backup: SOPS/age copy of the secret directory and the gateway's retained snapshot and `disabled-keys`.

#### 5. `connection_call.py` (Modify) — C1, now `crates/executor/connection_call.py`

*   Remove the record file, Pipe client loading, hash check and label. Entry points become `connection_call.py call <connection> <tool> [calls dir]` and `connection_call.py mcp`; the token comes from `FLEET_RUN_TOKEN`, the gateway URL from `FLEET_GATEWAY_URL`. Error classification (`UNKNOWN` set) stays. The local `.connection-calls.jsonl` is no longer written.
*   The file name stays, so `profile.rs:172` (`include_str!`), `package-assets.mjs:9` and `pipelines.ts:60,69` keep their paths; their arguments change.

#### 6. `packages/coordinator/tokens.ts` (Create); `app.ts`, `executions.ts`, `recovery.ts` (Modify) — C1

*   `mintRunToken(execution)` and `revokeRunToken(executionId)`. C1 calls `mintRunToken` in `enqueue` (`app.ts:129-143`) where the start command is written; people slice 6 moves the call into `dispatch.ts`.
*   Spool ingest every few seconds and at step completion, idempotent by `callId`, into `tool_run` plus `connection_cost`. Revocation on execution end and in recovery (§4).

#### 7. `crates/executor/src/main.rs`, `profile.rs`, `harness.rs` (Modify) — C1

*   `main.rs:428-440`: for each granted connection, add one MCP server `python3 <connection_call.py> mcp` with `FLEET_RUN_TOKEN` and `FLEET_GATEWAY_URL` in its environment only, instead of the record path and label. `FLEET_RUN_TOKEN` comes from the start command.
*   `profile.rs` tests (`:238-273`): connection name `tavily` instead of `pipe-read`.

#### 8. `packages/coordinator/pipelines.ts`, `lab/experiments.ts`, `lab/scores.ts` (Modify) — C1, C3

*   `api` steps call `connection_call.py call` with the step's run token; Lab units `pipe_calls`, `pipe_credits` become `calls`, `credits`; Meshy `job` mode in C3.

#### 9. Configuration and profiles (Modify) — C1

*   `config/pipelines/hello-search.json`, `search-basic.json`, `web-research.json` (including prompt text) and `config/agents/codex-researcher.json`, `claude-researcher.json`: `pipe-read` becomes `tavily`.
*   `config/connections.example.json`: the `pipe-read` record is removed; gateway layout added.

#### 10. Pipe removal (Modify/Delete) — C4

*   Files listed in Technical Plan §7. Delete `tests/coordinator/fixtures/pipe_client.py`; adapt `fake_connection.py` to fake the gateway.

#### 11. UI — C0-C3

*   `app/src/main.tsx`, `app/src/api.ts`, `app/src/lists.ts` (Modify): route `/keys` (list) and `/keys/<name>` (connection page; screen ids are one lowercase word): provider, holder, tools, keys (fingerprint, state), access, intake, spend, budgets, reconcile difference. Components from `packages/ui` (`state-badge`, `setting-field`, `kv-row`; state families `key` and `intake`); registered in `packages/ui/src/registry.ts`. `pnpm run check:design` clean.

#### 12. Docs (Modify)

*   `docs/designs/lab-brief.md` "Pipe in Fleet": superseded by this design.
*   `docs/designs/fleet-lab.md` Connections: points here; the Pipe token caveat is closed by per-run tokens.
*   `docs/designs/fleet-data-foundation.md` Q9 and the gateway non-goal: answered here.
*   `docs/connections.md`, `docs/configuration.md`, `packages/coordinator/README.md`: gateway and variables, no Pipe.

#### 13. Tests (Create/Modify)

*   Modify `tests/coordinator/connection_call.test.ts`: call lines against a fake gateway; no hash pin.
*   Create `tests/coordinator/connections.test.ts`: key add returns only a fingerprint; snapshot content; token mint, revoke, recovery revoke; spool ingest idempotence; budget flag.
*   `cargo test -p gateway`.

### Verification Plan

*   **Build:** `pnpm run build`, `cargo build -p gateway -p executor`.
*   **Tests:** `pnpm test`, `cargo test`, `python3 -m pytest deploy/infrastructure tests/executor`.
*   **Live (stage checks):** C0 restore from the backup alone on a clean container; C1 coordinator stop does not stop web reads, spool ingested with loss 0; C2 rotation with no failed calls, 403 disables a key; C3 a Meshy charge lands as `connection_cost`; C4 the Pipe search below is empty and a Lab web trial completes.
*   **C4 search:** `rg -i 'pipe[-_ ]?(read|lane|token|config|client|mcp)|pipeConfigPath|pipe\.ufobe|X-Pipe|pipe_search' -g '!docs/designs/fleet-connections.md' -g '!evidence/**' -g '!tests/coordinator/fixtures/documents-*.json'` returns nothing ("pipeline" does not match). This design records what was removed, and evidence files and migration fixtures are past records.

--------------------------------------------------------------------------------

*Drafted with EGM*
