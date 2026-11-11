// Telemetry (docs/designs/fleet-telemetry.md): the coordinator's own spans, the span route hosts post to, and the
// telemetry operations. Spans live in ClickHouse; with no ClickHouse configured they are dropped and counted.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/obs"
)

var (
	spanStore  *obs.ClickHouse // nil: no span store
	spanWriter *obs.Writer
)

// clickhouseFromEnv: FLEET_CLICKHOUSE_URL (default http://127.0.0.1:9480 when the env file exists), credentials from
// FLEET_CLICKHOUSE_ENV (default ~/.config/fleet/clickhouse.env: CLICKHOUSE_DB, CLICKHOUSE_USER, CLICKHOUSE_PASSWORD, the
// file the ClickHouse Quadlet reads). FLEET_CLICKHOUSE_URL=off turns telemetry off.
func clickhouseFromEnv() *obs.ClickHouse {
	u := os.Getenv("FLEET_CLICKHOUSE_URL")
	if u == "off" {
		return nil
	}
	f := os.Getenv("FLEET_CLICKHOUSE_ENV")
	if f == "" {
		h, _ := os.UserHomeDir()
		f = filepath.Join(h, ".config/fleet/clickhouse.env")
	}
	vals := map[string]string{}
	if b, err := os.ReadFile(f); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(l, "export ")), "="); ok {
				vals[k] = strings.Trim(v, `'"`)
			}
		}
		if u == "" {
			u = "http://127.0.0.1:9480"
		}
	}
	if u == "" {
		return nil
	}
	db := vals["CLICKHOUSE_DB"]
	if db == "" {
		db = "fleet"
	}
	return &obs.ClickHouse{URL: u, Database: db, User: vals["CLICKHOUSE_USER"], Password: vals["CLICKHOUSE_PASSWORD"]}
}

// startTelemetry: called by main before the other starters, so every loop's first span already has the identity and sink.
func startTelemetry(ctx context.Context, a *App) {
	{
		obs.Service, obs.Version, obs.HostID = "coordinator", version, pds18Host()
		if obs.HostID == "" {
			obs.HostID, _ = os.Hostname()
		}
		spanStore = clickhouseFromEnv()
		spanWriter = &obs.Writer{CH: spanStore, Fails: 3,
			OnDown: func() { telemetryProblem(ctx, a, true) },
			OnUp:   func(int64) { telemetryProblem(ctx, a, false) }}
		if spanStore == nil {
			obs.SetSink(func(*obs.Span) { spanWriter.Discard(1) }) // telemetry off: dropped and counted
			return
		}
		spanWriter.Run(ctx)
		obs.SetSink(spanWriter.Add)
		go func() {
			for ctx.Err() == nil {
				err := spanStore.Migrate(ctx)
				if err == nil {
					return
				}
				log.Printf("telemetry: span schema: %v", err)
				select {
				case <-ctx.Done():
				case <-time.After(time.Minute):
				}
			}
		}()
	}
}

func init() {
	// Spans from executors and gateways (fleet-telemetry.md §4). Under /api/hosts/ for host-secret auth; rows naming
	// another host, or an execution not on this host, are refused (counted in skipped).
	longRoute("POST /api/hosts/spans", hostSpans)
	longOp("telemetry_summary", typedOp(telemetrySummary))
	longOp("telemetry_trace", typedOp(telemetryTrace))
}

// telemetryProblem opens or closes problem:telemetry:clickhouse (3 failed flushes in a row; closed on the next good flush).
func telemetryProblem(ctx context.Context, a *App, open bool) {
	err := runTx(ctx, a, nil, true, "", func(q *Request) error {
		if open {
			return problemOpen(q, "problem:telemetry:clickhouse", nil, "Telemetry: ClickHouse refuses span inserts; spans are dropped and counted until it answers.")
		}
		return problemClose(q, "problem:telemetry:clickhouse")
	})
	if err != nil {
		log.Printf("telemetry problem: %v", err)
	}
}

// hostService: the services a host may write spans as.
func hostService(s string) bool {
	return s == "executor" || s == "gateway" || s == "fleet-update" || strings.HasPrefix(s, "harness:")
}

