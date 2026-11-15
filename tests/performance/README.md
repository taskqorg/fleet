# Interface qualification

`seed-interface.ts` produces synthetic fixture format1/seed1 and an independently enumerated manifest. `interface.ts` runs an isolated coordinator/PostgreSQL and a separate Chromium driver. A fixture, a successful development smoke, a release or CI success is not capacity/live acceptance. The required product behavior is in [prepared project resources](../../docs/designs/fleet-analytics.md#prepared-project-resources).

## Safety and prerequisites

Use task-owned disposable directories/processes, never a production database/origin or another project's data. `serve` creates its own database using `tests/harness.ts`; its PostgreSQL data/socket directory must be inside the specified output directory. Use a short absolute path: Unix socket paths have a platform length limit. Each case needs a fresh server/cluster. The server binds only loopback. Reach it through an SSH tunnel on the separate browser machine; the browser driver rejects non-loopback origins. Its proxy supplies only generated fixture credentials, which never enter output manifests. No agent/model/executor process is launched.

Server: Linux, PostgreSQL15+ binaries on PATH (`initdb`, `pg_ctl`, `psql`), `pg_stat_statements` library, Node22.18+, installed repository dependencies, built candidate coordinator and app. PostgreSQL starts with `track_io_timing`, lock-wait logging and `pg_stat_statements`; `fleet`/`fleet_app` remain non-superuser roles with normal RLS. Only the disposable cluster's local admin enables statistics access. The normal coordinator exporter writes real spans to the fixture's local JSONEachRow capture sink; this does not qualify ClickHouse. No production observability/credential file is read.

Browser: separate reserved Linux machine, installed Playwright Chromium and system libraries. Bound its process resources and preserve host headroom. One context/page per reader, headless Chromium with background throttling disabled, and a verified visible document. No heavy workload on the owner's Mac. `--development` permits only a <=618-row/one-reader/<=3-second smoke and records it as development.

Keep all output under ignored `.egm/` or task-owned remote scratch. The canonical files contain procedures and fixture definitions, never reports or screenshots.

## Placement declarations

Create a private working `server-placement.json` (no credentials):

```json
{"isolatedProfile":true,"vcpu":4,"memoryGiB":8,"ssd":true,"reservation":"task-owned reservation identity","effectiveControls":"observed cgroup paths, cpu.max, memory.max","topology":"actual host/VM/VHDX and physical SSD backing; disclose shared hardware"}
```

Browser `browser-placement.json`:

```json
{"separateMachine":true,"reservation":"exclusive browser reservation identity","effectiveControls":"observed browser cgroup CPU/memory limits","restore":"exact previous idle Fleet-only runner states and restoration command"}
```

Declarations never substitute for readback. `server.json` and `browser.json` record actual CPU/RAM, disk/mounts, cgroup, pressure, PostgreSQL, Chromium, source/diff/binary/bundle identities and network conditions. The original reference is dedicated4-vCPU/8-GiB/SSD Linux plus a separate browser at80ms totalRTT/10Mbps. A reserved cgroup on physically shared hardware or WAN RTT above80ms is an actual alternate profile, not that exact reference; keep original profile coverage untested unless observed. Do not relabel virtual `ROTA=1` as physical HDD or declare SSD without backing evidence.

For the authorized WSL alternate profile, wrap the WHOLE server process tree, including PostgreSQL, in `systemd-run --user --scope -p CPUQuota=400% -p MemoryMax=8G`; read its effective `cpu.max` and `memory.max`. No global WSL settings/shutdown or unrelated service changes. An idle Fleet-only CI guest may be reserved for browser only after confirming runner scope/idle jobs and hosted routing fallback, preserving/restoring its prior state. Never run benchmark coordinator/database load on the production host/CI guest.

## Run

Build the exact candidate and retain its identity before copying artifacts to test machines. Go code is unchanged by this runner; a published coordinator may be reused only with verified release-commit/digest identity. Build the candidate app for the default-selection change. Record uncommitted diff identity for development; final qualification must use the accepted exact candidate and its actual binaries/bundle.

```sh
node tests/performance/seed-interface.ts --history=18794 --projects=5 --out=.egm/manifest.json
# Server machine, inside its reserved cgroup; choose a fresh output path/port for each case.
FLEET_TRIAL_PGDATA="$PWD/.egm/case01/pg" FLEET_TRIAL_PGPORT=54379 \
  node tests/performance/interface.ts serve --out=.egm/case01 \
  --placement=.egm/server-placement.json --history=18794 --projects=5 --port=9479
# Browser machine: existing authorized SSH path forwards local9479 to test server loopback9479.
node tests/performance/interface.ts browse --origin=http://127.0.0.1:9479 \
  --out=.egm/case01-browser --placement=.egm/browser-placement.json \
  --readers=10 --rate=20 --network=unshaped
```

The server first seeds with processor off, then actually restarts it on, waits for complete ready/current summaries, and compares stored aggregates to the independent manifest. It adds a running marker actor per project only after baseline parity. The browser opens an ordinary `/projects/<id>` in a clean profile, checks a real Save control and ready figures, measures cold/warm editable state, then opens the actual Tools window. All readers must reach this barrier before writes begin. Each reader remains on one project; simultaneous writes also exercise other projects. Only mounted foreground project/reader pairs claim visible-lag samples. The server logs every confirmed fact, including projects without a mounted reader; those are not fabricated render samples.

Defaults are60-second warmup,600-second sustained phase,1000-change/one-second burst and60-second recovery. `rate` is the requested global source rate, distributed across projects, not per project. Persistent SQL writers avoid spawning a process per fact. Select `browse --source-distribution=hot|distinct` explicitly when comparing results (default `hot`). Hot uses one ordered writer per project and replaces that project's running marker leaf; several captured work hints may coalesce into one replacement. It qualifies hot-execution responsiveness only. Distinct uses eight ordered lanes per project and creates one task-owned included `agent_run` execution and measured totals per fact, in its own canonical transaction. Each distinct leaf contributes one call and every seventh global fact contributes one failure; no auxiliary marker changes during the distinct source phase. The zero-call marker remains only the initial visible row. The long native fixture is hot-only; use a separate fresh case for distinct. Both modes retain the same requested global schedule and burst; bounded concurrency is recorded as `source-lanes`. Actual source acceptance/commit brackets, schedule delay, burst scheduling duration and backlog reveal whether the requested offered rate was achieved. The common verdict independently requires all sustained and burst facts to be confirmed in distinct transactions, with the entire sustained bracket within its declared duration, each ordered one-second sustained group within one second, and all1000 burst facts within1000ms. Every bracket uses the same server monotonic clock, expanded outward by1ms per endpoint; it includes submission, SQL queueing and commit-receipt overhead. SQL transaction IDs, before-commit database timestamps and after-commit receipts remain raw evidence. Offered scheduling is not admission/commit throughput: inadequate, missing or ambiguous confirmation returns `status: incomplete`, `reason: invalid-source-rate`, even if UI lag passes. This applies to hot, distinct and exceed-capacity; a recovery outcome cannot qualify a source-rate point. Development keeps its shortened declared counts and remains development. A historic hot result reporting1000 offered in999.03ms, confirmed over2475.49ms and UImax851.66ms is source-rate-unqualified; keep its raw evidence and hot classification separately, never reinterpret it as distinct throughput.

For shaped runs use `--network=80ms-10Mbps`: CDP adds80ms latency and caps each reader's upload/download at10Mbps. This is an impairment **in addition to actual network RTT**, not a claim of80ms total. Browser clock probes record the observed shaped round-trip and conservative clock uncertainty; retain unshaped probes too. No firewall/system qdisc is changed. An exact80ms-total profile remains untested if WAN latency already exceeds it.

Run dimension axes separately before the combined worst case, with BOTH network profiles:

| Dimension | Points (other axes at baseline) |
|---|---|
| History |618,18794,100000,1000000 executions |
| Projects |1,5 |
| Readers |1,10,50 |
| Confirmed changes/s |1,20,100 |
| Combined |1000000 executions,5 projects,50 readers,100/s |

Baseline is618 executions,1 project,1 reader,1/s. Duplicate baseline points need one actual run per network profile. This axis set plus combined worst case is not a Cartesian matrix; name any additional combinations that remain untested. Each case includes its own warmup/sustained/burst/recovery. A required point without samples remains pending.

Long/high-cardinality case: add `serve --long-events=100000 --cardinality=10000`. One running native execution accepts exactly100000 synthetic usage events through the real host HTTP path; reordered duplicate IDs are replayed, and canonical input count/event count/state/no invented CPU/API/wall facts are checked. Models/prices and tool names create high-cardinality support; source writes during the measured run also traverse accepted native normalization on that execution before its exact rendered marker. Keep this separately identified, and inspect the token figure and same-version page/header alongside the exact marker. A marker alone does not prove every visible numeric field's representation.

Exceed-capacity uses a separate fresh server and `browse --mode=exceed-capacity --rate=<measured overload rate>` with the same phase lengths. Result `recovered` means eventual exact marker totals and drained durable work after overload, not a five-second pass. Record backlog/error/progress and capacity breaches honestly. Increase/repeat only as necessary to observe actual overload; do not choose an arbitrary owner-budget ceiling.

## Evidence and interpretation

`manifest.json` includes deterministic expected inclusion, measured-zero/missing counts, metric sums/present/expected/bases, complete tool calls/errors and separated measured/subscription/estimated money. Seed SQL and expected enumeration do not call production reducers. `parity.json` records the genuine PostgreSQL comparison before measured mutations. Schema/protocol/RLS/frozen-method/restart/cancellation proof is supplied by the exact-artifact predecessor tests; reuse it only when current changes do not invalidate it, and link actual runs. No benchmark prototype substitutes for those assertions.

`source-manifest.json` binds the exact source distribution and workload to independently enumerated expected final execution/tool/failure sums, logical fact IDs, changed leaf IDs/count, canonical SQL mutation counts and commit transaction IDs/count; these are separate quantities. `manifest.json` remains the unchanged seed baseline; the source manifest adds only task-owned measured mutations. Result JSON repeats the mode, identities/counts and common `sourceRate` verdict. `events.jsonl` contains every source bracket, render sample, backlog observation, burst duration, control ack, error and final result. The browser reads a genuinely visible exact `calls · errors failed` row, then rechecks after two animation frames; version attributes alone cannot acknowledge a fact. A count crossing an ordinal never proves distinct inclusion. Before acknowledging a fact, a fixture-only SQL SELECT reads its exact included contribution, marker calls/errors and project epoch/revision in one read snapshot. Hot proofs may include earlier values of the same monotonically increasing leaf; distinct proofs require that leaf's exact calls AND failures. The server retains observed publication numerics and requires the actual visible row to match a recorded publication exactly, at the same epoch and equal/newer revision than that fact's inclusion proof. Unobserved versions are retried; an unrelated leaf or summary event cannot acknowledge a pending contribution. A changed version is rechecked after the two paint frames. This conservative proof read can delay acknowledgement; its overhead stays in the lag. Each reader retains acknowledged fact IDs individually, so replays do not duplicate samples and a hole never disappears behind a higher count. No percentile discards an intermediate violation. Lower timing precedes source submission; upper timing is receipt of the painted-value acknowledgement on the same monotonic server clock. This includes source write/commit/ack and return-network overhead, and therefore conservatively bounds availability-to-render without subtracting an uncertain clock offset. Observation timestamps stay separate. Report min/p50/p95/p99/**max**, every>5000ms sample, missing pairs and errors. Empty samples never pass.

`spans.jsonl` retains actual processor batch/replacement/lock timings. PostgreSQL snapshots/statements retain settings, calls/rows/blocks/tempI/O, activity and backlog. Browser observations retain DOM size, long tasks, rendered text, version, visibility and client clocks; screenshots require actual visual inspection. Cold/warm editable and first-summary timings and configuration action acks are diagnostic targets500ms/1500ms/2000ms/300ms as applicable; no percentile replaces the connected foreground maximum5s.

Before accepting a case, inspect generator schedule/actual acceptance rate, server/browser resource/pressure readback, every missing/error/violation, final exact marker parity and drained work. A recovery failure, preparing state or absent browser sample remains failed/pending. Current source does not automatically aggregate a complete cross-case acceptance report; the supervising worker retains exact run coverage and the original-profile limitations.

Stop only this fixture with `POST /__interface/stop` over its loopback tunnel, or SIGTERM the owned server. It stops its coordinator, drops its generated database and stops its own PostgreSQL cluster. Close driver browsers/tunnels, verify scoped process/query disappearance, restore any reserved Fleet-only runner services to their exact previous state and verify their online status. Keep evidence; only task-owned fixture state may be deleted.

Production activation is separate, after isolated proof: apply `FLEET_RESOURCE_PROCESSOR=on` and `FLEET_RESOURCE_READS=on` by supported deployment, actual restart/process/capability readback, ready/drained summaries, then Site-agents ordinary Fleet+small-project editable and new-confirmed-fact actual-render checks with inspected screenshots. Capture never turns off. Production is never this workload runner's origin, and only explicitly task-owned synthetic project data may be changed for live confirmation.

Focused development checks: `tests/go/project-resources.test.ts` also exercises the actual canonical source transaction/commit receipt and single-snapshot proof query against the existing isolated PostgreSQL processor, deliberately withholding one contribution while a second publishes, then releasing it and replaying capture. This outside check runs in CI; it is not a load trial. Pure mode/rate/epoch/order/duplicate/missing/numeric regressions run with `node --test tests/ui/interface-fixture.test.ts tests/ui/api.test.ts`; `pnpm run app:build`; explicit performance-runner typecheck with the repository TypeScript flags. Full merge gate is CI-only. Small development invocation uses `--development --history=18`, browser `--readers=1 --rate=1 --warmup=0 --duration=3 --burst=1 --recovery=5` and a placement JSON marking development; it is never capacity acceptance.
