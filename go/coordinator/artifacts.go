package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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
	"github.com/jackc/pgx/v5/pgtype"
)

// Artifacts and stores (docs/designs/fleet-data-foundation.md §3). Bytes go between executors and the per-host stores;
// the coordinator holds rows, chooses stores, presigns, and promotes a staging object to its content-hash key only after
// the store host's executor re-hashed it over loopback (P3-D1). A verified object is never overwritten.
// Dropped (no data to keep): legacy base64 artifact documents, their adapter and the backfill.

const (
	emptySHA256   = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	partSize      = 32 << 20
	copyLimit     = 5 << 30
	copyPart      = 1 << 30
	leaseMS       = 3600000
	urlSeconds    = 900
	storeUnseenMS = 120000
	placementTick = 60 * time.Second
)

// Store settings: the default artifact locations (FLEET_DEFAULT_ARTIFACT_LOCATIONS, default pds18) when a project names none.
var (
	defaultLocations   = splitList(envOr("FLEET_DEFAULT_ARTIFACT_LOCATIONS", "pds18"))
	storeEndpointHosts = splitList(os.Getenv("FLEET_STORE_ENDPOINT_HOSTS"))
	storeSecretsDir    = envOr("FLEET_STORE_SECRETS", "var/stores")
	retryWindowMS, _   = strconv.ParseFloat(envOr("FLEET_ARTIFACT_RETRY_WINDOW_MS", "86400000"), 64)
)

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func splitList(s string) []string {
	out := []string{}
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func failf(code int, f string, v ...any) error { return fail(code, fmt.Sprintf(f, v...)) }

// ---- short transactions of the long routes (q.tx is nil there) ----

// An operation already runs in the dispatcher's transaction (in q.ctx(), via withTx); a long route has none and takes
// short ones of its own.
type txKey struct{}

func withTx(q *Request) context.Context {
	if q == nil || q.tx == nil {
		return q.baseCtx()
	}
	return context.WithValue(q.baseCtx(), txKey{}, q.tx)
}

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// conn returns the transaction carried by ctx (always present for q.ctx() while q.tx is set), else the write pool.
func conn(ctx context.Context, a *App) querier {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return a.store.pool
}

// txCtx runs fn in the transaction carried by ctx (refusing write helpers inside a read-only transaction), else opens a
// short GLOBAL write transaction on the pool.
func txCtx(ctx context.Context, a *App, fn func(pgx.Tx) error) error {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		if !txWrites(tx) {
			return errors.New("txCtx inside a read transaction")
		}
		return fn(tx)
	}
	return a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error { return fn(q.tx) })
}

// execCtx: an untyped write of the files not yet on the typed queries; this file reads and writes through
// queries/artifacts.sql (artDB, txCtx).
func execCtx(ctx context.Context, a *App, sql string, args ...any) error {
	return txCtx(ctx, a, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, sql, args...); return err })
}

// artWrite: typed writes in the context's transaction, else in a short one of their own.
func artWrite(ctx context.Context, a *App, fn func(*db.Queries) error) error {
	return txCtx(ctx, a, func(tx pgx.Tx) error { return fn(db.New(tx)) })
}

// ---- stores ----

type StoreRow struct {
	ID                    string  `json:"id"`
	HostID                string  `json:"host_id"`
	Bucket                string  `json:"bucket"`
	Endpoint              string  `json:"endpoint"`
	LocalEndpoint         *string `json:"local_endpoint"`
	State                 string  `json:"state"`
	CapacityBytes         *int64  `json:"capacity_bytes"`
	UsedBytes             *int64  `json:"used_bytes"`
	FreeSpaceWarningBytes *int64  `json:"free_space_warning_bytes"`
	RequiresLogon         bool    `json:"requires_logon"`
	Zone                  *string `json:"zone"`
	Holder                *string `json:"holder"`
}

func storeList(ctx context.Context, a *App) ([]StoreRow, error) {
	rs, err := connDB(ctx, a).ArtStores(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]StoreRow, 0, len(rs))
	for _, s := range rs {
		out = append(out, StoreRow{ID: s.ID, HostID: s.HostID, Bucket: s.Bucket, Endpoint: s.Endpoint, LocalEndpoint: s.LocalEndpoint, State: s.State,
			CapacityBytes: s.CapacityBytes, UsedBytes: s.UsedBytes, FreeSpaceWarningBytes: s.FreeSpaceWarningBytes, RequiresLogon: s.RequiresLogon, Zone: s.Zone, Holder: s.Holder})
	}
	return out, nil
}

// storeListAll reads every store on ctx's transaction (temporarily widening row scope when ctx carries a scoped
// request transaction) for internal store routing after artifact or project access has already been verified.
func storeListAll(ctx context.Context, a *App) ([]StoreRow, error) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	if !ok {
		return storeList(ctx, a)
	}
	d := db.New(tx)
	was, err := d.TxScope(ctx)
	if err != nil {
		return nil, err
	}
	if was.ProjectIds == "*" {
		return storeList(ctx, a)
	}
	if err := d.TxSetScope(ctx, db.TxSetScopeParams{ProjectIds: "*", Person: ""}); err != nil {
		return nil, err
	}
	out, err := storeList(ctx, a)
	if rerr := d.TxSetScope(ctx, db.TxSetScopeParams{ProjectIds: was.ProjectIds, Person: was.Person}); rerr != nil && err == nil {
		err = rerr
	}
	return out, err
}

