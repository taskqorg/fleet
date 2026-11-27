// Executor telemetry (docs/designs/fleet-telemetry.md): spans go to <data>/spans/current.jsonl and leave through the
// outbox to POST /api/hosts/spans; one host.heartbeat check span per minute with host readings, and one process.reading per Fleet
// process under it; one execution span per finished run with the CPU and peak memory of its process tree.
package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"fleet/contract"
	"fleet/errtrack"
	"fleet/obs"
)

// fleetProcesses: process name (fleet.process) to the ps command names that count as it (setting telemetry.processUnits).
var fleetProcesses = map[string][]string{
	"coordinator": {"fleet-coordinator"}, "gateway": {"fleet-gateway"}, "executor": {"fleet-executor"}, "fleet-update": {"fleet-update"},
	"postgres": {"postgres"}, "clickhouse": {"clickhouse-server", "clickhouse"}, "garage": {"garage"}, "cloudflared": {"cloudflared"},
}

var (
	spanMu  sync.Mutex
	spanDir string
)

func startTelemetry(root string, db *sql.DB, host string) {
	obs.Service, obs.HostID, obs.Version = "executor", host, version
	// Unexpected errors and panics go to Bugsink (docs/designs/fleet-error-tracking.md): FLEET_BUGSINK_DSN, else the
	// release default; none, no events.
	if err := errtrack.Start(errtrack.DSN(), "executor", host, version); err != nil {
		log.Printf("error tracking off: %v", err)
	}
	spanDir = filepath.Join(root, "spans")
	if os.MkdirAll(spanDir, 0o700) != nil {
		return
	}
	obs.SetSink(appendSpan)
	errtrack.Go("telemetry.minute", func() {
		prev := map[int]float64{}
		for t := time.Now(); ; t = time.Now() {
			heartbeat(root, prev)
			queueSpans(db)
			time.Sleep(time.Until(t.Add(time.Minute)))
		}
	})
}

