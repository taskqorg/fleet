package main

// Artifact routes: host artifact protocol, store registration, result bytes and preview, artifact and store operations,
// subscription limits, the built app, and the store/lease ticker.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fleet/contract"
	"fleet/coordinator/db"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func init() {
	// Long routes: each takes short transactions around its store calls.
	hostRoute(contract.RouteArtifactsReserve, executionOwned(func(b *contract.ArtifactReserveRequest) string { return b.ExecutionId },
		replyOf[contract.ArtifactReserveRequest, contract.ArtifactReserveReply](artifactReserve)))
	hostRoute(contract.RouteArtifactsParts, replyOf[contract.ArtifactPartsRequest, contract.ArtifactPartsReply](artifactParts))
	hostRoute(contract.RouteArtifactsCommit, replyOf[contract.ArtifactCommitRequest, contract.ArtifactStateReply](artifactCommit))
	hostRoute(contract.RouteArtifactsVerified, replyOf[contract.ArtifactVerifiedRequest, contract.ArtifactStateReply](artifactVerified))
	hostRoute(contract.RouteArtifactsRenew, replyOf[contract.ArtifactLeaseRequest, contract.ArtifactStateReply](artifactRenew))
	hostRoute(contract.RouteArtifactsFailed, replyOf[contract.ArtifactFailedRequest, contract.ArtifactStateReply](artifactRefused))
	hostRoute(contract.RouteArtifactsDeclare, executionOwned(func(b *contract.ArtifactDeclareRequest) string { return b.ExecutionId },
		replyOf[contract.ArtifactDeclareRequest, contract.OkReply](artifactDeclare)))
	hostRoute(contract.RouteArtifactsGet, replyOf[contract.HostArtifactGetRequest, contract.ArtifactGetReply](func(q *Request, b *contract.HostArtifactGetRequest) (any, error) {
		x, err := hostMayRead(q, b)
		if err != nil {
			return nil, err
		}
		return artifactView(q.ctx(), q.app, x.ID, q.hostID())
	}))
	hostRoute(contract.RouteArtifactsRead, func(q *Request, b *contract.HostArtifactGetRequest) (any, error) {
		x, err := hostMayRead(q, b)
		if err != nil {
			return nil, err
		}
		_, open, err := artifactRead(q.ctx(), q.app, x.ID, nil)
		if err != nil {
			return nil, err
		}
		return streamed(open, x.MediaType, nil), nil
	})
	hostRoute(contract.RouteArtifactsList, executionOwned(func(b *contract.HostArtifactListRequest) string { return b.ExecutionId }, hostArtifactList))
	hostRoute(contract.RouteStores, replyOf[contract.StoresRequest, contract.StoresReply](func(q *Request, b *contract.StoresRequest) (any, error) {
		holder, _ := q.host["holder"].(string)
		return storeRegister(q, q.hostID(), &b.Store, holder)
	}))
	route("GET /api/results/{id}", resultBytes)
	route("GET /api/results/{id}/preview", resultPreview)

	op("artifact_list", opArtifactList)
	op("artifact_get", func(q *Request, r *contract.ArtifactGetRequest) (contract.ArtifactView, error) {
		return artifactView(withTx(q), q.app, truncate(r.ArtifactId, 200), "")
	})
	op("artifact_purge", func(q *Request, r *contract.ArtifactPurgeRequest) (contract.ArtifactPurgeReply, error) {
		if err := operatorOnly(q); err != nil {
			return contract.ArtifactPurgeReply{}, err
		}
		return artifactPurge(withTx(q), q.app, r.ContentHash, r.ProjectId, r.Scope, r.Reason)
	})
	op("stores_list", func(q *Request, _ *contract.StoresListRequest) (contract.StoresListReply, error) {
		all, err := storeList(withTx(q), q.app)
		if err != nil {
			return contract.StoresListReply{}, err
		}
		problems := []map[string]json.RawMessage{}
		stores := make([]contract.StoreView, 0, len(all))
		for _, s := range all {
			stores = append(stores, contract.StoreView{Id: s.ID, HostId: s.HostID, Bucket: s.Bucket, Endpoint: s.Endpoint, LocalEndpoint: s.LocalEndpoint, State: s.State,
				CapacityBytes: s.CapacityBytes, UsedBytes: s.UsedBytes, FreeSpaceWarningBytes: s.FreeSpaceWarningBytes, RequiresLogon: s.RequiresLogon, Zone: s.Zone, Holder: s.Holder})
			if s.State == "full" {
				problems = append(problems, recOf(storeFull{Kind: "store-full", StoreID: s.ID, HostID: s.HostID, CapacityBytes: s.CapacityBytes,
					FreeSpaceWarningBytes: s.FreeSpaceWarningBytes, Actions: []string{"store_set_free_space_warning", "project artifact_locations", "artifact_rule_confirm"}}))
			}
		}
		for _, p := range lastPlacementProblems() {
			if pid, _ := p["projectId"].(*string); q.person == nil || fullAccess(q.person) || pid != nil && mayAccessProject(q.person, pid) {
				problems = append(problems, recOf(p))
			}
		}
		return contract.StoresListReply{Stores: stores, Problems: problems}, nil
	})
	op("store_set_free_space_warning", func(q *Request, b *contract.StoreSetFreeSpaceWarningRequest) (contract.StoreSetFreeSpaceWarningReply, error) {
		var none contract.StoreSetFreeSpaceWarningReply
		if err := operatorOnly(q); err != nil {
			return none, err
		}
		if !storeIDPattern.MatchString(b.StoreId) {
			return none, fail(400, "Invalid store id")
		}
		var v *int64
		if b.Bytes != nil {
			if *b.Bytes < 0 || *b.Bytes != float64(int64(*b.Bytes)) {
				return none, fail(400, "Expected bytes or null")
			}
			v = ptr(int64(*b.Bytes))
		}
		r, err := q.db().ArtStoreSetWarning(q.ctx(), db.ArtStoreSetWarningParams{ID: b.StoreId, Bytes: v})
		if errors.Is(err, pgx.ErrNoRows) {
			return none, fail(404, "Unknown store "+b.StoreId)
		}
		if err != nil {
			return none, err
		}
		return contract.StoreSetFreeSpaceWarningReply{Id: r.ID, State: r.State}, nil
	})
	op("subscription_limits", subscriptionLimits)
	// changes_since: the stream's read for the app's catch-up and polling fallback; no cursor is the head.
	op("changes_since", func(q *Request, r *contract.ChangesSinceRequest) (contract.ChangesSinceReply, error) {
		limit := 500
		if r.Limit != nil {
			limit = max(1, min(1000, int(*r.Limit)))
		}
		if r.Cursor == nil {
			c, err := head(q.ctx(), q.tx)
			return contract.ChangesSinceReply{Changes: []contract.Change{}, Cursor: c}, err
		}
		if *r.Cursor < 0 {
			return contract.ChangesSinceReply{}, fail(400, "Invalid cursor")
		}
		cs, next, resync, held, err := since(q.ctx(), q.tx, *r.Cursor, limit)
		if err != nil {
			return contract.ChangesSinceReply{}, err
		}
		out := contract.ChangesSinceReply{Changes: make([]contract.Change, 0, len(cs)), Cursor: next}
		for _, c := range cs {
			out.Changes = append(out.Changes, c.Change)
		}
		if resync {
			out.Resync = &resync
		}
		if held {
			out.Held = &held
		}
		return out, nil
	})

	appRoutes()

	// Stores a host has not reported for 2 minutes are unreachable; expired upload leases fail. Single flight per tick.
	// Reconcile artifact waits independently of the copy, purge, retention and lease work, and once at start (before the
	// server takes requests), so a restart recovers a callback it missed without waiting a tick.
	onStart(func(ctx context.Context, a *App) {
		if err := reconcileArtifactWaits(ctx, a); err != nil {
			log.Printf("recover artifact waits: %v", err)
		}
		every(ctx, "artifact-waits", placementTick, func(ctx context.Context) error {
			return reconcileArtifactWaits(ctx, a)
		})
		every(ctx, "placement", placementTick, func(ctx context.Context) error {
			sweep := artWrite(ctx, a, func(d *db.Queries) error { return d.ArtStoresSweep(ctx, storeUnseenMS) })
			if sweep != nil {
				log.Printf("store sweep: %v", sweep)
			}
			_, _, err := runPlacement(ctx, a)
			if err != nil {
				log.Printf("placement: %v", err)
			}
			return errors.Join(sweep, err)
		})
	})
}

