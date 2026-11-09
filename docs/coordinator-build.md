# Coordinator build and integration

## Local operation

Use Node 22.18.0. Run `npm ci`, `npm run build`, `npm test`, then `npm start` with distinct random FLEET_BROWSER_SECRET and FLEET_MACHINE_SECRET supplied outside Git. FLEET_DATABASE selects an owned persistent SQLite file (default var/fleet.sqlite); FLEET_HOST_ID selects the exact authorized executor (default owner-mac). Open http://127.0.0.1:8787 and enter the browser secret. The browser retains its session ID in localStorage; its credential remains in memory. The machine credential never reaches the browser.

## HTTP and persistence

Implemented operations are POST /api/sessions; GET /api/sessions/:id; POST /api/sessions/:id/messages; POST /api/executions/:id/stop; POST /api/hosts/poll, /receipts, /events, /artifacts; GET /api/results/:id. Session messages accept exactly requestId and text, retain server-generated identities, and deduplicate within their session; changed text under the same request identity conflicts. Every lookup checks its record kind. One execution may remain active per session. Follow-up start payloads retain the observed native session association. Instructions follow the requested discussion, review or creation scope; website creation delivers output/index.html with all source assets.

SQLite WAL with synchronous FULL retains commands and messages transactionally before acknowledgement; history has no expiry. Commands remain pending until receipt. Terminal receipts cannot regress and delayed receipts cannot overwrite terminal execution state. Events reject changed identity content and reused per-execution sequence. Session summary and GET /api/sessions/:id/events?after=-1&limit=100 use durable insertion order; cursor is a zero-based insertion position, maximum page size 500. GET /api/executions/:id/events returns per-execution sequence order. UPSERT preserves record insertion positions; identical event retries do not rewrite records. Session summary currently returns full retained history; storage queries scan records in this small prototype.

Artifacts retain immutable execution/path identities, media type, full content, size and computed SHA-256; optional producer digests are validated. Changed content under the same identity conflicts. FLEET_MAX_ARTIFACT_BYTES sets the decoded per-file upload bound (default 16777216); HTTP body allowance follows it. Storage currently uses base64 SQLite records rather than the planned retained-file store. Source artifacts are retained without default expiry.

## Preview boundary

Preview fetches retained HTML and assets through authenticated control requests and substitutes relative resources as data URLs inside opaque sandbox srcdoc, permitting scripts without same-origin privileges. Same-directory HTML CSS/script/image resources and CSS-relative images are supported. External resources, module import graphs, nested HTML navigation and dynamic backends remain outside the first static exercise. Executor must upload all source files. Unchanged session snapshots do not rebuild frames; preview frames are cached. Generated content has no coordinator bearer or same-origin access.

## Local evidence

`npm test` passes two integrated tests covering persistence, request dedup/conflict, caller-controlled identity rejection, typed record boundaries, requested-scope instructions, follow-up history insertion order, restart, cursor paging and per-execution sequence order. `npm run build` and `git diff --check` pass. The earlier `npx tsx tests/coordinator/browser.ts` Chromium run exercised actual Russian UI submit, fake executor events, event/sequence conflicts, terminal receipt regression, artifact retry/conflict, relative CSS preview, reload, native-context follow-up, stop request and observed stopped receipt. Evidence is evidence/coordinator-browser.json and evidence/coordinator-browser.png; screenshot visually inspected with all relevant elements visible at 1100px width. UI was unchanged in the subsequent identity/order patch, so browser qualification was not repeated. This is local fake execution evidence, not native or remote deployment proof. Latest dependency audit reported zero vulnerabilities with pinned Fastify 5.12.8.

## Deployment integration

Infrastructure owns final runtime, service and Caddy setup; no production activation occurred in this worker. Use Node 22.18.0 and dist/packages/coordinator/server.js, bind loopback with PORT=9470, FLEET_DATABASE=/home/fleet/data/fleet.sqlite, FLEET_HOST_ID=owner-mac, FLEET_BROWSER_ORIGIN=https://fleet.ufobe.com, distinct FLEET_BROWSER_SECRET/FLEET_MACHINE_SECRET, and configured FLEET_MAX_ARTIFACT_BYTES. Keep secrets in the protected external environment file. Explicit public origin configuration supports HTTPS reverse proxy without trusting forwarded origin headers.

