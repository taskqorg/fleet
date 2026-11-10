package main

// Placement (packages/coordinator/placement.ts): every stored artifact gets a verified copy on each reachable store of its
// project's artifact_locations, through copy commands to the target store's own executor. Copies outside the list are
// deleted only behind a surviving verified copy, never the last one. Pending purges are reconciled first. Confirmed
// retention rules run through the same copy-then-delete path. One pass per store tick (d_routes.go), single flight.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const copySourceSeconds = 6 * 3600

type artifactRule struct {
	Scope         string   `json:"scope"`
	OlderThanDays int64    `json:"older_than_days"`
	Action        string   `json:"action"`
	To            []string `json:"to,omitempty"`
}

// ruleHash: sha256 of {scope, older_than_days, action, to|null} in that key order (placement.ts ruleHash).
func ruleHash(r artifactRule) string {
	to := "null"
	if r.To != nil {
		b, _ := json.Marshal(r.To)
		to = string(b)
	}
	sc, _ := json.Marshal(r.Scope)
	ac, _ := json.Marshal(r.Action)
	h := sha256.Sum256([]byte(fmt.Sprintf(`{"scope":%s,"older_than_days":%d,"action":%s,"to":%s}`, sc, r.OlderThanDays, ac, to)))
	return hex.EncodeToString(h[:])
}

// validRule: hosts, when given, are the hosts with registered stores (a move rule must name only those).
func validRule(v any, hosts map[string]bool) (artifactRule, error) {
	m, _ := v.(map[string]any)
	var r artifactRule
	days, ok := m["older_than_days"].(float64)
	if m == nil || (m["scope"] != "lab" && m["scope"] != "prod") || !ok || days < 0 || days != float64(int64(days)) || (m["action"] != "move" && m["action"] != "delete") {
		return r, fail(400, "Invalid artifact rule")
	}
	r = artifactRule{Scope: m["scope"].(string), OlderThanDays: int64(days), Action: m["action"].(string)}
	to, hasTo := m["to"]
	if r.Action == "move" {
		l, _ := to.([]any)
		if len(l) == 0 {
			return r, fail(400, "A move rule needs to: hosts with registered stores")
		}
		for _, h := range l {
			s, ok := h.(string)
			if !ok || hosts != nil && !hosts[s] {
				return r, fail(400, "A move rule needs to: hosts with registered stores")
			}
			r.To = append(r.To, s)
		}
	} else if hasTo {
		return r, fail(400, "A delete rule takes no to")
	}
	return r, nil
}

func projectRules(projectID *string) []any {
	if projectID == nil {
		return nil
	}
	switch v := projectSettingsOf(*projectID)["artifact_rules"].(type) {
	case []any:
		return v
	case nil:
		return nil
	default:
		return []any{v}
	}
}

// Problems of the last pass, shown by stores_list.
var placementProblems struct {
	sync.Mutex
	list []doc
}

func problem(p doc) {
	placementProblems.Lock()
	placementProblems.list = append(placementProblems.list, p)
	placementProblems.Unlock()
}

func lastPlacementProblems() []doc {
	placementProblems.Lock()
	defer placementProblems.Unlock()
	return append([]doc{}, placementProblems.list...)
}

type plan struct {
	locations []string
	delete    *artifactRule
}

// effective: a confirmed matching move rule replaces the project list for the artifact; a delete rule ends it.
func effective(ctx context.Context, a *App, x *Artifact, hosts map[string]bool) (plan, error) {
	age := time.Since(x.Created).Hours() / 24
	rules := []artifactRule{}
	for i, raw := range projectRules(x.ProjectID) {
		r, err := validRule(raw, hosts)
		if err != nil {
			problem(doc{"kind": "artifact-rule-invalid", "projectId": x.ProjectID, "rule": i, "error": err.Error()})
			continue
		}
		if r.Scope == x.Scope && age >= float64(r.OlderThanDays) {
			rules = append(rules, r)
		}
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].OlderThanDays > rules[j].OlderThanDays })
	for _, r := range rules {
		ok, err := dOne(ctx, a, "SELECT 1 AS x FROM artifact_rule_confirmation WHERE project_id=$1 AND rule_hash=$2", projectKey(x.ProjectID), ruleHash(r))
		if err != nil {
			return plan{}, err
		}
		if ok == nil {
			continue
		}
		if r.Action == "delete" {
			return plan{delete: &r}, nil
		}
		return plan{locations: r.To}, nil
	}
	return plan{locations: artifactLocations(a, x.ProjectID)}, nil
}

var placementRunning sync.Mutex