// spanName: the server span name of a request, after the mux served it: the operation name for /api/ops/{name} when it
// exists, else the matched route pattern; "" for an unmatched path.
func spanName(r *http.Request) string {
	if r.Pattern == "" && r.URL.Path == "/api/stream" {
		return "GET /api/stream"
	}
	if r.Pattern == "POST /api/ops/{name}" || strings.HasPrefix(r.Pattern, "POST /api/ops/") {
		n := strings.TrimPrefix(r.URL.Path, "/api/ops/")
		if _, ok := operations[n]; ok {
			return n
		}
		return ""
	}
	return r.Pattern
}

func hostSpans(q *Request) (any, error) {
	var b contract.SpanBatchRequest
	if err := decode(q.body, &b); err != nil {
		return nil, err
	}
	if b.BatchId == "" || len(b.Spans) > 5000 {
		return nil, fail(400, "Expected batchId and at most 5000 spans")
	}
	host := hostOf(q)
	type run struct {
		ok                                 bool
		project, task, experiment, account *string
	}
	execs := map[string]*run{}
	var buf bytes.Buffer
	skipped := 0
	for _, raw := range b.Spans {
		var s obs.Span
		if json.Unmarshal(raw, &s) != nil || s.HostID != host || !obs.Valid(&s) || !hostService(s.Service) {
			skipped++
			continue
		}
		// The work identity comes from the execution row, never from the host: a host cannot write into a project's view.
		s.ProjectID, s.TaskID, s.ExperimentID, s.AccountID = nil, nil, nil, nil
		if s.ExecutionID != nil {
			id := *s.ExecutionID
			r := execs[id]
			if r == nil {
				r = &run{}
				var on *string
				err := q.app.store.pool.QueryRow(q.ctx(), "SELECT host_id,project_id,task_id,experiment_id,account_id FROM execution WHERE id=$1", id).Scan(&on, &r.project, &r.task, &r.experiment, &r.account)
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return nil, err // the host retries the batch
				}
				r.ok = err == nil && on != nil && *on == host
				execs[id] = r
			}
			if !r.ok {
				skipped++
				continue
			}
			s.ProjectID, s.TaskID, s.ExperimentID, s.AccountID = r.project, r.task, r.experiment, r.account
		}
		js, _ := json.Marshal(&s)
		buf.Write(js)
		buf.WriteByte('\n')
	}
	accepted := len(b.Spans) - skipped
	if accepted > 0 && spanStore == nil {
		spanWriter.Discard(accepted) // telemetry off: dropped and counted, the host does not retry
	} else if accepted > 0 {
		if err := spanStore.Insert(q.ctx(), "span", buf.Bytes(), host+":"+b.BatchId); err != nil {
			log.Printf("telemetry: host spans: %v", err)
			return nil, fail(503, "Span store unavailable")
		}
	}
	return contract.SpanBatchReply{Accepted: int64(accepted), Skipped: int64(skipped)}, nil
}

// spanScope: the SQL filter and parameters of the caller's spans: none with full access, else only rows of granted
// projects (system spans have no project and never match).
func spanScope(p *Person, params map[string]string) string {
	if fullAccess(p) {
		return "1"
	}
	ids := []string{}
	for _, g := range p.Grants {
		if g.Kind == "project" {
			ids = append(ids, "'"+strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(g.ID)+"'")
		}
	}
	params["scope"] = "[" + strings.Join(ids, ",") + "]"
	return "project_id IN {scope:Array(String)}"
}

var windowSeconds = map[contract.TelemetryWindow]int{"5m": 300, "1h": 3600, "24h": 86400, "7d": 7 * 86400}

