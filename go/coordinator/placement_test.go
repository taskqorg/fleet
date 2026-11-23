package main

import (
	"encoding/json"
	"errors"
	"testing"

	"fleet/obs"

	"github.com/jackc/pgx/v5/pgconn"
)

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
