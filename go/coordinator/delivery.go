// Delivery status (docs/design-system/pages/delivery.md): every connected GitHub repository's pull requests by delivery
// stage, read server side every few minutes and cached here, so browsers never call GitHub. The stages follow
// scripts/delivery-latency.sh and scripts/watch-landings.sh: PR CI (pull_request runs), merge queue (merge_group runs on
// gh-readonly-queue/<base>/pr-<n>-<sha>), merged, release (the first v* release whose target commit contains the merge;
// main is linear behind the merge queue, so "contains" is "merged no later than the release's target"), installed on
// hosts (every host seen in the last hour runs that version or newer).
// Reads go through the gateway's github connection when it exists (the owner adds a read-only key: scripts/key-add.sh github); without it
// the coordinator reads the fleet repository unauthenticated (60 requests an hour per address), which the reply says.
// Each read also stores the per-stage counts, so the chart is the recorded history of the last 24 hours.
package main

import (
	"context"
	"encoding/json"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"
)

var (
	deliveryMu   sync.Mutex
	deliveryLast contract.DeliveryStatusReply = contract.DeliveryStatusReply{Source: contract.DeliverySourceNone, Items: []contract.DeliveryItem{}, Samples: []contract.DeliverySample{}}
	queueBranch                               = regexp.MustCompile(`^gh-readonly-queue/[^/]+/pr-(\d+)-`)
	githubRemote                              = regexp.MustCompile(`github\.com[:/]([A-Za-z0-9_-]+)/([A-Za-z0-9_-]+?)(\.git)?$`)
)

func init() {
	onStart(func(ctx context.Context, a *App) {
		// Keyless the five reads each pass use 50 of the 60 an hour; with a key the period can be much shorter.
		d := envMs("FLEET_DELIVERY_TICK_MS", 6*60*1000)
		if d > 0 {
			go deliveryPass(ctx, a) // the first read at start: a deploy every few minutes must not blank the page
		}
		every(ctx, "delivery", d, func(ctx context.Context) error { return deliveryPass(ctx, a) })
	})
	operation("delivery_status", true, typedOp(deliveryStatus))
}

func deliveryStatus(q *Request, _ *contract.DeliveryStatusRequest) (contract.DeliveryStatusReply, error) {
	deliveryMu.Lock()
	last := deliveryLast
	deliveryMu.Unlock()
	full := fullAccess(q.person)
	ok := func(p string) bool { return full || mayAccessProjectID(q.person, p) }
	out := last
	out.Items, out.Samples = []contract.DeliveryItem{}, []contract.DeliverySample{}
	for _, it := range last.Items {
		if ok(it.ProjectId) {
			out.Items = append(out.Items, it)
		}
	}
	for _, s := range last.Samples {
		if ok(s.ProjectId) {
			out.Samples = append(out.Samples, s)
		}
	}
	return out, nil
}

// ghPull, ghRun and ghRelease: the fields of the GitHub REST replies the stages need.
type ghPull struct {
	Number    int64   `json:"number"`
	Title     string  `json:"title"`
	URL       string  `json:"html_url"`
	CreatedAt string  `json:"created_at"`
	MergedAt  *string `json:"merged_at"`
	MergeSHA  string  `json:"merge_commit_sha"`
	AutoMerge any     `json:"auto_merge"`
	Head      struct {
		SHA string `json:"sha"`
	} `json:"head"`
}
type ghRun struct {
	Event     string `json:"event"`
	Status    string `json:"status"`
	HeadSHA   string `json:"head_sha"`
	Branch    string `json:"head_branch"`
	CreatedAt string `json:"created_at"`
}
type ghRelease struct {
	Tag       string `json:"tag_name"`
	Target    string `json:"target_commitish"`
	Draft     bool   `json:"draft"`
	Published string `json:"published_at"`
}
type ghRepo struct {
	open, closed []ghPull
	runs         []ghRun
	releases     []ghRelease
}