func telemetrySummary(q *Request, r *contract.TelemetrySummaryRequest) (contract.TelemetrySummaryReply, error) {
	w := contract.TelemetryWindow("1h")
	if r.Window != nil {
		w = *r.Window
	}
	secs, ok := windowSeconds[w]
	if !ok {
		return contract.TelemetrySummaryReply{}, fail(400, "window must be 5m, 1h, 24h or 7d")
	}
	out := contract.TelemetrySummaryReply{Window: w, Enabled: spanStore != nil, Operations: []contract.OperationStats{},
		Hosts: []contract.HostReading{}, Processes: []contract.ProcessReading{}, Runs: []contract.RunReading{}}
	if spanWriter != nil {
		out.DroppedSpans = spanWriter.Dropped()
	}
	if spanStore == nil {
		return out, nil
	}
	params := map[string]string{"w": strconv.Itoa(secs)}
	where := spanScope(q.person, params)
	if r.ProjectId != nil {
		params["project"] = *r.ProjectId
		where += " AND project_id = {project:String}"
	}
	if r.Service != nil {
		params["service"] = *r.Service
		where += " AND service = {service:String}"
	}
	fail := func(err error) (contract.TelemetrySummaryReply, error) {
		out.Error = ptr("span store: " + err.Error())
		return out, nil
	}
	var ops []struct {
		Service, Name                  string
		Count, Errors, Refused, PC, PE int64
		P50, P95                       float64
	}
	if err := spanStore.Query(q.ctx(), `SELECT service AS Service, name AS Name,
	countIf(start >= now64() - toIntervalSecond({w:UInt32})) AS Count,
	countIf(start >= now64() - toIntervalSecond({w:UInt32}) AND outcome = 'error') AS Errors,
	countIf(start >= now64() - toIntervalSecond({w:UInt32}) AND outcome = 'refused') AS Refused,
	countIf(start < now64() - toIntervalSecond({w:UInt32})) AS PC,
	countIf(start < now64() - toIntervalSecond({w:UInt32}) AND outcome = 'error') AS PE,
	quantileIf(0.5)(duration_ns, start >= now64() - toIntervalSecond({w:UInt32})) / 1e6 AS P50,
	quantileIf(0.95)(duration_ns, start >= now64() - toIntervalSecond({w:UInt32})) / 1e6 AS P95
	FROM span WHERE start >= now64() - toIntervalSecond(2 * {w:UInt32}) AND kind IN ('server', 'client', 'internal', 'pass') AND `+where+`
	GROUP BY service, name HAVING Count > 0 ORDER BY service, name`, params, &ops); err != nil {
		return fail(err)
	}
	for _, o := range ops {
		out.Operations = append(out.Operations, contract.OperationStats{Service: o.Service, Name: o.Name, Count: o.Count, Errors: o.Errors,
			Refused: o.Refused, P50Ms: nanTo0(o.P50), P95Ms: nanTo0(o.P95), PreviousCount: ptr(o.PC), PreviousErrors: ptr(o.PE)})
	}
	if fullAccess(q.person) {
		var hs []struct {
			HostID, Version, At      string
			Load1, MemFree, DiskFree *float64
			Cpus, MemTotal           string
		}
		if err := spanStore.Query(q.ctx(), `SELECT host_id AS HostID, argMax(version, start) AS Version, toString(max(start)) AS At,
		argMax(load1, start) AS Load1, argMax(mem_free, start) AS MemFree, argMax(disk_free, start) AS DiskFree,
		argMax(attrs['fleet.cpus'], start) AS Cpus, argMax(attrs['fleet.mem_total'], start) AS MemTotal
		FROM span WHERE name = 'host.heartbeat' AND start >= now64() - toIntervalDay(7) GROUP BY host_id ORDER BY host_id`, nil, &hs); err != nil {
			return fail(err)
		}
		for _, h := range hs {
			row := contract.HostReading{HostId: h.HostID, At: chTime(h.At), Load1: h.Load1, MemFree: h.MemFree, DiskFree: h.DiskFree}
			if h.Version != "" {
				row.Version = ptr(h.Version)
			}
			if n, err := strconv.ParseInt(h.Cpus, 10, 64); err == nil {
				row.Cpus = &n
			}
			if n, err := strconv.ParseFloat(h.MemTotal, 64); err == nil {
				row.MemTotal = &n
			}
			out.Hosts = append(out.Hosts, row)
		}
		var ps []struct {
			HostID, Process, At string
			CPU                 *float64
			RSS                 *int64
			Files               *int64
		}
		if err := spanStore.Query(q.ctx(), `SELECT host_id AS HostID, attrs['fleet.process'] AS Process, toString(max(start)) AS At,
		argMax(cpu_seconds, start) AS CPU, argMax(rss_bytes, start) AS RSS, argMax(open_files, start) AS Files
		FROM span WHERE name = 'process.reading' AND start >= now64() - toIntervalDay(1) GROUP BY host_id, Process ORDER BY host_id, Process`, nil, &ps); err != nil {
			return fail(err)
		}
		for _, p := range ps {
			out.Processes = append(out.Processes, contract.ProcessReading{HostId: p.HostID, Process: p.Process, At: chTime(p.At), CpuSeconds: p.CPU, RssBytes: p.RSS, OpenFiles: p.Files})
		}
	}
	var rs []struct {
		ExecutionID, HostID, ProjectID, At, Outcome, TraceID, Orphans string
		DurationMs                                                    float64
		CPU                                                           *float64
		Peak                                                          *int64
	}
	if err := spanStore.Query(q.ctx(), `SELECT assumeNotNull(execution_id) AS ExecutionID, host_id AS HostID, ifNull(project_id, '') AS ProjectID,
	toString(start) AS At, toString(outcome) AS Outcome, trace_id AS TraceID, attrs['fleet.orphans'] AS Orphans,
	duration_ns / 1e6 AS DurationMs, cpu_seconds AS CPU, peak_rss_bytes AS Peak
	FROM span WHERE name = 'execution' AND execution_id IS NOT NULL AND start >= now64() - toIntervalSecond({w:UInt32}) AND `+where+`
	ORDER BY start DESC LIMIT 50`, params, &rs); err != nil {
		return fail(err)
	}
	for _, x := range rs {
		row := contract.RunReading{ExecutionId: x.ExecutionID, HostId: ptr(x.HostID), At: chTime(x.At), DurationMs: ptr(x.DurationMs),
			Outcome: ptr(contract.SpanOutcome(x.Outcome)), CpuSeconds: x.CPU, PeakRssBytes: x.Peak, TraceId: ptr(x.TraceID)}
		if x.ProjectID != "" {
			row.ProjectId = ptr(x.ProjectID)
		}
		if n, err := strconv.ParseInt(x.Orphans, 10, 64); err == nil {
			row.Orphans = &n
		}
		out.Runs = append(out.Runs, row)
	}
	return out, nil
}

