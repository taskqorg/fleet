package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A pending update must not claim a new command, but terminal delivery must drain first.
// Requirement: executor replacement waits for owned work and retained delivery; risk: a race interrupts active work or admits a start during replacement.
func TestUpdateDrainAdmissionAndAcknowledgement(t *testing.T) {
	e, root := scriptExecutor(t)
	lastSignal = nil
	t.Cleanup(func() {
		lastSignal = nil
		if e.activity != nil {
			e.activity.Close()
		}
	})
	c := M{"id": "active", "executionId": "active", "sessionId": "active", "kind": "script", "payload": M{"command": "sleep 0.1"}}
	if err := e.run(c); err != nil {
		t.Fatal(err)
	}
	target := "1.261113.20"
	lastSignal = &updateSignal{Target: &target, Running: "1.261113.19", AutoUpdate: "on"}
	next := M{"id": "next", "executionId": "next", "sessionId": "next", "kind": "readiness", "payload": M{"project": M{}}}
	if err := e.run(next); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.commandBody("next", false); ok {
		t.Fatal("claimed new command during drain")
	}
	if err := e.drainUpdate(); err != nil {
		t.Fatal(err)
	}
	if e.updateReady.Load() != nil {
		t.Fatal("live run declared ready")
	}
	<-e.live["active"].exited
	for len(e.live) > 0 {
		if err := e.tick(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if e.updateReady.Load() != nil {
		t.Fatal("unacknowledged terminal receipt declared ready")
	}
	if _, err := e.db.Exec("DELETE FROM outbox"); err != nil {
		t.Fatal(err)
	}
	if err := e.tick(); err != nil {
		t.Fatal(err)
	}
	if e.updateReady.Load() != nil {
		t.Fatal("pending artifact transfers declared ready")
	}
	if _, err := e.db.Exec("UPDATE transfers SET state='done'"); err != nil {
		t.Fatal(err)
	}
	if err := e.tick(); err != nil {
		t.Fatal(err)
	}
	if p := e.updateReady.Load(); p == nil || *p != target {
		t.Fatal("idle target not ready")
	}
	f, err := os.OpenFile(filepath.Join(root, "update", "activity.lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal("drained lease retained", err)
	}
	lastSignal.AutoUpdate = "off"
	if err := e.run(next); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.commandBody("next", false); ok {
		t.Fatal("racing admission ignored exclusive updater")
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if err := e.run(next); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.commandBody("next", false); !ok {
		t.Fatal("held recovery lost queued next command")
	}
}

// Requirement: executor replacement waits for owned work and retained delivery; risk: a race interrupts active work or admits a start during replacement.
func TestUpdateDrainTargetAndHold(t *testing.T) {
	for _, x := range []struct {
		target, running, auto string
		want                  bool
	}{
		{"1.261113.20", "1.261113.19", "on", true}, {"1.261113.10", "1.261113.9", "on", true}, {"1.261113.9", "1.261113.19", "on", false}, {"bad", "dev", "on", false}, {"1.261113.20", "1.261113.19", "off", false}, {"1.261113.20", "1.261113.19", time.Now().Add(time.Hour).UTC().Format(time.RFC3339), false},
	} {
		if got := updatePending(&updateSignal{Target: &x.target, Running: x.running, AutoUpdate: x.auto}); got != x.want {
			t.Fatalf("%+v got %v", x, got)
		}
	}
}

// Requirement: executor replacement waits for owned work and retained delivery; risk: a race interrupts active work or admits a start during replacement.
func TestUpdateTargetAndStartSamePollRemainUnclaimed(t *testing.T) {
	e, _ := scriptExecutor(t)
	lastSignal = nil
	t.Cleanup(func() {
		lastSignal = nil
		if e.activity != nil {
			e.activity.Close()
		}
	})
	command := M{"id": "queued", "executionId": "queued", "sessionId": "queued", "kind": "readiness", "payload": M{"project": M{}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(M{"target": "9.991231.0", "autoUpdate": "on", "commands": []any{command}})
	}))
	defer server.Close()
	e.url = server.URL
	if err := e.exchange(); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.commandBody("queued", false); ok {
		t.Fatal("same-poll start claimed before drain fence")
	}
	if err := e.tick(); err != nil {
		t.Fatal(err)
	}
	if p := e.updateReady.Load(); p == nil || *p != "9.991231.0" {
		t.Fatal("unclaimed coordinator start blocked update readiness")
	}
	// The new-version executor receives the retained command and may claim it normally.
	target := "9.991231.0"
	lastSignal = nil
	if err := writeUpdateSignal(e.root, updateSignal{Target: &target, RestartTarget: &target, Running: "9.991231.0", AutoUpdate: "on"}); err != nil {
		t.Fatal(err)
	}
	if err := e.run(command); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.commandBody("queued", false); !ok {
		t.Fatal("new executor lost retained queued command")
	}
	b, err := os.ReadFile(filepath.Join(e.root, "update", "update.json"))
	if err != nil {
		t.Fatal(err)
	}
	var signal updateSignal
	if err = json.Unmarshal(b, &signal); err != nil {
		t.Fatal(err)
	}
	if signal.RestartTarget != nil {
		t.Fatal("new command left durable stale readiness")
	}
}

// Requirement: only pending delivery blocks update drain; risk: old rejections deadlock idle hosts or recoverable authentication/required transfers are lost.
func TestUpdateDrainRetainsAuditButWaitsForRecoverableDelivery(t *testing.T) {
	e, root := scriptExecutor(t)
	lastSignal = nil
	t.Cleanup(func() {
		lastSignal = nil
		if e.activity != nil {
			e.activity.Close()
		}
	})
	target := "1.261113.22"
	lastSignal = &updateSignal{Target: &target, Running: "1.261113.21", AutoUpdate: "on"}
	for _, status := range []int{400, 409} {
		if _, err := e.db.Exec("INSERT INTO outbox_failed(route,body,status,reason,failed_at) VALUES(?,?,?,?,?)", "/api/hosts/receipts", enc(M{"commandId": "historical", "executionId": "historical", "status": "completed"}), status, "retained rejection", 123); err != nil {
			t.Fatal(err)
		}
	}
	audit := func() string {
		t.Helper()
		rows, err := e.db.Query("SELECT route,body,status,reason,failed_at FROM outbox_failed WHERE status<>401 ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []any
		for rows.Next() {
			var route, body, reason string
			var status, at int
			if err := rows.Scan(&route, &body, &status, &reason, &at); err != nil {
				t.Fatal(err)
			}
			out = append(out, []any{route, body, status, reason, at})
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return enc(out)
	}
	before := audit()
	lease := func(wantReady bool) {
		t.Helper()
		if err := e.drainUpdate(); err != nil {
			t.Fatal(err)
		}
		ready := e.updateReady.Load()
		if (ready != nil) != wantReady {
			t.Fatalf("restart readiness=%v want %v", ready, wantReady)
		}
		f, err := os.OpenFile(filepath.Join(root, "update", "activity.lock"), os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if wantReady {
			if err != nil {
				t.Fatal("idle exclusive lease denied", err)
			}
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		} else if !errors.Is(err, syscall.EWOULDBLOCK) {
			t.Fatal("pending work did not hold shared activity lease", err)
		}
		if audit() != before {
			t.Fatal("historical rejection audit changed")
		}
	}
	lease(true)
	receipt := M{"hostId": "fake", "commandId": "recoverable", "executionId": "recoverable", "status": "completed"}
	if _, err := e.db.Exec("INSERT INTO outbox_failed(route,body,status,reason,failed_at) VALUES(?,?,?,?,?)", "/api/hosts/receipts", enc(receipt), 401, "host authentication", 456); err != nil {
		t.Fatal(err)
	}
	lease(false)
	// Use the existing startup recovery, then require the actual queued receipt acknowledgement.
	if err := requeueUnauthorized(e.db); err != nil {
		t.Fatal(err)
	}
	if count(e.db, "outbox") != 1 {
		t.Fatal("recoverable receipt not requeued")
	}
	lease(false)
	url, _ := serve(t, func(string, M) int { return 200 })
	if err := flush(e.db, url, "fixture", "fake"); err != nil {
		t.Fatal(err)
	}
	lease(true)
	if err := queue(e.db, "/api/hosts/receipts", receipt); err != nil {
		t.Fatal(err)
	}
	lease(false)
	if err := flush(e.db, url, "fixture", "fake"); err != nil {
		t.Fatal(err)
	}
	lease(true)
	body := M{"required": true}
	if _, err := e.db.Exec("INSERT INTO transfers(id,kind,body,state,created_at) VALUES('required','upload',?,'running',0)", enc(body)); err != nil {
		t.Fatal(err)
	}
	lease(false)
	if err := settleJob(e.db, "fake", "required", "upload", body, 0, M{"state": "stored"}, nil); err != nil {
		t.Fatal(err)
	}
	lease(true)
	if count(e.db, "outbox_failed") != 2 {
		t.Fatal("terminal rejection archive was erased")
	}
}
