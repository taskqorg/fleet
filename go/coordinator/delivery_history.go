// Delivery history and health (docs/designs/fleet-delivery-pipeline.md, Delivery health): one record per merged pull
// request, per release and per merge-queue run of the fleet repository, read from GitHub through the github connection
// every 10 minutes (the first pass backfills from the repository start), with host installs and the UI on site recorded
// by the coordinator from the time it first ran this pass. Over the records: the rolling 7-day baseline (P50/P90 per
// stage and per job), named snapshots, and the regression check (daily and after each release) that holds one needs-you
// question per worsened stage or job and withdraws it when the value is back (problemsSync, source delivery).
// Stored as documents (kinds delivery-pr, delivery-run, delivery-release, delivery-baseline, delivery-settings,
// delivery-history): a few hundred rows a week, business records rather than spans, so Postgres and not ClickHouse.
// ponytail: the fleet repository only (its releases and hosts are the Fleet ones); other repositories need their own
// release and install mapping.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"
)

const (
	deliveryRepoStart = "2026-11-08" // the fleet repository's first day; the backfill reads from here
	deliveryWorkflow  = "ci.yml"
)

// deliveryStageKeys: the stage durations of a pull request, in pipeline order (DeliveryPr.durations).
var deliveryStageKeys = []string{"pr_ci", "enqueue", "queue", "merge", "release", "install", "ui", "lead", "total"}

var deliveryDefaults = contract.DeliverySettings{Ratio: 1.5, MinSamples: 5, MinDeltaMs: 60_000}

// dhState: the pass's own record (document delivery-history).
type dhState struct {
	BackfilledFrom string            `json:"backfilledFrom,omitempty"`
	Backfilled     bool              `json:"backfilled,omitempty"`
	Through        string            `json:"through,omitempty"` // the last UTC day whose runs were read
	PullsUpdated   string            `json:"pullsUpdated,omitempty"`
	InstallsFrom   string            `json:"installsFrom,omitempty"`
	UISeen         map[string]string `json:"uiSeen,omitempty"` // UI version to when the site first served it
	RefreshedAt    string            `json:"refreshedAt,omitempty"`
	Problem        string            `json:"problem,omitempty"`
	CheckedRelease string            `json:"checkedRelease,omitempty"`
	CheckedAt      string            `json:"checkedAt,omitempty"` // the pass's last check; the reply is document delivery-check
	RetryRuns      []dhRetry         `json:"retryRuns,omitempty"` // runs whose jobs read failed, read again each pass
	CiTried        map[string]string `json:"ciTried,omitempty"`   // pull request number to its last PR CI lookup while none was found
	CiRetry        map[string]int    `json:"ciRetry,omitempty"`   // pull request number to failed PR CI lookups, read again up to dhRetries
}

var coordinatorStarted = time.Now().UTC()

func init() {
	onStart(func(ctx context.Context, a *App) {
		every(ctx, "delivery-history", envMs("FLEET_DELIVERY_HISTORY_TICK_MS", 10*60*1000), func(ctx context.Context) error { return deliveryHistoryPass(ctx, a) })
	})
	operation("delivery_history", true, typedOp(deliveryHistory))
	operation("delivery_baseline", true, typedOp(deliveryBaseline))
	operation("delivery_baseline_set", false, typedOp(deliveryBaselineSet))
	operation("delivery_regressions", true, typedOp(deliveryRegressionsOp))
}

// ---- storage ----

func dhPut(q *Request, kind, id string, v any) error {
	return exec(q, `INSERT INTO document(kind,id,project_id,data) VALUES($1,$2,$3,$4) ON CONFLICT (id) DO UPDATE SET data=EXCLUDED.data,updated_at=now()`,
		kind, id, ciProject, jsonBytes(v))
}

func dhLoad[T any](q *Request, kind string) ([]T, error) {
	rs, err := rows(q, "SELECT data FROM document WHERE kind=$1", kind)
	out := []T{}
	for _, r := range rs {
		var v T
		if json.Unmarshal(jsonBytes(r), &v) == nil {
			out = append(out, v)
		}
	}
	return out, err
}

func dhStateOf(q *Request) (dhState, error) {
	var st dhState
	d, err := first(q, "SELECT data FROM document WHERE id='delivery-history'")
	if d != nil {
		json.Unmarshal(jsonBytes(d["data"]), &st)
	}
	return st, err
}

func deliverySettingsOf(q *Request) (contract.DeliverySettings, error) {
	s := deliveryDefaults
	d, err := first(q, "SELECT data FROM document WHERE id='delivery-settings'")
	if d != nil {
		json.Unmarshal(jsonBytes(d["data"]), &s)
	}
	return s, err
}

// ---- the pass ----

