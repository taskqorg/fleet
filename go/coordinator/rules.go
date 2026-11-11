// Rules pass (docs/designs/fleet-telemetry.md §6, stage 1): once a minute, each rule reads spans (ClickHouse) or state
// (Postgres) and returns its findings; findings become problem: cause questions through problemsSync, absence causes
// absorb the findings that depend on them, and telemetry_findings shows the last pass.
package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"fleet/obs"
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
	telemetryStale      = 3 * time.Minute
	problemCleanRuns    = 2 // problems.cleanRuns
)

var (
	rulesMu       sync.Mutex
	rulesLast     time.Time
	rulesFindings []contract.Finding
	rulesSince    = map[string]string{} // cause to when it first fired, while it keeps firing
	rulesDropped  int64                 // the writer's drop count at the previous pass
	passIntervals sync.Map              // pass name to its period, filled by every
)

func init() {
	onStart(func(ctx context.Context, a *App) {
		every(ctx, "rules", envMs("FLEET_RULES_TICK_MS", int(rulesEvery/time.Millisecond)), func(ctx context.Context) error { return rulesPass(ctx, a) })
	})
	longOp("telemetry_findings", typedOp(telemetryFindings))
}

// rulesPass runs every rule, groups the findings and syncs them as questions. With no span store the span rules are
// skipped; with the store refusing, every rule is skipped (problem:telemetry:clickhouse covers it).
func rulesPass(ctx context.Context, a *App) error {
	var fs []contract.Finding
	var err error
	if spanStore != nil {
		if fs, err = spanRules(ctx); err != nil {
			return err
		}
	}
	more, err := stateRules(ctx, a)
	if err != nil {
		return err
	}
	fs = append(fs, more...)
	fs = groupFindings(fs)
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
	for _, f := range fs {
		if f.GroupCause != nil {
			continue
		}
		p := Problem{Cause: f.Cause, Text: astr(nonNil(f.Text, f.Cause)), Rule: f.Rule, Data: doc{"exemplars": f.Exemplars, "value": f.Value}}
		if f.HostId != nil {
			p.Data["hostId"] = *f.HostId
		}
		if f.StoreId != nil {
			p.Data["storeId"] = *f.StoreId
		}
		if f.ProjectId != nil {
			p.ProjectID = *f.ProjectId
		}
		if f.ExecutionId != nil {
			p.Data["executionId"] = *f.ExecutionId
		}
		ps = append(ps, p)
	}
	if err := runTx(ctx, a, nil, true, "", func(q *Request) error {
		for i := range ps {
			if id, ok := ps[i].Data["executionId"].(string); ok && ps[i].ProjectID != "" {
				// A project cause goes to the project's director: the execution's directedBy.
				r, err := first(q, "SELECT directed_by FROM execution WHERE id=$1", id)
				if err != nil {
					return err
				}
				if r != nil {
					ps[i].AddressedTo = astr(r["directed_by"])
				}
			}
		}
		return problemsSync(q, "rules", ps)
	}); err != nil {
		return err
	}
	rulesMu.Lock()
	rulesLast, rulesFindings = time.Now(), fs
	rulesMu.Unlock()
	return nil
}

func text(f *contract.Finding, s string) { f.Text = &s }

