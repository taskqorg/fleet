// Rules pass (docs/designs/fleet-telemetry.md §6, stage 1): once a minute, each rule reads spans (ClickHouse) or state
// (Postgres) and returns its findings; findings become problem: cause questions through problemsSync, absence causes
// absorb the findings that depend on them, and telemetry_findings shows the last pass.
package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"fleet/obs"

	"github.com/jackc/pgx/v5"
)

// Rule factors (settings rules.<rule>.*; these are the defaults, tuned from the first week of data).
var (
	rulesEvery          = time.Minute
	ruleHostSilent      = 3 * time.Minute
	rulePassMisses      = 3
	ruleStuck           = 30 * time.Minute
	ruleSpikeRatio      = 0.5
	ruleSpikeCalls      = 10
	ruleSpikeWindow     = 5 * time.Minute
	ruleSaturationBeats = 5
	ruleMemFreeShare    = 0.05
	ruleSwapUsedShare   = 0.8
	telemetryStale      = 3 * time.Minute
	ruleDroppedFor      = 10 * time.Minute
	problemCleanRuns    = 2 // problems.cleanRuns
)

var (
	rulesMu        sync.Mutex
	rulesLast      time.Time
	rulesFindings  []contract.Finding
	rulesSince     = map[string]string{} // cause to when it first fired, while it keeps firing
	rulesDropped   int64                 // the writer's drop count when it last rose
	rulesDroppedAt time.Time             // when it last rose
	passIntervals  sync.Map              // pass name to its period, filled by every
)

func init() {
	onStart(func(ctx context.Context, a *App) {
		every(ctx, "rules", envMs("FLEET_RULES_TICK_MS", int(rulesEvery/time.Millisecond)), func(ctx context.Context) error { return rulesPass(ctx, a) })
	})
	op("telemetry_findings", telemetryFindings)
}