// storeHost: the host of a store, "" when there is none.
func storeHost(ctx context.Context, a *App, id string) (string, error) {
	h, err := connDB(ctx, a).ArtStoreHost(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return h, err
}

func storeGet(ctx context.Context, a *App, id string) (*StoreRow, error) {
	all, err := storeList(ctx, a)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, fail(404, "Unknown store "+id)
}

var storeIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func checkURL(s string, name string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fail(400, "Invalid "+name)
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

func isLoopback(u string) bool {
	p, err := url.Parse(u)
	if err != nil {
		return false
	}
	h := p.Hostname()
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

// allowedTunnel: a tunnel endpoint must be on an operator-allowed host (exact hosts or .suffixes).
func allowedTunnel(u string, hosts []string) bool {
	p, err := url.Parse(u)
	if err != nil {
		return false
	}
	h := p.Hostname()
	for _, x := range hosts {
		if strings.HasPrefix(x, ".") && strings.HasSuffix(h, x) || h == x {
			return true
		}
	}
	return false
}

func storeKeyFile(id string) string { return filepath.Join(storeSecretsDir, id+".key") }

type storeKey struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

// storeRegister: only the executor of the store's host may create or replace it; the S3 key goes to a 0600 file.
func storeRegister(q *Request, hostID string, b *contract.StoreRegistration, holder string) (any, error) {
	a := q.app
	id, bucket := b.Id, deref(b.Bucket)
	if !storeIDPattern.MatchString(id) {
		return nil, fail(400, "Invalid store id")
	}
	if !storeIDPattern.MatchString(bucket) {
		return nil, fail(400, "Invalid bucket")
	}
	key, secret := deref(b.AccessKeyId), deref(b.SecretAccessKey)
	if key == "" || secret == "" {
		return nil, fail(400, "Expected store credentials")
	}
	endpoint, err := checkURL(deref(b.Endpoint), "endpoint")
	if err != nil {
		return nil, err
	}
	var local *string
	if deref(b.LocalEndpoint) != "" {
		l, err := checkURL(*b.LocalEndpoint, "localEndpoint")
		if err != nil {
			return nil, err
		}
		local = &l
	}
	if !allowedTunnel(endpoint, storeEndpointHosts) {
		return nil, fail(403, "Store endpoint host is not allowed")
	}
	if local != nil && !isLoopback(*local) {
		return nil, fail(400, "localEndpoint must be a loopback URL")
	}
	ctx := q.ctx()
	if h, err := storeHost(ctx, a, id); err != nil {
		return nil, err
	} else if h != "" && h != hostID {
		return nil, fail(403, "Store belongs to another host")
	}
	err = artWrite(ctx, a, func(d *db.Queries) error {
		return storeUpsert(ctx, d, db.ArtStoreUpsertParams{ID: id, HostID: hostID, Zone: optStr(deref(b.Zone)), Bucket: bucket, Endpoint: endpoint, LocalEndpoint: local,
			CapacityBytes: b.CapacityBytes, UsedBytes: b.UsedBytes, RequiresLogon: b.RequiresLogon != nil && *b.RequiresLogon, Holder: &holder})
	})
	if err != nil {
		return nil, err
	}
	// The upsert's WHERE makes ownership part of the write: a store of another host is unchanged, and so is its key.
	if h, err := storeHost(ctx, a, id); err != nil {
		return nil, err
	} else if h != hostID {
		return nil, fail(403, "Store belongs to another host")
	}
	if err := os.MkdirAll(storeSecretsDir, 0o700); err != nil {
		return nil, err
	}
	kb, _ := json.Marshal(storeKey{key, secret})
	if err := os.WriteFile(storeKeyFile(id), kb, 0o600); err != nil {
		return nil, err
	}
	os.Chmod(storeKeyFile(id), 0o600)
	s3cache.Delete(id) // new credentials
	return contract.StoresReply{Ok: true, StoreId: id}, nil
}

// storesObserve: the poll report (the poll route calls it in its own transaction): fresh capacity left and used bytes; a
// store is full when the capacity left falls below its free_space_warning.
// storeReport: one store of a poll report as the host sent it; a field of another type counts as absent.
type storeReport struct {
	ID            any `json:"id"`
	CapacityBytes any `json:"capacityBytes"`
	UsedBytes     any `json:"usedBytes"`
	Reachable     any `json:"reachable"`
}

func storesObserve(ctx context.Context, tx pgx.Tx, hostID string, stores any) error {
	var list []json.RawMessage
	json.Unmarshal(jsonBytes(stores), &list)
	num := func(v any) *int64 {
		if f, ok := v.(float64); ok && f == float64(int64(f)) {
			n := int64(f)
			return &n
		}
		return nil
	}
	for _, x := range list {
		var s storeReport
		if json.Unmarshal(x, &s) != nil {
			continue
		}
		id, ok := s.ID.(string)
		if !ok {
			continue
		}
		if err := storeObserve(ctx, db.New(tx), db.ArtStoreObserveParams{ID: id, HostID: hostID, Capacity: num(s.CapacityBytes), Used: num(s.UsedBytes),
			Reachable: s.Reachable == true}); err != nil {
			return err
		}
	}
	return nil
}

func storeCredentials(id string) (storeKey, error) {
	var k storeKey
	b, err := os.ReadFile(storeKeyFile(id))
	if err != nil || json.Unmarshal(b, &k) != nil {
		return k, fail(503, "No credentials for store "+id)
	}
	return k, nil
}

// accessToken: Cloudflare Access service token from FLEET_CF_ACCESS_FILE (CF_ACCESS_CLIENT_ID, CF_ACCESS_CLIENT_SECRET),
// read when used.
func accessToken() map[string]string {
	f := os.Getenv("FLEET_CF_ACCESS_FILE")
	if f == "" {
		return nil
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return nil
	}
	v := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(l, "export "))
		if i := strings.Index(l, "="); i > 0 {
			v[l[:i]] = strings.Trim(l[i+1:], `'"`)
		}
	}
	if v["CF_ACCESS_CLIENT_ID"] == "" || v["CF_ACCESS_CLIENT_SECRET"] == "" {
		return nil
	}
	return map[string]string{"cf-access-client-id": v["CF_ACCESS_CLIENT_ID"], "cf-access-client-secret": v["CF_ACCESS_CLIENT_SECRET"]}
}

// One S3 client per store and route; tunnel requests get the Access headers after signing, only to an allowed host.
var s3cache sync.Map // store id -> map key -> *S3; ponytail: key includes endpoints, so a re-registration with new ones gets new clients

func s3For(s *StoreRow, local bool) (*S3, error) {
	lp := ""
	if s.LocalEndpoint != nil {
		lp = *s.LocalEndpoint
	}
	k := fmt.Sprint(local, "|", s.Endpoint, "|", lp)
	m, _ := s3cache.LoadOrStore(s.ID, &sync.Map{})
	if c, ok := m.(*sync.Map).Load(k); ok {
		return c.(*S3), nil
	}
	cred, err := storeCredentials(s.ID)
	if err != nil {
		return nil, err
	}
	endpoint := s.Endpoint
	var extra func() map[string]string
	if local && lp != "" {
		endpoint = lp
	} else if allowedTunnel(s.Endpoint, storeEndpointHosts) {
		extra = accessToken
	}
	c, err := newS3(endpoint, cred.AccessKeyID, cred.SecretAccessKey, extra)
	if err != nil {
		return nil, err
	}
	m.(*sync.Map).Store(k, c)
	return c, nil
}

func tunnel(s *StoreRow) (*S3, error) { return s3For(s, false) }

// presign for a host: the store's loopback endpoint when that host is the store's own, its tunnel otherwise.
func presignFor(s *StoreRow, hostID, method, key string, q map[string]string) (string, error) {
	c, err := s3For(s, hostID == s.HostID && s.LocalEndpoint != nil)
	if err != nil {
		return "", err
	}
	return c.Presign(method, s.Bucket, key, urlSeconds, q), nil
}

// ---- artifacts ----

type Artifact struct {
	ID, Scope, ContentHash, MediaType, Name, Role, State                       string
	ProjectID, ExecutionID, TaskID, LegacyID, DerivedFrom, Previous, LastError *string
	Code                                                                       *string // D17 R code; originals only
	Summary                                                                    *string // output.json's summary line
	Size                                                                       int64
	Required                                                                   bool
	Seq                                                                        int64
	Created                                                                    time.Time
}

func artifactOf(r db.Artifact) Artifact {
	return Artifact{ID: r.ID, Scope: r.Scope, ContentHash: r.ContentHash, MediaType: r.MediaType, Name: r.Name, Role: r.Role, State: r.State,
		ProjectID: r.ProjectID, ExecutionID: r.ExecutionID, TaskID: r.TaskID, LegacyID: r.LegacyID, DerivedFrom: r.DerivedFromArtifactID,
		Previous: r.PreviousArtifactID, LastError: r.LastError, Size: r.Size, Required: r.Required, Seq: r.Seq, Created: r.CreatedAt, Code: r.Code, Summary: r.Summary}
}

func artRows(rs []db.Artifact) []Artifact {
	out := make([]Artifact, 0, len(rs))
	for _, r := range rs {
		out = append(out, artifactOf(r))
	}
	return out
}

// diagnosticFile: a file the executor writes about its own run (the MCP lifecycle receipts, mcp_stdio.go), stored with
// role diagnostic: never a result, deleted after diagnosticDays (placement.go).
func diagnosticFile(name string) bool {
	return strings.HasPrefix(filepath.Base(name), "mcp-lifecycle-")
}

const diagnosticDays = 7

// storedArtifactRows: every stored artifact, for placement.go.
func storedArtifactRows(ctx context.Context, a *App) ([]Artifact, error) {
	rs, err := connDB(ctx, a).ArtStoredAll(ctx)
	return artRows(rs), err
}

func artifactRow(ctx context.Context, a *App, id string) (*Artifact, error) {
	// A deleted artifact is unknown, whoever asks (RLS hides it too; a host route that sees deleted rows does not read it).
	r, err := connDB(ctx, a).ArtByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fail(404, "Unknown artifact")
	}
	if err != nil {
		return nil, err
	}
	x := artifactOf(r)
	return &x, nil
}

func projectKey(p *string) string {
	if p == nil {
		return "_"
	}
	return *p
}
func keyOf(x *Artifact) string { return projectKey(x.ProjectID) + "/" + x.Scope + "/" + x.ContentHash }
func stagingOf(x *Artifact, storeID string) string {
	return projectKey(x.ProjectID) + "/" + x.Scope + "/.pending/" + x.ID + "." + storeID
}

// row: the artifact as artifact_list shows it.
func (x *Artifact) row() contract.ArtifactRow {
	return contract.ArtifactRow{Id: x.ID, LegacyId: x.LegacyID, ProjectId: x.ProjectID, Scope: x.Scope, ExecutionId: x.ExecutionID, TaskId: x.TaskID,
		Name: x.Name, MediaType: x.MediaType, Size: x.Size, Sha256: x.ContentHash, Required: x.Required, Role: x.Role,
		DerivedFromArtifactId: x.DerivedFrom, PreviousArtifactId: x.Previous, State: x.State, Error: x.LastError,
		Seq: x.Seq, CreatedAt: float64(x.Created.UnixMilli()), Code: x.Code, Summary: x.Summary}
}

// view: the artifact's facts (lab_retry.go's snapshot): the list row without its order and time.
func (x *Artifact) view() json.RawMessage {
	r := x.row()
	v, _ := openObject(jsonBytes(r))
	delete(v, "seq")
	delete(v, "createdAt")
	delete(v, "code")
	return jsonBytes(v)
}

var shaPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func checkName(s string) (string, error) {
	if s == "" || len(s) > 1000 {
		return "", fail(400, "Invalid artifact name")
	}
	for _, p := range strings.Split(s, "/") {
		if p == "" || p == "." || p == ".." {
			return "", fail(400, "Invalid artifact name")
		}
	}
	return s, nil
}

type execInfo struct {
	ID, HostID                    string
	ProjectID, TaskID, Experiment *string
	Required                      []string          // the names an agent declared required (artifactDeclare)
	Refused                       []artifactFailure // reservations the coordinator refused (artifactRefused)
}

func execGet(ctx context.Context, a *App, id string) (*execInfo, error) {
	r, err := connDB(ctx, a).ArtExecution(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fail(404, "Unknown execution "+id)
	}
	if err != nil {
		return nil, err
	}
	var refused []artifactFailure
	json.Unmarshal(r.RefusedArtifacts, &refused)
	return &execInfo{ID: r.ID, HostID: r.HostID, ProjectID: r.ProjectID, TaskID: r.TaskID, Experiment: r.ExperimentID, Required: jsonStrings(r.RequiredArtifacts),
		Refused: refused}, nil
}

