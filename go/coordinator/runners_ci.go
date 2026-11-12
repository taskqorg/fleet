// CI half of the Runners screen: GitHub Actions jobs in flight and queued, the self-hosted runners and the CI minutes
// spent today per runner type, read server side through githubRead (github.go), never from browsers.
package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"fleet/contract"
)

// ciRepo: the repository whose CI the screen shows, and the project its keyed reads are made for.
const ciOwner, ciRepo, ciProject = "taskqorg", "fleet", "fleet"

type ciJob struct {
	ID          int64    `json:"id"`
	RunID       int64    `json:"run_id"`
	Name        string   `json:"name"`
	WorkflowRun string   `json:"workflow_name"`
	Status      string   `json:"status"`
	Conclusion  *string  `json:"conclusion"`
	URL         string   `json:"html_url"`
	Labels      []string `json:"labels"`
	RunnerName  *string  `json:"runner_name"`
	RunnerGroup *string  `json:"runner_group_name"`
	CreatedAt   string   `json:"created_at"`
	StartedAt   string   `json:"started_at"`
	CompletedAt *string  `json:"completed_at"`
}

// ciDone: jobs of completed runs, which never change, so each run's jobs are read once per coordinator process.
var (
	ciMu   sync.Mutex
	ciDone = map[int64][]ciJob{}
)

// ciRunner: a job's runner type: the runner's name for a self-hosted one, else its first label (ubuntu-latest).
func ciRunner(j ciJob) (string, bool) {
	hosted := j.RunnerGroup != nil && *j.RunnerGroup == "GitHub Actions"
	if !hosted && j.RunnerName != nil && *j.RunnerName != "" {
		return *j.RunnerName, false
	}
	for _, l := range j.Labels {
		if l == "self-hosted" { // queued for a self-hosted runner: name it by its routing label (fleet-pds18)
			return j.Labels[len(j.Labels)-1], false
		}
	}
	if len(j.Labels) > 0 {
		return j.Labels[0], true
	}
	return "unknown", true
}

func ciMs(s string) any {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil || t.IsZero() || t.Year() < 2000 {
		return nil
	}
	return t.UnixMilli()
}

