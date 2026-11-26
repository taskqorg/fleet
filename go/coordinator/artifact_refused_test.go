package main

import "testing"

// Requirement: artifacts/failed for a refused reservation (no artifact row) is accepted, recorded on the execution and
// fails its artifact wait with the reason; risk: 404 "Unknown artifact" and a host job waiting a day (2026-11-26, DAM).
func TestRefusedReservationFailsWaitingExecution(t *testing.T) {
	e := openTestCoordinator(t, false)
	secret := e.registerHost(t, "h-refuse")
	if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO execution (id, kind, state, host_id, project_id, started_at, data, observation, config_ref)
		VALUES ('x-refuse', 'host_job', 'waiting', 'h-refuse', 'hello-world', now(),
		 jsonb_build_object('waitReason','artifacts','waitingSince',(extract(epoch from now())*1000)::bigint,'declaredArtifacts',jsonb_build_array('pages/p1.jpg')),
		 'confirmed', '{}')`); err != nil {
		t.Fatal(err)
	}
	host := map[string]string{"Authorization": "Bearer " + secret}
	body := doc{"hostId": "h-refuse", "artifactId": "job-1", "executionId": "x-refuse", "name": "pages/p1.jpg", "refused": true, "error": "409 Content was purged"}
	if code, reply := e.callJSON(t, "POST", "/api/hosts/artifacts/failed", body, host); code != 200 || reply["state"] != "failed" {
		t.Fatalf("artifacts/failed = %d %v", code, reply)
	}
	var state, code, msg string
	if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT state::text, coalesce(data->'error'->>'code',''), coalesce(data->'error'->>'message','') FROM execution WHERE id='x-refuse'`).Scan(&state, &code, &msg); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || code != "artifact-refused" || msg != "upload refused: pages/p1.jpg: 409 Content was purged" {
		t.Fatalf("execution %s %s %q", state, code, msg)
	}
	other := doc{"hostId": "h-other", "artifactId": "job-2", "executionId": "x-refuse", "name": "pages/p2.jpg", "error": "409"}
	if code, _ := e.callJSON(t, "POST", "/api/hosts/artifacts/failed", other, host); code == 200 {
		t.Fatal("another host's report accepted")
	}
}
