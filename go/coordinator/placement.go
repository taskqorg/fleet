package main

// Placement (docs/designs/fleet-data-foundation.md §3): every stored artifact gets a verified copy on each reachable store of its
// project's artifact_locations, through copy commands to the target store's own executor. Copies outside the list are
// deleted only behind a surviving verified copy, never the last one. Pending purges are reconciled first. Confirmed
// retention rules run through the same copy-then-delete path. One pass per store tick (artifact_routes.go), single flight.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

const copySourceSeconds = 6 * 3600

type artifactRule struct {
	Scope         string   `json:"scope"`
	OlderThanDays int64    `json:"older_than_days"`
	Action        string   `json:"action"`
	To            []string `json:"to,omitempty"`
}

// ruleHash: sha256 of {scope, older_than_days, action, to|null} in that key order.
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

// ruleInput: a rule as written in the project settings or a request, before validRule checks it.
type ruleInput struct {
	Scope         *string         `json:"scope"`
	OlderThanDays *float64        `json:"older_than_days"`
	Action        *string         `json:"action"`
	To            json.RawMessage `json:"to"`
}

// validRule: one rule's JSON; hosts, when given, are the hosts with registered stores (a move rule must name only those).
func validRule(raw json.RawMessage, hosts map[string]bool) (artifactRule, error) {
	var m *ruleInput
	var r artifactRule
	if json.Unmarshal(raw, &m) != nil || m == nil || m.Scope == nil || m.OlderThanDays == nil || m.Action == nil {
		return r, fail(400, "Invalid artifact rule")
	}
	days := *m.OlderThanDays
	if (*m.Scope != "lab" && *m.Scope != "prod") || days < 0 || days != float64(int64(days)) || (*m.Action != "move" && *m.Action != "delete") {
		return r, fail(400, "Invalid artifact rule")
	}
	r = artifactRule{Scope: *m.Scope, OlderThanDays: int64(days), Action: *m.Action}
	if r.Action == "move" {
		var l []*string
		if json.Unmarshal(m.To, &l) != nil || len(l) == 0 {
			return r, fail(400, "A move rule needs to: hosts with registered stores")
		}
		for _, h := range l {
			if h == nil || hosts != nil && !hosts[*h] {
				return r, fail(400, "A move rule needs to: hosts with registered stores")
			}
			r.To = append(r.To, *h)
		}
	} else if len(m.To) > 0 {
		return r, fail(400, "A delete rule takes no to")
	}
	return r, nil
}

// projectRules: the artifact_rules of a project's settings, each rule's JSON; a single rule counts as a list of one.
func projectRules(q *Request, projectID *string) []json.RawMessage {
	if projectID == nil {
		return nil
	}
	v, ok := projectSettingsOf(q, *projectID)["artifact_rules"]
	if !ok || v == nil {
		return nil
	}
	b := jsonBytes(v)
	var list []json.RawMessage
	if json.Unmarshal(b, &list) != nil {
		list = []json.RawMessage{b}
	}
	return list
}

// Problems of the last pass, shown by stores_list.
var placementProblems struct {
	sync.Mutex
	list []doc
}