// jsonStrings: the strings of a JSON list (other items and other values are left out).
func jsonStrings(b []byte) []string {
	var l []any
	json.Unmarshal(b, &l)
	out := []string{}
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func declaredRequired(e *execInfo) []string {
	if e == nil {
		return []string{}
	}
	return e.Required
}

// selectStore: the writer's host when listed, else the first listed reachable store.
func selectStore(ctx context.Context, a *App, locations []string, hostID string) (*StoreRow, error) {
	all, err := storeListAll(ctx, a)
	if err != nil {
		return nil, err
	}
	ok := []StoreRow{}
	for _, s := range all {
		if s.State == "reachable" && contains(locations, s.HostID) {
			ok = append(ok, s)
		}
	}
	for i := range ok {
		if ok[i].HostID == hostID {
			return &ok[i], nil
		}
	}
	for _, h := range locations {
		for i := range ok {
			if ok[i].HostID == h {
				return &ok[i], nil
			}
		}
	}
	return nil, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func failCode(code int, msg, c string) error { return &codedError{HTTPError{Code: code, Msg: msg}, c} }

type codedError struct {
	HTTPError
	code string
}

func (e *codedError) Unwrap() error { return &e.HTTPError }

func isMissingUpload(err error) bool {
	var se *S3Error
	if errors.As(err, &se) && (se.Code == "NoSuchUpload" || se.Status == 404 && strings.Contains(strings.ToLower(se.Msg), "upload not found")) {
		return true
	}
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "upload not found")
}

func isMissingKey(err error) bool {
	var se *S3Error
	return errors.As(err, &se) && (se.Code == "NoSuchKey" || se.Status == 404)
}

// reopenPendingCopy creates a fresh multipart upload session when a pending reservation's upload session or staging
// object is missing or its store became unreachable, avoiding duplicate pending artifact rows.
func reopenPendingCopy(ctx context.Context, a *App, id, executionID, hostID string) (*contract.ArtifactReserveReply, error) {
	c, err := copyOf(ctx, a, id, "")
	if err != nil {
		return nil, err
	}
	needNew := c == nil || (c.State != "pending" && c.State != "copying") || c.UploadID == nil
	if !needNew {
		oldStore, err := storeGet(ctx, a, c.StoreID)
		if err != nil {
			oldStore = nil
		}
		if oldStore == nil || oldStore.State != "reachable" {
			needNew = true
		} else if t, err := tunnel(oldStore); err == nil {
			if _, err := t.text("GET", t.path(oldStore.Bucket, deref(c.StagingKey)), map[string]string{"uploadId": *c.UploadID, "max-parts": "1"}, nil, nil); err != nil && isMissingUpload(err) {
				needNew = true
			}
		}
	}
	if !needNew {
		return nil, nil
	}
	x, err := artifactRow(ctx, a, id)
	if err != nil {
		return nil, err
	}
	e, err := execGet(ctx, a, executionID)
	if err != nil {
		return nil, err
	}
	target, err := selectStore(ctx, a, artifactLocations(a, e.ProjectID), hostID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, failCode(409, "No reachable store in the project locations", "no_store")
	}
	staging := stagingOf(x, target.ID)
	t, err := tunnel(target)
	if err != nil {
		return nil, err
	}
	upload, err := t.CreateMultipartUpload(target.Bucket, staging, x.MediaType)
	if err != nil {
		return nil, err
	}
	if err := txCtx(ctx, a, func(tx pgx.Tx) error {
		tctx := context.WithValue(ctx, txKey{}, tx)
		if err := objectLock(tctx, tx, target.ID, keyOf(x)); err != nil {
			return err
		}
		qd := db.New(tx)
		if c != nil && c.StoreID != target.ID && (c.State == "pending" || c.State == "copying") {
			if err := artifactCopyTransition(tctx, qd, id, c.StoreID, artifactCopyEvent{To: "failed", Why: ptr("re-reserved on another store"), RetryWindowMs: retryWindowMS}); err != nil {
				return err
			}
		}
		tc, err := copyOf(tctx, a, id, target.ID)
		if err != nil {
			return err
		}
		if tc == nil {
			return artifactCopyInsertPending(tctx, qd, db.ArtCopyInsertPendingParams{ArtifactID: id, StoreID: target.ID, ProjectID: x.ProjectID, UploadID: &upload, StagingKey: &staging, LeaseMs: leaseMS})
		}
		if tc.State != "failed" && tc.State != "deleted" {
			if err := artifactCopyTransition(tctx, qd, id, target.ID, artifactCopyEvent{To: "failed", Why: ptr("upload session replaced"), RetryWindowMs: retryWindowMS}); err != nil {
				return err
			}
		}
		return artifactCopyStart(tctx, qd, db.DispCopyStartParams{ArtifactID: id, StoreID: target.ID, ProjectID: x.ProjectID, UploadID: &upload, StagingKey: &staging})
	}); err != nil {
		return nil, err
	}
	ps := int64(partSize)
	return &contract.ArtifactReserveReply{ArtifactId: id, State: "pending", StoreId: &target.ID, PartSize: &ps}, nil
}

var artifactIDPattern = regexp.MustCompile(`^[a-zA-Z0-9-]{8,80}$`)

func artifactReserve(q *Request, b *contract.ArtifactReserveRequest) (any, error) {
	a, ctx := q.app, q.ctx()
	id := deref(b.ArtifactId)
	if !artifactIDPattern.MatchString(id) {
		id = newID()
	}
	d := connDB(ctx, a)
	if old, err := d.ArtReservation(ctx, id); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	} else if err == nil {
		// A retry of the same reservation only: another execution's artifact id is refused, not described.
		if deref(old.ExecutionID) != b.ExecutionId {
			return nil, fail(409, "Artifact id in use")
		}
		ps := int64(partSize)
		if old.State == "pending" {
			if rep, err := reopenPendingCopy(ctx, a, id, b.ExecutionId, q.hostID()); err != nil || rep != nil {
				return rep, err
			}
		}
		out := contract.ArtifactReserveReply{ArtifactId: id, State: old.State, PartSize: &ps}
		if sid, err := d.ArtLiveCopyStore(ctx, id); err == nil {
			out.StoreId = &sid
		}
		if old.State == "stored" || old.State == "failed" {
			if err := settleWaiting(ctx, a, b.ExecutionId); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	size := b.Size
	if size < 0 {
		return nil, fail(400, "Expected size")
	}
	hash := b.Sha256
	if !shaPattern.MatchString(hash) {
		return nil, fail(400, "Expected sha256")
	}
	file, err := checkName(b.Name)
	if err != nil {
		return nil, err
	}
	e, err := execGet(ctx, a, b.ExecutionId)
	if err != nil {
		return nil, err
	}
	scope := "prod"
	if e.Experiment != nil {
		scope = "lab"
	}
	if purged, err := d.ArtPurged(ctx, db.ArtPurgedParams{ProjectID: projectKey(e.ProjectID), Scope: scope, ContentHash: hash}); err != nil {
		return nil, err
	} else if purged {
		return nil, failCode(409, "Content was purged", "purged")
	}
	target, err := selectStore(ctx, a, artifactLocations(a, e.ProjectID), q.hostID())
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, failCode(409, "No reachable store in the project locations", "no_store")
	}
	media := b.MediaType
	if media == "" || len(media) >= 200 {
		media = "application/octet-stream"
	}
	required := b.Required != nil && *b.Required || contains(declaredRequired(e), file)
	role := deref(b.Role)
	if role != "thumbnail" && role != "poster" {
		role = "original"
	}
	if diagnosticFile(file) {
		role = "diagnostic"
	}
	insert := func(tx pgx.Tx, state string) error {
		return db.New(tx).ArtInsert(ctx, db.ArtInsertParams{ID: id, ProjectID: e.ProjectID, Scope: scope, ContentHash: hash, Size: size, MediaType: media, Name: file,
			ExecutionID: &e.ID, TaskID: e.TaskID, Required: required, Role: role, DerivedFromArtifactID: optStr(deref(b.DerivedFrom)),
			PreviousArtifactID: optStr(deref(b.PreviousArtifactId)), State: state})
	}
	verified := db.ArtVerifiedContentParams{ProjectID: e.ProjectID, Scope: scope, ContentHash: hash, StoreID: target.ID}
	same, err := d.ArtVerifiedContent(ctx, verified)
	if err != nil {
		return nil, err
	}
	at := target
	if size == 0 && hash != emptySHA256 {
		return nil, fail(400, "Empty file with a non-empty hash")
	}
	gone := false
	if same || size == 0 {
		// Without the GLOBAL lock when this is the request's own transaction: an empty file's store write happens in it,
		// and the object lock (writeObject, objectLock) is what serializes these rows with a purge of the content.
		tx := func(fn func(pgx.Tx) error) error {
			if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
				return fn(tx)
			}
			return a.store.Write(ctx, nil, nil, func(q *Request) error { return fn(q.tx) })
		}
		err := tx(func(tx pgx.Tx) error {
			// An empty file has nothing to upload (stores refuse an empty multipart part): the coordinator writes it
			// itself, under the object lock (writeObject: a Head, and a Put only when the object is missing).
			if size == 0 && !same {
				w, _, err := writeObject(context.WithValue(ctx, txKey{}, tx), a, e.ProjectID, scope, nil, media)
				if err != nil {
					return err
				}
				at = w
			}
			// Checked again under the object lock: system_purge deletes an object nobody maps to under the same lock
			// (cleanup.go runPurgeTasks), so a copy found before it may be gone now; then this upload stores its own.
			if err := objectLock(ctx, tx, at.ID, keyOf(&Artifact{ProjectID: e.ProjectID, Scope: scope, ContentHash: hash})); err != nil {
				return err
			}
			if same {
				still, err := db.New(tx).ArtVerifiedContent(ctx, verified)
				if err != nil {
					return err
				}
				gone = !still
				if gone && size > 0 {
					return nil // uploaded below
				}
				if gone { // an empty object a purge removed: written again
					w, _, err := writeObject(context.WithValue(ctx, txKey{}, tx), a, e.ProjectID, scope, nil, media)
					if err != nil {
						return err
					}
					at, gone = w, false
				}
			}
			if err := insert(tx, "stored"); err != nil {
				return err
			}
			return artifactCopyInsertVerified(ctx, db.New(tx), id, at.ID, e.ProjectID)
		})
		if err != nil {
			return nil, err
		}
		if !gone {
			if err := settleWaiting(ctx, a, e.ID); err != nil {
				return nil, err
			}
			return contract.ArtifactReserveReply{ArtifactId: id, State: "stored", StoreId: &at.ID}, nil
		}
	}
	staging := stagingOf(&Artifact{ID: id, ProjectID: e.ProjectID, Scope: scope, ContentHash: hash}, target.ID)
	c, err := tunnel(target)
	if err != nil {
		return nil, err
	}
	upload, err := c.CreateMultipartUpload(target.Bucket, staging, media)
	if err != nil {
		return nil, err
	}
	err = txCtx(ctx, a, func(tx pgx.Tx) error {
		// A new copy of this content: a purge of the same object waits for it, or finished before it (cleanup.go).
		if err := objectLock(ctx, tx, target.ID, keyOf(&Artifact{ProjectID: e.ProjectID, Scope: scope, ContentHash: hash})); err != nil {
			return err
		}
		if err := insert(tx, "pending"); err != nil {
			return err
		}
		return artifactCopyInsertPending(ctx, db.New(tx), db.ArtCopyInsertPendingParams{ArtifactID: id, StoreID: target.ID, ProjectID: e.ProjectID, UploadID: &upload,
			StagingKey: &staging, LeaseMs: leaseMS})
	})
	if err != nil {
		return nil, err
	}
	ps := int64(partSize)
	return contract.ArtifactReserveReply{ArtifactId: id, State: "pending", StoreId: &target.ID, PartSize: &ps}, nil
}

