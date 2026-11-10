package main

// Area A: host events and transport recovery (recovery.ts), and GET /api/executions/{id}/events. Events and stream deltas
// (kind event-delta) share one id and seq space per execution. A batch is validated whole, then stored in the execution's
// scope, in parallel with other executions. A seq taken by another event becomes a retained transport conflict that the
// operator may authorize; the owning host's resend then lands at a reserved seq.

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"reflect"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

func init() {
	longRoute("POST /api/hosts/events", func(q *Request) (any, error) { return q.app.ingest(q) })
	route("GET /api/executions/{id}/events", executionEvents)
	operation("execution_events", true, func(q *Request) (any, error) {
		q.params = []string{astr(q.obj()["executionId"])}
		return executionEvents(q)
	})
	operation("transport_inspect", true, func(q *Request) (any, error) { return transportInspect(q, q.obj()) })
	operation("transport_authorize", false, func(q *Request) (any, error) { return transportAuthorize(q, q.obj()) })
	route("POST /api/recovery/{name}", func(q *Request) (any, error) {
		switch q.r.PathValue("name") {
		case "transport_inspect":
			return transportInspect(q, q.obj())
		case "transport_authorize":
			return transportAuthorize(q, q.obj())
		}
		return nil, fail(404, "Unknown recovery operation")
	})
	route("POST /api/recovery-tools", recoveryTools)
}

func executionEvents(q *Request) (any, error) {
	id := q.r.PathValue("id")
	if id == "" && len(q.params) > 0 {
		id = q.params[0]
	}
	if _, err := aget(q, id, "execution"); err != nil {
		return nil, err
	}
	es, err := aRows(q, "SELECT data FROM document WHERE kind='event' AND data->>'executionId'=$1 AND (data->>'type') IS DISTINCT FROM 'execution.ended' ORDER BY (data->'seq')::numeric, seq", id)
	return doc{"events": es}, err
}

// frameHash: sha256 of the canonical JSON of a value.
func frameHash(v any) string {
	h := sha256.Sum256([]byte(canonical(v)))
	return hex.EncodeToString(h[:])
}

func eventsWhere(q *Request, f doc) ([]doc, error) {
	return aRows(q, "SELECT data FROM document WHERE kind IN ('event','event-delta') AND data @> $1 ORDER BY seq", js(f))
}

func eventKind(v doc) string {
	if mapOf(mapOf(v["data"])["fleetNormalized"])["deltaOf"] != nil {
		return "event-delta"
	}
	return "event"
}

// same: deep equality of two JSON values as decoded.
func same(a, b any) bool { return reflect.DeepEqual(norm(a), norm(b)) }

func norm(v any) any {
	var out any
	jsonRoundTrip(v, &out)
	return out
}

func safeSeq(v any) (int64, bool) {
	f, ok := v.(float64)
	if !ok || f != math.Trunc(f) || f < 0 || f > 1<<53-1 {
		return 0, false
	}
	return int64(f), true
}

type preparedEvent struct {
	value    doc
	conflict doc
}

func (a *App) ingest(q *Request) (any, error) {
	b := q.obj()
	id, ok := b["executionId"].(string)
	if !ok {
		return nil, fail(400, "Expected executionId")
	}
	// As app.ts store.atomic: a refused batch still commits the transport conflict it recorded (the batch's events are
	// written only after the whole batch is valid).
	var out any
	var refused error
	err := a.store.Tx(q.ctx(), nil, true, "execution:"+id, func(tx pgx.Tx) error {
		q.tx = tx
		var err error
		out, err = ingestIn(q, b)
		var he *HTTPError
		if asHTTP(err, &he) {
			refused, err = err, nil
		}
		return err
	})
	if err == nil {
		err = refused
	}
	return out, err
}

