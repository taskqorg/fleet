// Delivery history and health (docs/designs/fleet-delivery-pipeline.md, Delivery health): one record per merged pull
// request, per release and per merge-queue run of the fleet repository, read from GitHub through the github connection
// every 10 minutes (the first pass backfills from the repository start), with host installs and the UI on site recorded
// by the coordinator from the time it first ran this pass. Over the records: the rolling 7-day baseline (P50/P90 per
// stage and per job), named snapshots, and the regression check (daily and after each release) that holds one needs-you
// question per worsened stage or job and withdraws it when the value is back (problemsSync, source delivery).
// Stored in tables (migration 82: delivery_pr, delivery_run, delivery_release, delivery_baseline, delivery_check,
// delivery_settings, delivery_history_state with delivery_ui_seen, delivery_run_read): a few hundred rows a week,
// business records rather than spans, so Postgres and not ClickHouse.
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
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

const (
	deliveryRepoStart = "2026-11-08" // the fleet repository's first day; the backfill reads from here
	deliveryWorkflow  = "ci.yml"
)

// deliveryStageKeys: the stage durations of a pull request, in pipeline order (DeliveryPr.durations).
var deliveryStageKeys = []string{"pr_ci", "enqueue", "queue", "merge", "release", "install", "ui", "ui_install", "lead", "total"}

// deliveryBasis: what install and total measure. exact-install-v1: total ends at the exact coordinator install;
// delivered-v2 (now): the same, or for a UI-only release at the site serving its UI, whose UI on site is ui_install, no
// longer ui. A v1 baseline still compares install, not total or ui.
const deliveryBasis = "delivered-v2"

// deliveryBasisOf: the bases under which a baseline's stat compares with today's; a stat not listed compares under any.
var deliveryBasisOf = map[string][]string{"install": {"exact-install-v1", deliveryBasis}, "total": {deliveryBasis}, "ui": {deliveryBasis}}

var deliveryDefaults = contract.DeliverySettings{Ratio: 1.5, MinSamples: 5, MinDeltaMs: 60_000}

// dhState: the pass's own state: delivery_history_state and delivery_ui_seen, the runs to read again (delivery_run_read)
// and each pull request's PR CI lookups (the retry columns of delivery_pr).
type dhState struct {
	BackfilledFrom string
	Backfilled     bool
	Through        string // the last UTC day whose runs were read
	PullsUpdated   string
	InstallsFrom   string
	UISeen         map[string]string // UI version to when the site first served it
	RefreshedAt    string
	Problem        string
	CheckedRelease string
	CheckedAt      string            // the pass's last check; the reply is delivery_check
	RetryRuns      []dhRetry         // runs whose jobs read failed, read again each pass
	CiTried        map[string]string // pull request number to its last PR CI lookup while none was found
	CiRetry        map[string]int    // pull request number to failed PR CI lookups, read again up to dhRetries
	CiError        map[string]string // pull request number to its last failed PR CI lookup
}

var coordinatorStarted = time.Now().UTC()

func init() {
	onStart(func(ctx context.Context, a *App) {
		every(ctx, "delivery-history", envMs("FLEET_DELIVERY_HISTORY_TICK_MS", 10*60*1000), func(ctx context.Context) error { return deliveryHistoryPass(ctx, a) })
	})
	op("delivery_history", deliveryHistory)
	op("delivery_baseline", deliveryBaseline)
	op("delivery_baseline_set", deliveryBaselineSet)
	op("delivery_regressions", deliveryRegressionsOp)
}

// ---- storage ----

