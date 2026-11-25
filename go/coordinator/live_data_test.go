package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"fleet/contract"
)

// Requirement (docs/design-system/pages/live.md, Data): hosts_list answers each host's runs in slot order (oldest start
// first, preparing last), the slot count dispatch uses, and the latest load reading while the host polls. Risk: Live
// draws tiles or gauges from data that disagrees with dispatch, or a stale reading shows as current.
func TestHostsListRunsSlotsAndLoad(t *testing.T) {
	env := openTestCoordinator(t, false)
	const host = "live-mac"
	secret := env.registerHost(t, host)
	dispatchTestPoll(t, env, host, secret, &contract.HostLoad{Load1: ptr(3.0), Cpus: ptr(int64(8)), MemFree: ptr(4e9), MemTotal: ptr(16e9)})
	// A heartbeat span adds the free disk; the poll's reading keeps it.
	env.app.recordHostLoad(host, &hostLoadObservation{Load1: ptr(3.5), Cpus: ptr(int64(8)), DiskFree: ptr(1e11), UpdatedAt: nowMs()}, false, true)
	dispatchTestPoll(t, env, host, secret, &contract.HostLoad{Load1: ptr(4.0), Cpus: ptr(int64(8)), MemFree: ptr(4e9), MemTotal: ptr(16e9)})
	start := time.Now().Add(-time.Hour)
	if err := env.ownerStore.Write(env.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		return exec(q, `INSERT INTO execution(id, kind, state, host_id, started_at) VALUES
			('run-late', 'agent_run', 'running', $1, $2), ('run-early', 'agent_run', 'running', $1, $3),
			('run-new', 'agent_run', 'preparing', $1, NULL), ('run-done', 'agent_run', 'succeeded', $1, $3)`,
			host, start.Add(time.Minute), start)
	}); err != nil {
		t.Fatal(err)
	}
	var reply contract.HostsListReply
	env.opInto(t, "hosts_list", map[string]any{}, env.ownerHeaders(), &reply)
	var row *contract.HostRow
	for i := range reply.Hosts {
		if reply.Hosts[i].Id == host {
			row = &reply.Hosts[i]
		}
	}
	if row == nil {
		t.Fatalf("no row for %s: %+v", host, reply.Hosts)
	}
	ids := []string{}
	for _, r := range row.Runs {
		ids = append(ids, r.Id)
	}
	if strings.Join(ids, ",") != "run-early,run-late,run-new" || row.Runs[2].StartedAt != nil || row.Runs[2].State != "preparing" {
		t.Fatalf("runs = %+v, want run-early, run-late, run-new (preparing, no start)", row.Runs)
	}
	if row.SlotLimit == nil || *row.SlotLimit != 8 {
		t.Fatalf("slotLimit = %v, want the CPU count 8 (no maxConcurrent)", row.SlotLimit)
	}
	if l := row.Load; l == nil || valueOr(l.Load1) != 4.0 || valueOr(l.DiskFree) != 1e11 || valueOr(l.MemTotal) != 16e9 {
		t.Fatalf("load = %+v, want load1 4 from the last poll and diskFree from the heartbeat", row.Load)
	}
	// A reading older than two minutes is not current: no load, and no CPU-count slot limit.
	old := nowMs() - hostLoadStaleMs - 1
	env.app.pollMu.Lock()
	env.app.heartbeats[host].load.UpdatedAt = old
	env.app.pollMu.Unlock()
	if err := env.ownerStore.Write(env.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		return exec(q, `UPDATE host_observation SET payload = jsonb_set(payload, '{load,updatedAt}', to_jsonb($2::bigint)) WHERE id = $1`, host, old)
	}); err != nil {
		t.Fatal(err)
	}
	reply = contract.HostsListReply{}
	env.opInto(t, "hosts_list", map[string]any{}, env.ownerHeaders(), &reply)
	for _, h := range reply.Hosts {
		if h.Id == host && (h.Load != nil || h.SlotLimit != nil) {
			t.Fatalf("stale reading shown: load %+v, slotLimit %v", h.Load, h.SlotLimit)
		}
	}
}

// Requirement (docs/designs/fleet-people-access.md, Wall viewer): a viewer reads every read operation and the stream,
// and every other request is refused before it runs. Risk: a wall screen's credential changes state.
func TestViewerOnlyReads(t *testing.T) {
	env := openTestCoordinator(t, false)
	people := `[{"email": "owner@trial.test", "name": "Owner", "roles": ["operator", "architect"], "owner": true},
		{"email": "wall@trial.test", "name": "Wall", "roles": ["viewer"], "grants": [{"kind": "project", "id": "*"}]}]`
	if _, err := env.app.setPeople(env.ctx, []byte(people), true, nil); err != nil {
		t.Fatal(err)
	}
	wall := map[string]string{"Authorization": "Bearer " + env.browserSecret, "X-Fleet-Person": "wall@trial.test"}
	for _, op := range []string{"hosts_list", "me", "executions_list", "executions_runners"} {
		if code, body := env.callJSON(t, "POST", "/api/ops/"+op, map[string]any{}, wall); code != 200 {
			t.Fatalf("viewer %s = %d %v, want 200", op, code, body)
		}
	}
	for _, path := range []string{"/api/ops/task_create", "/api/ops/hosts_register", "/api/ops/telemetry_summary", "/api/sessions/s-1/messages"} {
		if code, body := env.callJSON(t, "POST", path, map[string]any{"title": "x", "id": "h"}, wall); code != 403 || !strings.Contains(fmt.Sprint(body), "viewer") {
			t.Fatalf("viewer POST %s = %d %v, want 403 for a viewer", path, code, body)
		}
	}
	for _, bad := range []string{
		`[{"email": "owner@trial.test", "name": "Owner", "roles": ["operator"], "owner": true}, {"email": "w@trial.test", "name": "W", "roles": ["viewer", "operator"], "grants": []}]`,
		`[{"email": "owner@trial.test", "name": "Owner", "roles": ["operator"], "owner": true}, {"email": "w@trial.test", "name": "W", "roles": ["viewer"], "grants": [{"kind": "*"}]}]`,
	} {
		if _, err := env.app.setPeople(env.ctx, []byte(bad), true, nil); err == nil || !strings.Contains(err.Error(), "viewer") {
			t.Fatalf("setPeople(%s) = %v, want a viewer refusal", bad, err)
		}
	}
}

