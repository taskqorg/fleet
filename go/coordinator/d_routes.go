package main

// Area D routes: host artifact protocol, store registration, result bytes and preview, artifact and store operations,
// subscription limits, the built app, and the store/lease ticker (packages/coordinator/app.ts).

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func init() {
	// Long routes: each takes short transactions around its store calls.
	longRoute("POST /api/hosts/artifacts/reserve", hostOwned(true, artifactReserve))
	longRoute("POST /api/hosts/artifacts/parts", hostOwned(false, artifactParts))
	longRoute("POST /api/hosts/artifacts/commit", hostOwned(false, artifactCommit))
	longRoute("POST /api/hosts/artifacts/verified", hostOwned(false, artifactVerified))
	longRoute("POST /api/hosts/artifacts/renew", hostOwned(false, artifactRenew))
	longRoute("POST /api/hosts/artifacts/failed", hostOwned(false, artifactRefused))
	longRoute("POST /api/hosts/artifacts/declare", hostOwned(true, artifactDeclare))
	longRoute("POST /api/hosts/artifacts/get", hostOwned(false, func(q *Request) (any, error) {
		x, err := hostMayRead(q)
		if err != nil {
			return nil, err
		}
		return artifactGetView(q.ctx(), q.app, x.ID, q.hostID())
	}))
	longRoute("POST /api/hosts/artifacts/read", hostOwned(false, func(q *Request) (any, error) {
		x, err := hostMayRead(q)
		if err != nil {
			return nil, err
		}
		_, open, err := artifactRead(q.ctx(), q.app, x.ID, nil)
		if err != nil {
			return nil, err
		}
		return streamed(open, x.MediaType, nil), nil
	}))
	longRoute("POST /api/hosts/artifacts/list", hostOwned(true, hostArtifactList))
	longRoute("POST /api/hosts/stores", hostOwned(false, func(q *Request) (any, error) {
		var b struct {
			Store map[string]any `json:"store"`
		}
		decode(q.body, &b)
		holder, _ := q.host["holder"].(string)
		return storeRegister(q, q.hostID(), b.Store, holder)
	}))
	route("GET /api/results/{id}", resultBytes)
	route("GET /api/results/{id}/preview", resultPreview)

	operation("artifact_list", true, opArtifactList)
	operation("artifact_get", true, func(q *Request) (any, error) {
		var b struct{ ArtifactID string }
		decode(q.body, &b)
		return artifactGetView(withTx(q), q.app, truncate(b.ArtifactID, 200), "")
	})
	operation("artifact_purge", false, func(q *Request) (any, error) {
		if err := operatorOnly(q); err != nil {
			return nil, err
		}
		var b struct {
			ContentHash, Reason string
			ProjectID, Scope    *string
		}
		decode(q.body, &b)
		return artifactPurge(withTx(q), q.app, b.ContentHash, b.ProjectID, b.Scope, b.Reason)
	})
	operation("stores_list", true, func(q *Request) (any, error) {
		all, err := storeList(withTx(q), q.app)
		if err != nil {
			return nil, err
		}
		problems := []doc{}
		for _, s := range all {
			if s.State == "full" {
				problems = append(problems, doc{"kind": "store-full", "storeId": s.ID, "hostId": s.HostID, "capacityBytes": s.CapacityBytes, "freeSpaceWarningBytes": s.FreeSpaceWarningBytes,
					"actions": []string{"store_set_free_space_warning", "project artifact_locations", "artifact_rule_confirm"}})
			}
		}
		for _, p := range lastPlacementProblems() {
			if pid, _ := p["projectId"].(*string); q.person == nil || q.person.Owner || pid != nil && mayAccessProject(q.person, pid) {
				problems = append(problems, p)
			}
		}
		if all == nil {
			all = []StoreRow{}
		}
		return doc{"stores": all, "problems": problems}, nil
	})
	operation("store_set_free_space_warning", false, func(q *Request) (any, error) {
		if err := operatorOnly(q); err != nil {
			return nil, err
		}
		var b struct {
			StoreID string
			Bytes   *float64
		}
		decode(q.body, &b)
		if !storeIDPattern.MatchString(b.StoreID) {
			return nil, fail(400, "Invalid store id")
		}
		var v any
		if b.Bytes != nil {
			if *b.Bytes < 0 || *b.Bytes != float64(int64(*b.Bytes)) {
				return nil, fail(400, "Expected bytes or null")
			}
			v = int64(*b.Bytes)
		}
		r, err := dOne(withTx(q), q.app, "UPDATE store SET free_space_warning_bytes=$2,state=CASE WHEN state='unreachable' THEN state WHEN $2::bigint IS NOT NULL AND capacity_bytes < $2::bigint THEN 'full' ELSE 'reachable' END WHERE id=$1 RETURNING id,state", b.StoreID, v)
		if err != nil {
			return nil, err
		}
		if r == nil {
			return nil, fail(404, "Unknown store "+b.StoreID)
		}
		return r, nil
	})
	operation("subscription_limits", true, subscriptionLimits)

	appRoutes()

	// Stores a host has not reported for 2 minutes are unreachable; expired upload leases fail. Single flight per tick.
	// Then one placement pass (copies, purges, rules, lease expiry, the artifact retry window).
	onStart(func(ctx context.Context, a *App) {
		go func() {
			t := time.NewTicker(placementTick)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if err := dExec(ctx, a, "UPDATE store SET state='unreachable' WHERE state<>'unreachable' AND (observed_at IS NULL OR observed_at < now() - ($1::text||' milliseconds')::interval)", strconv.Itoa(storeUnseenMS)); err != nil {
						log.Printf("store sweep: %v", err)
					}
					if _, _, err := runPlacement(ctx, a); err != nil {
						log.Printf("placement: %v", err)
					}
				}
			}
		}()
	})
}

