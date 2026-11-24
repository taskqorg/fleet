package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"fleet/obs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type cancelPlacementAfterScan struct {
	pgx.Tx
	cancel context.CancelFunc
}

func (tx cancelPlacementAfterScan) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := tx.Tx.Query(ctx, sql, args...)
	if err == nil && strings.HasPrefix(sql, "-- name: ArtStoredAll ") {
		return cancelOnPlacementRowsClose{Rows: rows, cancel: tx.cancel}, nil
	}
	return rows, err
}

type cancelOnPlacementRowsClose struct {
	pgx.Rows
	cancel context.CancelFunc
}

func (rows cancelOnPlacementRowsClose) Close() {
	rows.Rows.Close()
	rows.cancel()
}

// Requirement: a confirmed retention rule keeps its confirmation (artifact_rule_confirmation.rule_hash) across the rewrite.
// Risk: a different hash silently un-confirms every rule, so moves and deletes stop. Hashes pinned from the TypeScript coordinator's rule hash.
func TestRuleHashMatchesTypeScript(t *testing.T) {
	for want, r := range map[string]artifactRule{
		"a50da7d3ec279517bfbfdce28045fb4b5b8272889b6a266b24516edfce409e29": {Scope: "lab", OlderThanDays: 30, Action: "move", To: []string{"pds18"}},
		"1465b5fcb4a3b06ab8189e7cf14ae917368f36606508f4ded369e9847c21c3b9": {Scope: "prod", OlderThanDays: 0, Action: "delete"},
	} {
		if got := ruleHash(r); got != want {
			t.Errorf("%+v: %s", r, got)
		}
	}
}

