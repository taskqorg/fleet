package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
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

// An operation already runs in the dispatcher's transaction (in the context, see withTx); a long route has none and takes
// short ones of its own.
type txKey struct{}

func withTx(q *Request) context.Context {
	if q.tx == nil {
		return q.ctx()
	}
	return context.WithValue(q.ctx(), txKey{}, q.tx)
}

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func conn(ctx context.Context, a *App) querier {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return tx
	}
	return a.store.pool
}

func txCtx(ctx context.Context, a *App, fn func(pgx.Tx) error) error {
	if tx, ok := ctx.Value(txKey{}).(pgx.Tx); ok {
		return fn(tx)
	}
	return a.store.Tx(ctx, nil, true, "", fn)
}

func rowsCtx(ctx context.Context, db querier, sql string, args ...any) ([]map[string]any, error) {
	rs, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rs, pgx.RowToMap)
	if out == nil {
		out = []map[string]any{}
	}
	return out, err
}

func oneCtx(ctx context.Context, a *App, sql string, args ...any) (map[string]any, error) {
	r, err := rowsCtx(ctx, conn(ctx, a), sql, args...)
	if err != nil || len(r) == 0 {
		return nil, err
	}
	return r[0], nil
}

func execCtx(ctx context.Context, a *App, sql string, args ...any) error {
	return txCtx(ctx, a, func(tx pgx.Tx) error { _, err := tx.Exec(ctx, sql, args...); return err })
}

func i64(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case float64:
		return int64(v)
	}
	return 0
}
func strp(m map[string]any, k string) *string {
	if s, ok := m[k].(string); ok {
		return &s
	}
	return nil
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
	rs, err := conn(ctx, a).Query(ctx, "SELECT id,host_id,bucket,endpoint,local_endpoint,state,capacity_bytes,used_bytes,free_space_warning_bytes,requires_logon,zone,holder FROM store ORDER BY id")
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rs, func(r pgx.CollectableRow) (StoreRow, error) {
		var s StoreRow
		err := r.Scan(&s.ID, &s.HostID, &s.Bucket, &s.Endpoint, &s.LocalEndpoint, &s.State, &s.CapacityBytes, &s.UsedBytes, &s.FreeSpaceWarningBytes, &s.RequiresLogon, &s.Zone, &s.Holder)
		return s, err
	})
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