// dhPut stores one record of kind (its id is "<kind>:<key>", the key its table's unique column).
func dhPut(q *Request, kind, id string, v any) error {
	d, payload := q.db(), jsonBytes(v)
	key := strings.TrimPrefix(id, kind+":")
	number := func() (int64, error) {
		n, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("delivery record %s: %w", id, err)
		}
		return n, nil
	}
	switch kind {
	case "delivery-pr":
		n, err := number()
		if err != nil {
			return err
		}
		return d.DeliveryPrPut(q.ctx(), db.DeliveryPrPutParams{ID: id, Payload: payload, ProjectID: ciProject, Number: n})
	case "delivery-run":
		n, err := number()
		if err != nil {
			return err
		}
		return d.DeliveryRunPut(q.ctx(), db.DeliveryRunPutParams{ID: id, Payload: payload, ProjectID: ciProject, RunID: n})
	case "delivery-release":
		return d.DeliveryReleasePut(q.ctx(), db.DeliveryReleasePutParams{ID: id, Payload: payload, ProjectID: ciProject, Tag: key})
	case "delivery-baseline":
		return d.DeliveryBaselinePut(q.ctx(), db.DeliveryBaselinePutParams{ID: id, Payload: payload, ProjectID: ciProject, Name: key})
	case "delivery-check":
		return d.DeliveryCheckPut(q.ctx(), db.DeliveryCheckPutParams{ID: id, Payload: payload, ProjectID: ciProject})
	}
	return fmt.Errorf("unknown delivery record kind %s", kind)
}

// dhLoad: the records of kind in id order; one that does not decode as T is left out.
func dhLoad[T any](q *Request, kind string) ([]T, error) {
	read := map[string]func(context.Context) ([][]byte, error){"delivery-pr": q.db().DeliveryPrPayloads, "delivery-run": q.db().DeliveryRunPayloads,
		"delivery-release": q.db().DeliveryReleasePayloads, "delivery-baseline": q.db().DeliveryBaselinePayloads}[kind]
	if read == nil {
		return nil, fmt.Errorf("unknown delivery record kind %s", kind)
	}
	rs, err := read(q.ctx())
	out := []T{}
	for _, r := range rs {
		var v T
		if json.Unmarshal(r, &v) == nil {
			out = append(out, v)
		}
	}
	return out, err
}

func dhStateOf(q *Request) (dhState, error) {
	st := dhState{UISeen: map[string]string{}, CiTried: map[string]string{}, CiRetry: map[string]int{}, CiError: map[string]string{}}
	d, err := q.db().DeliveryHistoryState(q.ctx())
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return st, err
	}
	st.BackfilledFrom, st.Backfilled, st.Through, st.PullsUpdated = d.BackfilledFrom, d.Backfilled, d.Through, d.PullsUpdated
	st.InstallsFrom, st.RefreshedAt, st.Problem = d.InstallsFrom, d.RefreshedAt, d.Problem
	st.CheckedRelease, st.CheckedAt = d.CheckedRelease, d.CheckedAt
	seen, err := q.db().DeliveryUISeen(q.ctx())
	if err != nil {
		return st, err
	}
	for _, r := range seen {
		st.UISeen[r.Version] = r.SeenAt
	}
	runs, err := q.db().DeliveryRunReads(q.ctx())
	if err != nil {
		return st, err
	}
	for _, r := range runs {
		var run dhRun
		if json.Unmarshal(r.Payload, &run) == nil {
			st.RetryRuns = append(st.RetryRuns, dhRetry{Run: run, Tries: int(r.Attempts), Error: r.LastError})
		}
	}
	prs, err := q.db().DeliveryPrRetries(q.ctx())
	if err != nil {
		return st, err
	}
	for _, r := range prs {
		n := strconv.FormatInt(r.Number, 10)
		if r.Attempts > 0 {
			st.CiRetry[n] = int(r.Attempts)
		}
		if r.LastError != "" {
			st.CiError[n] = r.LastError // kept when the lookups were given up
		}
		if r.NextAttemptAt != nil {
			st.CiTried[n] = r.NextAttemptAt.Add(-time.Hour).UTC().Format(time.RFC3339) // a lookup is due an hour after the last
		}
	}
	return st, nil
}