func (q *Request) hostID() string { return q.host.HostID }

type copyRow struct {
	StoreID, State       string
	UploadID, StagingKey *string
}

// copyOf: the artifact's copy on the store, else (no store) the one in progress; nil when there is none.
func copyOf(ctx context.Context, a *App, artifactID, storeID string) (*copyRow, error) {
	r, err := connDB(ctx, a).ArtCopyWorking(ctx, db.ArtCopyWorkingParams{ArtifactID: artifactID, StoreID: optStr(storeID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &copyRow{StoreID: r.StoreID, State: r.State, UploadID: r.UploadID, StagingKey: r.StagingKey}, nil
}

// working: the copy an upload works on (the given store, else the one in progress); a committed upload (upload id cleared,
// staging complete) counts when committed is set.
func working(ctx context.Context, a *App, artifactID, storeID string, committed bool) (*copyRow, error) {
	c, err := copyOf(ctx, a, artifactID, storeID)
	if err != nil {
		return nil, err
	}
	if c == nil || (c.State != "pending" && c.State != "copying") || !(c.UploadID != nil || committed && c.StagingKey != nil) {
		return nil, fail(409, "No upload in progress for this artifact")
	}
	return c, nil
}

// allowedHost: only the writer (the execution's host) or the target store's host may drive an upload.
func allowedHost(ctx context.Context, a *App, x *Artifact, s *StoreRow, hostID string) error {
	if hostID == s.HostID {
		return nil
	}
	if x.ExecutionID != nil {
		if e, err := execGet(ctx, a, *x.ExecutionID); err == nil && e.HostID == hostID {
			return nil
		}
	}
	return fail(403, "Upload belongs to another host")
}

// lease: a fresh lease on the copy; clearUpload ends its multipart upload, keepLater never shortens the lease.
func lease(ctx context.Context, a *App, artifactID, storeID string, clearUpload, keepLater bool) error {
	return artWrite(ctx, a, func(d *db.Queries) error {
		return d.ArtCopyLease(ctx, db.ArtCopyLeaseParams{ArtifactID: artifactID, StoreID: storeID, LeaseMs: leaseMS, ClearUpload: clearUpload, KeepLater: keepLater})
	})
}

func artifactParts(q *Request, b *contract.ArtifactPartsRequest) (any, error) {
	a, ctx := q.app, q.ctx()
	x, err := artifactRow(ctx, a, b.ArtifactId)
	if err != nil {
		return nil, err
	}
	c, err := working(ctx, a, x.ID, deref(b.StoreId), false)
	if err != nil {
		return nil, err
	}
	s, err := storeGet(ctx, a, c.StoreID)
	if err != nil {
		return nil, err
	}
	if err := allowedHost(ctx, a, x, s, b.HostId); err != nil {
		return nil, err
	}
	if err := lease(ctx, a, x.ID, s.ID, false, false); err != nil {
		return nil, err
	}
	parts := []contract.ArtifactPartUrl{}
	for _, n := range b.PartNumbers {
		if n < 1 || n > 10000 || len(parts) == 64 {
			continue
		}
		u, err := presignFor(s, b.HostId, "PUT", *c.StagingKey, map[string]string{"partNumber": strconv.FormatInt(n, 10), "uploadId": *c.UploadID})
		if err != nil {
			return nil, err
		}
		parts = append(parts, contract.ArtifactPartUrl{PartNumber: n, Url: u})
	}
	return contract.ArtifactPartsReply{StoreId: s.ID, Parts: parts}, nil
}

func artifactCommit(q *Request, b *contract.ArtifactCommitRequest) (any, error) {
	a, ctx := q.app, q.ctx()
	x, err := artifactRow(ctx, a, b.ArtifactId)
	if err != nil {
		return nil, err
	}
	if x.State == "stored" && deref(b.StoreId) == "" {
		return contract.ArtifactStateReply{State: "stored"}, nil
	}
	// Idempotent: a repeated commit of a completed upload skips the completion and hands out the verification again.
	c, err := working(ctx, a, x.ID, deref(b.StoreId), true)
	if err != nil {
		return nil, err
	}
	s, err := storeGet(ctx, a, c.StoreID)
	if err != nil {
		return nil, err
	}
	if err := allowedHost(ctx, a, x, s, b.HostId); err != nil {
		return nil, err
	}
	parts := make([]S3Part, len(b.Parts))
	for i, p := range b.Parts {
		parts[i] = S3Part{int(p.PartNumber), deref(p.Etag)}
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	t, err := tunnel(s)
	if err != nil {
		return nil, err
	}
	if c.UploadID != nil {
		if err := t.CompleteMultipartUpload(s.Bucket, *c.StagingKey, *c.UploadID, parts); err != nil {
			if isMissingUpload(err) {
				_ = lease(ctx, a, x.ID, s.ID, true, false)
				return nil, failCode(409, "Multipart upload session not found (NoSuchUpload)", "no_such_upload")
			}
			return nil, err
		}
	}
	size, err := t.Head(s.Bucket, *c.StagingKey)
	if err != nil {
		return nil, err
	}
	if size < 0 {
		_ = lease(ctx, a, x.ID, s.ID, true, false)
		return nil, failCode(409, "Staging object not found (NoSuchKey)", "no_such_key")
	}
	if size != x.Size {
		return contract.ArtifactStateReply{State: "failed"}, reject(ctx, a, x, s, *c.StagingKey, fmt.Sprintf("size %d differs from reserved %d", size, x.Size))
	}
	if err := lease(ctx, a, x.ID, s.ID, true, false); err != nil {
		return nil, err
	}
	v, err := s3For(s, s.LocalEndpoint != nil)
	if err != nil {
		return nil, err
	}
	verify := contract.ArtifactVerify{ArtifactId: x.ID, StoreId: s.ID, Url: v.Presign("GET", s.Bucket, *c.StagingKey, urlSeconds, nil)}
	if b.HostId == s.HostID {
		return contract.ArtifactStateReply{State: "verifying", Verify: &verify}, nil
	}
	open, err := connDB(ctx, a).ArtVerifyQueued(ctx, db.ArtVerifyQueuedParams{HostID: &s.HostID, ArtifactID: x.ID, StoreID: s.ID})
	if err != nil {
		return nil, err
	}
	if !open {
		if err := queueCommand(ctx, a, s.HostID, "artifact-verify", verify.ArtifactId, verify); err != nil {
			return nil, err
		}
	}
	return contract.ArtifactStateReply{State: "verifying"}, nil
}

// jobExecution: the queued tool execution record, written through executionWrite (executions.go).
type jobExecution struct {
	ID          string `json:"id"`
	ExecutionID string `json:"executionId"`
	SessionID   string `json:"sessionId"`
	HostID      string `json:"hostId"`
	State       string `json:"state"`
	CreatedAt   int64  `json:"createdAt"`
	Actor       string `json:"actor"`
	ArtifactID  any    `json:"artifactId"`
}

// queueCommand: a tool command for a host's executor (copy, verify) on an artifact, in a program-worker execution of its
// own (a host job: no session); payload is the command's (a contract struct).
func queueCommand(ctx context.Context, a *App, hostID, kind, artifactID string, payload any) error {
	return txCtx(ctx, a, func(tx pgx.Tx) error {
		q := backgroundRequest(ctx, a, tx) // carries app: put wakes the host's held poll once the command commits
		id := newID()
		if err := executionWrite(q, jobExecution{ID: id, ExecutionID: id, HostID: hostID, State: "preparing", CreatedAt: nowMs(), Actor: "program-worker",
			ArtifactID: artifactID}); err != nil {
			return err
		}
		return commandPut(q, &agentCommand{ID: newID(), ExecutionID: id, HostID: hostID, Kind: kind, Status: "pending", Payload: jsonBytes(payload)})
	})
}

// Lease renewal by the executors holding an upload, copy or verification.
func artifactRenew(q *Request, b *contract.ArtifactLeaseRequest) (any, error) {
	a, ctx := q.app, q.ctx()
	x, err := artifactRow(ctx, a, b.ArtifactId)
	if err != nil {
		return nil, err
	}
	c, err := working(ctx, a, x.ID, deref(b.StoreId), true)
	if err != nil {
		return contract.ArtifactStateReply{State: x.State}, nil
	}
	s, err := storeGet(ctx, a, c.StoreID)
	if err != nil {
		return nil, err
	}
	if err := allowedHost(ctx, a, x, s, b.HostId); err != nil {
		return nil, err
	}
	if err := reverify(ctx, a, x, s, c, b.HostId); err != nil {
		return nil, err
	}
	return contract.ArtifactStateReply{State: c.State}, lease(ctx, a, x.ID, s.ID, false, true)
}

// reverify queues the store host's verification again for a committed upload that no open verification covers. The
// verification carries a presigned GET, so one that waited past its signature (a drain held it) or failed on the host
// left the copy pending and the uploader renewing forever (2026-11-26: 12 windows uploads to pds18 after the store's
// verify jobs failed with "Date is too old"). Each renew while none is open queues one with a fresh URL.
func reverify(ctx context.Context, a *App, x *Artifact, s *StoreRow, c *copyRow, uploader string) error {
	if c.State != "pending" || c.UploadID != nil || c.StagingKey == nil || uploader == s.HostID {
		return nil
	}
	open, err := connDB(ctx, a).ArtVerifyQueued(ctx, db.ArtVerifyQueuedParams{HostID: &s.HostID, ArtifactID: x.ID, StoreID: s.ID})
	if err != nil || open {
		return err
	}
	v, err := s3For(s, s.LocalEndpoint != nil)
	if err != nil {
		return err
	}
	verify := contract.ArtifactVerify{ArtifactId: x.ID, StoreId: s.ID, Url: v.Presign("GET", s.Bucket, *c.StagingKey, urlSeconds, nil)}
	return queueCommand(ctx, a, s.HostID, "artifact-verify", verify.ArtifactId, verify)
}

// reject: the staging object goes, the copy fails with a retry backoff (1 minute doubling, up to the retry window), the
// artifact fails when it has no verified copy.
func reject(ctx context.Context, a *App, x *Artifact, s *StoreRow, key, why string) error {
	if s.Bucket != "" && key != "" {
		if t, err := tunnel(s); err == nil {
			t.Delete(s.Bucket, key)
		}
	}
	err := artWrite(ctx, a, func(d *db.Queries) error {
		if err := artifactCopyTransition(ctx, d, x.ID, s.ID, artifactCopyEvent{To: "failed", Why: &why, RetryWindowMs: retryWindowMS}); err != nil {
			return err
		}
		return d.ArtFailUnstored(ctx, db.ArtFailUnstoredParams{ID: x.ID, Why: &why})
	})
	if err != nil || x.ExecutionID == nil {
		return err
	}
	return settleWaiting(ctx, a, *x.ExecutionID)
}

// artifactRefused: the host reports the store refused an upload permanently; the artifact fails now with its reason.
func artifactRefused(q *Request, b *contract.ArtifactFailedRequest) (any, error) {
	a, ctx := q.app, q.ctx()
	x, err := artifactRow(ctx, a, b.ArtifactId)
	var he *HTTPError
	if err != nil && asHTTP(err, &he) && he.Code == 404 && deref(b.ExecutionId) != "" && deref(b.Name) != "" {
		// A refused reservation has no artifact row: the refusal goes on the execution (Refused save).
		return refusedReservation(q, b)
	}
	if err != nil {
		return nil, err
	}
	if x.State != "pending" {
		return contract.ArtifactStateReply{State: x.State}, nil
	}
	if x.ExecutionID == nil {
		return nil, fail(403, "Artifact not uploaded by this host")
	}
	if e, err := execGet(ctx, a, *x.ExecutionID); err != nil || e.HostID != b.HostId {
		return nil, fail(403, "Artifact not uploaded by this host")
	}
	why := "upload refused: unknown reason"
	if b.Error != "" {
		why = "upload refused: " + truncate(b.Error, 500)
	}
	all, _ := storeList(ctx, a)
	cs, err := connDB(ctx, a).ArtCopiesInProgress(ctx, x.ID)
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		s := storeIn(all, c.StoreID)
		if up := deref(c.UploadID); up != "" && s.Bucket != "" {
			if t, err := tunnel(s); err == nil {
				t.AbortMultipartUpload(s.Bucket, deref(c.StagingKey), up)
			}
		}
		if err := reject(ctx, a, x, s, deref(c.StagingKey), why); err != nil {
			return nil, err
		}
	}
	x, err = artifactRow(ctx, a, x.ID)
	if err != nil {
		return nil, err
	}
	return contract.ArtifactStateReply{State: x.State}, nil
}

// storeIn: the store of the list with the id, else a store known only by its id.
func storeIn(all []StoreRow, id string) *StoreRow {
	s := &StoreRow{ID: id}
	for i := range all {
		if all[i].ID == id {
			s = &all[i]
		}
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// artifactVerified: promotion; the staging object becomes the content-hash key only when that key is absent.
func artifactVerified(q *Request, b *contract.ArtifactVerifiedRequest) (any, error) {
	a, ctx := q.app, q.ctx()
	x, err := artifactRow(ctx, a, b.ArtifactId)
	if err != nil {
		return nil, err
	}
	s, err := storeGet(ctx, a, b.StoreId)
	if err != nil {
		return nil, err
	}
	if b.HostId != s.HostID {
		return nil, fail(403, "Only the store host verifies its copies")
	}
	c, err := copyOf(ctx, a, x.ID, s.ID)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return contract.ArtifactStateReply{State: "unknown"}, nil
	}
	if c.State != "pending" && c.State != "copying" {
		if x.ExecutionID != nil && (c.State == "verified" || c.State == "failed") {
			if err := settleWaiting(ctx, a, *x.ExecutionID); err != nil {
				return nil, err
			}
		}
		return contract.ArtifactStateReply{State: c.State}, nil
	}
	staging := ""
	if c.StagingKey != nil {
		staging = *c.StagingKey
	}
	if b.Sha256 != x.ContentHash {
		return contract.ArtifactStateReply{State: "failed"}, reject(ctx, a, x, s, staging, "content hash differs from the reserved hash")
	}
	t, err := tunnel(s)
	if err != nil {
		return nil, err
	}
	if err := promote(t, s.Bucket, staging, keyOf(x), x.Size); err != nil {
		if isMissingKey(err) {
			return nil, failCode(409, "Staging object not found (NoSuchKey)", "no_such_key")
		}
		return nil, err
	}
	if err := t.Delete(s.Bucket, staging); err != nil {
		return nil, err
	}
	err = artWrite(ctx, a, func(d *db.Queries) error {
		if err := artifactCopyTransition(ctx, d, x.ID, s.ID, artifactCopyEvent{To: "verified"}); err != nil {
			return err
		}
		return d.ArtStoredNow(ctx, x.ID)
	})
	if err != nil {
		return nil, err
	}
	if x.ExecutionID != nil {
		if err := settleWaiting(ctx, a, *x.ExecutionID); err != nil {
			return nil, err
		}
	}
	return contract.ArtifactStateReply{State: "stored"}, nil
}

// artifactDeclare: an agent's required declaration (artifact_save required=true), recorded on the execution.
func artifactDeclare(q *Request, b *contract.ArtifactDeclareRequest) (any, error) {
	a, ctx := q.app, q.ctx()
	file, err := checkName(b.Name)
	if err != nil {
		return nil, err
	}
	err = artWrite(ctx, a, func(d *db.Queries) error {
		if err := d.ArtDeclareRequired(ctx, db.ArtDeclareRequiredParams{ExecutionID: b.ExecutionId, Name: file}); err != nil {
			return err
		}
		return d.ArtMarkRequired(ctx, db.ArtMarkRequiredParams{ExecutionID: &b.ExecutionId, Name: file})
	})
	if err != nil {
		return nil, err
	}
	return contract.OkReply{Ok: true}, nil
}

// source: a verified copy to read from, the reader host's own store first; a store with a purge pending serves nothing.
func source(ctx context.Context, a *App, x *Artifact, hostID string) (*StoreRow, error) {
	rs, err := connDB(ctx, a).ArtReadableStores(ctx, x.ID)
	if err != nil {
		return nil, err
	}
	all, err := storeListAll(ctx, a)
	if err != nil {
		return nil, err
	}
	var pick *StoreRow
	for i := range all {
		s := &all[i]
		if s.State == "unreachable" {
			continue
		}
		for _, r := range rs {
			if r == s.ID {
				if s.HostID == hostID {
					return s, nil
				}
				if pick == nil {
					pick = s
				}
			}
		}
	}
	return pick, nil
}

// artifactRead streams the bytes of a stored artifact from a verified copy.
func artifactRead(ctx context.Context, a *App, id string, rng []int64) (*Artifact, func() (interface {
	Read([]byte) (int, error)
	Close() error
}, error), error) {
	x, err := artifactRow(ctx, a, id)
	if err != nil {
		return nil, nil, err
	}
	if x.State != "stored" {
		return nil, nil, fail(409, "Artifact not stored")
	}
	s, err := source(ctx, a, x, "")
	if err != nil {
		return nil, nil, err
	}
	if s == nil {
		return nil, nil, fail(503, "Artifact unavailable: no reachable store holds a verified copy")
	}
	return x, func() (interface {
		Read([]byte) (int, error)
		Close() error
	}, error) {
		t, err := tunnel(s)
		if err != nil {
			return nil, err
		}
		return t.Get(s.Bucket, keyOf(x), rng)
	}, nil
}

// artifactBytes: bounded read for coordinator code; larger than max is refused before any byte is read.
func artifactBytes(ctx context.Context, a *App, id string, max int64) ([]byte, error) {
	x, open, err := artifactRead(ctx, a, id, nil)
	if err != nil {
		return nil, err
	}
	if x.Size > max {
		return nil, failf(413, "Artifact larger than %d bytes", max)
	}
	r, err := open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	buf := make([]byte, 0, x.Size)
	tmp := make([]byte, 64<<10)
	for {
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if int64(len(buf)) > max {
			return nil, failf(413, "Artifact larger than %d bytes", max)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return nil, err
		}
	}
}

// artifactView: metadata and a presigned GET for the given host.
func artifactView(ctx context.Context, a *App, id, hostID string) (contract.ArtifactView, error) {
	x, err := artifactRow(ctx, a, id)
	if err != nil {
		return contract.ArtifactView{}, err
	}
	cs, err := connDB(ctx, a).ArtCopyStates(ctx, x.ID)
	if err != nil {
		return contract.ArtifactView{}, err
	}
	v := contract.ArtifactView{Summary: x.Summary, Id: x.ID, LegacyId: x.LegacyID, ProjectId: x.ProjectID, Scope: x.Scope, ExecutionId: x.ExecutionID, TaskId: x.TaskID,
		Name: x.Name, MediaType: x.MediaType, Size: x.Size, Sha256: x.ContentHash, Required: x.Required, Role: x.Role,
		DerivedFromArtifactId: x.DerivedFrom, PreviousArtifactId: x.Previous, State: x.State, Error: x.LastError,
		Copies: make([]contract.ArtifactCopyState, 0, len(cs)), Availability: x.State, Code: x.Code}
	for _, c := range cs {
		v.Copies = append(v.Copies, contract.ArtifactCopyState{StoreId: c.StoreID, State: c.State})
	}
	if x.State == "stored" {
		s, err := source(ctx, a, x, hostID)
		if err != nil {
			return contract.ArtifactView{}, err
		}
		v.Availability = "unavailable"
		if s != nil {
			v.Availability = "available"
			if hostID == "" {
				// People and agent tools read through Fleet's existing authorization and stream.
				v.Url = ptr("/api/results/" + url.PathEscape(x.ID))
			} else {
				u, err := presignFor(s, hostID, "GET", keyOf(x), map[string]string{"response-content-type": x.MediaType})
				if err != nil {
					return contract.ArtifactView{}, err
				}
				v.Url = &u
			}
		}
	}
	return v, nil
}

type listFilter struct {
	ExecutionID, TaskID, MediaType, ProjectID, Q *string
	ProjectIDs, ExcludeProjectIDs                []*string
	States                                       []string
	Since                                        *float64
	Limit, Before                                *int64
	NoExamples                                   bool
}

// splitNull: project ids and whether null (work outside projects) is among them.
func splitNull(xs []*string) ([]string, bool) {
	named, null := []string{}, false
	for _, x := range xs {
		if x == nil {
			null = true
		} else {
			named = append(named, *x)
		}
	}
	return named, null
}

func artListParams(f listFilter) (db.ArtListParams, bool) {
	p := db.ArtListParams{ExecutionID: f.ExecutionID, TaskID: f.TaskID, ProjectID: f.ProjectID, NoExamples: f.NoExamples}
	paged := false
	if f.MediaType != nil {
		if strings.HasSuffix(*f.MediaType, "/") {
			like := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
			p.MediaLike = ptr(like.Replace(*f.MediaType) + "%")
		} else {
			p.MediaType = f.MediaType
		}
	}
	if len(f.ProjectIDs) > 0 {
		p.ProjectIds, p.ProjectIdsNull = splitNull(f.ProjectIDs)
	}
	if len(f.ExcludeProjectIDs) > 0 { // the person's ignored projects (OR-34); '-' or null is no project
		ids, null := splitNull(f.ExcludeProjectIDs)
		named := slices.DeleteFunc(slices.Clone(ids), func(x string) bool { return x == "-" })
		if len(named) > 0 {
			p.ExcludeIds = named
		}
		p.ExcludeNull = null || len(named) < len(ids)
	}
	if len(f.States) > 0 {
		p.States = f.States
	}
	if f.Since != nil {
		p.Since = pgtype.Timestamptz{Time: time.UnixMilli(int64(*f.Since)), Valid: true}
	}
	if f.Q != nil && *f.Q != "" { // the list search syntax (search.go searchWhere): name words, project and state; results have no agent or host
		s := parseSearchQuery(*f.Q)
		for _, w := range strings.Fields(s.text) {
			p.Words = append(p.Words, likePattern(w))
		}
		for k, v := range s.fields {
			switch k {
			case "project":
				p.ProjectLike = ptr(likePattern(v))
			case "state":
				p.SearchState = ptr(strings.ToLower(v))
			default:
				p.Never = true
			}
		}
	}
	if f.Limit != nil && *f.Limit > 0 {
		paged, p.RowLimit, p.Before = true, ptr(*f.Limit+1), f.Before
	}
	return p, paged
}

// artifactList: mediaType ending in '/' matches a family, newest-first pages by seq when limit is given (one row more
// than the page, so the caller knows another follows).
func artifactList(ctx context.Context, a *App, f listFilter) ([]contract.ArtifactRow, error) {
	p, paged := artListParams(f)
	rows, err := connDB(ctx, a).ArtList(ctx, p)
	if err != nil {
		return nil, err
	}
	if !paged { // oldest first
		slices.Reverse(rows)
	}
	out := make([]contract.ArtifactRow, 0, len(rows))
	for _, r := range rows {
		x := artifactOf(r)
		out = append(out, x.row())
	}
	return out, nil
}

// artifactWorkFacts: the page's work facts (controls/work-facts.md): the harness of the run that made each result and
// its task's executions so far, two indexed lookups per page. Only the operators' list (opArtifactList) reads them.
func artifactWorkFacts(ctx context.Context, a *App, rows []contract.ArtifactRow) error {
	var runs, tasks []string
	for _, r := range rows {
		if r.ExecutionId != nil {
			runs = append(runs, *r.ExecutionId)
		}
		if r.TaskId != nil {
			tasks = append(tasks, *r.TaskId)
		}
	}
	type facts struct {
		harness, variant *string
		attempts         int64
	}
	byRun, byTask := map[string]facts{}, map[string]int64{}
	if len(runs) > 0 {
		rs, err := connDB(ctx, a).ArtWorkFacts(ctx, runs)
		if err != nil {
			return err
		}
		for _, r := range rs {
			byRun[r.ID] = facts{r.Harness, r.Variant, r.TaskAttempts}
		}
	}
	if len(tasks) > 0 {
		ts, err := connDB(ctx, a).ArtTaskAttempts(ctx, tasks)
		if err != nil {
			return err
		}
		for _, t := range ts {
			byTask[t.TaskID] = t.TaskAttempts
		}
	}
	for i := range rows {
		r := &rows[i]
		var n int64
		if r.ExecutionId != nil {
			f := byRun[*r.ExecutionId]
			r.Harness, r.Variant, n = f.harness, f.variant, f.attempts
		}
		if r.TaskId != nil && n == 0 {
			n = byTask[*r.TaskId] // the run is gone or hidden: the task still counts its attempts
		}
		if r.TaskId != nil && n > 0 {
			r.TaskAttempts = ptr(n)
		}
	}
	return nil
}

// artifactCounts: rows per state matching f without states, before or limit.
func artifactCounts(ctx context.Context, a *App, f listFilter) (map[string]int64, error) {
	p, _ := artListParams(f)
	rows, err := connDB(ctx, a).ArtStateCounts(ctx, db.ArtStateCountsParams{
		ExecutionID: p.ExecutionID, TaskID: p.TaskID, ProjectID: p.ProjectID,
		MediaType: p.MediaType, MediaLike: p.MediaLike,
		ProjectIds: p.ProjectIds, ProjectIdsNull: p.ProjectIdsNull,
		ExcludeIds: p.ExcludeIds, ExcludeNull: p.ExcludeNull,
		Since: p.Since, Words: p.Words, ProjectLike: p.ProjectLike,
		SearchState: p.SearchState, Never: p.Never, NoExamples: p.NoExamples,
	})
	if err != nil {
		return nil, err
	}
	counts := zeroCounts(contract.Vocabulary.Lists["results"].States)
	for _, r := range rows {
		counts[r.State] += r.N
	}
	return counts, nil
}

// artifactPurge (operator): every store, now where reachable, pending where not; the hash is refused from then on.
func artifactPurge(ctx context.Context, a *App, hash string, projectID, scope *string, reason string) (contract.ArtifactPurgeReply, error) {
	var none contract.ArtifactPurgeReply
	if !shaPattern.MatchString(hash) {
		return none, fail(400, "Expected sha256")
	}
	if reason == "" {
		return none, fail(400, "Expected reason")
	}
	reason = truncate(reason, 500)
	d := connDB(ctx, a)
	// projectId "-" is no project (as in the list ops): only the project-less copies of the content. Omitted, the purge
	// takes the content in every project, so a null must never stand for "no project" (2026-11-26: a purge of
	// project-less pipeline files also deleted the same page images in project dam).
	noProject := projectID != nil && *projectID == "-"
	if noProject {
		projectID = nil
	}
	found, err := d.ArtOfContent(ctx, db.ArtOfContentParams{ContentHash: hash, ProjectID: projectID, Scope: scope})
	if err != nil {
		return none, err
	}
	rows := artRows(found)
	if noProject {
		kept := rows[:0]
		for _, x := range rows {
			if x.ProjectID == nil {
				kept = append(kept, x)
			}
		}
		rows = kept
	}
	all, err := storeList(ctx, a)
	if err != nil {
		return none, err
	}
	deleted, pending := 0, 0
	for i := range rows {
		x := &rows[i]
		if err := artWrite(ctx, a, func(d *db.Queries) error {
			return d.ArtPurgedInsert(ctx, db.ArtPurgedInsertParams{ProjectID: projectKey(x.ProjectID), Scope: x.Scope, ContentHash: hash, Reason: reason})
		}); err != nil {
			return none, err
		}
		cs, err := d.ArtCopiesLive(ctx, x.ID)
		if err != nil {
			return none, err
		}
		for _, c := range cs {
			var s *StoreRow
			for j := range all {
				if all[j].ID == c.StoreID {
					s = &all[j]
				}
			}
			state := "purge_pending"
			if s != nil && s.State != "unreachable" {
				if t, err := tunnel(s); err == nil {
					t.Delete(s.Bucket, keyOf(x))
					if k := deref(c.StagingKey); k != "" {
						t.Delete(s.Bucket, k)
					}
				}
				state = "deleted"
				deleted++
			} else {
				pending++
			}
			if err := artWrite(ctx, a, func(d *db.Queries) error {
				return artifactCopyTransition(ctx, d, x.ID, c.StoreID, artifactCopyEvent{To: state})
			}); err != nil {
				return none, err
			}
		}
		if err := artWrite(ctx, a, func(d *db.Queries) error {
			return d.ArtMarkDeleted(ctx, db.ArtMarkDeletedParams{ID: x.ID, DeletedBy: ptr("purge:" + reason)})
		}); err != nil {
			return none, err
		}
	}
	return contract.ArtifactPurgeReply{Artifacts: int64(len(rows)), Deleted: int64(deleted), Pending: int64(pending)}, nil
}

// expireLeases: reservations whose lease ran out are aborted and fail.
func expireLeases(ctx context.Context, a *App) error {
	stale, err := connDB(ctx, a).ArtLeasesExpired(ctx)
	if err != nil {
		return err
	}
	all, _ := storeList(ctx, a)
	for _, c := range stale {
		s := storeIn(all, c.StoreID)
		x, err := artifactRow(ctx, a, c.ArtifactID)
		if err != nil {
			continue
		}
		if up := deref(c.UploadID); up != "" && s.Bucket != "" && s.State != "unreachable" {
			if t, err := tunnel(s); err == nil {
				t.AbortMultipartUpload(s.Bucket, deref(c.StagingKey), up)
			}
		}
		if err := reject(ctx, a, x, s, deref(c.StagingKey), "upload lease expired"); err != nil {
			return err
		}
	}
	return nil
}

func glob(p string) *regexp.Regexp {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c == '*' && i+1 < len(p) && p[i+1] == '*':
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return regexp.MustCompile("^" + b.String() + "$")
}

// ArtifactRequirement: required artifacts of an execution (P3-D3) for the completion gate (receipts area): the task's
// completion_requirements.artifacts (names or globs) plus artifacts saved or declared required; the outputs a completed
// receipt declared are waited for while they upload.
type ArtifactRequirement struct {
	Pending        []string          `json:"pending"`
	Failed         []artifactFailure `json:"failed"`
	Missing        []string          `json:"missing"`
	Refused        []artifactFailure `json:"refused,omitempty"` // a refused save fails the execution, required or not
	storedRequired []string
}

// artifactFailure: a required artifact that failed, with its reason.
type artifactFailure struct {
	Name  string  `json:"name"`
	Error *string `json:"error"`
}

func (r *ArtifactRequirement) stillUploading() bool {
	return len(r.Pending) > 0 && len(r.Failed) == 0 && len(r.Missing) == 0 && len(r.Refused) == 0
}

// refusedError: the execution error of a refused save (docs/designs/fleet-data-foundation.md, Refused save), nil
// without one.
func (r *ArtifactRequirement) refusedError() *executionArtifactError {
	if len(r.Refused) == 0 {
		return nil
	}
	parts := []string{}
	for _, f := range r.Refused {
		parts = append(parts, f.Name+": "+deref(f.Error))
	}
	return &executionArtifactError{Code: "artifact-refused", Message: "upload refused: " + strings.Join(parts, "; "), Missing: r.Missing, Failed: r.Refused}
}

func artifactRequirement(ctx context.Context, a *App, executionID string, declared []string) (*ArtifactRequirement, error) {
	e, err := execGet(ctx, a, executionID)
	if err != nil {
		return nil, err
	}
	named := declaredRequired(e)
	patterns := []string{}
	d := connDB(ctx, a)
	if e.TaskID != nil {
		raw, _ := d.ArtTaskArtifactRules(ctx, *e.TaskID)
		patterns = jsonStrings(raw)
	}
	found, err := d.ArtOfExecution(ctx, db.ArtOfExecutionParams{ExecutionID: &executionID})
	if err != nil {
		return nil, err
	}
	rows := artRows(found)
	globs := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		globs[i] = glob(p)
	}
	match := func(name string) bool {
		for _, g := range globs {
			if g.MatchString(name) {
				return true
			}
		}
		return false
	}
	have := map[string]bool{}
	for _, r := range rows {
		have[r.Name] = true
	}
	isDiagnostic := diagnosticFile
	out := &ArtifactRequirement{Pending: []string{}, Failed: []artifactFailure{}, Missing: []string{}, Refused: e.Refused}
	for _, f := range e.Refused {
		have[f.Name] = true // refused: neither pending nor missing
	}
	for _, r := range rows {
		if r.Required || contains(named, r.Name) || match(r.Name) {
			if r.State == "stored" {
				if r.Role == "original" && !isDiagnostic(r.Name) {
					out.storedRequired = append(out.storedRequired, firstString(deref(r.LegacyID), r.ID))
				}
			} else if r.State == "pending" {
				out.Pending = append(out.Pending, r.Name)
			} else if r.State == "failed" {
				out.Failed = append(out.Failed, artifactFailure{Name: r.Name, Error: r.LastError})
			}
		} else if contains(declared, r.Name) && r.State == "pending" && !isDiagnostic(r.Name) {
			// An output the completed receipt declared is still uploading: the run is not done until it is stored
			// (a failed optional output does not fail the run). Lab script steps read output.json too early otherwise.
			out.Pending = append(out.Pending, r.Name)
		}
	}
	seen := map[string]bool{}
	unreserved := []string{}
	for _, n := range append(append([]string{}, declared...), named...) {
		// A declared output not reserved yet is pending too: the receipt arrives before the executor's uploads start.
		// Diagnostic mcp-lifecycle-* files never block completion unless explicitly required.
		if (contains(named, n) || match(n) || (contains(declared, n) && !isDiagnostic(n))) && !have[n] && !seen[n] {
			seen[n] = true
			unreserved = append(unreserved, n)
		}
	}
	out.Pending = append(out.Pending, unreserved...)
	present := append(append([]string{}, unreserved...), keys(have)...)
	for i, p := range patterns {
		g, found := globs[i], false
		for _, n := range present {
			if g.MatchString(n) {
				found = true
			}
		}
		if !found {
			out.Missing = append(out.Missing, p)
		}
	}
	return out, nil
}

func keys(m map[string]bool) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	return out
}

// promote copies a verified staging object to its content key when that key is absent (a verified object is never
// overwritten); above copyLimit as a multipart copy.
func promote(t *S3, bucket, staging, final string, size int64) error {
	n, err := t.Head(bucket, final)
	if err != nil || n >= 0 {
		return err
	}
	src := bucket + "/" + staging
	if size <= copyLimit {
		return t.Copy(bucket, final, src)
	}
	up, err := t.CreateMultipartUpload(bucket, final, "")
	if err != nil {
		return err
	}
	done := []S3Part{}
	for i, k := int64(0), 1; i < size; i, k = i+copyPart, k+1 {
		etag, err := t.UploadPartCopy(bucket, final, up, k, src, fmt.Sprintf("bytes=%d-%d", i, min(size, i+copyPart)-1))
		if err != nil {
			return err
		}
		done = append(done, S3Part{k, etag})
	}
	return t.CompleteMultipartUpload(bucket, final, up, done)
}

// writeObject: an object the coordinator writes itself, straight to its content key when absent.
func writeObject(ctx context.Context, a *App, projectID *string, scope string, data []byte, media string) (*StoreRow, string, error) {
	target, err := selectStore(ctx, a, artifactLocations(a, projectID), "")
	if err != nil {
		return nil, "", err
	}
	if target == nil {
		return nil, "", failCode(409, "No reachable store in the project locations", "no_store")
	}
	hash := sha256hex(data)
	key := keyOf(&Artifact{ProjectID: projectID, Scope: scope, ContentHash: hash})
	// In the caller's transaction, the object lock holds until its copy row commits: a purge of this content either
	// finished before the Head below (the object is written again) or waits and then sees the copy (cleanup.go).
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		if err := objectLock(ctx, tx, target.ID, key); err != nil {
			return nil, "", err
		}
	}
	t, err := tunnel(target)
	if err != nil {
		return nil, "", err
	}
	if n, err := t.Head(target.Bucket, key); err != nil || n < 0 {
		if data == nil {
			data = []byte{}
		}
		if err := t.Put(target.Bucket, key, data, media); err != nil {
			return nil, "", err
		}
	}
	return target, hash, nil
}

// outputSummary: the one-line summary an output.json carries at its top level (a pipeline's result names what it holds,
// such as doc_recognize.py's "KATARZYNA NOWAK · 5 files · 1 unreadable"); nil for anything else. Kept to 200 characters.
func outputSummary(name string, data []byte) *string {
	if name != "output.json" || len(data) > 4<<20 {
		return nil
	}
	var o struct {
		Summary json.RawMessage `json:"summary"`
	}
	if json.Unmarshal(data, &o) != nil {
		return nil
	}
	s, ok := jsonString(o.Summary)
	if s = strings.TrimSpace(strings.Join(strings.Fields(s), " ")); !ok || s == "" {
		return nil
	}
	if r := []rune(s); len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return &s
}

// saveObject: an output the coordinator writes for an execution (pipeline step output.json, example inputs), stored and
// verified at once.
func saveObject(ctx context.Context, a *App, projectID *string, scope, executionID, name string, data []byte, media string) (string, string, error) {
	// The object is written in the row transaction under its object lock (writeObject), so a purge of the same content
	// either finished before (the object is written again) or sees this copy.
	id, hash := "", ""
	write := func(tx pgx.Tx) error {
		target, h, err := writeObject(context.WithValue(ctx, txKey{}, tx), a, projectID, scope, data, media)
		if err != nil {
			return err
		}
		id, hash = newID(), h
		d := db.New(tx)
		if err := d.ArtInsertStored(ctx, db.ArtInsertStoredParams{ID: id, ProjectID: projectID, Scope: scope, ContentHash: hash, Size: int64(len(data)), MediaType: media,
			Name: name, ExecutionID: &executionID, Summary: outputSummary(name, data)}); err != nil {
			return err
		}
		return artifactCopyInsertVerified(ctx, d, id, target.ID, projectID)
	}
	var err error
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		err = write(tx)
	} else {
		// No GLOBAL lock: two new rows; the object lock (writeObject) is what serializes this write with a purge, so
		// the Head under it does not hold every other write.
		err = a.store.Write(ctx, nil, nil, func(q *Request) error { return write(q.tx) })
	}
	return id, hash, err
}

