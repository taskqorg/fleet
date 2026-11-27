package main

// Cleanup rules (docs/designs/fleet-host-cleanup.md#13-rules): disk-growth and orphan over spans, leftovers,
// personal-output, cleanup-failing and run-frequency over Postgres state. Each finding becomes a problem with a bot
// owner like every other rule's. The factors are settings (FLEET_RULE_*), permissive by default.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"
)

var (
	ruleDiskGrowthShare = envFloat("FLEET_RULE_DISK_GROWTH_SHARE", 0.5)
	ruleDiskGrowthBytes = envFloat("FLEET_RULE_DISK_GROWTH_BYTES", 5e9)
	ruleLeftoverHours   = envFloat("FLEET_RULE_LEFTOVERS_HOURS", 24)
	ruleFrequencyFactor = envFloat("FLEET_RULE_RUN_FREQUENCY_FACTOR", 5)
	ruleFrequencyMin    = envFloat("FLEET_RULE_RUN_FREQUENCY_MIN", 50)
	ruleFrequencyEvery  = 10 * time.Minute // the frequency query reads 7 days of starts: not every minute
)

func envFloat(k string, d float64) float64 {
	if v, err := strconv.ParseFloat(envOr(k, ""), 64); err == nil && v >= 0 {
		return v
	}
	return d
}

// diskKeys: the host.disk attributes that add up to Fleet's bytes on a host.
var diskKeys = []string{"sessions_worktree", "sessions_other", "run_tmp", "tmp_owned", "releases", "harnesses", "journal", "candidates", "spool"}

// cleanupSpanRules: disk-growth and orphan over the span table.
func cleanupSpanRules(ctx context.Context) ([]contract.Finding, error) {
	var out []contract.Finding
	sum := make([]string, len(diskKeys))
	for i, k := range diskKeys {
		sum[i] = "toFloat64OrZero(attrs['fleet.disk." + k + "'])"
	}
	var growth []struct {
		HostID          string
		Now, Before     float64
		Exemplar        string
		BeforeAt, NowAt string
	}
	if err := spanStore.Query(ctx, `SELECT host_id AS HostID, argMax(total, start) AS Now, argMin(total, start) AS Before,
	argMax(trace_id, start) AS Exemplar, toString(min(start)) AS BeforeAt, toString(max(start)) AS NowAt
	FROM (SELECT host_id, start, trace_id, `+strings.Join(sum, " + ")+` AS total FROM span
		WHERE name = 'host.disk' AND start >= now64() - toIntervalHour(25))
	GROUP BY host_id HAVING min(start) <= now64() - toIntervalHour(20)`, nil, &growth); err != nil {
		return nil, err
	}
	for _, g := range growth {
		if d := g.Now - g.Before; d > ruleDiskGrowthBytes && d > ruleDiskGrowthShare*g.Before {
			f := contract.Finding{Rule: "disk-growth", Subject: g.HostID, Value: d, Baseline: ptr(g.Before), Cause: "problem:host:" + g.HostID + ":disk-growth", HostId: ptr(g.HostID), Exemplars: []string{g.Exemplar}}
			text(&f, fmt.Sprintf("Fleet's disk use on %s grew from %s to %s GB in a day.", g.HostID, gb(g.Before), gb(g.Now)))
			out = append(out, f)
		}
	}
	// orphan: a run's processes outlived it (docs/designs/fleet-telemetry.md#6-rules); the run-end cleanup killed them.
	var orphans []struct {
		ExecutionID, HostID, ProjectID, TraceID string
		Orphans                                 float64
	}
	if err := spanStore.Query(ctx, `SELECT assumeNotNull(execution_id) AS ExecutionID, host_id AS HostID, ifNull(project_id, '') AS ProjectID,
	trace_id AS TraceID, toFloat64OrZero(attrs['fleet.orphans']) AS Orphans
	FROM span WHERE name = 'execution' AND execution_id IS NOT NULL AND start >= now64() - toIntervalHour(1)
		AND toFloat64OrZero(attrs['fleet.orphans']) > 0
	ORDER BY start DESC LIMIT 50`, nil, &orphans); err != nil {
		return nil, err
	}
	for _, o := range orphans {
		id := o.ExecutionID
		f := contract.Finding{Rule: "orphan", Subject: id, Value: o.Orphans, Cause: "problem:execution:" + id + ":orphans", ExecutionId: ptr(id), HostId: ptr(o.HostID), Exemplars: []string{o.TraceID}}
		if o.ProjectID != "" {
			f.ProjectId = ptr(o.ProjectID)
		}
		text(&f, fmt.Sprintf("Execution %s left %d processes running after it ended on %s; the run-end cleanup killed them.", id, int(o.Orphans), o.HostID))
		out = append(out, f)
	}
	return out, nil
}

var (
	frequencyMu       sync.Mutex
	frequencyAt       time.Time
	frequencyFindings []contract.Finding
)