// decodeValid: required keys at every depth (hostKeys), body into T, then T's generated Validate (enums, nested structs, required lists
// not null); strict refuses unknown fields.
func decodeValid[T any, P interface {
	*T
	Validate() error
}](body []byte, strict bool) (*T, error) {
	t := new(T)
	// Required keys present, and none named only case-insensitively (HostID): readBody checks the exact hostId key.
	if err := hostKeys(body, reflect.TypeOf(*t)); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(t); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return nil, fail(400, "Invalid "+te.Field+": expected "+te.Type.String())
		}
		return nil, fail(400, "Invalid body")
	}
	if err := P(t).Validate(); err != nil {
		return nil, fail(400, err.Error())
	}
	return t, nil
}

// mayAccessProject: the owner sees everything; anyone else a granted project, and architects the null project.
func mayAccessProject(p *Person, projectID *string) bool {
	if p == nil {
		return false
	}
	if fullAccess(p) {
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
func hostMayRead(q *Request, b *contract.HostArtifactGetRequest) (*Artifact, error) {
	a, ctx := q.app, q.ctx()
	x, err := artifactRow(ctx, a, truncate(b.ArtifactId, 200))
	if err != nil {
		return nil, err
	}
	hostID := q.hostID()
	if x.ProjectID != nil {
		holder, _ := q.host["holder"].(string)
		if !mayAccessProject(q.peopleSnapshot().person(holder), x.ProjectID) {
			return nil, fail(403, "Artifact not readable by this host")
		}
	}
	d := artDB(ctx, a)
	names := func(id any) bool { return id == x.ID || x.LegacyID != nil && id == *x.LegacyID }
	if deref(b.CommandId) != "" {
		if c, err := d.ArtCommandOf(ctx, deref(b.CommandId)); err == nil && c.HostID == hostID && (c.Status == "pending" || c.Status == "accepted" || c.Status == "started") {
			var p commandArtifacts
			json.Unmarshal(c.Payload, &p)
			switch c.Kind {
			case "delivery-step":
				for _, f := range p.Files {
					if names(f.ArtifactID) {
						return x, nil
					}
				}
			case "artifact-copy", "artifact-verify":
				if names(p.ArtifactID) {
					return x, nil
				}
			}
		}
	}
	if deref(b.ExecutionId) != "" && x.Role != "example" {
		e, err := execGet(ctx, a, *b.ExecutionId)
		if err != nil {
			return nil, err
		}
		if e.HostID != hostID {
			return nil, fail(403, "Execution rejected")
		}
		if x.ExecutionID != nil && *x.ExecutionID == e.ID || e.ProjectID != nil && x.ProjectID != nil && *e.ProjectID == *x.ProjectID {
			return x, nil
		}
		ps, err := d.ArtStartPayloads(ctx, db.ArtStartPayloadsParams{ExecutionID: &e.ID, HostID: &hostID})
		if err != nil {
			return nil, err
		}
		for _, raw := range ps {
			var p commandArtifacts
			json.Unmarshal(raw, &p)
			for _, i := range p.Inputs {
				if i.ArtifactID == x.ID {
					return x, nil
				}
			}
		}
	}
	return nil, fail(403, "Artifact not readable by this host")
}

// commandArtifacts: the artifacts a command payload names (a delivery step's files, a copy or verification, a start or
// script input); an id of another type names none.
type commandArtifacts struct {
	ArtifactID any `json:"artifactId"`
	Files      []struct {
		ArtifactID any `json:"artifactId"`
	} `json:"files"`
	Inputs []struct {
		ArtifactID any `json:"artifactId"`
	} `json:"inputs"`
}

// storeFull: the stores_list problem of a full store, with its actions.
type storeFull struct {
	Kind                  string   `json:"kind"`
	StoreID               string   `json:"storeId"`
	HostID                string   `json:"hostId"`
	CapacityBytes         *int64   `json:"capacityBytes"`
	FreeSpaceWarningBytes *int64   `json:"freeSpaceWarningBytes"`
	Actions               []string `json:"actions"`
}

// hostArtifactListPage: the artifacts of one host listing; an agent pages on with nextCursor.
const hostArtifactListPage = 50

// hostArtifactList: artifact_list for an agent: its own project's artifacts, or its own execution's without a project;
// newest first, one page per call, so no listing returns an unbounded reply.
func hostArtifactList(q *Request, b *contract.HostArtifactListRequest) (any, error) {
	a, ctx := q.app, q.ctx()
	e, err := execGet(ctx, a, truncate(b.ExecutionId, 200))
	if err != nil {
		return nil, err
	}
	bf := contract.HostArtifactListFilter{}
	if b.Filter != nil {
		bf = *b.Filter
	}
	limit := int64(hostArtifactListPage)
	f := listFilter{TaskID: bf.TaskId, MediaType: bf.MediaType, ProjectID: e.ProjectID, NoExamples: true, Limit: &limit}
	if e.ProjectID == nil {
		f.ExecutionID = &e.ID
	} else {
		f.ExecutionID = bf.ExecutionId
	}
	if bf.Cursor != nil {
		before, err := strconv.ParseInt(*bf.Cursor, 10, 64)
		if err != nil || before <= 0 {
			return nil, fail(400, "Expected cursor from nextCursor")
		}
		f.Before = &before
	}
	l, err := artifactList(ctx, a, f)
	if err != nil {
		return nil, err
	}
	out := contract.HostArtifactListReply{Artifacts: l}
	if int64(len(l)) > limit {
		out.Artifacts = l[:limit]
		out.NextCursor = ptr(strconv.FormatInt(out.Artifacts[limit-1].Seq, 10))
	}
	return out, nil
}

// opArtifactList: artifact_list. projectIds and excludeProjectIds are read as sent: a null item is work outside projects,
// which the typed list of strings cannot hold.
func opArtifactList(q *Request, r *contract.ArtifactListRequest) (contract.ArtifactListReply, error) {
	var ids struct {
		ProjectIDs        []*string `json:"projectIds"`
		ExcludeProjectIDs []*string `json:"excludeProjectIds"`
	}
	json.Unmarshal(q.body, &ids)
	f := listFilter{ExecutionID: r.ExecutionId, TaskID: r.TaskId, MediaType: r.MediaType, ProjectID: r.ProjectId, Q: r.Q, States: r.States, Since: r.Since,
		ProjectIDs: ids.ProjectIDs, ExcludeProjectIDs: ids.ExcludeProjectIDs, Limit: r.Limit, Before: r.Before}
	if r.Limit != nil && *r.Limit <= 0 {
		return contract.ArtifactListReply{}, fail(400, "Expected positive integer limit")
	}
	rows, err := artifactList(withTx(q), q.app, f)
	if err == nil {
		err = withTaskCodes(q, rows)
	}
	if err != nil {
		return contract.ArtifactListReply{}, err
	}
	out := contract.ArtifactListReply{Artifacts: rows}
	if f.Limit != nil && int64(len(rows)) > *f.Limit {
		out.Artifacts = rows[:*f.Limit]
		out.NextBefore = ptr(out.Artifacts[*f.Limit-1].Seq)
	}
	return out, nil
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
	found, err := artDB(withTx(q), a).ArtOfExecution(withTx(q), db.ArtOfExecutionParams{ExecutionID: x.ExecutionID, StoredOnly: true})
	if err != nil {
		return nil, err
	}
	siblings := artRows(found)
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

// ---- provider limit readings ----

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
	rs, err := artDB(ctx, a).ArtLimitReadings(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]Reading{}
	for _, x := range rs {
		var v struct {
			Period   any `json:"period"`
			Pct      any `json:"pct"`
			Status   any `json:"status"`
			ResetsAt any `json:"resetsAt"`
		}
		json.Unmarshal(x.Reading, &v)
		r := Reading{Period: "unknown", At: x.UpdatedAt.UnixMilli(), HostID: x.HostID, State: "known"}
		if p, ok := v.Period.(string); ok {
			r.Period = p
		}
		if p, ok := v.Pct.(float64); ok && p >= 0 && p <= 100 {
			r.Pct = &p
		}
		if s, ok := v.Status.(string); ok {
			r.Status = &s
		}
		if t, ok := v.ResetsAt.(float64); ok {
			r.ResetsAt = &t
			if t <= float64(nowMs()) {
				r.State = "unknown"
			}
		}
		out[x.AccountID] = append(out[x.AccountID], r)
	}
	return out, nil
}

// readingOverSwitch: the dispatch skip (§6) for an account: provider rejected, or a percent at or over switchPct (100
// without one: a full subscription cannot run anyway). Only
// readings that know when their period ends count. Empty when none applies. For the dispatch area.
func readingOverSwitch(account doc, list []Reading) string {
	old := func(r Reading) string { return strconv.FormatInt(max(0, (nowMs()-r.At+30000)/60000), 10) + " min old" }
	var best *Reading
	sw, ok := account["switchPct"].(float64)
	if !ok {
		sw = 100
	}
	for i := range list {
		r := list[i]
		if r.State != "known" || r.ResetsAt == nil {
			continue
		}
		if r.Status != nil && *r.Status == "rejected" {
			return "provider limit reached (" + r.Period + "), " + old(r)
		}
		if r.Pct != nil && *r.Pct >= sw && (best == nil || *r.Pct > *best.Pct) {
			best = &list[i]
		}
	}
	if best == nil {
		return ""
	}
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	if !ok {
		return "subscription full; reading " + f(*best.Pct) + " % (" + best.Period + "), " + old(*best)
	}
	return "switch point " + f(sw) + " %; reading " + f(*best.Pct) + " % (" + best.Period + "), " + old(*best)
}

// ---- the built app (FLEET_APP_DIR, default dist/app): hashed assets immutable, entry files revalidated ----

var appScreen func(http.ResponseWriter, *http.Request) bool

func appRoutes() {
	dir, _ := filepath.Abs(envOr("FLEET_APP_DIR", "dist/app"))
	// An incompatible independently installed UI never survives a backend rollback.
	appDir := func() string {
		if cur, _, ok := selectedUIRelease(); ok {
			return cur
		}
		return dir
	}
	file := func(name, media string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			b, err := os.ReadFile(filepath.Join(appDir(), name))
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
		if root := os.Getenv("FLEET_UI_ROOT"); root != "" {
			name := strings.TrimPrefix(r.URL.Path, "/assets/")
			if name != "" && filepath.Base(name) == name {
				if b, err := os.ReadFile(filepath.Join(root, "assets", name)); err == nil {
					http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
					return
				}
			}
		}
		assets.ServeHTTP(w, r)
	})
	publicRoute("GET /sw.js", file("sw.js", "text/javascript"))
	// The bundled UI has no release.json; it still reports itself, so a watcher sees which UI is selected.
	publicRoute("GET /ui-release.json", func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(filepath.Join(appDir(), "release.json")); err == nil {
			file("release.json", "application/json")(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"kind": "bundled", "version": version, "backendVersion": version})
	})
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
	appScreen = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != "GET" && r.Method != "HEAD" {
			return false
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) > 2 || parts[0] == "api" {
			return false
		}
		selected := appDir()
		var screens []string
		b, err := os.ReadFile(filepath.Join(selected, "screens.json"))
		if err == nil {
			json.Unmarshal(b, &screens)
		}
		for _, screen := range append(screens, "lab") {
			if screen == parts[0] {
				b, err := os.ReadFile(filepath.Join(selected, "index.html"))
				if err != nil {
					return false
				}
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Content-Type", "text/html")
				w.Write(b)
				return true
			}
		}
		return false
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
	if q.person == nil || !(fullAccess(q.person) || contains(q.person.Roles, "architect")) {
		return fail(403, "Architect role required")
	}
	return nil
}

// withTaskCodes: each row's task code (D17) as taskCode, in one query; none when the task has none or is out of scope.
func withTaskCodes(q *Request, rows []contract.ArtifactRow) error {
	ids := []string{}
	for _, r := range rows {
		if deref(r.TaskId) != "" {
			ids = append(ids, *r.TaskId)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	cs, err := q.db().ArtTaskCodes(q.ctx(), ids)
	code := map[string]*string{}
	for _, c := range cs {
		code[c.ID] = c.Code
	}
	for i := range rows {
		rows[i].TaskCode = code[deref(rows[i].TaskId)]
	}
	return err
}
