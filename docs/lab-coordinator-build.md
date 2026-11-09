# Laboratory coordinator candidate

Stage 2 of lab-first-experiment.md. No native launch, deployment, service restart, runtime/profile/schema edit, commit or push was performed. Existing records remain intact; the SQLite records table is reused without a destructive migration. Serena bootstrap was called using the installed Python MCP client and scoped stdio server; initial_instructions completed and that client/server scope closed without global configuration edits.

## Operator path

Open /lab after owner authentication. Submit the shared website task once. The two trusted repository profiles are baseline and design-rules; the latter combines MCP and its usage instructions as one variant. Both trials receive byte-identical task text, fresh Fleet sessions/executions and no inherited native session identity. One durable transaction records the experiment, requested configuration snapshots, user messages and both commands before acknowledgement. The second command is withheld by the existing poll handler until the first execution is completed, failed or stopped; unknown remains unresolved and does not trigger replay. There is no new scheduler or automatic retry.

A stable requestId deduplicates pair creation, including browser retry/reload with retained task text; changed content or profile order under that identity conflicts. To intentionally create another experiment with identical text through the API, supply a new requestId. UI retains its request identity for the same text to prevent repeated clicks launching another pair.

Comparison shows the selected two retained executions, changed requested fields and complete snapshots/hashes, receipt timestamps and duration, user/assistant text, expandable complete tool calls/results, preparation evidence, raw usage observations and latest resource coverage. Requested configuration, preparation/native mapping and observed named MCP call/result are separate evidence. Missing preparation, usage, cost or resource attribution remains unknown; counters from resumed native turns are never summed into a cost claim. Full session links open each retained session and all its events/results. Desktop previews are side by side; mobile previews are stacked and sandboxed with no same-origin permission. Preview frames remain intact when refreshing unchanged or updated comparison evidence. Existing ordinary session UI and follow-up remain available.

Notes persist on the experiment, along with optional selectedExecutionIds for any two retained executions; opening the experiment restores the selected comparison. A different pair can be selected through the two execution selectors. One pair is not statistical evidence of superiority. Source heading/palette checks and actual native application remain parent evidence.

## Endpoints and authorization

