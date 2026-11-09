# Prototype deployment evidence

Observed 2026-11-09. The real public web assignment, Mac execution, retained preview and same-native-session follow-up passed. The observed native stop defect was corrected and requalified on the deployed Mac executor. The bounded first-prototype acceptance path passed; limitations below remain.

## PDS-18

- `https://fleet.ufobe.com` routes through Cloudflare Access and Caddy to the dedicated Fleet coordinator on loopback 9470. The Fleet-only JWT verifier uses loopback 9471.
- Current release: `/home/fleet/releases/candidate-3af52968`, selected by `releases/current`. Uploaded archive SHA256: `3af5296808ac3ca16c0d7a125e95c292ba9499bd96d509c829e9f2a4b089881f`; remote verification matched. This identifies the built working-tree candidate, not a published Git release.
- Node 22.18.0 and production npm dependencies installed for Linux. Native SQLite import and an authenticated in-memory session check passed as the Fleet user, without a public listener or production test record.
- Dedicated user unit `fleet.service` enabled and running: MainPID 189617, NRestarts 0 at final readback. Caddy remained active after configuration validation and graceful reload.
- Browser proxy-auth mode is enabled: verified owner JWT yields a private upstream bearer; the page does not request or receive that credential. Machine routes retain independent exact-host bearer authentication.

## Mac

- LaunchAgent `com.ufobe.fleet.executor` is loaded and running. Current PID 42416, running, one launch and no exit since activation. Installed executable SHA256: `5e022086eec5ddf301f1cfee9497d13e9633887408669af8d19e880065ade8ac`, matching the checked build. The idle executor was replaced with the prior binary preserved. Initial immediate bootstrap failed while bootout settled; after confirming the old service and process were absent, bootstrap succeeded. Private settings are outside Git; executable and journals live under the owner's `.local/share/fleet-prototype`.
- Executor targets the public HTTPS coordinator with host identity `owner-mac`. Authenticated public polling returned a valid empty command list. Native execution through this public route is recorded below.
- The selected native model is `gpt-6.1-sol`, low reasoning, using the existing subscription. Earlier isolated native launch/resume proof and fake-process control/recovery evidence are recorded in [executor build](executor-build.md).

## Access checks

Anonymous public browser access returns 302 to Access. Anonymous exact machine POST returns 401. An authenticated machine poll succeeds. Guard fixtures and missing/forged JWT denials are documented in [infrastructure evidence](infrastructure-build.md); no public credential leakage was observed there.

## Real public session

The owner completed Cloudflare login. The assignment and follow-up were submitted through the visible web form, without injecting browser credentials.

- Session: `bf861c48-e2a6-40d7-8da4-3efad93d6e79`.
- Website execution: `4eafb177-b47c-446b-b424-0d38bb5d6931`, completed on `owner-mac`, `gpt-6.1-sol`, low.
- Follow-up: `502cf8ed-51e9-4975-abd1-51179dcdaa69`; native answer correctly recalled files, text and unverified browser checks. Final public API readback confirmed completed.
- Native thread for both: `01a1bdd7-49c8-73e0-b782-5b281efc40e8`.
- PDS retained `index.html` (370 bytes) and `style.css` (766 bytes). The preview renders Hello World and the requested subtitle with separate CSS applied.
- Actual authenticated preview screenshots: [desktop](../evidence/live-preview-desktop.png), [390px mobile](../evidence/live-preview-mobile.png). Both visually inspected, with no visible clipping. Parent document reported no horizontal overflow at 390px. Native Chrome failed to launch; visual acceptance came from the coordinator's real browser, not the worker.
- Coordinator restart and a new browser tab retained all three executions and results. Closing and reopening the test browser tab restored the selected website session, three completed executions, answers and results. The website session selection was restored before reopening; test sessions remain retained.
- Raw website usage reported input 148305, cached input 132096, output 1761. These are native-reported counters, not proven per-task billable deltas. Follow-up counters increased; do not sum resumed-session counters as independent task costs. Cost remains unknown.
- Resource observations cover native leader CPU/RSS and total executor-directory disk, not full process-tree or task-attributed disk/I/O. UI now keeps latest metrics compact and raw samples expandable.

## Native stop correction

Stop probe session `fd2b68b1-d1b1-40b2-be50-b0b3b3348071`, execution `084a6eba-ba06-4118-a781-90d11fe3d61d`, requested only `sleep 90`. Native PID 35738 (parent executor 13299) spawned sleep PID 35785 in a separate process group. The web Stop action produced `stopped` and removed Codex, but sleep survived with PPID 1. The parent removed only that exact probe child after verifying its command and creation time.

The original stop result failed process-tree acceptance despite its receipt. The updated executor tracks observed descendants with creation identities and macOS version-fenced signals, freezes/rescans owned lineage, and confirms tracked termination. Local separate-session regression also preserved an unrelated process; see [executor evidence](executor-build.md).

Deployed native requalification:

- Stop execution `2f692f6b-21d5-476b-aca5-7401ebfd89b9`: Codex PID 42567 spawned sleep PID 42613 in a separate process group. The visible Stop action produced `stopped`; both exact PIDs were absent at readback.
- Controlled failure `b5df45a4-0d2c-475e-ac59-d7b06bd91b3b`: after confirming ownership, parentage and creation time, the coordinator killed only probe Codex PID 43119. Executor removed separate-group sleep PID 43166 and reported `failed`. Both PIDs were absent at readback; no manual child cleanup was required.
- Final public API and browser readback: [session evidence](../evidence/live-session.json). The original website/follow-up stayed completed and retained.

## Remaining limits

This is a deployed working-tree prototype, not a published release. MCP, general agent configuration UI, automatic release delivery, comprehensive resource attribution and subscription cost are not implemented in this slice. Generated output is served in the authenticated Fleet preview, not on an independent public site domain.

Observed lineage is not kernel containment: a child reparented before any snapshot or after executor crash can remain unobserved. Crash reconciliation reports unknown rather than silently replaying work. Real executor-crash/network-outage containment was not requalified here; fake-process recovery evidence does not prove those native cases. UI tool entries retain native start/completion events separately, so an interrupted historical tool row may still say running beneath a terminal execution status.

No unrelated production service was restarted or migrated. Deployment did not commit or push the working-tree implementation.