// executionArtifacts and readArtifact: the artifact records of an execution (stored originals, in the record shape the old
// readers used) and its bounded bytes.

func readArtifact(a *App, id string) ([]byte, error) {
	return artifactBytes(context.Background(), a, id, 128000)
}

// executionArtifact: one stored original of an execution in the record shape the old readers used.
type executionArtifact struct {
	ID          string  `json:"id"`
	ArtifactID  string  `json:"artifactId"`
	ExecutionID *string `json:"executionId"`
	Path        string  `json:"path"`
	MediaType   string  `json:"mediaType"`
	Size        int64   `json:"size"`
	Sha256      string  `json:"sha256"`
}

func executionArtifacts(q *Request, executionID string) ([]executionArtifact, error) {
	ctx := withTx(q)
	rows, err := connDB(ctx, q.app).ArtOfExecution(ctx, db.ArtOfExecutionParams{ExecutionID: &executionID, StoredOnly: true})
	if err != nil {
		return nil, err
	}
	out := []executionArtifact{}
	for _, x := range rows {
		id := x.ID
		if x.LegacyID != nil {
			id = *x.LegacyID
		}
		out = append(out, executionArtifact{ID: id, ArtifactID: x.ID, ExecutionID: x.ExecutionID, Path: x.Name, MediaType: x.MediaType, Size: x.Size, Sha256: x.ContentHash})
	}
	return out, nil
}

