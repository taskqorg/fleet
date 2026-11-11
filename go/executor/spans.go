// Executor telemetry (docs/designs/fleet-telemetry.md): spans go to <data>/spans/current.jsonl and are shipped to
// POST /api/hosts/spans; one host.heartbeat check span per minute with host readings, and one process.reading per Fleet
// process under it; one execution span per finished run with the CPU and peak memory of its process tree.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"fleet/contract"
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

func startTelemetry(root, url, token, host string) {
	obs.Service, obs.HostID, obs.Version = "executor", host, version
	spanDir = filepath.Join(root, "spans")
	if os.MkdirAll(spanDir, 0o700) != nil {
		return
	}
	obs.SetSink(appendSpan)
	go func() {
		prev := map[string]float64{}
		for t := time.Now(); ; t = time.Now() {
			heartbeat(root, prev)
			shipSpans(url, token)
			time.Sleep(time.Until(t.Add(time.Minute)))
		}
	}()
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

// shipSpans: current.jsonl becomes a pending file; each pending file goes in batches of up to 1000 with a stable batch
// id (a retried batch is deduplicated by the coordinator), and is removed when every batch was accepted.
func shipSpans(url, token string) {
	spanMu.Lock()
	cur := filepath.Join(spanDir, "current.jsonl")
	if st, err := os.Stat(cur); err == nil && st.Size() > 0 {
		os.Rename(cur, filepath.Join(spanDir, fmt.Sprintf("pending-%d.jsonl", time.Now().UnixNano())))
	}
	spanMu.Unlock()
	files, _ := filepath.Glob(filepath.Join(spanDir, "pending-*.jsonl"))
	sort.Strings(files)
	for _, f := range files {
		if !shipFile(url, token, f) {
			return // the coordinator or the store is down: retry next minute
		}
		os.Remove(f)
	}
}

func shipFile(url, token, file string) bool {
	fh, err := os.Open(file)
	if err != nil {
		return false
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<16), 1<<22)
	base := strings.TrimSuffix(filepath.Base(file), ".jsonl")
	var batch []json.RawMessage
	n := 0
	send := func() bool {
		if len(batch) == 0 {
			return true
		}
		_, err := post(url, token, "/api/hosts/spans", contract.SpanBatchRequest{BatchId: base + "-" + strconv.Itoa(n), Spans: batch})
		batch, n = nil, n+1
		return err == nil
	}
	for sc.Scan() {
		if !json.Valid(sc.Bytes()) {
			continue // a torn line: skipped
		}
		batch = append(batch, append(json.RawMessage(nil), sc.Bytes()...))
		if len(batch) == 1000 && !send() {
			return false
		}
	}
	return send()
}

// heartbeat writes host.heartbeat with host readings and, as its children, one process.reading per running Fleet
// process. prev holds each process's CPU seconds at the previous beat.
func heartbeat(root string, prev map[string]float64) {
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
	}
	if table, err := processes(); err == nil {
		names := map[int]string{}
		if out, err := exec.Command("/bin/ps", "-axo", "pid=,comm=").Output(); err == nil {
			for _, l := range strings.Split(string(out), "\n") {
				if f := strings.Fields(l); len(f) >= 2 {
					pid, _ := strconv.Atoi(f[0])
					names[pid] = filepath.Base(strings.Join(f[1:], " "))
				}
			}
		}
		type sum struct {
			rss uint64
			cpu float64
		}
		sums := map[string]*sum{}
		for _, p := range table {
			for name, comms := range fleetProcesses {
				for _, c := range comms {
					if names[p.pid] == c {
						if sums[name] == nil {
							sums[name] = &sum{}
						}
						sums[name].rss += p.rssKB * 1024
						sums[name].cpu += p.cpu
					}
				}
			}
		}
		for name, s := range sums {
			_, r := obs.Start(ctx, "process.reading", obs.KindCheck)
			r.Set("fleet.process", name)
			rss := s.rss
			r.RSSBytes = &rss
			if p, ok := prev[name]; ok && s.cpu >= p {
				d := s.cpu - p
				r.CPUSeconds = &d
			} else {
				r.Set("missing.cpu_seconds", "first beat")
			}
			prev[name] = s.cpu
			r.Set("missing.open_files", "not collected")
			r.End(nil)
		}
	}
	hb.End(nil)
}

// executionSpan: one execution span for a finished run, with the CPU seconds and peak resident memory of its owned process tree.
func executionSpan(execution string, l *live) {
	ctx := obs.WithRun(context.Background(), obs.Run{ExecutionID: execution})
	_, s := obs.StartAt(ctx, "execution", obs.KindInternal, l.started)
	s.Set("fleet.harness", harnessName(jAt(l.command, "payload")))
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
	if l.state != nil && !l.state.Success() {
		s.Fail("internal")
	}
	if l.stopped {
		s.Outcome = obs.Cancelled
	}
	s.End(nil)
}
