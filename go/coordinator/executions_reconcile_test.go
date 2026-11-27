package main

import (
	"slices"
	"testing"
	"time"
)

// T-FLT-141: rejection of a start acknowledgement or a control receipt is
// insufficient evidence to abandon a running worker.
func TestReconcileRejectedWorkReceipt(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO session (id) VALUES ('terminal-session')`)
	exec(`INSERT INTO execution (id,kind,state,observation,host_id,session_id,data)
	 VALUES ('legacy','agent_run','running','confirmed','windows',NULL,'{}'),
	        ('started','agent_run','running','confirmed','windows',NULL,'{}'),
	        ('control','agent_run','running','confirmed','windows',NULL,'{}'),
	        ('terminal','agent_run','running','confirmed','windows','terminal-session','{}')`)
	exec(`INSERT INTO command (id,execution_id,host_id,kind,status)
	 VALUES ('started-cmd','started','windows','start','started'),
	        ('control-cmd','control','windows','steer','accepted'),
	        ('terminal-cmd','terminal','windows','start','started')`)
	exec(`INSERT INTO event (id,execution_id,delta,type,event_seq,payload)
	 VALUES ('legacy-rejected','legacy',false,'outbox.failed',1,'{"data":{"route":"/api/hosts/receipts"}}'),
	        ('start-rejected','started',false,'outbox.failed',1,'{"data":{"route":"/api/hosts/receipts","commandId":"started-cmd","receiptStatus":"started"}}'),
	        ('steer-rejected','control',false,'outbox.failed',1,'{"data":{"route":"/api/hosts/receipts","commandId":"control-cmd","receiptStatus":"completed"}}'),
	        ('work-rejected','terminal',false,'outbox.failed',1,'{"data":{"route":"/api/hosts/receipts","commandId":"terminal-cmd","receiptStatus":"failed"}}')`)
	var rep struct {
		Reconciled []string `json:"reconciled"`
	}
	e.opInto(t, "executions_reconcile", map[string]any{}, e.ownerHeaders(), &rep)
	if !slices.Equal(rep.Reconciled, []string{"terminal"}) {
		t.Fatalf("reconciled %v, want only the rejected terminal work receipt", rep.Reconciled)
	}
}

func TestStalledTurnShutdownRequestsHostCleanupOnce(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	exec(`INSERT INTO host_observation (id,last_seen_at,payload) VALUES
	 ('windows',now(),'{"activeExecutions":["stalled","still-working","closed","recent"]}')`)
	for _, id := range []string{"stalled", "still-working", "closed", "recent"} {
		exec(`INSERT INTO execution (id,kind,state,observation,harness,host_id,last_seen_at,data)
		 VALUES ($1,'agent_run','running','confirmed','codex','windows',$2,'{}')`, id, old)
		exec(`INSERT INTO command (id,execution_id,host_id,kind,status,payload)
		 VALUES ($1||'-start',$1,'windows','start','started','{}')`, id)
		exec(`INSERT INTO event (id,execution_id,delta,type,event_seq,updated_at,payload)
		 VALUES ($1||'-reject',$1,false,'outbox.failed',1,$2,'{"data":{"route":"/api/hosts/events"}}')`, id, old)
	}
	for _, id := range []string{"stalled", "closed", "recent"} {
		at := old
		if id == "recent" {
			at = time.Now()
		}
		exec(`INSERT INTO event (id,execution_id,delta,type,event_seq,updated_at,payload)
		 VALUES ($1||'-turned',$1,false,'turn.completed',2,$2,'{"data":{"fleetNormalized":{"lifecycle":"turn.completed"}}}')`, id, at)
	}
	exec(`INSERT INTO event (id,execution_id,delta,type,event_seq,payload)
	 VALUES ('closed-exit','closed',false,'process.closed',3,'{}')`)

	for range 2 {
		if err := requestStalledTurnShutdowns(e.ctx, e.app); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := e.ownerStore.pool.Query(e.ctx, `SELECT c.execution_id,c.status::text,e.state::text
	 FROM command c JOIN execution e ON e.id=c.execution_id WHERE c.kind='stop'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var stops [][3]string
	for rows.Next() {
		var row [3]string
		if err := rows.Scan(&row[0], &row[1], &row[2]); err != nil {
			t.Fatal(err)
		}
		stops = append(stops, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stops, [][3]string{{"stalled", "pending", "running"}}) {
		t.Fatalf("stops = %v, want one pending stop while the stalled execution awaits host proof", stops)
	}
}

// Requirement: executions_reconcile reconciles a preparing/open pipeline_step whose parent pipeline_run already reached
// a terminal state, leaving no dangling step in preparing (2026-11-10 pipeline_step cleanup).
func TestReconcileTerminalParentPipelineStep(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	old := time.Now().UTC().Add(-2 * time.Hour)
	exec(`INSERT INTO execution (id, kind, state, observation, started_at, ended_at, updated_at, problem, data, config_ref)
	 VALUES ('rec-run', 'pipeline_run', 'failed', 'confirmed', $1, $1, $1, 'step:cancelled', '{}', '{}')`, old)
	exec(`INSERT INTO execution (id, kind, state, observation, parent_execution_id, updated_at, data, config_ref)
	 VALUES ('rec-run:step:3', 'pipeline_step', 'preparing', 'confirmed', 'rec-run', $1, '{"id":"check","index":3,"kind":"script","state":"pending"}', '{}')`, old)

	var rep struct {
		Reconciled []string `json:"reconciled"`
	}
	e.opInto(t, "executions_reconcile", map[string]any{}, e.ownerHeaders(), &rep)
	if len(rep.Reconciled) != 1 || rep.Reconciled[0] != "rec-run:step:3" {
		t.Fatalf("reconciled: %+v, want [rec-run:step:3]", rep.Reconciled)
	}

	var state, problem string
	if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT state::text, coalesce(problem, '') FROM execution WHERE id = 'rec-run:step:3'`).Scan(&state, &problem); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || problem != "unknown:abandoned" {
		t.Fatalf("step state=%q problem=%q, want failed / unknown:abandoned", state, problem)
	}
}