// rulesPass runs every rule, groups the findings and syncs them as questions. With no span store the span rules are
// skipped; with the store refusing, every rule is skipped (problem:telemetry:clickhouse covers it).
func rulesPass(ctx context.Context, a *App) error {
	// The Postgres rules run even when the span store fails; its own question (problem:telemetry:clickhouse) covers that.
	more, active, err := stateRules(ctx, a)
	if err != nil {
		return err
	}
	var fs []contract.Finding
	var spanErr error
	if spanStore != nil {
		fs, spanErr = spanRules(ctx)
	}
	if spanErr != nil {
		// The span rules could not run: their findings of the last pass stand, so the sync neither closes their
		// questions nor resets their since; the Postgres rules still apply.
		rulesMu.Lock()
		for _, f := range rulesFindings {
			if spanRule[f.Rule] {
				fs = append(fs, f)
			}
		}
		rulesMu.Unlock()
	}
	fs = groupFindings(append(fs, more...), active)
	rulesMu.Lock()
	now := time.Now().UTC().Format(time.RFC3339)
	since := map[string]string{}
	for i := range fs {
		s := rulesSince[fs[i].Cause]
		if s == "" {
			s = now
		}
		since[fs[i].Cause], fs[i].Since = s, s
	}
	rulesSince = since
	rulesMu.Unlock()
	var ps []Problem
	var execs []*string // the finding's execution of each problem in ps
	for _, f := range fs {
		if f.GroupCause != nil {
			continue
		}
		txt := f.Cause
		if f.Text != nil {
			txt = *f.Text
		}
		data := ruleProblemData{Exemplars: f.Exemplars, Value: f.Value, HostID: f.HostId, StoreID: f.StoreId, ExecutionID: f.ExecutionId}
		p := Problem{Cause: f.Cause, Text: txt, Rule: f.Rule, RefreshText: f.Rule == "resource-failing", Data: data}
		if f.ProjectId != nil {
			p.ProjectID = *f.ProjectId
		}
		ps = append(ps, p)
		execs = append(execs, f.ExecutionId)
	}
	needDirector := false
	for i := range ps {
		if execs[i] != nil && ps[i].ProjectID != "" {
			needDirector = true
			break
		}
	}
	if needDirector {
		if err := a.store.Read(ctx, nil, func(q *Request) error {
			for i := range ps {
				if execs[i] != nil && ps[i].ProjectID != "" {
					// A project cause goes to the project's director: the execution's directedBy.
					by, err := q.db().RulesExecutionDirector(q.ctx(), *execs[i])
					if err != nil && !errors.Is(err, pgx.ErrNoRows) {
						return err
					}
					ps[i].AddressedTo = deref(by)
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	if err := a.store.Write(ctx, nil, []string{"problems:rules"}, func(q *Request) error {
		return problemsSync(q, "rules", ps)
	}); err != nil {
		return err
	}
	rulesMu.Lock()
	rulesLast, rulesFindings = time.Now(), fs
	rulesMu.Unlock()
	return spanErr
}

// ruleProblemData: the data of a rule finding's problem (Problem.Data, problems.go, is still a map).
type ruleProblemData struct {
	Exemplars   []string `json:"exemplars"`
	Value       float64  `json:"value"`
	HostID      *string  `json:"hostId,omitempty"`
	StoreID     *string  `json:"storeId,omitempty"`
	ExecutionID *string  `json:"executionId,omitempty"`
}

func text(f *contract.Finding, s string) { f.Text = &s }

var spanRule = map[string]bool{"absence-host": true, "absence-pass": true, "error-spike": true, "host-saturation": true}

// spanRules: absence-host, absence-pass, error-spike, host-saturation over the span table.
func spanRules(ctx context.Context) ([]contract.Finding, error) {
	var out []contract.Finding
	// absence-host: a host that ever sent a heartbeat and none for ruleHostSilent. Removed and paused hosts
	// are dropped in stateRules' host filter (hostsActive).
	var hosts []struct {
		HostID string
		Age    float64
	}
	if err := spanStore.Query(ctx, `SELECT host_id AS HostID, dateDiff('second', max(start), now64()) AS Age FROM span
	WHERE service = 'executor' AND name = 'host.heartbeat' GROUP BY host_id`, nil, &hosts); err != nil {
		return nil, err
	}
	for _, h := range hosts {
		if h.Age >= ruleHostSilent.Seconds() {
			f := contract.Finding{Rule: "absence-host", Subject: h.HostID, Value: h.Age, Cause: "problem:host:" + h.HostID + ":silent", HostId: ptr(h.HostID), Exemplars: []string{}}
			text(&f, fmt.Sprintf("Host %s has sent no heartbeat for %d minutes.", h.HostID, int(h.Age/60)))
			out = append(out, f)
		}
	}
	// A single-flight pass publishes its span at completion and waits its interval before running again.
	// Measure silence from that completion, since the span's start includes time the pass was working.
	var passes []struct {
		Name, TraceID string
		Age, Duration float64
	}
	if err := spanStore.Query(ctx, `SELECT name AS Name, dateDiff('millisecond', max(start), now64()) / 1000 AS Age,
	argMax(duration_ns, start) / 1e9 AS Duration, argMax(trace_id, start) AS TraceID FROM span
	WHERE service = 'coordinator' AND name LIKE 'pass.%' AND kind = 'pass' AND host_id = {h:String} GROUP BY name`, map[string]string{"h": obs.HostID}, &passes); err != nil {
		return nil, err
	}
	for _, p := range passes {
		d, ok := passIntervals.Load(strings.TrimPrefix(p.Name, "pass."))
		if !ok || p.Name == "pass.rules" {
			continue
		}
		// A restart stops every pass for a while: silence before this process started is the deploy, not a stuck pass
		// (2026-11-24: each coordinator release opened pass.dispatch, pass.lost and pass.watch questions).
		silence := min(p.Age-p.Duration, float64(nowMs()-processStarted)/1000)
		if limit := d.(time.Duration) * time.Duration(rulePassMisses); silence >= limit.Seconds() && limit > 0 {
			f := contract.Finding{Rule: "absence-pass", Subject: p.Name, Value: silence, Cause: "problem:absence:" + p.Name, Exemplars: []string{p.TraceID}}
			text(&f, fmt.Sprintf("Coordinator pass %s last completed %d seconds ago.", p.Name, int(silence)))
			out = append(out, f)
		}
	}
	// error-spike: error ratio above ruleSpikeRatio over ruleSpikeWindow with at least ruleSpikeCalls calls; client spans
	// are per server.address. Spans hosts post are grouped per host, so a spike is the reporting host's and folds into its
	// absence.
	var spikes []struct {
		Service, Name, HostID string
		Calls, Errors         uint64
		Exemplars             []string
	}
	if err := spanStore.Query(ctx, `SELECT service AS Service, if(kind = 'client', concat(name, ' ', attrs['server.address']), name) AS Name,
	if(service = 'coordinator', '', host_id) AS HostID, count() AS Calls, countIf(outcome = 'error') AS Errors, groupArrayIf(3)(trace_id, outcome = 'error') AS Exemplars
	FROM span WHERE start >= now64() - toIntervalSecond({w:UInt32}) AND kind IN ('server', 'client', 'internal', 'pass')
	GROUP BY service, Name, HostID HAVING Calls >= {n:UInt32} AND Errors / Calls > {r:Float64}`,
		map[string]string{"w": strconv.Itoa(int(ruleSpikeWindow.Seconds())), "n": strconv.Itoa(ruleSpikeCalls), "r": strconv.FormatFloat(ruleSpikeRatio, 'f', -1, 64)}, &spikes); err != nil {
		return nil, err
	}
	for _, s := range spikes {
		ratio := float64(s.Errors) / float64(s.Calls)
		name := cleanName(s.Name)
		cause := "problem:error-spike:" + s.Service + ":" + name
		where := ""
		if s.HostID != "" {
			cause, where = cause+"@"+s.HostID, " on "+s.HostID
		}
		f := contract.Finding{Rule: "error-spike", Subject: s.Service + " " + name, Value: ratio, Baseline: ptr(ruleSpikeRatio), Cause: cause, Exemplars: s.Exemplars}
		if s.HostID != "" {
			f.HostId = ptr(s.HostID)
		}
		text(&f, fmt.Sprintf("%s %s%s: %d of %d calls failed in the last %d minutes.", s.Service, name, where, s.Errors, s.Calls, int(ruleSpikeWindow.Minutes())))
		out = append(out, f)
	}
	// host-saturation: the last ruleSaturationBeats heartbeats all have load1 above the CPU count, swap use above
	// ruleSwapUsedShare of swap total, or memory free below ruleMemFreeShare of the total
	// (docs/designs/fleet-host-cleanup.md#6-host-saturation). The text names the latest beat's swap and its top three
	// consumers (fleet.top), as hostOverloadReason does for dispatch.
	var sat []struct {
		HostID              string
		Beats               uint64
		Hot                 uint64
		MaxLoad             float64
		Exemplar            string
		SwapUsed, SwapTotal float64
		Top                 string
	}
	if err := spanStore.Query(ctx, `SELECT host_id AS HostID, count() AS Beats,
	countIf(load1 > toFloat64OrZero(attrs['fleet.cpus']) OR mem_free < {m:Float64} * toFloat64OrZero(attrs['fleet.mem_total'])
		OR toFloat64OrZero(attrs['fleet.swap_used']) > {s:Float64} * toFloat64OrZero(attrs['fleet.swap_total']) AND toFloat64OrZero(attrs['fleet.swap_total']) > 0) AS Hot,
	max(ifNull(load1, 0)) AS MaxLoad, any(trace_id) AS Exemplar,
	argMax(toFloat64OrZero(attrs['fleet.swap_used']), start) AS SwapUsed, argMax(toFloat64OrZero(attrs['fleet.swap_total']), start) AS SwapTotal,
	argMax(attrs['fleet.top'], start) AS Top
	FROM (SELECT * FROM span WHERE name = 'host.heartbeat' AND start >= now64() - toIntervalMinute(30) ORDER BY start DESC LIMIT {b:UInt32} BY host_id)
	GROUP BY host_id`, map[string]string{"m": strconv.FormatFloat(ruleMemFreeShare, 'f', -1, 64), "s": strconv.FormatFloat(ruleSwapUsedShare, 'f', -1, 64), "b": strconv.Itoa(ruleSaturationBeats)}, &sat); err != nil {
		return nil, err
	}
	for _, s := range sat {
		if s.Beats >= uint64(ruleSaturationBeats) && s.Hot == s.Beats {
			f := contract.Finding{Rule: "host-saturation", Subject: s.HostID, Value: s.MaxLoad, Cause: "problem:host:" + s.HostID + ":saturated", HostId: ptr(s.HostID), Exemplars: []string{s.Exemplar}}
			text(&f, saturationText(s.HostID, s.SwapUsed, s.SwapTotal, s.Top))
			out = append(out, f)
		}
	}
	return out, nil
}

// saturationText: the host-saturation finding, with swap when above ruleSwapUsedShare and the top three consumers of
// the heartbeat's fleet.top.
func saturationText(hostID string, swapUsed, swapTotal float64, top string) string {
	t := fmt.Sprintf("Host %s is saturated: CPU load above its cores, swap above %d%% or memory below %d%% for %d heartbeats", hostID, int(ruleSwapUsedShare*100), int(ruleMemFreeShare*100), ruleSaturationBeats)
	if swapTotal > 0 && swapUsed > ruleSwapUsedShare*swapTotal {
		t += fmt.Sprintf("; swap %s/%s GB", gb(swapUsed), gb(swapTotal))
	}
	if c := topConsumers(parseHostTop(top), 3); c != "" {
		t += "; top: " + c
	}
	return t + "."
}

// stateRules: rules over Postgres state (store-fill, absence-store, stuck, unknown) and the host filter of the span rules.
func stateRules(ctx context.Context, a *App) ([]contract.Finding, map[string]bool, error) {
	var out []contract.Finding
	active := map[string]bool{}
	err := a.store.Read(ctx, nil, func(q *Request) error {
		stores, err := q.db().RulesStoresDown(q.ctx())
		if err != nil {
			return err
		}
		bytes := func(v *int64) float64 {
			if v == nil {
				return 0
			}
			return float64(*v)
		}
		for _, s := range stores {
			id, host := s.ID, s.HostID
			f := contract.Finding{Subject: id, StoreId: ptr(id), HostId: ptr(host), Exemplars: []string{}}
			if s.State == "full" {
				f.Rule, f.Cause = "store-fill", "problem:store:"+id+":fill"
				f.Value = bytes(s.CapacityBytes) // capacity left (store observe), compared with the warning level
				f.Baseline = ptr(bytes(s.FreeSpaceWarningBytes))
				text(&f, fmt.Sprintf("Store %s on %s has less free space than its warning level.", id, host))
			} else {
				f.Rule, f.Cause = "absence-store", "problem:store:"+id+":offline"
				text(&f, fmt.Sprintf("Store %s on %s has not reported as reachable.", id, host))
			}
			out = append(out, f)
		}
		stuck, err := q.db().RulesStuckExecutions(q.ctx(), ruleStuck.Seconds())
		if err != nil {
			return err
		}
		for _, s := range stuck {
			if s.Idle < ruleStuck.Seconds() {
				continue
			}
			id := s.ID
			f := contract.Finding{Rule: "stuck", Subject: id, Value: s.Idle, Cause: "problem:execution:" + id + ":stuck", ExecutionId: ptr(id), Exemplars: []string{}}
			if h := deref(s.HostID); h != "" {
				f.HostId = ptr(h)
			}
			if p := deref(s.ProjectID); p != "" {
				f.ProjectId = ptr(p)
			}
			text(&f, fmt.Sprintf("Execution %s has shown no activity for %d minutes.", id, int(s.Idle/60)))
			out = append(out, f)
		}
		// resource-failing: resource work whose retry pause reached its maximum (work_kit.go): an execution's work, a
		// project build or a metadata repair. One system question names them; it closes once they succeed.
		failing, err := q.db().RulesResourceFailing(q.ctx(), int32(kitFailingAttempts()))
		if err != nil {
			return err
		}
		items := append([]string{}, failing...)
		if len(items) > 0 {
			f := contract.Finding{Rule: "resource-failing", Subject: "resource processor", Value: float64(len(items)), Cause: "problem:resource:failing", Exemplars: []string{}}
			noun := "items"
			if len(items) == 1 {
				noun = "item"
			}
			text(&f, fmt.Sprintf("Resource processing keeps failing for %d %s (%s). Retry pauses are up to %s.", len(items), noun, strings.Join(items, ", "), kitRetryEvery()))
			out = append(out, f)
		}
		// work-stalled: a queue of this process's work kit whose own bookkeeping (selection, failure writes) keeps
		// failing, which no item's attempts can show.
		if f, ok := workStalled(a); ok {
			out = append(out, f)
		}
		// Hosts the span rules may name: not removed, intake not paused.
		hosts, err := q.db().DispatchableHostIDs(q.ctx())
		if err != nil {
			return err
		}
		for _, h := range hosts {
			active[h] = true
		}
		return nil
	})
	// unknown: spans were dropped within ruleDroppedFor; the question stays while drops go on.
	if spanWriter != nil {
		if d := spanWriter.Dropped(); d > rulesDropped {
			rulesDropped, rulesDroppedAt = d, time.Now()
		}
		if !rulesDroppedAt.IsZero() && time.Since(rulesDroppedAt) < ruleDroppedFor {
			f := contract.Finding{Rule: "unknown", Subject: "dropped-spans", Value: float64(rulesDropped), Cause: "problem:unknown:dropped-spans", Exemplars: []string{}}
			text(&f, fmt.Sprintf("Telemetry dropped spans (%d since start); the last drop was at %s.", rulesDropped, rulesDroppedAt.UTC().Format(time.RFC3339)))
			out = append(out, f)
		}
	}
	return out, active, err
}

// cleanName: an operation name (and a client span's server.address) as it goes into a cause and a question: the span
// name character set (ASCII letters, digits and " _.:/{}@+-"), others replaced by "_", at most 120 bytes.
func cleanName(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(" _.:/{}@+-", c) >= 0) {
			b[i] = '_'
		}
	}
	if len(b) > 120 {
		b = b[:120]
	}
	return string(b)
}

// groupFindings (Alertmanager-style inhibition): findings on a removed or paused host are dropped (active: the hosts
// that may be named), a silent host absorbs every other finding on that host, and causes are unique.
func groupFindings(fs []contract.Finding, active map[string]bool) []contract.Finding {
	silent, seen := map[string]string{}, map[string]bool{}
	var kept []contract.Finding
	for _, f := range fs {
		if seen[f.Cause] || (f.HostId != nil && f.Rule != "stuck" && f.StoreId == nil && !active[*f.HostId]) {
			continue
		}
		seen[f.Cause] = true
		kept = append(kept, f)
		if f.Rule == "absence-host" {
			silent[*f.HostId] = f.Cause
		}
	}
	for i := range kept {
		if f := &kept[i]; f.HostId != nil && f.Rule != "absence-host" && silent[*f.HostId] != "" {
			f.GroupCause = ptr(silent[*f.HostId])
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Cause < kept[j].Cause })
	return kept
}

func telemetryFindings(q *Request, r *contract.TelemetryFindingsRequest) (contract.TelemetryFindingsReply, error) {
	rulesMu.Lock()
	defer rulesMu.Unlock()
	out := contract.TelemetryFindingsReply{Findings: []contract.Finding{}}
	if !rulesLast.IsZero() {
		out.RulesLastRun = ptr(rulesLast.UTC().Format(time.RFC3339))
		out.Stale = ptr(time.Since(rulesLast) > telemetryStale)
	}
	full := fullAccess(q.person)
	for _, f := range rulesFindings {
		if r.ProjectId != nil && (f.ProjectId == nil || *f.ProjectId != *r.ProjectId) {
			continue
		}
		// System findings need full access; project findings the project grant.
		if !full && (f.ProjectId == nil || !mayAccessProjectID(q.person, *f.ProjectId)) {
			continue
		}
		out.Findings = append(out.Findings, f)
	}
	return out, nil
}