func nanTo0(f float64) float64 {
	if f != f {
		return 0
	}
	return f
}

// chTime: a ClickHouse DateTime64 text (UTC) as RFC 3339.
func chTime(s string) string {
	if t, err := time.Parse("2006-01-02 15:04:05.999999", s); err == nil {
		return t.UTC().Format(time.RFC3339Nano)
	}
	return s
}

func telemetryTrace(q *Request, r *contract.TelemetryTraceRequest) (contract.TelemetryTraceReply, error) {
	out := contract.TelemetryTraceReply{Spans: []contract.Span{}}
	if len(r.TraceId) != 32 || strings.Trim(r.TraceId, "0123456789abcdef") != "" {
		return out, fail(400, "traceId must be 32 lower-case hex characters")
	}
	if spanStore == nil {
		return out, nil
	}
	params := map[string]string{"t": r.TraceId}
	where := spanScope(q.person, params)
	var rows []struct {
		TraceID, SpanID, Parent, Service, HostID, Name, Kind, Start, Outcome, ErrorClass, ExecutionID string
		DurationMs                                                                                    float64
		CPU                                                                                           *float64
		RSS, Peak                                                                                     *int64
		Attrs                                                                                         map[string]string
		Visible                                                                                       uint8
	}
	if err := spanStore.Query(q.ctx(), `SELECT trace_id AS TraceID, span_id AS SpanID, parent_span_id AS Parent, service AS Service, host_id AS HostID,
	name AS Name, toString(kind) AS Kind, toString(start) AS Start, toString(outcome) AS Outcome, error_class AS ErrorClass,
	ifNull(execution_id, '') AS ExecutionID, duration_ns / 1e6 AS DurationMs, cpu_seconds AS CPU, rss_bytes AS RSS, peak_rss_bytes AS Peak,
	attrs AS Attrs, (`+where+`) AS Visible
	FROM span WHERE trace_id = {t:String} ORDER BY start LIMIT 10000`, params, &rows); err != nil {
		return out, fail(503, "Span store: "+err.Error())
	}
	for _, x := range rows {
		if x.Visible == 0 {
			out.Hidden++
			continue
		}
		s := contract.Span{TraceId: x.TraceID, SpanId: x.SpanID, Service: x.Service, HostId: ptr(x.HostID), Name: x.Name, Kind: contract.SpanKind(x.Kind),
			Start: chTime(x.Start), DurationMs: x.DurationMs, Outcome: contract.SpanOutcome(x.Outcome), CpuSeconds: x.CPU, RssBytes: x.RSS, PeakRssBytes: x.Peak, Attrs: x.Attrs}
		if x.Parent != "" {
			s.ParentSpanId = ptr(x.Parent)
		}
		if x.ErrorClass != "" {
			s.ErrorClass = ptr(x.ErrorClass)
		}
		if x.ExecutionID != "" {
			s.ExecutionId = ptr(x.ExecutionID)
		}
		out.Spans = append(out.Spans, s)
	}
	return out, nil
}