// appendSpan: the sink. One line per span; a failed write loses the span (help, never block).
func appendSpan(s *obs.Span) {
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	spanMu.Lock()
	defer spanMu.Unlock()
	f, err := os.OpenFile(filepath.Join(spanDir, "current.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	f.Write(append(b, '\n'))
	f.Close()
}

// queueSpans: current.jsonl becomes a pending file; each pending file is queued in the outbox as batches of up to 1000
// spans with a stable batch id (a retried batch is deduplicated by the coordinator), and removed in the same
// transaction. The outbox delivers them like every other row; a batch the coordinator refuses for good goes to
// outbox_failed. A file with a line longer than the buffer becomes failed-*.jsonl: kept for a look, never queued.
func queueSpans(db *sql.DB) {
	spanMu.Lock()
	cur := filepath.Join(spanDir, "current.jsonl")
	if st, err := os.Stat(cur); err == nil && st.Size() > 0 {
		os.Rename(cur, filepath.Join(spanDir, fmt.Sprintf("pending-%d.jsonl", time.Now().UnixNano())))
	}
	spanMu.Unlock()
	files, _ := filepath.Glob(filepath.Join(spanDir, "pending-*.jsonl"))
	sort.Strings(files)
	for _, f := range files {
		batches, err := spanBatches(f)
		if err != nil {
			os.Rename(f, filepath.Join(spanDir, "failed-"+strings.TrimPrefix(filepath.Base(f), "pending-")))
			continue
		}
		if (dbExec{db}).tx(func(x execer) error {
			for _, b := range batches {
				if err := queueTo(x, contract.RouteSpans, b); err != nil {
					return err
				}
			}
			return nil
		}) != nil {
			return // the journal is busy or failing: the file waits for the next minute
		}
		os.Remove(f)
	}
}

var spansRoute = contract.RouteSpans.Path

func durationMs(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }

func toolSpanContext(db *sql.DB, command, execution string) context.Context {
	if command == "" || execution == "" {
		return nil
	}
	var bound, tp string
	if db.QueryRow(`SELECT json_extract(body,'$.executionId'), json_extract(body,'$.traceparent')
		FROM commands WHERE id=?`, command).Scan(&bound, &tp) != nil || bound != execution {
		return nil
	}
	if _, _, ok := obs.ParseTraceparent(tp); !ok {
		return nil
	}
	return obs.WithRun(obs.WithRemote(context.Background(), tp), obs.Run{ExecutionID: execution})
}

func setToolAttrs(s *obs.Span, route, operation, key string) {
	s.Set("http.route", route)
	if _, known := contract.Operations[operation]; known {
		s.Set("fleet.operation", operation)
	}
	// Project requests can use a caller-supplied id. Correlate without sending that value to telemetry.
	s.Set("fleet.request", wakeName(key))
}

func toolDeliverySpan(db *sql.DB, r outboxRow, body M) (context.Context, *obs.Span) {
	if r.route != contract.RouteAgentTools.Path && r.route != contract.RouteProjectOperations.Path {
		return context.Background(), nil
	}
	b := typed[struct {
		CommandId string `json:"commandId"`
		AgentKey  string `json:"agentToolKey"`
		Request   struct {
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		} `json:"request"`
		Delegation struct {
			Operation string `json:"operation"`
			RequestId string `json:"requestId"`
		} `json:"delegation"`
	}](body)
	var operation, key string
	switch r.route {
	case contract.RouteAgentTools.Path:
		operation, key = b.Request.Params.Name, b.AgentKey
	case contract.RouteProjectOperations.Path:
		operation, key = b.Delegation.Operation, b.CommandId+":"+b.Delegation.RequestId
	}
	if parent := toolSpanContext(db, b.CommandId, r.execution); parent != nil {
		ctx, s := obs.Start(parent, "fleet.tool.delivery", obs.KindClient)
		setToolAttrs(s, r.route, operation, key)
		if age := nowMs() - r.queuedAt.Int64; r.queuedAt.Valid && age >= 0 {
			s.Set("fleet.queue_ms", strconv.FormatInt(age, 10))
		} else {
			s.Set("missing.queue_ms", "enqueue time unavailable")
		}
		if r.queuedBehind.Valid {
			s.Set("fleet.queued_behind_execution_row", strconv.FormatBool(r.queuedBehind.Bool))
		}
		return ctx, s
	}
	return context.Background(), nil
}

func observeToolAnswer(s *obs.Span, a answer, route string, elapsed time.Duration) {
	s.Set("fleet.http_ms", durationMs(elapsed))
	if a.status != 0 {
		s.Set("http.response.status_code", strconv.Itoa(a.status))
	}
	switch a.kind {
	case answerOK:
		s.Set("fleet.transport", "answered")
		reply := typedRaw[struct {
			Error  jsonValue `json:"error"`
			Result struct {
				IsError bool `json:"isError"`
			} `json:"result"`
		}](a.value)
		if (reply.Error.Set && reply.Error.V != nil) || route == contract.RouteAgentTools.Path && reply.Result.IsError {
			s.Set("fleet.tool_outcome", "error")
		} else {
			s.Set("fleet.tool_outcome", "result")
		}
	case answerReject:
		s.Set("fleet.transport", "rejected")
		s.Outcome, s.ErrorClass = obs.HTTPOutcome(a.status, a.status == 403 || a.status == 409)
	case answerEdge:
		s.Set("fleet.transport", "edge")
		s.Fail("network")
	case answerRetry:
		s.Set("fleet.transport", "retry")
		switch {
		case a.timeout:
			s.Fail("timeout")
		case a.status == 0:
			s.Fail("network")
		default:
			s.Outcome, s.ErrorClass = obs.HTTPOutcome(a.status, false)
		}
	}
}

// An MCP process records a deadline independently of the delivery lane, including when the row has yet to send.
// Publish a complete pending file for the host's span spool to pick up. A telemetry failure cannot change the reply.
func recordToolPending(db *sql.DB, host, command, execution, route, operation, key string, started time.Time) {
	ctx := toolSpanContext(db, command, execution)
	if ctx == nil {
		return
	}
	_, s := obs.StartAt(ctx, "fleet.tool.wait", obs.KindInternal, started)
	s.Service, s.HostID, s.Version = "executor", host, version
	setToolAttrs(s, route, operation, key)
	s.Set("fleet.wait", "pending")
	s.Fail("timeout")
	s.End(nil)
	line, err := json.Marshal(s)
	if err != nil {
		return
	}
	dir := journalDir(db)
	if dir == "" {
		return
	}
	f, err := os.CreateTemp(filepath.Join(dir, "spans"), "tool-*")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append(line, '\n'))
	if closeErr := f.Close(); err == nil && closeErr == nil {
		os.Rename(f.Name(), filepath.Join(filepath.Dir(f.Name()), "pending-"+filepath.Base(f.Name())+".jsonl"))
	}
}

func spanBatches(file string) ([]contract.SpanBatchRequest, error) {
	fh, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<16), 1<<22)
	base := strings.TrimSuffix(filepath.Base(file), ".jsonl")
	var out []contract.SpanBatchRequest
	var batch []json.RawMessage
	cut := func() {
		if len(batch) > 0 {
			out = append(out, contract.SpanBatchRequest{BatchId: base + "-" + strconv.Itoa(len(out)), Spans: batch})
			batch = nil
		}
	}
	for sc.Scan() {
		if !json.Valid(sc.Bytes()) {
			continue // a torn line: skipped
		}
		batch = append(batch, append(json.RawMessage(nil), sc.Bytes()...))
		if len(batch) == 1000 {
			cut()
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	cut()
	return out, nil
}

// heartbeat writes host.heartbeat with host readings and, as its children, one process.reading per running Fleet
// process. prev holds each Fleet pid's CPU seconds at the previous beat.
func heartbeat(root string, prev map[int]float64) {
	ctx, hb := obs.Start(context.Background(), "host.heartbeat", obs.KindCheck)
	if l, ok := load1(); ok {
		hb.Load1 = &l
	} else {
		hb.Set("missing.load1", "unreadable")
	}
	hb.Set("fleet.cpus", strconv.Itoa(runtime.NumCPU()))
	if free, total, ok := memory(); ok {
		hb.MemFree = &free
		hb.Set("fleet.mem_total", strconv.FormatFloat(total, 'f', 0, 64))
	} else {
		hb.Set("missing.mem_free", "unreadable")
	}
	var pressure contract.HostLoad
	hostPressure(&pressure)
	for k, v := range map[string]*float64{"fleet.swap_used": pressure.SwapUsed, "fleet.swap_total": pressure.SwapTotal,
		"fleet.mem_compressed": pressure.MemCompressed, "fleet.tmp_used": pressure.TmpUsed, "fleet.tmp_total": pressure.TmpTotal} {
		if v != nil {
			hb.Set(k, strconv.FormatFloat(*v, 'f', 0, 64))
		}
	}
	if len(pressure.Top) > 0 {
		hb.Set("fleet.top", topAttr(pressure.Top))
	}
	var st unix.Statfs_t
	if unix.Statfs(root, &st) == nil {
		d := float64(st.Bavail) * float64(st.Bsize)
		hb.DiskFree = &d
	} else {
		hb.Set("missing.disk_free", "unreadable")
	}
	if table, err := processes(); err == nil {
		// CPU per beat is summed per pid (CPU now minus at the last beat, all of it for a pid started since), so a
		// child that exited does not pull the total down; its last minute is lost.
		type sum struct {
			rss uint64
			cpu float64
		}
		sums, seen := map[string]*sum{}, map[int]float64{}
		first := len(prev) == 0
		for _, p := range table {
			for name, comms := range fleetProcesses {
				for _, c := range comms {
					if p.name == c {
						if sums[name] == nil {
							sums[name] = &sum{}
						}
						sums[name].rss += p.rssKB * 1024
						sums[name].cpu += max(p.cpu-prev[p.pid], 0)
						seen[p.pid] = p.cpu
					}
				}
			}
		}
		for name, s := range sums {
			_, r := obs.Start(ctx, "process.reading", obs.KindCheck)
			r.Set("fleet.process", name)
			rss := s.rss
			r.RSSBytes = &rss
			if !first {
				d := s.cpu
				r.CPUSeconds = &d
			} else {
				r.Set("missing.cpu_seconds", "first beat")
			}
			r.Set("missing.open_files", "not collected")
			r.End(nil)
		}
		clear(prev)
		for pid, c := range seen {
			prev[pid] = c
		}
		if len(prev) == 0 {
			prev[0] = 0 // a beat happened: the next one is not the first
		}
	}
	hb.End(nil)
}

// executionSpan: one execution span for a finished run, with the CPU seconds and peak resident memory of its owned process tree.
func executionSpan(execution string, l *live) {
	if l.spanned {
		return // a later step of the finish failed and the run is finished again next tick
	}
	l.spanned = true
	c := l.command
	run, tp := contract.CommandRun{}, str0(c.Traceparent())
	if c.Run() != nil {
		run = *c.Run()
	}
	ctx := obs.WithRun(obs.WithRemote(context.Background(), tp), obs.Run{ExecutionID: execution, ProjectID: str0(run.ProjectId), TaskID: str0(run.TaskId), ExperimentID: str0(run.ExperimentId), AccountID: str0(run.AccountId), Actor: str0(run.Actor)})
	if l.workerExit != nil {
		_, s := obs.StartAt(ctx, "execution", obs.KindInternal, l.workerExit.StartedAt)
		if _, _, ok := obs.ParseTraceparent(tp); !ok {
			s.Set("missing.parent", "command trace context unavailable")
		}
		if c.Script != nil {
			s.Set("fleet.harness", "script")
		} else {
			s.Set("fleet.harness", commandHarness(l.command))
		}
		if l.workerExit.HasCPU {
			cpu := l.workerExit.CPUSeconds
			peak := l.workerExit.PeakRSS
			s.CPUSeconds = &cpu
			s.PeakRSSBytes = &peak
		} else {
			s.Set("missing.cpu_seconds", "no sample")
		}
		switch {
		case l.workerExit.TimedOut:
			s.Fail("timeout")
		case l.workerExit.Stopped:
			s.Outcome = obs.Cancelled
		case !l.workerExit.Succeeded || l.workerExit.CleanupUnknown:
			s.Fail("internal")
		}
		s.Set("fleet.orphans", strconv.Itoa(l.workerExit.Orphans))
		s.End(nil)
		return
	}
	_, s := obs.StartAt(ctx, "execution", obs.KindInternal, l.started)
	if _, _, ok := obs.ParseTraceparent(tp); !ok {
		s.Set("missing.parent", "command trace context unavailable")
	}
	if c.Script != nil {
		s.Set("fleet.harness", "script")
	} else {
		s.Set("fleet.harness", commandHarness(l.command))
	}
	cpu := 0.0
	for _, c := range l.cpu {
		cpu += c
	}
	if len(l.cpu) > 0 {
		s.CPUSeconds = &cpu
		peak := l.peakRSS
		s.PeakRSSBytes = &peak
	} else {
		s.Set("missing.cpu_seconds", "no sample")
	}
	switch {
	case l.timedOut:
		s.Fail("timeout")
	case l.stopped:
		s.Outcome = obs.Cancelled
	case l.state() == nil || !l.state().Success() || l.cleanupUnknown:
		s.Fail("internal")
	}
	s.Set("fleet.orphans", strconv.Itoa(l.orphans))
	s.End(nil)
}
