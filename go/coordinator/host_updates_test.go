package main

import (
	"testing"

	"fleet/contract"
	"fleet/harnessrelease"
)

// Requirement: the update history keeps one row per change of a host's reported result (a repeated pending result is
// not a new row), each with the components that release changed against the host's last switch, journals a switch
// once, and reads newest first one page at a time. Risk: a poll every few seconds floods the history, or a page skips
// or repeats a row.
func TestHostUpdateHistory(t *testing.T) {
	e := openTestCoordinator(t, false)
	if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO host (id, host_id, holder) VALUES ('hu-mac', 'hu-mac', $1)`, testOwnerEmail); err != nil {
		t.Fatal(err)
	}
	running := "1.0.1"
	clis := func(codex string) []contract.HarnessVersion {
		return []contract.HarnessVersion{{Harness: "codex", Expected: codex}}
	}
	report := func(version string, state contract.UpdateState, reason, codex string) {
		t.Helper()
		r := contract.UpdateResult{Version: version, State: state, At: "2026-11-26T10:00:00Z"}
		if reason != "" {
			r.Error = &reason
		}
		if err := e.app.store.Write(e.ctx, nil, []string{"host:hu-mac"}, func(q *Request) error {
			return hostUpdateRecord(q, "hu-mac", testOwnerEmail, "darwin", &running, clis(codex), r)
		}); err != nil {
			t.Fatal(err)
		}
	}
	report("1.0.1", contract.UpdateStateOk, "", "0.168.0")
	running = "1.0.2"
	report("1.0.2", contract.UpdateStatePending, "executor work is draining", "0.168.0")
	report("1.0.2", contract.UpdateStatePending, "executor work is draining", "0.168.0") // the same result again
	report("1.0.2", contract.UpdateStateOk, "", "0.169.0")

	var all contract.HostUpdateHistoryReply
	e.opInto(t, "host_update_history", map[string]any{"hostId": "hu-mac"}, e.ownerHeaders(), &all)
	if len(all.Entries) != 3 || all.NextCursor != nil {
		t.Fatalf("entries %+v, want 3: ok 1.0.2, pending 1.0.2, ok 1.0.1", all.Entries)
	}
	top := all.Entries[0]
	if top.Version != "1.0.2" || top.State != contract.UpdateStateOk || len(top.Brought) != 1 || top.Brought[0] != (contract.ComponentVersion{Component: "cli/codex", Version: "0.169.0"}) {
		t.Fatalf("newest: %+v", top)
	}
	if p := all.Entries[1]; p.State != contract.UpdateStatePending || p.Reason == nil || *p.Reason != "executor work is draining" {
		t.Fatalf("pending: %+v", p)
	}
	var page contract.HostUpdateHistoryReply
	e.opInto(t, "host_update_history", map[string]any{"hostId": "hu-mac", "limit": 2}, e.ownerHeaders(), &page)
	if len(page.Entries) != 2 || page.NextCursor == nil {
		t.Fatalf("first page: %+v", page)
	}
	var rest contract.HostUpdateHistoryReply
	e.opInto(t, "host_update_history", map[string]any{"hostId": "hu-mac", "limit": 2, "before": *page.NextCursor}, e.ownerHeaders(), &rest)
	if len(rest.Entries) != 1 || rest.Entries[0].Version != "1.0.1" || rest.NextCursor != nil {
		t.Fatalf("second page: %+v", rest)
	}
	var switches int
	if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT count(*) FROM audit_log WHERE target='host:hu-mac' AND field='release'`).Scan(&switches); err != nil {
		t.Fatal(err)
	}
	if switches != 1 {
		t.Fatalf("journalled switches %d, want 1 (1.0.1 to 1.0.2; the first row only starts the history)", switches)
	}
}

// Requirement: a host cell of a component row says whether the host runs the pin, installs it, refused it or keeps its
// own. Risk: a host behind its pin reads as aligned.
func TestComponentHostCell(t *testing.T) {
	obs := func(v string) []contract.HostHarnessVersion {
		return []contract.HostHarnessVersion{{Harness: "codex", Expected: v, Observed: &v}}
	}
	h := contract.HostRow{Id: "mac", HarnessVersions: obs("0.168.0")}
	if c := componentHost(harnessrelease.CLIs, "codex", true, "0.168.0", h, pinsRegression{}); c.State != contract.ComponentHostStatePinned {
		t.Fatalf("on the pin: %+v", c)
	}
	if c := componentHost(harnessrelease.CLIs, "codex", true, "0.169.0", h, pinsRegression{}); c.State != contract.ComponentHostStateBehind || *c.Observed != "0.168.0" {
		t.Fatalf("behind: %+v", c)
	}
	h.Update = []byte(`{"version":"9.9.9","state":"pending","at":"2026-11-26T10:00:00Z"}`)
	if c := componentHost(harnessrelease.CLIs, "codex", true, "0.169.0", h, pinsRegression{}); c.State != contract.ComponentHostStateBehind {
		t.Fatalf("pending another release is not installing this pin: %+v", c)
	}
	next := map[string]string{"cli/codex": "2", "tool/node": "1", "cli/uv": "1"}
	if b := componentsBrought(next, map[string]string{"cli/codex": "1"}, false); len(b) != 2 || b["tool/node"] != "" {
		t.Fatalf("brought %v: codex changed, uv is new to the pinned CLIs, tools are unknown to the base", b)
	}
	if b := componentsBrought(next, map[string]string{"cli/codex": "1"}, true); len(b) != 1 || b["cli/codex"] != "2" {
		t.Fatalf("brought %v: a reported base compares only what it names", b)
	}
	// Installing: a pending update to the release that pins the version (this coordinator's).
	h.Update = []byte(`{"version":"` + ownRelease().Version + `","state":"pending","at":"2026-11-26T10:00:00Z"}`)
	if c := componentHost(harnessrelease.CLIs, "codex", true, "0.169.0", h, pinsRegression{}); c.State != contract.ComponentHostStateInstalling {
		t.Fatalf("installing: %+v", c)
	}
	if c := componentHost(harnessrelease.CLIs, "codex", false, "0.169.0", h, pinsRegression{}); c.State != contract.ComponentHostStateOwn {
		t.Fatalf("pins off: %+v", c)
	}
	flagged := pinsRegression{Harness: "codex", Version: "0.168.0", Host: "mac", Failed: 6, Total: 14}
	if c := componentHost(harnessrelease.CLIs, "codex", true, "0.168.0", h, flagged); c.State != contract.ComponentHostStateFailing || *c.Failed != 6 {
		t.Fatalf("failing: %+v", c)
	}
	if c := componentHost(harnessrelease.CLIs, "codex", true, "", h, pinsRegression{}); c.State != contract.ComponentHostStateUnknown {
		t.Fatalf("nothing pinned for the target is no drift: %+v", c)
	}
	if !componentOlder("0.168.0", "0.169.0") || componentOlder("2.1.331", "2.1.326") || !componentOlder("1.5.10", "1.5.11") {
		t.Fatal("component versions compare as x.y.z (a revert proposes an older one)")
	}
	if !pinsFailureNames("windows claude claude-opus-5-5: run failed", "claude") || pinsFailureNames("windows claude m: x", "codex") {
		t.Fatal("a trial failure names its CLI")
	}
}