type dhPull struct {
	ghPull
	UpdatedAt string `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

// dhRetry: a run whose jobs read failed, with the failures so far; it is dropped after dhRetries.
type dhRetry struct {
	Run   dhRun `json:"run"`
	Tries int   `json:"tries"`
}

const dhRetries = 5

// dhBatch reads run at a time, dhWorkers of them at once: each gateway call mints and revokes a token (seconds), so
// the backfill's hundreds of reads run side by side, and each batch is stored before the next.
const dhBatch, dhWorkers = 32, 8

// dhParallel runs fn for 0..n-1, dhWorkers at a time, and returns the results and errors by index.
func dhParallel[T any](n int, fn func(i int) (T, error)) ([]T, []error) {
	out, errs := make([]T, n), make([]error, n)
	sem, wg := make(chan struct{}, dhWorkers), sync.WaitGroup{}
	for i := range n {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			out[i], errs[i] = fn(i)
		}()
	}
	wg.Wait()
	return out, errs
}

type dhRun struct {
	ID         int64   `json:"id"`
	Attempt    int64   `json:"run_attempt"`
	Branch     string  `json:"head_branch"`
	HeadSHA    string  `json:"head_sha"`
	Status     string  `json:"status"`
	Conclusion *string `json:"conclusion"`
	CreatedAt  string  `json:"created_at"`
	StartedAt  string  `json:"run_started_at"`
	UpdatedAt  string  `json:"updated_at"`
	URL        string  `json:"html_url"`
}

func deliveryHistoryPass(ctx context.Context, a *App) error {
	var keyed bool
	var st dhState
	var obs []doc
	if err := runTx(ctx, a, nil, false, "", func(q *Request) (err error) {
		if _, _, keyed, err = githubConnectionOf(q, ciOwner); err != nil {
			return err
		}
		if st, err = dhStateOf(q); err != nil {
			return err
		}
		obs, err = all(q, "host-observation", nil)
		return err
	}); err != nil {
		return err
	}
	now := time.Now().UTC()
	save := func(fn func(q *Request) error) error {
		return runTx(ctx, a, nil, true, "", func(q *Request) error {
			if fn != nil {
				if err := fn(q); err != nil {
					return err
				}
			}
			return dhPut(q, "delivery-history", "delivery-history", st)
		})
	}
	if !keyed {
		// Keyless the backfill alone would take days of the 60 reads an hour, and Delivery's own pass needs them.
		st.Problem = "no github key: the delivery history is read through the github connection only"
		return save(nil)
	}
	read := func(into any, tool string, kv ...string) error {
		args := map[string]string{"owner": ciOwner, "repo": ciRepo}
		for i := 0; i+1 < len(kv); i += 2 {
			args[kv[i]] = kv[i+1]
		}
		b, err := githubReadKeyed(ctx, a, ciProject, tool, args)
		if err == nil {
			err = json.Unmarshal(b, into)
		}
		if err != nil {
			return fmt.Errorf("github %s %v: %w", tool, kv, err)
		}
		return nil
	}
	fail := func(err error) error {
		st.Problem = err.Error()
		if serr := save(nil); serr != nil {
			return fmt.Errorf("%w; saving the problem: %v", err, serr)
		}
		return err
	}
	// A refused read (the tool not granted, the key gone) says nothing about the item: stop instead of giving it up.
	// GitHub's own replies arrive as http-<status> codes; any other failure is the connection's or the gateway's.
	// GitHub's rate limits (403, 429) say nothing about the item either: the pass stops and the next one goes on.
	refused := func(err error) bool {
		m := err.Error()
		return !strings.Contains(m, ": http-") || strings.Contains(m, ": http-403") || strings.Contains(m, ": http-429")
	}
	backfill := !st.Backfilled
	// Pull requests: the most recently updated first; after the backfill, until a page older than the last pass.
	var pulls []dhPull
	for page := 1; page <= 50; page++ {
		var ps []dhPull
		if err := read(&ps, "pulls_closed_page", "page", strconv.Itoa(page)); err != nil {
			return fail(err)
		}
		pulls = append(pulls, ps...)
		if len(ps) < 100 || !backfill && st.PullsUpdated != "" && ps[len(ps)-1].UpdatedAt < st.PullsUpdated {
			break
		}
	}
	var rels []ghRelease
	for page := 1; page <= 20; page++ {
		var rs []ghRelease
		if err := read(&rs, "releases_page", "page", strconv.Itoa(page)); err != nil {
			return fail(err)
		}
		rels = append(rels, rs...)
		if len(rs) < 100 || !backfill {
			break
		}
	}
	// Merge-queue runs per UTC day (a run list query stops at 1,000 results): from the repository start once, then
	// yesterday and today.
	day := deliveryRepoStart
	if !backfill && st.Through != "" {
		t, _ := time.Parse("2006-01-02", st.Through)
		day = t.AddDate(0, 0, -1).Format("2006-01-02")
	}
	var runs []dhRun
	for d, _ := time.Parse("2006-01-02", day); !d.After(now); d = d.AddDate(0, 0, 1) {
		for page := 1; page <= 10; page++ {
			var r struct {
				Runs []dhRun `json:"workflow_runs"`
			}
			if err := read(&r, "workflow_runs_day", "workflow", deliveryWorkflow, "event", "merge_group", "date", d.Format("2006-01-02"), "page", strconv.Itoa(page)); err != nil {
				return fail(err)
			}
			runs = append(runs, r.Runs...)
			if len(r.Runs) < 100 {
				break
			}
		}
	}
	// ponytail: every pass loads and re-derives the whole history (hundreds of rows a week); load only the last weeks
	// once it is thousands.
	var prs []contract.DeliveryPr
	var stored []contract.DeliveryRun
	var releases []contract.DeliveryRelease
	if err := runTx(ctx, a, nil, false, "", func(q *Request) (err error) {
		if prs, err = dhLoad[contract.DeliveryPr](q, "delivery-pr"); err != nil {
			return err
		}
		if stored, err = dhLoad[contract.DeliveryRun](q, "delivery-run"); err != nil {
			return err
		}
		releases, err = dhLoad[contract.DeliveryRelease](q, "delivery-release")
		return err
	}); err != nil {
		return err
	}
	// What was stored, so the save writes only records that changed.
	before := map[string]string{}
	for _, p := range prs {
		before["delivery-pr:"+strconv.FormatInt(p.Number, 10)] = string(jsonBytes(p))
	}
	for _, r := range releases {
		before["delivery-release:"+r.Tag] = string(jsonBytes(r))
	}
	changed := func(id string, v any) bool { return before[id] != string(jsonBytes(v)) }
	var changedRuns []contract.DeliveryRun
	// flush stores what was read so far: a deploy restarts the coordinator every few minutes, and the backfill's
	// hundreds of reads must survive it (stored runs and PR CI times are not read again).
	flush := func() error {
		err := runTx(ctx, a, nil, true, "", func(q *Request) error {
			for _, r := range changedRuns {
				if err := dhPut(q, "delivery-run", "delivery-run:"+strconv.FormatInt(r.Id, 10), r); err != nil {
					return err
				}
			}
			for _, p := range prs {
				if id := "delivery-pr:" + strconv.FormatInt(p.Number, 10); changed(id, p) {
					if err := dhPut(q, "delivery-pr", id, p); err != nil {
						return err
					}
				}
			}
			return dhPut(q, "delivery-history", "delivery-history", st)
		})
		if err == nil {
			changedRuns = nil
			for _, p := range prs {
				before["delivery-pr:"+strconv.FormatInt(p.Number, 10)] = string(jsonBytes(p))
			}
		}
		return err
	}
	// Jobs of each finished run not stored yet (or re-run since).
	have := map[int64]contract.DeliveryRun{}
	for _, r := range stored {
		have[r.Id] = r
	}
	var skipped []string // single reads that failed; the pass keeps the rest
	// A run listed while still in progress is listed again: the window holds yesterday, so at least 24 hours.
	tries := map[int64]int{}
	old := now.AddDate(0, 0, -3).Format(time.RFC3339)
	for _, x := range st.RetryRuns {
		tries[x.Run.ID] = x.Tries
		if x.Tries < dhRetries && !slices.ContainsFunc(runs, func(r dhRun) bool { return r.ID == x.Run.ID }) {
			runs = append(runs, x.Run)
		}
	}
	// Given-up runs stay listed until they leave the window, so a re-listed one is not read again from zero tries.
	var keep []dhRetry
	for _, x := range st.RetryRuns {
		if x.Tries >= dhRetries && x.Run.CreatedAt >= old {
			keep = append(keep, x)
		}
	}
	retryNext := keep // st.RetryRuns stays whole until the loop ends, so a flush keeps the runs still to retry
	var todo []dhRun
	queued := map[int64]bool{} // a run on two pages (the list moved between reads) is read once
	for _, r := range runs {
		if o, ok := have[r.ID]; r.Status != "completed" || ok && o.Attempt == r.Attempt || tries[r.ID] >= dhRetries || queued[r.ID] {
			continue
		}
		queued[r.ID] = true
		todo = append(todo, r)
	}
	for c := range slices.Chunk(todo, dhBatch) {
		type jobs struct {
			Jobs []ciJob `json:"jobs"`
		}
		got, errs := dhParallel(len(c), func(i int) (jobs, error) {
			var js jobs
			return js, read(&js, "run_jobs", "run_id", strconv.FormatInt(c[i].ID, 10))
		})
		for i, r := range c {
			if err := errs[i]; err != nil {
				if refused(err) {
					return fail(errors.Join(err, flush())) // what this batch read before it stays stored
				}
				skipped = append(skipped, err.Error())
				retryNext = append(retryNext, dhRetry{r, tries[r.ID] + 1}) // read again next pass, outside the window too
				continue
			}
			dr := deliveryRunOf(r, got[i].Jobs)
			have[r.ID] = dr
			changedRuns = append(changedRuns, dr)
		}
		if err := flush(); err != nil {
			return fail(err)
		}
	}
	st.RetryRuns = retryNext
	byNum := map[int64]*contract.DeliveryPr{}
	for i := range prs {
		byNum[prs[i].Number] = &prs[i]
	}
	pullsUpdated := st.PullsUpdated
	for _, p := range pulls {
		if p.MergedAt == nil {
			continue
		}
		r := byNum[p.Number]
		if r == nil {
			r = &contract.DeliveryPr{Number: p.Number, Installed: map[string]string{}, Durations: map[string]int64{}}
			prs = append(prs, *r)
			r = &prs[len(prs)-1]
			// prs grew: the map's pointers may be stale, rebuild them
			for i := range prs {
				byNum[prs[i].Number] = &prs[i]
			}
		}
		r.Title, r.Url, r.HeadSha, r.MergeSha, r.Opened, r.Merged = p.Title, p.URL, p.Head.SHA, p.MergeSHA, p.CreatedAt, *p.MergedAt
		if p.User.Login != "" {
			r.Author = ptr(p.User.Login)
		}
		if p.UpdatedAt > pullsUpdated {
			pullsUpdated = p.UpdatedAt // stored at the end: a flush mid-pass must not skip these pages next time
		}
	}
	// PR CI done: the first green pull_request run of the ci workflow on the merged head, read once per pull request
	// (and again for two days while GitHub may still be running it).
	recent, hour := now.Add(-48*time.Hour).Format(time.RFC3339), now.Add(-time.Hour).Format(time.RFC3339)
	tried := map[string]string{}
	for k, v := range st.CiTried {
		if v >= recent || backfill { // a resumed backfill keeps every marker
			tried[k] = v
		}
	}
	st.CiTried = tried
	var look []int
	for i := range prs {
		p := &prs[i]
		num := strconv.FormatInt(p.Number, 10)
		// Read once in the backfill (a resumed one skips what it looked up), then at most hourly for two days: a head
		// without a green run stays without one.
		retry := st.CiRetry[num] > 0
		if p.PrCiDone != nil || p.HeadSha == "" || backfill && tried[num] != "" && !retry || !backfill && !retry && (p.Merged < recent || tried[num] >= hour) {
			continue
		}
		look = append(look, i)
	}
	for c := range slices.Chunk(look, dhBatch) {
		type ci struct {
			Runs []dhRun `json:"workflow_runs"`
		}
		got, errs := dhParallel(len(c), func(k int) (ci, error) {
			var r ci
			return r, read(&r, "workflow_runs_sha", "workflow", deliveryWorkflow, "event", "pull_request", "sha", prs[c[k]].HeadSha)
		})
		for k, i := range c {
			p := &prs[i]
			num := strconv.FormatInt(p.Number, 10)
			if err := errs[k]; err != nil {
				if refused(err) {
					return fail(errors.Join(err, flush())) // what this batch read before it stays stored
				}
				skipped = append(skipped, err.Error())
				if st.CiRetry == nil {
					st.CiRetry = map[string]int{}
				}
				if st.CiRetry[num]++; st.CiRetry[num] >= dhRetries {
					delete(st.CiRetry, num) // given up: the pull request keeps no PR CI time
					tried[num] = now.Format(time.RFC3339)
				}
				continue
			}
			delete(st.CiRetry, num)
			tried[num] = now.Format(time.RFC3339)
			for _, x := range got[k].Runs {
				if x.Conclusion != nil && *x.Conclusion == "success" && (p.PrCiDone == nil || x.UpdatedAt < *p.PrCiDone) {
					p.PrCiDone = ptr(x.UpdatedAt)
				}
			}
		}
		if err := flush(); err != nil {
			return fail(err)
		}
	}
	// Releases: new ones are added; installs and the UI on site are recorded for those published since installsFrom.
	if st.InstallsFrom == "" {
		st.InstallsFrom = now.Format(time.RFC3339)
	}
	relBy := map[string]*contract.DeliveryRelease{}
	for i := range releases {
		relBy[releases[i].Tag] = &releases[i]
	}
	for _, r := range rels {
		if r.Draft || r.Published == "" || relBy[r.Tag] != nil {
			continue
		}
		releases = append(releases, contract.DeliveryRelease{Tag: r.Tag, Published: r.Published, Target: r.Target, Installed: map[string]string{}})
		relBy = map[string]*contract.DeliveryRelease{}
		for i := range releases {
			relBy[releases[i].Tag] = &releases[i]
		}
	}
	if st.UISeen == nil {
		st.UISeen = map[string]string{}
	}
	if v := uiServed(); st.UISeen[v] == "" {
		st.UISeen[v] = now.Format(time.RFC3339)
	}
	hosts := deliveryHostVersions(obs, now)
	for i := range releases {
		deliveryInstalls(&releases[i], st, hosts, now)
	}
	runList := make([]contract.DeliveryRun, 0, len(have))
	for _, r := range have {
		runList = append(runList, r)
	}
	deliveryDerive(prs, runList, releases)
	newest, newestAt := "", ""
	for _, r := range releases {
		if strings.HasPrefix(r.Tag, "v") && r.Published > newestAt {
			newest, newestAt = r.Tag, r.Published
		}
	}
	if backfill {
		st.Backfilled, st.BackfilledFrom = true, deliveryRepoStart
	}
	st.Through, st.RefreshedAt, st.Problem, st.PullsUpdated = now.Format("2006-01-02"), now.Format(time.RFC3339), "", pullsUpdated
	if len(skipped) > 0 {
		st.Problem = fmt.Sprintf("%d reads failed, first: %s", len(skipped), skipped[0])
	}
	// The check: after each new release and at least daily.
	check := newest != st.CheckedRelease || st.CheckedAt < now.Add(-24*time.Hour).Format(time.RFC3339)
	err := save(func(q *Request) error {
		for _, p := range prs {
			if id := "delivery-pr:" + strconv.FormatInt(p.Number, 10); changed(id, p) {
				if err := dhPut(q, "delivery-pr", id, p); err != nil {
					return err
				}
			}
		}
		for _, r := range changedRuns {
			if err := dhPut(q, "delivery-run", "delivery-run:"+strconv.FormatInt(r.Id, 10), r); err != nil {
				return err
			}
		}
		for _, r := range releases {
			if id := "delivery-release:" + r.Tag; !changed(id, r) {
				continue
			} else if err := dhPut(q, "delivery-release", id, r); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || !check {
		return err
	}
	// Its own transaction: a failed check must not undo the records just read; it counts as done only when it ran.
	return runTx(ctx, a, nil, true, "", func(q *Request) error {
		if _, err := deliveryCheck(q, prs, runList, now, st.InstallsFrom); err != nil {
			return err
		}
		st.CheckedRelease, st.CheckedAt = newest, now.Format(time.RFC3339)
		return dhPut(q, "delivery-history", "delivery-history", st)
	})
}

func deliveryRunOf(r dhRun, jobs []ciJob) contract.DeliveryRun {
	out := contract.DeliveryRun{Id: r.ID, Attempt: r.Attempt, Url: r.URL, Sha: r.HeadSHA, Created: r.CreatedAt, Conclusion: deref(r.Conclusion), Jobs: []contract.DeliveryJob{}}
	if m := queueBranch.FindStringSubmatch(r.Branch); m != nil {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		out.Pr = &n
	}
	if r.StartedAt != "" {
		out.Started = ptr(r.StartedAt)
	}
	out.Completed = ptr(r.UpdatedAt)
	if d := msBetween(out.Started, out.Completed); d != nil {
		out.WallMs = d
	}
	for _, j := range jobs {
		c := deref(j.Conclusion)
		if c == "skipped" || j.CompletedAt == nil {
			continue
		}
		runner, hosted := ciRunner(j)
		dj := contract.DeliveryJob{Name: j.Name, Conclusion: c, Runner: runner, Hosted: hosted}
		if d := msBetween(&j.StartedAt, j.CompletedAt); d != nil {
			dj.DurationMs = *d
		}
		if d := msBetween(&j.CreatedAt, &j.StartedAt); d != nil {
			dj.WaitMs = *d
		}
		out.Jobs = append(out.Jobs, dj)
	}
	return out
}

// msBetween: b-a in ms when both parse and b is not before a.
func msBetween(a, b *string) *int64 {
	if a == nil || b == nil {
		return nil
	}
	x, e1 := time.Parse(time.RFC3339, *a)
	y, e2 := time.Parse(time.RFC3339, *b)
	if e1 != nil || e2 != nil || y.Before(x) {
		return nil
	}
	d := y.Sub(x).Milliseconds()
	return &d
}

// uiServed: the UI version the site serves now (the independently installed UI when it matches this backend, else the
// bundled one), as appRoutes picks it.
func uiServed() string {
	if root := os.Getenv("FLEET_UI_ROOT"); root != "" {
		var m struct{ Kind, Version, BackendVersion string }
		if cur, err := filepath.EvalSymlinks(filepath.Join(root, "current")); err == nil {
			if b, err := os.ReadFile(filepath.Join(cur, "release.json")); err == nil && json.Unmarshal(b, &m) == nil && m.Kind == "ui" && m.BackendVersion == version && m.Version != "" {
				return m.Version
			}
		}
	}
	return version
}

type hostVersion struct{ version, at string }

// deliveryHostVersions: each host seen in the last hour with its executor version and when it got it (the healthy
// update's time when it names that version, else now), plus "coordinator": this process.
func deliveryHostVersions(obs []doc, now time.Time) map[string]hostVersion {
	out := map[string]hostVersion{"coordinator": {version, coordinatorStarted.Format(time.RFC3339)}}
	for _, o := range obs {
		seen, _ := numOK(o["lastSeenAt"])
		v := astr(o["version"])
		if v == "" || now.UnixMilli()-int64(seen) > 3600_000 {
			continue
		}
		at := now.Format(time.RFC3339)
		if u, ok := decodeUpdate(o["update"]); ok && u.Version == v && u.State == "ok" && u.At != "" {
			at = u.At
		}
		out[astr(o["id"])] = hostVersion{v, at}
	}
	return out
}

// deliveryInstalls: a release published since installsFrom is installed on a host when the host runs it or newer
// (the first time seen); its UI is on the site when the site first served a UI of its version or newer.
func deliveryInstalls(r *contract.DeliveryRelease, st dhState, hosts map[string]hostVersion, now time.Time) {
	if r.Published < st.InstallsFrom {
		return
	}
	v := releaseVersionOf(r.Tag)
	if r.Installed == nil {
		r.Installed = map[string]string{}
	}
	for h, hv := range hosts {
		// A ui-* release ships the web app only: hosts never install it.
		if !strings.HasPrefix(r.Tag, "ui-") && r.Installed[h] == "" && !versionLess(hv.version, v) {
			at := hv.at
			if at < r.Published { // a newer version reported with an older update time: never before publication
				at = now.Format(time.RFC3339)
			}
			r.Installed[h] = at
		}
	}
	if r.UiOnSite == nil {
		for uv, at := range st.UISeen {
			if !versionLess(uv, v) && at >= r.Published && (r.UiOnSite == nil || at < *r.UiOnSite) {
				r.UiOnSite = ptr(at)
			}
		}
	}
}

// releaseVersionOf: v1.2.5 is 1.2.5; ui-1.2.3-v1.2.5 (ui-<backend>-v<UI version>, release.yml) is its UI version 1.2.5.
func releaseVersionOf(tag string) string {
	if i := strings.LastIndex(tag, "-v"); strings.HasPrefix(tag, "ui-") && i >= 0 {
		return tag[i+2:]
	}
	return strings.TrimPrefix(tag, "v")
}

// deliveryDerive fills each pull request's queue fields, release, installs and durations from the runs and releases.
func deliveryDerive(prs []contract.DeliveryPr, runs []contract.DeliveryRun, releases []contract.DeliveryRelease) {
	mergedAt := map[string]string{}
	for _, p := range prs {
		mergedAt[p.MergeSha] = p.Merged
	}
	type rel struct {
		r      contract.DeliveryRelease
		covers string
	}
	var vs []rel
	for _, r := range releases {
		// A ui-* release is the release of the merges it covers too: release.yml cuts one instead of a v* release when
		// only the web app changed since the last release.
		if c := mergedAt[r.Target]; (strings.HasPrefix(r.Tag, "v") || strings.HasPrefix(r.Tag, "ui-")) && c != "" {
			vs = append(vs, rel{r, c})
		}
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].r.Published < vs[j].r.Published })
	byPR := map[int64][]contract.DeliveryRun{}
	for _, r := range runs {
		if r.Pr != nil {
			byPR[*r.Pr] = append(byPR[*r.Pr], r)
		}
	}
	for i := range prs {
		p := &prs[i]
		p.Enqueued, p.MqCiDone, p.QueueRuns, p.QueueRemovals, p.Flakes = nil, nil, 0, 0, 0
		rs := byPR[p.Number]
		sort.Slice(rs, func(a, b int) bool { return rs[a].Created < rs[b].Created })
		var lastGreen *string
		for _, r := range rs {
			if r.Created > p.Merged {
				continue // a later queue entry of a reused number cannot belong to this merge
			}
			p.QueueRuns++
			if p.Enqueued == nil {
				p.Enqueued = ptr(r.Created)
			}
			switch r.Conclusion {
			case "success":
				lastGreen = r.Completed
				if r.Sha == p.MergeSha {
					p.MqCiDone = r.Completed
				}
			case "failure", "timed_out":
				p.QueueRemovals++
				if p.PrCiDone != nil && *p.PrCiDone < r.Created {
					p.Flakes++ // the head that landed had passed before this failure: no new commit was needed
				}
			}
		}
		if p.MqCiDone == nil {
			p.MqCiDone = lastGreen
		}
		p.Release, p.Released, p.UiOnSite, p.Installed = nil, nil, nil, map[string]string{}
		for _, v := range vs {
			if v.covers >= p.Merged {
				p.Release, p.Released, p.UiOnSite = ptr(v.r.Tag), ptr(v.r.Published), v.r.UiOnSite
				maps.Copy(p.Installed, v.r.Installed)
				break
			}
		}
		var installed *string
		if at := p.Installed["coordinator"]; at != "" {
			installed = &at
		}
		d := map[string]*int64{
			"pr_ci": msBetween(&p.Opened, p.PrCiDone), "enqueue": msBetween(p.PrCiDone, p.Enqueued), "queue": msBetween(p.Enqueued, p.MqCiDone),
			"merge": msBetween(p.MqCiDone, &p.Merged), "release": msBetween(&p.Merged, p.Released), "install": msBetween(p.Released, installed),
			"ui": msBetween(p.Released, p.UiOnSite), "lead": msBetween(&p.Opened, &p.Merged), "total": msBetween(&p.Opened, installed),
		}
		p.Durations = map[string]int64{}
		for k, v := range d {
			if v != nil {
				p.Durations[k] = *v
			}
		}
	}
}

// ---- statistics ----

// pctl: nearest rank, half up (as scripts/delivery-latency.sh).
func pctl(sorted []int64, q float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	return sorted[min(len(sorted)-1, int(q*float64(len(sorted)-1)+0.5))]
}

type deliverySeries map[string][]deliveryValue

type deliveryValue struct {
	at  string // merge time (stages) or run creation (runs, jobs)
	ms  int64
	pr  int64
	url string
}

// deliveryValues: every measured value in [from, until) per stat key.
func deliveryValues(prs []contract.DeliveryPr, runs []contract.DeliveryRun, from, until string) deliverySeries {
	out := deliverySeries{}
	for _, p := range prs {
		if p.Merged < from || p.Merged >= until {
			continue
		}
		for k, v := range p.Durations {
			out[k] = append(out[k], deliveryValue{p.Merged, v, p.Number, p.Url})
		}
	}
	for _, r := range runs {
		if r.Created < from || r.Created >= until || r.Conclusion != "success" {
			continue // a failed or cancelled run's times say how far it got, not how long a landing takes
		}
		pr := int64(0)
		if r.Pr != nil {
			pr = *r.Pr
		}
		if r.WallMs != nil {
			out["run"] = append(out["run"], deliveryValue{r.Created, *r.WallMs, pr, r.Url})
		}
		for _, j := range r.Jobs {
			out["job:"+j.Name] = append(out["job:"+j.Name], deliveryValue{r.Created, j.DurationMs, pr, r.Url})
			out["wait:"+j.Name] = append(out["wait:"+j.Name], deliveryValue{r.Created, j.WaitMs, pr, r.Url})
		}
	}
	for k := range out {
		sort.Slice(out[k], func(i, j int) bool { return out[k][i].at < out[k][j].at })
	}
	return out
}

// deliveryPending adds, for pull requests merged in [from, now) still short of a release (or of the coordinator install
// of a release published since installsFrom), the time waited so far: a stalled release or install then raises the
// stage's values instead of leaving it with fewer of them.
func deliveryPending(s deliverySeries, prs []contract.DeliveryPr, from, now, installsFrom string) {
	add := func(k string, p contract.DeliveryPr, since string) {
		if d := msBetween(&since, &now); d != nil {
			s[k] = append(s[k], deliveryValue{p.Merged, *d, p.Number, p.Url})
		}
	}
	for _, p := range prs {
		if p.Merged < from || p.Merged >= now {
			continue
		}
		switch {
		case p.Released == nil:
			add("release", p, p.Merged)
		case installsFrom != "" && *p.Released >= installsFrom && p.Installed["coordinator"] == "" && strings.HasPrefix(deref(p.Release), "v"):
			add("install", p, *p.Released)
		}
	}
	for k := range s {
		sort.Slice(s[k], func(i, j int) bool { return s[k][i].at < s[k][j].at })
	}
}

func statKeyOrder(k string) string {
	if i := slices.Index(deliveryStageKeys, k); i >= 0 {
		return fmt.Sprintf("0%02d", i)
	}
	if k == "run" {
		return "1"
	}
	return "2" + k
}

func (s deliverySeries) stats() []contract.DeliveryStat {
	out := []contract.DeliveryStat{}
	for k, vs := range s {
		ms := make([]int64, len(vs))
		for i, v := range vs {
			ms[i] = v.ms
		}
		slices.Sort(ms)
		out = append(out, contract.DeliveryStat{Key: k, N: int64(len(ms)), P50Ms: pctl(ms, .5), P90Ms: pctl(ms, .9)})
	}
	sort.Slice(out, func(i, j int) bool { return statKeyOrder(out[i].Key) < statKeyOrder(out[j].Key) })
	return out
}

func deliveryWindow(prs []contract.DeliveryPr, runs []contract.DeliveryRun, from, until time.Time) contract.DeliveryBaseline {
	f, u := from.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339)
	b := contract.DeliveryBaseline{From: f, Until: u, Stats: deliveryValues(prs, runs, f, u).stats()}
	for _, p := range prs {
		if p.Merged >= f && p.Merged < u {
			b.Counts.Prs++
			b.Counts.Flakes += p.Flakes
		}
	}
	for _, r := range runs {
		if r.Created >= f && r.Created < u {
			b.Counts.Runs++
			if r.Conclusion == "failure" || r.Conclusion == "timed_out" {
				b.Counts.Removals++
			}
		}
	}
	return b
}

// ---- regression check ----

// deliveryCompare: the regressions of current against base (a stat with enough values in both whose P50 or P90 grew
// past ratio and by at least minDelta), each with the first value over the baseline's in the window and the range.
func deliveryCompare(prs []contract.DeliveryPr, cur deliverySeries, base contract.DeliveryBaseline, s contract.DeliverySettings) []contract.DeliveryRegression {
	bs := map[string]contract.DeliveryStat{}
	for _, b := range base.Stats {
		bs[b.Key] = b
	}
	merged := slices.Clone(prs)
	sort.Slice(merged, func(i, j int) bool { return merged[i].Merged < merged[j].Merged })
	byNum := map[int64]contract.DeliveryPr{}
	for _, p := range prs {
		byNum[p.Number] = p
	}
	out := []contract.DeliveryRegression{}
	for _, c := range cur.stats() {
		b, ok := bs[c.Key]
		if !ok || c.N < s.MinSamples || b.N < s.MinSamples {
			continue
		}
		for _, m := range []struct {
			metric contract.DeliveryMetric
			cv, bv int64
		}{{contract.DeliveryMetricP50, c.P50Ms, b.P50Ms}, {contract.DeliveryMetricP90, c.P90Ms, b.P90Ms}} {
			if float64(m.cv) <= float64(m.bv)*s.Ratio || m.cv-m.bv < s.MinDeltaMs {
				continue
			}
			r := contract.DeliveryRegression{Key: c.Key, Metric: m.metric, BaselineMs: m.bv, CurrentMs: m.cv, N: c.N, Links: []string{}}
			if m.bv > 0 {
				r.Ratio = math.Round(float64(m.cv)/float64(m.bv)*100) / 100
			}
			for _, v := range cur[c.Key] {
				if float64(v.ms) > float64(m.bv)*s.Ratio {
					if v.pr > 0 {
						r.FirstPr = ptr(v.pr)
					}
					r.Links = append(r.Links, v.url)
					break
				}
			}
			fpn := int64(-1)
			if r.FirstPr != nil {
				fpn = *r.FirstPr
			}
			if fp, ok := byNum[fpn]; ok {
				var good *contract.DeliveryPr
				for i := range merged {
					if merged[i].Merged < fp.Merged {
						good = &merged[i]
					}
				}
				last := merged[len(merged)-1] // not empty: fp is one of them
				if good != nil {
					r.LastGoodPr = ptr(good.Number)
					r.CompareUrl = ptr("https://github.com/" + ciOwner + "/" + ciRepo + "/compare/" + good.MergeSha + "..." + last.MergeSha)
					r.Links = append(r.Links, *r.CompareUrl)
				}
				if fp.Url != "" && !slices.Contains(r.Links, fp.Url) {
					r.Links = append(r.Links, fp.Url)
				}
			}
			out = append(out, r)
		}
	}
	return out
}

func fmtMs(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%.1fh", d.Hours())
	case d >= time.Minute:
		return fmt.Sprintf("%.1fm", d.Minutes())
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

// deliveryCheck: the last 24 h against the baseline (the named snapshot of the settings, else the 7 days before);
// each regression holds the question problem:delivery:<key>:<metric>, gone ones withdraw (problemsSync).
func deliveryCheck(q *Request, prs []contract.DeliveryPr, runs []contract.DeliveryRun, now time.Time, installsFrom string) (contract.DeliveryRegressionsReply, error) {
	s, err := deliverySettingsOf(q)
	if err != nil {
		return contract.DeliveryRegressionsReply{}, err
	}
	day := now.Add(-24 * time.Hour)
	base := deliveryWindow(prs, runs, day.AddDate(0, 0, -7), day)
	if s.Baseline != "" {
		snaps, err := dhLoad[contract.DeliveryBaseline](q, "delivery-baseline")
		if err != nil {
			return contract.DeliveryRegressionsReply{}, err
		}
		for _, b := range snaps {
			if deref(b.Name) == s.Baseline {
				base = b
			}
		}
	}
	f, u := day.Format(time.RFC3339), now.Format(time.RFC3339)
	cur := deliveryValues(prs, runs, f, u)
	deliveryPending(cur, prs, f, u, installsFrom)
	current := deliveryWindow(prs, runs, day, now)
	current.Stats = cur.stats() // with the pending waits the comparison uses
	regs := deliveryCompare(prs, cur, base, s)
	var ps []Problem
	for _, r := range regs {
		times := ""
		if r.Ratio > 0 {
			times = fmt.Sprintf("×%.1f, ", r.Ratio)
		}
		text := fmt.Sprintf("Delivery got slower: %s %s is %s over the last 24 h, baseline %s (%s%d values).", r.Key, strings.ToUpper(string(r.Metric)), fmtMs(r.CurrentMs), fmtMs(r.BaselineMs), times, r.N)
		if r.FirstPr != nil {
			text += fmt.Sprintf(" First over the threshold: #%d", *r.FirstPr)
			if r.LastGoodPr != nil {
				text += fmt.Sprintf(", after #%d", *r.LastGoodPr)
			}
			text += "."
		}
		if len(r.Links) > 0 {
			text += " " + strings.Join(r.Links, " ")
		}
		ps = append(ps, Problem{Cause: "problem:delivery:" + r.Key + ":" + string(r.Metric), Text: text, Rule: "delivery-regression", ProjectID: ciProject,
			Data: doc{"key": r.Key, "metric": r.Metric, "baselineMs": r.BaselineMs, "currentMs": r.CurrentMs, "links": r.Links}})
	}
	if err := problemsSync(q, "delivery", ps); err != nil {
		return contract.DeliveryRegressionsReply{}, err
	}
	// A held question shows the latest numbers and range, not those of the check that opened it.
	for _, p := range ps {
		if err := exec(q, "UPDATE question SET text=$2,data=data||$3::jsonb WHERE cause=$1 AND state IN ('open','answered') AND text<>$2", p.Cause, p.Text, jsonBytes(p.Data)); err != nil {
			return contract.DeliveryRegressionsReply{}, err
		}
	}
	out := contract.DeliveryRegressionsReply{CheckedAt: ptr(u), Baseline: &base, Current: &current, Regressions: regs, Settings: s}
	return out, dhPut(q, "delivery-check", "delivery-check", out)
}

// ---- operations ----

func deliveryMay(q *Request) error {
	if q.person != nil && !mayAccessProjectID(q.person, ciProject) {
		return fail(404, "Unknown project")
	}
	return nil
}

func deliveryTime(s string, def time.Time) (string, error) {
	if s == "" {
		return def.UTC().Format(time.RFC3339), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Format(time.RFC3339), nil
	}
	return "", fail(400, "time: RFC 3339 or YYYY-MM-DD: "+s)
}

func deliveryHistory(q *Request, r *contract.DeliveryHistoryRequest) (contract.DeliveryHistoryReply, error) {
	out := contract.DeliveryHistoryReply{Repo: ciOwner + "/" + ciRepo, Prs: []contract.DeliveryPr{}, Runs: []contract.DeliveryRun{}, Releases: []contract.DeliveryRelease{}}
	if err := deliveryMay(q); err != nil {
		return out, err
	}
	now := time.Now()
	from, err := deliveryTime(deref(r.Since), now.AddDate(0, 0, -7))
	if err != nil {
		return out, err
	}
	until, err := deliveryTime(deref(r.Until), now.Add(time.Hour))
	if err != nil {
		return out, err
	}
	st, err := dhStateOf(q)
	if err != nil {
		return out, err
	}
	out.RefreshedAt, out.BackfilledFrom, out.InstallsFrom, out.Problem = optStr(st.RefreshedAt), optStr(st.BackfilledFrom), optStr(st.InstallsFrom), optStr(st.Problem)
	prs, err := dhLoad[contract.DeliveryPr](q, "delivery-pr")
	if err != nil {
		return out, err
	}
	runs, err := dhLoad[contract.DeliveryRun](q, "delivery-run")
	if err != nil {
		return out, err
	}
	rels, err := dhLoad[contract.DeliveryRelease](q, "delivery-release")
	if err != nil {
		return out, err
	}
	for _, p := range prs {
		if r.Number != nil && p.Number == *r.Number || r.Number == nil && p.Merged >= from && p.Merged < until {
			out.Prs = append(out.Prs, p)
		}
	}
	for _, x := range runs {
		if r.Number != nil && x.Pr != nil && *x.Pr == *r.Number || r.Number == nil && x.Created >= from && x.Created < until {
			out.Runs = append(out.Runs, x)
		}
	}
	for _, x := range rels {
		if r.Number == nil && x.Published >= from && x.Published < until || r.Number != nil && slices.ContainsFunc(out.Prs, func(p contract.DeliveryPr) bool { return deref(p.Release) == x.Tag }) {
			out.Releases = append(out.Releases, x)
		}
	}
	sort.Slice(out.Prs, func(i, j int) bool { return out.Prs[i].Merged < out.Prs[j].Merged })
	sort.Slice(out.Runs, func(i, j int) bool { return out.Runs[i].Created < out.Runs[j].Created })
	sort.Slice(out.Releases, func(i, j int) bool { return out.Releases[i].Published < out.Releases[j].Published })
	return out, nil
}

func deliveryBaselineReply(q *Request, days int64) (contract.DeliveryBaselineReply, error) {
	out := contract.DeliveryBaselineReply{Snapshots: []contract.DeliveryBaseline{}, Trend: []contract.DeliveryTrendDay{}}
	if days <= 0 {
		days = 7
	}
	days = min(days, 60)
	prs, err := dhLoad[contract.DeliveryPr](q, "delivery-pr")
	if err != nil {
		return out, err
	}
	runs, err := dhLoad[contract.DeliveryRun](q, "delivery-run")
	if err != nil {
		return out, err
	}
	now := time.Now().UTC()
	out.Rolling = deliveryWindow(prs, runs, now.AddDate(0, 0, -7), now) // the baseline is 7 days whatever the trend's length
	if out.Snapshots, err = dhLoad[contract.DeliveryBaseline](q, "delivery-baseline"); err != nil {
		return out, err
	}
	sort.Slice(out.Snapshots, func(i, j int) bool { return deref(out.Snapshots[i].At) < deref(out.Snapshots[j].At) })
	today := now.Truncate(24 * time.Hour)
	for d := today.AddDate(0, 0, -int(days)+1); !d.After(today); d = d.AddDate(0, 0, 1) {
		var st []contract.DeliveryStat
		for _, s := range deliveryWindow(prs, runs, d, d.AddDate(0, 0, 1)).Stats {
			if !strings.Contains(s.Key, ":") { // stages and run; jobs are in rolling and history
				st = append(st, s)
			}
		}
		if st == nil {
			st = []contract.DeliveryStat{}
		}
		out.Trend = append(out.Trend, contract.DeliveryTrendDay{Day: d.Format("2006-01-02"), Stats: st})
	}
	out.Settings, err = deliverySettingsOf(q)
	return out, err
}

func deliveryBaseline(q *Request, r *contract.DeliveryBaselineRequest) (contract.DeliveryBaselineReply, error) {
	if err := deliveryMay(q); err != nil {
		return contract.DeliveryBaselineReply{}, err
	}
	days := int64(7)
	if r.Days != nil {
		days = *r.Days
	}
	return deliveryBaselineReply(q, days)
}

func deliveryBaselineSet(q *Request, r *contract.DeliveryBaselineSetRequest) (contract.DeliveryBaselineReply, error) {
	if !fullAccess(q.person) {
		return contract.DeliveryBaselineReply{}, fail(403, "Only the owner or full access sets the delivery baseline")
	}
	if name := strings.TrimSpace(deref(r.Snapshot)); r.Snapshot != nil {
		if name == "" || len(name) > 120 {
			return contract.DeliveryBaselineReply{}, fail(400, "snapshot: a name of 1 to 120 characters")
		}
		b, err := deliveryBaselineReply(q, 7)
		if err != nil {
			return b, err
		}
		snap := b.Rolling
		snap.Name, snap.At = ptr(name), ptr(time.Now().UTC().Format(time.RFC3339))
		if err := dhPut(q, "delivery-baseline", "delivery-baseline:"+name, snap); err != nil {
			return b, err
		}
	}
	if r.Remove != nil {
		name := strings.TrimSpace(*r.Remove)
		if err := exec(q, "DELETE FROM document WHERE kind='delivery-baseline' AND id=$1", "delivery-baseline:"+name); err != nil {
			return contract.DeliveryBaselineReply{}, err
		}
		// The check must not compare with a snapshot that is gone while the settings still name it.
		if s, err := deliverySettingsOf(q); err != nil {
			return contract.DeliveryBaselineReply{}, err
		} else if s.Baseline == name {
			s.Baseline = ""
			if err := dhPut(q, "delivery-settings", "delivery-settings", s); err != nil {
				return contract.DeliveryBaselineReply{}, err
			}
		}
	}
	if s := r.Settings; s != nil {
		s.Baseline = strings.TrimSpace(s.Baseline)
		if s.Ratio <= 1 || s.MinSamples < 1 || s.MinDeltaMs < 0 {
			return contract.DeliveryBaselineReply{}, fail(400, "settings: ratio above 1, minSamples at least 1, minDeltaMs not negative")
		}
		if s.Baseline != "" {
			d, err := first(q, "SELECT 1 AS x FROM document WHERE id=$1", "delivery-baseline:"+s.Baseline)
			if err != nil {
				return contract.DeliveryBaselineReply{}, err
			}
			if d == nil {
				return contract.DeliveryBaselineReply{}, fail(404, "Unknown baseline snapshot "+s.Baseline)
			}
		}
		if err := dhPut(q, "delivery-settings", "delivery-settings", s); err != nil {
			return contract.DeliveryBaselineReply{}, err
		}
	}
	return deliveryBaselineReply(q, 7)
}

func deliveryRegressionsOp(q *Request, r *contract.DeliveryRegressionsRequest) (contract.DeliveryRegressionsReply, error) {
	if err := deliveryMay(q); err != nil {
		return contract.DeliveryRegressionsReply{}, err
	}
	if r.Check != nil && *r.Check {
		if !fullAccess(q.person) {
			return contract.DeliveryRegressionsReply{}, fail(403, "Only the owner or full access runs the check")
		}
		var c contract.DeliveryRegressionsReply
		// The check writes questions: a read operation's transaction cannot, so it runs in its own.
		err := runTx(q.ctx(), q.app, nil, true, "", func(w *Request) error {
			prs, err := dhLoad[contract.DeliveryPr](w, "delivery-pr")
			if err != nil {
				return err
			}
			runs, err := dhLoad[contract.DeliveryRun](w, "delivery-run")
			if err != nil {
				return err
			}
			st, err := dhStateOf(w)
			if err != nil {
				return err
			}
			c, err = deliveryCheck(w, prs, runs, time.Now().UTC(), st.InstallsFrom)
			return err
		})
		return c, err
	}
	d, err := first(q, "SELECT data FROM document WHERE id='delivery-check'")
	if err != nil {
		return contract.DeliveryRegressionsReply{}, err
	}
	if d != nil {
		var c contract.DeliveryRegressionsReply
		if json.Unmarshal(jsonBytes(d["data"]), &c) == nil {
			return c, nil
		}
	}
	s, err := deliverySettingsOf(q)
	return contract.DeliveryRegressionsReply{Regressions: []contract.DeliveryRegression{}, Settings: s}, err
}
