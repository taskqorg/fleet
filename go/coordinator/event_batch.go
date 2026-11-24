package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"math"

	"fleet/coordinator/db"
)

// rawObject: an open JSON object (a host's event, its data, a profile) read field by field with its exact keys.
type rawObject map[string]json.RawMessage

// eventRecord: an event as its record, the host's open object: payload with the typed columns under their record
// names (id, executionId, sessionId, projectId, seq, type, timestamp).
type eventRecord = rawObject

// eventRecordOf: a stored event as its record. A NULL column is absent, a column overrides a payload key.
func eventRecordOf(e db.Event) eventRecord {
	r := eventRecord{}
	json.Unmarshal(e.Payload, &r)
	set := func(k string, v any) { r[k] = jsonBytes(v) }
	set("id", e.ID)
	for k, v := range map[string]*string{"executionId": e.ExecutionID, "sessionId": e.SessionID, "projectId": e.ProjectID, "type": e.Type} {
		if v != nil {
			set(k, *v)
		}
	}
	if e.EventSeq != nil {
		set("seq", *e.EventSeq)
	}
	if e.At != nil {
		set("timestamp", int64(math.Round(float64(e.At.UnixMicro())/1000)))
	}
	return r
}

func eventRecordsOf(es []db.Event) []eventRecord {
	out := make([]eventRecord, 0, len(es))
	for _, e := range es {
		out = append(out, eventRecordOf(e))
	}
	return out
}

// Project ownership is coordinator metadata, outside the host's transport identity.
func eventFrameOf(e db.Event) eventRecord {
	r := eventRecordOf(e)
	delete(r, "projectId")
	return r
}

func matchesClosedEvent(digest string, value, incoming eventRecord) bool {
	if digest == frameHash(value.value()) {
		return true
	}
	// Older compact hashes could include a host-supplied project. Accept the
	// exact legacy frame without using that field for ownership.
	if !incoming.has("projectId") {
		return false
	}
	legacy := maps.Clone(value)
	legacy["projectId"] = incoming["projectId"]
	return digest == frameHash(legacy.value())
}

// str: field k as a string, false when absent, null or not a string.
func (r rawObject) str(k string) (string, bool) {
	var s *string
	if json.Unmarshal(r[k], &s) != nil || s == nil {
		return "", false
	}
	return *s, true
}

// text: field k as a string, "" when absent or not a string.
func (r rawObject) text(k string) string { s, _ := r.str(k); return s }

// ptr: field k as a string, nil when absent, null or not a string.
func (r rawObject) ptr(k string) *string {
	if s, ok := r.str(k); ok {
		return &s
	}
	return nil
}

// has: field k is present and not null (a decoded map's value is not nil).
func (r rawObject) has(k string) bool { return len(r[k]) > 0 && !bytes.Equal(r[k], []byte("null")) }

// object: field k as an object, nil when it is not one.
func (r rawObject) object(k string) rawObject {
	var o rawObject
	json.Unmarshal(r[k], &o)
	return o
}

// seq: the record's sequence when it is a safe integer.
func (r rawObject) seq() (int64, bool) {
	var f any
	if json.Unmarshal(r["seq"], &f) != nil {
		return 0, false
	}
	return safeSeq(f)
}

// value: the record as a decoded JSON value, the form frameHash and same compare.
func (r rawObject) value() any { return norm(r) }

// eventColumns: a record split into the event table's columns, as a record write splits it: a field whose value has its column's
// type is the column, any other field (or a field of the wrong type) stays in payload.
type eventColumns struct {
	ID          string          `json:"id"`
	Delta       bool            `json:"delta"`
	ExecutionID *string         `json:"executionId"`
	SessionID   *string         `json:"sessionId"`
	ProjectID   *string         `json:"projectId"`
	Seq         *int64          `json:"seq"`
	Type        *string         `json:"type"`
	At          *float64        `json:"at"`
	Payload     json.RawMessage `json:"payload"`
}

