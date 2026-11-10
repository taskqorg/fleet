# Fleet telemetry: one span per operation, problems found from spans

*   **Author:** Alex Kirs
*   **Status:** Proposed
*   **Date:** 2026-11-11
*   **Linked design:** [system management](fleet-system-management.md). This document owns spans, the rules pass and the read half of the System screen. System management owns the audit journal, problems as questions and the action half of the screen. Shared parts are designed once, in the section named in [Shared parts](#shared-parts).

--------------------------------------------------------------------------------

## Overview

Every Fleet service and host records each unit of work it does as one `span` row in ClickHouse, always on. A rules pass in the coordinator reads the spans every minute. When a rule fires, the owner or the project director gets one needs-you question, which closes itself when the rule stops firing. People, the PM agent and the System screen read the same spans through coordinator operations.

The design copies proven patterns and names: wide structured events per operation (Honeycomb), the OpenTelemetry (OTel) trace and span model with its semantic convention attribute names, W3C `traceparent`, RED (rate, errors, duration) for services and USE (utilization, saturation, errors) for hosts, multi-window burn-rate alerts (Google SRE workbook), exemplars, and Alertmanager-style grouping. It uses the Go standard library and no OTel SDK.

### Terms

*   **Span:** one row for one unit of timed work: an HTTP request, a pass, an outbound call, a model call, a heartbeat. Fields are in [Record model](#1-record-model).
*   **Trace:** all spans that share a `trace_id`; they form a tree by `parent_span_id`.
*   **Operation:** the pair `(service, name)`, for example `(coordinator, GET /api/tasks)`. Rates, error ratios and percentiles are computed per operation.
*   **Pass:** a periodic loop in a service (dispatch, reconcile, placement, update check, the rules pass itself).
*   **Rule:** one embedded SQL query that returns rows `(subject, value, baseline, exemplars, host_id, store_id, project_id)`; the last three drive grouping and the recipient. A returned row is a finding.
*   **Finding:** one rule firing for one subject, with up to five exemplar `trace_id`s. Findings are listed by `telemetry_findings`.
*   **Problem:** a finding (or a direct signal such as a refused insert) turned into a needs-you question with a `cause` key. Problems are owned by [system management, Problems](fleet-system-management.md#problems-needs-you-questions-from-rules).
*   **Audit journal:** durable Postgres record of who changed what. It is not telemetry. See [Spans and the journal](#7-spans-and-the-audit-journal).

## The Problem

Fleet records almost nothing about its own work. The Go code uses stdlib `log` in eight coordinator files with about 15 `log.Printf` calls; the executor and gateway log nothing structured. There is no request timing, no outcome count, no ClickHouse client. The [data foundation §4](fleet-data-foundation.md#4-clickhouse-events-spans-measurements) plans ClickHouse tables for executions only (Phase 4, not built). Coordinator routes, passes, dispatch decisions, gateway calls and host health have no planned record. The gateway writes a local spool file (`<state>/spool/<day>.jsonl`) that nothing ships.

The result: when Fleet is slow, a host goes quiet, a provider fails or a run costs more than usual, nobody learns about it until a person notices, and then the answer is in shell logs on three hosts. [fleet.md R5](../../fleet.md#principles) asks for every component and operation instrumented with queryable history; nothing meets it today.

## Goals

1.  One span shape for coordinator, executor, gateway and harness, with the columns of [Record model](#1-record-model).
2.  Trace context across every hop in the [hop table](#3-trace-context), so one trace runs from dispatch to a harness tool call.
3.  Always-on capture of the sources in [What is recorded](#2-what-is-recorded). No sampling of work.
4.  Telemetry never blocks or fails a request: with ClickHouse stopped, every request still succeeds and a drop counter rises.
5.  Stage 1 rules (absence, stuck, error spike, unknown) open one needs-you question per cause and close it when the cause clears.
6.  Operations `telemetry_summary`, `telemetry_trace`, `telemetry_findings` for the app and the PM agent, under the data foundation permission model.
7.  The read half of the System screen: services with RED and version, hosts with heartbeat age and USE readings, open findings, trace view.

## Non-Goals

*   OpenTelemetry SDK or Collector ([data foundation A12](fleet-data-foundation.md#a12-an-opentelemetry-collector-on-every-host-writing-to-clickhouse) rejected the collector).
*   Prometheus or any second metrics store.
*   ML anomaly detection, a separate alert service, an external watchdog.
*   Sampling of work.
*   Content in telemetry: no prompts, task text, model output, file contents, host paths or secrets.
*   Raw SQL access for people or agents.
*   Compatibility with specq, CoreQ, AssetQ or DashQ stores (prototypes, inputs only).
*   One span per Postgres query.

## Critical User Journeys (CUJs)

### CUJ 1: A host goes quiet

*   **Given:** the Windows/WSL executor sends a heartbeat span every minute.
*   **When:** no heartbeat arrives for 3 minutes.
*   **Then:** the owner gets one needs-you question "Host windows-wsl: no heartbeat since 14:02", with the last heartbeat's readings. Error-spike findings for executions on that host fold into the same question. When heartbeats resume, the question is withdrawn.

### CUJ 2: A provider starts failing

*   **Given:** the gateway records one `client` span per provider call.
*   **When:** more than 50% of calls to one provider fail over 5 minutes, with at least 10 calls.
*   **Then:** one question per provider (`problem:error-spike:gateway.call:<connection>`), with up to five exemplar traces. Answer `Investigate` opens a PM task with those links; `Dismiss` withdraws it until the cause clears and fires again.

### CUJ 3: Why was this task slow

*   **Given:** a task ran on the owner's Mac.
*   **When:** the director opens the task's trace from the task view.
*   **Then:** `telemetry_trace` returns one tree: the dispatch pass span, the executor command span, the harness turns, model calls and tool calls, each with duration and outcome.

### CUJ 4: ClickHouse is down

*   **Given:** ClickHouse refuses connections.
*   **When:** 3 coordinator flushes in a row fail (setting `telemetry.failedFlushes`).
*   **Then:** requests succeed. The coordinator counts dropped spans in memory and opens `problem:telemetry:clickhouse` in Postgres directly, without a ClickHouse query. On the next good flush it writes one `check` span with the dropped count and closes the question.

### CUJ 5: The PM looks at its director's projects

*   **Given:** a member's PM agent with `fleetOperations`.
*   **When:** it calls `telemetry_summary {window: "1h"}`.
*   **Then:** the reply holds operations of that member's projects only. System spans (no `project_id`) are left out because the member is not the owner.

## Technical Plan (Architecture)

```text
 executor ──spans file──▶ POST /api/spans ─┐
 gateway  ──spool──────▶ POST /api/spans ─┤         ClickHouse
 harness  ──OTLP JSON──▶ executor ─────────┘           span (MergeTree)
 coordinator obs ─────────── async_insert ──────────▶  span_minute (stage 2)
                                                          │
 rules pass (1/min) ◀─────────── SELECT ──────────────────┘
      │ findings
      ▼
 problems (cause questions, Postgres)  ──▶ needs-you list, System screen
 telemetry_* ops ◀──── app, PM agent
```

### 1. Record model

One unit of timed work is one `span` row. Rates, error ratios and percentiles come from spans at read time; no separate metric series exist.

| Column | Type | Meaning |
|---|---|---|
| `trace_id`, `span_id`, `parent_span_id` | `FixedString(32)`, `FixedString(16)`, `FixedString(16)` | W3C ids as lower-case hex; root has empty parent |
| `service` | `LowCardinality(String)` | `coordinator`, `executor`, `gateway`, `harness:<name>` |
| `host_id`, `version` | `LowCardinality(String)` | where it ran and the release version |
| `name` | `LowCardinality(String)` | operation name, see naming rules |
| `kind` | `Enum8` | `server` (handles an incoming request), `client` (outbound call), `internal` (work inside a service), `pass` (one run of a periodic loop), `check` (a reading: heartbeat, store probe, recovered drop count) |
| `start`, `duration_ns` | `DateTime64(6)`, `UInt64` | |
| `outcome` | `Enum8` | `ok`, `error`, `refused`, `cancelled` |
| `error_class` | `LowCardinality(String)` | short stable code; empty when ok |
| `actor` | `String` | person email, `agent:<execution_id>` or `fleet` |
| `project_id`, `task_id`, `execution_id`, `experiment_id`, `account_id` | `Nullable(String)` | copied from context, never joined |
| `tokens_in`, `tokens_out`, `tokens_cache_read`, `tokens_cache_write` | `Nullable(UInt64)` | model calls |
| `units`, `unit_kind` | `Nullable(Float64)`, `LowCardinality(String)` | gateway cost units, credits |
| `bytes`, `items`, `retries` | `Nullable(UInt64)` | |
| `pg_calls`, `pg_ms` | `Nullable(UInt32)`, `Nullable(Float32)` | Postgres sums per span (stage 2) |
| `load1`, `mem_free`, `disk_free`, `store_free` | `Nullable(Float64)` | `check` spans only |
| `cpu_seconds`, `rss_bytes`, `peak_rss_bytes`, `open_files` | `Nullable(Float64)`, `Nullable(UInt64)`, `Nullable(UInt64)`, `Nullable(UInt32)` | `process.reading` spans (CPU since last beat, RSS, open files) and execution spans (CPU and peak memory of the harness process tree) |
| `attrs` | `Map(LowCardinality(String), String)` | everything else |

Rules for the values:

*   **Names are stable and low-cardinality.** HTTP spans use the route pattern (`GET /api/tasks/{id}`), never the raw path. Passes use `pass.<name>` (`pass.dispatch`). Outbound calls use `<system>.<call>` (`clickhouse.insert`, `garage.put`, `github.pr`, `gateway.call`). A renamed operation starts a new series.
*   **Attribute names follow OTel semantic conventions** where one exists: `http.route`, `http.response.status_code`, `server.address`, `db.operation.name`, `gen_ai.request.model`. Fleet attributes use the `fleet.` prefix.
*   **Outcome mapping.** HTTP: 2xx and 3xx are `ok`; 401, 403, 409 from a limit, 423 and 429 are `refused`; other 4xx are `error` with `error_class` `http_4xx`; 5xx is `error` `http_5xx`. Client disconnect is `cancelled`. The coordinator maps from the HTTP status `writeError` writes; `HTTPError` gains a `reason` field (`limit`, `paused`, `grant`) that marks a 409 or 403 as `refused`; the gateway maps provider statuses the same way. `refused` (a decision by design: a limit, a paused host, a missing grant) never counts as an error.
*   **`error_class`** is one of a closed list kept in package `obs`: `timeout`, `auth`, `http_4xx`, `http_5xx`, `pg_conflict`, `pg`, `network`, `decode`, `panic`, `internal`. A new class is a code change.
*   **Unknown is never zero.** A value not collected is NULL, with the reason in `attrs['missing.<field>']`.
*   **Metadata only.** Content stays in transcripts under [data foundation §3 and §5](fleet-data-foundation.md#5-executor-harness-adapters-outbox-and-redaction).

**Tables.** Data foundation keeps `event` (the ordered per-execution stream) and `measurement` (cost with `basis`, `connection_id`). `span` becomes the home of all timed work across all services, not only inside executions. `host_metric` is dropped: host and store readings are `check` spans. Data foundation §4 is amended in this change.

### 2. What is recorded

| Source | Spans |
|---|---|
| Coordinator HTTP | one `server` span per request, wrapper outside both route tables (registered operations and the trial table) |
| Coordinator operations | the same `server` span, `name` = operation name, `actor` set |
| Coordinator passes | one `pass` span per run with `items` and outcome; dispatch, reconcile, totals, placement, update check, rules, backup |
| Route decisions | `internal` span `dispatch.route` per execution placed: host and subscription chosen in attrs; rejected candidate count in `items` (reasons per candidate: stage 2, in `event` rows) |
| Outbound calls | `client` span per ClickHouse, Garage, GitHub and provider call, via the `RoundTripper` wrapper |
| Executor | each command, execution start and end, each artifact upload, each failed poll; polls that carry work are recorded each |
| Executor heartbeat | idle polls fold into one `check` span `host.heartbeat` per host per minute with load, memory, disk free of the data dir, worktree roots and releases dir, harness versions, reachable stores |
| Process readings | one `check` span `process.reading` per Fleet process per heartbeat, child of `host.heartbeat`, `attrs['fleet.process']` = `coordinator`, `gateway`, `executor`, `fleet-update`, `postgres`, `clickhouse`, `garage`, `cloudflared`: CPU seconds since the last beat, RSS, open files. See [Process readings](#process-readings) |
| Execution resources | the execution end span carries `cpu_seconds` and `peak_rss_bytes` of the harness process tree |
| Gateway | `client` span `gateway.call` per call (connection, provider, status, units); `internal` span per snapshot refresh |
| Harness | turns, model calls, tool calls and shell commands (stage 3, data foundation Phase 4 adapters) |
| Lab | trials are executions; scores join by `execution_id` at read time |
| Self-update | each step is a span under one `update` trace per run |
| Telemetry itself | spans dropped on a refused insert are counted; one `check` span `telemetry.recovered` with the count is written on recovery |

**Volume.** Per minute: 3 executors x 1 heartbeat plus up to 8 process readings each (about 35,000 rows a day); coordinator passes about 6 x 60 = 360 (each pass runs about every 10 s); requests and calls as they happen. Without heartbeat folding, 4 pollers x 3 spans x 86,400 s would be about 1M no-op rows a day; with folding, idle load is about 0.5M pass rows a day, about 20 MB a day compressed. Pass spans with `items = 0` and outcome `ok` are kept: the absence rule needs them.

### Process readings

USE method (utilization, saturation, errors) per process, read cheaply with no new module.

*   **Linux:** for each systemd unit of a Fleet process, `systemctl show <unit> -p CPUUsageNSec -p MemoryCurrent -p MainPID` (cgroup accounting); open files from counting `/proc/<MainPID>/fd`. CPU seconds since the last beat is the `CPUUsageNSec` difference. The unit list is a host setting with the defaults above; a unit that does not exist is skipped, not reported as zero.
*   **macOS:** `ps -o pid=,rss=,time= -p <pids>` via `os/exec` for processes found by name; open files from `lsof -p <pid> -Fn` only every tenth beat (it is slow), otherwise NULL with `missing.open_files = sampled`.
*   **Per execution:** Linux reads the run's `fleet-launch@<user>:<execution>.service` unit at execution end (`systemctl show -p CPUUsageNSec -p MemoryPeak`), free. macOS samples the harness process tree (`ps -o pid=,ppid=,rss=,time= -A`, children of the harness PID) every 10 s during the run and keeps the peak RSS and the last CPU total. Each heartbeat the executor also writes one `check` span `execution.alive` per running execution with the CPU seconds since the last beat, so a run busy in one long model call is not taken for stuck. At end the executor also records the PIDs still alive in that tree into `attrs['fleet.orphans']` (count) for the orphan rule.
*   **Windows/WSL:** the executor runs inside WSL, so the Linux path applies; WSL VM memory is the host `mem_free`.

### 3. Trace context

W3C `traceparent` on every hop. The coordinator starts a trace per incoming request and per pass run.

| Hop | Carrier | When absent |
|---|---|---|
| Browser or agent to coordinator | `traceparent` header if sent; else a new root | new root |
| Coordinator to executor | fields `traceparent` and `run` (`projectId`, `taskId`, `executionId`, `experimentId`, `accountId`, `actor`) on each `Command` in `PollReply` (`contracts/host-protocol.schema.json`); the executor copies `run` onto every span of that command and passes it to the gateway as W3C `baggage` | executor starts a root with `missing.parent` |
| Executor to coordinator | header on outbox and `/api/spans` posts | new root on the coordinator side |
| Executor to gateway | `traceparent` header (not in the per-run token: a token gives one parent per run, not per call) | gateway root with `missing.parent` |
| Executor to harness | env `TRACEPARENT` at process start; harness native OTel exports to the executor loopback endpoint | harness spans parent to the execution span |

Old executors ignore the new `Command` field (the schema allows unknown fields to be skipped on decode by old code; new code adds it as optional). One execution is a subtree of the dispatch trace that started it. Lab `execution.request_id` is a trial id, not a trace id; it stays separate.

**Package `obs`** (new, stdlib only, in the Go module): `obs.Start(ctx, name, kind) (ctx, *Span)`, `(*Span).Set(key, value)`, `(*Span).End(err)`, `obs.Handler(http.Handler)`, `obs.Transport(http.RoundTripper)`, `obs.Pass(ctx, name, func(ctx) (items int, err error))`. Context carries trace and run identity (project, task, execution, actor); product code never passes ids by hand. Trace data rides in the request context, so handler signatures do not change. Passes have no common runner today, so stage 1 wraps each pass call site; the list is in [Detailed Implementation](#detailed-implementation). `log/slog` replaces `log` in a stage 0 change, and every line carries `trace_id` from context.

### 4. Storage, transport, retention

*   **Table** `span`: plain `MergeTree`, `PARTITION BY toYYYYMM(start)`, `ORDER BY (service, name, start)`, bloom-filter skip indexes on `trace_id`, `execution_id`, `project_id`. Plain `MergeTree` because `ReplacingMergeTree` dedups by sort key, and `span_id` is not in it. Table setting `non_replicated_deduplication_window = 1000` (a plain `MergeTree` ignores insert tokens without it). Retried batches are idempotent through `insert_deduplication_token` set to the batch id, within the last 1,000 inserted blocks. Rare duplicates outside that window barely move rates.
*   **Coordinator writes** its own spans through `net/http` to the ClickHouse HTTP interface: `INSERT INTO span FORMAT JSONEachRow` as one plain synchronous insert per batch (no `async_insert`: with `wait_for_async_insert=0` a failed write never reaches the caller, so drops could not be counted). A buffered channel feeds one writer goroutine that flushes each second or at 1,000 rows; the request goroutine never waits. When the channel is full or the insert fails, the span is dropped and counted (help, never block). The counter is in memory and is reported by `telemetry_summary` and the absence rule.
*   **Executor and gateway** send spans to a separate route `POST /api/spans`, not the execution outbox: outbox ingest requires an `executionId`, and state events must not queue behind telemetry. The executor appends spans to `<data>/spans/<day>.jsonl` and ships them on its own loop after the outbox, in batches of up to 1,000 with a batch id; a rejected batch is retried, a malformed line is skipped and counted. Outbox sends do not create spans; their counts go into the heartbeat attrs. The gateway ships its existing spool on the same route, reusing the executor shipper code. The coordinator forwards accepted batches to ClickHouse with the same token.
*   **Retention:** no TTL. Prune only when measured volume requires it, by whole oldest months, `kind = pass` first. A self-growth line in `telemetry_summary` shows bytes per day.
*   **Rollup (stage 2):** materialized view `span_minute`, `AggregatingMergeTree` keyed by `(service, name, outcome, project_id, minute)` with count, `quantileTDigestState(duration_ns)` and token and unit sums. It ships with the baseline rules, so rules never scan 7 days of raw rows.
*   **Off switch:** setting `telemetry.enabled` (default on). Off stops writes and the rules pass; the drop counter keeps counting.

### 5. Query surface

Operations, under the [data foundation §6](fleet-data-foundation.md#6-operations-for-people-and-agents) permission model. Contract `$defs` are listed below for `contracts/ops.schema.json` (wave 2).

| Operation | Stage | Reply |
|---|---|---|
| `telemetry_summary` | 1 (baseline column in 2) | RED per operation for a window, change against the previous window, drop counter, rules-pass last run, bytes per day |
| `telemetry_trace` | 1 | one trace as a flat list ordered by `start`; the app builds the tree |
| `telemetry_findings` | 1 | current findings with exemplars, grouped findings marked with their group cause |
| `telemetry_query` | 2 | grouped counts and quantiles over span columns with filters, no raw SQL |

**Permissions.** Project spans need the project grant; system spans (no `project_id`) need the owner. For non-owners the coordinator adds `project_id IN (<granted>)` to every query. **PM agent:** sees all telemetry within its director's rights; the owner's PM sees the whole system. Metadata only; transcripts only through its own transcript grant.

**Overlap with existing operations.** Boundary: telemetry queries spans (operations, latency, errors); [`analytics_query`](fleet-analytics.md#analytics_query) queries work records (executions, tokens, cost, experiments). `telemetry_query` never reports cost, and `analytics_query` never reads spans; a question needing both links them by `executionId`. `usage_summary` was never implemented; cost totals come from `analytics_query`. `executions_get` keeps state; it gains a `traceId` field that links to `telemetry_trace`.

```json
{
  "TelemetryWindow": {"type": "string", "enum": ["5m", "1h", "24h", "7d"]},
  "SpanOutcome": {"type": "string", "enum": ["ok", "error", "refused", "cancelled"]},
  "SpanKind": {"type": "string", "enum": ["server", "client", "internal", "pass", "check"]},
  "OperationStats": {"type": "object", "required": ["service", "name", "count", "errors", "refused", "p50Ms", "p95Ms"], "properties": {
    "service": {"type": "string"}, "name": {"type": "string"}, "count": {"type": "integer"}, "errors": {"type": "integer"},
    "refused": {"type": "integer"}, "p50Ms": {"type": "number"}, "p95Ms": {"type": "number"},
    "previousCount": {"type": "integer"}, "previousErrors": {"type": "integer"}, "baselineP95Ms": {"type": "number"}}},
  "TelemetrySummaryRequest": {"type": "object", "properties": {"window": {"$ref": "#/$defs/TelemetryWindow"}, "projectId": {"type": "string"}, "service": {"type": "string"}}},
  "TelemetrySummaryReply": {"type": "object", "required": ["window", "operations", "droppedSpans", "rulesLastRun"], "properties": {
    "window": {"$ref": "#/$defs/TelemetryWindow"}, "operations": {"type": "array", "items": {"$ref": "#/$defs/OperationStats"}},
    "droppedSpans": {"type": "integer"}, "rulesLastRun": {"type": "string", "format": "date-time"}, "bytesPerDay": {"type": "integer"}}},
  "Span": {"type": "object", "required": ["traceId", "spanId", "service", "name", "kind", "start", "durationMs", "outcome"], "properties": {
    "traceId": {"type": "string"}, "spanId": {"type": "string"}, "parentSpanId": {"type": "string"}, "service": {"type": "string"},
    "hostId": {"type": "string"}, "name": {"type": "string"}, "kind": {"$ref": "#/$defs/SpanKind"}, "start": {"type": "string", "format": "date-time"},
    "durationMs": {"type": "number"}, "outcome": {"$ref": "#/$defs/SpanOutcome"}, "errorClass": {"type": "string"},
    "executionId": {"type": "string"}, "attrs": {"type": "object", "additionalProperties": {"type": "string"}}}},
  "TelemetryTraceRequest": {"type": "object", "required": ["traceId"], "properties": {"traceId": {"type": "string"}}},
  "TelemetryTraceReply": {"type": "object", "required": ["spans"], "properties": {"spans": {"type": "array", "items": {"$ref": "#/$defs/Span"}}, "hidden": {"type": "integer"}}},
  "Finding": {"type": "object", "required": ["rule", "subject", "value", "since", "exemplars"], "properties": {
    "rule": {"type": "string"}, "subject": {"type": "string"}, "value": {"type": "number"}, "baseline": {"type": "number"},
    "since": {"type": "string", "format": "date-time"}, "exemplars": {"type": "array", "items": {"type": "string"}},
    "hostId": {"type": "string"}, "storeId": {"type": "string"}, "projectId": {"type": "string"},
    "cause": {"type": "string"}, "groupCause": {"type": "string"}, "questionId": {"type": "string"}}},
  "TelemetryFindingsRequest": {"type": "object", "properties": {"projectId": {"type": "string"}}},
  "TelemetryFindingsReply": {"type": "object", "required": ["findings", "rulesLastRun"], "properties": {
    "findings": {"type": "array", "items": {"$ref": "#/$defs/Finding"}}, "rulesLastRun": {"type": "string", "format": "date-time"}}},
  "SpanBatchRequest": {"type": "object", "required": ["batchId", "spans"], "properties": {"batchId": {"type": "string"}, "spans": {"type": "array", "items": {"type": "object"}}}},
  "SpanBatchReply": {"type": "object", "required": ["accepted", "skipped"], "properties": {"accepted": {"type": "integer"}, "skipped": {"type": "integer"}}}
}
```

`TelemetryTraceReply.hidden` counts spans of the trace the caller may not see (another project). `SpanBatchRequest.spans` items are the ClickHouse column names of [Record model](#1-record-model) in JSON; the route checks them against the host's own `host_id` and refuses rows that claim another host or an `execution_id` not assigned to that host. `SpanBatch*` belongs in `contracts/host-protocol.schema.json`, not `ops`.

### 6. Rules

One rules pass in the coordinator runs every minute (`pass.rules`). Each rule is one embedded ClickHouse query. Every factor below is a setting with this default, tuned from the first week of data.

| Rule | Stage | Fires when | Subject | Cause key |
|---|---|---|---|---|
| `absence-host` | 1 | no `host.heartbeat` for 3 minutes from a host not `removed` and with `intake` not `paused` | host | `problem:host:<id>:silent` |
| `absence-pass` | 1 | a coordinator pass has no span for 3 of its intervals | pass name | `problem:absence:pass.<name>` |
| `absence-store` | 1 | no store `check` span reports a store reachable for 3 heartbeats | store | `problem:store:<id>:offline` |
| `store-fill` | 1 | latest `store_free` or `disk_free` below the store's `free_space_warning` | store or host disk | `problem:store:<id>:fill` |
| `stuck` | 1 | an open execution with no span, no `event` and no CPU progress in its `execution.alive` readings for 30 minutes | execution | `problem:execution:<id>:stuck` |
| `error-spike` | 1 | an operation's error ratio over 5 minutes above 50% with at least 10 calls | operation (client spans: `server.address`) | `problem:error-spike:<service>:<name>` |
| `unknown` | 1 | executions ended with no terminal span; `outbox_failed` rows; dropped spans above 0 | kind | `problem:unknown:<kind>` |
| `orphan` | 1 | an execution end span with `fleet.orphans` above 0, or a `fleet-launch@…:<execution>` unit still running for an ended execution | execution | `problem:execution:<id>:orphans` |
| `host-saturation` | 1 | `load1` above the CPU count, or `mem_free` below 5% of memory, for 5 beats in a row | host | `problem:host:<id>:saturated` |
| `process-cpu` | 2 | a process's CPU seconds per beat above 0.9 x the CPU count for 10 beats, or above 3 x its own 7-day median | process on host | `problem:process-cpu:<host>:<process>` |
| `process-leak` | 2 | a process's RSS rises in at least 50 of the last 60 beats and is above 2 x its 7-day median | process on host | `problem:process-leak:<host>:<process>` |
| `error-burn` | 2 | 1-hour and 5-minute error ratios both above 14.4 x the operation's 7-day ratio | operation | `problem:error-burn:<service>:<name>` |
| `latency` | 2 | 1-hour p95 above 2 x the 7-day p95, at least 20 calls | operation | `problem:latency:<service>:<name>` |
| `cost` | 2 | tokens or credits per task, per experiment, per hour above 3 x the 7-day median; a subscription limit projected to run out before reset | project or subscription | `problem:cost:<subject>` |

All new keys start with `problem:` so the dispatch sweep, which withdraws any `host:`, `execution:`, `subscription:`, `placement:` or `budget:…:reached` cause it did not compute, never touches them. Rule results feed [Problems](fleet-system-management.md#problems-needs-you-questions-from-rules), which turns them into questions, groups them and closes them. `stuck` uses 30 minutes until stage 2 replaces it with the execution kind's own 7-day p99. A rule with no baseline yet (cold start: less than 7 days of rollup) does not fire.

**When the watcher is down.** The coordinator counts failed inserts in memory. After 3 failed flushes in a row it opens `problem:telemetry:clickhouse` in Postgres directly. The rules pass records its last run in `telemetry_summary`; the System screen shows findings as stale when that is older than 3 minutes. If the coordinator itself is down, nothing fires; `/api/health` and the existing provider checks remain the outside view.

### 7. Spans and the audit journal

These are two records with two jobs. Neither replaces the other.

| | Span | Audit journal entry |
|---|---|---|
| Question it answers | how long, did it work, where | who changed what, from what to what |
| Store | ClickHouse | Postgres, same transaction as the change |
| Loss | may be dropped (drop and count) | never: the change fails if the entry fails |
| Volume | every unit of work | only state changes by people, agents or Fleet |
| Content | metadata | previous and new value of the changed field |

An operation that writes a journal entry also gets a `server` span; the journal entry stores the span's `trace_id`, and the span gets `attrs['fleet.journal_id']`. The journal is designed in [system management, Audit journal](fleet-system-management.md#audit-journal).

### 8. System screen: read half

One System screen (owner decision), built from `packages/ui`. Telemetry supplies the read panels; [system management](fleet-system-management.md#system-screen) supplies the actions and the layout.

*   **Services:** one row per service and host: version, RED for the last hour from `telemetry_summary`, drop counter.
*   **Hosts:** heartbeat age, load, memory, disk free, store free from the latest `host.heartbeat`.
*   **Findings:** `telemetry_findings`, each with exemplar trace links; stale marker from `rulesLastRun`.
*   **Trace view:** `telemetry_trace` as a tree with durations; reachable from findings, tasks and executions.

## Settings

All numbers in this design are settings; these are their defaults, tuned from the first week of data.

| Key | Default |
|---|---|
| `telemetry.enabled` | true |
| `telemetry.flushSeconds`, `telemetry.flushRows` | 1, 1000 |
| `telemetry.failedFlushes` | 3 |
| `telemetry.staleMinutes` | 3 |
| `telemetry.attrMaxBytes` | 256 |
| `telemetry.lsofEvery` | 10 beats |
| `telemetry.processUnits` | coordinator, gateway, executor, fleet-update, postgres, clickhouse, garage, cloudflared |
| `rules.<rule>.*` | the factors in the [rules table](#6-rules) |

## Shared parts

| Part | Designed in | Used by |
|---|---|---|
| Audit journal, and how it differs from spans | [system management](fleet-system-management.md#audit-journal); difference in [§7 here](#7-spans-and-the-audit-journal) | both |
| Problems: rule results and direct signals as needs-you questions | [system management](fleet-system-management.md#problems-needs-you-questions-from-rules) | rules here, cause list there |
| System screen | layout and actions in [system management](fleet-system-management.md#system-screen); read panels in [§8 here](#8-system-screen-read-half) | both |
| Host readings (disk, store free) | `host.heartbeat` here | `store-fill` rule, System screen, purge preview |

## Alternatives Considered

### specq telemetry contract as given (CoreQ observation, DashQ global store)

*   **Pros:** written already.
*   **Cons:** built for prototypes Fleet replaces; a second store.
*   **Reason for rejection:** Rejected by author (owner direction): specq, CoreQ and AssetQ are prototypes; Fleet replaces DashQ.

### OpenTelemetry Go SDK and Collector on every host

*   **Pros:** standard exporters, wide tool support.
*   **Cons:** many modules and a process per host for what a few hundred stdlib lines do.
*   **Reason for rejection:** Rejected by author: stdlib first ([fleet-stack](fleet-stack.md)); data foundation A12 already rejected the collector. Fleet keeps OTel names and the W3C format, so a later switch is a mapping, not a redesign.

### Metrics as separate series (Prometheus, OTel metrics)

*   **Pros:** cheap long-term counters.
*   **Cons:** a second store and shape; RED is derivable from wide spans.
*   **Reason for rejection:** Rejected by author: one store; the rollup view gives the cheap read path.

### All four data foundation tables merged into one `span` table

*   **Reason for rejection:** Rejected after review: `event` is the ordered per-execution stream Phase 4 relies on; `measurement` carries cost with `basis`.

### `ReplacingMergeTree` dedup by `span_id`

*   **Reason for rejection:** Rejected after review: it dedups by sort key only, and reading with `FINAL` on every aggregate costs every query. Insert tokens instead.

### Spans inside the execution outbox

*   **Reason for rejection:** Rejected after review: outbox requires `executionId`, and telemetry must not delay state events.

### One span per idle poll

*   **Reason for rejection:** Rejected by author: about 1M no-op rows a day; folded into one heartbeat per host per minute.

### Baseline rules scanning raw rows every minute

*   **Reason for rejection:** Rejected after review: baselines read the per-minute rollup.

### Spans as the audit journal

*   **Pros:** one record.
*   **Cons:** spans are dropped under failure by design; an audit must never be.
*   **Reason for rejection:** Rejected by author: different loss rules; the two link by `trace_id`.

--------------------------------------------------------------------------------

## Non-Functional Requirements

*   **Never blocks.** The writer channel is non-blocking; a full channel drops. Insert and shipping run off request goroutines. Measured by the stage 1 test "ClickHouse stopped".
*   **Overhead.** One span is about 300 bytes in memory and one map; the target is under 1% CPU on the coordinator at current load, checked with `go test -bench` on the handler wrapper.
*   **Privacy.** Attributes are set only through `Span.Set`, which refuses keys outside an allow list per kind (counted in `attrs['missing.attrs']`, not silently lost) (`http.*`, `server.*`, `db.*`, `gen_ai.request.model`, `fleet.*`) and values longer than 256 bytes. Error messages never go into spans; `error_class` does.
*   **Cardinality.** `name` comes from route patterns and fixed pass and call names. A test fails when a span name contains a digit run longer than 6 or a UUID.
*   **Rollback.** `telemetry.enabled = false` stops writes and rules. The table and routes stay.

## Risks & Mitigations

*   **Span loss during a long ClickHouse outage.** Coordinator spans are dropped (counted); executor and gateway spans wait in their files. Accepted: the count and the clickhouse question say how much was lost.
*   **A rule that fires on noise.** Every factor is a setting; `Dismiss` withdraws a question until the cause clears.
*   **Attrs growing without bound.** The allow list and value cap in `Span.Set`.

## Detailed Implementation

Wave 1 renames Go files, so this list names packages and areas, not file paths.

### Stage 0: structured logs

1.  Coordinator: replace every `log.Printf` with `log/slog` using a JSON handler; executor and gateway get the same handler. Why: one log format with `trace_id` before spans exist.

### Stage 1

1.  **Package `obs`** (create): span type, context keys, `Start`, `End`, `Set` with the allow list, `Handler`, `Transport`, `Pass`, the writer (channel, flush, drop counter), W3C `traceparent` parse and format, `error_class` list, HTTP outcome mapping. Tests: parse and format round trip; outcome mapping table; full channel drops and counts; `Set` refuses keys outside the allow list.
2.  **Coordinator main and router:** wrap the whole mux with `obs.Handler` outside both route tables; set span `name` from the matched route pattern or operation name; map the status and the new `HTTPError.reason` to outcomes. Wrap the HTTP clients for ClickHouse, Garage, GitHub and providers with `obs.Transport`.
3.  **Coordinator passes:** wrap each pass call site with `obs.Pass`: dispatch, reconcile, totals, placement, update check, connections refresh, Lab scheduler loops, rules, backup. Stage 1 starts by listing every `go`-started loop in the coordinator package with a grep for `time.NewTicker` and `time.Tick`; each found loop is wrapped or recorded as not a pass in the PR description.
4.  **Coordinator dispatch:** `dispatch.route` span per placed execution.
5.  **Coordinator ClickHouse writer:** HTTP insert with `async_insert`, settings `clickhouse.url` and `telemetry.enabled`; on 3 failed flushes open `problem:telemetry:clickhouse` with `problems.open`, close it with `problems.close` on the next good flush.
6.  **Coordinator route `POST /api/spans`:** host-secret auth as other host routes; decode `SpanBatchRequest`, refuse rows with another `host_id`, forward to ClickHouse with `insert_deduplication_token = batchId`.
7.  **Coordinator rules pass:** embedded SQL per stage 1 rule; each returns `(subject, value, baseline, exemplars)`; results go to `problems.sync` of system management with source `rules`.
8.  **Coordinator operations:** `telemetry_summary`, `telemetry_trace`, `telemetry_findings`, with the project filter for non-owners; `executions_get` gains `traceId`.
9.  **ClickHouse schema** (the coordinator's ClickHouse migration area): create `span` with the indexes of §4.
10. **Executor:** span file writer and shipper; process readings and per-execution CPU and peak memory per [Process readings](#process-readings), with orphan count at end; command, execution, upload and failed-poll spans; heartbeat folding with readings; read `traceparent` from `Command`; send `traceparent` on outbox, gateway and span posts; set `TRACEPARENT` for the harness.
11. **Gateway:** `gateway.call` and snapshot-refresh spans written to the spool in span shape; ship the spool through the executor shipper code to `/api/spans`.
12. **Self-update:** one trace per run, one span per step, written to the executor span file.
13. **Contracts:** `contracts/host-protocol.schema.json` gains `Command.traceparent` (optional string), `Command.run` (optional object) and `SpanBatchRequest`/`SpanBatchReply`; the `ops` `$defs` of §5 go into `contracts/ops.schema.json` in wave 2.
14. **App (`packages/ui` only):** the System screen read panels of §8 and the trace view.
15. **Docs:** [data foundation §4](fleet-data-foundation.md#4-clickhouse-events-spans-measurements) amended in this change; [self-update](fleet-self-update.md) R5 and [PM agent](fleet-pm-agent.md) signals point here when those files are next edited.

### Stage 2 (after a week of data)

1.  `span_minute` rollup view; `error-burn`, `latency`, `cost` rules; `stuck` from p99; baseline column in `telemetry_summary`.
2.  `telemetry_query`; `pg_calls`, `pg_ms` summed in the store transaction wrapper; per-candidate route reasons as `event` rows.
3.  PM `Investigate` routine: answer `Investigate` opens a PM task with the exemplar links.

### Stage 3

1.  Harness adapters and the native OTel loopback (OTLP/HTTP JSON) from data foundation Phase 4, writing to `span`; Lab and cost views.

### Tests (Go, `tests/go` and package tests)

1.  ClickHouse stopped: 100 requests succeed; drop counter is 100+; `problem:telemetry:clickhouse` opens; after restart one `telemetry.recovered` span and the question closes.
2.  A span batch posted twice with the same `batchId` yields one set of rows.
3.  One trace spans dispatch pass, executor command and a fake harness span through `TRACEPARENT`.
4.  An injected error ratio on one operation opens one question; a clean window closes it.
5.  A non-owner `telemetry_summary` contains no system spans and no other project.

### Done when

Stage 1: the five tests pass in CI; on PDS18 the System screen shows RED for every coordinator route and heartbeat age for all three hosts; stopping the Windows executor for 3 minutes opens one question that closes when it returns.

## Open Questions

*   Whether the executor span file reuses the outbox file format. Default: a separate JSONL file with the same rotation code.
*   Starting rule factors are tuned from the first week; the values above are defaults.
