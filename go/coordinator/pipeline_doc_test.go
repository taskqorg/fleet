package main

import (
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

func initPipelinePlan(run doc) error {
	return viaRun(run, func(r *runRecord) error { return r.initPlan() })
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

// runOfDoc: a run record held as a map as the typed run; its plan stays the map's (an edit through either is the other's).
func runOfDoc(d doc) (*runRecord, error) {
	plan := pipelinePlanOfRun(d)
	r, err := asT[runRecord](d)
	if err != nil {
		return nil, err
	}
	r.Plan = plan
	return &r, nil
}

// docOfRun: run as the map its callers read, sharing run's plan; nil for none.
func docOfRun(r *runRecord) doc {
	if r == nil {
		return nil
	}
	d := docOf(r)
	if r.Plan != nil {
		d["plan"] = r.Plan
	}
	return d
}

// pipelineRecoveryFence: recoveryFence of a map record.
func pipelineRecoveryFence(q *Request, run doc) error {
	r, err := runOfDoc(run)
	if err != nil {
		return err
	}
	return recoveryFence(q, r)
}

// validatePipeline: validatePipelineDef of a map (definitions.go); d itself when valid.
func validatePipeline(d doc) (doc, error) {
	if _, err := validatePipelineDef(jsonBytes(d)); err != nil {
		return nil, err
	}
	return d, nil
}

// pipelinePlanOfRun: the plan of a run held as a map record (contract.PipelinePlan; nil for a run without one); a plan
// written as JSON (a fixture's map) is read into it once.
func pipelinePlanOfRun(run doc) *contract.PipelinePlan {
	switch p := run["plan"].(type) {
	case *contract.PipelinePlan:
		return p
	case nil:
		return nil
	default:
		plan, err := asT[contract.PipelinePlan](p)
		if err != nil {
			return nil
		}
		run["plan"] = &plan
		return &plan
	}
}

// pipelineEntries: the entries of a map record's plan, in index order (aliases: an edit is the plan's).
func pipelineEntries(run doc) []contract.PipelinePlanEntry {
	if p := pipelinePlanOfRun(run); p != nil {
		return p.Entries
	}
	return nil
}

// pipelineEntry: entry index of a map record's plan, nil out of range.
func pipelineEntry(run doc, index int) *contract.PipelinePlanEntry {
	entries := pipelineEntries(run)
	if index < 0 || index >= len(entries) {
		return nil
	}
	return &entries[index]
}

// mustRunOf: runOfDoc of a fixture; nil when it does not decode.
func mustRunOf(d doc) *runRecord {
	r, _ := runOfDoc(d)
	return r
}

// suffixProof: suffixProven of records as the tests build them.
func suffixProof(execution, command, event doc, stepID string) bool {
	return suffixProven(labRecordOf[labProofExecution](execution), labRecordOf[labSuffixCommand](command), labRecordOf[labProofEvent](event), stepID)
}

// suffixCommand: suffixScript of command records as the tests build them; the script command's record.
func suffixCommand(records []doc) (doc, error) {
	commands := []labSuffixCommand{}
	for _, r := range records {
		c, _ := asT[labSuffixCommand](r)
		commands = append(commands, c)
	}
	i, err := suffixScript(commands)
	if err != nil {
		return nil, err
	}
	return records[i], nil
}

// doc: the file reference as the tests read it.
func (f fileRef) doc() doc { return docOf(f) }
