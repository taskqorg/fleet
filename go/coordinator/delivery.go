// Delivery status (docs/design-system/pages/delivery.md): every connected GitHub repository's pull requests by delivery
// stage, read server side every few minutes and cached here, so browsers never call GitHub. The stages follow
// scripts/delivery-latency.sh and scripts/watch-landings.sh: PR CI (pull_request runs), merge queue (merge_group runs on
// gh-readonly-queue/<base>/pr-<n>-<sha>), merged, release (the first v* or, in the fleet repository, UI-only release
// whose target commit contains the merge; main is linear behind the merge queue, so "contains" is "merged no later than
// the release's target"), installed on hosts (every host seen in the last hour runs that version or newer). A UI-only
// release (ui-<backend>-v<UI version>, release.yml) ships the web app alone: hosts install nothing, so it is installed
// when the site serves that UI or newer.
// Reads go through the gateway's github connection when it exists (the owner adds a read-only key: scripts/key-add.sh github); without it
// the coordinator reads the fleet repository unauthenticated (60 requests an hour per address), which the reply says.
// Each read also stores the per-stage counts, so the chart is the recorded history of the last 24 hours.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"log"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
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
	projects  []string
	items     []contract.DeliveryItem
	landing   int64
	problem   string
	delivered []deliveryMergedPR
}

// deliveryMergedPR records a merged pull request snapshot and whether its delivery/deployment has completed.
type deliveryMergedPR struct {
	Repo        string `json:"repo"`
	Number      int64  `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"-"`
	URL         string `json:"url"`
	Branch      string `json:"branch,omitempty"`
	HeadSHA     string `json:"headSha,omitempty"`
	MergeSHA    string `json:"mergeSha,omitempty"`
	MergedAt    string `json:"mergedAt"`
	Stage       string `json:"stage"`
	Release     string `json:"release,omitempty"`
	DeliveredAt string `json:"deliveredAt,omitempty"`
	Delivered   bool   `json:"-"`
}

var (
	deliveryKeylessAt time.Time // when keyless repositories were last read: they keep FLEET_DELIVERY_KEYLESS_MS apart
	deliveryMu        sync.Mutex
	deliveryLast      = deliveryState{source: contract.DeliverySourceNone}
	queueBranch       = regexp.MustCompile(`^gh-readonly-queue/[^/]+/pr-(\d+)-`)
	githubRemote      = regexp.MustCompile(`github\.com[:/]([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(\.git)?/?$`)
)

func init() {
	onStart(func(ctx context.Context, a *App) {
		// Keyless the five reads each pass use 50 of the 60 an hour; with a key the period can be much shorter.
		// ponytail: the cache is in memory, so each start reads once more; keep the last pass in a row if deploys
		// exhaust the keyless budget.
		// With a key a pass runs every minute (GitHub allows 5000 reads an hour); keyless ones keep FLEET_DELIVERY_KEYLESS_MS apart.
		d := envMs("FLEET_DELIVERY_TICK_MS", 60*1000)
		if d > 0 { // the first read at start: a deploy every few minutes must not blank the page
			untilDone(ctx, "delivery-first", d, func(ctx context.Context) error { deliveryPass(ctx, a); return nil })
		}
		every(ctx, "delivery", d, func(ctx context.Context) error { return deliveryPass(ctx, a) })
	})
	op("delivery_status", deliveryStatus)
}

func deliveryStatus(q *Request, r *contract.DeliveryStatusRequest) (contract.DeliveryStatusReply, error) {
	if err := excludeCheck(r.ExcludeProjectIds); err != nil {
		return contract.DeliveryStatusReply{}, err
	}
	deliveryMu.Lock()
	last := deliveryLast
	deliveryMu.Unlock()
	full := fullAccess(q.person)
	in := func(p string) bool {
		has := func(ids []string) bool { return slices.Contains(ids, p) || p == "" && slices.Contains(ids, "-") }
		return (len(r.ProjectIds) == 0 || has(r.ProjectIds)) && !has(r.ExcludeProjectIds)
	}
	// The focus works like access: a repository shows when a focused project that is not left out (excludeProjectIds, the
	// person's ignored projects) lists it; items carry only its first project.
	out := deliveryReply(last, func(p string) bool { return (full || mayAccessProjectID(q.person, p)) && in(p) })
	return out, linkDeliveryItems(q, out.Items)
}