All new API routes use the existing browser bearer boundary, not machine credentials. In FLEET_BROWSER_PROXY_AUTH=true, /lab HTML also requires that bearer supplied by the verified guard/Caddy path. Browser mutations require the exact FLEET_BROWSER_ORIGIN; missing/wrong Origin refuses. No new identity header is trusted. Existing exact four machine POST routes and their credentials/host checks are unchanged. Parent must retain owner Access/origin protection for /lab and /api/*; do not create public bypass rules for new browser endpoints.

- GET /lab: comparison UI; proxy mode receives no bearer in HTML or JS.
- GET /api/lab/profiles: validated repository profile contents.
- POST /api/experiments: exactly `{requestId,text,profileIds:["baseline","design-rules"]}`; returns the experiment and stable trial/session/execution references. No executable, environment, URL, native flags or arbitrary profile paths are browser inputs.
- GET /api/experiments: retained experiment list.
- GET /api/experiments/:id: experiment, note, original trials and optional selectedTrials with their snapshots/messages/events/result metadata.
- POST /api/experiments/:id/notes: exactly `{note}`; returns stored experiment for readback.
- POST /api/experiments/:id/comparison: exactly `{executionIds:[left,right]}` with two distinct existing execution IDs; retains selection and returns trial detail.
- GET /api/lab/executions: retained execution choices, including old ordinary executions.
- GET /api/lab/compare?left=...&right=...: read-only comparison of any selected two retained execution IDs.

Body/identity validation, typed record lookup and existing auth/origin hooks apply. Server resolves only the two trusted profile IDs and rejects unsupported profile keys/model/effort/grants and size violations. Full snapshots and profile SHA-256 are stored on each trial execution; canonical serialization recursively sorts object keys in UTF-8 order, preserves array order and uses compact UTF-8 JSON matching the current Rust profiles. A configured follow-up resolves the same trusted repository profile ID anew and retains that invocation's snapshot/hash; previous executions remain unchanged. Specific old-home native resume limitations remain executor errors, never a reason to delete retained history.

## Build and release assets

Use Node 22.18.0 and existing npm dependencies. `npm run build` compiles TypeScript and copies the two profiles into dist/config/agents plus contracts/lab-config.json into dist/contracts. Release the complete dist tree and normal package.json/lockfile/node_modules dependency installation, not just compiled JavaScript files. Compiled server.js defaults FLEET_PROFILES_DIRECTORY to its installed dist/config/agents directory via import.meta.url, independently of WorkingDirectory. Optional FLEET_PROFILES_DIRECTORY must be a trusted server-owned directory with baseline.json and design-rules.json. Existing PORT, database, host, browser/machine secrets, proxy auth, Origin and artifact-bound settings remain unchanged. No new service/provider/dependency is required.

Current canonical profile hashes:

- baseline: 9d23f7fde668430ab9d50cb6db6fdcb3d92523b51f21c820f9e9d270825e8fd2
- design-rules: 34c2bf91daf333a39d3c23afc95651111310b2d15c58d08c30942ae6808943d3

The baseline digest is checked against an independently computed Python sorted compact JSON value. Both correspond to current repository profiles; future edits affect only new invocations.

## Local evidence and limits

`npm test`: five tests passed, including prior persistence/identity/auth/follow-up tests and focused experiment tests. `npx tsx --test tests/coordinator/lab.test.ts`: two affected tests passed after final follow-up changes. They cover exact schema, trusted selection, auth for all lab endpoints including HTML, mandatory Origin on every new mutation, machine/browser separation, independent sessions, stable dedup/conflict, sequential dispatch, snapshots surviving repository edits/restart, notes/selection readback and updated configuration on follow-up. `npm run build`, `git diff --check` pass. `npx tsx tests/coordinator/lab-packaged.ts` starts the compiled coordinator from a temporary unrelated cwd, reads packaged profiles and checks protected /lab; the scoped child exits afterward.

`npx tsx tests/coordinator/lab-browser.ts` exercises fake executor pair dispatch, realistic preparation/call/result/usage records, HTML with relative CSS, comparison refresh preserving preview DOM, note save/reload, duplicate create after reload and opening both full session histories. It checks desktop/mobile overflow and captures evidence/lab-desktop.png, evidence/lab-mobile.png, evidence/lab-mobile-baseline.png, evidence/lab-mobile-configured.png and evidence/lab-browser.json. Desktop and individual mobile preview screenshots were visually inspected: readable evidence columns and note, correct side-by-side/stacked presentation and rendered previews. The full-page mobile capture can omit offscreen iframe painting; individual mobile viewport captures show each real rendered frame.

This is deterministic/fake browser evidence, not a native experiment or superiority claim. Parent owns deployment, actual public pair, context-independence readback, named real tool/result, source checks and final operator acceptance. Refresh is explicit through Load history; no background native execution is inferred from UI health. First static preview supports same-directory relative assets; external resources/module graphs/dynamic servers remain outside this slice. No LLM judge or optimizer was added.

Final compiled SHA-256 readback:

- dist/packages/coordinator/lab.js: d70a451d645d0a4ded2a02a19b8f03b91f600fcf0166e3efac4db6eb80c314c7
- dist/packages/coordinator/lab-ui.js: 8aad2d650bafbb8ab1e2c452d82d8632a1c3639eab011abccd67498bfb8bbb5e
- dist/packages/coordinator/app.js: d94b4171d29c7c4dc169860df07be5c617d212d502309302ca74b1559815573b
- dist/packages/coordinator/server.js: 551afb0ba34d126064d8ccb23c67bfdb54b1bc7bd71491d999dc2d0055471ea4

The full-history browser check found and corrected the local login-page exemption for /?session=...; both history links now actually open and read retained assistant/raw events in the test, not merely exist as hrefs. Final fake lab browser, focused lab API tests, compiled packaging check, build and diff-check all passed after that correction.
