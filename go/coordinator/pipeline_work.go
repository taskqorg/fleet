// Pipeline runs and Lab experiments on the work kit (docs/designs/fleet-standard.md S6, DESIGN_RULES.md). A running run
// has one pipeline_work row (putRun), an experiment to look at one lab_work row (labEnqueue); the kit passes each queue
// on its period and on its fleet_work wake and runs one step per item in one transaction (pipelineWorkStep in
// pipelines.go, labWorkStep in lab.go). Work outside the database is a Call the step leaves (work_kit.go).
package main

import (
	"context"
	"time"

	"fleet/coordinator/db"
)

func init() {
	kitRegister(&kitQueue{Name: "pipeline", Period: pipelinePeriod(), Limit: 16, Workers: 4, Due: pipelineWorkDue, Step: pipelineWorkStep,
		Retry: kitRetry{Table: "pipeline_work", Key: "run_id", Guard: `pipeline_work.attempts=$5::int`, Class: func(error) string { return "step_failed" }}})
	kitRegister(&kitQueue{Name: "lab", Period: pipelinePeriod(), Limit: 8, Workers: 2, Due: labWorkDue, Step: labWorkStep,
		Retry: kitRetry{Table: "lab_work", Key: "experiment_id", Guard: `lab_work.attempts=$5::int`, Upsert: true, Class: func(error) string { return "look_failed" }}})
}

// pipelinePeriod: how often the pipeline and lab queues are passed besides their wakes: the waiting steps' own look
// (FLEET_PIPELINE_TICK_MS) when shorter than a second.
func pipelinePeriod() time.Duration {
	if d := pipelineEvery; d > 0 && d < time.Second {
		return d
	}
	return time.Second
}

func pipelineWorkDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rs, err := q.db().PipelineWorkDue(q.ctx(), db.PipelineWorkDueParams{Now: now, Lim: int32(limit)})
	out := make([]*kitClaim, 0, len(rs))
	for _, r := range rs {
		out = append(out, &kitClaim{Key: r.RunID, Attempts: int(r.Attempts), Guard: []any{r.Attempts}})
	}
	return out, err
}

func labWorkDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rs, err := q.db().LabWorkDue(q.ctx(), db.LabWorkDueParams{Now: now, Lim: int32(limit)})
	out := make([]*kitClaim, 0, len(rs))
	for _, r := range rs {
		out = append(out, &kitClaim{Key: r.ExperimentID, Attempts: int(r.Attempts), Guard: []any{r.Attempts}})
	}
	return out, err
}

// labEnqueue: experiment id has something new to look at (a trial's end, an answer), in the producer's transaction.
func labEnqueue(q *Request, id string) error {
	n, err := q.db().LabWorkEnsure(q.ctx(), id)
	if err != nil {
		return err
	}
	if n > 0 {
		return kitNotify(q, "lab")
	}
	return kitWake(q, "lab", id, time.Now())
}

// labRunNow: an operation that changed experiment id (a cell retry or suffix retry) looks at it at once, as a kit
// pass would, so its answer shows the updated trials.
func labRunNow(ctx context.Context, a *App, id string) error {
	return kitOf(a).runOne(ctx, "lab", &kitClaim{Key: id, Guard: []any{int32(0)}})
}
