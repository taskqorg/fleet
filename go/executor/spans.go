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
	s.End(nil)
}