func eventColumnsOf(r eventRecord) eventColumns {
	rest := eventRecord{}
	for k, v := range r {
		if k != "id" && r.has(k) {
			rest[k] = v
		}
	}
	row := eventColumns{ID: r.text("id"), Delta: eventKind(r) == "event-delta"}
	text := func(k string) *string {
		s, ok := r.str(k)
		if !ok {
			return nil
		}
		delete(rest, k)
		return &s
	}
	row.ExecutionID, row.SessionID, row.ProjectID, row.Type = text("executionId"), text("sessionId"), text("projectId"), text("type")
	if n, ok := jsonNumber(r["seq"]); ok && n == math.Trunc(n) {
		s := int64(n)
		row.Seq = &s
		delete(rest, "seq")
	}
	if at, ok := jsonNumber(r["timestamp"]); ok {
		row.At = &at
		delete(rest, "timestamp")
	}
	row.Payload = jsonBytes(rest)
	return row
}

// jsonNumber: raw as a number, false for anything else (null included).
func jsonNumber(raw json.RawMessage) (float64, bool) {
	var n *float64
	if json.Unmarshal(raw, &n) != nil || n == nil {
		return 0, false
	}
	return *n, true
}

// Event chunks are durable until a full closing block commits. Afterwards only
// immutable transport identities remain, so retries cannot resurrect chunks.
func eventBatchSnapshot(q *Request, executionID string, evs []eventRecord) (map[string]eventRecord, map[int64]eventRecord, map[string]string, error) {
	ids := make([]string, 0, len(evs))
	seqs := make([]int64, 0, len(evs))
	for _, ev := range evs {
		seq, ok := ev.seq()
		if !ok {
			continue
		}
		ids = append(ids, "event:"+executionID+":"+ev.text("id"))
		seqs = append(seqs, seq)
	}
	existing, err := q.db().EventsByIdentity(q.ctx(), db.EventsByIdentityParams{ExecutionID: &executionID, Seqs: seqs, Ids: ids})
	if err != nil {
		return nil, nil, nil, err
	}
	byID, bySeq := map[string]eventRecord{}, map[int64]eventRecord{}
	for _, e := range existing {
		r := eventFrameOf(e)
		byID[e.ID] = r
		var seq int64
		if e.EventSeq != nil {
			seq = *e.EventSeq
		}
		bySeq[seq] = r
	}
	compact, err := q.db().EventsClosedByIdentity(q.ctx(), db.EventsClosedByIdentityParams{ExecutionID: executionID, Seqs: seqs, Ids: ids})
	if err != nil {
		return nil, nil, nil, err
	}
	hashes := map[string]string{}
	for _, v := range compact {
		hashes[v.ID] = v.Digest
		bySeq[v.EventSeq] = eventRecord{"id": jsonBytes(v.ID), "type": jsonBytes(v.EventType)}
	}
	return byID, bySeq, hashes, nil
}

func cleanupClosedDeltas(q *Request, executionID string) error {
	// The complete closing event is authoritative. Hash the full normalized chunk
	// before removing its text; hashes preserve exact retry/conflict semantics.
	chunks, err := q.db().EventsClosedDeltas(q.ctx(), &executionID)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return nil
	}
	ids, seqs, hashes, types := []string{}, []int64{}, []string{}, []string{}
	for _, e := range chunks {
		ids = append(ids, e.ID)
		var seq int64
		if e.EventSeq != nil {
			seq = *e.EventSeq
		}
		seqs = append(seqs, seq)
		hashes = append(hashes, frameHash(eventFrameOf(e).value()))
		types = append(types, deref(e.Type))
	}
	if err := q.db().EventsClosedIdentityInsert(q.ctx(), db.EventsClosedIdentityInsertParams{Ids: ids, ExecutionID: executionID, Seqs: seqs, Digests: hashes, Types: types}); err != nil {
		return err
	}
	return q.db().EventsDeltasDelete(q.ctx(), ids)
}

// putEventBatch inserts a host's new events in one statement (an event already stored stays as it is).
func putEventBatch(q *Request, prepared []preparedEvent, projectID *string) error {
	rows := []eventColumns{}
	seen := map[string]bool{}
	for _, p := range prepared {
		row := eventColumnsOf(p.value)
		if seen[row.ID] {
			continue
		}
		seen[row.ID] = true
		row.ProjectID = projectID
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil
	}
	return q.db().EventsInsert(q.ctx(), jsonBytes(rows))
}
