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
	existing, err := rows(q, `SELECT data FROM document WHERE kind IN ('event','event-delta') AND data->>'executionId'=$1 AND ((data->>'seq')::bigint=ANY($2) OR id=ANY($3))`, executionID, seqs, ids)
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
	chunks, err := rows(q, `SELECT d.data FROM document d WHERE d.kind='event-delta' AND d.data->>'executionId'=$1 AND EXISTS(SELECT 1 FROM document c WHERE c.kind='event' AND c.data->>'executionId'=$1 AND c.data->'data'->'fleetNormalized'->'closes' ? (d.data->'data'->'fleetNormalized'->>'deltaOf'))`, executionID)
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
	return exec(q, "DELETE FROM document WHERE kind='event-delta' AND id=ANY($1)", ids)
}

func putEventBatch(q *Request, prepared []preparedEvent) error {
	kinds, ids, payloads, projects := []string{}, []string{}, []string{}, []*string{}
	seen := map[string]bool{}
	for _, p := range prepared {
		v := p.value
		id := astr(v["id"])
		if seen[id] {
			continue
		}
		seen[id] = true
		kinds = append(kinds, eventKind(v))
		ids = append(ids, id)
		payloads = append(payloads, js(v))
		projects = append(projects, strPtr(v["projectId"]))
	}
	if len(ids) == 0 {
		return nil
	}
	return exec(q, `INSERT INTO document(kind,id,data,project_id) SELECT k,i,d::jsonb,p FROM unnest($1::text[],$2::text[],$3::text[],$4::text[]) AS x(k,i,d,p) ON CONFLICT(id) DO NOTHING`, kinds, ids, payloads, projects)
}