// cleanupStateRules: leftovers, personal-output, cleanup-failing and run-frequency over Postgres.
func cleanupStateRules(ctx context.Context, a *App) ([]contract.Finding, error) {
	var out []contract.Finding
	err := a.store.Read(ctx, nil, func(q *Request) error {
		personalFailures, err := q.db().RulesPersonalScriptFailures(q.ctx())
		if err != nil {
			return err
		}
		for _, r := range personalFailures {
			project := r.ProjectID
			f := contract.Finding{Rule: "personal-output", Subject: project, Value: float64(r.N), Cause: "problem:project:" + project + ":personal-output", ProjectId: ptr(project), Exemplars: []string{}}
			text(&f, fmt.Sprintf("%d personal script jobs in %s had an output upload or staging cleanup fail.", r.N, project))
			out = append(out, f)
		}
		invs, err := q.db().RulesInventories(q.ctx())
		if err != nil {
			return err
		}
		for _, r := range invs {
			var s inventorySummary
			if json.Unmarshal(r.Inventory, &s) != nil {
				continue
			}
			h := r.HostID
			// Two inventories in a row name the same leftovers, or a settled session's teardown kept its worktree.
			age := time.Since(time.UnixMilli(int64(s.At))).Hours()
			if s.Repeated > 0 || (s.Kept > 0 && age < ruleLeftoverHours+2) {
				f := contract.Finding{Rule: "leftovers", Subject: h, Value: float64(s.Repeated + s.Kept), Cause: "problem:host:" + h + ":leftovers", HostId: ptr(h), Exemplars: []string{}}
				t := fmt.Sprintf("Host %s holds worktrees nothing needs:", h)
				if s.Repeated > 0 {
					t += fmt.Sprintf(" %d answered for removal in two inventories in a row (%s GB in all leftovers)", s.Repeated, gb(derefFloat(s.LeftoverBytes)))
				}
				if s.Kept > 0 {
					if s.Repeated > 0 {
						t += ";"
					}
					t += fmt.Sprintf(" %d of settled sessions kept by cleanup.keepWhen", s.Kept)
				}
				text(&f, t+".")
				out = append(out, f)
			}
			for project, n := range s.Personal {
				f := contract.Finding{Rule: "personal-output", Subject: project, Value: float64(n), Cause: "problem:project:" + project + ":personal-output", ProjectId: ptr(project), HostId: ptr(h), Exemplars: []string{}}
				text(&f, fmt.Sprintf("%d worktrees of personal project %s on %s still hold run results (output/).", n, project, h))
				out = append(out, f)
			}
		}
		failing, err := q.db().RulesCleanupFailing(q.ctx())
		if err != nil {
			return err
		}
		for _, r := range failing {
			h := r.HostID
			f := contract.Finding{Rule: "cleanup-failing", Subject: h, Value: float64(r.N), Cause: "problem:host:" + h + ":cleanup-failing", HostId: ptr(h), Exemplars: []string{}}
			text(&f, fmt.Sprintf("%d worktree teardowns on %s failed to remove their worktree in the last day.", r.N, h))
			out = append(out, f)
		}
		frequencyMu.Lock()
		defer frequencyMu.Unlock()
		if time.Since(frequencyAt) >= ruleFrequencyEvery {
			rows, err := q.db().RulesRunFrequency(q.ctx())
			if err != nil {
				return err
			}
			var fs []contract.Finding
			for _, r := range rows {
				if float64(r.N) < ruleFrequencyMin || float64(r.N) <= ruleFrequencyFactor*r.Median {
					continue
				}
				subject := cleanName(r.Subject)
				f := contract.Finding{Rule: "run-frequency", Subject: subject, Value: float64(r.N), Baseline: ptr(r.Median), Cause: "problem:frequency:" + subject, Exemplars: []string{}}
				text(&f, fmt.Sprintf("%s started %d runs in the last hour; its usual hour has %.0f.", r.Subject, r.N, r.Median))
				fs = append(fs, f)
			}
			frequencyAt, frequencyFindings = time.Now(), fs
		}
		out = append(out, frequencyFindings...)
		return nil
	})
	return out, err
}

func derefFloat(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func init() {
	op("telemetry_disk", telemetryDisk)
}

// telemetryDisk: the daily maximum of each host.disk reading (docs/designs/fleet-host-cleanup.md#12-disk-readings), for
// trends; host spans carry no project, so the span scope of the caller applies as in telemetry_summary.
func telemetryDisk(q *Request, r *contract.TelemetryDiskRequest) (contract.TelemetryDiskReply, error) {
	out := contract.TelemetryDiskReply{Points: []contract.TelemetryDiskPoint{}}
	if spanStore == nil {
		return out, nil
	}
	days := int64(30)
	if r.Days != nil {
		days = min(*r.Days, 365)
	}
	params := map[string]string{"d": strconv.FormatInt(days, 10)}
	where := spanScope(q.person, params)
	if r.Host != nil {
		params["host"] = *r.Host
		where += " AND host_id = {host:String}"
	}
	cols := make([]string, 0, len(diskKeys)+4)
	for _, k := range append(append([]string{}, diskKeys...), "sessions_worktree_count", "sessions_other_count", "free", "total") {
		cols = append(cols, "max(toFloat64OrZero(attrs['fleet.disk."+k+"']))")
	}
	var rows []struct {
		HostID, Day string
		Values      []float64
	}
	if err := spanStore.Query(q.ctx(), `SELECT host_id AS HostID, toString(toDate(start)) AS Day, [`+strings.Join(cols, ", ")+`] AS Values
	FROM span WHERE name = 'host.disk' AND start >= now64() - toIntervalDay({d:UInt32}) AND `+where+`
	GROUP BY host_id, Day ORDER BY host_id, Day`, params, &rows); err != nil {
		return out, fail(400, "span store: "+err.Error())
	}
	names := append(append([]string{}, diskKeys...), "sessions_worktree_count", "sessions_other_count", "free", "total")
	for _, x := range rows {
		p := contract.TelemetryDiskPoint{HostId: x.HostID, Day: x.Day, Values: map[string]float64{}}
		for i, v := range x.Values {
			if i < len(names) {
				p.Values[names[i]] = v
			}
		}
		out.Points = append(out.Points, p)
	}
	return out, nil
}
