package main

// host events and transport recovery (SL7, SL11), and GET /api/executions/{id}/events. Events and stream deltas
// (kind event-delta) share one id and seq space per execution. A batch is validated whole, then stored in the execution's
// scope, in parallel with other executions. A seq taken by another event becomes a retained transport conflict that the
// operator may authorize; the owning host's resend then lands at a reserved seq.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"reflect"
	"strings"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
)

func init() {
	hostRoute(contract.RouteEvents, func(q *Request, b *contract.HostEvents) (contract.OkReply, error) { return q.app.ingest(q, b) })
	route("GET /api/executions/{id}/events", executionEvents)
	op("execution_events", func(q *Request, r *contract.ExecutionEventsRequest) (contract.ExecutionEventsReply, error) {
		es, err := executionEventsOf(q, r.ExecutionId)
		return contract.ExecutionEventsReply{Events: recsOf(es)}, err
	})
	op("transport_inspect", func(q *Request, r *contract.TransportInspectRequest) (contract.TransportInspectReply, error) {
		return transportInspect(q, deref(r.ExecutionId))
	})
	op("transport_authorize", transportAuthorize)
	// The recovery MCP server: the two transport operations through runOp (their declared architect rule); the role is
	// checked first so initialize and tools/list are the architect's too.
	longRoute("POST /api/recovery-tools", func(q *Request) (any, error) {
		if err := requireArchitect(q); err != nil {
			return nil, err
		}
		return q.app.mcp(q, "fleet-recovery", map[string]registeredOp{"transport_inspect": operations["transport_inspect"], "transport_authorize": operations["transport_authorize"]})
	})
}

// requireArchitect: the architect role (or full access) for a route that is not an operation.
func requireArchitect(q *Request) error {
	if q.person != nil && !fullAccess(q.person) && !hasRole(q.person, "architect") {
		return fail(403, "Architect role required")
	}
	return nil
}

func executionEvents(q *Request) (any, error) {
	es, err := executionEventsOf(q, q.r.PathValue("id"))
	return doc{"events": es}, err
}

func executionEventsOf(q *Request, id string) ([]doc, error) {
	if _, err := get(q, id, "execution"); err != nil {
		return nil, err
	}
	return rows(q, "SELECT "+convTables["event"].record()+" FROM event WHERE NOT delta AND execution_id=$1 AND type IS DISTINCT FROM 'execution.ended' ORDER BY event_seq, seq", id)
}

// frameHash: sha256 of the canonical JSON of a value.
func frameHash(v any) string {
	h := sha256.Sum256([]byte(canonical(v)))
	return hex.EncodeToString(h[:])
}