// runPlacement: one pass; store calls run between short transactions.
func runPlacement(ctx context.Context, a *App) (queued, deleted int, err error) {
	if !placementRunning.TryLock() {
		return 0, 0, nil
	}
	defer placementRunning.Unlock()
	placementProblems.Lock()
	placementProblems.list = nil
	placementProblems.Unlock()
	if err := reconcilePurges(ctx, a); err != nil {
		return 0, 0, err
	}
	stores, err := storeList(ctx, a)
	if err != nil {
		return 0, 0, err
	}
	byID, byHost, hosts := map[string]*StoreRow{}, map[string]*StoreRow{}, map[string]bool{}
	for i := range stores {
		byID[stores[i].ID], byHost[stores[i].HostID], hosts[stores[i].HostID] = &stores[i], &stores[i], true
	}
	all, err := artifacts(ctx, a, "WHERE state='stored' ORDER BY seq")
	if err != nil {
		return 0, 0, err
	}
	for i := range all {
		x := &all[i]
		q, d, err := placeOne(ctx, a, x, stores, byID, byHost, hosts)
		if err != nil {
			problem(doc{"kind": "artifact-placement-error", "artifactId": x.ID, "error": err.Error()})
		}
		queued, deleted = queued+q, deleted+d
	}
	if err := expireLeases(ctx, a); err != nil {
		return queued, deleted, err
	}
	return queued, deleted, expireWaiting(ctx, a)
}

func placeOne(ctx context.Context, a *App, x *Artifact, stores []StoreRow, byID, byHost map[string]*StoreRow, hosts map[string]bool) (queued, deleted int, err error) {
	copies, err := dRows(ctx, conn(ctx, a), "SELECT store_id,state,last_error,attempts,next_attempt_at,(next_attempt_at IS NULL OR next_attempt_at<=now()) AS due FROM artifact_copy WHERE artifact_id=$1", x.ID)
	if err != nil {
		return 0, 0, err
	}
	verified := []*StoreRow{}
	for _, c := range copies {
		if str(c, "state") == "verified" {
			if s := byID[str(c, "store_id")]; s != nil {
				verified = append(verified, s)
			}
		}
	}
	p, err := effective(ctx, a, x, hosts)
	if err != nil {
		return 0, 0, err
	}
	if p.delete != nil {
		return 0, 0, deleteArtifact(ctx, a, x, "rule:"+ruleHash(*p.delete)[:12])
	}
	for _, h := range p.locations {
		s := byHost[h]
		if s == nil || s.State != "reachable" {
			continue
		}
		var c map[string]any
		for _, y := range copies {
			if str(y, "store_id") == s.ID {
				c = y
			}
		}
		if c != nil {
			switch str(c, "state") {
			case "verified", "pending", "copying", "purge_pending":
				continue
			case "failed":
				// A failed copy waits for its backoff (or an operator retry); its failure stays visible meanwhile.
				problem(doc{"kind": "artifact-copy-failed", "artifactId": x.ID, "storeId": s.ID, "error": c["last_error"], "attempts": c["attempts"], "nextAttemptAt": c["next_attempt_at"], "actions": []string{"artifact_copy_retry"}})
				if c["due"] != true {
					continue
				}
			}
		}
		var from *StoreRow
		for _, v := range verified {
			if v.State != "unreachable" {
				from = v
				break
			}
		}
		if from == nil {
			continue
		}
		t, err := tunnel(s)
		if err != nil {
			return queued, deleted, err
		}
		staging := stagingOf(x, s.ID)
		upload, err := t.CreateMultipartUpload(s.Bucket, staging, x.MediaType)
		if err != nil {
			return queued, deleted, err
		}
		if err := dExec(ctx, a, `INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,upload_id,staging_key,lease_expires_at) VALUES($1,$2,$3,'copying',$4,$5,now()+interval '6 hours')
     ON CONFLICT(artifact_id,store_id) DO UPDATE SET state='copying',next_attempt_at=NULL,upload_id=excluded.upload_id,staging_key=excluded.staging_key,lease_expires_at=excluded.lease_expires_at,last_error=NULL`, x.ID, s.ID, x.ProjectID, upload, staging); err != nil {
			return queued, deleted, err
		}
		src, err := s3For(from, s.HostID == from.HostID && from.LocalEndpoint != nil)
		if err != nil {
			return queued, deleted, err
		}
		url := src.Presign("GET", from.Bucket, keyOf(x), copySourceSeconds, nil)
		if err := queueCommand(ctx, a, s.HostID, "artifact-copy", doc{"artifactId": x.ID, "storeId": s.ID, "size": x.Size, "partSize": partSize, "source": doc{"storeId": from.ID, "url": url}}); err != nil {
			return queued, deleted, err
		}
		queued++
	}
	// Copies on stores outside the list: deleted only behind a verified copy on a reachable listed store that HEAD confirms.
	for _, s := range verified {
		if contains(p.locations, s.HostID) {
			continue
		}
		survivors := []*StoreRow{}
		for _, v := range verified {
			if v.ID != s.ID && contains(p.locations, v.HostID) {
				survivors = append(survivors, v)
			}
		}
		ok, err := removeCopy(ctx, a, x, s, survivors)
		if err != nil {
			return queued, deleted, err
		}
		if ok {
			deleted++
		}
	}
	return queued, deleted, nil
}