// Requirement: placement reports individual failures in structured telemetry and still expires leases.
// Risk: a failed artifact marks every pass as failed, prevents expiry, or hides a real expiry error.
func TestPlacementPartialFailureAndLeaseExpiry(t *testing.T) {
	env := openTestCoordinator(t, false)
	locations, secrets := defaultLocations, storeSecretsDir
	defaultLocations, storeSecretsDir = []string{"placement-target"}, t.TempDir()
	t.Cleanup(func() { defaultLocations, storeSecretsDir = locations, secrets })

	seed := func(sql string) {
		t.Helper()
		if _, err := env.ownerStore.pool.Exec(env.ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	seed(`
INSERT INTO store(id,host_id,bucket,endpoint,state) VALUES
 ('placement-source','placement-source','fixture','http://127.0.0.1:1','reachable'),
 ('placement-target','placement-target','fixture','http://127.0.0.1:1','reachable');
INSERT INTO artifact(id,scope,content_hash,size,media_type,name,state) VALUES
 ('copy-error-1','prod','fixture-1',1,'text/plain','broken-1','stored'),
 ('copy-error-2','prod','fixture-2',1,'text/plain','broken-2','stored'),
 ('already-placed','prod','fixture-3',1,'text/plain','placed','stored'),
 ('expired-lease','prod','fixture-4',1,'text/plain','expired','pending');
INSERT INTO artifact_copy(artifact_id,store_id,state,lease_expires_at) VALUES
 ('copy-error-1','placement-source','verified',NULL),
 ('copy-error-2','placement-source','verified',NULL),
 ('already-placed','placement-target','verified',NULL),
 ('expired-lease','placement-target','pending',now()-interval '1 hour');`)

	run := func(leaseFailed, outcome string) error {
		t.Helper()
		ctx, span := obs.Start(env.ctx, "pass.placement", obs.KindPass)
		_, _, err := runPlacement(ctx, env.app)
		span.End(err)
		if span.Outcome != outcome {
			t.Errorf("pass outcome = %q, want %q (error: %v)", span.Outcome, outcome, err)
		}
		for key, want := range map[string]string{
			"artifacts": "3", "failed": "2", "purges_failed": "0", "scan_failed": "0", "expire_leases_failed": leaseFailed,
		} {
			if got := span.Attrs["fleet.placement."+key]; got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		failures := map[string]bool{}
		for _, p := range lastPlacementProblems() {
			var problem struct {
				Kind       string `json:"kind"`
				ArtifactID string `json:"artifactId"`
			}
			if err := json.Unmarshal(p.fields, &problem); err != nil {
				t.Fatal(err)
			}
			if problem.Kind == "artifact-placement-error" {
				failures[problem.ArtifactID] = true
			}
		}
		if len(failures) != 2 || !failures["copy-error-1"] || !failures["copy-error-2"] {
			t.Errorf("retained placement problems = %v", failures)
		}
		return err
	}
	t.Cleanup(func() {
		placementProblems.Lock()
		placementProblems.list = nil
		placementProblems.Unlock()
	})

	// Requirement: cancellation after scanning preserves the lease phase and records both reached phases.
	// Risk: an early return bypasses lease expiry and hides the placement telemetry.
	t.Run("cancel after artifact scan", func(t *testing.T) {
		tx, err := env.ownerStore.pool.Begin(env.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(env.ctx)
		ctx, cancel := context.WithCancel(env.ctx)
		defer cancel()
		ctx = context.WithValue(ctx, txKey{}, cancelPlacementAfterScan{Tx: tx, cancel: cancel})
		ctx, span := obs.Start(ctx, "pass.placement", obs.KindPass)
		queued, deleted, err := runPlacement(ctx, env.app)
		span.End(err)
		if !errors.Is(err, context.Canceled) || queued != 0 || deleted != 0 {
			t.Fatalf("cancelled placement = %d queued, %d deleted, %v", queued, deleted, err)
		}
		for key, want := range map[string]string{
			"scan_failed": "0", "artifacts": "3", "failed": "3", "expire_leases_failed": "1",
		} {
			if got := span.Attrs["fleet.placement."+key]; got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		for _, key := range []string{"place_ms", "expire_leases_ms"} {
			if _, ok := span.Attrs["fleet.placement."+key]; !ok {
				t.Errorf("missing timing for reached phase %s", key)
			}
		}
	})

	if err := run("0", obs.OK); err != nil {
		t.Fatalf("partial artifact failures alone must not fail the pass: %v", err)
	}
	var copyState, artifactState string
	err := env.ownerStore.pool.QueryRow(env.ctx, `
SELECT c.state,a.state FROM artifact_copy c JOIN artifact a ON a.id=c.artifact_id WHERE a.id='expired-lease'
`).Scan(&copyState, &artifactState)
	if err != nil || copyState != "failed" || artifactState != "failed" {
		t.Fatalf("lease expiry = copy %q, artifact %q, error %v", copyState, artifactState, err)
	}

	seed(`
INSERT INTO artifact(id,scope,content_hash,size,media_type,name) VALUES
 ('lease-error','prod','fixture-5',1,'text/plain','lease error');
INSERT INTO artifact_copy(artifact_id,store_id,lease_expires_at) VALUES
 ('lease-error','placement-target',now()-interval '1 hour');
CREATE FUNCTION reject_fixture_lease() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.artifact_id='lease-error' THEN RAISE EXCEPTION 'fixture lease expiry failed'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER reject_fixture_lease BEFORE UPDATE ON artifact_copy
 FOR EACH ROW EXECUTE FUNCTION reject_fixture_lease();`)
	var leaseError *pgconn.PgError
	if err := run("1", obs.Error); !errors.As(err, &leaseError) || leaseError.Message != "fixture lease expiry failed" {
		t.Fatalf("pass did not propagate the lease error: %v", err)
	}
}

type countingPlacementTx struct {
	pgx.Tx
	mu     sync.Mutex
	counts map[string]int
}

func (tx *countingPlacementTx) note(sql string) {
	tx.mu.Lock()
	defer tx.mu.Unlock()
	name := "other"
	if rest, ok := strings.CutPrefix(sql, "-- name: "); ok {
		if end := strings.IndexByte(rest, ' '); end > 0 {
			name = rest[:end]
		}
	}
	tx.counts[name]++
}

func (tx *countingPlacementTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.note(sql)
	return tx.Tx.Query(ctx, sql, args...)
}

func (tx *countingPlacementTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.note(sql)
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func (tx *countingPlacementTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.note(sql)
	return tx.Tx.Exec(ctx, sql, args...)
}

// Requirement: pass.placement batches stored artifact copy lookups and caches project placement policies per pass.
// Risk: N+1 DispArtifactCopies, DispRuleConfirmed, and projectContext lookups drive pass.placement p95 over 100 s at ~17k artifacts.
func TestPlacementBatchesCopyAndPolicyQueries(t *testing.T) {
	env := openTestCoordinator(t, false)
	locations, secrets := defaultLocations, storeSecretsDir
	defaultLocations, storeSecretsDir = []string{"batch-target"}, t.TempDir()
	t.Cleanup(func() { defaultLocations, storeSecretsDir = locations, secrets })
	t.Cleanup(func() {
		placementProblems.Lock()
		placementProblems.list = nil
		placementProblems.Unlock()
	})

	runTestPSQL(t, env.binDir, env.sockDir, env.pgPort, env.dbName, "postgres", `
INSERT INTO store(id,host_id,bucket,endpoint,state) VALUES
 ('batch-target','batch-target','fixture','http://127.0.0.1:1','reachable');
INSERT INTO definition(kind,id,path,head,project_id) VALUES
 ('project','batch-loc','config/projects/batch-loc/project.json',1,'batch-loc'),
 ('project','batch-rule','config/projects/batch-rule/project.json',1,'batch-rule');
INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
 ('project','batch-loc',1,'{"settings":{"id":"batch-loc","name":"Batch Loc","contextFiles":[],"artifact_locations":["batch-target"]},"contextFiles":[]}','bl1','editor','person','t'),
 ('project','batch-rule',1,'{"settings":{"id":"batch-rule","name":"Batch Rule","contextFiles":[],"artifact_locations":["batch-target"],"artifact_rules":[{"scope":"prod","older_than_days":0,"action":"move","to":["batch-target"]},{"scope":"bad","older_than_days":-1,"action":"move","to":[]}]},"contextFiles":[]}','br1','editor','person','t');`)

	// Confirm the valid move rule on batch-rule so DispRuleConfirmed returns true.
	validRuleHash := ruleHash(artifactRule{Scope: "prod", OlderThanDays: 0, Action: "move", To: []string{"batch-target"}})
	if _, err := env.ownerStore.pool.Exec(env.ctx, `INSERT INTO artifact_rule_confirmation(project_id,rule_hash,confirmed_by) VALUES ('batch-rule',$1,'owner@trial.test')`, validRuleHash); err != nil {
		t.Fatal(err)
	}

	// Seed 25 stored artifacts across both projects, all already placed on batch-target.
	for i := range 25 {
		pid := "batch-loc"
		if i%2 == 1 {
			pid = "batch-rule"
		}
		aid := fmt.Sprintf("art-batch-%02d", i)
		hash := fmt.Sprintf("hash-batch-%02d", i)
		if _, err := env.ownerStore.pool.Exec(env.ctx, `
WITH a AS (
  INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,state) VALUES ($1,$2,'prod',$3,10,'text/plain','f','stored')
)
INSERT INTO artifact_copy(artifact_id,store_id,project_id,state) VALUES ($1,'batch-target',$2,'verified')`, aid, pid, hash); err != nil {
			t.Fatal(err)
		}
	}

	tx, err := env.ownerStore.pool.Begin(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(env.ctx)
	counter := &countingPlacementTx{Tx: tx, counts: map[string]int{}}
	ctx := context.WithValue(env.ctx, txKey{}, counter)
	ctx, span := obs.Start(ctx, "pass.placement", obs.KindPass)
	queued, deleted, err := runPlacement(ctx, env.app)
	span.End(err)
	if err != nil || queued != 0 || deleted != 0 {
		t.Fatalf("runPlacement = queued=%d deleted=%d err=%v", queued, deleted, err)
	}
	if got := span.Attrs["fleet.placement.artifacts"]; got != "25" {
		t.Fatalf("fleet.placement.artifacts = %q, want 25", got)
	}
	if counter.counts["DispArtifactCopies"] != 0 {
		t.Fatalf("expected 0 per-artifact DispArtifactCopies queries, got %d", counter.counts["DispArtifactCopies"])
	}
	if counter.counts["DispStoredArtifactCopies"] != 1 {
		t.Fatalf("expected 1 bulk DispStoredArtifactCopies query, got %d", counter.counts["DispStoredArtifactCopies"])
	}
	if counter.counts["DispRuleConfirmed"] != 1 {
		t.Fatalf("expected 1 cached DispRuleConfirmed query across 25 artifacts, got %d", counter.counts["DispRuleConfirmed"])
	}
	// Invalid rule problem on batch-rule should be recorded once per project, not 12 times.
	invalidCount := 0
	for _, p := range lastPlacementProblems() {
		var item struct {
			Kind      string  `json:"kind"`
			ProjectID *string `json:"projectId"`
		}
		if err := json.Unmarshal(p.fields, &item); err == nil && item.Kind == "artifact-rule-invalid" {
			invalidCount++
		}
	}
	if invalidCount != 1 {
		t.Fatalf("expected 1 deduplicated artifact-rule-invalid problem, got %d", invalidCount)
	}
}