// refusedReservation records a reservation the coordinator refused on the uploading host's execution
// (refusedArtifacts) and settles a waiting execution; the execution fails with the reason (refusedError). A failure that
// was no refusal (an older host, or a transfer that broke before reserve) is recorded the same way: no row means no
// upload, and waiting a day for it helps nobody.
func refusedReservation(q *Request, b *contract.ArtifactFailedRequest) (any, error) {
	a, ctx, id := q.app, q.ctx(), *b.ExecutionId
	why := truncate(firstString(b.Error, "unknown reason"), 500)
	err := a.store.Write(ctx, nil, []string{GLOBAL, DISPATCH}, func(w *Request) error {
		if err := lockExecutionTask(w, id); err != nil {
			return err
		}
		e, err := executionRawMaybe(w, id)
		if err != nil {
			return err
		}
		if e == nil || e.text("hostId") != b.HostId {
			return fail(403, "Artifact not uploaded by this host")
		}
		var refused []artifactFailure
		json.Unmarshal(e["refusedArtifacts"], &refused)
		for _, f := range refused {
			if f.Name == *b.Name {
				return nil
			}
		}
		e["refusedArtifacts"] = jsonBytes(append(refused, artifactFailure{Name: *b.Name, Error: &why}))
		return executionPut(w, &e)
	})
	if err != nil {
		return nil, err
	}
	if err := settleWaiting(ctx, a, id); err != nil {
		return nil, err
	}
	return contract.ArtifactStateReply{State: "failed"}, nil
}