// hostOwned: the host routes' ownership check; a body hostId is the authenticated host (readBody), and with execution the
// named execution must be that host's.
func hostOwned(execution bool, h handler) handler {
	return func(q *Request) (any, error) {
		if q.host == nil {
			return nil, fail(403, "Host rejected")
		}
		if execution {
			var b struct {
				ExecutionID string `json:"executionId"`
			}
			decode(q.body, &b)
			if b.ExecutionID == "" {
				return nil, fail(400, "Expected executionId")
			}
			{
				e, err := execGet(q.ctx(), q.app, b.ExecutionID)
				if err != nil {
					return nil, err
				}
				if e.HostID != q.hostID() {
					return nil, fail(403, "Execution rejected")
				}
			}
		}
		return h(q)
	}
}

// mayAccessProject: the owner sees everything; anyone else a granted project, and architects the null project.
func mayAccessProject(p *Person, projectID *string) bool {
	if p == nil {
		return false
	}
	if p.Owner {
		return true
	}
	if projectID == nil {
		return contains(p.Roles, "architect")
	}
	for _, g := range p.Grants {
		if g.Kind == "project" && g.ID == *projectID {
			return true
		}
	}
	return false
}

// hostMayRead: a host reads an artifact for its own open command that names it (delivery files, copies, verify), or for
// its own execution in the artifact's project, or a start/script input of that execution. Lab examples never.
func hostMayRead(q *Request) (*Artifact, error) {
	a, ctx := q.app, q.ctx()
	var b struct{ ArtifactID, CommandID, ExecutionID string }
	decode(q.body, &b)
	x, err := artifactRow(ctx, a, truncate(b.ArtifactID, 200))
	if err != nil {
		return nil, err
	}
	hostID := q.hostID()
	if x.ProjectID != nil {
		holder, _ := q.host["holder"].(string)
		if !mayAccessProject(a.people[holder], x.ProjectID) {
			return nil, fail(403, "Artifact not readable by this host")
		}
	}
	names := func(c map[string]any, s *Artifact) bool {
		p, _ := c["payload"].(map[string]any)
		ids := []any{}
		switch c["kind"] {
		case "delivery-step":
			fs, _ := p["files"].([]any)
			for _, f := range fs {
				if m, ok := f.(map[string]any); ok {
					ids = append(ids, m["artifactId"])
				}
			}
		case "artifact-copy", "artifact-verify":
			ids = append(ids, p["artifactId"])
		}
		for _, id := range ids {
			if id == s.ID || s.LegacyID != nil && id == *s.LegacyID {
				return true
			}
		}
		return false
	}
	if b.CommandID != "" {
		if r, err := dOne(ctx, a, "SELECT data FROM document WHERE kind='command' AND id=$1", b.CommandID); err == nil && r != nil {
			c, _ := r["data"].(map[string]any)
			st, _ := c["status"].(string)
			if c["hostId"] == hostID && (st == "pending" || st == "accepted" || st == "started") && names(c, x) {
				return x, nil
			}
		}
	}
	if b.ExecutionID != "" && x.Role != "example" {
		e, err := execGet(ctx, a, b.ExecutionID)
		if err != nil {
			return nil, err
		}
		if e.HostID != hostID {
			return nil, fail(403, "Execution rejected")
		}
		if x.ExecutionID != nil && *x.ExecutionID == e.ID || e.ProjectID != nil && x.ProjectID != nil && *e.ProjectID == *x.ProjectID {
			return x, nil
		}
		cs, err := dRows(ctx, conn(ctx, a), "SELECT data FROM document WHERE kind='command' AND data->>'executionId'=$1 AND data->>'hostId'=$2 AND data->>'kind' IN ('start','script')", e.ID, hostID)
		if err != nil {
			return nil, err
		}
		for _, r := range cs {
			c, _ := r["data"].(map[string]any)
			p, _ := c["payload"].(map[string]any)
			in, _ := p["inputs"].([]any)
			for _, i := range in {
				if m, ok := i.(map[string]any); ok && m["artifactId"] == x.ID {
					return x, nil
				}
			}
		}
	}
	return nil, fail(403, "Artifact not readable by this host")
}