// dhSave writes the pass's state: its row, the UI first seen, the runs to read again (the others go) and the PR CI
// lookup state on the stored pull requests (the work kit's retry columns: attempts, next_attempt_at, last_error).
func dhSave(q *Request, st dhState) error {
	d := q.db()
	if err := d.DeliveryHistoryStateSave(q.ctx(), db.DeliveryHistoryStateSaveParams{BackfilledFrom: st.BackfilledFrom, Backfilled: st.Backfilled,
		Through: st.Through, PullsUpdated: st.PullsUpdated, InstallsFrom: st.InstallsFrom, RefreshedAt: st.RefreshedAt, Problem: st.Problem,
		CheckedRelease: st.CheckedRelease, CheckedAt: st.CheckedAt, ProjectID: ciProject}); err != nil {
		return err
	}
	type seen struct {
		V string `json:"v"`
		A string `json:"a"`
	}
	ui := []seen{}
	for v, at := range st.UISeen {
		ui = append(ui, seen{v, at})
	}
	if err := d.DeliveryUISeenSave(q.ctx(), db.DeliveryUISeenSaveParams{Seen: jsonBytes(ui), ProjectID: ciProject}); err != nil {
		return err
	}
	type read struct {
		I int64           `json:"i"`
		P json.RawMessage `json:"p"`
		T int             `json:"t"`
		E string          `json:"e"`
	}
	ids, reads := []int64{}, []read{}
	for _, x := range st.RetryRuns {
		ids, reads = append(ids, x.Run.ID), append(reads, read{x.Run.ID, jsonBytes(x.Run), x.Tries, x.Error})
	}
	if err := d.DeliveryRunReadsKeep(q.ctx(), ids); err != nil {
		return err
	}
	if err := d.DeliveryRunReadsSave(q.ctx(), db.DeliveryRunReadsSaveParams{Reads: jsonBytes(reads), ProjectID: ciProject}); err != nil {
		return err
	}
	keys := map[string]bool{}
	for n := range st.CiTried {
		keys[n] = true
	}
	for n := range st.CiRetry {
		keys[n] = true
	}
	for n := range st.CiError {
		keys[n] = true
	}
	type lookup struct {
		N int64  `json:"n"`
		A int    `json:"a"`
		T string `json:"t"` // the next lookup (RFC 3339), "" for none
		E string `json:"e"`
	}
	prs := []lookup{}
	for n := range keys {
		pr, err := strconv.ParseInt(n, 10, 64)
		if err != nil {
			continue
		}
		next := ""
		if t, err := time.Parse(time.RFC3339, st.CiTried[n]); err == nil {
			next = t.Add(time.Hour).Format(time.RFC3339)
		}
		prs = append(prs, lookup{pr, st.CiRetry[n], next, st.CiError[n]})
	}
	return d.DeliveryPrRetrySave(q.ctx(), jsonBytes(prs))
}

func deliverySettingsOf(q *Request) (contract.DeliverySettings, error) {
	d, err := q.db().DeliverySettingsGet(q.ctx())
	if errors.Is(err, pgx.ErrNoRows) {
		return deliveryDefaults, nil
	}
	if err != nil {
		return deliveryDefaults, err
	}
	return contract.DeliverySettings{Ratio: d.Ratio, MinSamples: d.MinSamples, MinDeltaMs: d.MinDeltaMs, Baseline: d.Baseline}, nil
}

func deliverySettingsPut(q *Request, s contract.DeliverySettings) error {
	return q.db().DeliverySettingsPut(q.ctx(), db.DeliverySettingsPutParams{Ratio: s.Ratio, MinSamples: s.MinSamples, MinDeltaMs: s.MinDeltaMs,
		Baseline: s.Baseline, ProjectID: ciProject})
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
	Run   dhRun
	Tries int
	Error string // the last failed read
}

const dhRetries = 5

// dhBatch reads run at a time, dhWorkers of them at once: each gateway call mints and revokes a token (seconds), so
// the backfill's hundreds of reads run side by side, and each batch is stored before the next.
const dhBatch, dhWorkers = 32, 8

