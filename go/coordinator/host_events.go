package main

// host events and transport recovery (SL7, SL11), and GET /api/executions/{id}/events. Events and stream deltas
// (kind event-delta) share one id and seq space per execution. A batch is validated whole, then stored in the execution's
// scope, in parallel with other executions. A seq taken by another event becomes a retained transport conflict that the
// operator may authorize; the owning host's resend then lands at a reserved seq.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	hostRoute(contract.RouteEvents, func(q *Request, b *contract.HostEvents) (contract.OkReply, error) { return q.app.ingest(q, b) })
	route("GET /api/executions/{id}/events", executionEvents)
	op("execution_events", func(q *Request, r *contract.ExecutionEventsRequest) (contract.ExecutionEventsReply, error) {
		es, err := executionEventRecords(q, r.ExecutionId)
		out := make([]map[string]json.RawMessage, 0, len(es))
		for _, e := range es {
			out = append(out, e)
		}
		return contract.ExecutionEventsReply{Events: out}, err
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
	es, err := executionEventRecords(q, q.r.PathValue("id"))
	return struct {
		Events []eventRecord `json:"events"`
	}{es}, err
}

// executionEventRecords: an execution's events in sequence order (not its deltas nor its ended marker).
func executionEventRecords(q *Request, id string) ([]eventRecord, error) {
	if _, err := eventsExecution(q, id); err != nil {
		return nil, err
	}
	es, err := q.db().EventsOfExecution(q.ctx(), &id)
	return eventRecordsOf(es), err
}

// executionEventsOf: executionEventRecords as the records the generic record API reads (lab_loop.go).
func executionEventsOf(q *Request, id string) ([]doc, error) {
	es, err := executionEventRecords(q, id)
	if err != nil {
		return nil, err
	}
	var out []doc
	jsonRoundTrip(es, &out)
	return out, nil
}

// eventsExecution: the execution's record remainder (data) and state; not found when it is no agent run or host job.
func eventsExecution(q *Request, id string) (rawObject, error) {
	e, err := q.db().EventsExecution(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	var d rawObject
	json.Unmarshal(e.Data, &d)
	if d == nil {
		d = rawObject{}
	}
	d["state"] = jsonBytes(e.State)
	return d, nil
}

// frameHash: sha256 of the canonical JSON of a value.
func frameHash(v any) string {
	h := sha256.Sum256([]byte(canonical(v)))
	return hex.EncodeToString(h[:])
}

// eventsWhere: the events of either kind matching a record filter, for callers on the generic record API
// (lab_retry.go, pipeline_plan.go).
func eventsWhere(q *Request, f any) ([]doc, error) { return recAll[doc](q, recEventAny, f) }

func eventKind(v eventRecord) string {
	if v.object("data").object("fleetNormalized").has("deltaOf") {
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

// transportConflict: a host event whose seq another stored event holds, retained for the operator (kind
// transport-conflict). reservedSequence: the seq transport_authorize reserves for the owning host's resend.
type transportConflict struct {
	ID               string          `json:"id"`
	ExecutionID      string          `json:"executionId"`
	HostID           string          `json:"hostId"`
	OriginalEventID  string          `json:"originalEventId"`
	OriginalSequence int64           `json:"originalSequence"`
	FrameSha256      string          `json:"frameSha256"`
	Original         json.RawMessage `json:"original"`
	CollisionEventID string          `json:"collisionEventId"`
	Supported        bool            `json:"supported"`
	State            string          `json:"state"`
	ObservedAt       int64           `json:"observedAt"`
	ReservedSequence *float64        `json:"reservedSequence,omitempty"`
	AuthorizedAt     *int64          `json:"authorizedAt,omitempty"`
	DeliveredAt      *int64          `json:"deliveredAt,omitempty"`
}

// transportAudit: one step of a retained transport conflict's recovery (kind transport-audit).
type transportAudit struct {
	ID                   string `json:"id"`
	ExecutionID          string `json:"executionId"`
	ConflictID           string `json:"conflictId"`
	RequestID            string `json:"requestId,omitempty"`
	Signature            string `json:"signature,omitempty"`
	Actor                string `json:"actor"`
	Operation            string `json:"operation"`
	At                   int64  `json:"at"`
	NativeEffectReplayed bool   `json:"nativeEffectReplayed"`
}

func transportConflictPut(q *Request, c *transportConflict) error {
	return q.db().EventsTransportConflictPut(q.ctx(), jsonBytes(c))
}

func transportAuditPut(q *Request, a transportAudit) error {
	return q.db().EventsTransportAuditPut(q.ctx(), jsonBytes(a))
}

// transportConflictOf: the stored conflict, nil when there is none.
func transportConflictOf(q *Request, id string) (*transportConflict, error) {
	b, err := q.db().EventsTransportConflict(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c transportConflict
	return &c, json.Unmarshal(b, &c)
}

type preparedEvent struct {
	value    eventRecord
	conflict *transportConflict
}

func (a *App) ingest(q *Request, batch *contract.HostEvents) (contract.OkReply, error) {
	var b rawObject
	json.Unmarshal(q.body, &b)
	id, ok := b.str("executionId")
	if !ok {
		return contract.OkReply{}, fail(400, "Expected executionId")
	}
	// A refused batch still commits the transport conflict it recorded (the batch's events are
	// written only after the whole batch is valid).
	var out contract.OkReply
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
	return out, err
}

func ingestIn(q *Request, b rawObject, batch *contract.HostEvents) (contract.OkReply, error) {
	var none contract.OkReply
	if err := hostKeys(q.body, reflect.TypeOf(contract.HostEvents{})); err != nil {
		return none, err
	}
	hostID, xid := b.text("hostId"), b.text("executionId")
	if err := ownedBody(q, hostID, xid); err != nil {
		return none, err
	}
	// batch: the type check of the batch (hostRoute decoded it); each event's data stays the host's raw record.
	var raws []json.RawMessage
	if json.Unmarshal(b["events"], &raws) != nil || raws == nil || len(batch.Events) > 1000 || len(raws) != len(batch.Events) {
		return none, fail(400, "Invalid events")
	}
	evs := make([]eventRecord, len(raws))
	for i, raw := range raws {
		json.Unmarshal(raw, &evs[i])
	}
	execution, err := eventsExecution(q, xid)
	if err != nil {
		return none, err
	}
	byID, bySeq, closedHashes, err := eventBatchSnapshot(q, xid, evs)
	if err != nil {
		return none, err
	}
	prepared := []preparedEvent{}
	for _, ev := range evs {
		// Identity from the stored object itself, never from the struct: encoding/json matches keys case-insensitively,
		// so "Type" and "type" could disagree between the two.
		eid, isStr := ev.str("id")
		seq, okSeq := ev.seq()
		if !isStr || eid == "" || len([]rune(eid)) > 300 || ev.text("type") == "execution.ended" || !okSeq {
			return none, fail(400, "Invalid event identity")
		}
		key := "event:" + xid + ":" + eid
		value := eventRecord{}
		for k, v := range ev {
			value[k] = v
		}
		value["id"], value["executionId"] = jsonBytes(key), jsonBytes(xid)
		delete(value, "sessionId")
		if execution.has("sessionId") {
			value["sessionId"] = execution["sessionId"]
		}
		for k := range value { // a stored event keeps no null field (its record strips them), so a retry compares equal
			if !value.has(k) {
				delete(value, k)
			}
		}
		if digest, closed := closedHashes[key]; closed {
			if digest != frameHash(value.value()) {
				return none, fail(409, "Event conflict")
			}
			continue
		}
		previous := byID[key]
		var collision eventRecord
		if at := bySeq[seq]; at != nil && at.text("id") != key {
			collision = at
		}
		if previous != nil && !same(previous, value) {
			return none, fail(409, "Event conflict")
		}
		if collision == nil {
			prepared = append(prepared, preparedEvent{value: value})
			continue
		}
		digest := frameHash(ev.value())
		cid := "transport:" + xid + ":" + digest
		c, err := transportConflictOf(q, cid)
		if err != nil {
			return none, err
		}
		if c == nil {
			c = &transportConflict{ID: cid, ExecutionID: xid, HostID: hostID, OriginalEventID: eid, OriginalSequence: seq, FrameSha256: digest, Original: jsonBytes(ev),
				CollisionEventID: collision.text("id"), Supported: previous == nil && ev.text("type") == string(contract.FleetEventTypeProcessClosed) && collision.text("type") == string(contract.FleetEventTypeResourceObservation),
				State: "blocked", ObservedAt: nowMs()}
			if err := transportConflictPut(q, c); err != nil {
				return none, err
			}
		}
		if c.HostID != hostID {
			return none, fail(403, "Recovery host mismatch")
		}
		if c.State == "blocked" || !c.Supported {
			return none, fail(409, "Sequence conflict; inspect retained transport recovery")
		}
		mapped := eventRecord{}
		for k, v := range value {
			mapped[k] = v
		}
		mappedID := key + ":transport-recovered"
		mapped["id"], mapped["seq"] = jsonBytes(mappedID), jsonBytes(c.ReservedSequence)
		mapped["originalTransport"] = jsonBytes(struct {
			EventID     string `json:"eventId"`
			Sequence    int64  `json:"sequence"`
			FrameSha256 string `json:"frameSha256"`
			ConflictID  string `json:"conflictId"`
		}{eid, seq, digest, c.ID})
		bad := false
		old, err := q.db().EventByID(q.ctx(), mappedID)
		if err == nil && !same(eventRecordOf(old), mapped) {
			bad = true
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return none, err
		}
		if r := c.ReservedSequence; r != nil && *r == math.Trunc(*r) {
			n := int64(*r)
			taken, err := q.db().EventsAtSeq(q.ctx(), db.EventsAtSeqParams{ExecutionID: &xid, Seq: &n})
			if err != nil {
				return none, err
			}
			for _, t := range taken {
				if t.ID != mappedID {
					bad = true
				}
			}
		}
		if bad {
			return none, fail(409, "Reserved recovery sequence conflict")
		}
		prepared = append(prepared, preparedEvent{value: mapped, conflict: c})
	}
	// Validate the entire batch before inserting; conflicting original events remain immutable.
	ids, seen := map[string]eventRecord{}, map[string]string{}
	for _, p := range prepared {
		v := p.value
		k := v.text("id")
		if old, ok := ids[k]; ok && !same(old, v) {
			return none, fail(409, "Batch event conflict")
		}
		ids[k] = v
		var sv any
		json.Unmarshal(v["seq"], &sv)
		s := js(sv)
		if old, ok := seen[s]; ok && old != k {
			return none, fail(409, "Batch sequence conflict")
		}
		seen[s] = k
	}
	if err := putEventBatch(q, prepared); err != nil {
		return none, err
	}
	for _, p := range prepared {
		if c := p.conflict; c != nil && c.State != "delivered" {
			at := nowMs()
			c.State, c.DeliveredAt = "delivered", &at
			if err := transportConflictPut(q, c); err != nil {
				return none, err
			}
			a := transportAudit{ID: newID(), ExecutionID: xid, ConflictID: c.ID, Actor: "owning-host", Operation: "retained-transport-delivered", At: nowMs()}
			if err := transportAuditPut(q, a); err != nil {
				return none, err
			}
		}
	}
	if err := cleanupClosedDeltas(q, xid); err != nil {
		return none, err
	}
	if err := markObservation(q, xid, "confirmed"); err != nil {
		return none, err
	}
	hasProcessClosed, hasConfirmedUsage := false, false
	for _, p := range prepared {
		typ := p.value.text("type")
		if typ == string(contract.FleetEventTypeProcessClosed) {
			hasProcessClosed = true
		}
		d := p.value.object("data")
		normalized := d.object("fleetNormalized")
		lifecycle := firstString(normalized.text("lifecycle"), typ)
		if normalized.has("nativeUsage") || lifecycle == "assistant" || lifecycle == "result" || lifecycle == "turn.completed" || lifecycle == "usage.observation" || d.text("type") == "assistant" {
			hasConfirmedUsage = true
		}
		if v, ok := d.str("harnessVersion"); ok {
			if err := setHarnessVersion(q, xid, v); err != nil {
				return none, err
			}
		}
		var cs []any
		json.Unmarshal(d["children"], &cs)
		if len(cs) > 0 {
			if err := recordChildren(q, xid, cs); err != nil {
				return none, err
			}
		}
		if problem := eventProblem(typ, d); problem != "" {
			// The first cause wins: a later process.closed does not hide the preparation failure that caused it.
			if err := q.db().EventsExecutionProblem(q.ctx(), db.EventsExecutionProblemParams{ID: xid, Problem: &problem}); err != nil {
				return none, err
			}
		}
	}
	ended, err := executionEnded(q, xid)
	if err != nil {
		return none, err
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
func eventProblem(kind string, data any) string {
	var d rawObject
	jsonRoundTrip(data, &d)
	switch contract.FleetEventType(kind) {
	case contract.FleetEventTypePreparationFailed, contract.FleetEventTypeReadinessFailed:
		return "preparation failed: " + firstString(d.text("reason"), "no reason given")
	case contract.FleetEventTypeProtocolFailed:
		return "harness protocol failed: " + firstString(d.text("reason"), firstString(d.text("error"), "no reason given"))
	case contract.FleetEventTypeProcessClosed:
		code, ok := jsonNumber(d["exitCode"])
		if !ok || code == 0 {
			return ""
		}
		p := fmt.Sprintf("harness exited %d", int(code))
		if t := strings.TrimSpace(d.text("stderrTail")); t != "" {
			lines := strings.Split(t, "\n")
			p += ": " + lines[len(lines)-1]
		}
		return p
	}
	return ""
}

func transportInspect(q *Request, executionID string) (contract.TransportInspectReply, error) {
	var rep contract.TransportInspectReply
	e, err := eventsExecution(q, executionID)
	if err != nil {
		return rep, err
	}
	xid := e.text("id")
	cs, err := q.db().CommandsOfExecution(q.ctx(), &xid)
	if err != nil {
		return rep, err
	}
	commands := []contract.CommandRef{}
	for _, c := range cs {
		commands = append(commands, contract.CommandRef{Id: c.ID, Kind: c.Kind, Status: string(c.Status)})
	}
	n, err := q.db().EventsCountOfExecution(q.ctx(), &xid)
	if err != nil {
		return rep, err
	}
	results, err := executionArtifacts(q, xid)
	if err != nil {
		return rep, err
	}
	cdata, err := q.db().EventsTransportConflicts(q.ctx(), xid)
	if err != nil {
		return rep, err
	}
	conflicts := transportRecords(cdata)
	for _, c := range conflicts {
		delete(c, "original")
	}
	adata, err := q.db().EventsTransportAudits(q.ctx(), db.EventsTransportAuditsParams{ExecutionID: &xid})
	if err != nil {
		return rep, err
	}
	errRaw := e["error"]
	if !e.has("error") {
		errRaw = json.RawMessage("null")
	}
	var hostID *string
	if h := e.text("hostId"); h != "" {
		hostID = &h
	}
	return contract.TransportInspectReply{ExecutionId: xid, HostId: hostID, State: e.text("state"), Error: errRaw,
		HostJournal: "Unknown to coordinator; delivery observation is not native reinspection", NativeOutcome: "Not inferred from transport; inspect native evidence and validated receipts",
		Commands: commands, EventCount: n, Results: recsOf(results), Conflicts: conflicts, Audit: transportRecords(adata),
		Resend: "Owning host automatically resends its unchanged retained outbox; no native dispatch or program-effect replay"}, nil
}

// transportRecords: stored transport records as the open records transport_inspect shows.
func transportRecords(data [][]byte) []map[string]json.RawMessage {
	out := make([]map[string]json.RawMessage, 0, len(data))
	for _, b := range data {
		var r map[string]json.RawMessage
		json.Unmarshal(b, &r)
		if r == nil {
			r = map[string]json.RawMessage{}
		}
		out = append(out, r)
	}
	return out
}

// transportAuthorize: the request is closed with four required keys; its signature is the hash of exactly those (frameHash).
func transportAuthorize(q *Request, r *contract.TransportAuthorizeRequest) (contract.TransportAuthorizeReply, error) {
	var rep contract.TransportAuthorizeReply
	signature := frameHash(norm(struct {
		ConflictID  string `json:"conflictId"`
		ExecutionID string `json:"executionId"`
		FrameSha256 string `json:"frameSha256"`
		RequestID   string `json:"requestId"`
	}{r.ConflictId, r.ExecutionId, r.FrameSha256, r.RequestId}))
	rid, xid := r.RequestId, r.ExecutionId
	if rid == "" || len([]rune(rid)) > 200 {
		return rep, fail(400, "Expected exact recovery identity")
	}
	// Authorization rewrites the execution's transport records, which host ingest writes too: take its scope as well.
	if err := acquireTxLock(q, "execution:"+xid); err != nil {
		return rep, err
	}
	c, err := transportConflictOf(q, r.ConflictId)
	if err != nil {
		return rep, err
	}
	if c == nil {
		return rep, errNotFound
	}
	if c.ExecutionID != xid || c.FrameSha256 != r.FrameSha256 {
		return rep, fail(409, "Recovery identity mismatch")
	}
	if _, err := eventsExecution(q, c.ExecutionID); err != nil {
		return rep, err
	}
	if !c.Supported {
		return rep, fail(409, "Unsupported conflict; inspect original host evidence, no replay")
	}
	olds, err := q.db().EventsTransportAudits(q.ctx(), db.EventsTransportAuditsParams{RequestID: &rid})
	if err != nil {
		return rep, err
	}
	if len(olds) > 0 {
		var old transportAudit
		json.Unmarshal(olds[0], &old)
		if old.Signature != signature {
			return rep, fail(409, "Recovery request conflict")
		}
		return transportInspect(q, xid)
	}
	if c.State == "blocked" {
		top := -1.0
		evs, err := q.db().EventsAllOfExecution(q.ctx(), &xid)
		if err != nil {
			return rep, err
		}
		for _, e := range evs {
			if s, ok := eventRecordOf(e).seq(); ok && float64(s) > top {
				top = float64(s)
			}
		}
		others, err := q.db().EventsTransportConflicts(q.ctx(), xid)
		if err != nil {
			return rep, err
		}
		for _, b := range others {
			var o transportConflict
			json.Unmarshal(b, &o)
			if o.ReservedSequence != nil && *o.ReservedSequence > top {
				top = *o.ReservedSequence
			}
		}
		reserved, at := top+1, nowMs()
		c.ReservedSequence, c.State, c.AuthorizedAt = &reserved, "authorized", &at
		if err := transportConflictPut(q, c); err != nil {
			return rep, err
		}
	}
	a := transportAudit{ID: newID(), ExecutionID: c.ExecutionID, ConflictID: c.ID, RequestID: rid, Signature: signature,
		Actor: "operator-authority", Operation: "authorize-retained-transport", At: nowMs()}
	if err := transportAuditPut(q, a); err != nil {
		return rep, err
	}
	return transportInspect(q, xid)
}