// hostArtifactList: artifact_list for an agent: its own project's artifacts, or its own execution's without a project.
func hostArtifactList(q *Request) (any, error) {
	a, ctx := q.app, q.ctx()
	var b struct {
		ExecutionID string
		Filter      struct{ ExecutionID, TaskID, MediaType *string }
	}
	decode(q.body, &b)
	e, err := execGet(ctx, a, truncate(b.ExecutionID, 200))
	if err != nil {
		return nil, err
	}
	f := listFilter{TaskID: b.Filter.TaskID, MediaType: b.Filter.MediaType, ProjectID: e.ProjectID, NoExamples: true}
	if e.ProjectID == nil {
		f.ExecutionID = &e.ID
	} else {
		f.ExecutionID = b.Filter.ExecutionID
	}
	l, err := artifactList(ctx, a, f)
	return doc{"artifacts": l}, err
}

func opArtifactList(q *Request) (any, error) {
	var b struct {
		ExecutionID, TaskID, MediaType, ProjectID, Q *string
		States                                       []string
		Since                                        *float64
		ProjectIDs                                   []*string
		Limit                                        *float64
		Before                                       *float64
	}
	decode(q.body, &b)
	f := listFilter{ExecutionID: b.ExecutionID, TaskID: b.TaskID, MediaType: b.MediaType, ProjectID: b.ProjectID, Q: b.Q, States: b.States, Since: b.Since, ProjectIDs: b.ProjectIDs}
	if b.Limit != nil {
		if *b.Limit <= 0 || *b.Limit != float64(int64(*b.Limit)) {
			return nil, fail(400, "Expected positive integer limit")
		}
		l := int64(*b.Limit)
		f.Limit = &l
	}
	if b.Before != nil && *b.Before == float64(int64(*b.Before)) {
		v := int64(*b.Before)
		f.Before = &v
	}
	rows, err := artifactList(withTx(q), q.app, f)
	if err != nil || f.Limit == nil {
		return doc{"artifacts": rows}, err
	}
	var next any
	if int64(len(rows)) > *f.Limit {
		rows = rows[:*f.Limit]
		next = rows[len(rows)-1]["seq"]
	}
	return doc{"artifacts": rows, "nextBefore": next}, nil
}

type readCloser = interface {
	Read([]byte) (int, error)
	Close() error
}

// streamed: the bytes of an open store object as a raw response (status 200 unless set by head).
func streamed(open func() (readCloser, error), media string, head func(http.Header) int) raw {
	return func(w http.ResponseWriter) error {
		r, err := open()
		if err != nil {
			return err
		}
		defer r.Close()
		if media == "" {
			media = "application/octet-stream"
		}
		w.Header().Set("Content-Type", media)
		code := 200
		if head != nil {
			code = head(w.Header())
		}
		w.WriteHeader(code)
		_, err = io.Copy(w, r)
		return nil // headers are out; a broken stream only ends the response
	}
}