// problem: one problem of this pass, its fields as their JSON names.
func problem(v any) {
	p := docOf(v)
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

// ruleProblem: a project rule that does not validate; projectId stays a *string, as stores_list filters it.
func ruleProblem(projectID *string, rule int, err error) {
	p := docOf(struct {
		Kind  string `json:"kind"`
		Rule  int    `json:"rule"`
		Error string `json:"error"`
	}{"artifact-rule-invalid", rule, err.Error()})
	p["projectId"] = projectID
	placementProblems.Lock()
	placementProblems.list = append(placementProblems.list, p)
	placementProblems.Unlock()
}

// effective: a confirmed matching move rule replaces the project list for the artifact; a delete rule ends it.
func effective(ctx context.Context, a *App, x *Artifact, hosts map[string]bool) (plan, error) {
	q := backgroundRequest(ctx, a, nil)
	age := time.Since(x.Created).Hours() / 24
	rules := []artifactRule{}
	for i, raw := range projectRules(q, x.ProjectID) {
		r, err := validRule(raw, hosts)
		if err != nil {
			ruleProblem(x.ProjectID, i, err)
			continue
		}
		if r.Scope == x.Scope && age >= float64(r.OlderThanDays) {
			rules = append(rules, r)
		}
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].OlderThanDays > rules[j].OlderThanDays })
	for _, r := range rules {
		ok, err := connDB(ctx, a).DispRuleConfirmed(ctx, db.DispRuleConfirmedParams{ProjectID: projectKey(x.ProjectID), RuleHash: ruleHash(r)})
		if err != nil {
			return plan{}, err
		}
		if !ok {
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
			problem(struct {
				Kind       string `json:"kind"`
				ArtifactID string `json:"artifactId"`
				Error      string `json:"error"`
			}{"artifact-placement-error", x.ID, err.Error()})
		}
		queued, deleted = queued+q, deleted+d
	}
	if err := expireLeases(ctx, a); err != nil {
		return queued, deleted, err
	}
	return queued, deleted, nil
}