// Requirement (D17, FLT-39, FLT-40): an original artifact and a bot get an R and a B code that entity_resolve resolves;
// other artifact roles get none. The card and artifact_get name the result by its code and a file name without the
// UUID or hash its writer put in it. Risk: a result or bot is still addressed by its UUID only.
func TestResultAndBotCodes(t *testing.T) {
	env := openTestCoordinator(t, false)
	if err := env.ownerStore.Write(env.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		return exec(q, `INSERT INTO artifact(id, project_id, scope, content_hash, size, media_type, name, role) VALUES
			('a-orig', 'fleet', 'prod', 'h1', 1, 'application/x-ndjson',
			 'mcp-lifecycle-5eb2ce291c7d227dd684ec83f9ddc05776e2fe9a0c4e62927b4592383e66fb28-749bcb9e-f21a-4ef4-9584-f44074e048b2.jsonl', 'original'),
			('a-thumb', 'fleet', 'prod', 'h2', 1, 'image/png', 'report.png', 'thumbnail');
			INSERT INTO bot(id, project_id, director, revision) VALUES ('bot-1', 'fleet', 'owner@trial.test', 1)`)
	}); err != nil {
		t.Fatal(err)
	}
	codes := map[string]*string{}
	if err := env.ownerStore.Read(env.ctx, nil, func(q *Request) error {
		for _, id := range []string{"a-orig", "a-thumb"} {
			var c *string
			if err := q.tx.QueryRow(q.ctx(), `SELECT code FROM artifact WHERE id=$1`, id).Scan(&c); err != nil {
				return err
			}
			codes[id] = c
		}
		var c *string
		err := q.tx.QueryRow(q.ctx(), `SELECT code FROM bot WHERE id='bot-1'`).Scan(&c)
		codes["bot-1"] = c
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if deref(codes["a-orig"]) != "R-FLT-1" || codes["a-thumb"] != nil || deref(codes["bot-1"]) != "B-FLT-1" {
		t.Fatalf("codes = orig %v thumb %v bot %v, want R-FLT-1, none, B-FLT-1", deref(codes["a-orig"]), codes["a-thumb"], deref(codes["bot-1"]))
	}
	for code, id := range map[string]string{"r-flt-1": "a-orig", "B-FLT-1": "bot-1"} {
		var got contract.EntityCode
		env.opInto(t, "entity_resolve", map[string]any{"code": code}, env.ownerHeaders(), &got)
		if got.Id != id {
			t.Fatalf("entity_resolve(%s) = %+v, want %s", code, got, id)
		}
	}
	var card contract.EntityCardReply
	env.opInto(t, "entity_card", map[string]any{"code": "R-FLT-1"}, env.ownerHeaders(), &card)
	if card.Id != "a-orig" || deref(card.Code) != "R-FLT-1" || card.Title != "mcp-lifecycle.jsonl" || deref(card.Path) != "/results/R-FLT-1" {
		t.Fatalf("entity_card(R-FLT-1) = %+v", card)
	}
	var view contract.ArtifactView
	env.opInto(t, "artifact_get", map[string]any{"artifactId": "a-orig"}, env.ownerHeaders(), &view)
	if deref(view.Code) != "R-FLT-1" {
		t.Fatalf("artifact_get code = %v, want R-FLT-1", deref(view.Code))
	}
	for name, want := range map[string]string{"report.md": "report.md", "T-FLT-62-resumption-def7bd70.md": "T-FLT-62-resumption-def7bd70.md",
		"a-0123456789abcd-fedcba9876543210-b.txt": "a-b.txt", "749bcb9e-f21a-4ef4-9584-f44074e048b2.json": ""} {
		if got := resultLabel(name); got != want {
			t.Fatalf("resultLabel(%q) = %q, want %q", name, got, want)
		}
	}
}

// opInto: POST /api/ops/<op> as headers say, decoded into out; any status but 200 fails the test.
func (e *testCoordEnv) opInto(t *testing.T, op string, body any, headers map[string]string, out any) {
	t.Helper()
	code, got := e.callJSON(t, "POST", "/api/ops/"+op, body, headers)
	if code != 200 {
		t.Fatalf("%s = %d %v", op, code, got)
	}
	raw, _ := json.Marshal(got)
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s reply: %v", op, err)
	}
}
