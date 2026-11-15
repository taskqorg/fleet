package main

import (
	"errors"
	"sync"
	"testing"
)

func planFixture(t *testing.T, id string) doc {
	t.Helper()
	definition := doc{"version": float64(3), "id": "guard", "resultStep": "final", "steps": []any{
		doc{"id": "route", "kind": "script", "command": "fixture-route"},
		doc{"id": "review", "kind": "script", "command": "fixture-review", "when": doc{"kind": "enum", "source": doc{"step": "route", "path": []any{"action"}}, "domain": []any{"retain_candidate", "qualified_review"}, "equals": "qualified_review"}},
		doc{"id": "final", "kind": "script", "command": "fixture-final"},
	}}
	steps := []any{}
	for _, v := range anys(definition["steps"]) {
		d := mapOf(v)
		steps = append(steps, doc{"id": d["id"], "kind": d["kind"]})
	}
	run := doc{"id": id, "pipelineId": "guard", "projectId": "hello-world", "requestedBy": doc{"email": testOwnerEmail}, "definition": definition, "definitionSha256": sha256hex([]byte(canonical(definition))), "state": "running", "startedAt": nowMs(), "steps": steps}
	if err := initPipelinePlan(run); err != nil {
		t.Fatal(err)
	}
	return run
}

// Requirement: only one committed caller may hand off a selected API action,
// and a saved reservation cannot outlive current actor/connection authority.
// Risk: competing coordinators mint two calls, or stale access starts delayed work.
func TestPipelineAPIClaimAndCurrentAuthority(t *testing.T) {
	env := openTestCoordinator(t, false)
	t.Setenv("FLEET_GATEWAY_DIR", "")
	seed := func(id string) doc {
		run := planFixture(t, id)
		definition := mapOf(run["definition"])
		steps := anys(definition["steps"])
		steps[0] = doc{"id": "route", "kind": "api", "connection": "fixture", "tool": "system_one", "arguments": doc{}}
		run["definitionSha256"] = sha256hex([]byte(canonical(definition)))
		if err := initPipelinePlan(run); err != nil {
			t.Fatal(err)
		}
		if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
			if err := putRun(q, run); err != nil {
				return err
			}
			return selectPipelineStep(q, run, 0)
		}); err != nil {
			t.Fatal(err)
		}
		return run
	}
	connection := doc{"id": "connection:fixture", "name": "fixture", "provider": "typesafe", "intake": "all", "tools": []any{"system_one"}, "access": doc{"projects": []any{"hello-world"}}}
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error { return put(q, "api-connection", connection) }); err != nil {
		t.Fatal(err)
	}
	run := seed("api-claim")
	type result struct {
		won bool
		err error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			won := false
			err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
				_, token, claim, err := claimPipelineAPI(q, "api-claim", stepRow(run, 0), "fixture", "system_one")
				won = claim
				if claim && token == "" {
					return errors.New("winning claim has no token")
				}
				return err
			})
			results <- result{won, err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.won {
			winners++
		}
	}
	if winners != 1 {
		t.Fatal("competing DB sessions did not retain one claim owner", winners)
	}
	if err := runTx(env.ctx, env.app, nil, false, "", func(q *Request) error {
		var count int
		if err := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM document WHERE kind='run-token' AND data->>'executionId'=$1", stepRow(run, 0)).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatal("claim minted duplicate authority", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reserved := seed("api-revoked")
	connection["intake"] = "none"
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error { return put(q, "api-connection", connection) }); err != nil {
		t.Fatal(err)
	}
	err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		_, token, won, err := claimPipelineAPI(q, "api-revoked", stepRow(reserved, 0), "fixture", "system_one")
		if token != "" || won {
			t.Fatal("revoked connection authorized dispatch")
		}
		return err
	})
	var denied *HTTPError
	if !errors.As(err, &denied) || denied.Code != 403 {
		t.Fatal("saved reservation bypassed connection revocation", err)
	}
}