func stored(q *Request) (*Artifact, error) {
	x, err := artifactRow(withTx(q), q.app, q.r.PathValue("id"))
	if err != nil {
		return nil, err
	}
	if x.State != "stored" {
		return nil, fail(404, "Not found")
	}
	return x, nil
}

var rangePattern = regexp.MustCompile(`^bytes=(\d*)-(\d*)$`)

// resultBytes: agent output is untrusted; no scripts run, types without a view download, one Range part for paged text.
func resultBytes(q *Request) (any, error) {
	x, err := stored(q)
	if err != nil {
		return nil, err
	}
	ctx, a := context.WithoutCancel(q.ctx()), q.app
	media, size := x.MediaType, x.Size
	viewable := regexp.MustCompile(`^(image|video|text)/|^application/(json|xml)`).MatchString(media)
	head := func(h http.Header) {
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:; media-src 'self'")
		h.Set("Accept-Ranges", "bytes")
		if !viewable {
			h.Set("Content-Disposition", `attachment; filename="`+regexp.MustCompile(`[^\w.-]+`).ReplaceAllString(x.Name, "_")+`"`)
		}
	}
	m := rangePattern.FindStringSubmatch(q.r.Header.Get("Range"))
	var rng []int64
	if m != nil && size > 0 && (m[1] != "" || m[2] != "") {
		a1, _ := strconv.ParseInt(m[1], 10, 64)
		a2, _ := strconv.ParseInt(m[2], 10, 64)
		start, end := a1, size-1
		if m[1] == "" {
			start = max(0, size-a2)
		} else if m[2] != "" {
			end = min(a2, size-1)
		}
		if start >= size || start > end {
			return raw(func(w http.ResponseWriter) error {
				head(w.Header())
				w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
				w.WriteHeader(416)
				return nil
			}), nil
		}
		rng = []int64{start, end}
	}
	_, open, err := artifactRead(ctx, a, x.ID, rng)
	if err != nil {
		return nil, err
	}
	return streamed(open, media, func(h http.Header) int {
		head(h)
		if rng == nil {
			return 200
		}
		h.Set("Content-Range", "bytes "+strconv.FormatInt(rng[0], 10)+"-"+strconv.FormatInt(rng[1], 10)+"/"+strconv.FormatInt(size, 10))
		h.Set("Content-Length", strconv.FormatInt(rng[1]-rng[0]+1, 10))
		return 206
	}), nil
}

var refPattern = regexp.MustCompile(`(?i)(\b(?:src|href)\s*=\s*["'])([^"'#?]+)((?:[?#][^"']*)?["'])`)
var schemePattern = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*:|^//`)
var resourceType = regexp.MustCompile(`^(image|font|audio|video)/|^text/css\b|javascript\b`)