func ingestIn(q *Request, b doc) (any, error) {
	if err := ownedBody(q, b); err != nil {
		return nil, err
	}
	evs, ok := b["events"].([]any)
	if !ok || len(evs) > 1000 {
		return nil, fail(400, "Invalid events")
	}
	xid := astr(b["executionId"])
	execution, err := aget(q, xid, "execution")
	if err != nil {
		return nil, err
	}
	prepared := []preparedEvent{}
	for _, raw := range evs {
		ev := mapOf(raw)
		eid, isStr := ev["id"].(string)
		seq, okSeq := safeSeq(ev["seq"])
		if !isStr || eid == "" || len([]rune(eid)) > 300 || ev["type"] == "execution.ended" || !okSeq {
			return nil, fail(400, "Invalid event identity")
		}
		key := "event:" + xid + ":" + eid
		value := doc{}
		for k, v := range ev {
			value[k] = v
		}
		value["id"], value["executionId"], value["sessionId"] = key, xid, execution["sessionId"]
		prevs, err := eventsWhere(q, doc{"id": key})
		if err != nil {
			return nil, err
		}
		var previous doc
		for _, p := range prevs {
			if p["id"] == key {
				previous = p
			}
		}
		at, err := aRows(q, "SELECT data FROM document WHERE kind IN ('event','event-delta') AND data->>'executionId'=$1 AND data->'seq'=to_jsonb($2::bigint)", xid, seq)
		if err != nil {
			return nil, err
		}
		var collision doc
		for _, x := range at {
			if x["id"] != key {
				collision = x
				break
			}
		}
		if previous != nil && !same(previous, value) {
			return nil, fail(409, "Event conflict")
		}
		if collision == nil {
			prepared = append(prepared, preparedEvent{value: value})
			continue
		}
		digest := frameHash(ev)
		cid := "transport:" + xid + ":" + digest
		c, err := maybe(q, cid, "transport-conflict")
		if err != nil {
			return nil, err
		}
		if c == nil {
			c = doc{"id": cid, "executionId": xid, "hostId": b["hostId"], "originalEventId": eid, "originalSequence": seq, "frameSha256": digest, "original": ev,
				"collisionEventId": collision["id"], "supported": previous == nil && ev["type"] == "process.closed" && collision["type"] == "resource.observation",
				"state": "blocked", "observedAt": nowMs()}
			if err := put(q, "transport-conflict", c); err != nil {
				return nil, err
			}
		}
		if c["hostId"] != b["hostId"] {
			return nil, fail(403, "Recovery host mismatch")
		}
		if c["state"] == "blocked" || c["supported"] != true {
			return nil, fail(409, "Sequence conflict; inspect retained transport recovery")
		}
		mapped := doc{}
		for k, v := range value {
			mapped[k] = v
		}
		mapped["id"], mapped["seq"] = key+":transport-recovered", c["reservedSequence"]
		mapped["originalTransport"] = doc{"eventId": eid, "sequence": seq, "frameSha256": digest, "conflictId": c["id"]}
		olds, err := eventsWhere(q, doc{"id": mapped["id"]})
		if err != nil {
			return nil, err
		}
		taken, err := eventsWhere(q, doc{"executionId": xid, "seq": mapped["seq"]})
		if err != nil {
			return nil, err
		}
		bad := false
		for _, o := range olds {
			if o["id"] == mapped["id"] && !same(o, mapped) {
				bad = true
			}
		}
		for _, t := range taken {
			if t["id"] != mapped["id"] {
				bad = true
			}
		}
		if bad {
			return nil, fail(409, "Reserved recovery sequence conflict")
		}
		prepared = append(prepared, preparedEvent{value: mapped, conflict: c})
	}
	// Validate the entire batch before inserting; conflicting original events remain immutable.
	ids, seen := map[string]doc{}, map[string]string{}
	for _, p := range prepared {
		v := p.value
		k := astr(v["id"])
		if old, ok := ids[k]; ok && !same(old, v) {
			return nil, fail(409, "Batch event conflict")
		}
		ids[k] = v
		s := js(v["seq"])
		if old, ok := seen[s]; ok && old != k {
			return nil, fail(409, "Batch sequence conflict")
		}
		seen[s] = k
	}
	for _, p := range prepared {
		if err := put(q, eventKind(p.value), p.value); err != nil {
			return nil, err
		}
		if c := p.conflict; c != nil && c["state"] != "delivered" {
			c["state"], c["deliveredAt"] = "delivered", nowMs()
			if err := put(q, "transport-conflict", c); err != nil {
				return nil, err
			}
			if err := put(q, "transport-audit", doc{"id": uuid(), "executionId": xid, "conflictId": c["id"], "actor": "owning-host", "operation": "retained-transport-delivered", "at": nowMs(), "nativeEffectReplayed": false}); err != nil {
				return nil, err
			}
		}
	}
	if err := markObservation(q, xid, "confirmed"); err != nil {
		return nil, err
	}
	for _, p := range prepared {
		d := mapOf(p.value["data"])
		if v, ok := d["harnessVersion"].(string); ok {
			if err := setHarnessVersion(q, xid, v); err != nil {
				return nil, err
			}
		}
		if cs := anys(d["children"]); len(cs) > 0 {
			if err := recordChildren(q, xid, cs); err != nil {
				return nil, err
			}
		}
	}
	if ended, err := executionEnded(q, xid); err != nil || !ended {
		return doc{"ok": true}, err
	}
	return doc{"ok": true}, writeTotals(q, xid)
}