// deliveryReply: the part of the last pass a caller may see. A repository (its items, landing count, error and
// samples) is visible when the caller may see a project listing it; a sample recorded before samples carried their
// repository, when the caller may see its project. An item or sample names the repository's first project; when the
// caller may not see that one (no access, outside the focus, ignored), it names the first listing project the caller sees.
func deliveryReply(last deliveryState, may func(project string) bool) contract.DeliveryStatusReply {
	shown := func(r *deliveryRepo) (string, bool) {
		if r == nil {
			return "", false
		}
		i := slices.IndexFunc(r.projects, may)
		if i < 0 {
			return "", false
		}
		return r.projects[i], true
	}
	out := contract.DeliveryStatusReply{Source: last.source, RefreshedAt: last.refreshedAt, Items: []contract.DeliveryItem{}, Samples: []contract.DeliverySample{}}
	var problems []string
	for _, r := range last.repos {
		if p, ok := shown(r); ok {
			for _, it := range r.items {
				if !may(it.ProjectId) {
					it.ProjectId = p
				}
				out.Items = append(out.Items, it)
			}
			out.Landing += r.landing
			if r.problem != "" {
				problems = append(problems, r.problem)
			}
		}
	}
	for _, s := range last.samples {
		if p, ok := shown(last.repos[strings.ToLower(deref(s.Repo))]); ok {
			if !may(s.ProjectId) {
				s.ProjectId = p
			}
			out.Samples = append(out.Samples, s)
		} else if s.Repo == nil && may(s.ProjectId) {
			out.Samples = append(out.Samples, s)
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		out.Error = ptr(strings.Join(problems, "; "))
	}
	return out
}

// ghPull, ghRun and ghRelease: the fields of the GitHub REST replies the stages need.
type ghPullHead struct {
	SHA string `json:"sha"`
	Ref string `json:"ref"`
}
type ghPull struct {
	Number    int64      `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	URL       string     `json:"html_url"`
	Draft     bool       `json:"draft"`
	CreatedAt string     `json:"created_at"`
	MergedAt  *string    `json:"merged_at"`
	MergeSHA  string     `json:"merge_commit_sha"`
	AutoMerge any        `json:"auto_merge"`
	Head      ghPullHead `json:"head"`
	Base      ghPullHead `json:"base"`
}

func ghPullAutoMergeMethod(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	var m struct {
		MergeMethod string `json:"merge_method"`
	}
	if json.Unmarshal(b, &m) == nil {
		return strings.ToLower(m.MergeMethod)
	}
	return ""
}

type ghRun struct {
	Event      string `json:"event"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HeadSHA    string `json:"head_sha"`
	Branch     string `json:"head_branch"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	Workflow   int64  `json:"workflow_id"`
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
	finished     []ghRun // the latest finished pull_request runs (keyed reads only)
	queueDone    []ghRun // the latest finished merge_group runs (keyed reads only)
	releases     []ghRelease
}

func deliveryPass(ctx context.Context, a *App) error {
	// The UI the site serves, read once per pass like the host versions; noted first, so a failed read below still keeps
	// the minute's sighting (a UI-only release's UI on site).
	ui := uiServed()
	noteUISeen(ui, time.Now())
	type target struct {
		projects    []string
		provider    string
		host        string
		owner, repo string
		slug        string
		repoConn    string
	}
	var targets []string // keys of byKey in project order
	byKey := map[string]*target{}
	keyedRepo := map[string]bool{}
	keyed := false // any repository read with a key
	var versions []string
	var recordedUI []ghRelease
	err := a.store.Read(ctx, nil, func(q *Request) error {
		ids, err := projectIDs(q)
		if err != nil {
			return err
		}
		for _, id := range ids {
			for _, spec := range projectRepoSpecs(q, id) {
				if ref, rok := contract.ParseRepoRemote(spec.Remote, spec.Forge, spec.Host); rok && ref.Provider == "gitlab" {
					owner := strings.Split(ref.Slug, "/")[0]
					_, conn, k, cerr := gitlabConnectionOf(q, ref.Host, owner, spec.Connection)
					if cerr != nil {
						return cerr
					}
					if !k {
						continue
					}
					if grant, ok := connGrantOf(conn); !(ok && connAllowedFor(grant, mintRun{ProjectID: &id})) {
						continue
					}
					keyed = true
					key := "gitlab:" + strings.ToLower(ref.Host+"/"+ref.Slug)
					keyedRepo[key] = true
					if t := byKey[key]; t != nil {
						if !slices.Contains(t.projects, id) {
							t.projects = append(t.projects, id)
						}
						continue
					}
					byKey[key] = &target{projects: []string{id}, provider: "gitlab", host: ref.Host, owner: owner, repo: ref.Slug, slug: ref.Slug, repoConn: spec.Connection}
					targets = append(targets, key)
					continue
				}
				m := githubRemote.FindStringSubmatch(spec.Remote)
				if m == nil {
					continue
				}
				_, conn, k, err := githubConnectionOf(q, m[1])
				if err != nil {
					return err
				}
				// A keyed read is shown only to projects its connection is granted to: a project that merely lists a
				// private repository must not see what another project's grant read. Keyless, only the public repository.
				if grant, ok := connGrantOf(conn); k && !(ok && connAllowedFor(grant, mintRun{ProjectID: &id})) || !k && !strings.EqualFold(m[1]+"/"+m[2], githubPublicRepo) {
					continue
				}
				keyed = keyed || k
				key := strings.ToLower(m[1] + "/" + m[2])
				keyedRepo[key] = keyedRepo[key] || k
				if t := byKey[key]; t != nil {
					if !slices.Contains(t.projects, id) {
						t.projects = append(t.projects, id)
					}
					continue
				}
				byKey[key] = &target{projects: []string{id}, provider: "github", host: "github.com", owner: m[1], repo: m[2], slug: m[1] + "/" + m[2]}
				targets = append(targets, key)
			}
		}
		versions, err = liveHostVersions(q)
		return err
	})
	if err != nil {
		return err
	}
	// The UI-only releases the delivery history recorded (it reads 100 releases a pass): GitHub's release list is not by
	// date, and a ui-* tag sorts among older native ones, past the 30 the releases read returns (#626). Extra evidence
	// only, in its own transaction: a failed read leaves the pass as it was.
	if byKey[strings.ToLower(githubPublicRepo)] != nil {
		if rerr := a.store.Read(ctx, nil, func(q *Request) error {
			recordedUI = nil // a retried transaction starts over
			rs, err := q.db().DeliveryUIReleasePayloads(q.ctx())
			for _, r := range rs {
				var x contract.DeliveryRelease
				if json.Unmarshal(r, &x) == nil && uiOnlyRelease(x.Tag) {
					recordedUI = append(recordedUI, ghRelease{Tag: x.Tag, Target: x.Target, Published: x.Published})
				}
			}
			return err
		}); rerr != nil {
			recordedUI = nil
			log.Printf("delivery: recorded UI releases unread: %v", rerr)
		}
	}
	now := time.Now().UTC()
	deliveryMu.Lock()
	prev := deliveryLast
	// Keyless reads share GitHub's 60 an hour per address: a keyless repository is read at most every 6 minutes even
	// while keyed ones are read every minute; between its reads it keeps its last items.
	readKeyless := deliveryKeylessDue(now, deliveryKeylessAt)
	if readKeyless {
		deliveryKeylessAt = now
	}
	deliveryMu.Unlock()
	repos, counts, failed := map[string]*deliveryRepo{}, map[string]map[string]int64{}, 0
	for _, key := range targets {
		t := byKey[key]
		if t.provider == "gitlab" {
			var gl glRepo
			var err error
			projParam := url.PathEscape(t.slug)
			for _, r := range []struct {
				tool string
				into any
			}{
				{"mrs_open", &gl.open},
				{"mrs_merged", &gl.merged},
				{"pipelines_running", &gl.running},
				{"pipelines_pending", &gl.pending},
				{"pipelines_failed", &gl.failed},
				{"releases", &gl.releases},
			} {
				var body []byte
				if body, err = gitlabRead(ctx, a, t.projects[0], t.host, t.owner, t.repoConn, r.tool, map[string]string{"project_id": projParam}); err == nil {
					err = json.Unmarshal(body, r.into)
				}
				if err != nil {
					break
				}
			}
			if err != nil {
				failed++
				r := &deliveryRepo{projects: t.projects, problem: t.slug + ": " + err.Error()}
				if p := prev.repos[key]; p != nil {
					r.items, r.landing, r.delivered = p.items, p.landing, p.delivered
				}
				repos[key] = r
				continue
			}
			its, merged := gitlabDeliveryStages(t.projects[0], t.host, t.slug, gl)
			repos[key] = &deliveryRepo{projects: t.projects, items: its, delivered: merged}
			c := map[string]int64{}
			for _, s := range contract.DeliveryStageValues {
				c[string(s)] = 0
			}
			for _, it := range its {
				c[string(it.Stage)]++
			}
			counts[t.projects[0]+" "+t.slug] = c
			continue
		}
		if !keyedRepo[key] && !readKeyless {
			if p := prev.repos[key]; p != nil {
				repos[key] = &deliveryRepo{projects: t.projects, items: p.items, landing: p.landing, problem: p.problem, delivered: p.delivered}
				continue
			}
		}
		var g ghRepo
		var active, queued, finished, queueDone struct {
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
				r.items, r.landing, r.delivered = p.items, p.landing, p.delivered
			}
			repos[key] = r
			continue
		}
		// Failed checks: the latest finished pull_request runs, through the key only (never the keyless budget) and not
		// required: a connection that does not grant runs_completed_pr leaves failed checks unseen, not the repository.
		if keyedRepo[key] {
			if body, ferr := githubReadKeyed(ctx, a, t.projects[0], "runs_completed_pr", map[string]string{"owner": t.owner, "repo": t.repo}); ferr == nil {
				if uerr := json.Unmarshal(body, &finished); uerr != nil {
					log.Printf("delivery %s: failed checks unreadable: %v", name, uerr)
				}
			} else {
				log.Printf("delivery %s: failed checks unread: %v", name, ferr)
			}
			// Removed from the merge queue: the latest finished merge_group runs, the same way (runs_completed_mq).
			if body, ferr := githubReadKeyed(ctx, a, t.projects[0], "runs_completed_mq", map[string]string{"owner": t.owner, "repo": t.repo}); ferr == nil {
				if uerr := json.Unmarshal(body, &queueDone); uerr != nil {
					log.Printf("delivery %s: failed queue checks unreadable: %v", name, uerr)
				}
			} else {
				log.Printf("delivery %s: failed queue checks unread: %v", name, ferr)
			}
		}
		g.runs, g.finished, g.queueDone = append(active.Runs, queued.Runs...), finished.Runs, queueDone.Runs
		if strings.EqualFold(name, githubPublicRepo) {
			g.releases = withReleases(g.releases, recordedUI)
			if err := harnessPinsPass(ctx, a, g.open); err != nil {
				log.Printf("harness pins question: %v", err)
			}
		}
		its, l, merged := deliveryStagesWithMerged(t.projects[0], name, g, versions, ui, now)
		repos[key] = &deliveryRepo{projects: t.projects, items: its, landing: l, delivered: merged}
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
	// Watchers learn of a change through the change stream: one small delivery_state row per project whose rows changed,
	// so a pass that changes nothing writes nothing (fleet_change maps the row to the change kind delivery).
	changed := map[string]bool{}
	same := func(p, r *deliveryRepo) bool {
		return p.problem == r.problem && p.landing == r.landing && string(jsonBytes(p.items)) == string(jsonBytes(r.items))
	}
	for key, r := range repos {
		if p := prev.repos[key]; p == nil || !same(p, r) {
			for _, id := range r.projects {
				changed[id] = true
			}
		}
	}
	for key, p := range prev.repos { // a repository no longer read (grant revoked, removed from the project): its rows go
		if repos[key] == nil {
			for _, id := range p.projects {
				changed[id] = true
			}
		}
	}
	if len(changed) > 0 {
		if err := a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
			for id := range changed {
				if err := q.db().DeliveryStatePut(q.ctx(), db.DeliveryStatePutParams{ID: "delivery-state:" + id, ProjectID: id, RefreshedAt: next.refreshedAt}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			log.Printf("delivery state: %v", err)
		}
	}
	var delivered []deliveryMergedPR
	for _, r := range repos {
		for _, pr := range r.delivered {
			if pr.Delivered {
				delivered = append(delivered, pr)
			}
		}
	}
	if len(delivered) > 0 {
		if err := reconcileDeliveredTasks(ctx, a, delivered); err != nil {
			log.Printf("delivery task reconcile: %v", err)
		}
	}
	return nil
}

// snapshotDeliveredPRs returns the latest cached delivered pull requests without reading GitHub.
func snapshotDeliveredPRs() []deliveryMergedPR {
	deliveryMu.Lock()
	defer deliveryMu.Unlock()
	var out []deliveryMergedPR
	for _, r := range deliveryLast.repos {
		for _, pr := range r.delivered {
			if pr.Delivered {
				out = append(out, pr)
			}
		}
	}
	return out
}

// deliveryStages: each pull request's current stage. landing counts the pull requests a worker waits on (auto-merge on
// or in the merge queue). versions: the hosts' executor versions; ui: the UI version the site serves (uiServed).
func deliveryStages(project, repo string, g ghRepo, versions []string, ui string, now time.Time) ([]contract.DeliveryItem, int64) {
	its, l, _ := deliveryStagesWithMerged(project, repo, g, versions, ui, now)
	return its, l
}

func deliveryStagesWithMerged(project, repo string, g ghRepo, versions []string, ui string, now time.Time) ([]contract.DeliveryItem, int64, []deliveryMergedPR) {
	item := func(p ghPull, s contract.DeliveryStage, since string) contract.DeliveryItem {
		prov := string(contract.PublishingProviderGithub)
		kind := string(contract.PublishingKindPullRequest)
		host := "github.com"
		auto := p.AutoMerge != nil
		it := contract.DeliveryItem{
			ProjectId:        project,
			Repo:             repo,
			Number:           p.Number,
			Title:            p.Title,
			Url:              p.URL,
			Stage:            s,
			Since:            since,
			Provider:         &prov,
			Host:             &host,
			Kind:             &kind,
			Branch:           optStr(p.Head.Ref),
			BaseBranch:       optStr(p.Base.Ref),
			HeadSha:          optStr(p.Head.SHA),
			IsDraft:          ptr(p.Draft),
			AutoMergeEnabled: ptr(auto),
		}
		if p.AutoMerge != nil {
			if mm := ghPullAutoMergeMethod(p.AutoMerge); mm != "" {
				it.MergeMethod = optStr(mm)
			}
		}
		if p.CreatedAt != "" {
			it.Opened = &p.CreatedAt
		}
		it.Merged = p.MergedAt
		return it
	}
	// The latest finished pull_request run per head commit and workflow: a head whose latest run of any workflow failed
	// has failed checks (one workflow passing later does not hide another's failure); since is the latest such failure.
	type headRun struct {
		sha      string
		workflow int64
	}
	latest := map[headRun]ghRun{}
	for _, r := range g.finished {
		k := headRun{r.HeadSHA, r.Workflow}
		if o, ok := latest[k]; r.Event == "pull_request" && (!ok || r.UpdatedAt > o.UpdatedAt) {
			latest[k] = r
		}
	}
	failedAt, headFrom, checkedAt := map[string]string{}, map[string]string{}, map[string]string{}
	for k, r := range latest {
		if (r.Conclusion == "failure" || r.Conclusion == "timed_out") && r.UpdatedAt > failedAt[k.sha] {
			failedAt[k.sha] = r.UpdatedAt
		}
		checkedAt[k.sha] = max(checkedAt[k.sha], r.UpdatedAt)
	}
	// When a head was first checked: a head first checked after a queue failure is a new commit, the fix.
	for _, r := range g.finished {
		if r.Event == "pull_request" && r.CreatedAt != "" && (headFrom[r.HeadSHA] == "" || r.CreatedAt < headFrom[r.HeadSHA]) {
			headFrom[r.HeadSHA] = r.CreatedAt
		}
	}
	// The latest finished merge_group run per pull request (queue branch pr-<n>-) and workflow: a failure there removed it
	// from the queue, and another workflow of the group passing later does not hide it. Cancelled runs are left out: the
	// queue cancels a group when an entry ahead fails and starts it again. Both reads hold the latest 100 runs, so a
	// failure older than those is no longer seen.
	type queueRun struct {
		pr       int64
		workflow int64
	}
	queueLatest := map[queueRun]ghRun{}
	for _, r := range g.queueDone {
		m := queueBranch.FindStringSubmatch(r.Branch)
		if r.Event != "merge_group" || m == nil || r.Conclusion == "cancelled" || r.Conclusion == "skipped" {
			continue
		}
		n, _ := strconv.ParseInt(m[1], 10, 64)
		if k := (queueRun{n, r.Workflow}); queueLatest[k].UpdatedAt < r.UpdatedAt {
			queueLatest[k] = r
		}
	}
	// queueFailedAt: when the latest failure ended (the stage's since); queueFailedFrom: when that group started, so a
	// failure of a group started before a new head was pushed belongs to the old head.
	queueFailedAt, queueFailedFrom := map[int64]string{}, map[int64]string{}
	for k, r := range queueLatest {
		if (r.Conclusion == "failure" || r.Conclusion == "timed_out") && r.UpdatedAt > queueFailedAt[k.pr] {
			queueFailedAt[k.pr], queueFailedFrom[k.pr] = r.UpdatedAt, cmp.Or(r.CreatedAt, r.UpdatedAt)
		}
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
		} else if at, ok := failedAt[p.Head.SHA]; ok {
			out = append(out, item(p, contract.DeliveryStagePrFailed, at))
		} else if at, ok := queueFailedAt[p.Number]; ok && p.AutoMerge == nil && headFrom[p.Head.SHA] < queueFailedFrom[p.Number] {
			// Removed from the queue and not set to enter it again, and its head is no newer: it waits on a person.
			out = append(out, item(p, contract.DeliveryStageMqFailed, at))
		} else if p.AutoMerge != nil {
			// Waiting to enter the queue since its head's checks finished or its last queue failure, as far as the reads
			// know (else since it opened): an early alarm on a long wait, never one a comment resets.
			out = append(out, item(p, contract.DeliveryStageMqWait, max(p.CreatedAt, checkedAt[p.Head.SHA], queueFailedAt[p.Number])))
		} else {
			out = append(out, item(p, contract.DeliveryStagePrOpen, p.CreatedAt))
		}
	}
	// Releases: v<version> and, in the fleet repository whose site serves the UI, ui-<backend>-v<UI version> tags
	// (release.yml cuts a UI-only release instead of a v* one when only the web app changed since the last release; both
	// share one version sequence), oldest first, each with the merge time of its target commit; a release whose target is
	// not among the merged pull requests read is left out.
	fleetRepo := strings.EqualFold(repo, githubPublicRepo)
	mergedAt := map[string]string{}
	for _, p := range g.closed {
		if p.MergedAt != nil {
			mergedAt[p.MergeSHA] = *p.MergedAt
		}
	}
	type rel struct {
		tag, version, covers, published string
		uiOnly                          bool
	}
	var rels []rel
	for _, r := range g.releases {
		uiOnly := fleetRepo && uiOnlyRelease(r.Tag)
		if r.Draft || r.Published == "" || !uiOnly && !strings.HasPrefix(r.Tag, "v") {
			continue
		}
		c := mergedAt[r.Target]
		if c == "" {
			continue // the target's merge time is unknown: guessing it could claim merges the release lacks
		}
		rels = append(rels, rel{r.Tag, releaseVersionOf(r.Tag), c, r.Published, uiOnly})
	}
	sort.Slice(rels, func(i, j int) bool { return rels[i].published < rels[j].published })
	day := now.Add(-24 * time.Hour).Format(time.RFC3339)
	var merged []deliveryMergedPR
	for _, p := range g.closed {
		if p.MergedAt == nil {
			continue
		}
		m := *p.MergedAt
		dm := deliveryMergedPR{
			Repo:     repo,
			Number:   p.Number,
			Title:    p.Title,
			Body:     p.Body,
			URL:      p.URL,
			Branch:   p.Head.Ref,
			HeadSHA:  p.Head.SHA,
			MergeSHA: p.MergeSHA,
			MergedAt: m,
		}
		if len(rels) == 0 {
			dm.Stage = string(contract.DeliveryStageMerged)
			dm.DeliveredAt = m
			dm.Delivered = !fleetRepo
			merged = append(merged, dm)
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
			dm.Stage = string(contract.DeliveryStageReleasing)
		case r.uiOnly && versionLess(ui, r.version), !r.uiOnly && !installed(r.version, versions):
			dm.Stage = string(contract.DeliveryStageInstalling)
			dm.Release = r.tag
			if !fleetRepo {
				dm.Delivered = true
				dm.DeliveredAt = r.published
			}
			it := item(p, contract.DeliveryStageInstalling, r.published)
			it.Release = &r.tag
			out = append(out, it)
		default:
			dm.Stage = string(contract.DeliveryStageInstalled)
			dm.Release = r.tag
			dm.DeliveredAt = r.published
			dm.Delivered = true
			if m >= day && m >= rels[0].covers {
				it := item(p, contract.DeliveryStageInstalled, r.published)
				it.Release = &r.tag
				out = append(out, it)
			}
		}
		if dm.Stage == string(contract.DeliveryStageReleasing) {
			out = append(out, item(p, contract.DeliveryStageReleasing, m))
		}
		merged = append(merged, dm)
	}
	return out, landing, merged
}

// withReleases: read plus the recorded releases it lacks (by tag), only those published since the oldest one read: an
// older one would reach back past the native releases read and claim merges a release not read delivered.
func withReleases(read, recorded []ghRelease) []ghRelease {
	out := slices.Clone(read)
	oldest := ""
	for _, r := range read {
		if r.Published != "" && (oldest == "" || r.Published < oldest) {
			oldest = r.Published
		}
	}
	for _, r := range recorded {
		if oldest != "" && r.Published >= oldest && !slices.ContainsFunc(out, func(x ghRelease) bool { return x.Tag == r.Tag }) {
			out = append(out, r)
		}
	}
	return out
}

// liveHostVersions: the executor version of every host seen within the hour that reports one, the hosts a release
// must reach to be installed (deliveryPass, the release card).
func liveHostVersions(q *Request) ([]string, error) {
	obs, err := deliveryHostsSeen(q)
	versions := []string{}
	for _, o := range obs {
		if seen := q.app.effectiveHostSeen(o.ID, o.LastSeenMs); nowMs()-int64(seen) < 3600_000 && o.Version != "" {
			versions = append(versions, o.Version)
		}
	}
	return versions, err
}

// projectRepoSpecs: a project's configured repositories with forge/host metadata.
func projectRepoSpecs(q *Request, id string) []projectRepo {
	var settings struct {
		Repos []projectRepo `json:"repos"`
	}
	jsonRoundTrip(projectSettingsOf(q, id), &settings)
	return settings.Repos
}

// projectRemotes: the git remotes of a project's repositories (its settings' repos); one that is not a string is "".
func projectRemotes(q *Request, id string) []string {
	var settings struct {
		Repos []struct {
			Remote json.RawMessage `json:"remote"`
		} `json:"repos"`
	}
	jsonRoundTrip(projectSettingsOf(q, id), &settings)
	out := []string{}
	for _, r := range settings.Repos {
		var remote string
		json.Unmarshal(r.Remote, &remote)
		out = append(out, remote)
	}
	return out
}

type glMRPipeline struct {
	Status    string `json:"status"`
	SHA       string `json:"sha"`
	UpdatedAt string `json:"updated_at"`
}
type glMR struct {
	IID                       int64         `json:"iid"`
	Title                     string        `json:"title"`
	Description               string        `json:"description"`
	WebURL                    string        `json:"web_url"`
	Draft                     bool          `json:"draft"`
	WorkInProgress            bool          `json:"work_in_progress"`
	CreatedAt                 string        `json:"created_at"`
	UpdatedAt                 string        `json:"updated_at"`
	MergedAt                  *string       `json:"merged_at"`
	MergeCommitSHA            string        `json:"merge_commit_sha"`
	SHA                       string        `json:"sha"`
	SourceBranch              string        `json:"source_branch"`
	TargetBranch              string        `json:"target_branch"`
	MergeWhenPipelineSucceeds bool          `json:"merge_when_pipeline_succeeds"`
	HeadPipeline              *glMRPipeline `json:"head_pipeline"`
}
type glPipeline struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"`
	SHA       string `json:"sha"`
	Ref       string `json:"ref"`
	UpdatedAt string `json:"updated_at"`
}
type glRelease struct {
	TagName    string `json:"tag_name"`
	ReleasedAt string `json:"released_at"`
}
type glRepo struct {
	open, merged             []glMR
	running, pending, failed []glPipeline
	releases                 []glRelease
}

func gitlabDeliveryStages(project, host, repo string, g glRepo) ([]contract.DeliveryItem, []deliveryMergedPR) {
	items := []contract.DeliveryItem{}
	delivered := []deliveryMergedPR{}
	prov := string(contract.PublishingProviderGitlab)
	kind := string(contract.PublishingKindMergeRequest)
	mkItem := func(m glMR, st contract.DeliveryStage, since string) contract.DeliveryItem {
		isDraft := m.Draft || m.WorkInProgress
		it := contract.DeliveryItem{
			ProjectId:        project,
			Repo:             repo,
			Number:           m.IID,
			Title:            m.Title,
			Url:              m.WebURL,
			Stage:            st,
			Since:            since,
			Provider:         &prov,
			Host:             &host,
			Kind:             &kind,
			Branch:           optStr(m.SourceBranch),
			BaseBranch:       optStr(m.TargetBranch),
			HeadSha:          optStr(m.SHA),
			IsDraft:          ptr(isDraft),
			AutoMergeEnabled: ptr(m.MergeWhenPipelineSucceeds),
		}
		if m.CreatedAt != "" {
			it.Opened = &m.CreatedAt
		}
		it.Merged = m.MergedAt
		return it
	}
	pipeBySHA := map[string]glPipeline{}
	for _, list := range [][]glPipeline{g.failed, g.pending, g.running} {
		for _, p := range list {
			if prev, ok := pipeBySHA[p.SHA]; !ok || p.UpdatedAt > prev.UpdatedAt {
				pipeBySHA[p.SHA] = p
			}
		}
	}
	for _, m := range g.open {
		st := contract.DeliveryStagePrOpen
		since := m.CreatedAt
		if since == "" {
			since = m.UpdatedAt
		}
		pStatus := ""
		pSince := ""
		if p, ok := pipeBySHA[m.SHA]; ok {
			pStatus, pSince = p.Status, p.UpdatedAt
		} else if m.HeadPipeline != nil {
			pStatus, pSince = m.HeadPipeline.Status, m.HeadPipeline.UpdatedAt
		}
		switch pStatus {
		case "running", "pending":
			if m.MergeWhenPipelineSucceeds {
				st = contract.DeliveryStageMqCi
			} else {
				st = contract.DeliveryStagePrCi
			}
			if pSince != "" {
				since = pSince
			}
		case "failed", "canceled":
			st = contract.DeliveryStagePrFailed
			if pSince != "" {
				since = pSince
			}
		default:
			if m.MergeWhenPipelineSucceeds {
				st = contract.DeliveryStageMqWait
			}
		}
		items = append(items, mkItem(m, st, since))
	}
	for _, m := range g.merged {
		mergedAt := deref(m.MergedAt)
		if mergedAt == "" {
			continue
		}
		items = append(items, mkItem(m, contract.DeliveryStageMerged, mergedAt))
		delivered = append(delivered, deliveryMergedPR{
			Repo:        repo,
			Number:      m.IID,
			Title:       m.Title,
			Body:        m.Description,
			URL:         m.WebURL,
			Branch:      m.SourceBranch,
			HeadSHA:     m.SHA,
			MergeSHA:    m.MergeCommitSHA,
			MergedAt:    mergedAt,
			Stage:       string(contract.DeliveryStageMerged),
			DeliveredAt: mergedAt,
			Delivered:   true,
		})
	}
	return items, delivered
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

// deliverySamples records this read's counts of each repository (one delivery_sample row each) and returns the last 24
// hours. Written without a change row: nobody watches samples live, the page reads them with the status.
func deliverySamples(ctx context.Context, a *App, counts map[string]map[string]int64, now time.Time) ([]contract.DeliverySample, error) {
	out := []contract.DeliverySample{}
	err := a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
		for k, c := range counts { // per "<first project> <owner/repo>": one sample per repository
			p, repo, _ := strings.Cut(k, " ")
			if err := q.db().DeliverySampleAdd(q.ctx(), db.DeliverySampleAddParams{ProjectID: p, Repo: &repo, At: now, Payload: jsonBytes(c)}); err != nil {
				return err
			}
		}
		if err := q.db().DeliverySamplePrune(q.ctx()); err != nil {
			return err
		}
		rs, err := q.db().DeliverySamples(q.ctx(), now.Add(-24*time.Hour))
		for _, r := range rs {
			var counts map[string]int64
			if json.Unmarshal(r.Payload, &counts) != nil {
				continue
			}
			s := contract.DeliverySample{At: r.At.UTC().Format(time.RFC3339), ProjectId: r.ProjectID, Counts: counts}
			if r.Repo != nil && *r.Repo != "" {
				s.Repo = r.Repo
			}
			out = append(out, s)
		}
		return err
	})
	sort.Slice(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out, err
}

// deliveryKeylessDue reports whether keyless repositories are read again: FLEET_DELIVERY_KEYLESS_MS (6 minutes by
// default) since their last read. envMs already returns a duration.
func deliveryKeylessDue(now, last time.Time) bool {
	return now.Sub(last) >= envMs("FLEET_DELIVERY_KEYLESS_MS", 6*60*1000)
}