// resultPreview: an HTML or SVG result runs in a sandbox without allow-same-origin and with connect-src 'none'; the page's
// own sibling files (same execution) are inlined as data: URLs, since the opaque origin carries no Access cookie.
func resultPreview(q *Request) (any, error) {
	x, err := stored(q)
	if err != nil {
		return nil, err
	}
	media := x.MediaType
	if !regexp.MustCompile(`^(text/html|image/svg\+xml)\b`).MatchString(media) {
		return nil, fail(415, "No interactive preview for "+media)
	}
	ctx, a := context.WithoutCancel(q.ctx()), q.app
	csp := func(h http.Header) int {
		h.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-modals; default-src 'none'; script-src https: 'unsafe-inline' 'unsafe-eval' data: blob:; style-src https: 'unsafe-inline' data:; img-src https: data: blob:; media-src https: data: blob:; font-src https: data:; connect-src 'none'; form-action 'none'; frame-ancestors 'self'")
		return 200
	}
	if !strings.HasPrefix(media, "text/html") || x.ExecutionID == nil {
		_, open, err := artifactRead(ctx, a, x.ID, nil)
		if err != nil {
			return nil, err
		}
		return streamed(open, media, csp), nil
	}
	page, err := artifactBytes(ctx, a, x.ID, x.Size)
	if err != nil {
		return nil, err
	}
	siblings, err := artifacts(withTx(q), a, "WHERE execution_id=$1 AND state='stored' AND role='original'", *x.ExecutionID)
	if err != nil {
		return nil, err
	}
	byPath := map[string]*Artifact{}
	for i := range siblings {
		byPath[siblings[i].Name] = &siblings[i]
	}
	dir := strings.Split(x.Name, "/")
	dir = dir[:len(dir)-1]
	inline := map[string]string{}
	html := string(page)
	for _, m := range refPattern.FindAllStringSubmatch(html, -1) {
		ref := m[2]
		if schemePattern.MatchString(ref) || strings.HasPrefix(ref, "/") {
			continue
		}
		if _, ok := inline[ref]; ok {
			continue
		}
		parts := append([]string{}, dir...)
		for _, seg := range strings.Split(ref, "/") {
			if seg == ".." {
				if len(parts) > 0 {
					parts = parts[:len(parts)-1]
				}
			} else if seg != "" && seg != "." {
				parts = append(parts, seg)
			}
		}
		f := byPath[strings.Join(parts, "/")]
		if f == nil || !resourceType.MatchString(f.MediaType) {
			continue
		}
		b, err := artifactBytes(ctx, a, f.ID, f.Size)
		if err != nil {
			return nil, err
		}
		inline[ref] = "data:" + f.MediaType + ";base64," + base64.StdEncoding.EncodeToString(b)
	}
	out := refPattern.ReplaceAllStringFunc(html, func(all string) string {
		m := refPattern.FindStringSubmatch(all)
		if d, ok := inline[m[2]]; ok {
			return m[1] + d + regexp.MustCompile(`^[?#][^"']*`).ReplaceAllString(m[3], "")
		}
		return all
	})
	return raw(func(w http.ResponseWriter) error {
		w.Header().Set("Content-Type", media)
		csp(w.Header())
		_, err := io.WriteString(w, out)
		return err
	}), nil
}

// ---- readings (packages/coordinator/readings.ts) ----

type Reading struct {
	Period   string   `json:"period"`
	Pct      *float64 `json:"pct"`
	Status   *string  `json:"status"`
	ResetsAt *float64 `json:"resetsAt"`
	At       int64    `json:"at"`
	HostID   *string  `json:"hostId"`
	State    string   `json:"state"`
}

