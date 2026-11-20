package main

// Map adapters for the pipeline tests, which build and read runs as map records (pipelines.go, pipeline_plan.go hold a
// *runRecord): each converts the map once at the call and writes the typed run back into it in place, so the test's
// step maps and plan stay the ones it holds.

import (
	"context"
	"encoding/json"

	"fleet/contract"
)

// viaRun: f on the typed run of record d; d then holds the run as f left it.
func viaRun(d doc, f func(*runRecord) error) error {
	r, err := runOfDoc(d)
	if err != nil {
		return err
	}
	err = f(r)
	fresh := docOfRun(r)
	plan := fresh["plan"]
	delete(fresh, "plan")
	restorePipelineValue(d, fresh)
	if plan != nil {
		d["plan"] = plan
	}
	return err
}

func putRun(q *Request, run doc) error {
	return viaRun(run, func(r *runRecord) error { return r.put(q) })
}

func finishRun(q *Request, run doc, state, problem string) error {
	return viaRun(run, func(r *runRecord) error { return r.finish(q, state, problem) })
}

func initPipelinePlan(run doc) error {
	return viaRun(run, func(r *runRecord) error { return r.initPlan() })
}

func selectPipelineStep(q *Request, run doc, index int) error {
	return viaRun(run, func(r *runRecord) error { return r.selectStep(q, index) })
}

func completeStep(q *Request, run doc, i int, output any, extra doc) error {
	return viaRun(run, func(r *runRecord) error {
		usage := r.Steps[i].Usage
		if u, ok := extra["usage"]; ok {
			usage = jsonBytes(u)
		}
		return r.completeStep(q, i, jsonBytes(output), usage)
	})
}

func startScript(q *Request, run doc, stepExecutionID, command string, timeout any, hostID string, inputs []any) (string, error) {
	var id string
	err := viaRun(run, func(r *runRecord) error {
		files, err := asT[[]fileRef](inputs)
		if err != nil {
			return err
		}
		var t json.RawMessage
		if timeout != nil {
			t = jsonBytes(timeout)
		}
		id, err = r.startScript(q, stepExecutionID, command, t, hostID, files)
		return err
	})
	return id, err
}

func inputsFor(q *Request, run doc, i int) ([]any, error) {
	r, err := runOfDoc(run)
	if err != nil {
		return nil, err
	}
	fs, err := r.inputs(q, i)
	out := []any{}
	for _, f := range fs {
		out = append(out, f.doc())
	}
	return out, err
}

func rerunInputs(old doc, identity string) doc {
	r, _ := runOfDoc(old)
	extra, _ := rerunExtra(r, identity)
	return docOf(extra)
}

func pipelineRevision(run doc) int64 {
	if p := pipelinePlanOfRun(run); p != nil {
		return p.Revision
	}
	return 0
}

func pipelinePlanRead(q *Request, run doc) (doc, error) {
	r, err := runOfDoc(run)
	if err != nil {
		return nil, err
	}
	out, err := r.planRead(q)
	if err != nil {
		return nil, err
	}
	d := docOf(out)
	d["planSummary"] = planSummary(r.entries())
	return d, nil
}

// withArguments: the merged arguments' keys, each with the value the test wrote (an override over the step's).
func withArguments(def, overrides doc) any {
	d, _ := asT[pipelineDefStep](def)
	o, _ := asT[pipelineOverrides](overrides)
	merged := pipelineRuleOf(d.withArguments(&o))
	base, extra := mapOf(def["arguments"]), mapOf(mapOf(overrides["arguments"])[astr(def["id"])])
	out := doc{}
	for k := range merged {
		if v, ok := extra[k]; ok {
			out[k] = v
		} else {
			out[k] = base[k]
		}
	}
	return out
}

func jobMatches(task, args, tool doc) bool {
	t, err := asT[contract.GatewayTool](tool)
	if err != nil {
		return false
	}
	return jobMatchesRaw(recOf(task), recOf(args), &t)
}

func jobTool(def doc, name string) string {
	d, _ := asT[pipelineDefStep](def)
	return d.jobTool(name)
}

// runPipelineTransition retries DB work with the complete pre-transition
// snapshot, including a paid answer's already materialized output binding.
// Effects and object I/O must remain outside this callback.
func runPipelineTransition(ctx context.Context, a *App, run doc, fn func(*Request) error) error {
	snapshot := restorePipelineValue(nil, run)
	return runTx(ctx, a, nil, true, "", func(q *Request) error {
		cancelled, err := q.db().PipelineRunCancelled(q.ctx(), astr(run["id"]))
		if err != nil {
			return err
		}
		if cancelled {
			return fail(409, "Pipeline is cancelled")
		}
		onEnd(q.tx, func(committed bool) {
			if !committed {
				restorePipelineValue(run, snapshot)
			}
		})
		return fn(q)
	})
}

// restorePipelineValue keeps existing map/slice aliases alive while restoring a
// transition snapshot. Numeric types are preserved; no JSON roundtrip is used.
func restorePipelineValue(current, snapshot any) any {
	switch source := snapshot.(type) {
	case *contract.PipelinePlan:
		target, _ := current.(*contract.PipelinePlan)
		if target == nil || target == source {
			target = &contract.PipelinePlan{}
		}
		target.DefinitionSha256, target.Revision = source.DefinitionSha256, source.Revision
		if len(target.Entries) != len(source.Entries) {
			target.Entries = make([]contract.PipelinePlanEntry, len(source.Entries))
		}
		for i := range source.Entries {
			restorePipelineEntry(&target.Entries[i], source.Entries[i])
		}
		return target
	case map[string]any:
		target := mapOf(current)
		if target == nil {
			target = doc{}
		}
		for key := range target {
			if _, ok := source[key]; !ok {
				delete(target, key)
			}
		}
		for key, value := range source {
			target[key] = restorePipelineValue(target[key], value)
		}
		return target
	case []any:
		target := anys(current)
		if target == nil || len(target) != len(source) {
			target = make([]any, len(source))
		}
		for i, value := range source {
			target[i] = restorePipelineValue(target[i], value)
		}
		return target
	default:
		return snapshot
	}
}

// restorePipelineEntry sets *dst to a copy of src, keeping dst's dispatch and execution reference aliases alive.
func restorePipelineEntry(dst *contract.PipelinePlanEntry, src contract.PipelinePlanEntry) {
	if src.Decision != nil {
		d := *src.Decision
		d.Path = append([]string(nil), d.Path...)
		src.Decision = &d
	}
	if src.ExecutionRef != nil {
		r := *src.ExecutionRef
		if dst.ExecutionRef != nil {
			*dst.ExecutionRef, src.ExecutionRef = r, dst.ExecutionRef
		} else {
			src.ExecutionRef = &r
		}
	}
	if src.Dispatch != nil {
		x := *src.Dispatch
		if dst.Dispatch != nil {
			*dst.Dispatch, src.Dispatch = x, dst.Dispatch
		} else {
			src.Dispatch = &x
		}
	}
	if src.Inputs != nil {
		src.Inputs = append(make([]contract.PipelineInputBinding, 0, len(src.Inputs)), src.Inputs...)
	}
	if src.Problem != nil {
		p := *src.Problem
		src.Problem = &p
	}
	*dst = src
}