// dhParallel runs fn for 0..n-1, dhWorkers at a time (parallel, background.go), and returns the results and errors by index.
func dhParallel[T any](n int, fn func(i int) (T, error)) ([]T, []error) {
	return parallel(n, dhWorkers, fn)
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
	var obs []db.DeliveryHostsSeenRow
	if err := runTx(ctx, a, nil, false, "", func(q *Request) (err error) {
		if _, _, keyed, err = githubConnectionOf(q, ciOwner); err != nil {
			return err
		}
		if st, err = dhStateOf(q); err != nil {
			return err
		}
		obs, err = deliveryHostsSeen(q)
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
			return dhSave(q, st)
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
	// GitHub's 429 says nothing about the item either: the pass stops and the next one goes on. A 403 may be the item's
	// own (not accessible), so it uses up a retry like any other GitHub error.
	refused := func(err error) bool {
		m := err.Error()
		return !strings.Contains(m, ": http-") || strings.Contains(m, ": http-429")
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
		// After the backfill one page: GitHub's list is not by date, but a new ui-* release sorts right after its day's native
		// ones (ui-1.261117.26-v1.261117.27 was 33rd), well inside 100.
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
			return dhSave(q, st)
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
				retryNext = append(retryNext, dhRetry{r, tries[r.ID] + 1, err.Error()}) // read again next pass, outside the window too
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
					st.CiRetry, st.CiError = map[string]int{}, map[string]string{}
				}
				st.CiError[num] = err.Error()
				if st.CiRetry[num]++; st.CiRetry[num] >= dhRetries {
					delete(st.CiRetry, num) // given up: the pull request keeps no PR CI time
					tried[num] = now.Format(time.RFC3339)
				}
				continue
			}
			delete(st.CiRetry, num)
			delete(st.CiError, num)
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
	// The earliest this process saw each UI served: delivery_status's pass notes it every minute, so a UI-only release's
	// UI on site (its total's end) is not up to this pass's 10 minutes late. Noted here too for a coordinator whose
	// delivery_status pass is off (FLEET_DELIVERY_TICK_MS=0).
	// The time of the read, not the pass's start: the reads above take minutes in a backfill.
	ui, uiAt := uiServed(), time.Now().UTC()
	noteUISeen(ui, uiAt)
	for v, at := range uiSeenSnapshot() {
		if st.UISeen[v] == "" || at < st.UISeen[v] {
			st.UISeen[v] = at
		}
	}
	hosts := deliveryHostVersions(obs, now)
	for i := range releases {
		deliveryInstalls(&releases[i], st, hosts, hostVersion{ui, uiAt.Format(time.RFC3339)}, now)
		deliveryUpdateObservations(&releases[i], obs)
	}
	runList := make([]contract.DeliveryRun, 0, len(have))
	for _, r := range have {
		runList = append(runList, r)
	}
	deliveryDerive(prs, runList, releases)
	newest, newestAt := "", ""
	for _, r := range releases {
		if deliveryReleaseTag(r.Tag) && r.Published > newestAt {
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
		return dhSave(q, st)
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
	if _, m, ok := selectedUIRelease(); ok && m.Version != "" {
		return m.Version
	}
	return version
}

// uiSeenAt: UI version to the first time this process saw the site serve it (RFC 3339), noted by every delivery pass.
var uiSeenAt = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

func noteUISeen(v string, at time.Time) {
	uiSeenAt.Lock()
	defer uiSeenAt.Unlock()
	if v != "" && uiSeenAt.m[v] == "" {
		uiSeenAt.m[v] = at.UTC().Format(time.RFC3339)
	}
}

func uiSeenSnapshot() map[string]string {
	uiSeenAt.Lock()
	defer uiSeenAt.Unlock()
	return maps.Clone(uiSeenAt.m)
}

type hostVersion struct{ version, at string }

// deliveryProblemData: what a delivery regression's owner problem records besides its cause and text.
type deliveryProblemData struct {
	Key        string   `json:"key"`
	Metric     string   `json:"metric"`
	BaselineMs int64    `json:"baselineMs"`
	CurrentMs  int64    `json:"currentMs"`
	Links      []string `json:"links"`
}

// deliveryHostsSeen: every host's observation as the delivery reads it (last seen in ms, executor version, update).
func deliveryHostsSeen(q *Request) ([]db.DeliveryHostsSeenRow, error) {
	return q.db().DeliveryHostsSeen(q.ctx())
}

// deliveryHostVersions: each host seen in the last hour with its executor version and when it got it (the healthy
// update's time when it names that version, else now), plus "coordinator": this process.
func deliveryHostVersions(obs []db.DeliveryHostsSeenRow, now time.Time) map[string]hostVersion {
	out := map[string]hostVersion{"coordinator": {version, coordinatorStarted.Format(time.RFC3339)}}
	for _, o := range obs {
		v := o.Version
		if v == "" || now.UnixMilli()-int64(o.LastSeenMs) > 3600_000 {
			continue
		}
		u, ok := decodeUpdate(json.RawMessage(o.Update))
		if !ok || u.Version != v || u.State != "ok" {
			continue
		}
		at := u.At
		if at == "" {
			at = now.Format(time.RFC3339)
		}
		out[o.ID] = hostVersion{v, at}
	}
	return out
}

// deliveryObservation distinguishes exact installation from availability of a change.
// An older observation cannot establish availability before publication.
func deliveryObservation(tag, published string, hv hostVersion, now time.Time) (at string, exact bool) {
	if strings.HasPrefix(tag, "ui-") || hv.version == "" || versionLess(hv.version, releaseVersionOf(tag)) {
		return "", false
	}
	at = hv.at
	if at == "" || at < published {
		at = now.Format(time.RFC3339)
	}
	return at, hv.version == releaseVersionOf(tag)
}

// deliveryInstalls: a release published since installsFrom is installed on a host when the host runs it or newer
// (the first time seen); its UI is on the site when the site first served a UI of its version or newer. served: the UI
// the site serves now and when that was read; a release no sighting qualifies for is on the site then, as delivery_status
// says, so the two never disagree for long.
func deliveryInstalls(r *contract.DeliveryRelease, st dhState, hosts map[string]hostVersion, served hostVersion, now time.Time) {
	if r.Published < st.InstallsFrom || !deliveryReleaseTag(r.Tag) {
		return
	}
	v := releaseVersionOf(r.Tag)
	if r.Installed == nil {
		r.Installed = map[string]string{}
	}
	if r.InstalledVersions == nil {
		r.InstalledVersions = map[string]string{}
	}
	if r.Available == nil {
		r.Available = map[string]string{}
	}
	if r.AvailabilityVersions == nil {
		r.AvailabilityVersions = map[string]string{}
	}
	for h, hv := range hosts {
		at, exact := deliveryObservation(r.Tag, r.Published, hv, now)
		if at == "" {
			continue
		}
		if r.Available[h] == "" {
			r.Available[h] = at
			r.AvailabilityVersions[h] = hv.version
		}
		// Never attach today's version to a timestamp retained from a legacy observation.
		if exact && r.Installed[h] == "" {
			r.Installed[h] = at
			r.InstalledVersions[h] = hv.version
		}
	}
	if r.UiOnSite == nil {
		// A sighting before GitHub's publication time is the coordinator's clock behind GitHub's: a UI-only release's own
		// version exists only once release.yml publishes it (versions are never reused), and any other seen within a
		// minute is the same skew.
		skew := r.Published
		if p, err := time.Parse(time.RFC3339, r.Published); err == nil {
			skew = p.Add(-time.Minute).Format(time.RFC3339)
		}
		for uv, at := range st.UISeen {
			if at < r.Published && (uv == v && uiOnlyRelease(r.Tag) || at >= skew) {
				at = r.Published
			}
			if !versionLess(uv, v) && at >= r.Published && (r.UiOnSite == nil || at < *r.UiOnSite) {
				r.UiOnSite = ptr(at)
			}
		}
		// The UI served now has been served since its first sighting, and this release was on the site from then or from its
		// publication, whichever is later. A sighting after publication was taken above, so with the pass's sightings merged
		// this is a clock more than a minute behind GitHub's: on the site at publication, as delivery_status says.
		if r.UiOnSite == nil && served.version != "" && !versionLess(served.version, v) && served.at >= r.Published {
			at := served.at
			if s := st.UISeen[served.version]; s != "" && s < at {
				at = s
			}
			r.UiOnSite = ptr(max(at, r.Published))
		}
	}
}

// deliveryUpdateObservations retains actual version-specific updater results, including refusals.
func deliveryUpdateObservations(r *contract.DeliveryRelease, obs []db.DeliveryHostsSeenRow) {
	if strings.HasPrefix(r.Tag, "ui-") {
		return
	}
	for _, o := range obs {
		u, ok := decodeUpdate(json.RawMessage(o.Update))
		if !ok || u.Version != releaseVersionOf(r.Tag) || u.At == "" {
			continue
		}
		if r.UpdateObservations == nil {
			r.UpdateObservations = map[string]contract.UpdateResult{}
		}
		h := o.ID
		if old, ok := r.UpdateObservations[h]; !ok || old.At < u.At {
			r.UpdateObservations[h] = u
		}
	}
}

// releaseVersionOf: v1.2.5 is 1.2.5; ui-1.2.3-v1.2.5 (ui-<backend>-v<UI version>, release.yml) is its UI version 1.2.5.
func releaseVersionOf(tag string) string {
	if m := uiReleaseTag.FindStringSubmatch(tag); m != nil {
		return m[1]
	}
	return strings.TrimPrefix(tag, "v")
}

// uiReleaseTag: ui-<backend>-v<UI version> as release.yml names a UI-only release; the UI version is the match.
var uiReleaseTag = regexp.MustCompile(`^ui-\d+(?:\.\d+)*-v(\d+(?:\.\d+)*)$`)

// uiOnlyRelease: a ui-<backend>-v<UI version> tag, a release of the web app alone that no host installs. It is delivered
// when the site serves its UI or a newer one, which contains it (a later UI release or a later native release's bundled
// UI), so its pull requests have no install stage. watch-landing.sh's ui_ready is the exact case of that.
func uiOnlyRelease(tag string) bool { return uiReleaseTag.MatchString(tag) }

// deliveryReleaseTag: a tag that delivers the merges it covers, a native v<version> or a UI-only release; both share one
// version sequence (release.yml). Any other tag is no Fleet release.
func deliveryReleaseTag(tag string) bool { return strings.HasPrefix(tag, "v") || uiOnlyRelease(tag) }

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
		if c := mergedAt[r.Target]; deliveryReleaseTag(r.Tag) && c != "" {
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
		p.InstalledVersions, p.Available, p.AvailabilityVersions = nil, nil, nil
		p.UpdateObservations = nil
		p.Release, p.Released, p.UiOnSite, p.Installed = nil, nil, nil, map[string]string{}
		for _, v := range vs {
			if v.covers >= p.Merged {
				p.Release, p.Released, p.UiOnSite = ptr(v.r.Tag), ptr(v.r.Published), v.r.UiOnSite
				maps.Copy(p.Installed, v.r.Installed)
				p.UpdateObservations = maps.Clone(v.r.UpdateObservations)
				p.InstalledVersions = maps.Clone(v.r.InstalledVersions)
				p.Available = maps.Clone(v.r.Available)
				p.AvailabilityVersions = maps.Clone(v.r.AvailabilityVersions)
				break
			}
		}
		var installed *string
		if at := p.Installed["coordinator"]; at != "" && p.InstalledVersions["coordinator"] == releaseVersionOf(deref(p.Release)) {
			installed = &at
		}
		var available *string
		if at := p.Available["coordinator"]; at != "" {
			available = &at
		}
		// Delivered: on the PDS18 coordinator, or, for a UI-only release, when the site serves its UI (no install stage).
		// Every pass derives this again from the stored records, so records stored before UI-only releases counted get it too.
		// UI on site is ui for a native release (mostly its coordinator install and restart) and ui_install for a UI-only
		// one (its own install by the UI updater): two populations, two keys, so neither baseline moves with the mix.
		done, uiKey := installed, "ui"
		if uiOnlyRelease(deref(p.Release)) {
			done, uiKey = p.UiOnSite, "ui_install"
		}
		d := map[string]*int64{
			"availability": msBetween(p.Released, available), "time_to_available": msBetween(&p.Opened, available),
			"pr_ci": msBetween(&p.Opened, p.PrCiDone), "enqueue": msBetween(p.PrCiDone, p.Enqueued), "queue": msBetween(p.Enqueued, p.MqCiDone),
			"merge": msBetween(p.MqCiDone, &p.Merged), "release": msBetween(&p.Merged, p.Released), "install": msBetween(p.Released, installed),
			uiKey: msBetween(p.Released, p.UiOnSite), "lead": msBetween(&p.Opened, &p.Merged), "total": msBetween(&p.Opened, done),
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
		case installsFrom != "" && *p.Released >= installsFrom && p.Available["coordinator"] == "" && strings.HasPrefix(deref(p.Release), "v"):
			add("availability_wait", p, *p.Released)
		case installsFrom != "" && *p.Released >= installsFrom && p.UiOnSite == nil && uiOnlyRelease(deref(p.Release)):
			add("ui_wait", p, *p.Released) // a UI-only release not on the site yet, compared with ui_install
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

// deliveryAutomated: the first day of automatic releases and the merge queue as they run now; earlier days (manual
// releases, no queue) are history but never part of a baseline.
var deliveryAutomated = time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)

func init() {
	if t, err := time.Parse("2006-01-02", os.Getenv("FLEET_DELIVERY_BASELINE_FROM")); err == nil {
		deliveryAutomated = t // tests seed history days before the real start
	}
}

func baselineFrom(t time.Time) time.Time {
	if t.Before(deliveryAutomated) {
		return deliveryAutomated
	}
	return t
}

func deliveryWindow(prs []contract.DeliveryPr, runs []contract.DeliveryRun, from, until time.Time) contract.DeliveryBaseline {
	f, u := from.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339)
	b := contract.DeliveryBaseline{MetricBasis: ptr(deliveryBasis), From: f, Until: u, Stats: deliveryValues(prs, runs, f, u).stats()}
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
		if ok, listed := deliveryBasisOf[b.Key]; listed && !slices.Contains(ok, deref(base.MetricBasis)) {
			continue
		}
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
		baselineKey := c.Key
		switch c.Key {
		case "availability_wait":
			baselineKey = "availability"
		case "ui_wait":
			baselineKey = "ui_install"
		}
		b, ok := bs[baselineKey]
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
	base := deliveryWindow(prs, runs, baselineFrom(day.AddDate(0, 0, -7)), day)
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
			Data: docOf(deliveryProblemData{r.Key, string(r.Metric), r.BaselineMs, r.CurrentMs, r.Links})})
	}
	if err := problemsSync(q, "delivery", ps); err != nil {
		return contract.DeliveryRegressionsReply{}, err
	}
	// A held question shows the latest numbers and range, not those of the check that opened it.
	for _, p := range ps {
		if err := q.db().DeliveryProblemRefresh(q.ctx(), db.DeliveryProblemRefreshParams{Cause: &p.Cause, Text: p.Text, Data: jsonBytes(p.Data)}); err != nil {
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
	tag := deref(r.Release)
	releasePRs := map[int64]bool{}
	for _, p := range prs {
		byDate := tag == "" && r.Number == nil && p.Merged >= from && p.Merged < until
		if r.Number != nil && p.Number == *r.Number || tag != "" && deref(p.Release) == tag || byDate {
			releasePRs[p.Number] = true
			out.Prs = append(out.Prs, p)
		}
	}
	for _, x := range runs {
		if r.Number != nil && x.Pr != nil && *x.Pr == *r.Number || tag != "" && x.Pr != nil && releasePRs[*x.Pr] || tag == "" && r.Number == nil && x.Created >= from && x.Created < until {
			out.Runs = append(out.Runs, x)
		}
	}
	for _, x := range rels {
		if tag != "" && x.Tag == tag || tag == "" && r.Number == nil && x.Published >= from && x.Published < until || r.Number != nil && slices.ContainsFunc(out.Prs, func(p contract.DeliveryPr) bool { return deref(p.Release) == x.Tag }) {
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
	out.Rolling = deliveryWindow(prs, runs, baselineFrom(now.AddDate(0, 0, -7)), now) // the baseline is 7 days whatever the trend's length
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
		if err := q.db().DeliveryBaselineDelete(q.ctx(), name); err != nil {
			return contract.DeliveryBaselineReply{}, err
		}
		// The check must not compare with a snapshot that is gone while the settings still name it.
		if s, err := deliverySettingsOf(q); err != nil {
			return contract.DeliveryBaselineReply{}, err
		} else if s.Baseline == name {
			s.Baseline = ""
			if err := deliverySettingsPut(q, s); err != nil {
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
			known, err := q.db().DeliveryBaselineExists(q.ctx(), s.Baseline)
			if err != nil {
				return contract.DeliveryBaselineReply{}, err
			}
			if !known {
				return contract.DeliveryBaselineReply{}, fail(404, "Unknown baseline snapshot "+s.Baseline)
			}
		}
		if err := deliverySettingsPut(q, *s); err != nil {
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
	d, err := q.db().DeliveryCheckPayload(q.ctx())
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return contract.DeliveryRegressionsReply{}, err
	}
	if err == nil {
		var c contract.DeliveryRegressionsReply
		if json.Unmarshal(d, &c) == nil {
			return c, nil
		}
	}
	s, err := deliverySettingsOf(q)
	return contract.DeliveryRegressionsReply{Regressions: []contract.DeliveryRegression{}, Settings: s}, err
}