func transportInspect(q *Request, b doc) (any, error) {
	e, err := aget(q, b["executionId"], "execution")
	if err != nil {
		return nil, err
	}
	f := doc{"executionId": e["id"]}
	cs, err := all(q, "command", f)
	if err != nil {
		return nil, err
	}
	commands := []doc{}
	for _, c := range cs {
		commands = append(commands, doc{"id": c["id"], "kind": c["kind"], "status": c["status"]})
	}
	evs, err := eventsWhere(q, f)
	if err != nil {
		return nil, err
	}
	results, err := aArtifactsOf(q, astr(e["id"]))
	if err != nil {
		return nil, err
	}
	conflicts, err := all(q, "transport-conflict", f)
	if err != nil {
		return nil, err
	}
	for _, c := range conflicts {
		delete(c, "original")
	}
	audit, err := all(q, "transport-audit", f)
	if err != nil {
		return nil, err
	}
	return doc{"executionId": e["id"], "hostId": e["hostId"], "state": e["state"], "error": nonNil(e["error"]),
		"hostJournal": "Unknown to coordinator; delivery observation is not native reinspection", "nativeOutcome": "Not inferred from transport; inspect native evidence and validated receipts",
		"commands": commands, "eventCount": len(evs), "results": results, "conflicts": conflicts, "audit": audit,
		"resend": "Owning host automatically resends its unchanged retained outbox; no native dispatch or program-effect replay"}, nil
}

func transportAuthorize(q *Request, b doc) (any, error) {
	ks := []string{}
	for k := range b {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	rid, ok := b["requestId"].(string)
	if strings.Join(ks, ",") != "conflictId,executionId,frameSha256,requestId" || !ok || rid == "" || len([]rune(rid)) > 200 {
		return nil, fail(400, "Expected exact recovery identity")
	}
	xid, ok := b["executionId"].(string)
	if !ok {
		return nil, fail(400, "Expected exact recovery identity")
	}
	// Authorization rewrites the execution's transport records, which host ingest writes too: take its scope as well.
	if err := aExec(q, "SELECT pg_advisory_xact_lock(hashtext($1))", "execution:"+xid); err != nil {
		return nil, err
	}
	c, err := aget(q, b["conflictId"], "transport-conflict")
	if err != nil {
		return nil, err
	}
	if c["executionId"] != xid || c["frameSha256"] != b["frameSha256"] {
		return nil, fail(409, "Recovery identity mismatch")
	}
	if _, err := aget(q, c["executionId"], "execution"); err != nil {
		return nil, err
	}
	if c["supported"] != true {
		return nil, fail(409, "Unsupported conflict; inspect original host evidence, no replay")
	}
	olds, err := all(q, "transport-audit", doc{"requestId": rid})
	if err != nil {
		return nil, err
	}
	if len(olds) > 0 {
		if olds[0]["signature"] != frameHash(b) {
			return nil, fail(409, "Recovery request conflict")
		}
		return transportInspect(q, b)
	}
	if c["state"] == "blocked" {
		top := -1.0
		evs, err := eventsWhere(q, doc{"executionId": xid})
		if err != nil {
			return nil, err
		}
		for _, e := range evs {
			if s, ok := safeSeq(e["seq"]); ok && float64(s) > top {
				top = float64(s)
			}
		}
		others, err := all(q, "transport-conflict", doc{"executionId": xid})
		if err != nil {
			return nil, err
		}
		for _, o := range others {
			if r, ok := o["reservedSequence"].(float64); ok && r > top {
				top = r
			}
		}
		c["reservedSequence"], c["state"], c["authorizedAt"] = top+1, "authorized", nowMs()
		if err := put(q, "transport-conflict", c); err != nil {
			return nil, err
		}
	}
	if err := put(q, "transport-audit", doc{"id": uuid(), "executionId": c["executionId"], "conflictId": c["id"], "requestId": rid, "signature": frameHash(b),
		"actor": "operator-authority", "operation": "authorize-retained-transport", "at": nowMs(), "nativeEffectReplayed": false}); err != nil {
		return nil, err
	}
	return transportInspect(q, b)
}

// recoveryTools: the recovery operations as an MCP server over HTTP.
func recoveryTools(q *Request) (any, error) {
	m := q.obj()
	id := m["id"]
	result := func(r any) doc { return doc{"jsonrpc": "2.0", "id": id, "result": r} }
	switch m["method"] {
	case "initialize":
		return result(doc{"protocolVersion": "2025-06-18", "capabilities": doc{"tools": doc{}}, "serverInfo": doc{"name": "fleet-recovery", "version": "1"}}), nil
	case "tools/list":
		tools := []doc{}
		for _, n := range []string{"transport_inspect", "transport_authorize"} {
			tools = append(tools, doc{"name": n, "description": "Inspect/authorize retained transport; never rerun native work", "inputSchema": doc{"type": "object"}})
		}
		return result(doc{"tools": tools}), nil
	}
	p := mapOf(m["params"])
	args := mapOf(p["arguments"])
	if args == nil {
		args = doc{}
	}
	var out any
	var err error
	switch {
	case m["method"] != "tools/call":
		err = fail(400, "Unknown recovery tool")
	case p["name"] == "transport_inspect":
		out, err = transportInspect(q, args)
	case p["name"] == "transport_authorize":
		out, err = transportAuthorize(q, args)
	default:
		err = fail(400, "Unknown recovery tool")
	}
	if err != nil {
		return nil, err
	}
	return result(doc{"content": []doc{{"type": "text", "text": js(out)}}}), nil
}