Infrastructure prepares the separate Fleet-only Access JWT verifier on 127.0.0.1:9471; coordinator does not duplicate it. Browser Caddy routes must pass that verifier; exact POST machine routes use the separate machine Access applications and coordinator machine bearer. Coordinator independently checks exact host/execution ownership. Parent must verify Access/origin guard before activation; these integration instructions do not establish that it is active.

Coordinator agreed native file/shell/browser tools plus Fleet HTTP operations suffice for the first web-session-first slice. Agent-facing MCP is explicitly deferred, with no qualification claim or general architecture change. Parent/executor still own actual Codex execution, observed native model/usage, remote retained output and deployed browser acceptance. No commit or push performed.

## Readable session UI acceptance

UI now groups user messages and native `item.completed` / `agent_message` answers chronologically by execution. Native tool status, observed turn token usage and resource observations have compact summaries; absent usage and cost remain unknown. All raw events remain available in collapsed per-execution details. Results include an Open result action in a modal opaque sandbox: generated HTML receives neither same-origin privileges nor shared credentials. Result frames remain outside conversation rerenders.

The targeted Chromium scenario uses realistic native-shaped assistant, tool, usage and unknown resource events followed by a user follow-up. It checks raw details start collapsed, relative CSS works, Open result uses `sandbox=allow-scripts`, and preview DOM state survives both unchanged polling and changed conversation snapshots. Screenshots evidence/coordinator-browser.png (1100px desktop) and evidence/coordinator-mobile.png (390px mobile) were visually inspected for readable conversation, result action, preview and composer; mobile has no horizontal page overflow. This UI-only change requires no API or auth/deployment changes and was not deployed.

## Production proxy browser authentication

Set FLEET_BROWSER_PROXY_AUTH=true and FLEET_BROWSER_ORIGIN=https://fleet.ufobe.com for the owner-only Access deployment. UI hides the browser key input, emits no Authorization header, uses same-origin cookie-authenticated requests and automatically reloads its saved session. Without a saved session the user creates one with New session. Direct bearer UI remains the default for local/native tests.

Coordinator still requires the existing FLEET_BROWSER_SECRET bearer on browser API routes. The dedicated JWT guard must validate Access first and return Authorization: Bearer with that secret for Caddy to copy upstream; the secret must never enter the HTML or browser response. Arbitrary identity headers do not authorize anything. Proxy-mode browser mutations require the exact configured Origin, including rejecting missing Origin; authenticated GET reads may omit Origin. Machine bearer/host scoping is unchanged. Infrastructure owns the guard/Caddy change; this worker does not deploy it.

`npm test` covers missing/wrong Origin mutation rejection, correct Origin acceptance, mandatory bearer, untrusted identity header rejection and GET without Origin. `npx tsx tests/coordinator/proxy-browser.ts` simulates a trusted proxy injecting Authorization outside browser JS and checks hidden key input, session creation, message submission and automatic reload. Screenshot: evidence/coordinator-proxy-browser.png. This is a local simulation; Cloudflare native login was not tested here. The direct-bearer Chromium test remains the regression check for local mode.

## Resource stream readability correction

Execution conversation nodes now update incrementally rather than being recreated on polling. Each execution shows only its latest resource sample in one summary: CPU percent and RSS MiB for the native leader, disk MiB for the entire executor directory explicitly not attributed per task, and unknown cost. All samples remain in raw event details. Tool command/status labels are capped at 160 characters; complete tool records and output remain expandable. Existing assistant/user nodes, expanded details, preview frames and page scroll are preserved across updates. No API change, deployment or restart is part of this correction.

The targeted browser scenario adds 100 realistic resource samples to assistant/tool/follow-up history and checks one latest summary, all 105 raw events, retained conversation node identity, expanded details and scroll position, together with existing preview DOM checks. Desktop/mobile evidence remains evidence/coordinator-browser.png and evidence/coordinator-mobile.png.
