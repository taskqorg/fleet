# Observability

## Overview

`obs` builds trace spans, propagates W3C trace context, instruments HTTP handlers and transports, and can batch spans to ClickHouse.

## Interface

- `Start`, `StartAt`, `From`, `WithRun`, `RunOf`, `WithRemote`, and `Traceparent` create and propagate execution context.
- `Span.Set`, `Fail`, and `End` record attributes, error class, timing, and completion.
- `Handler` and `Transport` wrap HTTP server and client paths. `Recorder` captures response status and byte count; `MarkRefused` marks an access refusal.
- `ClickHouse` migrates, inserts, and queries over HTTP. `Writer` buffers spans, exposes dropped count, and flushes in `Run`.

## Invariants

- Trace IDs and span IDs are fixed-width lowercase hexadecimal values; invalid traceparent input is not propagated ([obs.go](obs.go)).
- A span ends once. `Valid` identifies complete spans, while `Classify` gives stable error classes ([obs.go](obs.go)).
- The ClickHouse writer batches asynchronously; full or failed batches increase the dropped counter instead of blocking the caller ([clickhouse.go](clickhouse.go)).

## Side effects

- `SetSink` invokes a process-wide span sink when spans end ([obs.go](obs.go)).
- HTTP wrappers add trace headers and record response results ([obs.go](obs.go)).
- `ClickHouse` sends HTTP migrations, inserts, and queries; `Writer.Run` flushes buffered spans until its context ends ([clickhouse.go](clickhouse.go)).

## Verification

Run from `go/`:

```sh
go test ./obs
```
