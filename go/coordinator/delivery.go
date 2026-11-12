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
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"
)

// deliveryState: the last pass, per repository (lower-case owner/name). A repository listed by several projects is read
// once; a caller sees it when it may see any of those projects.
type deliveryState struct {
	refreshedAt *string
	source      contract.DeliverySource
	repos       map[string]*deliveryRepo
	samples     []contract.DeliverySample
}

type deliveryRepo struct {
	projects []string
	items    []contract.DeliveryItem
	landing  int64
	problem  string
}

var (
	deliveryMu   sync.Mutex
	deliveryLast = deliveryState{source: contract.DeliverySourceNone}
	queueBranch  = regexp.MustCompile(`^gh-readonly-queue/[^/]+/pr-(\d+)-`)
	githubRemote = regexp.MustCompile(`github\.com[:/]([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(\.git)?/?$`)
)

func init() {
	onStart(func(ctx context.Context, a *App) {
		// Keyless the five reads each pass use 50 of the 60 an hour; with a key the period can be much shorter.
		// ponytail: the cache is in memory, so each start reads once more; keep the last pass in a row if deploys
		// exhaust the keyless budget.
		// every runs the first read at start: a deploy every few minutes must not blank the page.
		d := envMs("FLEET_DELIVERY_TICK_MS", 6*60*1000)
		every(ctx, "delivery", d, func(ctx context.Context) error { return deliveryPass(ctx, a) })
	})
	operation("delivery_status", true, typedOp(deliveryStatus))
}