// Requirement: nonselection remains visible without an execution or invented resource observation.
// Risk: eager rows or absent-branch defaults fabricate work and break final consumers.
func TestPipelinePlanFalseHasNoExecution(t *testing.T) {
	env := openTestCoordinator(t, false)
	run := planFixture(t, "plan-false")
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		if err := putRun(q, run); err != nil {
			return err
		}
		var count int
		if err := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM execution WHERE parent_execution_id=$1", run["id"]).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("pending plan created executions", count)
		}
		if err := selectPipelineStep(q, run, 0); err != nil {
			return err
		}
		// Synthetic retained output for testing the decision/storage boundary;
		// no script, host, provider or object-store call is executed here.
		output := doc{"action": "retain_candidate"}
		step := mapOf(anys(run["steps"])[0])
		step["state"], step["output"], step["outputArtifactId"] = "succeeded", output, "plan-output"
		if err := exec(q, `INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,state)
		 VALUES('plan-output','hello-world','lab',$1,$2,'application/json','output.json',$3,'stored')`, sha256hex(jsonBytes(output)), len(jsonBytes(output)), stepRow(run, 0)); err != nil {
			return err
		}
		if err := putRun(q, run); err != nil {
			return err
		}
		return selectPipelineStep(q, run, 1)
	}); err != nil {
		t.Fatal(err)
	}
	if err := runTx(env.ctx, env.app, nil, false, "", func(q *Request) error {
		got, err := getRun(q, "plan-false")
		if err != nil {
			return err
		}
		entry := pipelinePlanEntry(got, 1)
		if entry["activation"] != "not_selected" || entry["executionRef"] != nil || entry["dispatch"] != nil {
			t.Fatal("false guard acquired work", entry)
		}
		var count int
		if err := q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM execution WHERE parent_execution_id=$1", got["id"]).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatal("nonselected or pending step created a child", count)
		}
		view, err := pipelinePlanRead(q, got)
		if err != nil {
			return err
		}
		if view["steps"] != nil || len(pipelinePlanEntries(view)) != 3 || num(mapOf(view["planSummary"])["notSelected"]) != 1 {
			t.Fatal("public read lost the inactive plan", view)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Requirement: stale transitions cannot replace a committed plan, and a rolled-back
// transaction cannot advance the in-memory revision used by its safe retry.
func TestPipelinePlanWriteConflictAndRollback(t *testing.T) {
	env := openTestCoordinator(t, false)
	run := planFixture(t, "plan-conflict")
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error { return putRun(q, run) }); err != nil {
		t.Fatal(err)
	}
	var stale doc
	jsonRoundTrip(run, &stale)
	revision := mapOf(run["plan"])["revision"]
	abort := errors.New("fixture rollback")
	err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		if err := putRun(q, run); err != nil {
			return err
		}
		if err := putRun(q, run); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) || mapOf(run["plan"])["revision"] != revision {
		t.Fatal("rollback changed the plan revision", err, run)
	}
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error { return putRun(q, run) }); err != nil {
		t.Fatal("safe write after rollback", err)
	}
	err = runTx(env.ctx, env.app, nil, true, "", func(q *Request) error { return putRun(q, stale) })
	var conflict *HTTPError
	if !errors.As(err, &conflict) || conflict.Code != 409 {
		t.Fatal("stale plan write was accepted", err)
	}
}