// removeCopy deletes the object on s only when no other stored row of the same project, scope and hash needs that store.
func removeCopy(ctx context.Context, a *App, x *Artifact, s *StoreRow, survivors []*StoreRow) (bool, error) {
	alive := false
	for _, v := range survivors {
		if v.State == "unreachable" {
			continue
		}
		if t, err := tunnel(v); err == nil {
			if n, err := t.Head(v.Bucket, keyOf(x)); err == nil && n == x.Size {
				alive = true
			}
		}
	}
	if !alive {
		return false, nil
	}
	others, err := dRows(ctx, conn(ctx, a), "SELECT a.project_id FROM artifact a JOIN artifact_copy c ON c.artifact_id=a.id WHERE a.id<>$1 AND a.project_id IS NOT DISTINCT FROM $2 AND a.scope=$3 AND a.content_hash=$4 AND a.state='stored' AND c.store_id=$5 AND c.state='verified'", x.ID, x.ProjectID, x.Scope, x.ContentHash, s.ID)
	if err != nil {
		return false, err
	}
	needed := false
	for _, o := range others {
		if contains(artifactLocations(a, strp(o, "project_id")), s.HostID) {
			needed = true
		}
	}
	if !needed {
		t, err := tunnel(s)
		if err != nil {
			return false, err
		}
		if err := t.Delete(s.Bucket, keyOf(x)); err != nil {
			return false, err
		}
	}
	return true, dExec(ctx, a, "UPDATE artifact_copy SET state='deleted' WHERE artifact_id=$1 AND store_id=$2", x.ID, s.ID)
}

// deleteArtifact: a delete rule ends the artifact; every copy goes (objects only where no other stored row needs them) and
// the row stays as a tombstone. Nothing happens while a verified copy sits on an unreachable store.
func deleteArtifact(ctx context.Context, a *App, x *Artifact, by string) error {
	held, err := dOne(ctx, a, "SELECT 1 AS x FROM artifact_copy c JOIN store s ON s.id=c.store_id WHERE c.artifact_id=$1 AND c.state='verified' AND s.state='unreachable' LIMIT 1", x.ID)
	if err != nil || held != nil {
		return err
	}
	cs, err := dRows(ctx, conn(ctx, a), "SELECT store_id FROM artifact_copy WHERE artifact_id=$1 AND state='verified'", x.ID)
	if err != nil {
		return err
	}
	for _, c := range cs {
		s, err := storeGet(ctx, a, str(c, "store_id"))
		if err != nil {
			return err
		}
		shared, err := dOne(ctx, a, "SELECT 1 AS x FROM artifact y JOIN artifact_copy z ON z.artifact_id=y.id WHERE y.id<>$1 AND y.project_id IS NOT DISTINCT FROM $2 AND y.scope=$3 AND y.content_hash=$4 AND y.state='stored' AND z.store_id=$5 AND z.state='verified' LIMIT 1", x.ID, x.ProjectID, x.Scope, x.ContentHash, s.ID)
		if err != nil {
			return err
		}
		if shared == nil {
			t, err := tunnel(s)
			if err != nil {
				return err
			}
			if err := t.Delete(s.Bucket, keyOf(x)); err != nil {
				return err
			}
		}
		if err := dExec(ctx, a, "UPDATE artifact_copy SET state='deleted' WHERE artifact_id=$1 AND store_id=$2", x.ID, s.ID); err != nil {
			return err
		}
	}
	return dExec(ctx, a, "UPDATE artifact SET state='deleted',deleted_at=now(),deleted_by=$2 WHERE id=$1", x.ID, by)
}