func executionsCI(q *Request, _ *contract.ExecutionsCiRequest) (contract.ExecutionsCiReply, error) {
	ctx, a := q.ctx(), q.app
	args := func(kv ...string) map[string]string {
		m := map[string]string{"owner": ciOwner, "repo": ciRepo}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	problems := []string{}
	var keyed bool
	runsOf := func(tool string, kv ...string) []map[string]any {
		b, k, err := githubRead(ctx, a, ciProject, tool, args(kv...))
		keyed = keyed || k
		var r struct {
			Runs []map[string]any `json:"workflow_runs"`
		}
		if err == nil {
			err = json.Unmarshal(b, &r)
		}
		if err != nil {
			problems = append(problems, err.Error())
		}
		return r.Runs
	}
	jobsOf := func(id int64) ([]ciJob, error) {
		b, k, err := githubRead(ctx, a, ciProject, "run_jobs", args("run_id", strconv.FormatInt(id, 10)))
		keyed = keyed || k
		var r struct {
			Jobs []ciJob `json:"jobs"`
		}
		if err == nil {
			err = json.Unmarshal(b, &r)
		}
		return r.Jobs, err
	}
	now := time.Now().UTC()
	today := now.Format("2006-01-02")
	runIDs := func(rs []map[string]any) []int64 {
		out := []int64{}
		for _, r := range rs {
			if f, ok := r["id"].(float64); ok {
				out = append(out, int64(f))
			}
		}
		return out
	}
	active := append(runIDs(runsOf("runs_in_progress")), runIDs(runsOf("runs_queued"))...)
	recent := runsOf("runs_recent", "date", today)
	jobs := []any{}
	type acc struct {
		minutes float64
		jobs    int
		hosted  bool
	}
	minutes := map[string]*acc{}
	count := func(j ciJob) {
		start, _ := time.Parse(time.RFC3339, j.StartedAt)
		if start.IsZero() || start.Before(now.Truncate(24*time.Hour)) {
			return
		}
		end := now
		if j.CompletedAt != nil {
			if t, err := time.Parse(time.RFC3339, *j.CompletedAt); err == nil && !t.IsZero() {
				end = t
			}
		}
		name, hosted := ciRunner(j)
		if minutes[name] == nil {
			minutes[name] = &acc{hosted: hosted}
		}
		minutes[name].minutes += end.Sub(start).Minutes()
		minutes[name].jobs++
	}
	seen := map[int64]bool{}
	for _, id := range active {
		if seen[id] {
			continue
		}
		seen[id] = true
		js, err := jobsOf(id)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		for _, j := range js {
			count(j)
			if j.Status == "completed" {
				continue
			}
			name, hosted := ciRunner(j)
			jobs = append(jobs, doc{"id": j.ID, "runId": j.RunID, "workflow": j.WorkflowRun, "name": j.Name, "status": j.Status, "url": j.URL,
				"runner": name, "hosted": hosted, "runnerName": j.RunnerName, "createdAt": ciMs(j.CreatedAt), "startedAt": ciMs(j.StartedAt)})
		}
	}
	// Completed runs of today: their jobs count toward minutes. ponytail: at most 20 new runs read per call, so a
	// keyless coordinator stays inside 60 requests an hour; the rest fill in on later calls (partial until then).
	fresh, missing := 0, 0
	for _, id := range runIDs(recent) {
		if seen[id] {
			continue
		}
		seen[id] = true
		ciMu.Lock()
		js, ok := ciDone[id]
		ciMu.Unlock()
		if !ok {
			if fresh >= 20 {
				missing++
				continue
			}
			fresh++
			var err error
			if js, err = jobsOf(id); err != nil {
				missing++
				continue
			}
			done := true
			for _, j := range js {
				done = done && j.Status == "completed"
			}
			if done {
				ciMu.Lock()
				ciDone[id] = js
				ciMu.Unlock()
			}
		}
		for _, j := range js {
			count(j)
		}
	}
	mins := []any{}
	names := make([]string, 0, len(minutes))
	for n := range minutes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		mins = append(mins, doc{"runner": n, "hosted": minutes[n].hosted, "minutes": minutes[n].minutes, "jobs": minutes[n].jobs})
	}
	out := doc{"serverNow": now.UnixMilli(), "keyed": keyed, "jobs": jobs, "minutesToday": mins, "runsUncounted": missing, "runners": nil, "problems": problems}
	if len(recent) >= 100 {
		out["runsUncounted"] = missing + 1 // more runs today than one page holds
	}
	// Self-hosted runners' online state needs a key with organization runner read; without one it is unknown (null).
	if keyed {
		if b, _, err := githubRead(ctx, a, ciProject, "org_runners", map[string]string{"owner": ciOwner}); err != nil {
			problems = append(problems, fmt.Sprintf("runners: %v", err))
			out["problems"] = problems
		} else {
			var r struct {
				Runners []struct {
					Name   string `json:"name"`
					Status string `json:"status"`
					Busy   bool   `json:"busy"`
					Labels []struct {
						Name string `json:"name"`
					} `json:"labels"`
				} `json:"runners"`
			}
			if json.Unmarshal(b, &r) == nil {
				rs := []any{}
				for _, x := range r.Runners {
					ls := []string{}
					for _, l := range x.Labels {
						ls = append(ls, l.Name)
					}
					rs = append(rs, doc{"name": x.Name, "status": x.Status, "busy": x.Busy, "labels": ls})
				}
				out["runners"] = rs
			}
		}
	}
	return replyAs[contract.ExecutionsCiReply](out, nil)
}

func init() {
	longOp("executions_ci", typedOp(executionsCI))
}