// Requirement: preparation is not dispatch; cancel and the first host handoff
// serialize on the persisted plan, and redelivery retains one immutable command.
func TestPipelineNativeHandoffAndCancellation(t *testing.T) {
	env := openTestCoordinator(t, false)
	env.registerHost(t, "plan-host")
	prepare := func(id string) (doc, doc, doc) {
		run := planFixture(t, id)
		var native, command doc
		if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
			if err := putRun(q, run); err != nil {
				return err
			}
			if err := selectPipelineStep(q, run, 0); err != nil {
				return err
			}
			nativeID, err := startScript(q, run, stepRow(run, 0), "fixture-not-executed", nil, "plan-host", []any{})
			if err != nil {
				return err
			}
			native, err = get(q, nativeID, "execution")
			if err != nil {
				return err
			}
			commands, err := all(q, "command", doc{"executionId": nativeID, "kind": "script"})
			if err != nil {
				return err
			}
			if len(commands) != 1 {
				return errors.New("script preparation did not create one command")
			}
			command = commands[0]
			step := mapOf(anys(run["steps"])[0])
			step["state"], step["executionId"] = "running", nativeID
			dispatch := mapOf(pipelinePlanEntry(run, 0)["dispatch"])
			dispatch["phase"], dispatch["nativeExecutionId"], dispatch["commandId"], dispatch["hostId"] = "prepared", nativeID, command["id"], "plan-host"
			return putRun(q, run)
		}); err != nil {
			t.Fatal(err)
		}
		return run, native, command
	}
	run, native, command := prepare("native-once")
	body := canonical(command)
	for repeat := 0; repeat < 2; repeat++ {
		if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
			ok, err := handoffPipelineCommand(q, native, command)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("current authorized host handoff was refused")
			}
			fresh, err := get(q, command["id"], "command")
			if err != nil {
				return err
			}
			if canonical(fresh) != body {
				return errors.New("redelivery changed the immutable command")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := runTx(env.ctx, env.app, nil, false, "", func(q *Request) error {
		got, err := getRun(q, astr(run["id"]))
		if err != nil {
			return err
		}
		dispatch := mapOf(pipelinePlanEntry(got, 0)["dispatch"])
		if dispatch["phase"] != "handed_off" || dispatch["claimId"] == nil {
			return errors.New("handoff claim was not persisted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cancelled, unstarted, pending := prepare("native-cancelled")
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		_, _, err := pipelineCancelIn(q, astr(cancelled["id"]))
		if err != nil {
			return err
		}
		ok, err := handoffPipelineCommand(q, unstarted, pending)
		if err != nil {
			return err
		}
		if ok {
			return errors.New("cancelled preparation was handed off")
		}
		fresh, err := getRun(q, astr(cancelled["id"]))
		if err != nil {
			return err
		}
		if fresh["state"] != "cancelled" || pipelinePlanEntry(fresh, 1)["activation"] != "blocked" || pipelinePlanEntry(fresh, 2)["activation"] != "blocked" {
			return errors.New("cancel did not block future plan entries")
		}
		stored, err := get(q, pending["id"], "command")
		if err != nil {
			return err
		}
		if stored["status"] != "stopped" {
			return errors.New("prepared command remains deliverable after cancel")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Requirement: finalizer lineage is an exact staged producer binding, with a
// same-owner control artifact. Risk: a readable foreign result becomes a valid source.
func TestPipelineFinalizerBinding(t *testing.T) {
	for _, bad := range []bool{false, true} {
		name := "valid"
		if bad {
			name = "foreign-source"
		}
		t.Run(name, func(t *testing.T) {
			env := openTestCoordinator(t, false)
			run := planFixture(t, "finalizer-binding")
			err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
				if err := putRun(q, run); err != nil {
					return err
				}
				seed := func(i int, id, name string, output any) error {
					b := jsonBytes(output)
					return exec(q, `INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,state) VALUES($1,'hello-world','lab',$2,$3,'application/json',$4,$5,'stored')`, id, sha256hex(b), len(b), name, stepRow(run, i))
				}
				if err := selectPipelineStep(q, run, 0); err != nil {
					return err
				}
				output := doc{"action": "retain_candidate"}
				if err := seed(0, "route-result", "output.json", output); err != nil {
					return err
				}
				route := mapOf(anys(run["steps"])[0])
				route["outputArtifactId"] = "route-result"
				mapOf(pipelinePlanEntry(run, 0)["dispatch"])["phase"] = "handed_off"
				if err := completeStep(q, run, 0, output, nil); err != nil {
					return err
				}
				if err := selectPipelineStep(q, run, 1); err != nil {
					return err
				}
				if err := selectPipelineStep(q, run, 2); err != nil {
					return err
				}
				binding := doc{"path": "route/output.json", "artifactId": "route-result", "sha256": sha256hex(jsonBytes(output)), "producerRunId": run["id"], "producerStepId": "route", "producerExecutionId": stepRow(run, 0)}
				control := doc{"schema": "fleet.pipeline-control.v1"}
				if err := seed(2, "final-control", "__fleet_control/pipeline.json", control); err != nil {
					return err
				}
				inputs := []any{binding, doc{"path": "__fleet_control/pipeline.json", "artifactId": "final-control", "sha256": sha256hex(jsonBytes(control))}}
				entry := pipelinePlanEntry(run, 2)
				entry["inputs"] = inputs
				mapOf(entry["executionRef"])["sourceInputSha256"] = sha256hex([]byte(canonical(inputs)))
				source := doc{}
				for k, v := range binding {
					source[k] = v
				}
				if bad {
					source["artifactId"] = "readable-but-not-staged"
				}
				final := doc{"fleetLineage": doc{"schema": "fleet.result-lineage.v1", "selectedSources": []any{source}}, "projectResult": doc{"decision": "accepted"}}
				if err := seed(2, "final-result", "output.json", final); err != nil {
					return err
				}
				mapOf(anys(run["steps"])[2])["outputArtifactId"] = "final-result"
				mapOf(entry["dispatch"])["phase"] = "handed_off"
				if err := completeStep(q, run, 2, final, nil); err != nil {
					return err
				}
				return finishRun(q, run, "succeeded", "")
			})
			if err != nil {
				t.Fatal(err)
			}
			if bad {
				if run["state"] != "failed" || run["canonicalResult"] != nil {
					t.Fatal(run)
				}
			} else if run["state"] != "succeeded" || mapOf(run["canonicalResult"])["mode"] != "finalizer" || pipelinePlanEntry(run, 1)["activation"] != "not_selected" {
				t.Fatal(run)
			}
		})
	}
}

// Requirement: reuse preserves original artifact/execution ownership and does
// not create a fresh execution or spend. Risk: missing or corrupt proof silently repeats work.
func TestPipelineVerifiedPrefixReuse(t *testing.T) {
	env := openTestCoordinator(t, false)
	old := planFixture(t, "prefix-source")
	definition := doc{"version": float64(2), "id": "prefix", "steps": []any{doc{"id": "route", "kind": "script", "command": "fixture-route"}}}
	old["definition"], old["definitionSha256"], old["pipelineId"] = definition, sha256hex([]byte(canonical(definition))), "prefix"
	old["steps"] = anys(old["steps"])[:1]
	if err := initPipelinePlan(old); err != nil {
		t.Fatal(err)
	}
	err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		if err := putRun(q, old); err != nil {
			return err
		}
		if err := selectPipelineStep(q, old, 0); err != nil {
			return err
		}
		output := doc{"p": 0.75}
		if err := exec(q, `INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,state) VALUES('prefix-output','hello-world','lab',$1,$2,'application/json','output.json',$3,'stored')`, sha256hex(jsonBytes(output)), len(jsonBytes(output)), stepRow(old, 0)); err != nil {
			return err
		}
		entry := pipelinePlanEntry(old, 0)
		entry["inputs"] = []any{}
		mapOf(entry["dispatch"])["phase"] = "handed_off"
		mapOf(anys(old["steps"])[0])["outputArtifactId"] = "prefix-output"
		if err := completeStep(q, old, 0, output, nil); err != nil {
			return err
		}
		return finishRun(q, old, "succeeded", "")
	})
	if err != nil {
		t.Fatal(err)
	}
	var derived doc
	err = runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		q.person = &Person{Email: testOwnerEmail, Owner: true}
		source, err := getRun(q, astr(old["id"]))
		if err != nil {
			return err
		}
		reuse, err := succeededPrefix(q, source, definition)
		if err != nil {
			return err
		}
		if len(reuse) != 1 {
			return errors.New("verified source was not reused")
		}
		derived, err = pipelineStart(q, doc{"id": "prefix-derived", "draftDefinition": definition, "projectId": "hello-world", "rerunOf": old["id"], "reuse": reuse})
		if err != nil {
			return err
		}
		var n int
		if err = q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM execution WHERE parent_execution_id=$1", derived["id"]).Scan(&n); err != nil {
			return err
		}
		if n != 0 || mapOf(pipelinePlanEntry(derived, 0)["executionRef"])["executionId"] != stepRow(old, 0) || mapOf(pipelinePlanEntry(derived, 0)["executionRef"])["mode"] != "reused" {
			return errors.New("reuse fabricated work or changed source identity")
		}
		return finishRun(q, derived, "succeeded", "")
	})
	if err != nil {
		t.Fatal(err)
	}
	if derived["state"] != "succeeded" {
		t.Fatal(derived)
	}
	err = runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		if err := exec(q, "UPDATE artifact SET content_hash=$2 WHERE id=$1", "prefix-output", sha256hex([]byte("corrupt"))); err != nil {
			return err
		}
		source, err := getRun(q, astr(derived["id"]))
		if err != nil {
			return err
		}
		_, err = succeededPrefix(q, source, definition)
		return err
	})
	var conflict *HTTPError
	if !errors.As(err, &conflict) || conflict.Code != 409 {
		t.Fatal("corrupt reused output accepted", err)
	}
}