// reconcilePurges: pending purges are deleted as soon as their store is reachable again, before it serves anything.
func reconcilePurges(ctx context.Context, a *App) error {
	rs, err := dRows(ctx, conn(ctx, a), "SELECT c.artifact_id,c.store_id,a.project_id,a.scope,a.content_hash FROM artifact_copy c JOIN artifact a ON a.id=c.artifact_id WHERE c.state='purge_pending'")
	if err != nil {
		return err
	}
	for _, c := range rs {
		s, err := storeGet(ctx, a, str(c, "store_id"))
		if err != nil || s.State == "unreachable" {
			continue
		}
		if t, err := tunnel(s); err == nil {
			t.Delete(s.Bucket, keyOf(&Artifact{ProjectID: strp(c, "project_id"), Scope: str(c, "scope"), ContentHash: str(c, "content_hash")}))
		}
		if err := dExec(ctx, a, "UPDATE artifact_copy SET state='deleted' WHERE artifact_id=$1 AND store_id=$2", str(c, "artifact_id"), s.ID); err != nil {
			return err
		}
	}
	return nil
}

// expireWaiting: executions waiting for their artifacts longer than the retry window fail (P3-D2).
func expireWaiting(ctx context.Context, a *App) error {
	rs, err := dRows(ctx, conn(ctx, a), "SELECT id FROM execution WHERE legacy_kind='execution' AND data->>'state'='waiting' AND data->>'waitReason'='artifacts' AND coalesce((data->>'waitingSince')::bigint,0) < $1", now()-int64(retryWindowMS))
	if err != nil {
		return err
	}
	for _, r := range rs {
		if err := settleWaitingAs(ctx, a, str(r, "id"), "artifact retry window passed"); err != nil {
			return err
		}
	}
	return nil
}

func ruleArg(q *Request) (*string, artifactRule, error) {
	var b struct {
		ProjectID *string `json:"projectId"`
		Rule      any     `json:"rule"`
	}
	decode(q.body, &b)
	r, err := validRule(b.Rule, nil)
	return b.ProjectID, r, err
}

func init() {
	hookPlacement = func(a *App) error { _, _, err := runPlacement(context.Background(), a); return err }
	// Retention rules: the operator previews the effect, then confirms; editing a rule changes its hash.
	operation("artifact_rule_preview", true, func(q *Request) (any, error) {
		p, r, err := ruleArg(q)
		if err != nil {
			return nil, err
		}
		rows, err := dRows(withTx(q), conn(withTx(q), q.app), "SELECT id,name,size FROM artifact WHERE state='stored' AND project_id IS NOT DISTINCT FROM $1 AND scope=$2 AND created_at <= now() - ($3::text||' days')::interval ORDER BY seq", p, r.Scope, fmt.Sprint(r.OlderThanDays))
		if err != nil {
			return nil, err
		}
		var bytes int64
		for _, x := range rows {
			bytes += i64(x, "size")
		}
		return doc{"ruleHash": ruleHash(r), "count": len(rows), "bytes": bytes, "sample": rows[:min(10, len(rows))]}, nil
	})
	operation("artifact_rule_confirm", false, func(q *Request) (any, error) {
		if err := operatorOnly(q); err != nil {
			return nil, err
		}
		p, r, err := ruleArg(q)
		if err != nil {
			return nil, err
		}
		ss, err := storeList(withTx(q), q.app)
		if err != nil {
			return nil, err
		}
		hosts := map[string]bool{}
		for _, s := range ss {
			hosts[s.HostID] = true
		}
		var raw map[string]any
		b, _ := json.Marshal(r)
		json.Unmarshal(b, &raw)
		if _, err := validRule(raw, hosts); err != nil {
			return nil, err
		}
		found := false
		for _, x := range projectRules(p) {
			if y, err := validRule(x, nil); err == nil && ruleHash(y) == ruleHash(r) {
				found = true
			}
		}
		if !found {
			return nil, fail(404, "Rule is not in the project settings")
		}
		_, err = q.tx.Exec(q.ctx(), "INSERT INTO artifact_rule_confirmation(project_id,rule_hash,confirmed_by) VALUES($1,$2,$3) ON CONFLICT(project_id,rule_hash) DO UPDATE SET confirmed_by=excluded.confirmed_by,confirmed_at=now()", projectKey(p), ruleHash(r), "operator:"+q.person.Email)
		return doc{"ruleHash": ruleHash(r), "confirmed": true}, err
	})
	// An operator retry of a failed copy: placement queues it on its next pass.
	operation("artifact_copy_retry", false, func(q *Request) (any, error) {
		var b struct{ ArtifactID, StoreID string }
		decode(q.body, &b)
		var id string
		err := q.tx.QueryRow(q.ctx(), "UPDATE artifact_copy SET next_attempt_at=now(),attempts=0 WHERE artifact_id=$1 AND store_id=$2 AND state='failed' RETURNING artifact_id", truncate(b.ArtifactID, 200), truncate(b.StoreID, 200)).Scan(&id)
		if err == pgx.ErrNoRows {
			return nil, fail(409, "No failed copy for this artifact and store")
		}
		if err != nil {
			return nil, err
		}
		return doc{"retrying": true}, nil
	})
}