func eventsWhere(q *Request, f doc) ([]doc, error) {
	return convAll(q, convEventAny, f)
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

func (a *App) ingest(q *Request, batch *contract.HostEvents) (contract.OkReply, error) {
	b := q.obj()
	id, ok := b["executionId"].(string)
	if !ok {
		return contract.OkReply{}, fail(400, "Expected executionId")
	}
	// A refused batch still commits the transport conflict it recorded (the batch's events are
	// written only after the whole batch is valid).
	var out any
	var refused error
	err := a.store.Tx(q.ctx(), nil, true, "execution:"+id, func(tx pgx.Tx) error {
		q.tx = tx
		var err error
		out, err = ingestIn(q, b, batch)
		var he *HTTPError
		if asHTTP(err, &he) {
			refused, err = err, nil
		}
		return err
	})
	if err == nil {
		err = refused
	}
	r, _ := out.(contract.OkReply)
	return r, err
}

func ingestIn(q *Request, b doc, batch *contract.HostEvents) (any, error) {
	if err := hostKeys(q.body, reflect.TypeOf(contract.HostEvents{})); err != nil {
		return nil, err
	}
	if err := ownedBody(q, astr(b["hostId"]), astr(b["executionId"])); err != nil {
		return nil, err
	}
	// batch: the type check of the batch (hostRoute decoded it); each event's data stays the host's raw record.
	evs, isList := b["events"].([]any)
	if !isList || len(batch.Events) > 1000 || len(evs) != len(batch.Events) {
		return nil, fail(400, "Invalid events")
	}
	xid := astr(b["executionId"])
	execution, err := get(q, xid, "execution")
	if err != nil {
		return nil, err
	}
	byID, bySeq, closedHashes, err := eventBatchSnapshot(q, xid, evs)
	if err != nil {
		return nil, err
	}
	prepared := []preparedEvent{}
	for _, raw := range evs {
		// Identity from the stored object itself, never from the struct: encoding/json matches keys case-insensitively,
		// so "Type" and "type" could disagree between the two.
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
		for k, v := range value { // a stored event keeps no null field (its record strips them), so a retry compares equal
			if v == nil {
				delete(value, k)
			}
		}
		if digest, closed := closedHashes[key]; closed {
			if digest != frameHash(value) {
				return nil, fail(409, "Event conflict")
			}
			continue
		}
		previous := byID[key]
		var collision doc
		if at := bySeq[seq]; at != nil && at["id"] != key {
			collision = at
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
				"collisionEventId": collision["id"], "supported": previous == nil && ev["type"] == string(contract.FleetEventTypeProcessClosed) && collision["type"] == string(contract.FleetEventTypeResourceObservation),
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
	if err := putEventBatch(q, prepared); err != nil {
		return nil, err
	}
	for _, p := range prepared {
		if c := p.conflict; c != nil && c["state"] != "delivered" {
			c["state"], c["deliveredAt"] = "delivered", nowMs()
			if err := put(q, "transport-conflict", c); err != nil {
				return nil, err
			}
			if err := put(q, "transport-audit", doc{"id": newID(), "executionId": xid, "conflictId": c["id"], "actor": "owning-host", "operation": "retained-transport-delivered", "at": nowMs(), "nativeEffectReplayed": false}); err != nil {
				return nil, err
			}
		}
	}
	if err := cleanupClosedDeltas(q, xid); err != nil {
		return nil, err
	}
	if err := markObservation(q, xid, "confirmed"); err != nil {
		return nil, err
	}
	hasProcessClosed, hasConfirmedUsage := false, false
	for _, p := range prepared {
		if astr(p.value["type"]) == string(contract.FleetEventTypeProcessClosed) {
			hasProcessClosed = true
		}
		d := mapOf(p.value["data"])
		normalized := mapOf(d["fleetNormalized"])
		lifecycle := firstString(normalized["lifecycle"], p.value["type"])
		if normalized["nativeUsage"] != nil || lifecycle == "assistant" || lifecycle == "result" || lifecycle == "turn.completed" || lifecycle == "usage.observation" || astr(d["type"]) == "assistant" {
			hasConfirmedUsage = true
		}
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
		if problem := eventProblem(astr(p.value["type"]), d); problem != "" {
			// The first cause wins: a later process.closed does not hide the preparation failure that caused it.
			if err := exec(q, "UPDATE execution SET problem=$2 WHERE id=$1 AND problem IS NULL", xid, problem); err != nil {
				return nil, err
			}
		}
	}
	ended, err := executionEnded(q, xid)
	if err != nil {
		return nil, err
	}
	if !ended && !hasProcessClosed {
		if hasConfirmedUsage {
			return contract.OkReply{Ok: true}, writeConfirmedUsage(q, xid)
		}
		return contract.OkReply{Ok: true}, nil
	}
	return contract.OkReply{Ok: true}, writeTotals(q, xid)
}

// eventProblem: the cause an agent failure records on its execution: a preparation that failed, a protocol failure,
// or a harness that exited non-zero (with the last line of its stderr). Empty for every other event.
func eventProblem(kind string, d doc) string {
	switch contract.FleetEventType(kind) {
	case contract.FleetEventTypePreparationFailed, contract.FleetEventTypeReadinessFailed:
		return "preparation failed: " + firstString(d["reason"], "no reason given")
	case contract.FleetEventTypeProtocolFailed:
		return "harness protocol failed: " + firstString(d["reason"], firstString(d["error"], "no reason given"))
	case contract.FleetEventTypeProcessClosed:
		code, ok := d["exitCode"].(float64)
		if !ok || code == 0 {
			return ""
		}
		p := fmt.Sprintf("harness exited %d", int(code))
		if t := strings.TrimSpace(astr(d["stderrTail"])); t != "" {
			lines := strings.Split(t, "\n")
			p += ": " + lines[len(lines)-1]
		}
		return p
	}
	return ""
}

func transportInspect(q *Request, executionID string) (contract.TransportInspectReply, error) {
	var rep contract.TransportInspectReply
	e, err := get(q, executionID, "execution")
	if err != nil {
		return rep, err
	}
	f := doc{"executionId": e["id"]}
	cs, err := all(q, "command", f)
	if err != nil {
		return rep, err
	}
	commands := []contract.CommandRef{}
	for _, c := range cs {
		commands = append(commands, contract.CommandRef{Id: astr(c["id"]), Kind: astr(c["kind"]), Status: astr(c["status"])})
	}
	evs, err := eventsWhere(q, f)
	if err != nil {
		return rep, err
	}
	results, err := executionArtifacts(q, astr(e["id"]))
	if err != nil {
		return rep, err
	}
	conflicts, err := all(q, "transport-conflict", f)
	if err != nil {
		return rep, err
	}
	for _, c := range conflicts {
		delete(c, "original")
	}
	audit, err := all(q, "transport-audit", f)
	if err != nil {
		return rep, err
	}
	return contract.TransportInspectReply{ExecutionId: astr(e["id"]), HostId: strPtr(e["hostId"]), State: astr(e["state"]), Error: jsonBytes(nonNil(e["error"])),
		HostJournal: "Unknown to coordinator; delivery observation is not native reinspection", NativeOutcome: "Not inferred from transport; inspect native evidence and validated receipts",
		Commands: commands, EventCount: int64(len(evs)), Results: recsOf(results), Conflicts: recsOf(conflicts), Audit: recsOf(audit),
		Resend: "Owning host automatically resends its unchanged retained outbox; no native dispatch or program-effect replay"}, nil
}

// transportAuthorize: the request is closed with four required keys; its signature is the hash of exactly those (frameHash).
func transportAuthorize(q *Request, r *contract.TransportAuthorizeRequest) (contract.TransportAuthorizeReply, error) {
	var rep contract.TransportAuthorizeReply
	b := doc{"conflictId": r.ConflictId, "executionId": r.ExecutionId, "frameSha256": r.FrameSha256, "requestId": r.RequestId}
	rid, xid := r.RequestId, r.ExecutionId
	if rid == "" || len([]rune(rid)) > 200 {
		return rep, fail(400, "Expected exact recovery identity")
	}
	// Authorization rewrites the execution's transport records, which host ingest writes too: take its scope as well.
	if err := acquireTxLock(q, "execution:"+xid); err != nil {
		return rep, err
	}
	c, err := get(q, b["conflictId"], "transport-conflict")
	if err != nil {
		return rep, err
	}
	if c["executionId"] != xid || c["frameSha256"] != b["frameSha256"] {
		return rep, fail(409, "Recovery identity mismatch")
	}
	if _, err := get(q, c["executionId"], "execution"); err != nil {
		return rep, err
	}
	if c["supported"] != true {
		return rep, fail(409, "Unsupported conflict; inspect original host evidence, no replay")
	}
	olds, err := all(q, "transport-audit", doc{"requestId": rid})
	if err != nil {
		return rep, err
	}
	if len(olds) > 0 {
		if olds[0]["signature"] != frameHash(b) {
			return rep, fail(409, "Recovery request conflict")
		}
		return transportInspect(q, xid)
	}
	if c["state"] == "blocked" {
		top := -1.0
		evs, err := eventsWhere(q, doc{"executionId": xid})
		if err != nil {
			return rep, err
		}
		for _, e := range evs {
			if s, ok := safeSeq(e["seq"]); ok && float64(s) > top {
				top = float64(s)
			}
		}
		others, err := all(q, "transport-conflict", doc{"executionId": xid})
		if err != nil {
			return rep, err
		}
		for _, o := range others {
			if r, ok := o["reservedSequence"].(float64); ok && r > top {
				top = r
			}
		}
		c["reservedSequence"], c["state"], c["authorizedAt"] = top+1, "authorized", nowMs()
		if err := put(q, "transport-conflict", c); err != nil {
			return rep, err
		}
	}
	if err := put(q, "transport-audit", doc{"id": newID(), "executionId": c["executionId"], "conflictId": c["id"], "requestId": rid, "signature": frameHash(b),
		"actor": "operator-authority", "operation": "authorize-retained-transport", "at": nowMs(), "nativeEffectReplayed": false}); err != nil {
		return rep, err
	}
	return transportInspect(q, xid)
}