func deliveryPass(ctx context.Context, a *App) error {
	type target struct{ project, owner, repo string }
	var targets []target
	keyed := githubKeyed(ctx, a)
	var versions []string
	err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		ids, err := projectIDs(q)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, id := range ids {
			for _, r := range anys(projectSettingsOf(q, id)["repos"]) {
				m := githubRemote.FindStringSubmatch(astr(mapOf(r)["remote"]))
				if m == nil || seen[m[1]+"/"+m[2]] || !keyed && m[1]+"/"+m[2] != githubPublicRepo {
					continue
				}
				seen[m[1]+"/"+m[2]] = true
				targets = append(targets, target{id, m[1], m[2]})
			}
		}
		obs, err := all(q, "host-observation", nil)
		for _, o := range obs {
			if seen, _ := numOK(o["lastSeenAt"]); nowMs()-int64(seen) < 3600_000 && astr(o["version"]) != "" {
				versions = append(versions, astr(o["version"]))
			}
		}
		return err
	})
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	items, problems := []contract.DeliveryItem{}, []string{}
	counts := map[string]map[string]int64{}
	var landing int64
	for _, t := range targets {
		var g ghRepo
		get := func(tool string, into any) error {
			body, _, err := githubRead(ctx, a, t.project, tool, map[string]string{"owner": t.owner, "repo": t.repo})
			if err == nil {
				err = json.Unmarshal(body, into)
			}
			return err
		}
		var active, queued struct {
			Runs []ghRun `json:"workflow_runs"`
		}
		err := firstErr(get("pulls_open", &g.open), get("pulls_closed", &g.closed), get("runs_in_progress", &active), get("runs_queued", &queued), get("releases", &g.releases))
		if err != nil {
			problems = append(problems, t.owner+"/"+t.repo+": "+err.Error())
			continue
		}
		g.runs = append(active.Runs, queued.Runs...)
		its, l := deliveryStages(t.project, t.owner+"/"+t.repo, g, versions, now)
		landing += l
		items = append(items, its...)
		c := map[string]int64{}
		for _, s := range contract.DeliveryStageValues {
			c[string(s)] = 0
		}
		for _, it := range its {
			c[string(it.Stage)]++
		}
		counts[t.project] = sumCounts(counts[t.project], c)
	}
	samples, err := deliverySamples(ctx, a, counts, now)
	if err != nil {
		log.Printf("delivery samples: %v", err)
	}
	reply := contract.DeliveryStatusReply{Source: contract.DeliverySourcePublic, Items: items, Samples: samples, Landing: landing, RefreshedAt: ptr(now.Format(time.RFC3339))}
	if keyed {
		reply.Source = contract.DeliverySourceKey
	}
	if len(problems) > 0 {
		reply.Error = ptr(strings.Join(problems, "; "))
	}
	deliveryMu.Lock()
	if len(problems) > 0 && len(items) == 0 && deliveryLast.RefreshedAt != nil {
		// Every read failed (a rate limit, GitHub down): the last good items stand, marked with the error and their time.
		reply.Items, reply.RefreshedAt, reply.Landing = deliveryLast.Items, deliveryLast.RefreshedAt, deliveryLast.Landing
	}
	deliveryLast = reply
	deliveryMu.Unlock()
	return nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func sumCounts(a, b map[string]int64) map[string]int64 {
	if a == nil {
		return b
	}
	for k, v := range b {
		a[k] += v
	}
	return a
}

