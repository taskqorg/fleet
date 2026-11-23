package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fleet/obs"
)

// Requirement: absence-pass measures time without a completed pass and links the last trace.
// Risk: slow but continuous single-flight work opens periodic false absence findings.
func TestPassAbsenceUsesLastCompletion(t *testing.T) {
	const trace = "12345678901234567890123456789012"
	var age atomic.Int32
	age.Store(261)
	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, _ := io.ReadAll(r.Body)
		if strings.Contains(string(query), "name LIKE 'pass.%'") {
			fmt.Fprintf(w, "{\"Name\":\"pass.placement-fixture\",\"Age\":%d,\"Duration\":104,\"TraceID\":%q}\n", age.Load(), trace)
		}
	}))
	t.Cleanup(ch.Close)
	oldStore := spanStore
	spanStore = &obs.ClickHouse{URL: ch.URL}
	t.Cleanup(func() { spanStore = oldStore })
	passIntervals.Store("placement-fixture", time.Minute)
	t.Cleanup(func() { passIntervals.Delete("placement-fixture") })

	findings, err := spanRules(context.Background())
	if err != nil || len(findings) != 0 {
		t.Fatalf("157 seconds since completion: findings=%+v err=%v", findings, err)
	}
	age.Store(290)
	findings, err = spanRules(context.Background())
	if err != nil || len(findings) != 1 || findings[0].Value != 186 || len(findings[0].Exemplars) != 1 || findings[0].Exemplars[0] != trace {
		t.Fatalf("186 seconds since completion: findings=%+v err=%v", findings, err)
	}
}

// Requirement: #548 — rulesPass reads execution directors before writing under the scoped "problems:rules" lock
// rather than acquiring GLOBAL.
// Risk: holding GLOBAL in rulesPass stalls concurrent GLOBAL writers (and vice versa), while skipping the pre-read
// director lookup misaddresses stuck project execution problems.
func TestRulesPassNoGlobalLock(t *testing.T) {
	env := openTestCoordinator(t, false)
	if _, err := env.ownerStore.pool.Exec(env.ctx, `
		INSERT INTO execution(id, kind, state, observation, project_id, directed_by, started_at, data)
		VALUES ('stuck-exec-548', 'agent_run', 'running', 'confirmed', 'hello-world', 'director@example.com', now() - interval '1 hour', '{}')
	`); err != nil {
		t.Fatal(err)
	}

	globalHeld := make(chan struct{})
	releaseGlobal := make(chan struct{})
	globalDone := make(chan error, 1)
	go func() {
		globalDone <- env.appStore.Write(env.ctx, nil, []string{GLOBAL}, func(_ *Request) error {
			close(globalHeld)
			select {
			case <-releaseGlobal:
				return nil
			case <-time.After(5 * time.Second):
				return errors.New("test timeout waiting for rulesPass while GLOBAL held")
			}
		})
	}()
	<-globalHeld

	passDone := make(chan error, 1)
	go func() {
		passDone <- rulesPass(env.ctx, env.app)
	}()

	select {
	case err := <-passDone:
		close(releaseGlobal)
		if err != nil {
			t.Fatalf("rulesPass while GLOBAL held: %v", err)
		}
	case <-time.After(4 * time.Second):
		close(releaseGlobal)
		t.Fatal("rulesPass blocked waiting on GLOBAL lock")
	}
	if err := <-globalDone; err != nil {
		t.Fatalf("GLOBAL holder: %v", err)
	}

	var addressedTo, projectID string
	if err := env.ownerStore.pool.QueryRow(env.ctx, `
		SELECT coalesce(addressed_to, ''), coalesce(project_id, '')
		FROM question
		WHERE cause = 'problem:execution:stuck-exec-548:stuck' AND state = 'open'
	`).Scan(&addressedTo, &projectID); err != nil {
		t.Fatalf("query stuck problem question: %v", err)
	}
	if addressedTo != "director@example.com" || projectID != "hello-world" {
		t.Fatalf("got addressed_to=%q project_id=%q, want director@example.com / hello-world", addressedTo, projectID)
	}
}