func placeOne(ctx context.Context, a *App, x *Artifact, stores []StoreRow, byID, byHost map[string]*StoreRow, hosts map[string]bool) (queued, deleted int, err error) {
	copies, err := connDB(ctx, a).DispArtifactCopies(ctx, x.ID)
	if err != nil {
		return 0, 0, err
	}
	verified := []*StoreRow{}
	for _, c := range copies {
		if c.State == "verified" {
			if s := byID[c.StoreID]; s != nil {
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
		var c *db.DispArtifactCopiesRow
		for i := range copies {
			if copies[i].StoreID == s.ID {
				c = &copies[i]
			}
		}
		if c != nil {
			switch c.State {
			case "verified", "pending", "copying", "purge_pending":
				continue
			case "failed":
				// A failed copy waits for its backoff (or an operator retry); its failure stays visible meanwhile.
				problem(struct {
					Kind          string     `json:"kind"`
					ArtifactID    string     `json:"artifactId"`
					StoreID       string     `json:"storeId"`
					Error         *string    `json:"error"`
					Attempts      int32      `json:"attempts"`
					NextAttemptAt *time.Time `json:"nextAttemptAt"`
					Actions       []string   `json:"actions"`
				}{"artifact-copy-failed", x.ID, s.ID, c.LastError, c.Attempts, c.NextAttemptAt, []string{"artifact_copy_retry"}})
				if !c.Due {
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
		if err := txCtx(ctx, a, func(tx pgx.Tx) error {
			return db.New(tx).DispCopyStart(ctx, db.DispCopyStartParams{ArtifactID: x.ID, StoreID: s.ID, ProjectID: x.ProjectID, UploadID: &upload, StagingKey: &staging})
		}); err != nil {
			return queued, deleted, err
		}
		src, err := s3For(from, s.HostID == from.HostID && from.LocalEndpoint != nil)
		if err != nil {
			return queued, deleted, err
		}
		url := src.Presign("GET", from.Bucket, keyOf(x), copySourceSeconds, nil)
		payload := docOf(copyPayload{ArtifactID: x.ID, StoreID: s.ID, Size: x.Size, PartSize: partSize, Source: copySource{StoreID: from.ID, URL: url}})
		if err := queueCommand(ctx, a, s.HostID, "artifact-copy", payload); err != nil {
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
	others, err := connDB(ctx, a).DispCopySharers(ctx, db.DispCopySharersParams{ID: x.ID, ProjectID: x.ProjectID, Scope: x.Scope, ContentHash: x.ContentHash, StoreID: s.ID})
	if err != nil {
		return false, err
	}
	needed := false
	for _, o := range others {
		if contains(artifactLocations(a, o), s.HostID) {
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
	return true, copyDeleted(ctx, a, x.ID, s.ID)
}

// copyDeleted: the copy of artifact on store is gone.
func copyDeleted(ctx context.Context, a *App, artifact, store string) error {
	return txCtx(ctx, a, func(tx pgx.Tx) error {
		return db.New(tx).DispCopyDeleted(ctx, db.DispCopyDeletedParams{ArtifactID: artifact, StoreID: store})
	})
}

// deleteArtifact: a delete rule ends the artifact; every copy goes (objects only where no other stored row needs them) and
// the row stays as a tombstone. Nothing happens while a verified copy sits on an unreachable store.
func deleteArtifact(ctx context.Context, a *App, x *Artifact, by string) error {
	held, err := connDB(ctx, a).DispArtifactHeld(ctx, x.ID)
	if err != nil || held {
		return err
	}
	cs, err := connDB(ctx, a).DispVerifiedStores(ctx, x.ID)
	if err != nil {
		return err
	}
	for _, c := range cs {
		s, err := storeGet(ctx, a, c)
		if err != nil {
			return err
		}
		shared, err := connDB(ctx, a).DispCopyShared(ctx, db.DispCopySharedParams{ID: x.ID, ProjectID: x.ProjectID, Scope: x.Scope, ContentHash: x.ContentHash, StoreID: s.ID})
		if err != nil {
			return err
		}
		if !shared {
			t, err := tunnel(s)
			if err != nil {
				return err
			}
			if err := t.Delete(s.Bucket, keyOf(x)); err != nil {
				return err
			}
		}
		if err := copyDeleted(ctx, a, x.ID, s.ID); err != nil {
			return err
		}
	}
	return txCtx(ctx, a, func(tx pgx.Tx) error {
		return db.New(tx).DispArtifactDelete(ctx, db.DispArtifactDeleteParams{ID: x.ID, DeletedBy: &by})
	})
}

// reconcilePurges: pending purges are deleted as soon as their store is reachable again, before it serves anything.
func reconcilePurges(ctx context.Context, a *App) error {
	rs, err := connDB(ctx, a).DispPurgesPending(ctx)
	if err != nil {
		return err
	}
	for _, c := range rs {
		s, err := storeGet(ctx, a, c.StoreID)
		if err != nil || s.State == "unreachable" {
			continue
		}
		if t, err := tunnel(s); err == nil {
			t.Delete(s.Bucket, keyOf(&Artifact{ProjectID: c.ProjectID, Scope: c.Scope, ContentHash: c.ContentHash}))
		}
		if err := copyDeleted(ctx, a, c.ArtifactID, s.ID); err != nil {
			return err
		}
	}
	return nil
}

// reconcileArtifactWaits recovers missed storage callbacks and enforces the retry window.
func reconcileArtifactWaits(ctx context.Context, a *App) error {
	rs, err := connDB(ctx, a).DispArtifactWaits(ctx, nowMs()-int64(retryWindowMS))
	if err != nil {
		return err
	}
	var failures error
	for _, r := range rs {
		id := r.ID
		expired := ""
		if r.Expired {
			expired = "artifact retry window passed"
		} else {
			// Keep ordinary pending checks off the global write lock. Settlement rechecks the gate under the lock.
			requirement, err := artifactRequirement(ctx, a, id, declaredNames(jsonStrings(r.Declared)))
			if err != nil {
				failures = errors.Join(failures, fmt.Errorf("%s: %w", id, err))
				continue
			}
			if requirement.stillUploading() {
				continue
			}
		}
		if err := settleWaitingAs(ctx, a, id, expired); err != nil {
			failures = errors.Join(failures, fmt.Errorf("%s: %w", id, err))
		}
	}
	return failures
}

func ruleArg(projectID *string, rule json.RawMessage) (*string, artifactRule, error) {
	r, err := validRule(rule, nil)
	return projectID, r, err
}

// copyPayload: the payload of an artifact-copy command (contract.ArtifactTransferPayload and its open fields).
type copyPayload struct {
	ArtifactID string     `json:"artifactId"`
	StoreID    string     `json:"storeId"`
	Size       int64      `json:"size"`
	PartSize   int64      `json:"partSize"`
	Source     copySource `json:"source"`
}

type copySource struct {
	StoreID string `json:"storeId"`
	URL     string `json:"url"`
}

// jsonStrings: the strings of a JSON array; other elements are skipped, anything else is no strings.
func jsonStrings(b []byte) []string {
	var xs []json.RawMessage
	json.Unmarshal(b, &xs)
	out := []string{}
	for _, x := range xs {
		var s string
		if json.Unmarshal(x, &s) == nil {
			out = append(out, s)
		}
	}
	return out
}

func init() {
	// Retention rules: the operator previews the effect, then confirms; editing a rule changes its hash.
	op("artifact_rule_preview", func(q *Request, b *contract.ArtifactRulePreviewRequest) (contract.ArtifactRulePreviewReply, error) {
		var none contract.ArtifactRulePreviewReply
		if err := operatorOnly(q); err != nil {
			return none, err
		}
		p, r, err := ruleArg(b.ProjectId, b.Rule)
		if err != nil {
			return none, err
		}
		rows, err := q.db().DispRulePreview(q.ctx(), db.DispRulePreviewParams{ProjectID: p, Scope: r.Scope, Days: fmt.Sprint(r.OlderThanDays)})
		if err != nil {
			return none, err
		}
		var bytes int64
		sample := []map[string]json.RawMessage{}
		for i, x := range rows {
			bytes += x.Size
			if i < 10 {
				sample = append(sample, recOf(struct {
					ID   string `json:"id"`
					Name string `json:"name"`
					Size int64  `json:"size"`
				}{x.ID, x.Name, x.Size}))
			}
		}
		return contract.ArtifactRulePreviewReply{RuleHash: ruleHash(r), Count: int64(len(rows)), Bytes: bytes, Sample: sample}, nil
	})
	op("artifact_rule_confirm", func(q *Request, b *contract.ArtifactRuleConfirmRequest) (contract.ArtifactRuleConfirmReply, error) {
		var none contract.ArtifactRuleConfirmReply
		if err := operatorOnly(q); err != nil {
			return none, err
		}
		p, r, err := ruleArg(b.ProjectId, b.Rule)
		if err != nil {
			return none, err
		}
		if b.RuleHash != nil && *b.RuleHash != ruleHash(r) {
			return none, fail(409, "The rule changed since the preview")
		}
		ss, err := storeList(withTx(q), q.app)
		if err != nil {
			return none, err
		}
		hosts := map[string]bool{}
		for _, s := range ss {
			hosts[s.HostID] = true
		}
		rb, _ := json.Marshal(r)
		if _, err := validRule(rb, hosts); err != nil {
			return none, err
		}
		found := false
		for _, x := range projectRules(q, p) {
			if y, err := validRule(x, nil); err == nil && ruleHash(y) == ruleHash(r) {
				found = true
			}
		}
		if !found {
			return none, fail(404, "Rule is not in the project settings")
		}
		err = q.db().DispRuleConfirm(q.ctx(), db.DispRuleConfirmParams{ProjectID: projectKey(p), RuleHash: ruleHash(r), ConfirmedBy: "operator:" + q.person.Email})
		return contract.ArtifactRuleConfirmReply{RuleHash: ruleHash(r), Confirmed: true}, err
	})
	// An operator retry of a failed copy: placement queues it on its next pass.
	op("artifact_copy_retry", func(q *Request, b *contract.ArtifactCopyRetryRequest) (contract.ArtifactCopyRetryReply, error) {
		if err := operatorOnly(q); err != nil {
			return contract.ArtifactCopyRetryReply{}, err
		}
		_, err := q.db().DispCopyRetry(q.ctx(), db.DispCopyRetryParams{ArtifactID: truncate(b.ArtifactId, 200), StoreID: truncate(b.StoreId, 200)})
		if err == pgx.ErrNoRows {
			return contract.ArtifactCopyRetryReply{}, fail(409, "No failed copy for this artifact and store")
		}
		if err != nil {
			return contract.ArtifactCopyRetryReply{}, err
		}
		return contract.ArtifactCopyRetryReply{Retrying: true}, nil
	})
}