func checkURL(v any, name string) (string, error) {
	s, _ := v.(string)
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
func storeRegister(q *Request, hostID string, b map[string]any, holder string) (any, error) {
	a := q.app
	id, _ := b["id"].(string)
	bucket, _ := b["bucket"].(string)
	if !storeIDPattern.MatchString(id) {
		return nil, fail(400, "Invalid store id")
	}
	if !storeIDPattern.MatchString(bucket) {
		return nil, fail(400, "Invalid bucket")
	}
	key, _ := b["accessKeyId"].(string)
	secret, _ := b["secretAccessKey"].(string)
	if key == "" || secret == "" {
		return nil, fail(400, "Expected store credentials")
	}
	endpoint, err := checkURL(b["endpoint"], "endpoint")
	if err != nil {
		return nil, err
	}
	var local *string
	if b["localEndpoint"] != nil && b["localEndpoint"] != "" {
		l, err := checkURL(b["localEndpoint"], "localEndpoint")
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
	if r, err := oneCtx(ctx, a, "SELECT host_id FROM store WHERE id=$1", id); err != nil {
		return nil, err
	} else if r != nil && astr(r["host_id"]) != hostID {
		return nil, fail(403, "Store belongs to another host")
	}
	intOrNil := func(v any) any {
		if f, ok := v.(float64); ok && f == float64(int64(f)) {
			return int64(f)
		}
		return nil
	}
	zone, _ := b["zone"].(string)
	var zonep any
	if zone != "" {
		zonep = zone
	}
	err = execCtx(ctx, a, `INSERT INTO store(id,host_id,zone,bucket,endpoint,local_endpoint,capacity_bytes,used_bytes,requires_logon,state,observed_at,holder) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'reachable',now(),$10)
   ON CONFLICT(id) DO UPDATE SET zone=excluded.zone,bucket=excluded.bucket,endpoint=excluded.endpoint,local_endpoint=excluded.local_endpoint,capacity_bytes=excluded.capacity_bytes,used_bytes=excluded.used_bytes,requires_logon=excluded.requires_logon,state='reachable',observed_at=now(),registered_at=now(),holder=excluded.holder WHERE store.host_id=excluded.host_id`,
		id, hostID, zonep, bucket, endpoint, local, intOrNil(b["capacityBytes"]), intOrNil(b["usedBytes"]), b["requiresLogon"] == true, holder)
	if err != nil {
		return nil, err
	}
	// The upsert's WHERE makes ownership part of the write: a store of another host is unchanged, and so is its key.
	if r, err := oneCtx(ctx, a, "SELECT host_id FROM store WHERE id=$1", id); err != nil {
		return nil, err
	} else if r == nil || astr(r["host_id"]) != hostID {
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
	return doc{"ok": true, "storeId": id}, nil
}

// storesObserve: the poll report (the poll route calls it in its own transaction): fresh capacity left and used bytes; a
// store is full when the capacity left falls below its free_space_warning.
func storesObserve(ctx context.Context, tx pgx.Tx, hostID string, stores any) error {
	list, _ := stores.([]any)
	for _, x := range list {
		s, _ := x.(map[string]any)
		id, ok := s["id"].(string)
		if !ok {
			continue
		}
		num := func(k string) any {
			if f, ok := s[k].(float64); ok && f == float64(int64(f)) {
				return int64(f)
			}
			return nil
		}
		if _, err := tx.Exec(ctx, "UPDATE store SET state=CASE WHEN NOT $5 THEN 'unreachable' WHEN free_space_warning_bytes IS NOT NULL AND coalesce($3,capacity_bytes) < free_space_warning_bytes THEN 'full' ELSE 'reachable' END,capacity_bytes=coalesce($3,capacity_bytes),used_bytes=coalesce($4,used_bytes),observed_at=CASE WHEN $5 THEN now() ELSE observed_at END WHERE id=$1 AND host_id=$2",
			id, hostID, num("capacityBytes"), num("usedBytes"), s["reachable"] == true); err != nil {
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
	Size                                                                       int64
	Required                                                                   bool
	Seq                                                                        int64
	Created                                                                    time.Time
}

const artifactCols = "id,project_id,scope,content_hash,size,media_type,name,execution_id,task_id,required,role,derived_from_artifact_id,previous_artifact_id,state,last_error,legacy_id,seq,created_at"

func scanArtifact(r pgx.CollectableRow) (Artifact, error) {
	var x Artifact
	err := r.Scan(&x.ID, &x.ProjectID, &x.Scope, &x.ContentHash, &x.Size, &x.MediaType, &x.Name, &x.ExecutionID, &x.TaskID, &x.Required, &x.Role, &x.DerivedFrom, &x.Previous, &x.State, &x.LastError, &x.LegacyID, &x.Seq, &x.Created)
	return x, err
}

func artifacts(ctx context.Context, a *App, where string, args ...any) ([]Artifact, error) {
	rs, err := conn(ctx, a).Query(ctx, "SELECT "+artifactCols+" FROM artifact "+where, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rs, scanArtifact)
}

func artifactRow(ctx context.Context, a *App, id string) (*Artifact, error) {
	r, err := artifacts(ctx, a, "WHERE id=$1 OR legacy_id=$1", id)
	if err != nil {
		return nil, err
	}
	if len(r) == 0 {
		return nil, fail(404, "Unknown artifact")
	}
	return &r[0], nil
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

func (x *Artifact) view() doc {
	return doc{"id": x.ID, "legacyId": x.LegacyID, "projectId": x.ProjectID, "scope": x.Scope, "executionId": x.ExecutionID, "taskId": x.TaskID,
		"name": x.Name, "mediaType": x.MediaType, "size": x.Size, "sha256": x.ContentHash, "required": x.Required, "role": x.Role,
		"derivedFromArtifactId": x.DerivedFrom, "previousArtifactId": x.Previous, "state": x.State, "error": x.LastError}
}

var shaPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func checkName(v any) (string, error) {
	s, _ := v.(string)
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
	Data                          map[string]any
}

func execGet(ctx context.Context, a *App, id string) (*execInfo, error) {
	var e execInfo
	var data []byte
	err := conn(ctx, a).QueryRow(ctx, "SELECT id,coalesce(host_id,data->>'hostId',''),coalesce(project_id,data->>'projectId'),coalesce(task_id,data->>'taskId'),coalesce(experiment_id,data->>'experimentId'),data FROM execution WHERE id=$1", id).
		Scan(&e.ID, &e.HostID, &e.ProjectID, &e.TaskID, &e.Experiment, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fail(404, "Unknown execution "+id)
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal(data, &e.Data)
	return &e, nil
}

func declaredRequired(e *execInfo) []string {
	out := []string{}
	if e == nil {
		return out
	}
	l, _ := e.Data["requiredArtifacts"].([]any)
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// selectStore: the writer's host when listed, else the first listed reachable store.
func selectStore(ctx context.Context, a *App, locations []string, hostID string) (*StoreRow, error) {
	all, err := storeList(ctx, a)
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

func failCode(code int, msg, c string) error { return &codedError{HTTPError{code, msg}, c} }

type codedError struct {
	HTTPError
	code string
}

func (e *codedError) Unwrap() error { return &e.HTTPError }

var artifactIDPattern = regexp.MustCompile(`^[a-zA-Z0-9-]{8,80}$`)

type reserveBody struct {
	ExecutionID, HostID, Name, MediaType, Sha256, ArtifactID, DerivedFrom, Role, PreviousArtifactID string
	Size                                                                                            *float64
	Required                                                                                        bool
}

func artifactReserve(q *Request) (any, error) {
	a, ctx := q.app, q.ctx()
	var b map[string]any
	decode(q.body, &b)
	sv := func(k string) string { s, _ := b[k].(string); return s }
	// The execution is required before anything is read: hostOwned checks its host only when it is named.
	// The execution hostOwned verified: decoded the same way (struct, same matching rules), never read from the map.
	var eb struct {
		ExecutionID string `json:"executionId"`
	}
	decode(q.body, &eb)
	if eb.ExecutionID == "" {
		return nil, fail(400, "Expected executionId")
	}
	id := sv("artifactId")
	if !artifactIDPattern.MatchString(id) {
		id = newID()
	}
	if old, err := oneCtx(ctx, a, "SELECT state,execution_id FROM artifact WHERE id=$1", id); err != nil {
		return nil, err
	} else if old != nil {
		// A retry of the same reservation only: another execution's artifact id is refused, not described.
		if astr(old["execution_id"]) != eb.ExecutionID {
			return nil, fail(409, "Artifact id in use")
		}
		c, _ := oneCtx(ctx, a, "SELECT store_id FROM artifact_copy WHERE artifact_id=$1 AND state IN ('pending','copying','verified') ORDER BY state LIMIT 1", id)
		out := doc{"artifactId": id, "state": old["state"], "partSize": partSize}
		if c != nil {
			out["storeId"] = c["store_id"]
		}
		return out, nil
	}
	sz, ok := b["size"].(float64)
	if !ok || sz < 0 || sz != float64(int64(sz)) {
		return nil, fail(400, "Expected size")
	}
	size := int64(sz)
	hash := sv("sha256")
	if !shaPattern.MatchString(hash) {
		return nil, fail(400, "Expected sha256")
	}
	file, err := checkName(b["name"])
	if err != nil {
		return nil, err
	}
	e, err := execGet(ctx, a, eb.ExecutionID)
	if err != nil {
		return nil, err
	}
	scope := "prod"
	if e.Experiment != nil {
		scope = "lab"
	}
	if r, err := oneCtx(ctx, a, "SELECT 1 AS x FROM purged_hash WHERE project_id=$1 AND scope=$2 AND content_hash=$3", projectKey(e.ProjectID), scope, hash); err != nil {
		return nil, err
	} else if r != nil {
		return nil, failCode(409, "Content was purged", "purged")
	}
	target, err := selectStore(ctx, a, artifactLocations(a, e.ProjectID), q.hostID())
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, failCode(409, "No reachable store in the project locations", "no_store")
	}
	media := sv("mediaType")
	if media == "" || len(media) >= 200 {
		media = "application/octet-stream"
	}
	required := b["required"] == true || contains(declaredRequired(e), file)
	role := sv("role")
	if role != "thumbnail" && role != "poster" {
		role = "original"
	}
	nilIf := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	insert := func(tx pgx.Tx, state string) error {
		_, err := tx.Exec(ctx, "INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,task_id,required,role,derived_from_artifact_id,previous_artifact_id,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)",
			id, e.ProjectID, scope, hash, size, media, file, e.ID, e.TaskID, required, role, nilIf(sv("derivedFrom")), nilIf(sv("previousArtifactId")), state)
		return err
	}
	same, err := oneCtx(ctx, a, "SELECT 1 AS x FROM artifact a JOIN artifact_copy c ON c.artifact_id=a.id WHERE a.project_id IS NOT DISTINCT FROM $1 AND a.scope=$2 AND a.content_hash=$3 AND c.store_id=$4 AND c.state='verified' LIMIT 1", e.ProjectID, scope, hash, target.ID)
	if err != nil {
		return nil, err
	}
	at := target
	// An empty file has nothing to upload (stores refuse an empty multipart part): the coordinator writes it itself.
	if same == nil && size == 0 {
		if hash != emptySHA256 {
			return nil, fail(400, "Empty file with a non-empty hash")
		}
		w, _, err := writeObject(ctx, a, e.ProjectID, scope, nil, media)
		if err != nil {
			return nil, err
		}
		at = w
	}
	if same != nil || size == 0 {
		err := txCtx(ctx, a, func(tx pgx.Tx) error {
			if err := insert(tx, "stored"); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,verified_at) VALUES($1,$2,$3,'verified',now())", id, at.ID, e.ProjectID)
			return err
		})
		if err != nil {
			return nil, err
		}
		if err := settleWaiting(ctx, a, e.ID); err != nil {
			return nil, err
		}
		return doc{"artifactId": id, "state": "stored", "storeId": at.ID}, nil
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
		if err := insert(tx, "pending"); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,upload_id,staging_key,lease_expires_at) VALUES($1,$2,$3,'pending',$4,$5,now()+($6::text||' milliseconds')::interval)", id, target.ID, e.ProjectID, upload, staging, strconv.Itoa(leaseMS))
		return err
	})
	if err != nil {
		return nil, err
	}
	return doc{"artifactId": id, "state": "pending", "storeId": target.ID, "partSize": partSize}, nil
}

func (q *Request) hostID() string { return q.host["hostId"].(string) }

type copyRow struct {
	StoreID, State       string
	UploadID, StagingKey *string
}

func copyOf(ctx context.Context, a *App, where string, args ...any) (*copyRow, error) {
	var c copyRow
	err := conn(ctx, a).QueryRow(ctx, "SELECT store_id,state,upload_id,staging_key FROM artifact_copy WHERE "+where+" LIMIT 1", args...).Scan(&c.StoreID, &c.State, &c.UploadID, &c.StagingKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &c, err
}

// working: the copy an upload works on (the given store, else the one in progress); a committed upload (upload id cleared,
// staging complete) counts when committed is set.
func working(ctx context.Context, a *App, artifactID, storeID string, committed bool) (*copyRow, error) {
	var c *copyRow
	var err error
	if storeID != "" {
		c, err = copyOf(ctx, a, "artifact_id=$1 AND store_id=$2", artifactID, storeID)
	} else {
		c, err = copyOf(ctx, a, "artifact_id=$1 AND state IN ('pending','copying')", artifactID)
	}
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

type uploadBody struct {
	ArtifactID  string    `json:"artifactId"`
	StoreID     string    `json:"storeId"`
	HostID      string    `json:"hostId"`
	PartNumbers []float64 `json:"partNumbers"`
	Parts       []S3Part  `json:"parts"`
	Sha256      string    `json:"sha256"`
	Error       *string   `json:"error"`
	ExecutionID string    `json:"executionId"`
	Name        any       `json:"name"`
}

func uploadArgs(q *Request) (*App, context.Context, uploadBody, *Artifact, error) {
	a, ctx := q.app, q.ctx()
	var b uploadBody
	if err := decode(q.body, &b); err != nil {
		return a, ctx, b, nil, err
	}
	x, err := artifactRow(ctx, a, b.ArtifactID)
	return a, ctx, b, x, err
}

func lease(ctx context.Context, a *App, artifactID, storeID, set string) error {
	return execCtx(ctx, a, "UPDATE artifact_copy SET "+set+" WHERE artifact_id=$1 AND store_id=$2", artifactID, storeID, strconv.Itoa(leaseMS))
}

func artifactParts(q *Request) (any, error) {
	a, ctx, b, x, err := uploadArgs(q)
	if err != nil {
		return nil, err
	}
	c, err := working(ctx, a, x.ID, b.StoreID, false)
	if err != nil {
		return nil, err
	}
	s, err := storeGet(ctx, a, c.StoreID)
	if err != nil {
		return nil, err
	}
	if err := allowedHost(ctx, a, x, s, b.HostID); err != nil {
		return nil, err
	}
	if b.PartNumbers == nil {
		return nil, fail(400, "Expected partNumbers")
	}
	if err := lease(ctx, a, x.ID, s.ID, "lease_expires_at=now()+($3::text||' milliseconds')::interval"); err != nil {
		return nil, err
	}
	parts := []doc{}
	for _, f := range b.PartNumbers {
		n := int(f)
		if float64(n) != f || n < 1 || n > 10000 || len(parts) == 64 {
			continue
		}
		u, err := presignFor(s, b.HostID, "PUT", *c.StagingKey, map[string]string{"partNumber": strconv.Itoa(n), "uploadId": *c.UploadID})
		if err != nil {
			return nil, err
		}
		parts = append(parts, doc{"partNumber": n, "url": u})
	}
	return doc{"storeId": s.ID, "parts": parts}, nil
}

func artifactCommit(q *Request) (any, error) {
	a, ctx, b, x, err := uploadArgs(q)
	if err != nil {
		return nil, err
	}
	if x.State == "stored" && b.StoreID == "" {
		return doc{"state": "stored"}, nil
	}
	// Idempotent: a repeated commit of a completed upload skips the completion and hands out the verification again.
	c, err := working(ctx, a, x.ID, b.StoreID, true)
	if err != nil {
		return nil, err
	}
	s, err := storeGet(ctx, a, c.StoreID)
	if err != nil {
		return nil, err
	}
	if err := allowedHost(ctx, a, x, s, b.HostID); err != nil {
		return nil, err
	}
	if b.Parts == nil {
		return nil, fail(400, "Expected parts")
	}
	sort.Slice(b.Parts, func(i, j int) bool { return b.Parts[i].PartNumber < b.Parts[j].PartNumber })
	t, err := tunnel(s)
	if err != nil {
		return nil, err
	}
	if c.UploadID != nil {
		if err := t.CompleteMultipartUpload(s.Bucket, *c.StagingKey, *c.UploadID, b.Parts); err != nil {
			return nil, err
		}
	}
	size, err := t.Head(s.Bucket, *c.StagingKey)
	if err != nil {
		return nil, err
	}
	if size != x.Size {
		got := strconv.FormatInt(size, 10)
		if size < 0 {
			got = "missing"
		}
		return doc{"state": "failed"}, reject(ctx, a, x, s, *c.StagingKey, fmt.Sprintf("size %s differs from reserved %d", got, x.Size))
	}
	if err := lease(ctx, a, x.ID, s.ID, "upload_id=NULL,lease_expires_at=now()+($3::text||' milliseconds')::interval"); err != nil {
		return nil, err
	}
	v, err := s3For(s, s.LocalEndpoint != nil)
	if err != nil {
		return nil, err
	}
	verify := doc{"artifactId": x.ID, "storeId": s.ID, "url": v.Presign("GET", s.Bucket, *c.StagingKey, urlSeconds, nil)}
	if b.HostID == s.HostID {
		return doc{"state": "verifying", "verify": verify}, nil
	}
	open, err := oneCtx(ctx, a, "SELECT 1 AS x FROM document WHERE kind='command' AND data->>'kind'='artifact-verify' AND data->>'hostId'=$1 AND data->>'status' IN ('pending','accepted','started') AND data->'payload'->>'artifactId'=$2 AND data->'payload'->>'storeId'=$3 LIMIT 1", s.HostID, x.ID, s.ID)
	if err != nil {
		return nil, err
	}
	if open == nil {
		if err := queueCommand(ctx, a, s.HostID, "artifact-verify", verify); err != nil {
			return nil, err
		}
	}
	return doc{"state": "verifying"}, nil
}

// queueCommand: a tool command for a host's executor (copy, verify), in a program-worker execution of its own.
func queueCommand(ctx context.Context, a *App, hostID, kind string, payload doc) error {
	return txCtx(ctx, a, func(tx pgx.Tx) error {
		q := &Request{r: (&http.Request{}).WithContext(ctx), tx: tx}
		sid, id := newID(), newID()
		if err := put(q, "session", doc{"id": sid, "createdAt": nowMs(), "actor": "program-worker"}); err != nil {
			return err
		}
		if err := putExecution(q, doc{"id": id, "executionId": id, "sessionId": sid, "hostId": hostID, "state": "preparing", "createdAt": nowMs(), "actor": "program-worker", "artifactId": payload["artifactId"]}); err != nil {
			return err
		}
		return put(q, "command", doc{"id": newID(), "executionId": id, "sessionId": sid, "hostId": hostID, "kind": kind, "status": "pending", "payload": payload})
	})
}

// Lease renewal by the executors holding an upload, copy or verification.
func artifactRenew(q *Request) (any, error) {
	a, ctx, b, x, err := uploadArgs(q)
	if err != nil {
		return nil, err
	}
	c, err := working(ctx, a, x.ID, b.StoreID, true)
	if err != nil {
		return doc{"state": x.State}, nil
	}
	s, err := storeGet(ctx, a, c.StoreID)
	if err != nil {
		return nil, err
	}
	if err := allowedHost(ctx, a, x, s, b.HostID); err != nil {
		return nil, err
	}
	return doc{"state": c.State}, lease(ctx, a, x.ID, s.ID, "lease_expires_at=greatest(lease_expires_at,now()+($3::text||' milliseconds')::interval)")
}

// reject: the staging object goes, the copy fails with a retry backoff (1 minute doubling, up to the retry window), the
// artifact fails when it has no verified copy.
func reject(ctx context.Context, a *App, x *Artifact, s *StoreRow, key, why string) error {
	if s.Bucket != "" && key != "" {
		if t, err := tunnel(s); err == nil {
			t.Delete(s.Bucket, key)
		}
	}
	err := txCtx(ctx, a, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE artifact_copy SET state='failed',last_error=$3,upload_id=NULL,lease_expires_at=NULL,attempts=attempts+1,next_attempt_at=now()+(least($4::float8,60000*power(2,least(attempts,30)))::text||' milliseconds')::interval WHERE artifact_id=$1 AND store_id=$2", x.ID, s.ID, why, retryWindowMS); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "UPDATE artifact SET state='failed',last_error=$2 WHERE id=$1 AND state='pending' AND NOT EXISTS (SELECT 1 FROM artifact_copy WHERE artifact_id=$1 AND state='verified')", x.ID, why)
		return err
	})
	if err != nil || x.ExecutionID == nil {
		return err
	}
	return settleWaiting(ctx, a, *x.ExecutionID)
}

// artifactRefused: the host reports the store refused an upload permanently; the artifact fails now with its reason.
func artifactRefused(q *Request) (any, error) {
	a, ctx, b, x, err := uploadArgs(q)
	if err != nil {
		return nil, err
	}
	if x.State != "pending" {
		return doc{"state": x.State}, nil
	}
	if x.ExecutionID == nil {
		return nil, fail(403, "Artifact not uploaded by this host")
	}
	if e, err := execGet(ctx, a, *x.ExecutionID); err != nil || e.HostID != b.HostID {
		return nil, fail(403, "Artifact not uploaded by this host")
	}
	why := "upload refused: unknown reason"
	if b.Error != nil {
		why = "upload refused: " + truncate(*b.Error, 500)
	}
	all, _ := storeList(ctx, a)
	cs, err := rowsCtx(ctx, conn(ctx, a), "SELECT store_id,upload_id,staging_key FROM artifact_copy WHERE artifact_id=$1 AND state IN ('pending','copying')", x.ID)
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		s := &StoreRow{ID: astr(c["store_id"])}
		for i := range all {
			if all[i].ID == s.ID {
				s = &all[i]
			}
		}
		if up := astr(c["upload_id"]); up != "" && s.Bucket != "" {
			if t, err := tunnel(s); err == nil {
				t.AbortMultipartUpload(s.Bucket, astr(c["staging_key"]), up)
			}
		}
		if err := reject(ctx, a, x, s, astr(c["staging_key"]), why); err != nil {
			return nil, err
		}
	}
	x, err = artifactRow(ctx, a, x.ID)
	if err != nil {
		return nil, err
	}
	return doc{"state": x.State}, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// artifactVerified: promotion; the staging object becomes the content-hash key only when that key is absent.
func artifactVerified(q *Request) (any, error) {
	a, ctx, b, x, err := uploadArgs(q)
	if err != nil {
		return nil, err
	}
	s, err := storeGet(ctx, a, b.StoreID)
	if err != nil {
		return nil, err
	}
	if b.HostID != s.HostID {
		return nil, fail(403, "Only the store host verifies its copies")
	}
	c, err := copyOf(ctx, a, "artifact_id=$1 AND store_id=$2", x.ID, s.ID)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return doc{"state": "unknown"}, nil
	}
	if c.State != "pending" && c.State != "copying" {
		return doc{"state": c.State}, nil
	}
	staging := ""
	if c.StagingKey != nil {
		staging = *c.StagingKey
	}
	if b.Sha256 != x.ContentHash {
		return doc{"state": "failed"}, reject(ctx, a, x, s, staging, "content hash differs from the reserved hash")
	}
	t, err := tunnel(s)
	if err != nil {
		return nil, err
	}
	final := keyOf(x)
	n, err := t.Head(s.Bucket, final)
	if err != nil {
		return nil, err
	}
	src := s.Bucket + "/" + staging
	if n < 0 {
		if x.Size <= copyLimit {
			err = t.Copy(s.Bucket, final, src)
		} else {
			up, err2 := t.CreateMultipartUpload(s.Bucket, final, "")
			if err2 != nil {
				return nil, err2
			}
			done := []S3Part{}
			for i, k := int64(0), 1; i < x.Size; i, k = i+copyPart, k+1 {
				etag, err := t.UploadPartCopy(s.Bucket, final, up, k, src, fmt.Sprintf("bytes=%d-%d", i, min(x.Size, i+copyPart)-1))
				if err != nil {
					return nil, err
				}
				done = append(done, S3Part{k, etag})
			}
			err = t.CompleteMultipartUpload(s.Bucket, final, up, done)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := t.Delete(s.Bucket, staging); err != nil {
		return nil, err
	}
	err = txCtx(ctx, a, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "UPDATE artifact_copy SET state='verified',verified_at=now(),upload_id=NULL,staging_key=NULL,lease_expires_at=NULL,last_error=NULL WHERE artifact_id=$1 AND store_id=$2", x.ID, s.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "UPDATE artifact SET state='stored',last_error=NULL WHERE id=$1 AND state='pending'", x.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if x.ExecutionID != nil {
		if err := settleWaiting(ctx, a, *x.ExecutionID); err != nil {
			return nil, err
		}
	}
	return doc{"state": "stored"}, nil
}

// artifactDeclare: an agent's required declaration (artifact_save required=true), recorded on the execution.
func artifactDeclare(q *Request) (any, error) {
	a, ctx := q.app, q.ctx()
	var b uploadBody
	if err := decode(q.body, &b); err != nil {
		return nil, err
	}
	file, err := checkName(b.Name)
	if err != nil {
		return nil, err
	}
	err = txCtx(ctx, a, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE execution SET data=jsonb_set(data,'{requiredArtifacts}',(SELECT coalesce(jsonb_agg(DISTINCT v),'[]') FROM jsonb_array_elements_text(coalesce(data->'requiredArtifacts','[]')||to_jsonb($2::text)) v)) WHERE id=$1`, b.ExecutionID, file); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "UPDATE artifact SET required=true WHERE execution_id=$1 AND name=$2", b.ExecutionID, file)
		return err
	})
	if err != nil {
		return nil, err
	}
	return doc{"ok": true}, nil
}

// source: a verified copy to read from, the reader host's own store first; a store with a purge pending serves nothing.
func source(ctx context.Context, a *App, x *Artifact, hostID string) (*StoreRow, error) {
	rs, err := rowsCtx(ctx, conn(ctx, a), "SELECT store_id FROM artifact_copy WHERE artifact_id=$1 AND state='verified' AND store_id NOT IN (SELECT store_id FROM artifact_copy WHERE state='purge_pending')", x.ID)
	if err != nil {
		return nil, err
	}
	all, err := storeList(ctx, a)
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
			if astr(r["store_id"]) == s.ID {
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

// artifactGetView: metadata and a presigned GET for the given host.
func artifactGetView(ctx context.Context, a *App, id, hostID string) (doc, error) {
	x, err := artifactRow(ctx, a, id)
	if err != nil {
		return nil, err
	}
	cs, err := rowsCtx(ctx, conn(ctx, a), "SELECT store_id AS \"storeId\",state FROM artifact_copy WHERE artifact_id=$1 ORDER BY store_id", x.ID)
	if err != nil {
		return nil, err
	}
	v := x.view()
	v["copies"] = cs
	v["availability"] = x.State
	if x.State == "stored" {
		s, err := source(ctx, a, x, hostID)
		if err != nil {
			return nil, err
		}
		v["availability"] = "unavailable"
		if s != nil {
			v["availability"] = "available"
			if v["url"], err = presignFor(s, hostID, "GET", keyOf(x), map[string]string{"response-content-type": x.MediaType}); err != nil {
				return nil, err
			}
		}
	}
	return v, nil
}

type listFilter struct {
	ExecutionID, TaskID, MediaType, ProjectID, Q *string
	ProjectIDs                                   []*string
	States                                       []string
	Since                                        *float64
	Limit, Before                                *int64
	NoExamples                                   bool
}

// artifactList: mediaType ending in '/' matches a family, newest-first pages by seq when limit is given.
func artifactList(ctx context.Context, a *App, f listFilter) ([]doc, error) {
	c, p := []string{}, []any{}
	add := func(cond string, v any) {
		p = append(p, v)
		c = append(c, strings.ReplaceAll(cond, "$?", "$"+strconv.Itoa(len(p))))
	}
	like := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	for _, kv := range []struct {
		v   *string
		col string
	}{{f.ExecutionID, "execution_id"}, {f.TaskID, "task_id"}, {f.ProjectID, "project_id"}} {
		if kv.v != nil {
			add(kv.col+"=$?", *kv.v)
		}
	}
	if f.MediaType != nil {
		if strings.HasSuffix(*f.MediaType, "/") {
			add("media_type LIKE $?", like.Replace(*f.MediaType)+"%")
		} else {
			add("media_type=$?", *f.MediaType)
		}
	}
	if len(f.ProjectIDs) > 0 {
		named, null := []string{}, ""
		for _, x := range f.ProjectIDs {
			if x == nil {
				null = " OR project_id IS NULL"
			} else {
				named = append(named, *x)
			}
		}
		add("(project_id=ANY($?)"+null+")", named)
	}
	if len(f.States) > 0 {
		add("state=ANY($?)", f.States)
	}
	if f.Since != nil {
		add("created_at>=$?", time.UnixMilli(int64(*f.Since)))
	}
	if f.Q != nil && *f.Q != "" {
		add("name ILIKE $?", "%"+like.Replace(*f.Q)+"%")
	}
	if f.NoExamples {
		c = append(c, "role<>'example'")
	}
	paged := f.Limit != nil && *f.Limit > 0
	if paged && f.Before != nil {
		add("seq<$?", *f.Before)
	}
	where := ""
	if len(c) > 0 {
		where = "WHERE " + strings.Join(c, " AND ")
	}
	order := "ORDER BY seq"
	if paged {
		order += fmt.Sprintf(" DESC LIMIT %d", *f.Limit+1)
	}
	rows, err := artifacts(ctx, a, where+" "+order, p...)
	if err != nil {
		return nil, err
	}
	out := make([]doc, 0, len(rows))
	for i := range rows {
		v := rows[i].view()
		v["seq"], v["createdAt"] = rows[i].Seq, rows[i].Created.UnixMilli()
		out = append(out, v)
	}
	return out, nil
}

// artifactPurge (operator): every store, now where reachable, pending where not; the hash is refused from then on.
func artifactPurge(ctx context.Context, a *App, hash string, projectID, scope *string, reason string) (doc, error) {
	if !shaPattern.MatchString(hash) {
		return nil, fail(400, "Expected sha256")
	}
	if reason == "" {
		return nil, fail(400, "Expected reason")
	}
	reason = truncate(reason, 500)
	rows, err := artifacts(ctx, a, "WHERE content_hash=$1 AND ($2::text IS NULL OR project_id IS NOT DISTINCT FROM $2) AND ($3::text IS NULL OR scope=$3)", hash, projectID, scope)
	if err != nil {
		return nil, err
	}
	all, err := storeList(ctx, a)
	if err != nil {
		return nil, err
	}
	deleted, pending := 0, 0
	for i := range rows {
		x := &rows[i]
		if err := execCtx(ctx, a, "INSERT INTO purged_hash(project_id,scope,content_hash,reason) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING", projectKey(x.ProjectID), x.Scope, hash, reason); err != nil {
			return nil, err
		}
		cs, err := rowsCtx(ctx, conn(ctx, a), "SELECT store_id,staging_key FROM artifact_copy WHERE artifact_id=$1 AND state<>'deleted'", x.ID)
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			var s *StoreRow
			for j := range all {
				if all[j].ID == astr(c["store_id"]) {
					s = &all[j]
				}
			}
			state := "purge_pending"
			if s != nil && s.State != "unreachable" {
				if t, err := tunnel(s); err == nil {
					t.Delete(s.Bucket, keyOf(x))
					if k := astr(c["staging_key"]); k != "" {
						t.Delete(s.Bucket, k)
					}
				}
				state = "deleted"
				deleted++
			} else {
				pending++
			}
			if err := execCtx(ctx, a, "UPDATE artifact_copy SET state=$3 WHERE artifact_id=$1 AND store_id=$2", x.ID, astr(c["store_id"]), state); err != nil {
				return nil, err
			}
		}
		if err := execCtx(ctx, a, "UPDATE artifact SET state='deleted',deleted_at=now(),deleted_by=$2 WHERE id=$1", x.ID, "purge:"+reason); err != nil {
			return nil, err
		}
	}
	return doc{"artifacts": len(rows), "deleted": deleted, "pending": pending}, nil
}

// expireLeases: reservations whose lease ran out are aborted and fail.
func expireLeases(ctx context.Context, a *App) error {
	stale, err := rowsCtx(ctx, conn(ctx, a), "SELECT artifact_id,store_id,upload_id,staging_key FROM artifact_copy WHERE state IN ('pending','copying') AND lease_expires_at < now()")
	if err != nil {
		return err
	}
	all, _ := storeList(ctx, a)
	for _, c := range stale {
		s := &StoreRow{ID: astr(c["store_id"])}
		for i := range all {
			if all[i].ID == s.ID {
				s = &all[i]
			}
		}
		x, err := artifactRow(ctx, a, astr(c["artifact_id"]))
		if err != nil {
			continue
		}
		if up := astr(c["upload_id"]); up != "" && s.Bucket != "" && s.State != "unreachable" {
			if t, err := tunnel(s); err == nil {
				t.AbortMultipartUpload(s.Bucket, astr(c["staging_key"]), up)
			}
		}
		if err := reject(ctx, a, x, s, astr(c["staging_key"]), "upload lease expired"); err != nil {
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
// completion_requirements.artifacts (names or globs) plus artifacts saved or declared required.
type ArtifactRequirement struct {
	Pending []string `json:"pending"`
	Failed  []doc    `json:"failed"`
	Missing []string `json:"missing"`
}

func artifactRequirement(ctx context.Context, a *App, executionID string, declared []string) (*ArtifactRequirement, error) {
	e, err := execGet(ctx, a, executionID)
	if err != nil {
		return nil, err
	}
	named := declaredRequired(e)
	patterns := []string{}
	if e.TaskID != nil {
		var raw []byte
		conn(ctx, a).QueryRow(ctx, "SELECT completion_requirements->'artifacts' FROM task WHERE id=$1", *e.TaskID).Scan(&raw)
		var l []any
		json.Unmarshal(raw, &l)
		for _, x := range l {
			if s, ok := x.(string); ok {
				patterns = append(patterns, s)
			}
		}
	}
	rows, err := artifacts(ctx, a, "WHERE execution_id=$1 AND state<>'deleted' AND role='original' ORDER BY seq", executionID)
	if err != nil {
		return nil, err
	}
	match := func(name string) bool {
		for _, p := range patterns {
			if glob(p).MatchString(name) {
				return true
			}
		}
		return false
	}
	have := map[string]bool{}
	for _, r := range rows {
		have[r.Name] = true
	}
	out := &ArtifactRequirement{Pending: []string{}, Failed: []doc{}, Missing: []string{}}
	for _, r := range rows {
		if r.Required || contains(named, r.Name) || match(r.Name) {
			if r.State == "pending" {
				out.Pending = append(out.Pending, r.Name)
			} else if r.State == "failed" {
				out.Failed = append(out.Failed, doc{"name": r.Name, "error": r.LastError})
			}
		}
	}
	seen := map[string]bool{}
	unreserved := []string{}
	for _, n := range append(append([]string{}, declared...), named...) {
		if (contains(named, n) || match(n)) && !have[n] && !seen[n] {
			seen[n] = true
			unreserved = append(unreserved, n)
		}
	}
	out.Pending = append(out.Pending, unreserved...)
	for _, p := range patterns {
		g, found := glob(p), false
		for _, n := range append(append([]string{}, unreserved...), keys(have)...) {
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

// saveObject: an output the coordinator writes for an execution (pipeline step output.json, example inputs), stored and
// verified at once.
func saveObject(ctx context.Context, a *App, projectID *string, scope, executionID, name string, data []byte, media string) (string, string, error) {
	target, hash, err := writeObject(ctx, a, projectID, scope, data, media)
	if err != nil {
		return "", "", err
	}
	id := newID()
	err = txCtx(ctx, a, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'stored')", id, projectID, scope, hash, len(data), media, name, executionID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,verified_at) VALUES($1,$2,$3,'verified',now())", id, target.ID, projectID)
		return err
	})
	return id, hash, err
}

// executionArtifacts and readArtifact: the artifact records of an execution (stored originals, in the record shape the old
// readers used) and its bounded bytes.

func readArtifact(a *App, id string) ([]byte, error) {
	return artifactBytes(context.Background(), a, id, 128000)
}

func executionArtifacts(q *Request, executionID string) ([]doc, error) {
	rows, err := artifacts(withTx(q), q.app, "WHERE execution_id=$1 AND state='stored' AND role='original' ORDER BY seq", executionID)
	if err != nil {
		return nil, err
	}
	out := []doc{}
	for _, x := range rows {
		id := x.ID
		if x.LegacyID != nil {
			id = *x.LegacyID
		}
		out = append(out, doc{"id": id, "artifactId": x.ID, "executionId": x.ExecutionID, "path": x.Name, "mediaType": x.MediaType, "size": x.Size, "sha256": x.ContentHash})
	}
	return out, nil
}
