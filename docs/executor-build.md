# Mac executor candidate

Build: `(cd go && CGO_ENABLED=0 go build -o bin/fleet-executor ./executor)`.
Checks: `(cd go && go test ./executor)`.

Run locally against a local coordinator:

```sh
FLEET_COORDINATOR_URL=http://127.0.0.1:8787 FLEET_HOST_ID=mac FLEET_DATA_DIR=/absolute/owned/fleet-data go/bin/fleet-executor
```

Only `FLEET_HOST_ID` is required. Release defaults ([self-update §5](designs/fleet-self-update.md#5-config-defaults-and-env)): `FLEET_COORDINATOR_URL` `https://fleet.ufobe.com`; `FLEET_DATA_DIR` `~/.local/state/fleet/executor` (Linux) or `~/Library/Application Support/Fleet/executor` (macOS); `FLEET_HOST_SECRET_FILE` `~/.config/fleet/host-secret`; `FLEET_STORE_ENDPOINT_HOSTS` from `config/executor-defaults.json` of the release; `FLEET_CODEX_BIN` `codex` on `PATH`. Env values override them. The secret file stays outside Git and is never printed. Service units: [deploy](../deploy/README.md).

## Native selection

Inspected AgentQ revision `7c48127cd952d7b001207575f7c2ce7889c1ef51`, runtime-consumer example and `runtime/src/agent/registry/codex.rs` mapping/native_args. Its exec JSON/resume, explicit model/effort, subscription, denied approvals and isolated multi-agent settings informed this implementation. No AgentQ source copied. AgentQ library brings account/session-store/preflight and CoreQ/SQLite/ring closure; this candidate uses installed Codex CLI 0.167.0 directly for a bounded sequential turn path. Exact Rust dependencies are in Cargo.lock. Native command construction remains in the executor candidate and must be isolated into an adapter before extension.

Fleet stores command acceptance before spawn and preserves duplicate/conflicting command identities. SQLite WAL/FULL stores events and upload obligations. Native thread identity is retained per Fleet session; follow-up resumes that identity in its original workspace. Only executor-derived session directory names are accepted. Caller instructions materialize as AGENTS.md. Only output/ regular files are uploaded, symlinks skipped. Coordinator owns serving; no SSH deployment exists.

## Evidence and limits

2026-11-09: cargo build and two deterministic tests passed (path refusal; duplicate/conflicting identity prevents repeated dispatch). These tests do not qualify real transport or crash recovery.

One authorized bounded native probe succeeded: thread `01a1bdc6-4937-7211-839e-dfcba0871b67`, launch completed, follow-up returned retained marker FLEET_PROBE_731. Requested model gpt-6.1-sol, effort low, ChatGPT login forced; read-only sandbox, no tool/file/public side effects requested. Raw local evidence directory: `/var/folders/sw/55r1dvwn2g30l77zs5nwp3_80000gn/T/fleet-native-probe-ysyj5f_0`. This proves CLI launch/resume only, not full executor integration or loaded instruction/tool enforcement.

## Current local acceptance

The candidate now runs native stdout ingestion separately from outbound polling. The event loop retains each event and upload obligation in WAL/FULL SQLite before transmission; HTTP failures leave the same JSON payload queued. HTTP exchange has a five-second operation timeout; polling and uploading can delay each other during failed HTTP calls, while native processing continues. Poll cadence is 100ms plus actual exchange time. Host exchange remains outbound only.

Native mapping is isolated in `go/executor/harness_*.go`. Environment is cleared, then only HOME/PATH/USER/LOGNAME/TMPDIR/CODEX_HOME/LANG/LC_ALL/SSL_CERT_FILE/SSL_CERT_DIR are passed. Fleet credentials and API keys never enter child environment. User config and execpolicy rules are ignored; no account/settings/trust repairs occur. Subscription home remains unchanged. No MCP tools or skills are granted in this website slice. Native shell is workspace-write, network off, approval denied; apps/plugins/browser/computer/image-generation and multi-agent are disabled. Preparation records instruction SHA256 and explicit grants. Actual instruction loading and sandbox enforcement still require the supervised website run.

Stop tracks observed descendants by parent linkage plus native creation identity, independent of process group/session. The executor snapshots lineage during each tick and again on stop. It freezes identity-bound observed processes, rescans until no additional descendant is discovered, then signals those exact incarnations with SIGKILL. On macOS the signal uses the CoreQ-derived proc_pidinfo unique-version plus proc_signal_with_audittoken kernel fence; reused PIDs cannot retarget the signal. No name, arbitrary PID or process-group-only kill is used for native cleanup. A native leader exit triggers the same cleanup even before stdout EOF, so an orphan retaining stdout cannot block cleanup. Tracked identities survive reparenting within the running executor.

Observed stop is emitted only after leader exit, stdout drain and absence of live tracked descendants. Zombies are terminated, not live survivors. Snapshot/signal uncertainty produces unknown rather than stopped. Normal executor shutdown uses the same descendant cleanup. Abrupt executor death still requires reconciliation; no automatic restart-time kill or relaunch is introduced.

Exact limitation: this is observed lineage coverage, not a kernel containment facility. A descendant that is created and reparented before any snapshot can remain unobserved, especially if the leader exits between polls or the executor crashes. Snapshot access failures remain uncertain. The routine handles ordinary native tool children in separate groups/sessions, including the observed real sleep case; deliberate daemon escape and post-crash containment are not proven. HTTP exchange can delay process observation by its operation timeout. Native stop/failure must be rechecked by the parent with the updated binary; no paying native call or deployment occurred in this patch.


An advisory flock on executor.lock prevents a second executor using the data directory. Restart emits unknown for every unfinished accepted command and preserves native session identity; it does not replay native launches. This intentionally requires operator reconciliation if a native process survived an executor crash. Spawn failure emits a durable failed receipt. An accepted stop is retained as nonterminal until observed or reconciliation unknown.

Resource events include native-leader ps CPU percentage (lifetime average), leader RSS bytes, and allocated disk bytes for the entire executor data directory. They explicitly exclude child-tree attribution; I/O and subscription cost remain null/unknown. Sampling starts after two seconds, so short turns may have no resource sample. Native usage JSON remains separate raw evidence.

Static uploads accept relative safe path components and declared static extensions, skip symlinks and refuse a symlink output root; files are opened no-follow and capped at FLEET_MAX_ARTIFACT_BYTES (default 16777216 decoded bytes). MIME and SHA256 accompany retained bytes. Coordinator validates integrity and immutable execution/path identity. Website instructions must require output/index.html; files elsewhere are retained locally but not uploaded. Native output may be untrusted; coordinator owns preview isolation.

Raw native JSONL is also fsynced into the owned session directory before event delivery; restart retains it for reconciliation without claiming automatic native history ingestion.

Local proof command: `(cd go && go test ./executor)`. Until the Go port (2026-11-11) the proof was the Rust tests plus `tests/executor/fake_integration.py` (deleted with the Rust executor after it passed against the Go binary). That fake integration used a real HTTP server and owned fake process group; it checks events before exit, stop plus descendant termination, singleton rejection, failed-upload retry preserving payload and SHA256, completed turn, abrupt executor crash and restart unknown without replay, accepted-before-spawn recovery, missing executable failure, secret exclusion, and resource events. This does not substitute for the supervised native website acceptance.

No launchd activation, remote deployment, model/account changes, extra native calls or global hooks occurred. Remaining supervised gates: actual website tool/instruction enforcement, coordinator/PDS integration, preview rendering and restart/reconnect on deployed services. Native direct launch/resume evidence above remains the sole real-model probe; it was not repeated after changes to environment/isolation mapping.

Final fake integration evidence directory before the private-directory mode change: `/var/folders/sw/55r1dvwn2g30l77zs5nwp3_80000gn/T/fleet-fake-v748ngf1`; executor data directory is forced to mode 0700 at startup.

## Descendant cleanup regression

2026-11-09 focused checks: locked Cargo tests (2 passed), locked debug build, and fake HTTP/child integration passed. Fake child now launches sleep with start_new_session=True. Explicit stop and abnormal native exit both leave that separate-group sleep absent or zombie; an independently launched unrelated sleep remains alive. The existing scenario still checks live history, acceptance, singleton, upload retry/digest, restart uncertainty and environment exclusion. Evidence directory: `/var/folders/sw/55r1dvwn2g30l77zs5nwp3_80000gn/T/fleet-fake-a68bstib`. Earlier group-only stop evidence did not qualify separate-group native tool children and is superseded by this regression.

Final rerun evidence: `/var/folders/sw/55r1dvwn2g30l77zs5nwp3_80000gn/T/fleet-fake-2y8kii0e`. Debug executable SHA256: `5e022086eec5ddf301f1cfee9497d13e9633887408669af8d19e880065ade8ac`. No installed/live binary was replaced.

## Releases

Release files and updates: [executor delivery](designs/fleet-executor-delivery.md), [self-update](designs/fleet-self-update.md).
