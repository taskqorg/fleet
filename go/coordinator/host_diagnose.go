package main

// host_diagnose (docs/designs/fleet-system-management.md#hosts): what a person used to read on the host itself. The
// coordinator asks the host on its next poll (PollReply.diagnose), the executor answers in the poll after it
// (PollRequest.diagnosis, executor/diagnose.go), and the reply joins that report with the coordinator's own facts: the
// last poll, the commands the host has not confirmed and the queued runs dispatch skipped this host for.

import (
	"encoding/json"
	"errors"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

func init() {
	op("host_diagnose", hostDiagnose)
}

const (
	hostDiagnoseWait    = 30 * time.Second
	hostDiagnoseMaxWait = 60 * time.Second
)

func hostDiagnose(q *Request, b *contract.HostDiagnoseRequest) (contract.HostDiagnoseReply, error) {
	a, ctx := q.app, q.ctx()
	out := contract.HostDiagnoseReply{Host: b.Host, CoordinatorCommands: []contract.HostDiagnoseCommand{}, Waiting: []contract.HostDiagnoseWaiting{}}
	if b.Host == "" {
		return out, fail(400, "host is required")
	}
	d := connDB(ctx, a)
	obs, err := d.HostDiagnoseObservation(ctx, b.Host)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, fail(404, "Unknown host "+b.Host+" (or it never polled)")
	}
	if err != nil {
		return out, err
	}
	if q.person != nil && !fullAccess(q.person) && deref(obs.Holder) != q.person.Email {
		return out, fail(404, "Unknown host "+b.Host) // not the holder: the host is not shown, as in hosts_list
	}
	wait := hostDiagnoseWait
	if b.Wait != nil {
		wait = min(time.Duration(max(*b.Wait, 0))*time.Second, hostDiagnoseMaxWait)
	}
	asked := time.Now()
	if wait > 0 {
		if err := d.HostDiagnosisAsk(ctx, b.Host); err != nil {
			return out, err
		}
		a.wakeHost(b.Host) // a host in its long poll answers now and reports in the poll after it
	}
	var rep db.HostDiagnosis
	deadline := time.Now().Add(wait)
	for {
		rep, err = d.HostDiagnosisGet(ctx, b.Host)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
		if wait == 0 || rep.ReportedAt != nil && !rep.ReportedAt.Before(asked) || time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	if rep.ReportedAt != nil {
		ms := rep.ReportedAt.UnixMilli()
		out.ReportedAt = &ms
		out.Fresh = !rep.ReportedAt.Before(asked)
		var r contract.HostDiagnosis
		if json.Unmarshal(rep.Report, &r) == nil {
			out.Report = &r
		}
	}
	// The coordinator's facts, read after the wait so they are as new as the report.
	if obs, err = d.HostDiagnoseObservation(ctx, b.Host); err != nil {
		return out, err
	}
	if obs.LastSeenAt != nil {
		ms := obs.LastSeenAt.UnixMilli()
		out.LastSeenAt = &ms
		out.Online = time.Since(*obs.LastSeenAt) < hostStaleMs*time.Millisecond
	}
	var p hostPollObservationPayload
	lenientDecode(obs.Payload, &p)
	out.Version = p.Version
	for _, x := range p.Problems {
		out.Problems = append(out.Problems, firstString(x.Detail, x.Kind))
	}
	cs, err := d.HostDiagnoseCommands(ctx, b.Host)
	if err != nil {
		return out, err
	}
	for _, c := range cs {
		out.CoordinatorCommands = append(out.CoordinatorCommands, contract.HostDiagnoseCommand{Id: c.ID, Kind: c.Kind, ExecutionId: c.ExecutionID, State: c.Status, UpdatedAt: c.UpdatedAt.UnixMilli()})
	}
	ws, err := d.HostDiagnoseWaiting(ctx)
	if err != nil {
		return out, err
	}
	for _, w := range ws {
		if q.person != nil && !fullAccess(q.person) && !q.person.mayAccess(deref(w.ProjectID)) {
			continue
		}
		x := contract.HostDiagnoseWaiting{ExecutionId: w.ID, SessionId: w.SessionID, ProjectId: w.ProjectID, WaitReason: w.WaitReason, WaitDetail: nonEmpty(&w.WaitDetail),
			Since: firstNonZero(w.FirstMs, w.UpdatedAt.UnixMilli())}
		if w.LastMs > 0 {
			x.DecidedAt = &w.LastMs
		}
		var cands []candidate
		json.Unmarshal(w.Candidates, &cands)
		for _, c := range cands {
			if c.Host == b.Host {
				reason := firstString(c.Skipped, "not skipped: another candidate ranked first")
				x.SkippedHere = &reason
				break
			}
		}
		out.Waiting = append(out.Waiting, x)
	}
	return out, nil
}

func firstNonZero(xs ...int64) int64 {
	for _, x := range xs {
		if x != 0 {
			return x
		}
	}
	return 0
}

// recordDiagnosis stores the report a poll carried; pollWantsDiagnosis answers PollReply.diagnose.
func recordDiagnosis(q *Request, host string, r *contract.HostDiagnosis) error {
	if r == nil || host == "" {
		return nil
	}
	return connDB(q.ctx(), q.app).HostDiagnosisPut(q.ctx(), db.HostDiagnosisPutParams{HostID: host, Report: jsonBytes(r)})
}

func pollWantsDiagnosis(q *Request, host string) bool {
	want, err := connDB(q.ctx(), q.app).HostDiagnosisWanted(q.ctx(), host)
	return err == nil && want
}
