package main

// Event chunks are durable until a full closing block commits. Afterwards only
// immutable transport identities remain, so retries cannot resurrect chunks.
func eventBatchSnapshot(q *Request, executionID string, evs []any) (map[string]doc, map[int64]doc, map[string]string, error) {
	ids := make([]string, 0, len(evs))
	seqs := make([]int64, 0, len(evs))
	for _, raw := range evs {
		ev := mapOf(raw)
		seq, ok := safeSeq(ev["seq"])
		if !ok {
			continue
		}
		ids = append(ids, "event:"+executionID+":"+astr(ev["id"]))
		seqs = append(seqs, seq)
	}
	existing, err := rows(q, "SELECT "+convTables["event"].record()+" FROM event WHERE execution_id=$1 AND (event_seq=ANY($2) OR id=ANY($3))", executionID, seqs, ids)
	if err != nil {
		return nil, nil, nil, err
	}
	byID, bySeq := map[string]doc{}, map[int64]doc{}
	for _, v := range existing {
		byID[astr(v["id"])] = v
		bySeq[int64(num(v["seq"]))] = v
	}
	compact, err := rowsOf(q, "SELECT id,event_seq,digest,event_type FROM closed_delta_identity WHERE execution_id=$1 AND (event_seq=ANY($2) OR id=ANY($3))", executionID, seqs, ids)
	if err != nil {
		return nil, nil, nil, err
	}
	hashes := map[string]string{}
	for _, v := range compact {
		id := astr(v["id"])
		hashes[id] = astr(v["digest"])
		bySeq[int64(num(v["event_seq"]))] = doc{"id": id, "type": v["event_type"]}
	}
	return byID, bySeq, hashes, nil
}

func cleanupClosedDeltas(q *Request, executionID string) error {
	// The complete closing event is authoritative. Hash the full normalized chunk
	// before removing its text; hashes preserve exact retry/conflict semantics.
	chunks, err := rows(q, "SELECT "+convTables["event-delta"].record()+` FROM event d WHERE d.delta AND d.execution_id=$1 AND EXISTS(SELECT 1 FROM event c WHERE NOT c.delta AND c.execution_id=$1 AND c.payload->'data'->'fleetNormalized'->'closes' ? (d.payload->'data'->'fleetNormalized'->>'deltaOf'))`, executionID)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return nil
	}
	ids, seqs, hashes, types := []string{}, []int64{}, []string{}, []string{}
	for _, v := range chunks {
		ids = append(ids, astr(v["id"]))
		seqs = append(seqs, int64(num(v["seq"])))
		hashes = append(hashes, frameHash(v))
		types = append(types, astr(v["type"]))
	}
	if err := exec(q, `INSERT INTO closed_delta_identity(id,execution_id,event_seq,digest,event_type) SELECT i,$1,s,h,t FROM unnest($2::text[],$3::bigint[],$4::text[],$5::text[]) AS x(i,s,h,t) ON CONFLICT(id) DO NOTHING`, executionID, ids, seqs, hashes, types); err != nil {
		return err
	}
	return exec(q, "DELETE FROM event WHERE delta AND id=ANY($1)", ids)
}

// putEventBatch inserts a host's new events in one statement (an event already stored stays as it is).
func putEventBatch(q *Request, prepared []preparedEvent) error {
	var ids, executions, sessions, projects, types []*string
	var deltas []bool
	var seqs []*int64
	var ats []*float64
	var payloads []string
	seen := map[string]bool{}
	t := convTables["event"]
	for _, p := range prepared {
		v := p.value
		id := astr(v["id"])
		if seen[id] {
			continue
		}
		seen[id] = true
		rest := doc{}
		for k, x := range v {
			if _, found := t.col(k); !found && k != "id" {
				rest[k] = x
			}
		}
		text := func(field string) *string {
			c, _ := t.col(field)
			if x, ok := c.value(v[field]); ok {
				s := x.(string)
				return &s
			} else if v[field] != nil {
				rest[field] = v[field]
			}
			return nil
		}
		c, _ := t.col("seq")
		var seq *int64
		if x, ok := c.value(v["seq"]); ok {
			n := x.(int64)
			seq = &n
		} else if v["seq"] != nil {
			rest["seq"] = v["seq"]
		}
		c, _ = t.col("timestamp")
		var at *float64
		if x, ok := c.value(v["timestamp"]); ok {
			f := x.(float64)
			at = &f
		} else if v["timestamp"] != nil {
			rest["timestamp"] = v["timestamp"]
		}
		ids = append(ids, &id)
		deltas = append(deltas, eventKind(v) == "event-delta")
		executions, sessions, projects, types = append(executions, text("executionId")), append(sessions, text("sessionId")), append(projects, text("projectId")), append(types, text("type"))
		seqs, ats = append(seqs, seq), append(ats, at)
		payloads = append(payloads, js(rest))
	}
	if len(ids) == 0 {
		return nil
	}
	return exec(q, `INSERT INTO event(id,delta,execution_id,session_id,project_id,event_seq,type,at,payload)
	 SELECT i,d,e,s,p,n,t,to_timestamp(a/1000),pl::jsonb FROM unnest($1::text[],$2::boolean[],$3::text[],$4::text[],$5::text[],$6::bigint[],$7::text[],$8::float8[],$9::text[]) AS x(i,d,e,s,p,n,t,a,pl)
	 ON CONFLICT(id) DO NOTHING`, ids, deltas, executions, sessions, projects, seqs, types, ats, payloads)
}
