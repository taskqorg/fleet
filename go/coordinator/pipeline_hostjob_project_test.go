package main

import "testing"

// Requirement: a script step's host job belongs to the run's project, so the files the script saves are the project's
// and the next step takes them as inputs. Risk: a host job has no session (#1108), the project came from the session,
// and the next step refuses the script's files as "Unavailable or foreign pipeline input".
func TestPipelineScriptHostJobKeepsRunProject(t *testing.T) {
	e := openTestCoordinator(t, false)
	project := "hello-world"
	run := &runRecord{ID: "hostjob-project-run", ProjectID: &project}
	var id string
	if err := e.app.store.Write(e.ctx, nil, nil, func(q *Request) error {
		var err error
		id, err = run.startScript(q, run.ID+":step:0", "fixture-route", nil, e.app.hostID, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	info, err := execGet(e.ctx, e.app, id)
	if err != nil {
		t.Fatal(err)
	}
	if deref(info.ProjectID) != project {
		t.Fatalf("host job project %q, want %q", deref(info.ProjectID), project)
	}
}