// deliveryStages: each pull request's current stage. landing counts the pull requests a worker waits on (auto-merge on
// or in the merge queue).
func deliveryStages(project, repo string, g ghRepo, versions []string, now time.Time) ([]contract.DeliveryItem, int64) {
	item := func(p ghPull, s contract.DeliveryStage, since string) contract.DeliveryItem {
		return contract.DeliveryItem{ProjectId: project, Repo: repo, Number: p.Number, Title: p.Title, Url: p.URL, Stage: s, Since: since}
	}
	// The earliest active run per pull request: pull_request runs by head commit, merge_group runs by queue branch.
	type run struct{ at, status string }
	prRun, mqRun := map[string]run{}, map[int64]run{}
	for _, r := range g.runs {
		if r.Event == "pull_request" {
			if o, ok := prRun[r.HeadSHA]; !ok || r.CreatedAt < o.at {
				prRun[r.HeadSHA] = run{r.CreatedAt, r.Status}
			}
		}
		if m := queueBranch.FindStringSubmatch(r.Branch); r.Event == "merge_group" && m != nil {
			n, _ := strconv.ParseInt(m[1], 10, 64)
			o, ok := mqRun[n]
			// One running run makes the entry "in merge-queue CI"; only queued runs leave it waiting for a runner.
			if !ok || r.Status == "in_progress" && o.status != "in_progress" || r.Status == o.status && r.CreatedAt < o.at {
				mqRun[n] = run{r.CreatedAt, r.Status}
			}
		}
	}
	var out []contract.DeliveryItem
	var landing int64
	for _, p := range g.open {
		if r, ok := mqRun[p.Number]; ok {
			landing++
			if r.status == "in_progress" {
				out = append(out, item(p, contract.DeliveryStageMqCi, r.at))
			} else {
				out = append(out, item(p, contract.DeliveryStageMqWait, r.at))
			}
			continue
		}
		if p.AutoMerge != nil {
			landing++
		}
		if r, ok := prRun[p.Head.SHA]; ok {
			out = append(out, item(p, contract.DeliveryStagePrCi, r.at))
		} else if p.AutoMerge != nil {
			out = append(out, item(p, contract.DeliveryStageMqWait, p.CreatedAt))
		} else {
			out = append(out, item(p, contract.DeliveryStagePrOpen, p.CreatedAt))
		}
	}
	// Releases: v<version> tags only (ui-* releases ship the web app on their own), oldest first, each with the merge
	// time of its target commit; a target outside the merged list falls back to the publish time.
	mergedAt := map[string]string{}
	for _, p := range g.closed {
		if p.MergedAt != nil {
			mergedAt[p.MergeSHA] = *p.MergedAt
		}
	}
	type rel struct{ tag, version, covers, published string }
	var rels []rel
	for _, r := range g.releases {
		if r.Draft || !strings.HasPrefix(r.Tag, "v") || r.Published == "" {
			continue
		}
		c := mergedAt[r.Target]
		if c == "" {
			c = r.Published
		}
		rels = append(rels, rel{r.Tag, strings.TrimPrefix(r.Tag, "v"), c, r.Published})
	}
	sort.Slice(rels, func(i, j int) bool { return rels[i].published < rels[j].published })
	day := now.Add(-24 * time.Hour).Format(time.RFC3339)
	for _, p := range g.closed {
		if p.MergedAt == nil {
			continue
		}
		m := *p.MergedAt
		if len(rels) == 0 {
			if m >= day {
				out = append(out, item(p, contract.DeliveryStageMerged, m))
			}
			continue
		}
		var r *rel
		for i := range rels {
			if rels[i].covers >= m {
				r = &rels[i]
				break
			}
		}
		switch {
		case r == nil:
			out = append(out, item(p, contract.DeliveryStageReleasing, m))
		case !installed(r.version, versions):
			it := item(p, contract.DeliveryStageInstalling, r.published)
			it.Release = &r.tag
			out = append(out, it)
		case m >= day && m >= rels[0].covers:
			it := item(p, contract.DeliveryStageInstalled, r.published)
			it.Release = &r.tag
			out = append(out, it)
		}
	}
	return out, landing
}

// installed: every host runs version or newer (1.261112.31 compares part by part); no host known is not installed.
func installed(version string, hosts []string) bool {
	for _, h := range hosts {
		if versionLess(h, version) {
			return false
		}
	}
	return len(hosts) > 0
}

func versionLess(a, b string) bool {
	x, y := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(x) && i < len(y); i++ {
		m, _ := strconv.Atoi(x[i])
		n, _ := strconv.Atoi(y[i])
		if m != n {
			return m < n
		}
	}
	return len(x) < len(y)
}

// deliverySamples appends this read's counts to the day's sample document of each project and returns the last 24
// hours. Written without a change row: nobody watches samples live, the page reads them with the status.
func deliverySamples(ctx context.Context, a *App, counts map[string]map[string]int64, now time.Time) ([]contract.DeliverySample, error) {
	out := []contract.DeliverySample{}
	at, from := now.Format(time.RFC3339), now.Add(-24*time.Hour).Format(time.RFC3339)
	err := runTx(ctx, a, nil, true, "", func(q *Request) error {
		for p, c := range counts {
			id := "delivery-samples:" + p + ":" + now.Format("2006-01-02")
			if err := exec(q, `INSERT INTO document(kind,id,project_id,data) VALUES('delivery-samples',$1,$2,jsonb_build_object('samples',jsonb_build_array($3::jsonb)))
				ON CONFLICT (id) DO UPDATE SET data=jsonb_set(document.data,'{samples}',document.data->'samples' || $3::jsonb),updated_at=now()`,
				id, p, jsonBytes(contract.DeliverySample{At: at, ProjectId: p, Counts: c})); err != nil {
				return err
			}
		}
		if err := exec(q, "DELETE FROM document WHERE kind='delivery-samples' AND updated_at < now() - interval '7 days'"); err != nil {
			return err
		}
		rs, err := rows(q, "SELECT data FROM document WHERE kind='delivery-samples' AND updated_at >= now() - interval '25 hours'")
		for _, r := range rs {
			var d struct{ Samples []contract.DeliverySample }
			if json.Unmarshal(jsonBytes(r), &d) == nil {
				for _, s := range d.Samples {
					if s.At >= from {
						out = append(out, s)
					}
				}
			}
		}
		return err
	})
	sort.Slice(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out, err
}