func deliveryStatus(q *Request, _ *contract.DeliveryStatusRequest) (contract.DeliveryStatusReply, error) {
	deliveryMu.Lock()
	last := deliveryLast
	deliveryMu.Unlock()
	full := fullAccess(q.person)
	visible := func(projects ...string) bool {
		for _, p := range projects {
			if full || mayAccessProjectID(q.person, p) {
				return true
			}
		}
		return false
	}
	out := contract.DeliveryStatusReply{Source: last.source, RefreshedAt: last.refreshedAt, Items: []contract.DeliveryItem{}, Samples: []contract.DeliverySample{}}
	var problems []string
	for _, r := range last.repos {
		if visible(r.projects...) {
			out.Items = append(out.Items, r.items...)
			out.Landing += r.landing
			if r.problem != "" {
				problems = append(problems, r.problem)
			}
		}
	}
	for _, s := range last.samples {
		if r := last.repos[strings.ToLower(deref(s.Repo))]; r != nil && visible(r.projects...) || s.Repo == nil && visible(s.ProjectId) {
			out.Samples = append(out.Samples, s)
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		out.Error = ptr(strings.Join(problems, "; "))
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
	type target struct {
		projects    []string
		owner, repo string
	}
	var targets []string // keys of byKey in project order
	byKey := map[string]*target{}
	keyed := githubKeyed(ctx, a)
	var versions []string
	err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		ids, err := projectIDs(q)
		if err != nil {
			return err
		}
		conn, err := maybe(q, "connection:github", "api-connection")
		if err != nil {
			return err
		}
		for _, id := range ids {
			// A keyed read is shown only to projects the github connection is granted to: a project that merely lists a
			// private repository must not see what another project's grant read.
			if keyed && (conn == nil || !connAllowed(conn, doc{"projectId": id})) {
				continue
			}
			for _, r := range anys(projectSettingsOf(q, id)["repos"]) {
				m := githubRemote.FindStringSubmatch(astr(mapOf(r)["remote"]))
				if m == nil || !keyed && !strings.EqualFold(m[1]+"/"+m[2], githubPublicRepo) {
					continue
				}
				key := strings.ToLower(m[1] + "/" + m[2])
				if t := byKey[key]; t != nil {
					if !slices.Contains(t.projects, id) {
						t.projects = append(t.projects, id)
					}
					continue
				}
				byKey[key] = &target{[]string{id}, m[1], m[2]}
				targets = append(targets, key)
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
	deliveryMu.Lock()
	prev := deliveryLast
	deliveryMu.Unlock()
	repos, counts, failed := map[string]*deliveryRepo{}, map[string]map[string]int64{}, 0
	for _, key := range targets {
		t := byKey[key]
		var g ghRepo
		var active, queued struct {
			Runs []ghRun `json:"workflow_runs"`
		}
		var err error
		for _, r := range []struct {
			tool string
			into any
		}{{"pulls_open", &g.open}, {"pulls_closed", &g.closed}, {"runs_in_progress", &active}, {"runs_queued", &queued}, {"releases", &g.releases}} {
			var body []byte
			// The read is made for the first project listing the repository (its token's access check).
			if body, _, err = githubRead(ctx, a, t.projects[0], r.tool, map[string]string{"owner": t.owner, "repo": t.repo}); err == nil {
				err = json.Unmarshal(body, r.into)
			}
			if err != nil {
				break // the rest would be thrown away, and a rate limit only grows with more reads
			}
		}
		name := t.owner + "/" + t.repo
		if err != nil {
			failed++
			// The last good items of this repository stand; it records no sample this pass (a gap, not a dip).
			r := &deliveryRepo{projects: t.projects, problem: name + ": " + err.Error()}
			if p := prev.repos[key]; p != nil {
				r.items, r.landing = p.items, p.landing
			}
			repos[key] = r
			continue
		}
		g.runs = append(active.Runs, queued.Runs...)
		its, l := deliveryStages(t.projects[0], name, g, versions, now)
		repos[key] = &deliveryRepo{projects: t.projects, items: its, landing: l}
		c := map[string]int64{}
		for _, s := range contract.DeliveryStageValues {
			c[string(s)] = 0
		}
		for _, it := range its {
			c[string(it.Stage)]++
		}
		counts[t.projects[0]+" "+name] = c
	}
	samples, err := deliverySamples(ctx, a, counts, now)
	if err != nil {
		log.Printf("delivery samples: %v", err)
	}
	next := deliveryState{source: contract.DeliverySourcePublic, repos: repos, samples: samples, refreshedAt: ptr(now.Format(time.RFC3339))}
	if keyed {
		next.source = contract.DeliverySourceKey
	}
	if failed == len(targets) && len(targets) > 0 && prev.refreshedAt != nil {
		next.refreshedAt = prev.refreshedAt // nothing new was read: the age shows how old the items are
	}
	deliveryMu.Lock()
	deliveryLast = next
	deliveryMu.Unlock()
	return nil
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
	// time of its target commit; a release whose target is not among the merged pull requests read is left out.
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
			continue // the target's merge time is unknown: guessing it could claim merges the release lacks
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

// deliverySamples appends this read's counts to the day's sample document of each repository and returns the last 24
// hours. Written without a change row: nobody watches samples live, the page reads them with the status.
func deliverySamples(ctx context.Context, a *App, counts map[string]map[string]int64, now time.Time) ([]contract.DeliverySample, error) {
	out := []contract.DeliverySample{}
	at, from := now.Format(time.RFC3339), now.Add(-24*time.Hour).Format(time.RFC3339)
	err := runTx(ctx, a, nil, true, "", func(q *Request) error {
		for k, c := range counts { // per "<first project> <owner/repo>": one sample per repository
			p, repo, _ := strings.Cut(k, " ")
			id := "delivery-samples:" + p + ":" + repo + ":" + now.Format("2006-01-02")
			if err := exec(q, `INSERT INTO document(kind,id,project_id,data) VALUES('delivery-samples',$1,$2,jsonb_build_object('samples',jsonb_build_array($3::jsonb)))
				ON CONFLICT (id) DO UPDATE SET data=jsonb_set(document.data,'{samples}',document.data->'samples' || $3::jsonb),updated_at=now()`,
				id, p, jsonBytes(contract.DeliverySample{At: at, ProjectId: p, Repo: &repo, Counts: c})); err != nil {
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