// spanRules: absence-host, absence-pass, error-spike, host-saturation over the span table.
func spanRules(ctx context.Context) ([]contract.Finding, error) {
	var out []contract.Finding
	// absence-host: a host that sent heartbeats in the last 7 days and none for ruleHostSilent. Removed and paused hosts
	// are dropped in stateRules' host filter (hostsActive).
	var hosts []struct {
		HostID string
		Age    float64
	}
	if err := spanStore.Query(ctx, `SELECT host_id AS HostID, dateDiff('second', max(start), now64()) AS Age FROM span
	WHERE name = 'host.heartbeat' AND start >= now64() - toIntervalDay(7) GROUP BY host_id`, nil, &hosts); err != nil {
		return nil, err
	}
	for _, h := range hosts {
		if h.Age >= ruleHostSilent.Seconds() {
			f := contract.Finding{Rule: "absence-host", Subject: h.HostID, Value: h.Age, Cause: "problem:host:" + h.HostID + ":silent", HostId: ptr(h.HostID), Exemplars: []string{}}
			text(&f, fmt.Sprintf("Host %s has sent no heartbeat for %d minutes.", h.HostID, int(h.Age/60)))
			out = append(out, f)
		}
	}
	// absence-pass: a coordinator pass with no span for rulePassMisses of its intervals (this coordinator's passes only).
	var passes []struct {
		Name string
		Age  float64
	}
	if err := spanStore.Query(ctx, `SELECT name AS Name, dateDiff('second', max(start), now64()) AS Age FROM span
	WHERE service = 'coordinator' AND kind = 'pass' AND host_id = {h:String} AND start >= now64() - toIntervalDay(1) GROUP BY name`, map[string]string{"h": obs.HostID}, &passes); err != nil {
		return nil, err
	}
	for _, p := range passes {
		d, ok := passIntervals.Load(strings.TrimPrefix(p.Name, "pass."))
		if !ok || p.Name == "pass.rules" {
			continue
		}
		if limit := d.(time.Duration) * time.Duration(rulePassMisses); p.Age >= limit.Seconds() && limit > 0 {
			f := contract.Finding{Rule: "absence-pass", Subject: p.Name, Value: p.Age, Cause: "problem:absence:" + p.Name, Exemplars: []string{}}
			text(&f, fmt.Sprintf("Coordinator pass %s has not run for %d seconds.", p.Name, int(p.Age)))
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
	// host-saturation: the last ruleSaturationBeats heartbeats all have load1 above the CPU count or memory free below
	// ruleMemFreeShare of the total.
	var sat []struct {
		HostID   string
		Beats    uint64
		Hot      uint64
		MaxLoad  float64
		Exemplar string
	}
	if err := spanStore.Query(ctx, `SELECT host_id AS HostID, count() AS Beats,
	countIf(load1 > toFloat64OrZero(attrs['fleet.cpus']) OR mem_free < {m:Float64} * toFloat64OrZero(attrs['fleet.mem_total'])) AS Hot,
	max(ifNull(load1, 0)) AS MaxLoad, any(trace_id) AS Exemplar
	FROM (SELECT * FROM span WHERE name = 'host.heartbeat' AND start >= now64() - toIntervalMinute(30) ORDER BY start DESC LIMIT {b:UInt32} BY host_id)
	GROUP BY host_id`, map[string]string{"m": strconv.FormatFloat(ruleMemFreeShare, 'f', -1, 64), "b": strconv.Itoa(ruleSaturationBeats)}, &sat); err != nil {
		return nil, err
	}
	for _, s := range sat {
		if s.Beats >= uint64(ruleSaturationBeats) && s.Hot == s.Beats {
			f := contract.Finding{Rule: "host-saturation", Subject: s.HostID, Value: s.MaxLoad, Cause: "problem:host:" + s.HostID + ":saturated", HostId: ptr(s.HostID), Exemplars: []string{s.Exemplar}}
			text(&f, fmt.Sprintf("Host %s is saturated: CPU load above its cores or memory below %d%% for %d heartbeats.", s.HostID, int(ruleMemFreeShare*100), ruleSaturationBeats))
			out = append(out, f)
		}
	}
	return out, nil
}

// stateRules: rules over Postgres state (store-fill, absence-store, stuck, unknown) and the host filter of the span rules.
func stateRules(ctx context.Context, a *App) ([]contract.Finding, error) {
	var out []contract.Finding
	err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		stores, err := queryRows(q, "SELECT id,host_id,state,capacity_bytes::float8,free_space_warning_bytes::float8 FROM store WHERE observed_at IS NOT NULL AND state IN ('full','unreachable')")
		if err != nil {
			return err
		}
		for _, s := range stores {
			id, host := astr(s["id"]), astr(s["host_id"])
			f := contract.Finding{Subject: id, StoreId: ptr(id), HostId: ptr(host), Exemplars: []string{}}
			if s["state"] == "full" {
				f.Rule, f.Cause = "store-fill", "problem:store:"+id+":fill"
				f.Value = num(s["capacity_bytes"])
				f.Baseline = ptr(num(s["free_space_warning_bytes"]))
				text(&f, fmt.Sprintf("Store %s on %s has less free space than its warning level.", id, host))
			} else {
				f.Rule, f.Cause = "absence-store", "problem:store:"+id+":offline"
				text(&f, fmt.Sprintf("Store %s on %s has not reported as reachable.", id, host))
			}
			out = append(out, f)
		}
		stuck, err := queryRows(q, `SELECT e.id,e.project_id,e.host_id,extract(epoch FROM now()-coalesce((SELECT max(d.updated_at) FROM document d WHERE d.kind='event'
			AND d.data @> jsonb_build_object('executionId', e.id) AND d.data->>'type' <> 'resource.observation'), e.started_at))::float8 AS idle
			FROM execution e WHERE e.state='running' AND e.started_at < now() - make_interval(secs => $1)`, ruleStuck.Seconds())
		if err != nil {
			return err
		}
		for _, s := range stuck {
			if num(s["idle"]) < ruleStuck.Seconds() {
				continue
			}
			id := astr(s["id"])
			f := contract.Finding{Rule: "stuck", Subject: id, Value: num(s["idle"]), Cause: "problem:execution:" + id + ":stuck", ExecutionId: ptr(id), Exemplars: []string{}}
			if h := astr(s["host_id"]); h != "" {
				f.HostId = ptr(h)
			}
			if p := astr(s["project_id"]); p != "" {
				f.ProjectId = ptr(p)
			}
			text(&f, fmt.Sprintf("Execution %s has shown no activity for %d minutes.", id, int(num(s["idle"])/60)))
			out = append(out, f)
		}
		// Hosts the span rules may name: not removed, intake not paused.
		hosts, err := queryRows(q, "SELECT data->>'hostId' AS id FROM document WHERE kind='host' AND coalesce(data->>'state','')<>'removed' AND coalesce(data->>'intake','all')<>'paused'")
		if err != nil {
			return err
		}
		active := map[string]bool{}
		for _, h := range hosts {
			active[astr(h["id"])] = true
		}
		hostsActive = active
		return nil
	})
	if spanWriter != nil {
		if d := spanWriter.Dropped(); d > rulesDropped {
			f := contract.Finding{Rule: "unknown", Subject: "dropped-spans", Value: float64(d - rulesDropped), Cause: "problem:unknown:dropped-spans", Exemplars: []string{}}
			text(&f, fmt.Sprintf("Telemetry dropped %d spans since the last rules pass.", d-rulesDropped))
			out = append(out, f)
			rulesDropped = d
		}
	}
	return out, err
}

var hostsActive map[string]bool

// cleanName: an operation name (and a client span's server.address) as it goes into a question: printable, at most 120 bytes.
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// groupFindings (Alertmanager-style inhibition): findings on a removed or paused host are dropped; a silent host
// absorbs every other finding on that host; an offline store absorbs that store's other findings.
func groupFindings(fs []contract.Finding) []contract.Finding {
	silent, offline := map[string]string{}, map[string]string{}
	var kept []contract.Finding
	for _, f := range fs {
		if f.HostId != nil && f.Rule != "stuck" && hostsActive != nil && !hostsActive[*f.HostId] && f.StoreId == nil {
			continue
		}
		kept = append(kept, f)
		if f.Rule == "absence-host" {
			silent[*f.HostId] = f.Cause
		}
		if f.Rule == "absence-store" {
			offline[*f.StoreId] = f.Cause
		}
	}
	for i := range kept {
		f := &kept[i]
		if f.HostId != nil && f.Rule != "absence-host" && silent[*f.HostId] != "" {
			f.GroupCause = ptr(silent[*f.HostId])
		} else if f.StoreId != nil && f.Rule != "absence-store" && offline[*f.StoreId] != "" {
			f.GroupCause = ptr(offline[*f.StoreId])
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
