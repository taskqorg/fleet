# First prototype launch

Execution scope: a person submits work through a minimal web session at `fleet.ufobe.com`; Fleet runs Codex with `gpt-6.1-sol`, low reasoning, on the owner's Mac; the generated static website is retained and served from PDS-18; messages, execution history, results and available measurements remain in the originating web session. Follow-up conversation uses the same session and relevant native context.

## Placement

- PDS-18: dedicated Fleet service identity, loopback coordinator, local database and retained artifacts; existing Caddy provides HTTPS. Preserve other services and their access policies.
- Mac: separately owned Fleet executor, current subscription-authenticated Codex, isolated task working areas, outbound authenticated coordinator connection.
- Browser: minimal real session interface with compose, history, progress, results and observed usage. Full operator/architect panels remain later work.
- Generated preview: static output on PDS-18 with isolation from the control interface and no coordinator credentials available to generated content.

Provision exact Fleet routes and service credentials only. Preserve shared rules requiring origin/browser authentication and scoped machine access. No secrets enter Git, artifacts or session history. Check DNS and Access administration before public activation; hostname resolution alone does not establish correct routing.

## Integration contract for the first build

This is the common starting contract for parallel implementation. Coordinating workers may refine it together and update this file; neither side silently changes it.

- `POST /api/sessions` creates a session; `GET /api/sessions/:id` returns its messages, executions, results and measurements.
- `POST /api/sessions/:id/messages` accepts `{requestId, text}` and returns `{sessionId, executionId, state}`. Duplicate request identities must not duplicate work. Retain follow-up native session association.
- `POST /api/executions/:id/stop` records a stop request; expose requested versus observed stop.
- `POST /api/hosts/poll` accepts `{hostId, capabilities}` and returns `{commands: [...]}`. Each command has `{id, executionId, sessionId, kind, payload}`; kinds initially `start`, `input`, `stop`.
- Start payload contains `{text, model, effort, instructions, nativeSessionId?}` with coordinator-resolved inputs. Working-area ownership remains executor-local, not an arbitrary path supplied by a browser.
- `POST /api/hosts/receipts` accepts `{hostId, commandId, executionId, status, nativeSessionId?, error?}`. Status distinguishes accepted, started, completed, failed, stopped and unknown.
- `POST /api/hosts/events` accepts `{hostId, executionId, events: [{id, seq, type, timestamp, data}]}`. Retain raw/native detail where available and deduplicate event IDs.
- `POST /api/hosts/artifacts` accepts authenticated `{hostId, executionId, path, mediaType, contentBase64}` for the bounded first static exercise. Validate relative paths and deployment-configured sizes, retain content durably, and return a result reference. Streaming can replace base64 without changing result ownership.

Browser authentication and a distinct machine bearer credential protect their respective routes. Host identity must match its credential; execution ownership must be checked. No browser receives the machine secret. All mutations return stable identities and explicit errors. Exact schemas and response shapes belong in shared fixtures during implementation.

## Acceptance

Exercise the actual website: create a session, submit the Hello World assignment, observe execution on the Mac and its retained history, open generated output served by PDS-18, then send a follow-up and observe the answer in the same session. Retain browser evidence and actual native identity/model/usage where available. Verify client close/reopen, an owned stop and one controlled failure separately. Missing metrics remain unknown.

Local builds, contract tests, fake execution and health checks are preparation, not substitutes for this real path. The final report identifies deployed revision, service state, session/execution IDs, preview link, evidence and remaining gaps.