// latestReadings: the latest limit_reading per (subscription, period); one whose resetsAt has passed is unknown.
func latestReadings(ctx context.Context, a *App) (map[string][]Reading, error) {
	rs, err := conn(ctx, a).Query(ctx, `SELECT DISTINCT ON (x.account_id, r->>'period') x.account_id, x.host_id, r, d.updated_at
   FROM document d JOIN execution x ON x.id = d.data->>'executionId' CROSS JOIN LATERAL jsonb_array_elements(coalesce(d.data->'data'->'readings','[]'::jsonb)) r
   WHERE d.kind='event' AND d.data->>'type'='limit_reading' AND x.account_id IS NOT NULL
   ORDER BY x.account_id, r->>'period', d.seq DESC`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := map[string][]Reading{}
	for rs.Next() {
		var account string
		var host *string
		var v map[string]any
		var at time.Time
		if err := rs.Scan(&account, &host, &v, &at); err != nil {
			return nil, err
		}
		r := Reading{Period: "unknown", At: at.UnixMilli(), HostID: host, State: "known"}
		if p, ok := v["period"].(string); ok {
			r.Period = p
		}
		if p, ok := v["pct"].(float64); ok && p >= 0 && p <= 100 {
			r.Pct = &p
		}
		if s, ok := v["status"].(string); ok {
			r.Status = &s
		}
		if t, ok := v["resetsAt"].(float64); ok {
			r.ResetsAt = &t
			if t <= float64(now()) {
				r.State = "unknown"
			}
		}
		out[account] = append(out[account], r)
	}
	return out, rs.Err()
}

func subscriptionLimits(q *Request) (any, error) {
	ctx := withTx(q)
	m, err := latestReadings(ctx, q.app)
	if err != nil {
		return nil, err
	}
	accounts, err := dRows(ctx, conn(ctx, q.app), "SELECT data FROM document WHERE kind='managed-account' ORDER BY seq")
	if err != nil {
		return nil, err
	}
	subs := []doc{}
	for _, r := range accounts {
		x, _ := r["data"].(map[string]any)
		id, _ := x["id"].(string)
		rd := m[id]
		if rd == nil {
			rd = []Reading{}
		}
		subs = append(subs, doc{"id": id, "name": x["name"], "harness": x["harness"], "holder": x["holder"], "intake": x["intake"], "switchPct": x["switchPct"], "readings": rd})
	}
	return doc{"serverNow": now(), "subscriptions": subs}, nil
}

// readingOverSwitch: the dispatch skip (§6) for an account: provider rejected, or a percent at or over switchPct. Only
// readings that know when their period ends count. Empty when none applies. For the dispatch area.
func readingOverSwitch(account map[string]any, list []Reading) string {
	old := func(r Reading) string { return strconv.FormatInt(max(0, (now()-r.At+30000)/60000), 10) + " min old" }
	var best *Reading
	sw, hasSw := account["switchPct"].(float64)
	for i := range list {
		r := list[i]
		if r.State != "known" || r.ResetsAt == nil {
			continue
		}
		if r.Status != nil && *r.Status == "rejected" {
			return "provider limit reached (" + r.Period + "), " + old(r)
		}
		if hasSw && r.Pct != nil && *r.Pct >= sw && (best == nil || *r.Pct > *best.Pct) {
			best = &list[i]
		}
	}
	if best == nil {
		return ""
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	return "switch point " + f(sw) + " %; reading " + f(*best.Pct) + " % (" + best.Period + "), " + old(*best)
}

// ---- the built app (FLEET_APP_DIR, default dist/app): hashed assets immutable, entry files revalidated ----

func appRoutes() {
	dir, _ := filepath.Abs(envOr("FLEET_APP_DIR", "dist/app"))
	file := func(name, media string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				writeError(w, fail(404, "App not built"))
				return
			}
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Content-Type", media)
			w.Write(b)
		}
	}
	publicRoute("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/overview", http.StatusFound) })
	assets := http.StripPrefix("/assets/", http.FileServer(noDirs{http.Dir(filepath.Join(dir, "assets"))}))
	publicRoute("GET /assets/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		assets.ServeHTTP(w, r)
	})
	publicRoute("GET /sw.js", file("sw.js", "text/javascript"))
	manifest, icon := regexp.MustCompile(`^[a-z]+\.webmanifest$`), regexp.MustCompile(`^[a-z0-9-]+\.svg$`)
	publicRoute("GET /manifests/{name}", func(w http.ResponseWriter, r *http.Request) {
		if n := r.PathValue("name"); manifest.MatchString(n) {
			file("manifests/"+n, "application/manifest+json")(w, r)
			return
		}
		writeError(w, fail(404, "Unknown manifest"))
	})
	publicRoute("GET /icons/{name}", func(w http.ResponseWriter, r *http.Request) {
		if n := r.PathValue("name"); icon.MatchString(n) {
			file("icons/"+n, "image/svg+xml")(w, r)
			return
		}
		writeError(w, fail(404, "Unknown icon"))
	})
	// App screens come from the build (screens.json); /lab is always one.
	var screens []string
	if b, err := os.ReadFile(filepath.Join(dir, "screens.json")); err == nil {
		json.Unmarshal(b, &screens)
	}
	seen := map[string]bool{}
	for _, s := range append(screens, "lab") {
		if !regexp.MustCompile(`^[a-z]+$`).MatchString(s) || seen[s] {
			continue
		}
		seen[s] = true
		publicRoute("GET /"+s, file("index.html", "text/html"))
		publicRoute("GET /"+s+"/{id}", file("index.html", "text/html"))
	}
}

// noDirs: no directory listings under /assets/.
type noDirs struct{ fs http.FileSystem }

func (n noDirs) Open(name string) (http.File, error) {
	f, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err == nil && st.IsDir() {
		f.Close()
		return nil, os.ErrNotExist
	}
	return f, nil
}

// operatorOnly: purge and store settings change every project's bytes; the owner or an architect only (TypeScript let any
// person call them, inside row-level security).
func operatorOnly(q *Request) error {
	if q.person == nil || !(q.person.Owner || contains(q.person.Roles, "architect")) {
		return fail(403, "Architect role required")
	}
	return nil
}
